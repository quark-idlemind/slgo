// Package client attaches to a server holding grid connections.
//
// It deliberately holds no grid state: the circuit, its sequence
// numbers and the capability URLs stay on the far end, which is the
// whole point.  A client may be stopped, rebuilt and restarted as often
// as you like without the grid noticing, which is what makes it the
// place to put the code you are actually working on.
//
// # Writing a program against this directly
//
// Most programs should use sl instead, which turns these messages into
// objects, inventory and chat, and waits for the confirmations this
// protocol mostly does not send.  Reach for this package when what you
// want IS the messages -- watching the wire, or handling something sl
// does not model.
//
// The whole of it is: dial, attach, and read.  Attaching names the
// session to use -- empty takes the daemon's default -- and the
// messages to relay, by template name; "*" is everything, and naming
// nothing relays nothing until Subscribe says otherwise, because a busy
// region will otherwise flood the link.
//
//	conn, err := client.Dial(ctx, "localhost:7807")
//	if err != nil {
//		return err
//	}
//	defer conn.Close()
//
//	if _, err := conn.Attach(ctx, "", "ChatFromSimulator"); err != nil {
//		return err
//	}
//
//	for m := range conn.Messages() {
//		v, err := m.Decode()
//		if err != nil || v == nil {
//			continue // not in this build's template, and that is fine
//		}
//		chat := v.(*msg.ChatFromSimulator)
//		fmt.Println(string(chat.ChatData.Message))
//	}
//
// A message this build has never heard of still arrives, with its
// number and its undecoded bytes: Decode returns nil for it rather than
// an error, and the body is there to look at.  That is deliberate --
// the server relays what it does not understand to a client subscribed
// to "*", so a client can handle a message added to the protocol after
// the server was built.
//
// Sending goes the other way, and fills its own identity blocks: the
// server relays bytes and does not fill anything in.
//
//	m := &msg.ChatFromViewer{}
//	m.AgentData.AgentID, m.AgentData.SessionID = agentID, sessionID
//	m.ChatData.Message = append([]byte("hello"), 0)
//	err = conn.Send(ctx, m, true)
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"github.com/quark-idlemind/slgo/internal/auth"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
	"os"
	"path/filepath"
)

// Conn is a connection to a server.
type Conn struct {
	cc   *grpc.ClientConn
	grid pb.GridClient

	agent string

	// perAgent is how many places per avatar this client can wear, sent
	// with every ask for places; zero says nothing, which a daemon reads
	// as twelve.  Set by SetSlotsPerAgent.
	perAgent atomic.Uint32

	// dropped counts what this connection threw away because nobody was
	// reading fast enough, and OnDrop says what each one was, so that a
	// line lost here can be told from a line never said.
	// Why: doc/client.md#counting-what-is-dropped
	dropped atomic.Uint64
	onDrop  func(what string)

	mu   sync.RWMutex
	info *pb.AgentInfo
	caps map[string]bool

	// moves counts the region changes heard on the stream -- a teleport,
	// a crossing, a session re-established -- and capsAt is the count
	// info and caps were read at.  While they differ the two describe a
	// session or a region the avatar has left, and HasCap and Caps ask
	// again first.  refreshMu keeps that to one ask at a time.
	moves, capsAt uint64
	refreshMu     sync.Mutex

	stream   pb.Grid_StreamClient
	messages chan *Message
	events   chan *Event
	notices  chan *pb.AgentEvent

	// regions is the region changes among the notices, on a channel of
	// their own rather than sifted out of that one.
	//
	// A notice stream has one reader, and two things want these: a
	// program showing a person what happened to their session, and the
	// bookkeeping that has to throw away everything keyed on the region
	// left behind.  Sifting would make the second steal from the first.
	regions chan *RegionChange

	// handled is the offers that somebody -- this client or another --
	// has dealt with, on a channel of their own for the reason regions
	// is: the notice stream has one reader already.
	handled chan *pb.OfferHandled

	// offers is what the server said was still waiting when this
	// client attached, and nil when it said nothing -- a server too old
	// to keep a record, or an attach that did not ask for instant
	// messages.
	offers *pb.OfferRecord

	// relaying says recvLoop is running, and closed says Close has
	// been. Both are under mu, and the pair is what decides who closes
	// the five channels above; see closeRelay.
	relaying bool
	closed   bool

	// sendMu makes sends on stream one at a time.  See sendPacket.
	sendMu sync.Mutex

	// locks is the requests for a lock still waiting for slgod's
	// answer.  See lock.go.
	locks locking

	// grants is the requests for shared objects still waiting for
	// slgod's answer, and those given up on.  See slots.go.
	grants granting

	closeOnce sync.Once
	done      chan struct{}
	err       atomic.Value

	// relayOnce guards the close of the five relay channels, and
	// relayDone is closed with them so that Close can wait.
	relayOnce sync.Once
	relayDone chan struct{}
}

// Message is a grid message relayed by the server.
type Message struct {
	ID       msg.ID
	Name     string
	Sequence uint32
	Flags    uint32
	Body     []byte
	At       time.Time
	// FromClient names the client that sent this, when it came from
	// another client of the same session rather than from the grid.
	// Empty is the grid, which is everything that arrived over the
	// circuit.  See slgo.proto: the grid does not echo what an avatar
	// says, so this is the only way two clients on one session can see
	// the whole of a conversation.
	FromClient string

	// Offer is the server's name for an offer it is keeping, when this
	// message is one, and empty for everything else.  It is what
	// Conn.Handled takes.
	Offer string

	// Recorded says this did not just arrive: it came out of the
	// server's record of offers still waiting, put back after a client
	// said it would answer and then could not.  At is still when it
	// arrived at the server.  What was waiting at attach is not relayed
	// this way at all; see Conn.Offers.
	Recorded bool
}

// Decode turns the relayed bytes into a typed message.  A nil result
// with a nil error means the number is not in this build's template --
// the server relayed it anyway, and Body is still there.
func (m *Message) Decode() (msg.Message, error) {
	v := msg.New(m.ID)
	if v == nil {
		return nil, nil
	}
	if err := v.Decode(m.Body); err != nil {
		return nil, err
	}
	return v, nil
}

// Reliable reports whether the simulator wanted this acknowledged.  The
// server has already done it.
func (m *Message) Reliable() bool { return m.Flags&msg.FlagReliable != 0 }

// Event is one entry from the grid's event queue.
type Event struct {
	Name string
	Body []byte // LLSD encoded
	At   time.Time
}

// RegionChange is the avatar being somewhere else: the name and handle
// of the region it is in now.
//
// It means more than it says.  Local ids are the region's own numbering
// and are reused by the next one, an object cache describes a place the
// avatar has left, and anything keyed on either is now a claim about
// somewhere else.  So this is the news that what a client holds should
// be dropped, and the name and handle are here so that it can be
// dropped without a round trip asking where we are.
//
// A change with no name is still a change.  A server too old to fill
// the fields in sends the kind alone, and the kind is what says the
// avatar is somewhere else; the name is what saves asking where.
type RegionChange struct {
	Region string
	Handle uint64

	// TeleportFlags are the flags of the TeleportStart that began the
	// teleport, in the viewer's TELEPORT_FLAGS_* bits: zero for a
	// change that was not a teleport, and for a server that does not
	// say.  Cause puts the likeliest in a word.
	TeleportFlags uint32
}

// Cause says in a word what moved the avatar, from the first of these
// its TeleportFlags carry: "home", "lure" (somebody's offer), "landmark",
// "location" (a teleport to a map position or a region), "god" and
// "forced" (ForceRedirect, which a parcel's owner sends an avatar off
// it with).  It is empty when there are none, or when the flags say
// something else -- a telehub, a login, a region named by its id.
func (c *RegionChange) Cause() string {
	f := c.TeleportFlags
	switch {
	case f&agent.TeleportViaHome != 0:
		return "home"
	case f&(agent.TeleportViaLure) != 0:
		return "lure"
	case f&agent.TeleportViaLandmark != 0:
		return "landmark"
	case f&agent.TeleportViaLocation != 0:
		return "location"
	case f&(agent.TeleportViaGodlikeLure|agent.TeleportGodlike) != 0:
		return "god"
	case f&agent.TeleportForceRedirect != 0:
		return "forced"
	}
	return ""
}

// Decode parses the event body.
func (e *Event) Decode() (map[string]any, error) {
	v, err := llsd.Decode(bytes.NewReader(e.Body))
	if err != nil {
		return nil, err
	}
	return llsd.Map(v), nil
}

// Dial connects to slgod over TLS and authenticates both ways.  It opens
// no stream: Attach starts the messages.
//
// One connection, used for the handshake and everything after, because
// the connection is what the handshake proves. The certificate is not
// checked and is not meant to be: it proves nothing, and what proves the
// server is its half of the exchange, tied to this TLS session.
//
// Passing explicit dial options skips all of it, which is for tests that
// bring up a server in the same process.
//
// A channel that sat idle, or whose transport was lost, comes back on a
// new transport that has not logged in; the calls on this connection
// log in again by themselves.  Streams are not retried.
// Why: doc/client.md#a-connection-that-comes-back
func Dial(ctx context.Context, addr string, opts ...grpc.DialOption) (*Conn, error) {
	return dial(ctx, addr, len(opts) == 0, opts...)
}

// dial is Dial with the choice made by the caller: authenticate takes
// the real handshake, and extra then adds to the options it builds
// rather than replacing them, which is how a test shortens the idle
// timeout and still logs in.
func dial(ctx context.Context, addr string, authenticate bool, opts ...grpc.DialOption) (*Conn, error) {
	var re *relogin
	if authenticate {
		creds, binding := auth.ClientTLS()
		re = &relogin{addr: addr, binding: binding}
		opts = append([]grpc.DialOption{
			grpc.WithTransportCredentials(creds),
			grpc.WithChainUnaryInterceptor(re.intercept),
		}, opts...)
	}
	// Ping an idle connection, so that a server or a network that has
	// gone away is noticed in seconds rather than whenever TCP says
	// so.  It matters most for locks: slgod frees what this client
	// holds when the stream ends, and this is what ends it.
	opts = append(opts, grpc.WithKeepaliveParams(keepalive.ClientParameters{
		Time:                20 * time.Second,
		Timeout:             10 * time.Second,
		PermitWithoutStream: true,
	}))
	cc, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, err
	}
	if authenticate {
		re.cc = cc
		if err := login(ctx, cc, addr, re.binding); err != nil {
			cc.Close()
			return nil, err
		}
	}
	return &Conn{
		cc:        cc,
		grid:      pb.NewGridClient(cc),
		caps:      map[string]bool{},
		messages:  make(chan *Message, 1024),
		events:    make(chan *Event, 256),
		notices:   make(chan *pb.AgentEvent, 32),
		regions:   make(chan *RegionChange, 32),
		handled:   make(chan *pb.OfferHandled, 64),
		done:      make(chan struct{}),
		relayDone: make(chan struct{}),
	}, nil
}

// relogin is what a connection keeps to log in again on a new transport.
type relogin struct {
	addr    string
	binding func() ([]byte, error)
	cc      *grpc.ClientConn

	// gen counts the logins made again, under mu, so that calls refused
	// together cause one login between them.
	mu  sync.Mutex
	gen uint64
}

// intercept makes a call that slgod refused as not logged in once more
// after logging in again.
//
// slgod proves a transport, not a channel, and a call refused for that
// never reached its handler, so the retry is safe for every method.
// Why: doc/client.md#a-connection-that-comes-back
func (r *relogin) intercept(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if method == pb.Grid_Login_FullMethodName {
		return invoker(ctx, method, req, reply, cc, opts...)
	}
	r.mu.Lock()
	seen := r.gen
	r.mu.Unlock()
	err := invoker(ctx, method, req, reply, cc, opts...)
	if status.Code(err) != codes.Unauthenticated {
		return err
	}
	if lerr := r.again(ctx, seen); lerr != nil {
		// The refusal stays the code; why logging in failed is added.
		return fmt.Errorf("%w (logging in again: %v)", err, lerr)
	}
	return invoker(ctx, method, req, reply, cc, opts...)
}

// again logs in, unless another call has logged in since the
// generation seen.
func (r *relogin) again(ctx context.Context, seen uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gen != seen {
		return nil
	}
	if err := login(ctx, r.cc, r.addr, r.binding); err != nil {
		return err
	}
	r.gen++
	return nil
}

// Name is what this program calls itself to slgod.
//
// The basename of the running binary, so slsh is "slsh" and slbotd is
// "slbotd" with nothing to remember and no way for the two to disagree.
//
// It is not a credential and proves nothing.  The handshake is over the
// shared secret and is bound to the TLS session; this is a label on the
// far end of an already-proved connection, for a person reading a
// sentence about it.
// Why: doc/client.md#the-name-a-client-gives-slgod
func Name() string {
	if len(os.Args) == 0 {
		return "slgo"
	}
	if n := filepath.Base(os.Args[0]); n != "" && n != "." && n != string(filepath.Separator) {
		return n
	}
	return "slgo"
}

// loginRequest is one half of the handshake, saying who is asking.
//
// The name travels as four fixed-width words rather than a string, so
// that nothing sent before the caller has proved itself can be longer
// than the server chose; auth.PackName cuts a longer one short.
func loginRequest(challenge, proof []byte) *pb.LoginRequest {
	name := auth.PackName(Name())
	return &pb.LoginRequest{
		Client_0: name[0], Client_1: name[1], Client_2: name[2], Client_3: name[3],
		Pid:       int32(os.Getpid()),
		Challenge: challenge,
		Proof:     proof,
	}
}

// login runs the two-call handshake on this connection, with the secret
// kept for the slgod at addr -- see auth.SecretPathsFor.
func login(ctx context.Context, cc *grpc.ClientConn, addr string, binding func() ([]byte, error)) error {
	secret, secretFile, err := auth.LoadSecretFor(addr)
	if err != nil {
		return fmt.Errorf("%w\nThe client and slgod share this file", err)
	}
	g := pb.NewGridClient(cc)

	begun, err := g.Login(ctx, loginRequest(nil, nil))
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	schal := begun.GetChallenge()
	if len(schal) != auth.ChallengeSize {
		return fmt.Errorf("login: the server offered a %d byte challenge", len(schal))
	}
	bind, err := binding()
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}

	cchal := make([]byte, auth.ChallengeSize)
	if _, err := rand.Read(cchal); err != nil {
		return err
	}
	done, err := g.Login(ctx, loginRequest(cchal, auth.ClientProof(secret, schal, bind)))
	if err != nil {
		return fmt.Errorf("login refused: %w (the secret used was %s)", err, secretFile)
	}
	// The server's half. A server that cannot prove it knows the secret
	// is not the server, whatever else it says.
	if subtle.ConstantTimeCompare(auth.ServerProof(secret, cchal, bind), done.GetProof()) != 1 {
		return fmt.Errorf("slgod did not prove it knows the shared secret in %s; refusing to talk to it", secretFile)
	}
	return nil
}

// Close ends the connection, and returns once the relay channels are
// closed.
//
// It does not close them itself when there is a recvLoop, because
// recvLoop is the only thing that sends on them and closing a channel
// under its sender is a race at best and a panic at worst.  So Close
// shuts the transport down instead, which makes recvLoop's Recv fail,
// and waits for it to close them on the way out.
//
// With no recvLoop there is no sender and nothing to wait for, and
// Close does it here: a connection that never attached must still
// leave anybody ranging over Messages with an end to range to.
// Why: doc/client.md#closing-while-the-relay-runs
func (c *Conn) Close() error {
	c.finish(nil)
	err := c.cc.Close()

	c.mu.Lock()
	c.closed = true
	relaying := c.relaying
	c.mu.Unlock()

	if relaying {
		<-c.relayDone
	} else {
		c.closeRelay()
	}
	return err
}

func (c *Conn) finish(err error) {
	c.closeOnce.Do(func() {
		if err != nil {
			c.err.Store(err)
		}
		close(c.done)
	})
}

// closeRelay closes the five channels the server's packets arrive on.
//
// Only recvLoop may call this while one is running.  See Close.
func (c *Conn) closeRelay() {
	c.relayOnce.Do(func() {
		close(c.messages)
		close(c.events)
		close(c.notices)
		close(c.regions)
		close(c.handled)
		close(c.relayDone)
	})
}

// Done is closed when the stream ends or the connection is closed.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err reports why it ended.
func (c *Conn) Err() error {
	if v := c.err.Load(); v != nil {
		return v.(error)
	}
	return nil
}

// Messages yields relayed grid messages, and is closed when the stream
// ends.
func (c *Conn) Messages() <-chan *Message { return c.messages }

// Events yields what arrived on the grid's event queue: the messages
// that no longer come over UDP.  Their bodies are LLSD, not the binary
// message encoding, which is why they are not on Messages.
func (c *Conn) Events() <-chan *Event { return c.events }

// Notices yields word about the grid connection itself -- it went
// away, it came back -- rather than anything the grid said.
func (c *Conn) Notices() <-chan *pb.AgentEvent { return c.notices }

// RegionChanges yields the notices that say the avatar is in another
// region, which is the one kind a client cannot afford to merely log:
// see RegionChange.  It is closed with the rest when the stream ends.
//
// Every one of these is also a notice, and Notices still carries it.
func (c *Conn) RegionChanges() <-chan *RegionChange { return c.regions }

// HandledOffers yields the offers the server was keeping that have since
// been dealt with, by this client or another, and is closed with the
// rest when the stream ends.  A client holding one should drop it: it is
// no longer waiting, and answering it again would answer it twice.
func (c *Conn) HandledOffers() <-chan *pb.OfferHandled { return c.handled }

// Offers is what the server said was waiting for an answer when this
// client attached, the offers that arrived while nobody was attached
// among them, and nil when it said nothing.
//
// Nil is an answer worth passing on.  It is a server too old to keep a
// record, or an attach that did not subscribe to ImprovedInstantMessage,
// and either way a client knows only what it sees arrive from here on.
//
// The messages are here and not in Messages.  They are history rather
// than arrivals, and a client that wants them wants all of them before it
// reads anything live -- which a value it can take whole gives it, and a
// channel shared with the live relay does not.
func (c *Conn) Offers() *pb.OfferRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.offers
}

// Handled tells the server this client is dealing with an offer it is
// keeping, BEFORE the answer is sent.  See the rpc in slgo.proto: the
// answer says whether to send, and undo puts an offer back when the
// answer could not be sent after all.
func (c *Conn) Handled(ctx context.Context, offer, how string, undo bool) (*pb.HandledResponse, error) {
	name := c.agentName()
	return c.grid.Handled(ctx, &pb.HandledRequest{Agent: name, Offer: offer, How: how, Undo: undo})
}

// Attach opens the packet stream against one of the server's agents and
// subscribes to the named messages.  "*" means everything; naming
// nothing means nothing is relayed until Subscribe says otherwise.
//
// An empty name takes the daemon's default, and every call made on this
// connection afterwards names the session that was chosen: the default
// moving later does not move this connection with it.
//
// The stream outlives a session that drops and is re-established under
// it.  It ends, with codes.FailedPrecondition and the daemon's reason,
// when the session is stopped for good or no longer hosted, the daemon
// shutting down among them.
func (c *Conn) Attach(ctx context.Context, agentName string, subscribe ...string) (*pb.AgentInfo, error) {
	return c.attach(ctx, agentName, false, subscribe)
}

// AttachWeak attaches without counting as somebody USING the avatar.
//
// For a client that attends rather than uses: it is relayed to and
// counted like any other, but slgod does not consult it when deciding
// whether anybody would mind the session being taken away.  See
// Attach.weak in slgo.proto -- a daemon sitting attached to every
// avatar all day would otherwise make "logout" mean "logout --force",
// which is a flag people learn to type without reading.
func (c *Conn) AttachWeak(ctx context.Context, agentName string, subscribe ...string) (*pb.AgentInfo, error) {
	return c.attach(ctx, agentName, true, subscribe)
}

func (c *Conn) attach(ctx context.Context, agentName string, weak bool, subscribe []string) (*pb.AgentInfo, error) {
	stream, err := c.grid.Stream(context.WithoutCancel(ctx))
	if err != nil {
		return nil, err
	}
	if err := c.sendPacket(stream, &pb.ClientPacket{Body: &pb.ClientPacket_Attach{
		Attach: &pb.Attach{Agent: agentName, Subscribe: subscribe, Weak: weak},
	}}); err != nil {
		return nil, err
	}

	// The server answers an attach with who we are attached to.
	first, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	att := first.GetAttached()
	if att == nil {
		return nil, fmt.Errorf("client: server answered attach with %T", first.Body)
	}

	// Under the lock with the rest, so that Close either sees the
	// recvLoop about to start and waits for it, or gets here first and
	// is refused -- and never decides there is no sender just as one
	// begins.
	// Why: doc/client.md#closing-while-the-relay-runs
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("client: this connection is closed")
	}
	// The name the daemon resolved, rather than the one asked for.  An
	// empty name took the default, and the calls after this have to
	// reach the session this stream is on, not whichever is the default
	// by the time they are made.
	name := att.Agent.GetName()
	if name == "" {
		name = agentName
	}
	c.agent = name
	c.stream = stream
	c.keepInfoLocked(att.Agent, c.moves)
	c.offers = att.GetOffers()
	c.relaying = true
	c.mu.Unlock()

	go c.recvLoop(stream)
	return att.Agent, nil
}

func (c *Conn) recvLoop(stream pb.Grid_StreamClient) {
	// The sender closes, so that nothing is ever sending on a channel
	// somebody else has closed.
	defer c.closeRelay()
	for {
		p, err := stream.Recv()
		if err != nil {
			c.finish(err)
			return
		}
		switch b := p.Body.(type) {
		case *pb.ServerPacket_Message:
			c.deliver(b.Message)
		case *pb.ServerPacket_Handled:
			select {
			case c.handled <- b.Handled:
			default:
				c.dropped.Add(1)
				c.noteDrop("offer handled")
			}
		case *pb.ServerPacket_Event:
			e := &Event{Name: b.Event.Message, Body: b.Event.Body}
			if b.Event.ReceivedAt != 0 {
				e.At = time.UnixMicro(b.Event.ReceivedAt)
			}
			select {
			case c.events <- e:
			default:
				c.dropped.Add(1)
				c.noteDrop("event " + e.Name)
			}
		case *pb.ServerPacket_Notice:
			if b.Notice.GetKind() == pb.AgentEvent_REGION_CHANGED {
				// Before the notice goes anywhere, so that nobody
				// told of the change is answered from the list of
				// before it.
				c.mu.Lock()
				c.moves++
				c.mu.Unlock()
			}
			select {
			case c.notices <- b.Notice:
			default:
				c.dropped.Add(1)
				c.noteDrop("notice")
			}
			if b.Notice.GetKind() == pb.AgentEvent_REGION_CHANGED {
				// Dropped like the rest when nobody is reading,
				// and it is the drop that costs most: a session
				// that missed one goes on believing it is in a
				// region it has left.  Nothing here can do
				// better -- blocking would stop this stream, and
				// with it the messages, the events and the
				// answers to locks -- so the buffer is what
				// stands between the two, and these arrive one
				// to a teleport, crossing or reconnect where a
				// message arrives one to a packet.
				select {
				case c.regions <- &RegionChange{
					Region: b.Notice.GetRegion(),
					Handle: b.Notice.GetRegionHandle(),

					TeleportFlags: b.Notice.GetTeleportFlags(),
				}:
				default:
					c.dropped.Add(1)
					c.noteDrop("region change")
				}
			}
		case *pb.ServerPacket_Granted:
			// Given back off this loop: a send can wait on flow
			// control, and this loop is how everything else arrives.
			if late := c.grants.deliver(b.Granted); late != "" {
				go c.giveBack(late)
			}
		case *pb.ServerPacket_Locked:
			// Never dropped: somebody is waiting on this, and losing
			// it would leave them waiting for a lock they have been
			// given.
			c.locks.deliver(b.Locked)
		}
	}
}

// deliver hands one relayed message to whoever reads Messages.
//
// Only recvLoop calls it; see Close for why nothing else may send on
// the channel.
func (c *Conn) deliver(m *pb.InboundMessage) {
	out := &Message{
		ID:         msg.ID(m.Id),
		Name:       m.Name,
		Sequence:   m.Sequence,
		Flags:      m.Flags,
		Body:       m.Body,
		FromClient: m.FromClient,
		Offer:      m.Offer,
		Recorded:   m.Recorded,
	}
	if m.ReceivedAt != 0 {
		out.At = time.UnixMicro(m.ReceivedAt)
	}
	select {
	case c.messages <- out:
	default:
		// A client that stops reading loses messages, and each is
		// counted.
		// Why: doc/client.md#counting-what-is-dropped
		c.dropped.Add(1)
		c.noteDrop("message " + m.Name)
	}
}

// Info is what the server said about the attached agent: at attach,
// or at the last Refresh.
func (c *Conn) Info() *pb.AgentInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info
}

// Refresh asks the daemon who the attached agent is now, and keeps the
// answer as Info and the capability list.
//
// A session re-established under this connection has a new session id
// and new capabilities, and a teleport or crossing new capabilities;
// each arrives as a region change, after which HasCap and Caps call
// this themselves.  See also DoCap.
func (c *Conn) Refresh(ctx context.Context) (*pb.AgentInfo, error) {
	c.mu.RLock()
	moves := c.moves
	c.mu.RUnlock()
	st, err := c.Status(ctx)
	if err != nil {
		return nil, err
	}
	a := st.GetAgent()
	if a == nil {
		return nil, fmt.Errorf("client: the daemon said nothing about %s", c.agentName())
	}
	c.mu.Lock()
	// Not over an answer asked for after a later change.
	if moves >= c.capsAt {
		c.keepInfoLocked(a, moves)
	}
	c.mu.Unlock()
	return a, nil
}

// keepInfoLocked takes what the daemon said about the agent, as of the
// region change counted as moves.  An answer asked for before a later
// change leaves the list marked for asking again.
func (c *Conn) keepInfoLocked(a *pb.AgentInfo, moves uint64) {
	c.info = a
	c.caps = make(map[string]bool, len(a.GetCaps()))
	for _, n := range a.GetCaps() {
		c.caps[n] = true
	}
	c.capsAt = moves
}

// capsRefreshWait bounds the ask HasCap and Caps make after a region
// change, which have no context of their own.
const capsRefreshWait = 10 * time.Second

// freshen asks again when a region change has come since the list was
// read.  A failed ask keeps the old list, still marked, so the next use
// tries again.
func (c *Conn) freshen() {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.mu.RLock()
	stale := c.capsAt != c.moves
	c.mu.RUnlock()
	if !stale {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), capsRefreshWait)
	defer cancel()
	c.Refresh(ctx)
}

// Subscribe replaces the set of messages relayed to this stream.
//
// With no names it clears it, and nothing is relayed until something
// is added back.  Replace is what says so: an empty repeated field
// does not survive proto3, so the flag is the only thing that tells
// the far end this was meant.
func (c *Conn) Subscribe(names ...string) error {
	return c.sub(&pb.Subscribe{Set: names, Replace: true})
}

// Watch adds to it.
func (c *Conn) Watch(names ...string) error {
	return c.sub(&pb.Subscribe{Add: names})
}

// Unwatch removes from it.
func (c *Conn) Unwatch(names ...string) error {
	return c.sub(&pb.Subscribe{Remove: names})
}

func (c *Conn) sub(s *pb.Subscribe) error {
	c.mu.RLock()
	stream := c.stream
	c.mu.RUnlock()
	if stream == nil {
		return errors.New("client: not attached")
	}
	return c.sendPacket(stream, &pb.ClientPacket{Body: &pb.ClientPacket_Subscribe{Subscribe: s}})
}

// Send puts a message on the circuit.  The server assigns the sequence
// number and handles reliability.
func (c *Conn) Send(ctx context.Context, m msg.Message, reliable bool) error {
	body, err := m.Encode()
	if err != nil {
		return err
	}
	return c.SendRaw(ctx, m.MsgInfo().ID, body, reliable)
}

// SendRaw is Send for a message number and body, without a type.
func (c *Conn) SendRaw(ctx context.Context, id msg.ID, body []byte, reliable bool) error {
	out := &pb.OutboundMessage{Id: uint32(id), Body: body, Reliable: reliable}

	c.mu.RLock()
	stream := c.stream
	c.mu.RUnlock()

	// On a stream when there is one, so ordering with everything
	// else the client is sending is preserved.
	if stream != nil {
		return c.sendPacket(stream, &pb.ClientPacket{Body: &pb.ClientPacket_Message{Message: out}})
	}
	_, err := c.grid.Send(ctx, &pb.SendRequest{Agent: c.agentName(), Message: out})
	return err
}

// sendPacket puts one frame on an attach stream, and every send on one
// goes through it.  gRPC allows only one send at a time on a stream, and
// this connection sends from its callers, from their waits for locks and
// places, and from the goroutines the receive loop starts to give grants
// back.
func (c *Conn) sendPacket(stream pb.Grid_StreamClient, p *pb.ClientPacket) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return stream.Send(p)
}

// ListAgents asks what the server hosts.
func (c *Conn) ListAgents(ctx context.Context) ([]*pb.AgentInfo, error) {
	resp, err := c.grid.ListAgents(ctx, &pb.ListAgentsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.Agents, nil
}

// Status asks about the attached agent.
func (c *Conn) Status(ctx context.Context) (*pb.StatusResponse, error) {
	name := c.agentName()
	return c.grid.Status(ctx, &pb.StatusRequest{Agent: name})
}

// ViewerCredential asks the daemon for a password a real viewer may log
// in as this agent with, once.
//
// Deliberately not folded into Status.  Every call makes a secret and
// drops the one before it, so it must be something a caller does on
// purpose; and the answer is the one thing this package handles that
// must not be logged, kept or repeated -- it is passed straight to
// whatever is starting the viewer and then forgotten.
func (c *Conn) ViewerCredential(ctx context.Context) (*pb.ViewerCredentialResponse, error) {
	name := c.agentName()
	return c.grid.ViewerCredential(ctx, &pb.ViewerCredentialRequest{Agent: name})
}

// Presence reads where the avatar is and what it can see.  A draw
// distance above zero sets it; zero leaves it alone.
//
// It goes to the server because the server owns the camera: AgentUpdate
// has to keep being sent, and the simulator works its interest list out
// from the camera rather than from where the avatar is.
func (c *Conn) Presence(ctx context.Context, drawDistance float32) (*pb.PresenceResponse, error) {
	return c.grid.Presence(ctx, &pb.PresenceRequest{
		Agent:        c.agentName(),
		DrawDistance: drawDistance,
	})
}

// Attachments asks the server what the simulator last said an avatar
// is wearing.  An empty avatar is this one.
func (c *Conn) Attachments(ctx context.Context, avatar string) (*pb.AttachmentsResponse, error) {
	return c.grid.Attachments(ctx, &pb.AttachmentsRequest{Agent: c.agentName(), Avatar: avatar})
}

// Objects asks the server what the region has said about itself.
//
// The server holds this because a region describes itself once, when
// the avatar arrives, and a client that attaches later never hears it.
func (c *Conn) Objects(ctx context.Context, named, id string) (*pb.ObjectsResponse, error) {
	return c.grid.Objects(ctx, &pb.ObjectsRequest{
		Agent: c.agentName(), Named: named, Id: id,
	})
}

// Friends asks who this avatar's friends are and which are logged in.
//
// The server holds it for the same reason it holds the objects: the
// list arrives only in the login response and online status only as a
// burst just after the handshake, so a client that attached later was
// not there for either.
func (c *Conn) Friends(ctx context.Context) ([]*pb.Friend, error) {
	r, err := c.grid.Friends(ctx, &pb.FriendsRequest{Agent: c.agentName()})
	if err != nil {
		return nil, err
	}
	return r.Friends, nil
}

// NoteFriend tells the server about a friendship just formed.
//
// Accepting an offer is the one thing the grid never reports back: the
// side that offered is told, and the side that accepted -- which is the
// side that knows -- is told nothing.  Whoever accepts is expected to
// remember, so this is where the remembering is put, alongside the rest
// of what a client restart must not lose.
func (c *Conn) NoteFriend(ctx context.Context, id msg.UUID, online bool) error {
	_, err := c.grid.NoteFriend(ctx, &pb.NoteFriendRequest{
		Agent: c.agentName(), Id: id.String(), Online: online,
	})
	return err
}

// Host asks the daemon to bring a session up.
//
// Safe to repeat: one already hosted comes back with Already set rather
// than being logged in a second time, which would kick the session it
// has.  Force starts one that was stopped deliberately -- check nobody
// is using that avatar first, because that is usually why it stopped.
func (c *Conn) Host(ctx context.Context, name string, force bool) (*pb.HostResponse, error) {
	return c.grid.Host(ctx, &pb.HostRequest{Agent: name, Force: force})
}

// Logout puts a session down and keeps it down.
//
// Unless force is set, it is refused while a client that attached with
// Attach rather than AttachWeak is using the session, and the refusal
// names them: a benchmark mid-run has a script installed and a reading
// half taken, and losing that should be a decision.
func (c *Conn) Logout(ctx context.Context, name string, force bool) (*pb.LogoutResponse, error) {
	r, err := c.grid.Logout(ctx, &pb.LogoutRequest{Agent: name, Force: force})
	if err == nil {
		return r, nil
	}
	// A refusal says who is holding the session, and says it in the
	// status details, because a unary call hands back a response or an
	// error and never both.  Handing it on beside the error is what
	// lets a caller name them without reading the sentence.
	for _, d := range status.Convert(err).Details() {
		if lr, ok := d.(*pb.LogoutResponse); ok {
			return lr, err
		}
	}
	return r, err
}

// Region asks what the simulator said about itself in the handshake,
// which happens once, before any client is listening.
func (c *Conn) Region(ctx context.Context) (*pb.RegionInfo, error) {
	return c.grid.Region(ctx, &pb.RegionRequest{Agent: c.agentName()})
}

// SimStats asks what the simulator has said about how it is doing over
// the last minute, which only a client that was listening all along
// could otherwise know.
func (c *Conn) SimStats(ctx context.Context) (*pb.SimStatsResponse, error) {
	return c.grid.SimStats(ctx, &pb.SimStatsRequest{Agent: c.agentName()})
}

// RegionDetails asks for the last RegionInfo the region sent.  It does
// not ask the region: sending RequestRegionInfo is a message, and goes
// by Send.
func (c *Conn) RegionDetails(ctx context.Context) (*pb.RegionDetailsResponse, error) {
	return c.grid.RegionDetails(ctx, &pb.RegionDetailsRequest{Agent: c.agentName()})
}

// Land is what the session was told about the ground it is on: the
// parcel it was pushed on arrival, and the region's parcel overlay.
//
// It goes to the server for the reason Region does: both arrive once,
// unasked, before any client is listening.  The overlay cannot be asked
// for a second time at all, so a client that was not there when the
// avatar arrived can get it here or nowhere.
func (c *Conn) Land(ctx context.Context) (*pb.LandInfo, error) {
	return c.grid.Land(ctx, &pb.LandRequest{Agent: c.agentName()})
}

// Ground is the height of the land in the region the avatar is in: the
// highest it comes in a rectangle, which for a point is the height
// there.  Known is false where the land under it has not all arrived,
// or where the rectangle reaches outside the region.
//
// It goes to the server for the reason Land does: the heightmap is sent
// once, when the avatar arrives, and never again for the asking.
func (c *Conn) Ground(ctx context.Context, west, south, east, north float32) (float32, bool, error) {
	r, err := c.grid.Ground(ctx, &pb.GroundRequest{
		Agent: c.agentName(), West: west, South: south, East: east, North: north,
	})
	if err != nil {
		return 0, false, err
	}
	return r.GetHeight(), r.GetKnown(), nil
}

// Neighbours reads the circuits the server holds to the regions around
// the one the avatar is in, and turns them on or off.
//
// A nil set asks without changing anything, which the wire spells as an
// absent field rather than as a false: the difference between "leave it
// alone" and "turn it off" is the whole reason the field has presence.
//
// It goes to the server for the reason Presence does: the circuits are
// the server's, they cost a socket and a share of the traffic each for
// as long as they are held, and a client that owned them would take
// them away from every other client by exiting.
func (c *Conn) Neighbours(ctx context.Context, set *bool) (*pb.NeighboursResponse, error) {
	return c.grid.Neighbours(ctx, &pb.NeighboursRequest{Agent: c.agentName(), Set: set})
}

// Control asks the server to send one AgentUpdate carrying these
// control flags, and forget them.
//
// It goes to the server because an AgentUpdate is not just its flags: it
// carries the camera, its three axes and the draw distance, and the
// simulator scopes its interest list by them.  This side knows a
// position and no axes at all, so a client that built one itself would
// be guessing at the camera and inventing a draw distance, and the
// simulator would believe it until the server's own update a second
// later put it back.
//
// A single call rather than a sit and a stand, because the flags are the
// same shape for everything that moves an avatar; agent.ControlStandUp
// and agent.ControlSitOnGround are the two this repository has measured.
func (c *Conn) Control(ctx context.Context, flags uint32) error {
	_, err := c.grid.Control(ctx, &pb.ControlRequest{Agent: c.agentName(), Flags: flags})
	return err
}

// Move asks the server to walk the avatar to a place in its region, and
// hands each report on how it is going to progress, on this goroutine,
// until the last; the last is what it returns, and progress is not given
// it.  The request's agent is filled in.
//
// The walk is the server's and not this side's: it steers ten times a
// second from where the avatar actually is, and it stops the avatar the
// moment this call's stream ends.  So cancelling ctx is how a walk is
// abandoned, and what a program that exits in the middle of one does
// without having to think about it.  See the rpc in slgo.proto.
//
// A walk that could not start is not an error: it is a last report whose
// state says why.  The error is for the call itself.
func (c *Conn) Move(ctx context.Context, req *pb.MoveRequest, progress func(*pb.MoveEvent)) (*pb.MoveEvent, error) {
	r := proto.Clone(req).(*pb.MoveRequest)
	r.Agent = c.agentName()
	stream, err := c.grid.Move(ctx, r)
	if err != nil {
		return nil, err
	}
	for {
		e, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				err = errors.New("client: the walk's stream ended without saying how the walk did")
			}
			return nil, err
		}
		if e.State != pb.MoveEvent_MOVING {
			return e, nil
		}
		if progress != nil {
			progress(e)
		}
	}
}

// Face turns the avatar toward a place, or to a heading when target is
// nil, and returns the heading it was given.
func (c *Conn) Face(ctx context.Context, target *pb.Vector3, yaw float32) (float32, error) {
	req := &pb.FaceRequest{Agent: c.agentName()}
	if target != nil {
		req.Toward = &pb.FaceRequest_Target{Target: target}
	} else {
		req.Toward = &pb.FaceRequest_Yaw{Yaw: yaw}
	}
	r, err := c.grid.Face(ctx, req)
	if err != nil {
		return 0, err
	}
	return r.Yaw, nil
}

// Halt ends any walk and stops the avatar, and says whether a walk was
// under way.
func (c *Conn) Halt(ctx context.Context) (bool, error) {
	r, err := c.grid.Halt(ctx, &pb.HaltRequest{Agent: c.agentName()})
	if err != nil {
		return false, err
	}
	return r.Walking, nil
}

// Posture asks slgod how the avatar is placed: standing, sitting on the
// ground, or sitting on the object whose local id and (when described)
// uuid come with it.
func (c *Conn) Posture(ctx context.Context) (*pb.PostureResponse, error) {
	return c.grid.Posture(ctx, &pb.PostureRequest{Agent: c.agentName()})
}

// agentName is the session this connection is attached to, read under
// the lock attach writes it under.  Every read of c.agent goes through
// here, and TestTheAgentIsReadOnlyUnderTheLock refuses one anywhere else.
func (c *Conn) agentName() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.agent
}

// Flush empties the server's object cache.
func (c *Conn) Flush(ctx context.Context) (int, error) {
	r, err := c.grid.Flush(ctx, &pb.FlushRequest{Agent: c.agentName()})
	if err != nil {
		return 0, err
	}
	return int(r.Forgotten), nil
}

// ---------------------------------------------------------- capabilities

// A Conn is an agent.CapDoer, so anything written against capabilities
// runs unchanged on either side of the link.
//
// This is the return on making the server a gateway rather than a
// participant.  agent.FetchInventory takes a CapDoer: give it an Agent
// and it makes the HTTPS requests itself, give it a Conn and the server
// makes them on its behalf.  The inventory code does not know which,
// and the server does not know what inventory is.
var _ agent.CapDoer = (*Conn)(nil)

// HasCap reports whether the attached agent offers a capability.  After
// a region change the daemon is asked again first; see Refresh.
func (c *Conn) HasCap(name string) bool {
	c.freshen()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.caps[name]
}

// Caps lists them, asking again first as HasCap does.
func (c *Conn) Caps() []string {
	c.freshen()
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.caps))
	for n := range c.caps {
		out = append(out, n)
	}
	return out
}

// DoCap makes a capability request through the server.
//
// A named capability answered 404 "cap not found" is taken for one
// looked up in a session or region that has since been replaced, which
// is when that answer has been seen.  The agent is asked about again,
// and the request made once more; what that answers is what is
// returned.  A request to a URL is not made again, since its URL would
// be the same.
// Why: doc/client.md#capabilities-after-the-session-changes
func (c *Conn) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	resp, err := c.doCap(ctx, r)
	if err != nil || r.URL != "" || !capNotFound(resp) {
		return resp, err
	}
	if _, err := c.Refresh(ctx); err != nil {
		return resp, nil
	}
	return c.doCap(ctx, r)
}

// capNotFound is the grid's answer to a capability URL it does not know.
func capNotFound(r *agent.CapResponse) bool {
	return r.Status == http.StatusNotFound &&
		bytes.Contains(bytes.ToLower(r.Body), []byte("cap not found"))
}

func (c *Conn) doCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	name := c.agentName()

	resp, err := c.grid.Cap(ctx, &pb.CapRequest{
		Agent:       name,
		Cap:         r.Cap,
		Method:      r.Method,
		Path:        r.Path,
		Body:        r.Body,
		ContentType: r.Type,
		Url:         r.URL,
	}, grpc.MaxCallRecvMsgSize(64<<20))
	if err != nil {
		return nil, err
	}
	return &agent.CapResponse{Status: int(resp.Status), Body: resp.Body}, nil
}

// Dropped is how many messages, events, notices, region changes and
// handled offers this connection has thrown away because nobody was
// reading them fast enough.
//
// Anything but zero means something was missed, and what was missed is
// gone: there is no way to ask for it again.  It is worth looking at
// after a run whose answer surprised you.
func (c *Conn) Dropped() uint64 { return c.dropped.Load() }

// OnDrop sets what to call when something is dropped, which is how a
// program says so in its own words.  It is called on the receiving
// goroutine and must not block.
func (c *Conn) OnDrop(fn func(what string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onDrop = fn
}

func (c *Conn) noteDrop(what string) {
	c.mu.RLock()
	fn := c.onDrop
	c.mu.RUnlock()
	if fn != nil {
		fn(what)
	}
}
