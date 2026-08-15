package viewer

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/llsd"
)

func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := llsd.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestSeedChangesOnlyTheEventQueue is the point of proxying rather than
// composing: the set of capabilities grows, and one this build has never
// heard of is one a viewer may still need.
func TestSeedChangesOnlyTheEventQueue(t *testing.T) {
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(encode(t, map[string]any{
			"EventQueueGet":     "https://sim.invalid/cap/events",
			"GetTexture":        "https://sim.invalid/cap/texture",
			"GetMesh2":          "https://sim.invalid/cap/mesh",
			"ViewerAsset":       "https://sim.invalid/cap/asset",
			"SomethingBrandNew": "https://sim.invalid/cap/new",
		}))
	}))
	defer real.Close()

	s := httptest.NewServer(&Seed{
		Real:       real.URL,
		EventQueue: "http://slgod.invalid/cap/x/event",
		Logf:       func(string, ...any) {},
	})
	defer s.Close()

	resp, err := http.Post(s.URL, "application/llsd+xml", strings.NewReader("<llsd><array/></llsd>"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	v, err := llsd.Decode(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	m := llsd.Map(v)

	if got := llsd.String(m, "EventQueueGet"); got != "http://slgod.invalid/cap/x/event" {
		t.Errorf("EventQueueGet = %q, want ours", got)
	}
	// Everything else is the simulator's, including one nothing here
	// knows anything about.
	for _, k := range []string{"GetTexture", "GetMesh2", "ViewerAsset", "SomethingBrandNew"} {
		if got := llsd.String(m, k); !strings.HasPrefix(got, "https://sim.invalid/") {
			t.Errorf("%s = %q, want the simulator's own", k, got)
		}
	}
	if len(m) != 5 {
		t.Errorf("handed on %d capabilities, want all 5", len(m))
	}
}

// TestSeedPassesOnWhatItCannotRead: a viewer given nothing cannot start,
// so an unreadable answer is handed over rather than lost.
func TestSeedPassesOnWhatItCannotRead(t *testing.T) {
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("this is not llsd"))
	}))
	defer real.Close()

	s := httptest.NewServer(&Seed{Real: real.URL, EventQueue: "x", Logf: func(string, ...any) {}})
	defer s.Close()

	resp, err := http.Post(s.URL, "application/llsd+xml", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := new(bytes.Buffer)
	body.ReadFrom(resp.Body)
	if body.String() != "this is not llsd" {
		t.Errorf("body = %q, want it passed through", body.String())
	}
}

// TestEventsReachAViewerWaitingForThem: the poll is held open until
// there is something to say, which is how the simulator's own works.
func TestEventsReachAViewerWaitingForThem(t *testing.T) {
	q := NewEventQueue()
	s := httptest.NewServer(q)
	defer s.Close()

	go func() {
		time.Sleep(50 * time.Millisecond)
		q.Add("TeleportFinish", encode(t, map[string]any{"Info": "somewhere"}))
	}()

	resp, err := http.Post(s.URL, "application/llsd+xml", strings.NewReader("<llsd><map/></llsd>"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	v, err := llsd.Decode(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	m := llsd.Map(v)
	events, _ := m["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %#v", len(events), m)
	}
	got := llsd.Map(events[0])
	if llsd.String(got, "message") != "TeleportFinish" {
		t.Errorf("event = %#v", got)
	}
	if llsd.Map(got["body"]) == nil {
		t.Errorf("the body did not survive: %#v", got["body"])
	}
}

// TestAnIdlePollIsAnsweredRatherThanLeftHanging: a viewer must not wait
// on this longer than PollHold.
func TestAnIdlePollIsAnsweredRatherThanLeftHanging(t *testing.T) {
	q := NewEventQueue()
	s := httptest.NewServer(q)
	defer s.Close()

	// Something already waiting, so the first poll returns at once and
	// the test does not sit out PollHold.
	q.Add("A", encode(t, map[string]any{}))
	resp, err := http.Post(s.URL, "application/llsd+xml", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %s", resp.Status)
	}
}

// TestAddNeverBlocks: this is called from the session's own event poll,
// so a viewer that has stopped collecting must not be able to stop the
// session's queue from draining.
func TestAddNeverBlocks(t *testing.T) {
	q := NewEventQueue()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < QueueLimit*3; i++ {
			q.Add("Flood", []byte("<llsd><map/></llsd>"))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("adding events blocked the session")
	}
	if _, dropped, _ := q.Stats(); dropped == 0 {
		t.Error("nothing was dropped, so the bound did nothing")
	}
}

// TestNeighbourRegionsAreNotOfferedToTheViewer: the event carries a
// neighbouring simulator's UDP address and seed capability, and a
// viewer given it opens a direct connection there using this session's
// credentials -- to a simulator that has never heard of slgod.
func TestNeighbourRegionsAreNotOfferedToTheViewer(t *testing.T) {
	q := NewEventQueue()
	q.Add("EstablishAgentCommunication", []byte("<llsd><map/></llsd>"))
	q.Add("ParcelProperties", []byte("<llsd><map/></llsd>"))

	delivered, _, withheld := q.Stats()
	if withheld != 1 {
		t.Errorf("withheld %d events, want the one", withheld)
	}
	_ = delivered

	s := httptest.NewServer(q)
	defer s.Close()
	resp, err := http.Post(s.URL, "application/llsd+xml", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "EstablishAgentCommunication") {
		t.Errorf("the neighbour was offered to the viewer anyway:\n%s", body)
	}
	if !strings.Contains(string(body), "ParcelProperties") {
		t.Errorf("ordinary events stopped going across:\n%s", body)
	}
}

// TestEventsAreCopied: the body belongs to the session's decoder and
// keeping the slice would keep whatever it decoded next.
func TestEventsAreCopied(t *testing.T) {
	q := NewEventQueue()
	buf := []byte("<llsd><map/></llsd>")
	q.Add("A", buf)
	copy(buf, "<llsd><undef/>!")

	s := httptest.NewServer(q)
	defer s.Close()
	resp, err := http.Post(s.URL, "application/llsd+xml", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	v, _ := llsd.Decode(resp.Body)
	events, _ := llsd.Map(v)["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("got %d events", len(events))
	}
}
