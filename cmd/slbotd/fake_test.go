package main

// A grid that is not there.
//
// Every command in this package is a function of a *sl.Session, and a
// session is a function of an sl.Backend, so a fake backend is the
// difference between testing the commands and testing their flag
// parsing.  This one is deliberately smaller than slsh's: it answers
// the questions the commands under test ask and refuses the rest,
// because a fake that answered everything would be a second
// implementation of the grid and would need tests of its own.
//
// Inventory it does only as far as a listing.  That arrives over AIS,
// which is HTTP against a capability, and a fake for all of it is an
// httptest server and a folder tree -- worth having in slsh, where the
// inventory commands are the bulk of the program, and not here, where
// they are a thin layer over calls sl already tests.  The inventory
// commands are tested for what this package adds to them: how a path is
// resolved to one entry, what is refused, and what is said of something
// made by a command given up on.  For that last, a test can give the
// fake folders to list.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
	"github.com/quark-idlemind/slgo/sl"
)

// Who the fake says everybody is.  Invented, like every identifier in
// this repository: nothing here is anybody's.
var (
	testMe        = msg.MustParseUUID("3ac37e57-7e57-c0de-5607-527da8fa08de")
	testSessionID = msg.MustParseUUID("72427e57-7e57-c0de-39d2-e78c47465eb6")
	testRoot      = msg.MustParseUUID("23077e57-7e57-c0de-245c-d6b83f1a8b6d")
	testSender    = msg.MustParseUUID("88987e57-7e57-c0de-6aa2-1d9b0f5a04db")
	testStranger  = msg.MustParseUUID("19d17e57-7e57-c0de-11be-a4325a5080a2")
	testLamp      = msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000001")
)

var _ sl.Backend = (*fakeGrid)(nil)

// fakeGrid is an sl.Backend with nothing behind it.
type fakeGrid struct {
	mu sync.Mutex

	info     *sl.Info
	messages chan *sl.Message
	notices  chan *pb.AgentEvent
	done     chan struct{}
	closed   bool

	// sent is every message the session put on the wire, which is how
	// a test reads what a command did.
	sent []msg.Message

	presence *sl.Presence
	region   *sl.Region
	objects  []*sl.Seen
	parcel   *agent.Parcel

	// folders, when a test sets it, is what the inventory capability
	// lists: each folder's contents, by the folder's id.
	folders map[msg.UUID][]fakeEntry

	// onSend, when set, is the grid acting on a message sent.  It is
	// called outside the lock.
	onSend func(msg.Message)
}

// fakeEntry is one thing in a folder the fake lists.
type fakeEntry struct {
	ID     msg.UUID
	Name   string
	Folder bool
	Type   int // an item's asset type, and a folder's preferred type when not zero
}

// testNotecards is the folder withNotecardsFolder makes, the default
// home of the notecard every offering() offers.
var testNotecards = msg.MustParseUUID("c9247e57-7e57-c0de-15e9-2ca5fedc4942")

// withNotecardsFolder gives the fake a Notecards folder under the root,
// which an accept with no folder named looks for, as a viewer's does.
func withNotecardsFolder(f *fakeGrid) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.folders = map[msg.UUID][]fakeEntry{testRoot: {
		{ID: testNotecards, Name: "Notecards", Folder: true, Type: int(sl.AssetNotecard)},
	}}
}

func newFakeGrid() *fakeGrid {
	return &fakeGrid{
		info: &sl.Info{
			Name:          "example",
			AgentID:       testMe,
			SessionID:     testSessionID,
			AvatarName:    "Example Resident",
			Region:        "Nowhere",
			InventoryRoot: testRoot,
		},
		messages: make(chan *sl.Message, 16),
		notices:  make(chan *pb.AgentEvent, 8),
		done:     make(chan struct{}),
		presence: &sl.Presence{
			Region:   "Nowhere",
			Position: msg.Vector3{X: 128, Y: 64, Z: 25},
		},
		region: &sl.Region{Name: "Nowhere", Access: sl.AccessGeneral, WaterHeight: 20},
	}
}

// Sent is what the session has put on the wire so far.
func (f *fakeGrid) Sent() []msg.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]msg.Message(nil), f.sent...)
}

// IMsSent is the instant messages among them, in order, leaving out the
// typing notifications.
//
// Those are not things said.  A test waiting for what an avatar
// ANSWERED should not be satisfied by it announcing that it was about
// to; see pace.go, where the pause before a reply is spent looking like
// somebody typing.  typingSent has them for the one test that is about
// them.
func (f *fakeGrid) IMsSent() []*msg.ImprovedInstantMessage {
	var out []*msg.ImprovedInstantMessage
	for _, im := range f.allIMsSent() {
		switch im.MessageBlock.Dialog {
		case sl.DialogTypingStart, sl.DialogTypingStop:
		default:
			out = append(out, im)
		}
	}
	return out
}

// typingSent is how many times the far end was told somebody was
// writing, and had stopped.
func (f *fakeGrid) typingSent() (starts, stops int) {
	for _, im := range f.allIMsSent() {
		switch im.MessageBlock.Dialog {
		case sl.DialogTypingStart:
			starts++
		case sl.DialogTypingStop:
			stops++
		}
	}
	return starts, stops
}

func (f *fakeGrid) allIMsSent() []*msg.ImprovedInstantMessage {
	var out []*msg.ImprovedInstantMessage
	for _, m := range f.Sent() {
		if im, ok := m.(*msg.ImprovedInstantMessage); ok {
			out = append(out, im)
		}
	}
	return out
}

func (f *fakeGrid) Info() *sl.Info { return f.info }

// Refresh is Backend's; nothing here rebuilds a session underneath, so
// the identity it hands back is the one it has.
func (f *fakeGrid) Refresh(context.Context) (*sl.Info, error) { return f.Info(), nil }

func (f *fakeGrid) Send(ctx context.Context, m msg.Message, reliable bool) error {
	f.mu.Lock()
	f.sent = append(f.sent, m)
	onSend := f.onSend
	f.mu.Unlock()
	if onSend != nil {
		onSend(m)
	}
	return nil
}

func (f *fakeGrid) Control(ctx context.Context, flags uint32) error { return nil }

func (f *fakeGrid) Messages() <-chan *sl.Message           { return f.messages }
func (f *fakeGrid) Events() <-chan *sl.QueueEvent          { return nil }
func (f *fakeGrid) RegionChanges() <-chan *sl.RegionChange { return nil }
func (f *fakeGrid) Done() <-chan struct{}                  { return f.done }

func (f *fakeGrid) Err() error { return nil }

// SimAttachments: this fake has never heard an appearance.
func (f *fakeGrid) SimAttachments(ctx context.Context, avatar msg.UUID) (*sl.SimAttachments, error) {
	return nil, nil
}

func (f *fakeGrid) Presence(ctx context.Context, drawDistance float32) (*sl.Presence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := *f.presence
	return &p, nil
}

// Objects fails on a context that is done, as a call to slgod does.
func (f *fakeGrid) Objects(ctx context.Context, named, id string) ([]*sl.Seen, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*sl.Seen
	for _, o := range f.objects {
		switch {
		case id != "":
			if o.ID.String() == id {
				out = append(out, o)
			}
		case named != "":
			if o.Name == named {
				out = append(out, o)
			}
		default:
			out = append(out, o)
		}
	}
	return out, nil
}

func (f *fakeGrid) Region(ctx context.Context) (*sl.Region, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.region == nil {
		return nil, false, nil
	}
	r := *f.region
	return &r, true, nil
}

func (f *fakeGrid) Land(ctx context.Context) (*sl.Land, error) {
	return &sl.Land{Overlay: &agent.Overlay{}}, nil
}

// Ground knows no land: none has arrived.
func (f *fakeGrid) Ground(ctx context.Context, west, south, east, north float32) (float32, bool, error) {
	return 0, false, nil
}

func (f *fakeGrid) SimStats(ctx context.Context) (*sl.SimStats, error) {
	return &sl.SimStats{}, nil
}

func (f *fakeGrid) Neighbours(ctx context.Context, set *bool) (*sl.Neighbours, error) {
	return &sl.Neighbours{}, nil
}

func (f *fakeGrid) Lock(ctx context.Context, name string) error { return nil }
func (f *fakeGrid) Unlock(name string) error                    { return nil }

func (f *fakeGrid) TryLock(ctx context.Context, name string) (bool, string, error) {
	return true, "", nil
}

func (f *fakeGrid) Flush(ctx context.Context) (int, error) { return 0, nil }

func (f *fakeGrid) Friends(ctx context.Context) ([]sl.Friend, error) { return nil, nil }

func (f *fakeGrid) NoteFriend(ctx context.Context, id msg.UUID, online bool) error { return nil }

func (f *fakeGrid) HasCap(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return name == agent.InventoryCap && f.folders != nil
}

// DoCap answers a listing of a folder from folders, one level deep
// whatever depth was asked for, and fails on a context that is done, as
// an HTTP request does.
func (f *fakeGrid) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.folders == nil || r.Cap != agent.InventoryCap {
		return nil, fmt.Errorf("the fake grid has no capabilities")
	}
	path, _, _ := strings.Cut(r.Path, "?")
	path, ok := strings.CutPrefix(path, "/category/")
	path, children := strings.CutSuffix(path, "/children")
	id, err := msg.ParseUUID(path)
	if !ok || !children || err != nil || (r.Method != "" && r.Method != "GET") {
		return &agent.CapResponse{Status: 404, Body: []byte("the fake lists folders and does nothing else")}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<llsd><map><key>category_id</key><string>%s</string>`, id)
	b.WriteString(`<key>name</key><string>a folder</string><key>version</key><integer>1</integer>`)
	b.WriteString(`<key>_embedded</key><map><key>categories</key><map>`)
	for _, e := range f.folders[id] {
		if e.Folder {
			fmt.Fprintf(&b, `<key>%s</key><map><key>category_id</key><string>%s</string>`, e.ID, e.ID)
			fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string><key>name</key><string>%s</string>`, id, e.Name)
			preferred := -1
			if e.Type != 0 {
				preferred = e.Type
			}
			fmt.Fprintf(&b, `<key>type_default</key><integer>%d</integer><key>version</key><integer>1</integer></map>`, preferred)
		}
	}
	b.WriteString(`</map><key>items</key><map>`)
	for _, e := range f.folders[id] {
		if !e.Folder {
			fmt.Fprintf(&b, `<key>%s</key><map><key>item_id</key><string>%s</string>`, e.ID, e.ID)
			fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string><key>name</key><string>%s</string>`, id, e.Name)
			fmt.Fprintf(&b, `<key>type</key><integer>%d</integer><key>inv_type</key><integer>%d</integer></map>`, e.Type, e.Type)
		}
	}
	b.WriteString(`</map><key>links</key><map/></map></map></llsd>`)
	return &agent.CapResponse{Status: 200, Body: []byte(b.String())}, nil
}

func (f *fakeGrid) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.messages)
		close(f.done)
	}
	return nil
}

// deliver puts a message on the relay, as the grid would.
func (f *fakeGrid) deliver(t *testing.T, m msg.Message) {
	t.Helper()
	body, err := msg.Marshal(m)
	if err != nil {
		t.Fatalf("marshalling %T: %v", m, err)
	}
	select {
	case f.messages <- &sl.Message{ID: msg.IDOf(m), Name: m.MsgInfo().Name, Body: body, At: time.Now()}:
	case <-time.After(time.Second):
		t.Fatal("the relay would not take a message")
	}
}

// ------------------------------------------------------------- a daemon

// newTestDaemon builds a daemon with one avatar attached to a fake
// grid, and the sender trusted.
//
// It starts no run loop.  What the run loop does -- hosting, attaching,
// backing off -- needs an slgod to do it against, and what is under
// test here is what happens to a message once an avatar has one.
func newTestDaemon(t *testing.T) (*daemon, *bot, *fakeGrid) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Path = "(test)"
	cfg.Avatars = []string{"example"}
	cfg.trustedIDs[testSender] = true
	cfg.trustedNames["trusted resident"] = "Trusted Resident"

	d := newDaemon(cfg, "127.0.0.1:0", func(string, ...any) {})
	b := d.bots["example"]

	f := newFakeGrid()
	s, err := sl.New(f)
	if err != nil {
		t.Fatalf("sl.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	b.setSession(s)
	b.setState(stateAttached, s.Info().AvatarName)
	return d, b, f
}

// send runs one command line as the trusted sender and returns
// everything it printed, including the error if it failed -- which is
// what an answer over an instant message contains.
func send(t *testing.T, d *daemon, b *bot, line string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := &req{d: d, bot: b, from: testSender, who: "Trusted Resident", base: ctx}
	var out strBuilder
	err := r.Run(ctx, &out, line)
	if err != nil {
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(err.Error())
	}
	return out.String()
}

// strBuilder is a strings.Builder that satisfies io.Writer without the
// import dance in every test.
type strBuilder struct {
	b []byte
}

func (s *strBuilder) Write(p []byte) (int, error) { s.b = append(s.b, p...); return len(p), nil }
func (s *strBuilder) WriteString(t string)        { s.b = append(s.b, t...) }
func (s *strBuilder) Len() int                    { return len(s.b) }
func (s *strBuilder) String() string              { return string(s.b) }

// writeFile is os.WriteFile with the directory made, for the tests that
// need a file in a place that may not exist yet.
func writeFile(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o600)
}

// waitGroup is a throwaway for the tests that call arrived directly.
// arrived hands its work to a goroutine and the group is how a caller
// waits; a test that waits on what was written instead needs one only
// to pass in.
func waitGroup() *sync.WaitGroup { return new(sync.WaitGroup) }
