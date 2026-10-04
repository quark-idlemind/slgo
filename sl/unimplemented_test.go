package sl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
)

// minimalBackend is the whole of a fake that embeds UnimplementedBackend:
// the ids New needs, and a relay that stays open.
type minimalBackend struct {
	UnimplementedBackend
	msgs chan *Message
	done chan struct{}
}

func (b *minimalBackend) Info() *Info {
	return &Info{Name: "fake", AgentID: testAgentID, SessionID: testSessionID}
}
func (b *minimalBackend) Messages() <-chan *Message { return b.msgs }
func (b *minimalBackend) Done() <-chan struct{}     { return b.done }
func (b *minimalBackend) Close() error {
	close(b.msgs)
	close(b.done)
	return nil
}

func TestEmbeddingUnimplementedBackendIsEnoughForNew(t *testing.T) {
	b := &minimalBackend{msgs: make(chan *Message), done: make(chan struct{})}
	s, err := New(b)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end after Close")
	}
}

func TestNewRefusesAnUnimplementedBackendWithNoIdentity(t *testing.T) {
	if _, err := New(UnimplementedBackend{}); err == nil {
		t.Fatal("New accepted a backend with no agent id")
	}
}

func TestUnimplementedBackendAnswersNotSupported(t *testing.T) {
	var b Backend = UnimplementedBackend{}
	ctx := context.Background()

	if b.Info() == nil {
		t.Error("Info is nil")
	}
	if b.HasCap("x") {
		t.Error("HasCap is true")
	}
	if err := b.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	errs := map[string]error{}
	_, errs["Refresh"] = b.Refresh(ctx)
	errs["Send"] = b.Send(ctx, nil, true)
	errs["Control"] = b.Control(ctx, 0)
	errs["Err"] = b.Err()
	_, errs["Presence"] = b.Presence(ctx, 0)
	_, errs["Objects"] = b.Objects(ctx, "", "")
	_, errs["SimAttachments"] = b.SimAttachments(ctx, testAgentID)
	_, _, errs["Region"] = b.Region(ctx)
	_, errs["SimStats"] = b.SimStats(ctx)
	_, _, errs["LastRegionDetails"] = b.LastRegionDetails(ctx)
	_, errs["Land"] = b.Land(ctx)
	_, _, errs["Ground"] = b.Ground(ctx, 0, 0, 1, 1)
	_, errs["Neighbours"] = b.Neighbours(ctx, nil)
	errs["Lock"] = b.Lock(ctx, "x")
	errs["Unlock"] = b.Unlock("x")
	_, _, errs["TryLock"] = b.TryLock(ctx, "x")
	_, errs["Flush"] = b.Flush(ctx)
	_, errs["Friends"] = b.Friends(ctx)
	errs["NoteFriend"] = b.NoteFriend(ctx, testAgentID, true)
	_, errs["DoCap"] = b.DoCap(ctx, agent.CapRequest{})
	for name, err := range errs {
		if !errors.Is(err, ErrNotSupported) {
			t.Errorf("%s: %v, want ErrNotSupported", name, err)
		}
	}
}

func TestUnimplementedBackendRelaysAreClosed(t *testing.T) {
	var b Backend = UnimplementedBackend{}
	if _, ok := <-b.Messages(); ok {
		t.Error("Messages is open")
	}
	if _, ok := <-b.Events(); ok {
		t.Error("Events is open")
	}
	if _, ok := <-b.RegionChanges(); ok {
		t.Error("RegionChanges is open")
	}
	select {
	case <-b.Done():
	default:
		t.Error("Done is open")
	}
}
