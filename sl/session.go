package sl

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/internal/xfer"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// ErrTimeout is reported when the simulator never confirmed something.
// It is worth distinguishing: it means the request may well have taken
// effect and we did not see it, which is a different situation from a
// refusal.
var ErrTimeout = errors.New("sl: timed out waiting for the simulator")

// Subscriptions are the messages this package needs relayed to it.
// Passing anything less to Attach leaves it waiting for confirmations
// that will not arrive.
//
// AvatarSitResponse is here and AvatarAnimation is not, and the
// difference is what each costs.  The first arrives once, when this
// avatar sits on something, and carries the seat offset; the second
// arrives for every avatar in range, in full, about every three seconds,
// which is a bill slrun and slbench would pay for ever for
// something only a sit reads.  So the sit borrows it for the length of
// the command and gives it back; see sit.go.
var Subscriptions = []string{
	"ObjectUpdate", "ObjectUpdateCompressed", "ObjectProperties",
	"ObjectPropertiesFamily", "KillObject",
	"UpdateCreateInventoryItem", "ReplyTaskInventory",
	"SendXferPacket", "AbortXfer", "TransferInfo", "TransferPacket",
	"ChatFromSimulator", "ChatFromViewer", "AlertMessage",
	"AvatarSitResponse",
	"ImprovedInstantMessage", "UUIDNameReply", "AvatarPickerReply",
	"AvatarPropertiesReply", "AvatarInterestsReply", "AvatarGroupsReply",
	"OnlineNotification", "OfflineNotification",
	"ScriptRunningReply", "ScriptQuestion", "ScriptDialog",
	"TeleportLocal", "TeleportFailed", "TeleportFinish",
	"AgentMovementComplete", "ParcelProperties", "ParcelDwellReply", "ParcelInfoReply",
	"MapBlockReply", "MoneyBalanceReply",

	// A group's chat, which is joined and spoken to over the circuit but
	// answered here.  See groupchat.go.
	"ChatterBoxInvitation", "ChatterBoxSessionStartReply",
	"ChatterBoxSessionEventReply", "ChatterBoxSessionAgentListUpdates",
	"ForceCloseChatterBoxSession",
}

// Session is a connection to a hosted agent, with the bookkeeping needed
// to tell whether anything asked for actually happened.
//
// It is safe for concurrent use.  It reads the relay in one goroutine
// and everything else takes the lock.
type Session struct {
	b Backend

	// degrabbed is when this session last sent an ObjectDeGrab for each
	// linkset (its root's local id), under touchMu, so that the next grab
	// of any prim of it can keep
	// touchGap (touch.go).
	touchMu   sync.Mutex
	degrabbed map[uint32]time.Time

	// ident is who this session is NOW: the avatar, the session id,
	// the capability URLs.  It is a pointer that gets replaced rather
	// than a struct that gets edited, and it is atomic rather than
	// under mu, because agentBlock reads it while building nearly
	// every message this package sends -- some of them from code that
	// already holds mu, which a lock here would deadlock.
	//
	// It changes because a daemon can re-establish the grid session
	// under an attached client: same avatar, new session id.  Sending
	// the old one is sending into silence.  See Backend.Refresh and
	// refreshIdentity below.
	ident atomic.Pointer[Info]

	// opts is what SetOptions last set, or nil for every default.
	opts atomic.Pointer[Options]

	// me and invRoot do not change.  The avatar is the same avatar
	// however often its session is rebuilt, and the inventory root
	// belongs to the account rather than to the session, so both are
	// read without going through ident.
	me      msg.UUID
	invRoot msg.UUID

	xfers     *xfer.Xfers
	transfers *xfer.Transfers

	mu sync.Mutex

	// materials is what Materials has read.
	materials materialCache

	// at is the visit the avatar is on: which region, and which run of
	// it, a local id means something in.  See Object and Session.local.
	at visit

	// What the simulator has said about objects.
	locals      map[msg.UUID]uint32   // object id to local id
	owners      map[msg.UUID]msg.UUID // object id to owner
	groups      map[msg.UUID]msg.UUID // object id to group, from its properties
	objectNames map[msg.UUID]string   // object id to name
	// nameAt is when each name in objectNames last arrived, so that
	// asking for a name again can wait for an answer newer than the
	// asking.  Why: doc/objects.md#a-name-a-script-changed
	nameAt map[msg.UUID]time.Time
	// reaskDone is when the last full re-ask of every name finished,
	// and reaskFlight the one in progress, if any; see askAllNamesAgain.
	reaskDone   time.Time
	reaskFlight *reaskFlight
	parents     map[uint32]uint32 // local id to parent local id
	attach      map[msg.UUID]*Attached
	killed      map[uint32]bool

	// playing is what the simulator last said was playing on THIS
	// avatar, and playingAt when it said it (zero until it has), and
	// sitOn and sitOffset are the last AvatarSitResponse it sent us.
	// See sit.go: the animations are the whole of the evidence that a
	// ground sit happened, and the offset is a detail of an object sit
	// that the reparenting does not carry.  See playing.go.
	playing   []PlayingAnimation
	playingAt time.Time
	sitOn     msg.UUID
	sitOffset msg.Vector3

	// animWatch is how many calls are holding the borrowed
	// AvatarAnimation subscription.  A count rather than a flag,
	// because there is one subscription set for the whole connection
	// and the first of two overlapping sits to finish would otherwise
	// take it away from the second.
	animWatch int

	// userInfoWatch is the same for UserInfoReply, which is borrowed
	// only while a UserInfo call waits: it carries the account's email,
	// and a session that did not ask is not sent it.
	userInfoWatch int

	// soundWatch is the same for the four sound messages (sounds.go),
	// and heard, heardSeq and loops are what they said: the log of
	// every sound heard, the number of the last, and the loops in force
	// by the prim that plays each.
	soundWatch int
	heard      []HeardSound
	heardSeq   uint64
	loops      map[msg.UUID]*LoopingSound

	// Replies keyed by what was asked.  created holds an entry only
	// while CreateItem is waiting on that callback id: nil until the
	// reply comes, and gone when the wait ends.
	created map[uint32]*msg.UpdateCreateInventoryItem_InventoryData

	// taskInv holds the filename from ReplyTaskInventory and taskSeen
	// whether a reply arrived at all.  Both are needed: an object
	// holding nothing answers with an empty filename, so waiting for a
	// non-empty one waits for ever on an empty object.
	taskInv  map[msg.UUID]string
	taskSeen map[msg.UUID]bool

	// Chat subscriptions, and the channel that adds and removes them.
	//
	// Both go through the reader goroutine so that it is the only
	// thing that ever touches a subscription's channel: the only
	// writer and the only closer.  Closing from anywhere else races
	// with a send no matter how it is locked.
	chatSubs   map[<-chan Line]*chatSub
	permSubs   map[<-chan *Permission]*permSub
	imSubs     map[<-chan *IM]*imSub
	regionSubs map[<-chan *RegionChange]*regionSub
	moneySubs  map[<-chan *Money]*moneySub
	gchatSubs  map[<-chan *GroupChat]*groupChatSub
	friendSubs map[<-chan *FriendChange]*friendSub

	// gchat is what is known of each group's chat, by group.  See
	// groupchat.go.
	gchat   map[msg.UUID]*groupChatState
	chatSeq int // how many sessions gchat has held, for their order

	// confStarts are the conferences being started, by the temporary id
	// the start was made with, until the grid's reply names the session.
	// See conference.go.
	confStarts map[msg.UUID]*confStart
	chatCtl    chan chatCmd
	readDone   chan struct{}

	// subsClosed says closeChat has been, so that a subscription asked
	// for after the session ended is handed back closed rather than
	// added to a map nobody will ever read again.  It is under mu, the
	// lock closeChat takes and every subscription is registered under,
	// which is what stops one slipping in behind it.
	subsClosed bool

	// Chat collectors, and everything heard, in arrival order.
	collectors []*collector
	alerts     []string
	propsFns   []func(*Properties)

	// profileFns are who is waiting for the three replies an
	// AvatarPropertiesRequest is answered with.  See profile.go: they
	// are called with mu held, which is what makes a profile assembled
	// from three messages safe for the caller to read.
	profileFns []func(*avatarReply)

	// mapFns are who is waiting for the blocks a MapNameRequest is
	// answered with.  See worldmap.go: they are called with mu held,
	// which is what makes an answer assembled from several packets safe
	// for the caller to read.
	mapFns []func([]msg.MapBlockReply_Data)

	// teleportFns are who is waiting to hear what became of a teleport
	// they asked for.  See teleport.go: they are called with mu held,
	// like the two above, and for the same reason -- the answer is put
	// together on the reader goroutine and read on another.
	teleportFns []func(*teleportAnswer)

	// scriptFns are who is waiting for a ScriptRunningReply.  See
	// ScriptRunning: the state is not remembered, because a script
	// starts and stops on its own and a remembered answer would be a
	// claim about the past dressed as one about now.
	scriptFns []func(object, item msg.UUID, running bool)

	// parcelFns and dwellFns are who is waiting for an answer about a
	// piece of land.  See parcel.go: a ParcelProperties carries the
	// sequence id it was asked with and the waiters sort themselves
	// out by it, because the answer arrives on the queue where nothing
	// else pairs it with its question.
	parcelFns []func(*agent.Parcel)
	dwellFns  []func(local int32, id msg.UUID, dwell float32)

	// infoFns are who is waiting for a ParcelInfoReply; see parcelinfo.go.
	infoFns []func(*ParcelInfo)

	// userInfoFns are who is waiting for a UserInfoReply; see userinfo.go.
	userInfoFns []func(msg.UUID, *UserInfo)

	// moneySeq numbers the grid's answers about L$, moneyLog is the last
	// few of them, and payWaits the payments waiting for theirs, oldest
	// first.  See money.go.
	moneySeq uint64
	moneyLog []*Money
	payWaits []*payWait

	// Permission requests waiting for an answer, in arrival order.
	// See waiting.go for how long one is kept.
	asked []*Permission

	// Who is who: names learned or asked for, the questions still
	// out, and the friendship offers waiting for an answer.
	names  map[msg.UUID]string
	asking map[msg.UUID]bool
	offers map[msg.UUID]*Offer
	// invOffers are inventory offers waiting, keyed by transaction:
	// two people may offer at once, and the transaction is what tells
	// the answers apart.
	invOffers map[msg.UUID]*InventoryOffer

	// lures are the teleport offers waiting for an answer, by whoever
	// offered.  See lure.go.
	lures map[msg.UUID]*Lure

	// tpRequests are the people asking to be teleported here, by
	// whoever asked.  See lure.go.
	tpRequests map[msg.UUID]*TeleportRequest

	// invites are the group invitations waiting for an answer, by the
	// group they are into -- which is also who they came from.  See
	// invite.go.
	invites map[msg.UUID]*Invitation

	pickers map[msg.UUID]chan []Found

	// Dialogs a script has put up, in arrival order.  Kept rather
	// than only delivered, because a dialog that appears the instant
	// a script starts would otherwise be a race nobody can win.  See
	// waiting.go for how long one is kept.
	dialogs []Dialog

	// syntax is the LSL the region implements, which is half a
	// megabyte and changes only when Linden Lab changes the language.
	syntax *Syntax

	// OnAlert, if set, is called for every AlertMessage.  Alerts are
	// how a simulator refuses something it has no reply for.
	OnAlert func(string)

	// OnDialog, if set, is called for every script dialog.  See
	// WaitDialog for the other way to catch one.
	OnDialog func(Dialog)

	// OnHandled, if set, is called when an offer this session was
	// holding is dealt with somewhere else -- by another client of
	// the same avatar, which the daemon says -- and has been dropped.
	// Not for the ones this session deals with itself.  See offers.go.
	//
	// It is also called when a dialog or a permission request is
	// dropped unanswered, for its age or to make room: see
	// UnansweredFor and MaxUnanswered.  Such a Handled has no Key and
	// is By "this session", and it may be called on the reader
	// goroutine as another arrives, or from Dialogs, Asked or
	// WaitDialog on the caller's, without the session's lock either
	// way.
	OnHandled func(Handled)

	// record is what slgod said about the offers it keeps, and nil
	// when it said nothing.  claims are the offers this session has
	// told it it is answering, and gone the ones it has said somebody
	// else answered; see offers.go for why each is needed.
	record *OfferRecord
	claims map[string]bool
	gone   map[string]bool

	// sendFn stands in for the connection, so that what a call puts
	// on the wire can be read back without a grid to put it on.  Set
	// by tests and by nothing else.
	sendFn func(msg.Message) error
}

// Dial attaches to a session slgod is holding.
//
// An empty name takes the one SLGO_AGENT names, and failing that the
// daemon's default; see Attach.  For a session this process holds
// instead, see LoginDirect; everything after that call is the same
// either way.
func Dial(ctx context.Context, addr, agentName string) (*Session, error) {
	return dial(ctx, addr, agentName, false)
}

// DialWeak is Dial for a client that ATTENDS an avatar rather than uses
// it: a daemon that sits attached all day and would not mind the
// session being taken away.  See AttachWeak.
func DialWeak(ctx context.Context, addr, agentName string) (*Session, error) {
	return dial(ctx, addr, agentName, true)
}

func dial(ctx context.Context, addr, agentName string, weak bool) (*Session, error) {
	at := Attach
	if weak {
		at = AttachWeak
	}
	h, err := at(ctx, addr, agentName, Subscriptions...)
	if err != nil {
		return nil, err
	}
	s, err := New(h)
	if err != nil {
		h.Close()
		return nil, err
	}
	return s, nil
}

// LoginDirect logs in and holds the session in this process.
//
// It also settles the active group, as slgod does at startup, since a
// parcel usually grants building to a group rather than to individuals
// and a fresh login has none active.
func LoginDirect(ctx context.Context, l agent.Login) (*Session, error) {
	d, err := Login(ctx, l)
	if err != nil {
		return nil, err
	}
	d.activeGroup(ctx)
	s, err := New(d)
	if err != nil {
		d.Close()
		return nil, err
	}
	return s, nil
}

// Lock takes exclusive use of something named, waiting until it is
// this session's, and Unlock gives it back.  See Backend.Lock.
func (w *Session) Lock(ctx context.Context, name string) error {
	return w.b.Lock(ctx, name)
}

// TryLock takes one only if it is free, and says who holds it if not.
func (w *Session) TryLock(ctx context.Context, name string) (bool, string, error) {
	return w.b.TryLock(ctx, name)
}

// Unlock gives a lock back.
func (w *Session) Unlock(name string) error { return w.b.Unlock(name) }

// New wraps a backend that is already connected.
func New(b Backend) (*Session, error) {
	info := b.Info()
	if info.AgentID.IsZero() {
		return nil, fmt.Errorf("sl: the backend gave no agent id")
	}
	if info.SessionID.IsZero() {
		return nil, fmt.Errorf("sl: the backend gave no session id")
	}

	w := &Session{
		b: b, me: info.AgentID, invRoot: info.InventoryRoot,
		at:          newVisit(),
		locals:      map[msg.UUID]uint32{},
		owners:      map[msg.UUID]msg.UUID{},
		groups:      map[msg.UUID]msg.UUID{},
		objectNames: map[msg.UUID]string{},
		nameAt:      map[msg.UUID]time.Time{},
		parents:     map[uint32]uint32{},
		attach:      map[msg.UUID]*Attached{},
		killed:      map[uint32]bool{},
		created:     map[uint32]*msg.UpdateCreateInventoryItem_InventoryData{},
		taskInv:     map[msg.UUID]string{},
		taskSeen:    map[msg.UUID]bool{},
		chatSubs:    map[<-chan Line]*chatSub{},
		permSubs:    map[<-chan *Permission]*permSub{},
		imSubs:      map[<-chan *IM]*imSub{},
		regionSubs:  map[<-chan *RegionChange]*regionSub{},
		moneySubs:   map[<-chan *Money]*moneySub{},
		gchatSubs:   map[<-chan *GroupChat]*groupChatSub{},
		friendSubs:  map[<-chan *FriendChange]*friendSub{},
		confStarts:  map[msg.UUID]*confStart{},
		names:       map[msg.UUID]string{},
		asking:      map[msg.UUID]bool{},
		offers:      map[msg.UUID]*Offer{},
		pickers:     map[msg.UUID]chan []Found{},
		chatCtl:     make(chan chatCmd),
		readDone:    make(chan struct{}),
	}
	// Before anything can send: agentBlock reads this on every message.
	w.ident.Store(info)
	w.xfers = xfer.NewXfers(b)
	w.transfers = xfer.NewTransfers(b)
	// Before the reader, so that whatever the daemon kept is here by
	// the time anybody can ask.  See offers.go.
	w.loadKept()
	go w.read(context.Background())
	return w, nil
}

// Backend is what this session runs against, for the few things that
// only one kind can do: Hosted.Sessions, Direct.Logout.  A message sent
// through it still reaches the same reader.
func (w *Session) Backend() Backend { return w.b }

// Me is the avatar's id, Session the session id, and Info who this
// session is now.
//
// Info is not what the server said at attach time.  A session that has
// been re-established under this client says something different, and
// a caller holding the old answer is holding a session id the
// simulator will not accept.
func (w *Session) Me() msg.UUID            { return w.me }
func (w *Session) Session() msg.UUID       { return w.identity().SessionID }
func (w *Session) Info() *Info             { return w.identity() }
func (w *Session) InventoryRoot() msg.UUID { return w.invRoot }

// identity is the current one, and never nil.
//
// New always stores one, so the empty answer is for a Session built by
// hand -- which the tests do.  Answering with a zero Info rather than
// panicking keeps a mistake in test scaffolding a wrong value instead
// of a crash, which is what the field it replaced did.
func (w *Session) identity() *Info {
	if i := w.ident.Load(); i != nil {
		return i
	}
	return &Info{}
}

// Close hangs up.
func (w *Session) Close() error { return w.b.Close() }

// Done closes when the session ends, and Err says why.  Both come
// straight from the backend: the session ends when the thing holding it
// does.
func (w *Session) Done() <-chan struct{} { return w.b.Done() }
func (w *Session) Err() error            { return w.b.Err() }

// Send puts a message on the wire, reliably.
func (w *Session) Send(ctx context.Context, m msg.Message) error {
	if w.sendFn != nil {
		return w.sendFn(m)
	}
	return w.b.Send(ctx, m, true)
}

// agentBlock fills the AgentID and SessionID that nearly every message
// starts with.
//
// Read through ident every time rather than from a field settled at
// attach: this is the one place the session id reaches the wire, so it
// is the one place that has to be right about which session it is.
func (w *Session) agentBlock() (msg.UUID, msg.UUID) {
	return w.me, w.identity().SessionID
}

// refreshIdentity asks the backend who this session is now, and keeps
// the answer.
//
// Called when the region changes, because a re-established session
// arrives as a region change -- the daemon says so in the detail, and
// this does not read the detail: matching on the words would be one
// more thing to be wrong about when they change.  An ordinary teleport
// gets the same identity back, with the new region's name and
// capabilities.
//
// It runs OFF the reader goroutine.  Anything that waits on the daemon
// -- or, direct, on the new region's capabilities -- from there stops
// the relay this session is reading, and a session that stops reading
// stops hearing about the very thing it is trying to react to.
func (w *Session) refreshIdentity(ctx context.Context) {
	info, err := w.b.Refresh(ctx)
	if err != nil || info == nil || info.SessionID.IsZero() {
		// Nothing to be done and nobody here to tell.  The old
		// identity is kept, which is what would have happened
		// anyway, and the next region change tries again.
		return
	}
	w.ident.Store(info)
}

// Settle waits, doing nothing, so the simulator's interest list can
// fill.
//
// A session that has just connected has been told about nothing.
// Rezzing immediately and then looking for the new object finds every
// object in the region looking new, because none of them had been
// mentioned before.
func (w *Session) Settle(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// alertsSince renders the alerts heard after a mark, for an error.
func (w *Session) alertsSince(mark int) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if mark >= len(w.alerts) {
		return ""
	}
	// Quoted into a slice of its own: the alerts are kept, and quoting
	// them where they lie would quote them again at the next timeout.
	said := make([]string, 0, len(w.alerts)-mark)
	for _, a := range w.alerts[mark:] {
		said = append(said, strconv.Quote(a))
	}
	return "; the simulator said " + strings.Join(said, ", ")
}

// Alerts returns the AlertMessage text heard so far.  A simulator that
// refuses something often says so only here.
func (w *Session) Alerts() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.alerts...)
}

// read is the one goroutine that consumes both relays.
//
// It also owns the chat subscriptions, which is why it selects rather
// than ranging: adding and removing one has to happen here, in between
// deliveries, so that nothing can be closed while a delivery is in
// flight.
//
// The circuit is what says the session is over.  The event queue ending
// is not the same thing -- a simulator answers 404 to a queue it has
// finished with while the circuit carries on -- so a closed event
// channel only stops this listening to it.  The same goes for the
// region changes, which a backend may not have at all.
func (w *Session) read(ctx context.Context) {
	msgs := w.b.Messages()
	events := w.b.Events()
	regions := w.b.RegionChanges()
	// The fourth relay, which only a daemon has: offers somebody has
	// dealt with.  Nil blocks, as for the other two that may be absent.
	var handled <-chan *Handled
	if k, ok := w.b.(OfferKeeper); ok {
		handled = k.HandledOffers()
	}
	defer func() {
		close(w.readDone)
		w.closeChat()
	}()

	for {
		select {
		case c := <-w.chatCtl:
			w.applyChat(c)

		case <-ctx.Done():
			return

		case m, ok := <-msgs:
			if !ok {
				return
			}
			if w.xfers.Handle(ctx, m) {
				continue
			}
			if w.transfers.Handle(m) {
				continue
			}
			v, err := m.Decode()
			if err != nil || v == nil {
				continue
			}
			w.handle(m, v)

		case e, ok := <-events:
			if !ok {
				// A nil channel blocks for ever, which is what stops
				// this arm spinning on a queue that has finished.
				events = nil
				continue
			}
			w.event(e)

		// The avatar is in another region, so most of what this
		// session remembers is about somewhere else.  Handled here
		// rather than by whoever teleported, because a teleport is
		// not the only way it happens: an accepted lure, a border
		// crossing, and a session re-established after the circuit
		// was lost all arrive as this and nothing else.
		case c, ok := <-regions:
			if !ok {
				regions = nil
				continue
			}
			w.regionChanged(c)

		case h, ok := <-handled:
			if !ok {
				handled = nil
				continue
			}
			w.offerHandled(h)
		}
	}
}

// eventHandlers are the event queue messages this session acts on.
//
// A map keyed by name, because that is all an event has: it arrives
// under its message name with its blocks as LLSD and no number to look
// up, which is how the viewer treats one too -- lleventpoll.cpp:110
// hands the name and the body to the same dispatch the circuit's
// messages go through.
//
// The map is so that wanting one more is a line here rather than
// another arm of a switch nobody can find.  Nothing is registered
// speculatively: an event with no reader is an event whose shape nobody
// has checked, and this package has been wrong about the shape of one
// already -- see scriptRunningEvent.
var eventHandlers = map[string]func(*Session, map[string]any){
	"ScriptRunningReply": (*Session).scriptRunningEvent,

	// Both halves of a teleport's answer arrive here rather than on the
	// circuit.  The template says so of the first and not of the second,
	// and stage 0 watched both come off the queue.
	"TeleportFinish": (*Session).teleportFinishEvent,
	"TeleportFailed": (*Session).teleportFailedEvent,

	// The land under the avatar, which arrives here whether it was
	// asked for or not.  See parcel.go.
	"ParcelProperties": (*Session).parcelEvent,

	// A group's chat: an invitation, the answer to joining, a refusal, who
	// came and went, and being put out.  Their shapes are the viewer's
	// handlers' and have not been seen on a grid; see doc/group-chat.md.
	"ChatterBoxInvitation":              (*Session).chatInvitationEvent,
	"ChatterBoxSessionStartReply":       (*Session).chatStartReplyEvent,
	"ChatterBoxSessionEventReply":       (*Session).chatEventReplyEvent,
	"ChatterBoxSessionAgentListUpdates": (*Session).chatAgentListEvent,
	"ForceCloseChatterBoxSession":       (*Session).chatForceCloseEvent,
}

// event dispatches one entry from the event queue.
//
// Anything unknown is dropped without complaint.  A hosted session is
// handed whatever the daemon was asked to relay, which is a longer list
// than this reads, and a direct one is handed the whole queue.
func (w *Session) event(e *QueueEvent) {
	fn := eventHandlers[e.Name]
	if fn == nil {
		return
	}
	m, err := e.Decode()
	if err != nil || m == nil {
		return
	}
	fn(w, m)
}

// scriptRunningEvent reads a ScriptRunningReply that came over the event
// queue, which on Second Life is the only place it comes from.
//
// Two things about the body are load bearing here.  The Script block
// arrives as an ARRAY of maps where the template declares a single
// block, so it is read as one and a lone map is accepted too, since
// which of them a grid sends is not this package's to insist on.  And
// the maps carry fields the template has never had -- Mono, and Luau and
// LuauLanguage, which Agni had added by August 2026 -- so only the three
// fields wanted are read and everything else goes past unlooked at.
// Why: doc/scripts.md#what-agni-does
//
// A block whose ids will not parse is passed on as zeroes rather than
// guarded against, because zero cannot match: a question is asked about
// an item that came out of an object's contents, and the answer is
// matched on both ids.  A guard here would be a branch nothing could
// ever reach through.
func (w *Session) scriptRunningEvent(m map[string]any) {
	for _, b := range llsdBlocks(m, "Script") {
		w.scriptRunning(
			parseUUIDOrZero(llsd.String(b, "ObjectID")),
			parseUUIDOrZero(llsd.String(b, "ItemID")),
			llsd.Bool(b, "Running"))
	}
}

// llsdBlocks is one named block of an event body, as the list of maps a
// message block always is.
//
// Liberal on purpose.  A block declared Single in the template still
// arrives inside an array here, and a block declared Multiple obviously
// does, so the array is the shape to expect -- but a grid that sent the
// bare map would otherwise be silently read as nothing at all, which is
// the failure that looks exactly like a message never arriving.
func llsdBlocks(m map[string]any, name string) []map[string]any {
	switch v := m[name].(type) {
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, e := range v {
			if b := llsd.Map(e); b != nil {
				out = append(out, b)
			}
		}
		return out
	case map[string]any:
		return []map[string]any{v}
	}
	return nil
}

func (w *Session) handle(raw *client.Message, v msg.Message) {
	switch t := v.(type) {
	case *msg.ObjectUpdate:
		w.mu.Lock()
		for i := range t.ObjectData {
			o := &t.ObjectData[i]
			w.locals[o.FullID] = o.ID
			w.parents[o.ID] = o.ParentID
			delete(w.killed, o.ID)
			w.updateSound(o)
			if item, ok := attachItem(o.NameValue); ok {
				w.attach[item] = &Attached{
					Object: Object{ID: o.FullID, Local: o.ID, from: w.at},
					Item:   item,
					Point:  attachPoint(o.State),
				}
			}
		}
		w.mu.Unlock()

	case *msg.KillObject:
		w.mu.Lock()
		for _, d := range t.ObjectData {
			w.killed[d.ID] = true
			w.objectGone(d.ID)
			// A worn attachment that the region kills is no longer
			// worn: a script's llDetachFromAvatar, or our own take-off.
			// A teleport sends no kill for it and describes it again
			// under a new local id, which replaces the entry.
			// Why: doc/outfit.md#an-attachment-that-takes-itself-off
			for item, at := range w.attach {
				if at.Object.Local == d.ID {
					delete(w.attach, item)
				}
			}
		}
		w.mu.Unlock()

	case *msg.SoundTrigger:
		w.soundTrigger(t)
	case *msg.AttachedSound:
		w.attachedSound(t)
	case *msg.AttachedSoundGainChange:
		w.attachedSoundGain(t)
	case *msg.PreloadSound:
		w.preloadSound(t)

	case *msg.ParcelInfoReply:
		w.parcelInfoReply(parcelInfoFrom(t))

	case *msg.UserInfoReply:
		w.userInfoReply(t.AgentData.AgentID, userInfoFrom(t))

	case *msg.ParcelDwellReply:
		w.dwellReply(t.Data.LocalID, t.Data.ParcelID, t.Data.Dwell)

	case *msg.ObjectPropertiesFamily:
		w.mu.Lock()
		w.owners[t.ObjectData.ObjectID] = t.ObjectData.OwnerID
		w.groups[t.ObjectData.ObjectID] = t.ObjectData.GroupID
		w.objectNames[t.ObjectData.ObjectID] = trimNul(t.ObjectData.Name)
		w.nameAt[t.ObjectData.ObjectID] = time.Now()
		w.mu.Unlock()

	case *msg.ObjectProperties:
		var out []*Properties
		w.mu.Lock()
		for i := range t.ObjectData {
			o := &t.ObjectData[i]
			w.owners[o.ObjectID] = o.OwnerID
			w.groups[o.ObjectID] = o.GroupID
			w.objectNames[o.ObjectID] = trimNul(o.Name)
			w.nameAt[o.ObjectID] = time.Now()
			out = append(out, &Properties{
				Object: o.ObjectID, Name: trimNul(o.Name),
				Description: trimNul(o.Description),
				Creator:     o.CreatorID, Owner: o.OwnerID, Group: o.GroupID,
				// CreationDate is microseconds.  Read as seconds it
				// dates a fresh prim to the year 56 million.
				Created:       time.UnixMicro(int64(o.CreationDate)),
				BaseMask:      o.BaseMask,
				OwnerMask:     o.OwnerMask,
				GroupMask:     o.GroupMask,
				EveryoneMask:  o.EveryoneMask,
				NextOwnerMask: o.NextOwnerMask,
				SaleType:      o.SaleType, SalePrice: o.SalePrice,
				InventorySerial: o.InventorySerial,
			})
		}
		fns := make([]func(*Properties), len(w.propsFns))
		copy(fns, w.propsFns)
		w.mu.Unlock()
		for _, p := range out {
			for _, fn := range fns {
				if fn != nil {
					fn(p)
				}
			}
		}

	case *msg.ScriptRunningReply:
		// Kept for a grid that still answers on the circuit.  Second
		// Life does not -- see ScriptRunning -- and the arm that does
		// the work there is scriptRunningEvent.
		w.scriptRunning(t.Script.ObjectID, t.Script.ItemID, t.Script.Running)

	// A teleport inside this region, which the simulator does itself and
	// announces on the circuit.  It is the whole answer to a lure to
	// somewhere nearby: there is no finish for one of those and waiting
	// for one waits for the timeout.
	case *msg.TeleportLocal:
		w.teleportAnswered(&teleportAnswer{local: true})

	// These two are the queue's on Second Life -- see eventHandlers --
	// and these arms are for a grid that still sends them on the
	// circuit, as the ScriptRunningReply arm above is.
	case *msg.TeleportFinish:
		w.teleportAnswered(&teleportAnswer{handle: t.Info.RegionHandle})

	case *msg.TeleportFailed:
		w.teleportAnswered(&teleportAnswer{failed: true, refusal: agent.ReadTeleportFailed(t)})

	// Kept only for a creation this session is waiting on.  The reply
	// to another client's comes here too, since slgod relays it to all
	// of them, and so does one that arrives after its wait gave up.
	case *msg.UpdateCreateInventoryItem:
		w.mu.Lock()
		for i := range t.InventoryData {
			d := t.InventoryData[i]
			if _, waiting := w.created[d.CallbackID]; waiting {
				w.created[d.CallbackID] = &d
			}
		}
		w.mu.Unlock()

	case *msg.ReplyTaskInventory:
		w.mu.Lock()
		w.taskInv[t.InventoryData.TaskID] = trimNul(t.InventoryData.Filename)
		w.taskSeen[t.InventoryData.TaskID] = true
		w.mu.Unlock()

	// This avatar's own animation list, and nobody else's.  The message
	// arrives for every avatar in range and keeping the crowd's would be
	// keeping a list that grows with the region; the agent's animations,
	// in agent/posture.go, make the same choice and say more about it.
	// The list is replaced rather than merged, because the message is the
	// whole of it every time and an animation that has stopped is simply
	// absent from the next one -- which is the only way standing up from
	// a ground sit is ever heard about.
	case *msg.AvatarAnimation:
		if t.Sender.ID == w.me {
			list := playingOf(t)
			w.mu.Lock()
			w.playing, w.playingAt = list, time.Now()
			w.mu.Unlock()
		}

	// Where the simulator put us on the thing we asked to sit on.  It is
	// not what a sit waits for -- the reparenting is, and it arrives
	// whether or not this does -- so this is kept and never blocked on.
	case *msg.AvatarSitResponse:
		w.mu.Lock()
		w.sitOn = t.SitObject.ID
		w.sitOffset = t.SitTransform.SitPosition
		w.mu.Unlock()

	case *msg.ChatFromSimulator:
		w.chat(raw, t)

	// What this avatar said out loud, echoed back by slgod because
	// another client of the same session said it.  The grid never
	// sends this one inward, so it is always an echo; see
	// Session.saidElsewhere.
	case *msg.ChatFromViewer:
		w.saidElsewhere(raw, t)

	case *msg.ScriptDialog:
		w.dialog(raw, t)

	case *msg.ScriptQuestion:
		w.permission(t)

	case *msg.ImprovedInstantMessage:
		w.instantMessage(raw, t)

	case *msg.MoneyBalanceReply:
		w.moneyReply(raw, t)

	case *msg.UUIDNameReply:
		w.nameReply(t)

	// A friend coming or going, which a viewer announces; see
	// friendnote.go.  The agent keeps who is online on its own.
	case *msg.OnlineNotification:
		ids := make([]msg.UUID, 0, len(t.AgentBlock))
		for _, b := range t.AgentBlock {
			ids = append(ids, b.AgentID)
		}
		w.friendsChanged(ids, true)

	case *msg.OfflineNotification:
		ids := make([]msg.UUID, 0, len(t.AgentBlock))
		for _, b := range t.AgentBlock {
			ids = append(ids, b.AgentID)
		}
		w.friendsChanged(ids, false)

	case *msg.AvatarPickerReply:
		w.pickerReply(t)

	// The three answers to one AvatarPropertiesRequest.  Which avatar
	// each is about is in its own AgentData block rather than in
	// anything the request left behind, so that is what a waiter sifts
	// them by; see profile.go.
	case *msg.AvatarPropertiesReply:
		w.avatarReplyTo(&avatarReply{Avatar: t.AgentData.AvatarID, Props: t})

	case *msg.AvatarInterestsReply:
		w.avatarReplyTo(&avatarReply{Avatar: t.AgentData.AvatarID, Interests: t})

	case *msg.AvatarGroupsReply:
		w.avatarReplyTo(&avatarReply{Avatar: t.AgentData.AvatarID, Groups: t})

	// One name asked about is answered with as many of these as it
	// takes, so the blocks go to whoever asked and the list is put back
	// together there; see worldmap.go.
	case *msg.MapBlockReply:
		w.mapBlocks(t.Data)

	case *msg.AlertMessage:
		s := trimNul(t.AlertData.Message)
		w.mu.Lock()
		w.alerts = append(w.alerts, s)
		fn := w.OnAlert
		w.mu.Unlock()
		if fn != nil {
			fn(s)
		}
	}
}

// scriptRunning hands an answer about a script to whoever asked for it,
// whichever relay it came in on.
//
// Delivered rather than stored.  Whether a script is running is a fact
// with a moment attached -- a script stops itself, and another client
// may start one -- so the answer belongs to the question it answers and
// to nothing else.
func (w *Session) scriptRunning(object, item msg.UUID, running bool) {
	w.mu.Lock()
	fns := make([]func(msg.UUID, msg.UUID, bool), len(w.scriptFns))
	copy(fns, w.scriptFns)
	w.mu.Unlock()
	for _, fn := range fns {
		if fn != nil {
			fn(object, item, running)
		}
	}
}

// await polls a condition until it holds, the deadline passes, or the
// context is cancelled.
//
// Polling rather than signalling is deliberate.  What is being waited
// for usually arrives as several messages that have to agree with each
// other -- an object update and a properties reply, say -- and a
// predicate over the accumulated state says that plainly where a
// condition variable per fact would not.
//
// It is for what the session has been told.  What has to be asked for,
// a round trip each time, is poll's.
func (w *Session) await(ctx context.Context, timeout time.Duration, what string, ok func() bool) error {
	deadline := time.Now().Add(timeout)

	// Where the alert log stood when the wait began, so a timeout can
	// quote what the simulator said while we were waiting.
	//
	// A refusal usually arrives as an AlertMessage and NOT as a reply to
	// the thing refused, so without this every refusal is indistinguishable
	// from a slow simulator: the caller is told only that nothing happened,
	// which is the one fact that never explains anything.
	w.mu.Lock()
	mark := len(w.alerts)
	w.mu.Unlock()

	for {
		w.mu.Lock()
		done := ok()
		w.mu.Unlock()
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s (after %s)%s", ErrTimeout, what, timeout, w.alertsSince(mark))
		}
		t := time.NewTimer(100 * time.Millisecond)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
}

// poll asks read until it holds, pausing every between asks.  It is how
// a call here confirms what the grid does not answer: by looking.
//
// It stops early for two things.  The caller giving up returns
// ctx.Err(), and the deadline passing returns ErrTimeout naming what
// was waited for.  A read that fails is asked again, and a timeout
// whose last read failed says why.  A read that holds is believed even
// if ctx was cancelled meanwhile, since the thing did happen.
//
// The mistake it prevents is the loop of read, check the deadline,
// time.Sleep, go round, ignoring the read's error.  A cancelled read
// fails at once, or, like Direct's Presence, never looks at ctx, so that
// loop sleeps on to its own deadline and then reports a timeout, which
// is neither true nor quick.  So ctx is checked here after every read,
// not left to the read.  TestNothingInThisPackageSleeps refuses the
// loop.
//
// await is its neighbour, for what the session has already been told.
func poll(ctx context.Context, timeout, every time.Duration, what string,
	read func(context.Context) (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := read(ctx)
		if ok {
			return nil
		}
		if gone := ctx.Err(); gone != nil {
			return gone
		}
		if time.Now().After(deadline) {
			// A read's own ErrTimeout is its way of saying "not yet",
			// and is not worth quoting.
			var last string
			if err != nil && !errors.Is(err, ErrTimeout) {
				last = fmt.Sprintf("; the last look failed: %v", err)
			}
			return fmt.Errorf("%w: %s (after %s)%s", ErrTimeout, what, timeout, last)
		}
		t := time.NewTimer(every)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
}

// lastLookFor bounds lastLook's one read.
const lastLookFor = 3 * time.Second

// lastLook is for a call that asked for something to be made and whose
// poll has failed.  If the caller gave up, what was asked for may exist
// by now, and the caller has to be told of it to clean it up -- as Build
// returns what it made -- so this reads once more, on a context the
// cancel does not reach, and reports whether the read held.  After a
// timeout it reports false without reading: poll has only just looked.
func lastLook(ctx context.Context, read func(context.Context) (bool, error)) bool {
	if ctx.Err() == nil {
		return false
	}
	look, cancel := context.WithTimeout(context.WithoutCancel(ctx), lastLookFor)
	defer cancel()
	ok, _ := read(look)
	return ok
}

func trimNul(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}

// capDo runs a capability request and insists on a 2xx.
func (w *Session) capDo(ctx context.Context, r agent.CapRequest) ([]byte, error) {
	resp, err := w.b.DoCap(ctx, r)
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		name := r.Cap
		if name == "" {
			name = r.URL
		}
		return nil, &CapError{What: name, Status: resp.Status, Body: snippet(resp.Body)}
	}
	return resp.Body, nil
}

// A CapError is a capability that answered with a status rather than
// with what was asked for.
//
// The status is kept rather than only printed because what to do about
// one depends on it: a 5xx is the far end having a bad moment and may
// be worth asking again, where a 4xx is this end having asked for
// something it will not get however often it asks.
//
// What is named is the CAPABILITY for the first half of an upload and
// the URL for the second, since that is all the request carries -- which
// is also how a reader can tell which half of a two step upload failed.
type CapError struct {
	// What is the capability's name, or the URL when the request went
	// to one directly.
	What string

	// Status is the HTTP status it answered with.
	Status int

	// Body is the beginning of what came back, for a person to read.
	Body string
}

func (e *CapError) Error() string {
	return fmt.Sprintf("sl: %s: status %d: %s", e.What, e.Status, e.Body)
}

// Temporary is whether asking again might do better: the far end
// failed, rather than refusing.
func (e *CapError) Temporary() bool { return e.Status >= 500 }

func snippet(b []byte) string {
	const n = 300
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}
