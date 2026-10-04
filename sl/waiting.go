package sl

// How long a script's dialog and its request for permission are kept
// waiting for an answer.
//
// Both are kept until they are answered, but a dialog cannot be declined
// and is often never answered at all, and a request left alone waits for
// as long as its script does.  Kept for ever, either list would grow for
// the life of the session.  So one is forgotten after UnansweredFor, and
// no more than MaxUnanswered of each are held, the oldest giving way to
// the newest.  Each list is pruned when something arrives and when it is
// read.  What goes is forgotten the way an answered one is, and told to
// OnHandled, so that whoever counts what is waiting sees the count fall.

import (
	"strconv"
	"strings"
	"time"
)

// Neither value is promised: each may change in any release, so refer to it
// by name.
const (
	// UnansweredFor is how long a dialog or a permission request is kept
	// with nobody answering it.
	UnansweredFor = time.Hour

	// MaxUnanswered is how many dialogs, and how many permission
	// requests, are kept at once.
	MaxUnanswered = 32
)

// overdue picks, from things waiting in arrival order, what to drop now
// so that room more can be added: whatever has waited UnansweredFor, and
// then the oldest until no more than MaxUnanswered-room remain.  Each
// comes with the How of the Handled that reports it.
func overdue[T any](list []T, at func(T) time.Time, now time.Time, room int) (drop []T, how []string) {
	stale := func(x T) bool { return now.Sub(at(x)) >= UnansweredFor }
	left := len(list)
	for _, x := range list {
		if stale(x) {
			drop = append(drop, x)
			how = append(how, "forgotten after "+spoken(UnansweredFor)+" unanswered")
			left--
		}
	}
	for _, x := range list {
		if left <= MaxUnanswered-room {
			break
		}
		if !stale(x) {
			drop = append(drop, x)
			how = append(how, "dropped as the oldest of more than "+
				strconv.Itoa(MaxUnanswered)+" waiting")
			left--
		}
	}
	return drop, how
}

// droppedBy is who a Handled for something overdue names.
const droppedBy = "this session"

// handledForLocked is OnHandled when there is something to tell it, and
// nil otherwise, so that a list read with nothing overdue does not read
// the field at all: a program may still be setting it on a goroutine of
// its own.  Called with mu held.
func (w *Session) handledForLocked(hs []Handled) func(Handled) {
	if len(hs) == 0 {
		return nil
	}
	return w.OnHandled
}

// tellHandled hands what was dropped to OnHandled, as handledForLocked
// read it; called without the lock.
func tellHandled(fn func(Handled), hs []Handled) {
	if fn == nil {
		return
	}
	for _, h := range hs {
		fn(h)
	}
}

// spoken is a duration without its zero tail: "1h" rather than "1h0m0s".
func spoken(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
