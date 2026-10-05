package slate

// A grid that is not there.
//
// Run drives an sl.Session, and a session is a function of an sl.Backend,
// so the runner is tested against a backend that answers from fields and
// records what was sent. The fakes in sl, slsh and slbotd are unexported
// or in test files of other packages; this one is trimmed from them. A
// message the test relays travels the path it would from the grid: it is
// decoded by the session's reader and delivered to the subscriptions.

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var _ sl.Backend = (*fakeGrid)(nil)

// Invented ids, made with tools/new-id.
var (
	testMe       = msg.MustParseUUID("112e7e57-7e57-c0de-4677-7c7c8af4f04a")
	testSession  = msg.MustParseUUID("4f127e57-7e57-c0de-a2a6-2f8e224733d0")
	testRegion   = msg.MustParseUUID("5f5e7e57-7e57-c0de-2bef-e55b67d8b14f")
	testInvRoot  = msg.MustParseUUID("99267e57-7e57-c0de-55c2-b0e23ccf1e53")
	idSign       = msg.MustParseUUID("a12e7e57-7e57-c0de-1f13-2165984b7290")
	idVendor     = msg.MustParseUUID("af547e57-7e57-c0de-72fa-1a6791ece834")
	idVisitor    = msg.MustParseUUID("b47f7e57-7e57-c0de-3b33-1df8af60d806")
	idStray      = msg.MustParseUUID("edab7e57-7e57-c0de-68b6-2bab00446989")
	idStranger   = msg.MustParseUUID("f9fe7e57-7e57-c0de-3d7b-b112648c1e1d")
	barrierMsgID = msg.MakeID(msg.FreqLow, 65530)
)

const testAvatar = "Quark Idlemind"

// fakeGrid is an sl.Backend with nothing behind it. Every field is read
// under the lock.
type fakeGrid struct {
	mu sync.Mutex

	info     *sl.Info
	msgs     chan *sl.Message
	done     chan struct{}
	doneOnce sync.Once
	// beforeClose runs first in Close: whatever sends on msgs must be
	// finished before msgs is closed (see relayUntil). It is not a
	// t.Cleanup because session registers Close as one, and cleanups run
	// last in, first out.
	beforeClose []func()

	sent   []msg.Message
	onSend func(msg.Message) // called after a send is recorded, without the lock

	objects []*sl.Seen
	region  *sl.Region

	linkOrder   map[uint32][]msg.UUID // root local id -> its children in the store's link order (setLinkOrder)
	linkUnknown map[uint32]bool       // root local id -> the set's order is not known (unknownLinks)

	group     msg.UUID    // the tester's active group, in Presence
	groups    []sl.Group  // the groups the avatar has joined, in Presence
	sess      *sl.Session // the last session made
	controls  []uint32    // the control flags the session asked for
	onControl func(flags uint32)
	reply     func(tx msg.UUID, ok bool, bal int, said string, typ int, from, to msg.UUID, amount int, echo string) *msg.MoneyBalanceReply

	assets map[string][]byte // texture bytes by id, served at the asset capability
	ex     fakeExtra         // changes over time, properties, offers, the inventory (fake_expect_test.go)
}

// newGrid makes a grid with two prims: a sign the tester owns and a
// vendor a stranger owns.
func newGrid(t *testing.T) *fakeGrid {
	t.Helper()
	f := &fakeGrid{
		info: &sl.Info{
			Name: "fake", AgentID: testMe, SessionID: testSession,
			AvatarName: testAvatar, Region: "Test Region", InventoryRoot: testInvRoot,
		},
		msgs:   make(chan *sl.Message),
		done:   make(chan struct{}),
		region: &sl.Region{ID: testRegion, Name: "Test Region", Handle: 1099511628032},
		objects: []*sl.Seen{
			at(prim(idSign, 101, "Example Sign", testMe), 130, 128, 25),
			at(prim(idVendor, 102, "Example Tip Jar", idStranger), 128, 133, 25),
			avatar(),
		},
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// plainTE is what a prim that has been described carries: a touch reads
// it, and one without it asks the region again and waits for the answer.
var plainTE = func() []byte {
	te, err := sl.EncodeTextureEntry(sl.PlainFaces(6))
	if err != nil {
		panic(err)
	}
	return te
}()

// prim is a plain box of six faces, described as the grid describes one;
// withShape and asSculpt change what it presents.
func prim(id msg.UUID, local uint32, name string, owner msg.UUID) *sl.Seen {
	s := &sl.Seen{
		Object: sl.Object{ID: id, Local: local, Name: name},
		Owner:  owner, PCode: 9, TextureEntry: plainTE,
	}
	withShape(sl.DefaultShape())(s)
	return s
}

// avatar is the tester's own body, standing at the middle of the region. It is
// named, which the real one is not, so that AllObjects has nothing to wait for.
func avatar() *sl.Seen {
	return at(&sl.Seen{Object: sl.Object{ID: testMe, Local: 1, Name: testAvatar}, PCode: 47}, 128, 128, 22)
}

// at puts a prim at a position, and returns it.
func at(s *sl.Seen, x, y, z float32) *sl.Seen {
	s.Position = msg.Vector3{X: x, Y: y, Z: z}
	return s
}

// child makes s a child of root's linkset.
func child(s, root *sl.Seen) *sl.Seen {
	s.Parent = root.Local
	return s
}

// session is a real Session over the fake, its reader running.
func (f *fakeGrid) session(t *testing.T) *sl.Session {
	t.Helper()
	w, err := sl.New(f)
	if err != nil {
		t.Fatalf("sl.New: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	f.mu.Lock()
	f.sess = w
	f.mu.Unlock()
	return w
}

// sentOf is the messages of one type the session sent, in order.
func sentOf[T msg.Message](f *fakeGrid) []T {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []T
	for _, m := range f.sent {
		if x, ok := m.(T); ok {
			out = append(out, x)
		}
	}
	return out
}

// relay hands a message to the session as if the grid had sent it, and
// returns once the reader has finished with it: the message is followed
// by one the template does not have, which the reader discards, and the
// reader takes one at a time.
func (f *fakeGrid) relay(m msg.Message) error { return f.relayUntil(m, nil) }

// relayUntil is relay that also gives up when stop closes, so that a
// simulator goroutine blocked here can be stopped before the grid is
// closed. Closing f.msgs while a send on it is in flight is a data race
// (and a panic), so whatever sends must be finished by then.
func (f *fakeGrid) relayUntil(m msg.Message, stop <-chan struct{}) error {
	body, err := m.Encode()
	if err != nil {
		return err
	}
	for _, raw := range []*sl.Message{
		{ID: msg.IDOf(m), Name: m.MsgInfo().Name, Body: body, At: time.Now()},
		{ID: barrierMsgID, Name: "relay barrier", At: time.Now()},
	} {
		select {
		case f.msgs <- raw:
		case <-f.done:
			return errors.New("the fake grid is closed")
		case <-stop:
			return errors.New("the simulator is stopped")
		case <-time.After(5 * time.Second):
			return errors.New("nothing read the relay")
		}
	}
	return nil
}

// chat is one heard line. A type of sl.ChatSay is open chat.
func chatMsg(from string, source msg.UUID, kind uint8, text string) *msg.ChatFromSimulator {
	m := &msg.ChatFromSimulator{}
	d := &m.ChatData
	d.FromName = append([]byte(from), 0)
	d.SourceID = source
	d.OwnerID = idStranger
	d.SourceType = sl.SourceObject
	d.ChatType = kind
	d.Audible = 1
	d.Message = append([]byte(text), 0)
	return m
}

// dialogMsg is a script dialog from an object.
func dialogMsg(object msg.UUID, name, message string, channel int32, buttons ...string) *msg.ScriptDialog {
	m := &msg.ScriptDialog{}
	m.Data.ObjectID = object
	m.Data.ObjectName = append([]byte(name), 0)
	m.Data.FirstName = append([]byte("Example"), 0)
	m.Data.LastName = append([]byte("Resident"), 0)
	m.Data.Message = append([]byte(message), 0)
	m.Data.ChatChannel = channel
	for _, b := range buttons {
		m.Buttons = append(m.Buttons, msg.ScriptDialog_Buttons{ButtonLabel: append([]byte(b), 0)})
	}
	m.OwnerData = []msg.ScriptDialog_OwnerData{{OwnerID: idStranger}}
	return m
}

// quietly relays from a goroutine of the test's, which may not Fatal.
func (f *fakeGrid) quietly(t *testing.T, d time.Duration, m msg.Message) {
	t.Helper()
	timer := time.AfterFunc(d, func() { f.relay(m) })
	t.Cleanup(func() { timer.Stop() })
}

// says is what the tester said in a message the session sent, and on
// which channel. A negative channel goes as a script dialog reply.
func says(m msg.Message) (string, int32, bool) {
	switch x := m.(type) {
	case *msg.ChatFromViewer:
		return strings.TrimRight(string(x.ChatData.Message), "\x00"), x.ChatData.Channel, true
	case *msg.ScriptDialogReply:
		return strings.TrimRight(string(x.Data.ButtonLabel), "\x00"), x.Data.ChatChannel, true
	}
	return "", 0, false
}

// replyTo makes the far end answer: fn is called for each message the
// session sends, from the sending goroutine, without the lock.
func (f *fakeGrid) replyTo(fn func(msg.Message)) {
	f.mu.Lock()
	f.onSend = fn
	f.mu.Unlock()
}

// said is the text of each say sent, in order.
func (f *fakeGrid) said() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		if text, _, ok := says(m); ok {
			out = append(out, text)
		}
	}
	return out
}

func (f *fakeGrid) replies() []*msg.ScriptDialogReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*msg.ScriptDialogReply
	for _, m := range f.sent {
		if r, ok := m.(*msg.ScriptDialogReply); ok {
			out = append(out, r)
		}
	}
	return out
}

// ----------------------------------------------------------- sl.Backend

func (f *fakeGrid) Info() *sl.Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info
}

func (f *fakeGrid) Refresh(context.Context) (*sl.Info, error) { return f.Info(), nil }

func (f *fakeGrid) Send(ctx context.Context, m msg.Message, reliable bool) error {
	f.mu.Lock()
	f.sent = append(f.sent, m)
	fn := f.onSend
	f.mu.Unlock()
	f.afterSend(m)
	if fn != nil {
		fn(m)
	}
	// After the test's own answer, so that one is heard first.
	f.answerNames(m)
	return nil
}

// answerNames answers a request for names the way a simulator does,
// from the names the fake holds: a root to a family request, a child to
// a selection.  A root is not answered when selected: that is how a
// test reads its properties, and a test that has the properties never
// come answers them itself.  A lookup by name asks again for names that are
// already known and waits for an answer, so a fake that never answered
// would cost every lookup the quiet rounds.
// Why: doc/objects.md#a-name-a-script-changed
func (f *fakeGrid) answerNames(m msg.Message) {
	var replies []msg.Message
	f.mu.Lock()
	switch m := m.(type) {
	case *msg.RequestObjectPropertiesFamily:
		for _, o := range f.objects {
			if o.ID == m.ObjectData.ObjectID && o.Parent == 0 && !o.IsAvatar() && o.Name != "" {
				r := &msg.ObjectPropertiesFamily{}
				r.ObjectData.ObjectID, r.ObjectData.OwnerID = o.ID, o.Owner
				r.ObjectData.Name = append([]byte(o.Name), 0)
				replies = append(replies, r)
			}
		}
	case *msg.ObjectSelect:
		for _, d := range m.ObjectData {
			for _, o := range f.objects {
				if o.Local == d.ObjectLocalID && o.Parent != 0 && !o.IsAvatar() && o.Name != "" {
					r := &msg.ObjectProperties{ObjectData: []msg.ObjectProperties_ObjectData{{
						ObjectID: o.ID, OwnerID: o.Owner, Name: append([]byte(o.Name), 0),
					}}}
					replies = append(replies, r)
				}
			}
		}
	}
	f.mu.Unlock()
	for _, r := range replies {
		if f.relay(r) != nil {
			return
		}
	}
}

func (f *fakeGrid) Control(_ context.Context, flags uint32) error {
	f.mu.Lock()
	f.controls = append(f.controls, flags)
	fn := f.onControl
	f.mu.Unlock()
	if fn != nil {
		fn(flags)
	}
	return nil
}
func (f *fakeGrid) Messages() <-chan *sl.Message           { return f.msgs }
func (f *fakeGrid) Events() <-chan *sl.QueueEvent          { return nil }
func (f *fakeGrid) RegionChanges() <-chan *sl.RegionChange { return nil }
func (f *fakeGrid) Done() <-chan struct{}                  { return f.done }
func (f *fakeGrid) Err() error                             { return nil }
func (f *fakeGrid) SimAttachments(context.Context, msg.UUID) (*sl.SimAttachments, error) {
	return nil, nil
}

func (f *fakeGrid) Presence(context.Context, float32) (*sl.Presence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &sl.Presence{Position: msg.Vector3{X: 128, Y: 128, Z: 25}, ActiveGroup: f.group, Groups: f.groups}, nil
}

func (f *fakeGrid) Objects(ctx context.Context, named, id string) ([]*sl.Seen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Copies, as the session's own are: a test changes the prim while the
	// runner reads what it was given.
	all := make([]*sl.Seen, len(f.objects))
	for i, o := range f.objects {
		c := *o
		all[i] = &c
	}
	f.numberLinks(all)
	var out []*sl.Seen
	for _, o := range all {
		if id != "" && !strings.EqualFold(o.ID.String(), id) {
			continue
		}
		if named != "" && o.Name != named {
			continue
		}
		out = append(out, o)
	}
	return out, nil
}

// numberLinks gives each prim the link number and LinkKnown the store
// would: 0 alone, 1 for a root with children, and 2 and up for a child in
// the order of its parent's children in f.objects, or in the order a test
// set with setLinkOrder. A set a test marked with unknownLinks is not
// known.
// Why: doc/objects.md#link-numbers
func (f *fakeGrid) numberLinks(all []*sl.Seen) {
	kids := map[uint32][]*sl.Seen{}
	byLocal := map[uint32]*sl.Seen{}
	for _, o := range all {
		byLocal[o.Local] = o
		if o.Parent != 0 && o.PCode == pcodePrim {
			kids[o.Parent] = append(kids[o.Parent], o)
		}
	}
	for p, ks := range kids {
		order := f.linkOrder[p]
		at := func(s *sl.Seen) int {
			for i, id := range order {
				if id == s.ID {
					return i
				}
			}
			return len(order)
		}
		sort.SliceStable(ks, func(i, j int) bool { return at(ks[i]) < at(ks[j]) })
	}
	for _, o := range all {
		if o.PCode != pcodePrim {
			continue
		}
		set := o.Local // the root of the linkset the prim is in
		if par := byLocal[o.Parent]; par != nil && par.PCode == pcodePrim {
			set = par.Local
		}
		o.LinkKnown = !f.linkUnknown[set]
		switch {
		case o.Parent != 0:
			for i, k := range kids[o.Parent] {
				if k.ID == o.ID {
					o.LinkNumber = i + 2
				}
			}
		case len(kids[o.Local]) > 0:
			o.LinkNumber = 1
		}
	}
}

// setLinkOrder has the store number the children of root in the order
// given, which a test uses to give a set an order other than the one a
// probe in it would report.
func (f *fakeGrid) setLinkOrder(root *sl.Seen, children ...*sl.Seen) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.linkOrder == nil {
		f.linkOrder = map[uint32][]msg.UUID{}
	}
	f.linkOrder[root.Local] = nil
	for _, c := range children {
		f.linkOrder[root.Local] = append(f.linkOrder[root.Local], c.ID)
	}
}

// unknownLinks makes the order of root's linkset not known, as a set
// that several prims joined in one update is, or known again.
func (f *fakeGrid) unknownLinks(root *sl.Seen, unknown bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.linkUnknown == nil {
		f.linkUnknown = map[uint32]bool{}
	}
	f.linkUnknown[root.Local] = unknown
}

func (f *fakeGrid) Region(context.Context) (*sl.Region, bool, error) { return f.region, true, nil }
func (f *fakeGrid) SimStats(context.Context) (*sl.SimStats, error)   { return nil, nil }

func (f *fakeGrid) LastRegionDetails(context.Context) (*sl.RegionDetails, uint64, error) {
	return nil, 0, nil
}
func (f *fakeGrid) Land(context.Context) (*sl.Land, error) { return nil, nil }
func (f *fakeGrid) Ground(context.Context, float32, float32, float32, float32) (float32, bool, error) {
	return 0, false, nil
}
func (f *fakeGrid) Neighbours(context.Context, *bool) (*sl.Neighbours, error) {
	return &sl.Neighbours{}, nil
}
func (f *fakeGrid) Lock(context.Context, string) error { return nil }
func (f *fakeGrid) Unlock(string) error                { return nil }
func (f *fakeGrid) TryLock(context.Context, string) (bool, string, error) {
	return true, "", nil
}
func (f *fakeGrid) Flush(context.Context) (int, error)               { return 0, nil }
func (f *fakeGrid) Friends(context.Context) ([]sl.Friend, error)     { return nil, nil }
func (f *fakeGrid) NoteFriend(context.Context, msg.UUID, bool) error { return nil }
func (f *fakeGrid) HasCap(name string) bool                          { return f.hasAssetCap(name) || f.hasCap(name) }
func (f *fakeGrid) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	if resp, ok := f.assetCap(r); ok {
		return resp, nil
	}
	return f.doCap(ctx, r)
}

// Close ends the session, which stops the reader and closes every
// subscription. Closing twice is not an error.
func (f *fakeGrid) Close() error {
	f.doneOnce.Do(func() {
		for _, fn := range f.beforeClose {
			fn()
		}
		close(f.msgs)
		close(f.done)
	})
	return nil
}

// ------------------------------------------------------- running a script

// testCfg is the runner's pacing for a test: short polls, and budgets
// shorter than anything a script asks for.
func testCfg() runCfg {
	return runCfg{lookup: 200 * time.Millisecond, props: 150 * time.Millisecond, chatDepth: 256, poll: 5 * time.Millisecond,
		objects: 10 * time.Millisecond, redescribe: 60 * time.Millisecond, click: 300 * time.Millisecond,
		settle: 40 * time.Millisecond, inv: 20 * time.Millisecond}
}

// play parses and checks src, and runs it against the grid.
func play(t *testing.T, f *fakeGrid, src string) *Result {
	t.Helper()
	return playWith(t, f, src, Options{}, testCfg())
}

func playWith(t *testing.T, f *fakeGrid, src string, opt Options, cfg runCfg) *Result {
	t.Helper()
	res, err := tryPlay(t, f, src, opt, cfg)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, res.Transcript)
	}
	return res
}

func tryPlay(t *testing.T, f *fakeGrid, src string, opt Options, cfg runCfg) (*Result, error) {
	t.Helper()
	s := mustCheck(t, src)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return run(ctx, f.session(t), s, opt, cfg)
}

var stampRE = regexp.MustCompile(`(?m)^\d\d:\d\d:\d\d\.\d{3} `)

// lines is the transcript with the times taken off.
func lines(res *Result) []string {
	return strings.Split(strings.TrimRight(stampRE.ReplaceAllString(res.Transcript, ""), "\n"), "\n")
}

// has reports whether a line of the transcript contains want.
func has(res *Result, want string) bool {
	for _, l := range lines(res) {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

func mustHave(t *testing.T, res *Result, want ...string) {
	t.Helper()
	for _, w := range want {
		if !has(res, w) {
			t.Errorf("transcript has no line containing %q:\n%s", w, res.Transcript)
		}
	}
}

func mustNotHave(t *testing.T, res *Result, bad string) {
	t.Helper()
	if has(res, bad) {
		t.Errorf("transcript has a line containing %q:\n%s", bad, res.Transcript)
	}
}

func wantExit(t *testing.T, res *Result, exit int) {
	t.Helper()
	if res.Exit != exit {
		t.Fatalf("exit = %d, want %d:\n%s", res.Exit, exit, res.Transcript)
	}
}
