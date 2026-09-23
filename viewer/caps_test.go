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

// TestSeedFollowsNoRedirectOffTheSimulatorsHost: the seed is asked by
// the daemon because a viewer asked, and Go follows a redirect by
// default to wherever it points.  An answer that sent it somewhere else
// would have the daemon post there on the viewer's behalf.  On the
// seed's own host a redirect is still followed.
func TestSeedFollowsNoRedirectOffTheSimulatorsHost(t *testing.T) {
	var away int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		away++
		w.Write(encode(t, map[string]any{"Somewhere": "else"}))
	}))
	defer other.Close()
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/seed/moved":
			http.Redirect(w, r, "/seed/here", http.StatusTemporaryRedirect)
		case "/seed/here":
			w.Write(encode(t, map[string]any{"GetTexture": "https://sim.invalid/cap/texture"}))
		case "/seed/away":
			http.Redirect(w, r, other.URL+"/private", http.StatusTemporaryRedirect)
		}
	}))
	defer real.Close()

	ask := func(path string) int {
		s := httptest.NewServer(&Seed{Real: real.URL + path, EventQueue: "x", Logf: func(string, ...any) {}})
		defer s.Close()
		resp, err := http.Post(s.URL, "application/llsd+xml", strings.NewReader("<llsd><array/></llsd>"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := ask("/seed/away"); code != http.StatusBadGateway {
		t.Errorf("a seed redirected off its host answered %d, want %d", code, http.StatusBadGateway)
	}
	if away != 0 {
		t.Errorf("the other host was asked %d times", away)
	}
	if code := ask("/seed/moved"); code != http.StatusOK {
		t.Errorf("a seed redirected on its own host answered %d", code)
	}
}

// TestEventsReachAViewerWaitingForThem: the poll is held open until
// there is something to say, which is how the simulator's own works.
//
// ParcelProperties rather than the TeleportFinish this used to carry.
// That one is withheld now, so a test that used it as a sample event
// was measuring the withholding and calling it a delivery.
func TestEventsReachAViewerWaitingForThem(t *testing.T) {
	q := NewEventQueue()
	s := httptest.NewServer(q)
	defer s.Close()

	go func() {
		time.Sleep(50 * time.Millisecond)
		q.Add("ParcelProperties", encode(t, map[string]any{"ParcelData": "somewhere"}))
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
	if llsd.String(got, "message") != "ParcelProperties" {
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

// TestTeleportFinishIsNeverGivenToAViewer is the hole the daemon opened
// when it learned to follow a teleport, and it is reachable without any
// viewer teleport at all: `slsh tp` moves the session, the simulator
// puts the finish on the queue, and slgod fans it out.  A viewer handed
// it opens a circuit to the real simulator with this session's own ids.
//
// TeleportFailed goes across in the same breath on purpose.  It carries
// a reason and no address, it is what takes a viewer out of a teleport
// it should never have been shown, and withholding it would buy
// tidiness at the price of the only safety net there is.
func TestTeleportFinishIsNeverGivenToAViewer(t *testing.T) {
	q := NewEventQueue()
	q.Add("TeleportFinish", []byte("<llsd><map/></llsd>"))
	q.Add("TeleportFailed", []byte("<llsd><map/></llsd>"))

	if _, _, withheld := q.Stats(); withheld != 1 {
		t.Errorf("withheld %d events, want the finish and only the finish", withheld)
	}
	if why := WhyWithheld("TeleportFinish"); why == "" {
		t.Error("nothing says why a viewer is not given the finish")
	}
	if why := WhyWithheld("TeleportFailed"); why != "" {
		t.Errorf("TeleportFailed is withheld: %s", why)
	}

	s := httptest.NewServer(q)
	defer s.Close()
	resp, err := http.Post(s.URL, "application/llsd+xml", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "TeleportFinish") {
		t.Errorf("the viewer was told where the avatar went:\n%s", body)
	}
	if !strings.Contains(string(body), "TeleportFailed") {
		t.Errorf("the viewer was not told the teleport failed:\n%s", body)
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

// TestANeighbouringSimulatorsAddressIsNeverGivenToAViewer: the hole
// that has been open for as long as this front end has existed, and the
// one that needed nothing to happen to reach it -- an ordinary region
// introduces its neighbours several times a minute.
//
// EnableSimulator carries a handle, an IP and a port and no capability,
// and a capability is not what opening a circuit takes: UseCircuitCode
// is the circuit code, the session id and the agent id, all three of
// which a viewer holding this session already has.  So the address is
// the only thing it was short of, and this is the address.
func TestANeighbouringSimulatorsAddressIsNeverGivenToAViewer(t *testing.T) {
	q := NewEventQueue()
	q.Add("EnableSimulator", []byte("<llsd><map/></llsd>"))
	q.Add("CrossedRegion", []byte("<llsd><map/></llsd>"))
	q.Add("ParcelProperties", []byte("<llsd><map/></llsd>"))

	if _, _, withheld := q.Stats(); withheld != 2 {
		t.Errorf("withheld %d events, want the neighbour and the crossing", withheld)
	}
	for _, name := range []string{"EnableSimulator", "CrossedRegion"} {
		if why := WhyWithheld(name); why == "" {
			t.Errorf("nothing says why a viewer is not given %s", name)
		}
	}

	s := httptest.NewServer(q)
	defer s.Close()
	resp, err := http.Post(s.URL, "application/llsd+xml", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, name := range []string{"EnableSimulator", "CrossedRegion"} {
		if strings.Contains(string(body), name) {
			t.Errorf("%s was handed to the viewer anyway:\n%s", name, body)
		}
	}
	if !strings.Contains(string(body), "ParcelProperties") {
		t.Errorf("ordinary events stopped going across:\n%s", body)
	}
}
