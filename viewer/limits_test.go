package viewer

import (
	"context"
	"fmt"
	"io"
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

// TestASeedAnswerLargerThanAnyIsRefused: the simulator's answer is read
// only as far as MaxSeedAnswer, and one longer is a 502, as the other
// failures are, with none of it handed on.
func TestASeedAnswerLargerThanAnyIsRefused(t *testing.T) {
	body := string(encode(t, map[string]any{
		"EventQueueGet": "https://sim.invalid/cap/events",
		"Padding":       strings.Repeat("x", MaxSeedAnswer),
	}))
	status, got, log := askSeed(t, http.StatusOK, body)
	if status != http.StatusBadGateway {
		t.Errorf("a %d byte answer gave the viewer %d, want %d", len(body), status, http.StatusBadGateway)
	}
	if strings.Contains(got, "/cap/") || strings.Contains(got, "xxxx") {
		t.Errorf("the viewer was handed some of the oversized answer: %.200s", got)
	}
	if !strings.Contains(log, "over") {
		t.Errorf("the log did not say why: %q", log)
	}
}

// TestASeedAnswerThatBreaksOffIsNotQuoted: a read that fails partway is
// a 502, and its error's text goes to the log, not to the viewer.
func TestASeedAnswerThatBreaksOffIsNotQuoted(t *testing.T) {
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<llsd><map><key>EventQueueGet</key>`))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	defer real.Close()
	var log syncBuffer
	s := httptest.NewServer(&Seed{
		Real: real.URL, EventQueue: "x",
		Logf: func(f string, a ...any) { fmt.Fprintf(&log, f+"\n", a...) },
	})
	defer s.Close()

	resp, err := http.Post(s.URL, "application/llsd+xml", strings.NewReader("<llsd><array/></llsd>"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("an answer that broke off gave the viewer %s, want %d", resp.Status, http.StatusBadGateway)
	}
	if strings.Contains(string(got), "EOF") {
		t.Errorf("the viewer was handed the read error: %q", got)
	}
	if !strings.Contains(log.String(), "EOF") {
		t.Errorf("the log did not get the read error: %q", log.String())
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
