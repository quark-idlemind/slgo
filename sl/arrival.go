package sl

import (
	"cmp"
	"slices"
	"time"
)

// nextArrival numbers one more thing waiting for an answer.  w.mu is held.
func (w *Session) nextArrival() uint64 {
	w.arrivals++
	return w.arrivals
}

// byArrival sorts things waiting for an answer by when they were
// heard, and those heard at the same time in the order they were noted.
// The time alone does not order them: Windows' clock moves in steps
// (half a millisecond on the machine measured), so two heard together
// often read the same, and these are kept in maps, which give them back
// in any order.  A shell numbers them by this order, so it has to be the
// same every time it is asked.
func byArrival[T any](out []T, key func(T) (time.Time, uint64)) {
	slices.SortFunc(out, func(a, b T) int {
		ta, sa := key(a)
		tb, sb := key(b)
		if c := ta.Compare(tb); c != 0 {
			return c
		}
		return cmp.Compare(sa, sb)
	})
}
