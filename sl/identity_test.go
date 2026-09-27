package sl

import (
	"context"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// TestASessionRebuiltUnderneathIsSentUnderItsNewID: a daemon may rebuild
// the grid session under a client that stays attached: the same avatar, a
// new session id.  A client that goes on sending the old one is sending
// into silence -- the simulator discards it, nothing reports an error,
// and receiving keeps working, which is what made it take fourteen hours
// to notice.
func TestASessionRebuiltUnderneathIsSentUnderItsNewID(t *testing.T) {
	w, f := newFakeSession(t)
	was := w.Session()
	if was.IsZero() {
		t.Fatal("no session id to begin with")
	}

	// The daemon logs the avatar in again and says so the only way it
	// can: the avatar is in a region, which may even be the same one.
	next := msg.MustParseUUID("fc117e57-7e57-c0de-6733-b1fb8bb150ed")
	f.Reidentify(next)
	f.RelayRegion(t, "Example Region", 1)

	waitFor(t, "the session id to be asked for again", func() bool {
		return w.Session() == next
	})

	// And it is the new one that goes on the wire, which is the whole
	// point: everything below agentBlock was fine all along.
	f.Forget()
	if err := w.OfferFriendship(context.Background(), somebody, "hello"); err != nil {
		t.Fatal(err)
	}
	m := onlySent[*msg.ImprovedInstantMessage](t, f)
	if m.AgentData.SessionID != next {
		t.Errorf("sent under %s, want %s", m.AgentData.SessionID, next)
	}
	if m.AgentData.AgentID != w.Me() {
		t.Errorf("the avatar changed: %s", m.AgentData.AgentID)
	}
}

// An ordinary teleport asks too, and the answer is the same session.
// Telling the two apart would mean reading the words in the notice.
func TestAnOrdinaryRegionChangeAsksAndChangesNothing(t *testing.T) {
	w, f := newFakeSession(t)
	was := w.Session()

	f.RelayRegion(t, "Example Region", 1)
	waitFor(t, "the identity to be asked for", func() bool {
		return f.Refreshes() > 0
	})
	if got := w.Session(); got != was {
		t.Errorf("the session id moved on a plain teleport: %s, was %s", got, was)
	}
}

// A refresh that fails keeps what it had.  The alternative is a
// session that forgets who it is because a daemon was busy for a
// moment, which is worse than being briefly out of date.
func TestAFailedRefreshKeepsTheOldIdentity(t *testing.T) {
	w, f := newFakeSession(t)
	was := w.Session()

	f.mu.Lock()
	f.refreshErr = context.DeadlineExceeded
	f.mu.Unlock()

	f.RelayRegion(t, "Example Region", 1)
	waitFor(t, "the failed ask", func() bool { return f.Refreshes() > 0 })

	if got := w.Session(); got != was {
		t.Errorf("a failed refresh changed the identity to %s", got)
	}
	if err := w.OfferFriendship(context.Background(), somebody, "hello"); err != nil {
		t.Errorf("a failed refresh stopped the session sending: %v", err)
	}
}
