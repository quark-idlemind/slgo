package main

// A grid that is not there, trimmed from the one in the slate package's
// tests, which cannot be imported: an sl.Backend that answers from fields
// and records what was sent. The session over it is real.

import (
	"context"
	"sync"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var _ sl.Backend = (*fakeGrid)(nil)

// Invented ids, made with tools/new-id.
var (
	testMe      = msg.MustParseUUID("112e7e57-7e57-c0de-4677-7c7c8af4f04a")
	testSession = msg.MustParseUUID("4f127e57-7e57-c0de-a2a6-2f8e224733d0")
	testRegion  = msg.MustParseUUID("5f5e7e57-7e57-c0de-2bef-e55b67d8b14f")
	testInvRoot = msg.MustParseUUID("99267e57-7e57-c0de-55c2-b0e23ccf1e53")
	idPane      = msg.MustParseUUID("adb37e57-7e57-c0de-ccaf-c554c41e4c8a")
	idSign      = msg.MustParseUUID("a12e7e57-7e57-c0de-1f13-2165984b7290")
	idSecond    = msg.MustParseUUID("b7f37e57-7e57-c0de-9241-25f659887717")
)

type fakeGrid struct {
	sl.UnimplementedBackend // what a test does not use answers ErrNotSupported
	mu                      sync.Mutex
	msgs                    chan *sl.Message
	done                    chan struct{}
	doneOnce                sync.Once
	sent                    []msg.Message
	objects                 []*sl.Seen
	me                      msg.UUID // the avatar's id; testMe when zero
}

func newGrid(t *testing.T) *fakeGrid {
	t.Helper()
	f := &fakeGrid{msgs: make(chan *sl.Message), done: make(chan struct{})}
	f.objects = []*sl.Seen{{
		Object: sl.Object{ID: idSign, Local: 101, Name: "Example Sign"}, Owner: testMe, PCode: 9,
		Position: msg.Vector3{X: 130, Y: 128, Z: 25},
	}, {
		Object: sl.Object{ID: testMe, Local: 1, Name: "Quark Idlemind"}, PCode: 47,
		Position: msg.Vector3{X: 128, Y: 128, Z: 22},
	}}
	t.Cleanup(func() { f.Close() })
	return f
}

// session is a real Session over the fake.
func (f *fakeGrid) session(t *testing.T) *sl.Session {
	t.Helper()
	w, err := sl.New(f)
	if err != nil {
		t.Fatalf("sl.New: %v", err)
	}
	return w
}

// said is the text of each say sent, in order.
func (f *fakeGrid) said() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		if c, ok := m.(*msg.ChatFromViewer); ok {
			s := string(c.ChatData.Message)
			out = append(out, s[:len(s)-1])
		}
	}
	return out
}

func (f *fakeGrid) Info() *sl.Info {
	me := testMe
	if !f.me.IsZero() {
		me = f.me
	}
	return &sl.Info{
		Name: "fake", AgentID: me, SessionID: testSession,
		AvatarName: "Quark Idlemind", Region: "Test Region", InventoryRoot: testInvRoot,
	}
}
func (f *fakeGrid) Refresh(context.Context) (*sl.Info, error) { return f.Info(), nil }
func (f *fakeGrid) Send(_ context.Context, m msg.Message, _ bool) error {
	f.mu.Lock()
	f.sent = append(f.sent, m)
	f.mu.Unlock()
	return nil
}
func (f *fakeGrid) Messages() <-chan *sl.Message { return f.msgs }
func (f *fakeGrid) Done() <-chan struct{}        { return f.done }
func (f *fakeGrid) Presence(context.Context, float32) (*sl.Presence, error) {
	return &sl.Presence{Position: msg.Vector3{X: 128, Y: 128, Z: 25}}, nil
}
func (f *fakeGrid) Objects(_ context.Context, named, id string) ([]*sl.Seen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*sl.Seen
	for _, o := range f.objects {
		if (id != "" && o.ID.String() != id) || (named != "" && o.Name != named) {
			continue
		}
		c := *o
		out = append(out, &c)
	}
	return out, nil
}
func (f *fakeGrid) Region(context.Context) (*sl.Region, bool, error) {
	return &sl.Region{ID: testRegion, Name: "Test Region", Handle: 1099511628032}, true, nil
}
func (f *fakeGrid) Neighbours(context.Context, *bool) (*sl.Neighbours, error) {
	return &sl.Neighbours{}, nil
}
func (f *fakeGrid) Lock(context.Context, string) error { return nil }
func (f *fakeGrid) Unlock(string) error                { return nil }
func (f *fakeGrid) TryLock(context.Context, string) (bool, string, error) {
	return true, "", nil
}
func (f *fakeGrid) Close() error {
	f.doneOnce.Do(func() {
		close(f.msgs)
		close(f.done)
	})
	return nil
}

// withPane wears a half-metre box on HUD centre 2, in front of anything
// else worn.
func (f *fakeGrid) withPane() {
	shape, err := sl.DefaultShape().Pack()
	if err != nil {
		panic(err)
	}
	f.objects = append(f.objects, &sl.Seen{
		Object: sl.Object{ID: idPane, Local: 301, Name: "Example Panel"}, Owner: testMe, PCode: 9,
		Parent: 1, AttachPoint: sl.HUDCenter2, Shape: shape, LinkKnown: true,
		Position: msg.Vector3{X: -0.5}, Scale: msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5},
	})
}

// withMe makes the grid another avatar's, with a body of its own.
func (f *fakeGrid) withMe(id msg.UUID) *fakeGrid {
	f.me = id
	f.objects[len(f.objects)-1] = &sl.Seen{
		Object: sl.Object{ID: id, Local: 2, Name: "Example Resident"}, PCode: 47,
		Position: msg.Vector3{X: 129, Y: 128, Z: 22},
	}
	return f
}

// grabs is how many times the pane was pressed.
func (f *fakeGrid) grabs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.sent {
		if _, ok := m.(*msg.ObjectGrab); ok {
			n++
		}
	}
	return n
}
