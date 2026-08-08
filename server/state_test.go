package server

// The backoff, on its own.
//
// state.go's other half -- what an agent's state is -- is reached
// through the calls that report it, and lives with them.  What cannot
// be reached that way is the arithmetic at the edges: a count of
// attempts that is somehow zero, or one that has gone past the end of
// the table, both of which have to yield a legal wait rather than
// index out of range.

import (
	"testing"
	"time"
)

func TestTheRetryWaitStaysInsideTheTable(t *testing.T) {
	t.Parallel()

	first := ReconnectDelays[0]
	last := ReconnectDelays[len(ReconnectDelays)-1]

	// A first attempt, and a count that never happened: both wait the
	// shortest time rather than falling off the front of the table.
	for _, tries := range []int{0, -1, 1} {
		if got := retryAfter(tries); got != first {
			t.Errorf("retryAfter(%d) = %v, want %v", tries, got, first)
		}
	}

	// And a session that has been failing all afternoon waits the
	// longest, repeating, rather than growing without bound.
	for _, tries := range []int{len(ReconnectDelays), len(ReconnectDelays) + 1, 1000} {
		if got := retryAfter(tries); got != last {
			t.Errorf("retryAfter(%d) = %v, want %v", tries, got, last)
		}
	}

	// The waits are generous on purpose: a login server throttles a
	// client that hammers it, and the throttle presents as a different
	// fault entirely.
	if first < time.Second {
		t.Errorf("the first retry waits %v, which is fast enough to look like hammering", first)
	}
}
