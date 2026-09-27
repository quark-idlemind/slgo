package sl

// A grid that is not there.
//
// Everything in this package reaches the world through Backend, so a
// fake one is the difference between testing the package and testing
// almost none of it.  Without a backend a test can only build a bare
// Session by hand and call the private handlers; Send, the reader
// goroutine, and every call that asks the grid a question are out of
// reach.  That is why so much of this package was only ever exercised
// with SLGO_TEST_ADDR set -- not because the code needs a grid, but
// because the tests had no way to pretend to be one.
//
// fakeBackend answers from fields instead of from a simulator, and
// records what was sent.  newFakeSession wraps one in a real Session
// through New, so the reader goroutine is running and a relayed
// message travels the path it would from the grid: decoded, handled,
// delivered to subscriptions.
//
// It complements newTestSession in chat_test.go rather than replacing
// it.  That one builds the subscription machinery and nothing else,
// which is all a test of the subscription machinery should depend on;
// this one is for anything that has to reach the far end.

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

var (
	_ Backend = (*fakeBackend)(nil)
	_ Watcher = (*fakeBackend)(nil)
)

// Who the fake says we are.  New refuses a backend with no agent or
// session id, so these are load bearing rather than decoration.
var (
	testAgentID   = msg.MustParseUUID("3ac37e57-7e57-c0de-5607-527da8fa08de")
	testSessionID = msg.MustParseUUID("72427e57-7e57-c0de-39d2-e78c47465eb6")
	testRegionID  = msg.MustParseUUID("a4fd7e57-7e57-c0de-559f-7a9b7da6044a")
	testInvRoot   = msg.MustParseUUID("23077e57-7e57-c0de-245c-d6b83f1a8b6d")
)

// sentMessage is one message the session put on the wire, with the
// flag it asked for: reliable and unreliable are different promises,
// and a call that gets it wrong is worth catching.
type sentMessage struct {
	Msg      msg.Message
	Reliable bool
}

// fakeBackend is a Backend with nothing behind it.
//
// Build one with newFake, or newFakeSession for one already wrapped in
// a Session.  Every field is read under the lock, so a test may change
// an answer while the session is running -- a friend list that grows, a
// Send that starts failing -- without racing the reader.
type fakeBackend struct {
	mu sync.Mutex

	info *Info

	// refreshed counts the asks and refreshErr makes one fail, for
	// the tests about a session that was rebuilt underneath.
	refreshed  int
	refreshErr error

	// msgs is the relay, and is unbuffered on purpose; see Relay.
	// events is the other relay, and is unbuffered for the same
	// reason: what arrives on the grid's event queue rather than on
	// the circuit.  See RelayEvent.  regions is the third, unbuffered
	// for the third time: see RelayRegion.
	msgs     chan *Message
	events   chan *QueueEvent
	regions  chan *RegionChange
	done     chan struct{}
	doneOnce sync.Once
	err      error

	// sent is everything the session put on the wire, in order, and
	// noted is every friendship it recorded through NoteFriend.
	sent  []sentMessage
	noted []Friend

	// sendErr, when set, is what Send answers with, and nothing is
	// recorded.  This is how the other half of every call that sends
	// something -- the half where the message never went -- is
	// reached.
	sendErr error

	// onSend, when set, is called with each message after it is
	// recorded and without the lock held.  A test that wants the far
	// end to answer relays the answer from here, which is what a
	// simulator does; see AnswerNames.
	//
	// It must not be used to answer something the reader goroutine
	// itself sends: relaying from inside such a call would have the
	// reader waiting for itself.
	onSend func(msg.Message)

	// scripts is whether each script is running, as a simulator would
	// know it: SetScriptRunning sets it, and GetScriptRunning is answered
	// from it -- not running, for a script never set -- unless onSend is
	// answering instead or quietScripts says the region says nothing.
	// t is what the answer is relayed with.
	scripts      map[msg.UUID]bool
	quietScripts bool
	t            *testing.T

	presence    *Presence
	presenceErr error

	// afterPresence and afterObjects, when set, are called with the
	// lock held once each answer has been made.  A region that changes
	// between one look and the next is staged from here, where no look
	// can be half way through.
	afterPresence func()
	afterObjects  func()

	region      *Region
	land        *Land
	landErr     error
	regionKnown bool
	regionErr   error

	// ground answers Ground, for a rectangle in the region; unset, no
	// land has arrived.
	ground    func(west, south, east, north float32) (float32, bool)
	groundErr error

	// neighbours is what the far end holds, and it is changed by a
	// set the way a real backend changes it: turning them off drops
	// what is held rather than only refusing the next offer, which is
	// the half of the behaviour a caller can see from here.
	neighbours    Neighbours
	neighboursErr error

	// objectsErrs are answered first, one to an ask, and objectsErr
	// after them to every ask.
	objects     []*Seen
	objectsErrs []error
	objectsErr  error

	friends    []Friend
	friendsErr error
	noteErr    error
	flushErr   error

	// locks is what has been taken, and lockedBy is who TryLock
	// should say holds one rather than handing it over.
	locks    map[string]bool
	lockedBy string
	lockErr  error

	// caps maps a capability name to the base URL serving it; see
	// ServeCap.
	caps   map[string]string
	capErr error

	// controls is every set of control flags Control was asked for, in
	// order, and controlErr is what it answers with instead.  A slice
	// rather than a union, because the flags are edge triggered: two
	// stands are two events and a test that could not tell them apart
	// would not notice one going missing.
	controls   []uint32
	controlErr error

	// watching is what the relay has been asked for since the session
	// started, and watched is the order Watch and Unwatch were called
	// in.  Both are kept: what is subscribed NOW is what decides
	// whether a message arrives, and the order is what says a borrowed
	// subscription was given back rather than never taken.
	watching map[string]bool
	watched  []string
	watchErr error
}

// newFake builds a backend that answers plausibly and reaches nothing.
//
// The answers are the ones a session standing in a region would get,
// because most calls ask for a few of them on the way past -- an
// instant message carries the position it was sent from -- and a test
// about something else should not have to say so.
func newFake(t *testing.T) *fakeBackend {
	t.Helper()
	f := &fakeBackend{
		info: &Info{
			Name:          "fake",
			AgentID:       testAgentID,
			SessionID:     testSessionID,
			AvatarName:    "Quark Idlemind",
			Region:        "Test Region",
			InventoryRoot: testInvRoot,
			Channel:       "slgo test 1.0",
		},
		t:        t,
		scripts:  map[msg.UUID]bool{},
		msgs:     make(chan *Message),
		events:   make(chan *QueueEvent),
		regions:  make(chan *RegionChange),
		done:     make(chan struct{}),
		locks:    map[string]bool{},
		caps:     map[string]string{},
		watching: map[string]bool{},
		presence: &Presence{
			Position: msg.Vector3{X: 128, Y: 128, Z: 25},
			LookAt:   msg.Vector3{X: 1},
			Camera:   msg.Vector3{X: 128, Y: 128, Z: 26},
			Region:   "Test Region",
		},
		region:      &Region{ID: testRegionID, Name: "Test Region", Handle: 1099511628032},
		regionKnown: true,
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// newFakeSession is a real Session over a fake backend, with the
// reader goroutine running.
//
// Going through New rather than filling a Session in by hand is the
// point: the maps New builds and the ones it leaves nil are part of
// what the session does, and a test that built its own would be
// testing a session that does not exist.
func newFakeSession(t *testing.T) (*Session, *fakeBackend) {
	t.Helper()
	f := newFake(t)
	w, err := New(f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Waiting for the reader makes a goroutine left running a failure
	// here, rather than a puzzle three tests later.
	t.Cleanup(func() {
		f.Close()
		select {
		case <-w.readDone:
		case <-time.After(5 * time.Second):
			t.Error("the reader goroutine did not stop when the session ended")
		}
	})
	return w, f
}

// foundHere marks an object as found in the region the session is in
// now, as one that came back from ObjectsNamed would be, and hands it
// back.
//
// Most tests here are about what a call sends for an object rather than
// about finding it.  An Object built by hand is looked up by its id
// before its local id is sent, and this fake's region has described
// nothing; see TestALocalIDIsTheRegionsItCameFrom for the looking up.
func foundHere(w *Session, o *Object) *Object {
	o.from = w.here(context.Background())
	return o
}

// barrierID is a message number the template does not have.  See
// Relay.
var barrierID = msg.MakeID(msg.FreqLow, 65530)

// TestTheRelayBarrierIsNotAMessage: Relay works only because the
// reader throws the barrier away.  If the template ever grows this
// number, every Relay in the package would quietly be feeding the
// session a second message nobody asked for.
func TestTheRelayBarrierIsNotAMessage(t *testing.T) {
	if info := msg.Lookup(barrierID); info != nil {
		t.Fatalf("the relay barrier is %s, which the session would act on", info.Name)
	}
}

// Relay hands a message to the session as though the grid had sent it,
// and returns once the reader has finished with it.
//
// The waiting is the point.  A test that relays something and then
// asserts on what the session did with it is otherwise racing the
// reader, and it is a race the test usually wins -- which is worse
// than losing it, because it fails once a month on somebody else's
// machine instead.  So the message is followed by a number the
// template does not have, which the reader takes and discards: the
// reader handles one message at a time, so its taking the second means
// it has finished the first.
func (f *fakeBackend) Relay(t *testing.T, m msg.Message) {
	t.Helper()
	body, err := m.Encode()
	if err != nil {
		t.Fatalf("encoding %s: %v", m.MsgInfo().Name, err)
	}
	f.RelayRaw(t, &Message{
		ID: msg.IDOf(m), Name: m.MsgInfo().Name, Body: body, At: time.Now(),
	})
}

// RelayRaw is Relay for a message whose bytes the test built itself,
// which is what a test of the decoding wants.
func (f *fakeBackend) RelayRaw(t *testing.T, raw *Message) {
	t.Helper()
	f.put(t, raw)
	f.put(t, &Message{ID: barrierID, Name: "slgo relay barrier", At: time.Now()})
}

// RelayEvent hands one entry to the session as though it had come off
// the grid's event queue, and returns once the reader has finished with
// it.
//
// The body is written as LLSD text rather than built from a struct,
// because that is the only thing an event ever is: there is no template
// for it and no generated type, so what a test asserts against has to be
// the shape a live grid actually sent.  See
// doc/scripts.md#whether-a-script-is-running for the one that was
// captured on Agni.
//
// The barrier is Relay's, for Relay's reason: the reader takes one thing
// at a time, whichever relay it came from, so its taking the message
// after means it has finished with the event.
func (f *fakeBackend) RelayEvent(t *testing.T, name, body string) {
	t.Helper()
	select {
	case f.events <- &QueueEvent{Name: name, Body: []byte(body), At: time.Now()}:
	case <-time.After(5 * time.Second):
		t.Fatalf("nothing read the event relay: is a session attached to this backend?")
	}
	f.put(t, &Message{ID: barrierID, Name: "slgo relay barrier", At: time.Now()})
}

// RelayRegion tells the session the avatar is in another region, as the
// daemon does when it has followed a teleport, and returns once the
// reader has finished with it.
//
// The barrier is Relay's and is here for Relay's reason, and it earns
// its keep twice over here: everything a region change does happens on
// the reader goroutine -- the forgetting as much as the delivery -- so a
// test asserting on what was dropped without it is asserting on a
// session that may not have been told yet.
func (f *fakeBackend) RelayRegion(t *testing.T, region string, handle uint64) {
	t.Helper()
	select {
	case f.regions <- &RegionChange{Region: region, Handle: handle}:
	case <-time.After(5 * time.Second):
		t.Fatalf("nothing read the region relay: is a session attached to this backend?")
	}
	f.put(t, &Message{ID: barrierID, Name: "slgo relay barrier", At: time.Now()})
}

func (f *fakeBackend) put(t *testing.T, raw *Message) {
	t.Helper()
	select {
	case f.msgs <- raw:
	case <-time.After(5 * time.Second):
		t.Fatalf("nothing read the relay: is a session attached to this backend?")
	}
}

// AnswerNames makes the fake reply to UUIDNameRequest the way a
// simulator does, from a table of who is who.
//
// Several calls ask for names and then wait for them -- FriendList and
// Nearby both do -- so a backend that never answers turns each of them
// into a three second pause and a list of "(3ac37e57)".
func (f *fakeBackend) AnswerNames(t *testing.T, names map[msg.UUID]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		q, ok := m.(*msg.UUIDNameRequest)
		if !ok {
			return
		}
		r := &msg.UUIDNameReply{}
		for _, b := range q.UUIDNameBlock {
			n, ok := names[b.ID]
			if !ok {
				continue
			}
			first, last, _ := strings.Cut(n, " ")
			r.UUIDNameBlock = append(r.UUIDNameBlock, msg.UUIDNameReply_UUIDNameBlock{
				ID:        b.ID,
				FirstName: append([]byte(first), 0),
				LastName:  append([]byte(last), 0),
			})
		}
		if len(r.UUIDNameBlock) > 0 {
			f.Relay(t, r)
		}
	}
}

// ServeInventory answers the inventory reads a folder listing makes.
//
// FolderItems does not go over the wire at all -- inventory is AIS, an
// ordinary GET returning LLSD -- so everything built on it, which is
// Take and Worn and EnsureAttached, needs a folder to read rather than
// a message to relay.  contents is asked afresh for every request, so a
// folder that is empty until something has been taken into it can say
// so.
func (f *fakeBackend) ServeInventory(t *testing.T, contents func(folder msg.UUID) []*Item) {
	t.Helper()
	f.ServeInventoryTree(t, func(id msg.UUID) ([]*Folder, []*Item) {
		return nil, contents(id)
	})
}

// ServeInventoryTree is ServeInventory for a test that needs folders in
// the answer as well as items.
//
// Finding a folder by name is a read of the root's categories -- Folder,
// ObjectsFolder and TrashFolder are all that and nothing else -- so a
// reply whose categories are always empty makes every one of them say
// there is no such folder.
func (f *fakeBackend) ServeInventoryTree(t *testing.T, contents func(folder msg.UUID) ([]*Folder, []*Item)) {
	t.Helper()
	f.ServeCap(t, agent.InventoryCap, func(w http.ResponseWriter, r *http.Request) {
		id := capFolderID(r.URL.Path)
		folders, items := contents(id)
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, folderLLSDTree(id, folders, items))
	})
}

// capFolderID is the folder an inventory request is about, out of a
// path of the form /category/<folder>/children.
func capFolderID(path string) msg.UUID {
	var id msg.UUID
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if p == "category" && i+1 < len(parts) {
			id, _ = msg.ParseUUID(parts[i+1])
		}
	}
	return id
}

// folderLLSD is what AIS says about one folder: the folder itself, and
// its items under _embedded.  Only the fields anything here reads are
// filled in.
func folderLLSD(folder msg.UUID, items []*Item) string {
	return folderLLSDTree(folder, nil, items)
}

// folderLLSDTree is folderLLSD with child categories as well.
func folderLLSDTree(folder msg.UUID, folders []*Folder, items []*Item) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" ?><llsd><map>`)
	fmt.Fprintf(&b, `<key>category_id</key><string>%s</string>`, folder)
	fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string>`, testInvRoot)
	b.WriteString(`<key>name</key><string>a folder</string>`)
	b.WriteString(`<key>type_default</key><integer>-1</integer>`)
	b.WriteString(`<key>version</key><integer>1</integer>`)
	b.WriteString(`<key>_embedded</key><map><key>categories</key><map>`)
	for _, sub := range folders {
		fmt.Fprintf(&b, `<key>%s</key><map>`, sub.ID)
		fmt.Fprintf(&b, `<key>category_id</key><string>%s</string>`, sub.ID)
		fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string>`, folder)
		fmt.Fprintf(&b, `<key>name</key><string>%s</string>`, xmlText(sub.Name))
		fmt.Fprintf(&b, `<key>type_default</key><integer>%d</integer>`, sub.Type)
		b.WriteString(`<key>version</key><integer>1</integer></map>`)
	}
	b.WriteString(`</map><key>items</key><map>`)
	for _, it := range items {
		fmt.Fprintf(&b, `<key>%s</key><map>`, it.ID)
		fmt.Fprintf(&b, `<key>item_id</key><string>%s</string>`, it.ID)
		fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string>`, folder)
		fmt.Fprintf(&b, `<key>asset_id</key><string>%s</string>`, it.AssetID)
		fmt.Fprintf(&b, `<key>name</key><string>%s</string>`, xmlText(it.Name))
		fmt.Fprintf(&b, `<key>desc</key><string>%s</string>`, xmlText(it.Desc))
		fmt.Fprintf(&b, `<key>type</key><integer>%d</integer>`, it.Type)
		fmt.Fprintf(&b, `<key>inv_type</key><integer>%d</integer>`, it.InvType)
		fmt.Fprintf(&b, `<key>flags</key><integer>%d</integer>`, it.Flags)
		b.WriteString(`<key>permissions</key><map>`)
		fmt.Fprintf(&b, `<key>group_id</key><uuid>%s</uuid>`, it.GroupID)
		fmt.Fprintf(&b, `<key>group_mask</key><integer>%d</integer>`, it.GroupMask)
		fmt.Fprintf(&b, `<key>everyone_mask</key><integer>%d</integer>`, it.EveryoneMask)
		fmt.Fprintf(&b, `<key>next_owner_mask</key><integer>%d</integer>`, it.NextOwnerMask)
		b.WriteString(`</map></map>`)
	}
	b.WriteString(`</map><key>links</key><map/></map></map></llsd>`)
	return b.String()
}

func xmlText(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// ServeCap points a capability at an http server that lives as long as
// the test.
//
// Everything here that reaches a capability goes through DoCap, so
// this is how those calls are exercised: httptest listens on loopback,
// which is not the network and does not need one.
func (f *fakeBackend) ServeCap(t *testing.T, name string, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	f.mu.Lock()
	f.caps[name] = s.URL
	f.mu.Unlock()
	return s
}

// ---------------------------------------------------------- assertions

// Sent is everything the session has put on the wire, in order.
func (f *fakeBackend) Sent() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

// Forget drops the record of what was sent, so a test making several
// calls can assert on each without counting from the beginning.
func (f *fakeBackend) Forget() {
	f.mu.Lock()
	f.sent = nil
	f.mu.Unlock()
}

// FailSends makes every Send from now on answer with err and record
// nothing.  This is how the other half of every call that sends
// something -- the half where the message never went -- is reached.
func (f *fakeBackend) FailSends(err error) {
	f.mu.Lock()
	f.sendErr = err
	f.mu.Unlock()
}

// Noted is every friendship the session recorded through NoteFriend.
func (f *fakeBackend) Noted() []Friend {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Friend(nil), f.noted...)
}

// describe names everything sent, for a failure that has to say what
// went out instead of what was wanted.
func (f *fakeBackend) describe() string {
	var names []string
	for _, s := range f.Sent() {
		names = append(names, s.Msg.MsgInfo().Name)
	}
	if len(names) == 0 {
		return "nothing"
	}
	return strings.Join(names, ", ")
}

// aside runs a call that cannot return until the session has been told
// something, and hands back a wait for its answer.
//
// The test goroutine is the only one that may Relay -- Relay fails the
// test when nothing reads it, and a failure from anywhere else is a
// panic rather than a failure -- so a call that waits for the simulator
// has to be the one that moves aside.  Everything here that waits does:
// Rez waits for the region to describe the prim, Wear for it to say the
// attachment went on.
func aside[T any](t *testing.T, fn func() (T, error)) (wait func() (T, error)) {
	t.Helper()
	type answer struct {
		v   T
		err error
	}
	done := make(chan answer, 1)
	go func() {
		v, err := fn()
		done <- answer{v, err}
	}()
	return func() (T, error) {
		t.Helper()
		select {
		case a := <-done:
			return a.v, a.err
		case <-time.After(60 * time.Second):
			var zero T
			t.Fatalf("the call set aside never returned: it is still waiting " +
				"to be told something the test did not relay")
			return zero, nil
		}
	}
}

// asideErr is aside for the calls that answer with an error alone,
// which is most of the ones that change something.
func asideErr(t *testing.T, fn func() error) (wait func() error) {
	t.Helper()
	w := aside(t, func() (struct{}, error) { return struct{}{}, fn() })
	return func() error {
		t.Helper()
		_, err := w()
		return err
	}
}

// waitFor polls until something holds, and fails the test if it never
// does.  It is for watching a call that is running aside: what it has
// done so far is only visible from outside.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitSent waits until a message of one kind has gone out, and answers
// with the first of them.  A test driving a call that is running aside
// has to know it has got as far as asking before it answers.
func waitSent[T msg.Message](t *testing.T, f *fakeBackend) T {
	t.Helper()
	return waitSentN[T](t, f, 1)
}

// waitSentN is waitSent for the nth of them.  A call that asks the same
// question twice has to be told apart from one that has only asked once
// -- SetName asks what an object is called after every attempt, and
// answering its first question as though it were its second confirms a
// rename that has not happened yet.
func waitSentN[T msg.Message](t *testing.T, f *fakeBackend, n int) T {
	t.Helper()
	var got []T
	waitFor(t, fmt.Sprintf("message %d of a kind the call has not sent yet", n), func() bool {
		got = sentOf[T](f)
		return len(got) >= n
	})
	return got[n-1]
}

// sentOf returns the messages of one kind, which is what an assertion
// usually wants: not what went out, but what ImprovedInstantMessages
// went out.
func sentOf[T msg.Message](f *fakeBackend) []T {
	var out []T
	for _, s := range f.Sent() {
		if m, ok := s.Msg.(T); ok {
			out = append(out, m)
		}
	}
	return out
}

// sentLocked is whether a message of one kind has gone out, for a hook
// that runs with the fake's lock already held; see afterObjects.
func sentLocked[T msg.Message](f *fakeBackend) bool {
	for _, s := range f.sent {
		if _, ok := s.Msg.(T); ok {
			return true
		}
	}
	return false
}

// onlySent is sentOf insisting on exactly one.  Most calls send one
// message, and a test that saw two was watching something else happen.
func onlySent[T msg.Message](t *testing.T, f *fakeBackend) T {
	t.Helper()
	got := sentOf[T](f)
	if len(got) != 1 {
		var zero T
		t.Fatalf("%d messages of type %T went out, want 1; all of them: %s",
			len(got), zero, f.describe())
	}
	return got[0]
}

// ------------------------------------------------------------- backend

func (f *fakeBackend) Info() *Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info
}

// Refresh hands back whatever the test has set since, which is how a
// re-established session is staged: Reidentify, then deliver a region
// change.
func (f *fakeBackend) Refresh(context.Context) (*Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed++
	if f.refreshErr != nil {
		return nil, f.refreshErr
	}
	return f.info, nil
}

// Reidentify is the daemon having rebuilt the session underneath: the
// same avatar, a new session id.
func (f *fakeBackend) Reidentify(sess msg.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	next := *f.info
	next.SessionID = sess
	f.info = &next
}

// Refreshes is how many times the session asked who it is.
func (f *fakeBackend) Refreshes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshed
}

func (f *fakeBackend) Send(ctx context.Context, m msg.Message, reliable bool) error {
	f.mu.Lock()
	err, onSend, quiet := f.sendErr, f.onSend, f.quietScripts
	var answer *msg.ScriptRunningReply
	if err == nil {
		f.sent = append(f.sent, sentMessage{Msg: m, Reliable: reliable})
		switch q := m.(type) {
		case *msg.SetScriptRunning:
			f.scripts[q.Script.ItemID] = q.Script.Running
		case *msg.GetScriptRunning:
			if onSend == nil && !quiet {
				answer = scriptRunningReply(q.Script.ObjectID, q.Script.ItemID, f.scripts[q.Script.ItemID])
			}
		}
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if onSend != nil {
		onSend(m)
	}
	if answer != nil {
		f.Relay(f.t, answer)
	}
	return nil
}

// Control records the flags a one-shot AgentUpdate was asked to carry.
//
// Nothing is sent and nothing is decoded: the update is built at the far
// end, by whoever owns the camera, so the flags are the whole of what a
// session can be held to here.
func (f *fakeBackend) Control(ctx context.Context, flags uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.controlErr != nil {
		return f.controlErr
	}
	f.controls = append(f.controls, flags)
	return nil
}

// Watch and Unwatch keep the subscription set a real daemon would keep,
// so that a borrowed subscription can be seen being taken and given
// back.
func (f *fakeBackend) Watch(names ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.watchErr != nil {
		return f.watchErr
	}
	for _, n := range names {
		f.watching[n] = true
		f.watched = append(f.watched, "+"+n)
	}
	return nil
}

func (f *fakeBackend) Unwatch(names ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range names {
		delete(f.watching, n)
		f.watched = append(f.watched, "-"+n)
	}
	return nil
}

// Controls is every set of control flags the session asked for.
func (f *fakeBackend) Controls() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint32(nil), f.controls...)
}

// Watched is the subscription changes in order, each name prefixed with
// + for a Watch and - for an Unwatch.
func (f *fakeBackend) Watched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.watched...)
}

// Watching reports whether a name is subscribed at this moment.
func (f *fakeBackend) Watching(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.watching[name]
}

func (f *fakeBackend) Messages() <-chan *Message           { return f.msgs }
func (f *fakeBackend) Events() <-chan *QueueEvent          { return f.events }
func (f *fakeBackend) RegionChanges() <-chan *RegionChange { return f.regions }
func (f *fakeBackend) Done() <-chan struct{}               { return f.done }

func (f *fakeBackend) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// SimAttachments: this fake has never heard an appearance.
func (f *fakeBackend) SimAttachments(ctx context.Context, avatar msg.UUID) (*SimAttachments, error) {
	return nil, nil
}

func (f *fakeBackend) Presence(ctx context.Context, drawDistance float32) (*Presence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.presenceErr != nil {
		return nil, f.presenceErr
	}
	if f.presence == nil {
		return &Presence{}, nil
	}
	// A draw distance above zero sets it, as the real ones do, so a
	// caller reading the answer back sees what it asked for.
	if drawDistance > 0 {
		f.presence.DrawDistance = drawDistance
	}
	p := *f.presence
	if f.afterPresence != nil {
		f.afterPresence()
	}
	return &p, nil
}

// Objects filters the way the real backends do, so that a caller
// asking for one object does not have to sift the region itself.
func (f *fakeBackend) Objects(ctx context.Context, named, id string) ([]*Seen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.objectsErrs) > 0 {
		err := f.objectsErrs[0]
		f.objectsErrs = f.objectsErrs[1:]
		return nil, err
	}
	if f.objectsErr != nil {
		return nil, f.objectsErr
	}
	out := make([]*Seen, 0, len(f.objects))
	for _, o := range f.objects {
		if id != "" && !strings.EqualFold(o.ID.String(), id) {
			continue
		}
		if named != "" && o.Name != named {
			continue
		}
		out = append(out, o)
	}
	if f.afterObjects != nil {
		f.afterObjects()
	}
	return out, nil
}

func (f *fakeBackend) Neighbours(ctx context.Context, set *bool) (*Neighbours, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.neighboursErr != nil {
		return nil, f.neighboursErr
	}
	if set != nil {
		f.neighbours.On = *set
		if !*set {
			f.neighbours.Held = nil
		}
	}
	n := f.neighbours
	n.Held = append([]Neighbour(nil), f.neighbours.Held...)
	return &n, nil
}

func (f *fakeBackend) Region(ctx context.Context) (*Region, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.regionErr != nil {
		return nil, false, f.regionErr
	}
	return f.region, f.regionKnown, nil
}

func (f *fakeBackend) Land(ctx context.Context) (*Land, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.landErr != nil {
		return nil, f.landErr
	}
	if f.land == nil {
		return &Land{Overlay: agent.OverlayFrom(nil, 0)}, nil
	}
	return f.land, nil
}

func (f *fakeBackend) Ground(ctx context.Context, west, south, east, north float32) (float32, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.groundErr != nil {
		return 0, false, f.groundErr
	}
	if f.ground == nil {
		return 0, false, nil
	}
	h, known := f.ground(west, south, east, north)
	return h, known, nil
}

func (f *fakeBackend) Lock(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockErr != nil {
		return f.lockErr
	}
	f.locks[name] = true
	return nil
}

func (f *fakeBackend) Unlock(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.locks, name)
	return nil
}

func (f *fakeBackend) TryLock(ctx context.Context, name string) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockErr != nil {
		return false, "", f.lockErr
	}
	if f.lockedBy != "" {
		return false, f.lockedBy, nil
	}
	f.locks[name] = true
	return true, "", nil
}

// Flush empties what the backend was holding and says how much that
// was, which is what makes a second call answer zero.
func (f *fakeBackend) Flush(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.flushErr != nil {
		return 0, f.flushErr
	}
	n := len(f.objects)
	f.objects = nil
	return n, nil
}

func (f *fakeBackend) Friends(ctx context.Context) ([]Friend, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.friendsErr != nil {
		return nil, f.friendsErr
	}
	return append([]Friend(nil), f.friends...), nil
}

func (f *fakeBackend) NoteFriend(ctx context.Context, id msg.UUID, online bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noteErr != nil {
		return f.noteErr
	}
	f.noted = append(f.noted, Friend{ID: id, Online: online})
	f.friends = append(f.friends, Friend{ID: id, Online: online})
	return nil
}

func (f *fakeBackend) HasCap(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.caps[name]
	return ok
}

func (f *fakeBackend) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	f.mu.Lock()
	err, base := f.capErr, f.caps[r.Cap]
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}

	url := r.URL
	if url == "" {
		if base == "" {
			return nil, fmt.Errorf("fake: no %s capability", r.Cap)
		}
		url = base + r.Path
	}
	method := r.Method
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(r.Body))
	if err != nil {
		return nil, err
	}
	if r.Type != "" {
		req.Header.Set("Content-Type", r.Type)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &agent.CapResponse{Status: resp.StatusCode, Body: body}, nil
}

// EndEvents closes the event relay and nothing else, which is a
// simulator that has finished with a queue while the circuit carries on
// -- it answers 404 to the next poll and the session is not over.
func (f *fakeBackend) EndEvents() { close(f.events) }

// Close ends the session, which is what stops the reader and closes
// every subscription.  Closing twice is not an error, since the test
// cleanup and the test itself may both do it.
//
// The event relay is left open on purpose.  The circuit ending is what
// ends a session, so closing events here would say nothing extra and
// would race EndEvents, which a test may have called already.
func (f *fakeBackend) Close() error {
	f.doneOnce.Do(func() {
		close(f.msgs)
		close(f.done)
	})
	return nil
}

// failSendsAfter makes every send from the one after a message of this
// kind onwards fail.
//
// A test that watches for a message and then calls FailSends is racing
// the call it is driving, which is usually already several messages
// further on.  This trips on the way past instead, so the failure lands
// at exactly the step being tested.
func failSendsAfter[T msg.Message](f *fakeBackend, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		if _, ok := m.(T); ok {
			f.FailSends(err)
		}
	}
}
