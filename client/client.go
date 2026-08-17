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
// the server relays what it does not understand, so a client can handle
// a message added to the protocol after the server was built.
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
	"github.com/quark-idlemind/slgo/auth"
	"google.golang.org/grpc/credentials"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// Conn is a connection to a server.
type Conn struct {
	cc   *grpc.ClientConn
	grid pb.GridClient

	agent string

	mu   sync.RWMutex
	info *pb.AgentInfo
	caps map[string]bool

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

	// relaying says recvLoop is running, and closed says Close has
	// been. Both are under mu, and the pair is what decides who closes
	// the four channels above; see closeRelay.
	relaying bool
	closed   bool

	// locks is what this connection has asked slgod for exclusive use
	// of.  See lock.go.
	locks locking

	closeOnce sync.Once
	done      chan struct{}
	err       atomic.Value

	// relayOnce guards the close of the four relay channels, and
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
}

// Decode parses the event body.
func (e *Event) Decode() (map[string]any, error) {
	v, err := llsd.Decode(bytes.NewReader(e.Body))
	if err != nil {
		return nil, err
	}
	return llsd.Map(v), nil
}

// Dial connects to a server without opening a stream.  Use Attach to
// start receiving messages.
// Dial connects to slgod over TLS and authenticates both ways.
//
// One connection, used for the handshake and everything after, because
// the connection is what the handshake proves. The certificate is not
// checked and is not meant to be: it proves nothing, and what proves the
// server is its half of the exchange, tied to this TLS session.
//
// Passing explicit dial options skips all of it, which is for tests that
// bring up a server in the same process.
func Dial(ctx context.Context, addr string, opts ...grpc.DialOption) (*Conn, error) {
	authenticate := len(opts) == 0
	var binding func() ([]byte, error)
	if authenticate {
		var creds credentials.TransportCredentials
		creds, binding = auth.ClientTLS()
		opts = []grpc.DialOption{grpc.WithTransportCredentials(creds)}
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
		if err := login(ctx, cc, binding); err != nil {
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
		done:      make(chan struct{}),
		relayDone: make(chan struct{}),
	}, nil
}

// login runs the two-call handshake on this connection.
func login(ctx context.Context, cc *grpc.ClientConn, binding func() ([]byte, error)) error {
	secret, err := auth.LoadSecret("")
	if err != nil {
		return fmt.Errorf("%w\nThe client and slgod share this file", err)
	}
	g := pb.NewGridClient(cc)

	begun, err := g.Login(ctx, &pb.LoginRequest{Client: "slgo"})
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
	done, err := g.Login(ctx, &pb.LoginRequest{
		Client: "slgo", Challenge: cchal, Proof: auth.ClientProof(secret, schal, bind),
	})
	if err != nil {
		return fmt.Errorf("login refused: %w", err)
	}
	// The server's half. A server that cannot prove it knows the secret
	// is not the server, whatever else it says.
	if subtle.ConstantTimeCompare(auth.ServerProof(secret, cchal, bind), done.GetProof()) != 1 {
		return fmt.Errorf("slgod did not prove it knows the shared secret; refusing to talk to it")
	}
	return nil
}

// Close ends the connection, and returns once the relay channels are
// closed.
//
// It does not close them itself when there is a recvLoop, because
// recvLoop is the only thing that sends on them and closing a channel
// under its sender is a race at best and a panic at worst -- which is
// what this used to be, on the one path every client takes to hang up.
// So Close shuts the transport down instead, which makes recvLoop's
// Recv fail, and waits for it to close them on the way out.
//
// With no recvLoop there is no sender and nothing to wait for, and
// Close does it here: a connection that never attached must still
// leave anybody ranging over Messages with an end to range to.
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

// closeRelay closes the three channels the server's packets arrive on.
//
// Only recvLoop may call this while one is running.  See Close.
func (c *Conn) closeRelay() {
	c.relayOnce.Do(func() {
		close(c.messages)
		close(c.events)
		close(c.notices)
		close(c.regions)
		close(c.relayDone)
	})
}

// Done is closed when the stream ends.
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

// Attach opens the packet stream against one of the server's agents and
// subscribes to the named messages.  "*" means everything; naming
// nothing means nothing is relayed until Subscribe says otherwise.
func (c *Conn) Attach(ctx context.Context, agentName string, subscribe ...string) (*pb.AgentInfo, error) {
	stream, err := c.grid.Stream(context.WithoutCancel(ctx))
	if err != nil {
		return nil, err
	}
	if err := stream.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Attach{
		Attach: &pb.Attach{Agent: agentName, Subscribe: subscribe},
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
	//
	// No test reaches the refusal, and none can through this package's
	// own doors: Close shuts the transport down, so an attach that got
	// this far -- stream opened, first packet read -- ran entirely
	// before it.  What is left is the window between that read and this
	// lock, which is exactly what the guard is for.
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("client: this connection is closed")
	}
	c.agent = agentName
	c.stream = stream
	c.info = att.Agent
	c.caps = make(map[string]bool, len(att.Agent.GetCaps()))
	for _, n := range att.Agent.GetCaps() {
		c.caps[n] = true
	}
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
			m := b.Message
			out := &Message{
				ID:       msg.ID(m.Id),
				Name:     m.Name,
				Sequence: m.Sequence,
				Flags:    m.Flags,
				Body:     m.Body,
			}
			if m.ReceivedAt != 0 {
				out.At = time.UnixMicro(m.ReceivedAt)
			}
			select {
			case c.messages <- out:
			default: // a client that stops reading loses messages
			}
		case *pb.ServerPacket_Event:
			e := &Event{Name: b.Event.Message, Body: b.Event.Body}
			if b.Event.ReceivedAt != 0 {
				e.At = time.UnixMicro(b.Event.ReceivedAt)
			}
			select {
			case c.events <- e:
			default:
			}
		case *pb.ServerPacket_Notice:
			select {
			case c.notices <- b.Notice:
			default:
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
				// to a teleport where a message arrives one to a
				// packet.
				select {
				case c.regions <- &RegionChange{
					Region: b.Notice.GetRegion(),
					Handle: b.Notice.GetRegionHandle(),
				}:
				default:
				}
			}
		case *pb.ServerPacket_Locked:
			// Never dropped: somebody is waiting on this, and losing
			// it would leave them waiting for a lock they have been
			// given.
			c.locks.deliver(b.Locked)
		}
	}
}

// Info is what the server said about the attached agent.
func (c *Conn) Info() *pb.AgentInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info
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
	return stream.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Subscribe{Subscribe: s}})
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
	stream, name := c.stream, c.agent
	c.mu.RUnlock()

	// On a stream when there is one, so ordering with everything
	// else the client is sending is preserved.
	if stream != nil {
		return stream.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Message{Message: out}})
	}
	_, err := c.grid.Send(ctx, &pb.SendRequest{Agent: name, Message: out})
	return err
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
	c.mu.RLock()
	name := c.agent
	c.mu.RUnlock()
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
	c.mu.RLock()
	name := c.agent
	c.mu.RUnlock()
	return c.grid.ViewerCredential(ctx, &pb.ViewerCredentialRequest{Agent: name})
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

// HasCap reports whether the attached agent offered a capability.
// Presence reads where the avatar is and what it can see.  A draw
// distance above zero sets it; zero leaves it alone.
//
// It goes to the server because the server owns the camera: AgentUpdate
// has to keep being sent, and the simulator works its interest list out
// from the camera rather than from where the avatar is.
func (c *Conn) Presence(ctx context.Context, drawDistance float32) (*pb.PresenceResponse, error) {
	return c.grid.Presence(ctx, &pb.PresenceRequest{
		Agent:        c.agent,
		DrawDistance: drawDistance,
	})
}

// Objects asks the server what the region has said about itself.
//
// The server holds this because a region describes itself once, when
// the avatar arrives, and a client that attaches later never hears it.
func (c *Conn) Objects(ctx context.Context, named, id string) (*pb.ObjectsResponse, error) {
	return c.grid.Objects(ctx, &pb.ObjectsRequest{
		Agent: c.agent, Named: named, Id: id,
	})
}

// Friends asks who this avatar's friends are and which are logged in.
//
// The server holds it for the same reason it holds the objects: the
// list arrives only in the login response and online status only as a
// burst just after the handshake, so a client that attached later was
// not there for either.
func (c *Conn) Friends(ctx context.Context) ([]*pb.Friend, error) {
	r, err := c.grid.Friends(ctx, &pb.FriendsRequest{Agent: c.agent})
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
		Agent: c.agent, Id: id.String(), Online: online,
	})
	return err
}

// Region asks what the simulator said about itself.
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
// It is refused while clients are attached unless force is set, and the
// refusal names them: a benchmark mid-run has a script installed and a
// reading half taken, and losing that should be a decision.
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

func (c *Conn) Region(ctx context.Context) (*pb.RegionInfo, error) {
	return c.grid.Region(ctx, &pb.RegionRequest{Agent: c.agent})
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
	return c.grid.Neighbours(ctx, &pb.NeighboursRequest{Agent: c.agent, Set: set})
}

// Flush empties the server's object cache.
func (c *Conn) Flush(ctx context.Context) (int, error) {
	r, err := c.grid.Flush(ctx, &pb.FlushRequest{Agent: c.agent})
	if err != nil {
		return 0, err
	}
	return int(r.Forgotten), nil
}

func (c *Conn) HasCap(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.caps[name]
}

// Caps lists them.
func (c *Conn) Caps() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.caps))
	for n := range c.caps {
		out = append(out, n)
	}
	return out
}

// DoCap makes a capability request through the server.
func (c *Conn) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	c.mu.RLock()
	name := c.agent
	c.mu.RUnlock()

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
