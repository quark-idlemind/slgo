package agent

import (
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

func healthMsg(h float32) *msg.HealthMessage {
	m := &msg.HealthMessage{}
	m.HealthData.Health = h
	return m
}

// TestHealthIsKeptAsSaidAndNotKnownUntilItIs: a session that has been
// told nothing is not at full health, it is not told; and the last
// figure wins, fractions and all.
func TestHealthIsKeptAsSaidAndNotKnownUntilItIs(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	if h, known := a.Health(); known || h != 0 {
		t.Fatalf("health %v known %v before any HealthMessage", h, known)
	}

	feed(t, a, healthMsg(100))
	if h, known := a.Health(); !known || h != 100 {
		t.Errorf("health %v known %v after 100", h, known)
	}

	feed(t, a, healthMsg(73.5), healthMsg(41.25))
	if h, known := a.Health(); !known || h != 41.25 {
		t.Errorf("health %v known %v after 73.5 and then 41.25", h, known)
	}

	// Zero is a health and not the absence of one.
	feed(t, a, healthMsg(0))
	if h, known := a.Health(); !known || h != 0 {
		t.Errorf("health %v known %v after 0", h, known)
	}
}
