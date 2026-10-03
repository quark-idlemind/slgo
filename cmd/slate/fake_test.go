package main

// A grid that is not there, trimmed from the one in the slate package's
// tests, which cannot be imported: an sl.Backend that answers from fields
// and records what was sent. The session over it is real.

import (
	"context"
	"sync"
	"testing"

	"github.com/quark-idlemind/slgo/agent"
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
	idSign      = msg.MustParseUUID("a12e7e57-7e57-c0de-1f13-2165984b7290")
)

type fakeGrid struct {
	mu       sync.Mutex
	msgs     chan *sl.Message
	done     chan struct{}
	doneOnce sync.Once
	sent     []msg.Message
	objects  []*sl.Seen
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
	return &sl.Info{
		Name: "fake", AgentID: testMe, SessionID: testSession,
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
func (f *fakeGrid) Control(context.Context, uint32) error  { return nil }
func (f *fakeGrid) Messages() <-chan *sl.Message           { return f.msgs }
func (f *fakeGrid) Events() <-chan *sl.QueueEvent          { return nil }
func (f *fakeGrid) RegionChanges() <-chan *sl.RegionChange { return nil }
func (f *fakeGrid) Done() <-chan struct{}                  { return f.done }
func (f *fakeGrid) Err() error                             { return nil }
func (f *fakeGrid) SimAttachments(context.Context, msg.UUID) (*sl.SimAttachments, error) {
	return nil, nil
}
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
func (f *fakeGrid) SimStats(context.Context) (*sl.SimStats, error) { return nil, nil }

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
func (f *fakeGrid) HasCap(string) bool                               { return false }
func (f *fakeGrid) DoCap(context.Context, agent.CapRequest) (*agent.CapResponse, error) {
	return nil, context.Canceled
}
func (f *fakeGrid) Close() error {
	f.doneOnce.Do(func() {
		close(f.msgs)
		close(f.done)
	})
	return nil
}
