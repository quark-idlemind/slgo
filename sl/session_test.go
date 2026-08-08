package sl

// The session itself: what it is made of, what it remembers, and what
// the one reader goroutine does with everything that arrives.
//
// The bookkeeping in handle is what every other call in the package
// waits on.  Nothing here replies to anything: a link is confirmed by an
// update that would have arrived anyway, a name by a reply to a
// different question, and a refusal usually arrives as an AlertMessage
// addressed to nobody.  So the maps this fills are the only evidence
// most calls ever get, and a message dropped here is a call that hangs
// somewhere else entirely.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// TestASessionNeedsToKnowWhoItIs: every message starts with the agent
// and session ids, so a backend that gave neither would produce a
// session whose every call is refused by the simulator for a reason it
// never explains.
func TestASessionNeedsToKnowWhoItIs(t *testing.T) {
	f := newFake(t)

	f.info = &Info{Name: "no ids"}
	if _, err := New(f); err == nil || !strings.Contains(err.Error(), "agent id") {
		t.Errorf("New = %v, want it to say there was no agent id", err)
	}

	f.info = &Info{Name: "half", AgentID: testAgentID}
	if _, err := New(f); err == nil || !strings.Contains(err.Error(), "session id") {
		t.Errorf("New = %v, want it to say there was no session id", err)
	}
}

// TestASessionAnswersForWhatItIsAttachedTo: a program holding one has
// to be able to ask who it is and how to reach past the package, and
// none of it is worth a round trip.
func TestASessionAnswersForWhatItIsAttachedTo(t *testing.T) {
	w, f := newFakeSession(t)

	if w.Me() != testAgentID || w.Session() != testSessionID {
		t.Errorf("the session is %s/%s", w.Me(), w.Session())
	}
	if w.InventoryRoot() != testInvRoot {
		t.Errorf("the inventory root is %s", w.InventoryRoot())
	}
	if w.Info().AvatarName != "Quark Idlemind" {
		t.Errorf("Info = %+v", w.Info())
	}
	// The capability list is what a call checks before trying to use
	// one, since asking for a capability the simulator never offered is
	// a request to nowhere rather than an error the simulator explains.
	w.Info().Caps = []string{"SimulatorFeatures", "ViewerAsset"}
	if !w.Info().HasCap("ViewerAsset") {
		t.Error("HasCap missed a capability that was offered")
	}
	if w.Info().HasCap("LSLSyntax") {
		t.Error("HasCap found a capability that was not offered")
	}
	// The backend itself, for the few things only one kind can do:
	// listing the sessions a daemon holds, logging the avatar out.
	if w.Backend() != Backend(f) {
		t.Error("Backend answered with something other than what it was built on")
	}

	// Done and Err come straight from the backend, because a session
	// ends when the thing holding it does.
	select {
	case <-w.Done():
		t.Error("a session that is up says it has ended")
	default:
	}
	if err := w.Err(); err != nil {
		t.Errorf("Err = %v on a session that has not ended", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-w.Done():
	case <-time.After(5 * time.Second):
		t.Error("a closed session never said it had ended")
	}
}

// TestLocksGoStraightToWhoeverCanHoldOne: a lock exists so that two
// clients of one daemon do not use the same object at once, which is
// something only the thing holding the session can arbitrate.
func TestLocksGoStraightToWhoeverCanHoldOne(t *testing.T) {
	w, f := newFakeSession(t)

	if err := w.Lock(context.Background(), "the workbench"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	f.mu.Lock()
	held := f.locks["the workbench"]
	f.mu.Unlock()
	if !held {
		t.Error("Lock did not reach the backend")
	}

	if err := w.Unlock("the workbench"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	got, by, err := w.TryLock(context.Background(), "the workbench")
	if err != nil || !got || by != "" {
		t.Errorf("TryLock = %v, %q, %v", got, by, err)
	}

	// Somebody else holding it is an answer rather than a failure: it
	// is what lets a caller move on to another of a pool.
	f.mu.Lock()
	f.lockedBy = "another client"
	f.mu.Unlock()
	if got, by, err = w.TryLock(context.Background(), "the workbench"); err != nil || got {
		t.Errorf("TryLock took a lock somebody else holds: %v, %v", got, err)
	}
	if by != "another client" {
		t.Errorf("TryLock said it was held by %q", by)
	}

	f.mu.Lock()
	f.lockErr = errors.New("no session")
	f.mu.Unlock()
	if err := w.Lock(context.Background(), "the workbench"); err == nil {
		t.Error("Lock reported taking one from a backend that refused")
	}
	if _, _, err := w.TryLock(context.Background(), "the workbench"); err == nil {
		t.Error("TryLock reported on a backend that refused")
	}
}

// TestSettleWaitsAndCanBeGivenUpOn: a session that has just connected
// has been told about nothing, so several calls have to do nothing for a
// moment -- and a caller that has changed its mind must not have to wait
// out somebody else's constant.
func TestSettleWaitsAndCanBeGivenUpOn(t *testing.T) {
	w, _ := newFakeSession(t)

	start := time.Now()
	if err := w.Settle(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if time.Since(start) < 10*time.Millisecond {
		t.Error("Settle did not wait at all")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Settle(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("Settle = %v, want the context's reason", err)
	}
}

// TestAKilledObjectIsRememberedAsGone: an object that has been taken or
// deleted goes on being in the maps that describe it, so the only record
// that it is no longer there is the one KillObject leaves.
func TestAKilledObjectIsRememberedAsGone(t *testing.T) {
	w, f := newFakeSession(t)

	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: thePrim, ID: 77}))
	w.mu.Lock()
	killed := w.killed[77]
	w.mu.Unlock()
	if killed {
		t.Fatal("a fresh object was already recorded as killed")
	}

	f.Relay(t, &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: 77}}})
	w.mu.Lock()
	killed = w.killed[77]
	w.mu.Unlock()
	if !killed {
		t.Error("KillObject left nothing behind saying the object had gone")
	}

	// And it comes back if the region describes it again, which is what
	// happens when an object is returned rather than deleted.
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: thePrim, ID: 77}))
	w.mu.Lock()
	killed = w.killed[77]
	w.mu.Unlock()
	if killed {
		t.Error("an object described again is still remembered as killed")
	}
}

// TestAlertsAreKeptBecauseNothingElseCarriesARefusal: a simulator that
// will not do something usually says so in an AlertMessage addressed to
// nobody, rather than as a reply to the thing refused -- so without
// these a refusal is indistinguishable from a slow simulator.
func TestAlertsAreKeptBecauseNothingElseCarriesARefusal(t *testing.T) {
	w, f := newFakeSession(t)

	said := make(chan string, 4)
	w.mu.Lock()
	w.OnAlert = func(s string) { said <- s }
	w.mu.Unlock()

	f.Relay(t, alert("Can't rez object at { 1, 1, 1 } because the owner "+
		"of this land does not allow it."))
	select {
	case got := <-said:
		if !strings.Contains(got, "does not allow it") {
			t.Errorf("OnAlert was called with %q", got)
		}
	default:
		t.Error("OnAlert was not called")
	}

	if got := w.Alerts(); len(got) != 1 {
		t.Errorf("Alerts = %q", got)
	}
}

// TestATimeoutQuotesWhatTheSimulatorSaidWhileWaiting: a caller told only
// that nothing happened has been given the one fact that never explains
// anything, and the explanation was usually sitting in the alert log.
func TestATimeoutQuotesWhatTheSimulatorSaidWhileWaiting(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	// Alerts from before the wait began are not this call's business:
	// quoting them would blame a refusal on the wrong request.
	f.Relay(t, alert("an older complaint"))
	// A dialog nothing is looking for, so that the wait has something
	// to run its match against -- which is how the test knows the wait
	// has started and taken its mark.  Relaying the alert before that
	// would put it among the ones from before, and the test would be
	// asserting the opposite of what it means to.
	f.Relay(t, testScriptDialog())

	marked := make(chan struct{})
	var once sync.Once
	wait := aside(t, func() (Dialog, error) {
		return w.WaitDialog(context.Background(), 700*time.Millisecond,
			func(Dialog) bool {
				once.Do(func() { close(marked) })
				return false
			})
	})
	<-marked
	f.Relay(t, alert("the land does not allow it"))

	_, err := wait()
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("WaitDialog = %v, want a timeout", err)
	}
	if !strings.Contains(err.Error(), "the land does not allow it") {
		t.Errorf("the timeout did not quote the alert: %v", err)
	}
	if strings.Contains(err.Error(), "an older complaint") {
		t.Errorf("the timeout quoted an alert from before it started: %v", err)
	}
}

// alert is the simulator complaining to nobody in particular.
func alert(text string) *msg.AlertMessage {
	m := &msg.AlertMessage{}
	m.AlertData.Message = append([]byte(text), 0)
	return m
}

// TestTheReaderThrowsAwayWhatItCannotRead: the server relays whatever it
// was subscribed to, including numbers this build's template does not
// have, and a reader that fell over one of them would take the session
// with it.
func TestTheReaderThrowsAwayWhatItCannotRead(t *testing.T) {
	w, f := newFakeSession(t)

	// A message this build does not know, and one it knows but whose
	// body is nonsense.
	f.RelayRaw(t, &Message{ID: barrierID, Name: "not in the template", At: time.Now()})
	f.RelayRaw(t, &Message{
		ID: msg.IDOf(&msg.ObjectUpdate{}), Name: "ObjectUpdate",
		Body: []byte{0xff, 0xff, 0xff}, At: time.Now(),
	})

	// Still going, and still handling what it can read.
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: thePrim, ID: 77}))
	w.mu.Lock()
	local := w.locals[thePrim]
	w.mu.Unlock()
	if local != 77 {
		t.Error("the reader stopped on a message it could not decode")
	}
}

// TestAFailedCapabilityQuotesOnlyTheStartOfWhatCameBack: a capability
// that refuses often answers with a page of html, and an error carrying
// all of it is unreadable in a log.
func TestAFailedCapabilityQuotesOnlyTheStartOfWhatCameBack(t *testing.T) {
	w, f := newFakeSession(t)
	long := strings.Repeat("x", 1000)
	f.ServeCap(t, "SimulatorFeatures", func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(500)
		fmt.Fprint(rw, long)
	})

	_, err := w.Features(context.Background())
	if err == nil {
		t.Fatal("Features reported on a capability that answered 500")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("the error does not say what came back: %v", err)
	}
	if len(err.Error()) > 500 {
		t.Errorf("the error carries %d bytes of the body", len(err.Error()))
	}

	// A request by URL rather than by capability name has no name to
	// put in the message, so it names the URL instead.
	f.mu.Lock()
	base := f.caps["SimulatorFeatures"]
	f.mu.Unlock()
	_, err = w.capDo(context.Background(), agent.CapRequest{URL: base + "/x", Method: "GET"})
	if err == nil || !strings.Contains(err.Error(), base) {
		t.Errorf("capDo = %v, want it to name the URL it asked", err)
	}
}

// TestDialNeedsADaemonToDial: there is no daemon here, and the failure
// has to name the address rather than leave somebody wondering which of
// several it tried.
func TestDialNeedsADaemonToDial(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	// Port 1 on loopback: nothing listens there and nothing has to.
	if _, err := Dial(ctx, "127.0.0.1:1", ""); err == nil {
		t.Error("Dial attached to a daemon that is not running")
	}
}

// TestLoginDirectRefusesCredentialsItCannotUse: a login with no name is
// refused before anything is sent, since the login server would answer
// with something less clear about it.
func TestLoginDirectRefusesCredentialsItCannotUse(t *testing.T) {
	t.Parallel()
	if _, err := LoginDirect(context.Background(), agent.Login{}); err == nil {
		t.Error("LoginDirect logged in with no credentials at all")
	}
}
