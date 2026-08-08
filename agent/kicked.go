package agent

// Being thrown off, as opposed to falling off.
//
// A session can end in two quite different ways and the difference is
// the whole of what a supervisor needs to know:
//
//   - The circuit failed.  The network went away, the simulator went
//     quiet, a packet was lost once too often.  Nobody decided
//     anything, so getting the session back is right and the only
//     question is how soon.
//
//   - The grid ENDED it.  Somebody logged this avatar in somewhere
//     else, an estate banned it, an administrator kicked it.  A
//     decision was made, and logging straight back in overrules it.
//
// The second case is the one that matters here, because the obvious
// implementation gets it exactly backwards.  A supervisor that treats
// every ending as something to recover from will, when you log your
// avatar into a viewer, wait five seconds and take it back -- and then
// do it again, for as long as you keep trying.  The daemon would be
// fighting its own operator, and it would win.
//
// So a kick is reported as this rather than as a plain error, and a
// caller tells the two apart with errors.As.  Every kick is treated the
// same way: whatever the grid's reason, being ejected and immediately
// returning is the wrong answer to all of them, and the reason is
// carried along so that a person can see which it was.

import "errors"

// Kicked is a session the grid ended deliberately.  It is not
// retryable: see Retryable.
type Kicked struct {
	// Reason is the grid's own words, which are meant for a person and
	// are the only explanation there is.  It may be empty.
	Reason string
}

func (k *Kicked) Error() string {
	if k.Reason == "" {
		return "agent: kicked, and the grid said no more than that"
	}
	return "agent: kicked: " + k.Reason
}

// Kicked reports the grid's reason, if this session was ended by one.
func (a *Agent) Kicked() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.kicked == "" {
		return "", false
	}
	return a.kicked, true
}

// Retryable says whether a session that ended this way should be
// re-established.
//
// Everything is, except a deliberate ejection.  That is the safe
// default: a fault nobody understands is more likely to be a lost
// circuit than a decision, and failing to reconnect is visible and
// recoverable while reconnecting when told not to is neither.
func Retryable(err error) bool {
	var k *Kicked
	return !errors.As(err, &k)
}
