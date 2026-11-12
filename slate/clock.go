package slate

import (
	"runtime"
	"time"
)

// boundary is the time for a line between what came before it and what
// comes after: now, returned only once the clock has moved past it, so
// that anything stamped from here on is later than the line.
//
// Events are sorted to one side of a step's start by their stamps, and
// two stamps can only be told apart if the clock moved between them.
// Windows' clock moves in steps (about half a millisecond on the machine
// measured), and the fake grid in the tests answers inside one: an
// answer to the stimulus was stamped the same as the step's start and
// read as having come before it.  Elsewhere the clock moves every
// nanosecond, and this returns at once.
func boundary() time.Time {
	now := time.Now()
	for !time.Now().After(now) {
		runtime.Gosched()
	}
	return now
}
