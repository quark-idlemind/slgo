package agent

// What Connect wires up on the way past.
//
// Connect is the only place several things are ever installed -- the
// packet tap, the two hooks that report what nobody handled or could
// not decode, the capability fetch, the event queue poll -- and each of
// them is one branch that either happens or silently does not.  A
// capability fetch that was skipped looks exactly like a simulator that
// offered nothing, and a session with no OnUnhandled is how a protocol
// change goes unnoticed for a year.
//
// These run against the fake simulator in agent_test.go, so there is
// still no grid involved.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// rawSend puts bytes on the wire without building a message out of
// them, which is the only way to produce a packet that will not decode.
func rawSend(f *fakeSim, b []byte) {
	f.mu.Lock()
	peer := f.peer
	f.mu.Unlock()
	if peer != nil {
		f.conn.WriteToUDP(b, peer)
	}
}

// TestConnectNeedsAnAccountItCanReach: both of these fail before
// anything is started, and getting that wrong leaves a socket and three
// goroutines behind for a session that never existed.
func TestConnectNeedsAnAccountItCanReach(t *testing.T) {
	t.Parallel()

	if _, err := Connect(context.Background(), nil, Options{}); err == nil {
		t.Error("expected an error for a session with no account")
	}

	bad := &Account{SimIP: net.IPv4(127, 0, 0, 1), SimPort: 70000}
	if _, err := Connect(context.Background(), bad, Options{Timeout: time.Second}); err == nil {
		t.Error("expected an error for an address that is not one")
	}
}

// TestConnectTakesItsDefaults: a caller that fills in nothing must still
// get a working session, since Options is mostly for turning things off.
func TestConnectTakesItsDefaults(t *testing.T) {
	t.Parallel()

	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	a, err := Connect(context.Background(), testAccount(sim), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if l := a.Look(); l.Far != DefaultDrawDistance {
		t.Errorf("draw distance = %v, want the default", l.Far)
	}
}

// TestConnectReportsWhatNobodyHandled: this is how a protocol change
// announces itself.  The C client only ever noticed CameraConstraint
// because unhandled messages got dumped.
func TestConnectReportsWhatNobodyHandled(t *testing.T) {
	t.Parallel()

	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	var mu sync.Mutex
	var tapped, unhandled, failed int
	a, err := Connect(context.Background(), testAccount(sim), Options{
		Timeout: 5 * time.Second,
		Tap: func(*msg.Packet) {
			mu.Lock()
			tapped++
			mu.Unlock()
		},
		OnUnhandled: func(*msg.Packet) {
			mu.Lock()
			unhandled++
			mu.Unlock()
		},
		OnError: func(p *msg.Packet) {
			mu.Lock()
			failed++
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	// Something nobody registered for.
	chat := &msg.ChatFromSimulator{}
	chat.ChatData.FromName = []byte("Someone\x00")
	chat.ChatData.Message = []byte("hello there\x00")
	sim.send(chat, 0)

	// And something that names a message but carries none of it, which
	// is what a truncated datagram looks like from here.
	truncated := msg.AppendHeader(nil, &msg.Header{Sequence: 9999})
	truncated = msg.AppendID(truncated, msg.IDOf(&msg.ObjectUpdate{}))
	rawSend(sim, truncated)

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		done := unhandled > 0 && failed > 0 && tapped > 0
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			mu.Lock()
			t.Fatalf("tapped %d, unhandled %d, undecodable %d", tapped, unhandled, failed)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestConnectFetchesTheCapabilities: almost everything above this layer
// needs them, so they are fetched here rather than left for the caller
// to remember.  The event queue starts after them, since it is polled
// through one.
func TestConnectFetchesTheCapabilities(t *testing.T) {
	t.Parallel()

	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	polled := make(chan struct{}, 1)
	var once sync.Once
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/eq") {
			once.Do(func() { close(polled) })
			// The queue is finished with us, which is how a poll ends
			// without waiting one out.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `<llsd><map>
			<key>EventQueueGet</key><string>%s/eq</string>
			<key>InventoryAPIv3</key><string>%s/inv</string>
		</map></llsd>`, "http://"+r.Host, "http://"+r.Host)
	}))
	defer hs.Close()

	acct := testAccount(sim)
	acct.SeedCapability = hs.URL + "/seed"

	a, err := Connect(context.Background(), acct, Options{
		Timeout: 5 * time.Second,
		Caps:    []string{"EventQueueGet", "InventoryAPIv3"},
		OnEvent: func(string, []byte) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if !a.HasCap("InventoryAPIv3") {
		t.Errorf("capabilities = %v", a.Caps.Names())
	}
	select {
	case <-polled:
	case <-time.After(5 * time.Second):
		t.Error("the event queue was never polled")
	}
}

// TestConnectFailsWhenTheCapabilitiesDo: a session without them is one
// that cannot read its own inventory or hear an event, so it is better
// reported as a failed login than handed over half working.
func TestConnectFailsWhenTheCapabilitiesDo(t *testing.T) {
	t.Parallel()

	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no capabilities for you", http.StatusInternalServerError)
	}))
	defer hs.Close()

	acct := testAccount(sim)
	acct.SeedCapability = hs.URL + "/seed"

	a, err := Connect(context.Background(), acct, Options{Timeout: 5 * time.Second})
	if err == nil {
		a.Close()
		t.Fatal("expected the capability failure to fail the connect")
	}
	if !strings.Contains(err.Error(), "seed capability") {
		t.Errorf("err = %v", err)
	}
}

// TestTheWatchdogStopsWithTheSessionItIsWatching: it outlives nothing.
// A goroutine still ticking over a session that has gone would keep the
// whole Agent alive with it.
func TestTheWatchdogStopsWithTheSessionItIsWatching(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() { defer close(done); a.watchdog(ctx, time.Hour) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watchdog ignored its context")
	}
}

// TestAHandshakeOnACircuitThatIsAlreadyGone: the first step is a send,
// and a send that fails has to be reported as the step it was rather
// than as a timeout waiting for an answer nobody was ever asked for.
func TestAHandshakeOnACircuitThatIsAlreadyGone(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	// Stopping the sender is what a dead circuit looks like from here.
	a.cancel()
	deadline := time.Now().Add(2 * time.Second)
	for a.Send.Send(context.Background(), &msg.LogoutRequest{}) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the sender would not stop")
		}
		time.Sleep(time.Millisecond)
	}

	err := a.handshake(context.Background(), 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "UseCircuitCode") {
		t.Errorf("err = %v, should name the step that failed", err)
	}
}

// TestACircuitThatDiesBetweenTheTwoSteps: the handshake is two sends
// with a wait in between, and the second one is where a circuit that
// opened and then went away shows up.
func TestACircuitThatDiesBetweenTheTwoSteps(t *testing.T) {
	t.Parallel()

	a := &Agent{
		Account: &Account{},
		// A socket that has gone: the first datagram is queued
		// happily and stops the sender on its way out.
		Send:      msg.NewSender(refusesToWrite{}),
		done:      make(chan struct{}),
		anyPacket: newSignal(),
		inRegion:  newSignal(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The circuit is reported up only once the sender has given out, so
	// the first send is the one that succeeds and the second is the one
	// that cannot.
	go func() { _ = a.Send.Run(ctx); a.anyPacket.fire() }()

	err := a.handshake(ctx, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "CompleteAgentMovement") {
		t.Errorf("err = %v, should name the step that failed", err)
	}
}

// refusesToWrite is a socket that is no longer there.
type refusesToWrite struct{}

func (refusesToWrite) Write(p []byte) (int, error) { return 0, net.ErrClosed }
