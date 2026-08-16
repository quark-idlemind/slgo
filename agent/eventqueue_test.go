package agent

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/llsd"
)

// eqServer is a simulator's end of the event queue: it holds a poll
// open until it has something, answers with an id, and expects that id
// acknowledged next time.
type eqServer struct {
	mu      sync.Mutex
	pending [][2]any // name, body
	acks    []any
	dones   int
	id      int64
	fail    int // return 500 this many times first
}

func (e *eqServer) push(name string, body map[string]any) {
	e.mu.Lock()
	e.pending = append(e.pending, [2]any{name, body})
	e.mu.Unlock()
}

func (e *eqServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		buf.ReadFrom(r.Body)
		v, err := llsd.Decode(bytes.NewReader(buf.Bytes()))
		if err != nil {
			http.Error(w, "bad llsd", 400)
			return
		}
		req := llsd.Map(v)

		e.mu.Lock()
		if e.fail > 0 {
			e.fail--
			e.mu.Unlock()
			http.Error(w, "later", 500)
			return
		}
		if a, ok := req["ack"]; ok && a != nil {
			e.acks = append(e.acks, a)
		}
		if d, _ := req["done"].(bool); d {
			e.dones++
			e.mu.Unlock()
			w.Write([]byte("<llsd><undef/></llsd>"))
			return
		}
		batch := e.pending
		e.pending = nil
		if len(batch) == 0 {
			e.mu.Unlock()
			// The classic "nothing happened, ask again".
			http.Error(w, "", http.StatusBadGateway)
			return
		}
		e.id++
		id := e.id
		e.mu.Unlock()

		events := make([]any, 0, len(batch))
		for _, b := range batch {
			events = append(events, map[string]any{
				"message": b[0],
				"body":    b[1],
			})
		}
		out, err := llsd.Encode(map[string]any{"events": events, "id": id})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		w.Write(out)
	})
}

func (e *eqServer) seenAcks() []any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]any(nil), e.acks...)
}

// eqAgent makes an Agent with only the capability plumbing wired up,
// which is all the event queue needs.
func eqAgent(url string) *Agent {
	a := &Agent{Account: &Account{}, done: make(chan struct{})}
	a.SetCaps(Caps{EventQueueCap: url})
	return a
}

func TestEventQueueDelivers(t *testing.T) {
	eq := &eqServer{}
	hs := httptest.NewServer(eq.handler())
	defer hs.Close()

	eq.push("ParcelProperties", map[string]any{
		"parcel_data": []any{map[string]any{"Name": "Halcyonae", "ParcelFlags": int64(0x40)}},
	})
	eq.push("TeleportFinish", map[string]any{"Info": []any{map[string]any{"SimIP": "1.2.3.4"}}})

	a := eqAgent(hs.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type got struct {
		name string
		body []byte
	}
	ch := make(chan got, 8)
	go a.runEventQueue(ctx, func(name string, body []byte) {
		ch <- got{name, body}
	})

	names := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case g := <-ch:
			names[g.name] = true
			// The body is LLSD the client can parse.
			v, err := llsd.Decode(bytes.NewReader(g.body))
			if err != nil {
				t.Fatalf("%s: body is not LLSD: %v", g.name, err)
			}
			if llsd.Map(v) == nil {
				t.Errorf("%s: body decoded to %T", g.name, v)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("event not delivered")
		}
	}
	if !names["ParcelProperties"] || !names["TeleportFinish"] {
		t.Errorf("delivered %v", names)
	}

	// The id must be acknowledged on the next poll, or the
	// simulator sends everything again.
	deadline := time.Now().Add(5 * time.Second)
	for len(eq.seenAcks()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acks := eq.seenAcks(); len(acks) == 0 {
		t.Error("the poll never acknowledged an id")
	} else if acks[0] != int64(1) {
		t.Errorf("acknowledged %v, want 1", acks[0])
	}

	if st := a.EventStats(); st.Events != 2 {
		t.Errorf("stats = %+v", st)
	}
}

// TestEventQueueTimeoutIsNotAnError: an empty poll is how the queue
// says nothing happened, and must not be counted or backed off.
func TestEventQueueTimeoutIsNotAnError(t *testing.T) {
	eq := &eqServer{}
	hs := httptest.NewServer(eq.handler())
	defer hs.Close()

	a := eqAgent(hs.URL)
	ctx, cancel := context.WithCancel(context.Background())
	go a.runEventQueue(ctx, nil)

	deadline := time.Now().Add(3 * time.Second)
	for a.EventStats().Timeouts < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	st := a.EventStats()
	if st.Timeouts < 3 {
		t.Errorf("only %d timeouts; the poll is not looping", st.Timeouts)
	}
	if st.Errors != 0 {
		t.Errorf("an empty poll was counted as an error: %+v", st)
	}
}

func TestEventQueueBacksOffOnFailure(t *testing.T) {
	eq := &eqServer{fail: 2}
	hs := httptest.NewServer(eq.handler())
	defer hs.Close()

	saved := eventBackoff
	eventBackoff = []time.Duration{10 * time.Millisecond}
	defer func() { eventBackoff = saved }()

	eq.push("Something", map[string]any{"x": int64(1)})

	a := eqAgent(hs.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var n atomic.Int64
	go a.runEventQueue(ctx, func(string, []byte) { n.Add(1) })

	deadline := time.Now().Add(5 * time.Second)
	for n.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n.Load() == 0 {
		t.Fatal("the poll gave up instead of retrying")
	}
	if st := a.EventStats(); st.Errors < 2 {
		t.Errorf("stats = %+v, expected the failures counted", st)
	}
}

func TestEventQueueSaysDoneOnShutdown(t *testing.T) {
	eq := &eqServer{}
	hs := httptest.NewServer(eq.handler())
	defer hs.Close()
	eq.push("Something", map[string]any{"x": int64(1)})

	a := eqAgent(hs.URL)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	got := make(chan struct{}, 1)
	go func() {
		defer close(done)
		a.runEventQueue(ctx, func(string, []byte) {
			select {
			case got <- struct{}{}:
			default:
			}
		})
	}()

	<-got
	cancel()
	<-done

	eq.mu.Lock()
	defer eq.mu.Unlock()
	if eq.dones == 0 {
		t.Error("shutting down did not tell the simulator we were finished")
	}
}

func TestEventQueueNeedsTheCapability(t *testing.T) {
	a := &Agent{Account: &Account{}, done: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); a.runEventQueue(context.Background(), nil) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("the poll ran without the capability")
	}
	if st := a.EventStats(); st.Polls != 0 {
		t.Errorf("polled anyway: %+v", st)
	}
}

// TestTheQueueIsWhereGroupMembershipArrives: the agent reads its own
// session state off the queue before anything else sees it.  A relay
// hands events to whichever client is attached, and a client may be
// attached late or never, so state the session owns cannot be learned
// there.
func TestTheQueueIsWhereGroupMembershipArrives(t *testing.T) {
	a := eqAgent("")

	body, err := llsd.Encode(map[string]any{
		"id": int64(1),
		"events": []any{
			map[string]any{"message": "AgentGroupDataUpdate", "body": map[string]any{
				"GroupData": []any{
					map[string]any{
						"GroupID":     aGroup.String(),
						"GroupName":   "Builders",
						"GroupPowers": int64(0x101),
					},
					// Rows that make no sense are stepped over rather
					// than taken as a reason to lose the list.
					"not a row at all",
					map[string]any{"GroupID": "not a uuid"},
				},
			}},
			// Everything else is the clients' business and passes
			// through untouched.
			map[string]any{"message": "ParcelProperties", "body": map[string]any{"x": int64(1)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var seen []string
	id, n := a.deliver(body, func(name string, _ []byte) { seen = append(seen, name) })
	if n != 2 || len(seen) != 2 {
		t.Errorf("delivered %d events, handler saw %v", n, seen)
	}
	if id != int64(1) {
		t.Errorf("id = %#v, want the one to acknowledge next time", id)
	}

	gs := a.Groups()
	if len(gs) != 1 || gs[0].ID != aGroup || gs[0].Name != "Builders" || gs[0].Powers != 0x101 {
		t.Errorf("Groups = %+v", gs)
	}
}

// TestTheQueueIgnoresWhatItCannotUse: a poll's reply arrives from the
// grid and a malformed one must cost no more than the poll it came on.
func TestTheQueueIgnoresWhatItCannotUse(t *testing.T) {
	a := eqAgent("")

	empty, _ := llsd.Encode(map[string]any{})
	odd, _ := llsd.Encode(map[string]any{"events": []any{
		"not an event",
		map[string]any{"body": map[string]any{}},   // no message name
		map[string]any{"message": "", "body": nil}, // nor this
		map[string]any{"message": "Something", "body": nil},
	}})
	// An AgentGroupDataUpdate whose rows are all unusable leaves the
	// list alone rather than emptying it.
	noGroups, _ := llsd.Encode(map[string]any{"events": []any{
		map[string]any{"message": "AgentGroupDataUpdate", "body": map[string]any{
			"GroupData": []any{map[string]any{"GroupID": "not a uuid"}},
		}},
	}})
	notAGroupUpdate, _ := llsd.Encode(map[string]any{"events": []any{
		map[string]any{"message": "AgentGroupDataUpdate", "body": "not a map"},
	}})

	for _, c := range []struct {
		name string
		body []byte
		want int
	}{
		{"not LLSD at all", []byte("<not-llsd"), 0},
		{"LLSD that is not a map", []byte(`<llsd><string>hello</string></llsd>`), 0},
		{"a map with no events in it", empty, 0},
		{"events that are not events", odd, 1},
		{"a group update with no usable groups", noGroups, 1},
		{"a group update whose body is not a map", notAGroupUpdate, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, n := a.deliver(c.body, nil); n != c.want {
				t.Errorf("delivered %d, want %d", n, c.want)
			}
		})
	}
	if gs := a.Groups(); len(gs) != 0 {
		t.Errorf("Groups = %+v, want none", gs)
	}
}

// TestEventQueueStopsWhenTheQueueIsFinishedWithUs: a 404 says the
// simulator has forgotten this session's queue, so there is nothing left
// to close and nothing to be gained by asking again.  Whether the
// session itself is over is the circuit watchdog's business.
func TestEventQueueStopsWhenTheQueueIsFinishedWithUs(t *testing.T) {
	var polls atomic.Int64
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer hs.Close()

	a := eqAgent(hs.URL)
	done := make(chan struct{})
	go func() { defer close(done); a.runEventQueue(context.Background(), nil) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the poll kept going after the queue was gone")
	}
	if n := polls.Load(); n != 1 {
		t.Errorf("polled %d times, want one: the queue said it was finished", n)
	}
}

// TestAPollThatBringsNothingIsNotAnError: the simulator holds a poll
// open until it has something or it times out, and a reply carrying no
// events is the ordinary end of a quiet minute.
func TestAPollThatBringsNothingIsNotAnError(t *testing.T) {
	var polls atomic.Int64
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if polls.Add(1) > 1 {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.Write([]byte(`<llsd><map><key>id</key><integer>7</integer>` +
			`<key>events</key><array/></map></llsd>`))
	}))
	defer hs.Close()

	a := eqAgent(hs.URL)
	done := make(chan struct{})
	go func() { defer close(done); a.runEventQueue(context.Background(), nil) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the poll never stopped")
	}

	st := a.EventStats()
	if st.Timeouts != 1 || st.Events != 0 || st.Errors != 0 {
		t.Errorf("stats = %+v, want one timeout and nothing else", st)
	}
}

// TestEventQueueGivesUpWhenTheSessionEndsMidBackoff: a queue that is
// refusing us is usually a session that has gone, and the backoff must
// not outlive it.
func TestEventQueueGivesUpWhenTheSessionEndsMidBackoff(t *testing.T) {
	t.Parallel()

	// A server that is not there, so the request itself fails rather
	// than coming back with a status.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()

	a := eqAgent(url)
	done := make(chan struct{})
	go func() { defer close(done); a.runEventQueue(context.Background(), nil) }()

	// Two failures: the first backs off and tries again, the second is
	// still waiting when the session ends.
	deadline := time.Now().Add(5 * time.Second)
	for a.EventStats().Errors < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d failures; the poll is not retrying", a.EventStats().Errors)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(a.done)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the backoff outlived the session")
	}
}

// TestEventQueueDoesNotStartOnASessionAlreadyOver: the poll is spawned
// beside the circuit, so it can be racing a session that has ended by
// the time it runs.
func TestEventQueueDoesNotStartOnASessionAlreadyOver(t *testing.T) {
	t.Parallel()

	var polls atomic.Int64
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		w.WriteHeader(http.StatusGone)
	}))
	defer hs.Close()

	a := eqAgent(hs.URL)
	close(a.done)
	a.runEventQueue(context.Background(), nil)

	// The same for a caller that has already given up, which is the
	// other half of the same check.
	b := eqAgent(hs.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.runEventQueue(ctx, nil)

	if n := polls.Load(); n != 0 {
		t.Errorf("polled %d times for a session that had already ended", n)
	}
}

// TestARefusedPollBacksOffUntilTheCallerGivesUp: a queue answering with
// a status nobody expects is not something to spin on, and the wait must
// end the moment the caller does.
func TestARefusedPollBacksOffUntilTheCallerGivesUp(t *testing.T) {
	t.Parallel()

	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer hs.Close()

	a := eqAgent(hs.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.runEventQueue(ctx, nil) }()

	deadline := time.Now().Add(5 * time.Second)
	for a.EventStats().Errors == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the poll never noticed the refusal")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the backoff outlived the caller")
	}
}
