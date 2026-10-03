package slate

import (
	"testing"
	"time"
)

// A line that waited in the buffer while the runner was busy keeps the
// time it arrived; a line that does not say, or says a time not yet come,
// is stamped now.
func TestALineIsStampedWhenItArrived(t *testing.T) {
	waited := time.Now().Add(-4500 * time.Millisecond)
	if got := arrived(waited); !got.Equal(waited) {
		t.Fatalf("stamped %v, want %v", got, waited)
	}
	for _, at := range []time.Time{{}, time.Now().Add(time.Hour)} {
		before := time.Now()
		if got := arrived(at); got.Before(before) || got.After(time.Now()) {
			t.Fatalf("arrived(%v) = %v, want now", at, got)
		}
	}
}
