package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Limits on what a request may make these handlers hold.
//
// The login is reached by anybody who can reach the port and the other
// two by a viewer holding a capability, and in each a request used to
// be read for as long as the sender kept sending, or held for as long
// as it liked alongside as many others as it liked.

// padded is a login call that would be a good one but for being longer
// than any login: the padding is a parameter no viewer sends, after the
// ones that matter, so that a decoder must read past the limit to reach
// the end.
func padded(t *testing.T) string {
	call := loginCall(t, map[string]any{
		"first": "Taren", "last": "Holt", "passwd": "secret",
		"padding": strings.Repeat("x", MaxRequestBody),
	})
	if len(call) <= MaxRequestBody {
		t.Fatalf("the padded call is only %d bytes", len(call))
	}
	return call
}

// TestALoginLargerThanAnyLoginIsNotRead: the body is cut off at the
// limit, so the call is refused as unreadable and nobody is looked up.
func TestALoginLargerThanAnyLoginIsNotRead(t *testing.T) {
	var looked atomic.Int32
	s := httptest.NewServer(LoginHandler(func(first, last string) *Handover {
		looked.Add(1)
		return testHandover()
	}, func(string, ...any) {}))
	defer s.Close()

	// The server may hang up on the rest of the body once it has said
	// no, which the client can see as an error writing it.  Either is
	// a refusal; what matters is that nothing was looked up.
	if resp, err := http.Post(s.URL, "text/xml", strings.NewReader(padded(t))); err == nil {
		resp.Body.Close()
	}
	if n := looked.Load(); n != 0 {
		t.Errorf("a %d byte login was read to its end and looked up %d times", len(padded(t)), n)
	}
}

// TestASeedRequestLargerThanAnyIsRefused: refused, and not passed on to
// the simulator either whole or cut short.
func TestASeedRequestLargerThanAnyIsRefused(t *testing.T) {
	var asked atomic.Int32
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Write(encode(t, map[string]any{"EventQueueGet": "https://sim.example/eq"}))
	}))
	defer real.Close()
	s := httptest.NewServer(&Seed{Real: real.URL, EventQueue: "x", Logf: func(string, ...any) {}})
	defer s.Close()

	resp, err := http.Post(s.URL, "application/llsd+xml",
		strings.NewReader(strings.Repeat(" ", MaxRequestBody+1)))
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("an oversized seed request = %s, want %d", resp.Status, http.StatusRequestEntityTooLarge)
		}
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("the simulator was asked %d times on an oversized request's behalf", n)
	}
}

// TestOnlySoManyPollsAreHeldAtOnce: each held poll is a request and a
// goroutine for PollHold, so one past the limit is answered at once as
// unavailable rather than held with the rest.
func TestOnlySoManyPollsAreHeldAtOnce(t *testing.T) {
	q := NewEventQueue()
	s := httptest.NewServer(q)
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < MaxHeldPolls; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
		}()
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		q.mu.Lock()
		held := q.held
		q.mu.Unlock()
		if held == MaxHeldPolls {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d polls were ever held", held)
		}
		time.Sleep(10 * time.Millisecond)
	}

	answered := make(chan int, 1)
	go func() {
		resp, err := http.Post(s.URL, "application/llsd+xml", nil)
		if err != nil {
			answered <- 0
			return
		}
		resp.Body.Close()
		answered <- resp.StatusCode
	}()
	select {
	case code := <-answered:
		if code != http.StatusServiceUnavailable {
			t.Errorf("a poll past the limit = %d, want %d", code, http.StatusServiceUnavailable)
		}
	case <-time.After(PollHold / 2):
		t.Fatalf("a poll past the limit of %d was held with the rest", MaxHeldPolls)
	}

	// And the places come back when the polls end.
	cancel()
	deadline = time.Now().Add(20 * time.Second)
	for {
		q.mu.Lock()
		held := q.held
		q.mu.Unlock()
		if held == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d polls still counted as held after every one ended", held)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
