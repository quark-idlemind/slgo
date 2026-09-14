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
// What it does not do is inventory.  That arrives over AIS, which is
// HTTP against a capability, and a fake for it is an httptest server
// and a folder tree -- worth having in slsh, where the inventory
// commands are the bulk of the program, and not here, where they are a
// thin layer over calls sl already tests.  The inventory commands are
// tested for what this package adds to them: how a path is resolved to
// one entry, and what is refused.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Who the fake says everybody is.  Invented, like every identifier in
// this repository: nothing here is anybody's.
var (
	testMe        = msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
	testSessionID = msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")
	testRoot      = msg.MustParseUUID("23077e57-7e57-c0de-622e-77274d813d21")
	testSender    = msg.MustParseUUID("88987e57-7e57-c0de-1877-3b69b9febf7a")
	testStranger  = msg.MustParseUUID("19d17e57-7e57-c0de-b3a6-3f42edc56d31")
	testLamp      = msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000001")
)

var _ sl.Backend = (*fakeGrid)(nil)

// fakeGrid is an sl.Backend with nothing behind it.
type fakeGrid struct {
	mu sync.Mutex

	info     *sl.Info
	messages chan *sl.Message
	done     chan struct{}
	closed   bool

	// sent is every message the session put on the wire, which is how
	// a test reads what a command did.
	sent []msg.Message

	presence *sl.Presence
	region   *sl.Region
	objects  []*sl.Seen
	parcel   *agent.Parcel

	// fail, when set for a call's name, is what that call answers
	// with instead of doing anything.
	fail map[string]error
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
		done:     make(chan struct{}),
		presence: &sl.Presence{
			Region:   "Nowhere",
			Position: msg.Vector3{X: 128, Y: 64, Z: 25},
		},
		region: &sl.Region{Name: "Nowhere", Access: sl.AccessGeneral, WaterHeight: 20},
		fail:   map[string]error{},
	}
}

// Sent is what the session has put on the wire so far.
func (f *fakeGrid) Sent() []msg.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]msg.Message(nil), f.sent...)
}

// IMsSent is the instant messages among them, in order.
func (f *fakeGrid) IMsSent() []*msg.ImprovedInstantMessage {
	var out []*msg.ImprovedInstantMessage
	for _, m := range f.Sent() {
		if im, ok := m.(*msg.ImprovedInstantMessage); ok {
			out = append(out, im)
		}
	}
	return out
}

func (f *fakeGrid) Info() *sl.Info { return f.info }

func (f *fakeGrid) Send(ctx context.Context, m msg.Message, reliable bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["Send"]; err != nil {
		return err
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeGrid) Control(ctx context.Context, flags uint32) error { return nil }

func (f *fakeGrid) Messages() <-chan *sl.Message           { return f.messages }
func (f *fakeGrid) Events() <-chan *sl.QueueEvent          { return nil }
func (f *fakeGrid) RegionChanges() <-chan *sl.RegionChange { return nil }
func (f *fakeGrid) Done() <-chan struct{}                  { return f.done }

func (f *fakeGrid) Err() error { return nil }

func (f *fakeGrid) Presence(ctx context.Context, drawDistance float32) (*sl.Presence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["Presence"]; err != nil {
		return nil, err
	}
	p := *f.presence
	return &p, nil
}

func (f *fakeGrid) Objects(ctx context.Context, named, id string) ([]*sl.Seen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["Objects"]; err != nil {
		return nil, err
	}
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
	if err := f.fail["Region"]; err != nil {
		return nil, false, err
	}
	if f.region == nil {
		return nil, false, nil
	}
	r := *f.region
	return &r, true, nil
}

func (f *fakeGrid) Land(ctx context.Context) (*sl.Land, error) {
	return &sl.Land{Overlay: &agent.Overlay{}}, nil
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

func (f *fakeGrid) HasCap(name string) bool { return false }

func (f *fakeGrid) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	return nil, fmt.Errorf("the fake grid has no capabilities")
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
