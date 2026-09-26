package session

// Stopping whatever is running in an object, before listening to it.
//
// # Why it has to be done at all
//
// Chat carries the OBJECT a line came from and never the script's name.
// That was measured, and it is the whole reason this exists: a script
// left running by whoever had the object last says things, and the next
// caller's collector -- which can only filter on the object -- reads
// them as its own output.  A run that reports somebody else's numbers is
// not a failure, it is a plausible answer, which is the worst kind.
//
// # Why an empty script rather than removing them
//
// Installing a script over one of the same name destroys the one that
// was there, measured: the old script never says another word.  So an
// empty script silences whatever was running and leaves the ITEM in
// place -- and an item already there is what makes the next install cost
// about 0.9s instead of the 8.1s creating one costs.  Removing the
// scripts would be tidier and would make every later run slower.
//
// # Why it waits to hear from it
//
// The upload's answer says the new script is in; it says nothing about
// what the old one had already said.  Chat arrives on the UDP path with
// no ordering relationship to an HTTP reply, so the only honest way to
// know the noise is over is to hear something come the same way after
// it.  The empty script says a word nobody else could say, and the
// clearing is finished when that word arrives.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

// clearTimeout bounds one script's clearing.  An object that will not
// answer is one to give up on rather than wait for: it has probably gone.
const clearTimeout = 30 * time.Second

// Clear silences every script in an object and waits until it has
// stopped talking.
//
// It is what a caller does with a place the daemon handed it marked
// dirty, before it listens to anything.
func Clear(ctx context.Context, s *sl.Session, obj *sl.Object) error {
	items, err := s.TaskInventory(ctx, obj)
	if err != nil {
		return fmt.Errorf("reading what %s holds: %w", obj, err)
	}

	for _, it := range items {
		if it.Type != "lsltext" {
			continue
		}
		if err := clearScript(ctx, s, obj, it.Name); err != nil {
			return err
		}
	}
	return nil
}

// clearScript replaces one script with an empty one and waits to hear
// the object say so.
func clearScript(ctx context.Context, s *sl.Session, obj *sl.Object, name string) error {
	// A word nobody else could say.  Not a fixed one however odd: the
	// point is that a script still running cannot produce it, and a
	// fresh one each time is that by construction rather than by
	// choosing an alphabet nobody would use.
	word := "cleared-" + randomWord()

	res, err := s.Run(ctx, sl.Script{
		In:     obj,
		Name:   name,
		Source: fmt.Sprintf("default { state_entry() { llOwnerSay(\"%s\"); } }", word),
		// Everything the old script says arrives while this waits, and
		// is thrown away with the result.
		Done:    word,
		Timeout: clearTimeout,
	})
	if err != nil {
		return fmt.Errorf("clearing %q in %s: %w", name, obj, err)
	}
	if !res.Compiled {
		return fmt.Errorf("clearing %q in %s: it would not compile: %v",
			name, obj, res.Errors)
	}
	if res.Blocked != "" {
		return fmt.Errorf("clearing %q in %s: %s", name, obj, res.Blocked)
	}
	if !res.Finished {
		// The object took the script and never spoke.  Whatever was in
		// there is stopped -- installing over a name destroys what it
		// had -- but nothing can be said about what else is going on,
		// so this is not a place to hand on as clean.
		return fmt.Errorf("clearing %q in %s: it did not answer within %v",
			name, obj, clearTimeout)
	}
	return nil
}

func randomWord() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice, and a word that
		// repeats only matters if the same object is being cleared
		// twice at once, which one caller holding it makes impossible.
		return fmt.Sprint(time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
