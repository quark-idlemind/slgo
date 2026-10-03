package sl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

func theOldDetails() *RegionDetails {
	return &RegionDetails{Name: "Testville", AgentLimit: 40, WaterHeight: 20, Heard: time.Now().Add(-time.Hour)}
}

func theNewDetails() *RegionDetails {
	return &RegionDetails{Name: "Testville", AgentLimit: 60, WaterHeight: 21.5, ObjectBonus: 1.5, Heard: time.Now()}
}

// TestRegionDetailsAsksAndTakesTheReplyHeardAfter: the description
// already held is not the answer, however recent it looks.
func TestRegionDetailsAsksAndTakesTheReplyHeardAfter(t *testing.T) {
	w, f := newFakeSession(t)
	f.hearRegionDetails(theOldDetails())
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.RequestRegionInfo); ok {
			f.hearRegionDetails(theNewDetails())
		}
	}

	got, err := w.RegionDetails(context.Background())
	if err != nil {
		t.Fatalf("RegionDetails: %v", err)
	}
	if got.AgentLimit != 60 || got.WaterHeight != 21.5 || got.ObjectBonus != 1.5 {
		t.Errorf("returned %+v, want the one heard after the request", got)
	}
	sent := f.Sent()
	if len(sent) != 1 {
		t.Fatalf("%d messages sent, want the one request", len(sent))
	}
	req, ok := sent[0].Msg.(*msg.RequestRegionInfo)
	if !ok || req.AgentData.AgentID.IsZero() || req.AgentData.SessionID.IsZero() {
		t.Errorf("sent %T %+v, want a RequestRegionInfo naming the agent and session", sent[0].Msg, sent[0].Msg)
	}
}

// TestRegionDetailsNeverReturnsWhatWasHeldBefore: a region that does
// not answer is a timeout, not the old description.
func TestRegionDetailsNeverReturnsWhatWasHeldBefore(t *testing.T) {
	w, f := newFakeSession(t)
	f.hearRegionDetails(theOldDetails())
	w.SetOptions(Options{RegionInfoTimeout: 300 * time.Millisecond})

	got, err := w.RegionDetails(context.Background())
	if got != nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %+v, %v; want a timeout and nothing", got, err)
	}
	if !strings.Contains(err.Error(), "the region to describe itself") {
		t.Errorf("error %q does not say what was waited for", err)
	}
}

// TestRegionDetailsStopsWhenTheCallerGivesUp rather than waiting out
// the bound.
func TestRegionDetailsStopsWhenTheCallerGivesUp(t *testing.T) {
	w, _ := newFakeSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := w.RegionDetails(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v, want the caller's deadline", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the call outlived its context")
	}
}

// TestRegionDetailsThroughADaemon: the same wait over the RPC, where the
// description comes with an age and a count.
func TestRegionDetailsThroughADaemon(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	w := newSession(t, h)

	d.regionMu.Lock()
	d.regionDetails = &pb.RegionDetailsResponse{Known: true, Heard: 1, Name: "Testville", MaxAgents: 40, AgeMs: 3_600_000}
	d.regionMu.Unlock()

	// The daemon's region answers the request once it has seen it.
	go func() {
		for p := range d.sent {
			if m := p.GetMessage(); m != nil && msg.ID(m.Id) == msg.IDOf(&msg.RequestRegionInfo{}) {
				d.regionMu.Lock()
				d.regionDetails = &pb.RegionDetailsResponse{
					Known: true, Heard: 2, Name: "Testville", MaxAgents: 40, MaxAgents32: 60,
					WaterHeight: 21.5, ObjectBonus: 1.5, AgeMs: 20,
					Chat: &pb.RegionChat{Shout: 100},
				}
				d.regionMu.Unlock()
				return
			}
		}
	}()

	got, err := w.RegionDetails(context.Background())
	if err != nil {
		t.Fatalf("RegionDetails: %v", err)
	}
	// The U8 is the agent limit, as the viewer reads it, not MaxAgents32.
	if got.AgentLimit != 40 || got.WaterHeight != 21.5 || got.Chat == nil || got.Chat.Shout != 100 {
		t.Errorf("returned %+v, want the second description", got)
	}
	if age := time.Since(got.Heard); age < 0 || age > 5*time.Second {
		t.Errorf("heard %v ago, want the 20 ms age placed by this clock", age)
	}
}

// TestRegionDetailsThroughADaemonThatHearsNothing times out.
func TestRegionDetailsThroughADaemonThatHearsNothing(t *testing.T) {
	t.Parallel()
	h, _ := newFakeDaemon(t)
	w := newSession(t, h)
	w.SetOptions(Options{RegionInfoTimeout: 300 * time.Millisecond})
	if got, err := w.RegionDetails(context.Background()); got != nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %+v, %v; want a timeout", got, err)
	}
}
