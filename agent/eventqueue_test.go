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
	return &Agent{
		Account: &Account{},
		Caps:    Caps{EventQueueCap: url},
		done:    make(chan struct{}),
	}
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
	a := &Agent{Account: &Account{}, Caps: Caps{}, done: make(chan struct{})}
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
