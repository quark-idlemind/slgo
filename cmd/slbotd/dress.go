package main

// Putting an avatar's outfit back on after it has been logged in again.
//
// The simulator puts most of an avatar's attachments back by itself at
// login, but not all of them: it appears to put back one attachment per
// attachment point, and an outfit that has several on one point comes
// back without the rest.  See the note in sl/wearable.go for what was
// measured.  A viewer covers the gap by putting on, once the Current
// Outfit folder has loaded, whatever it names that is not on, and so
// does this.
// Why: doc/slbotd.md#putting-the-outfit-back-on
//
// This is the attendant's job rather than slgod's because the work is
// inventory work, and this side is where inventory is understood: the
// folder is read, the items are fetched out of it and the requests are
// built by sl, which slgod does not link and has no business linking.
// slgod's own restoring is for things that belong to the session --
// where the avatar is standing, what it is sitting on.

import (
	"context"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

// DressSettle is how long to wait after attaching before putting an
// outfit back on, DressRetry how long between passes, and DressPasses
// how many passes at most.
//
// The settling is not politeness.  An attach can happen while slgod is
// still logging the avatar in, and a restore that ran then would ask
// the region to rez things before the avatar was in it.
//
// Vars so that a test can shorten them.
//
// DressGiveUp bounds the retrying of a pass that failed outright, which
// does not use up a pass: that is the session being unusable, not the
// outfit being hard to put on.
var (
	DressSettle = 15 * time.Second
	DressRetry  = 20 * time.Second
	DressPasses = 3
	DressGiveUp = 5 * time.Minute
)

// keepDressed puts back on whatever the Current Outfit folder names and
// the avatar is not wearing.
//
// Passes rather than one attempt, because the first one may be too
// early: the region hands over its contents gradually, and an
// attachment asked for before the avatar is properly in the region is
// answered with nothing.
//
// It stops as soon as a pass finds nothing left to ask for, and it
// stops early when a pass PUT NOTHING ON while still reporting things
// missing.  The second is the important one: it is what an avatar whose
// attachments are all on but undescribed looks like, and asking again
// would achieve nothing except another round of requests.
func (b *bot) keepDressed(ctx context.Context, s *sl.Session) {
	if !sleep(ctx, DressSettle) {
		return
	}
	giveUp := time.Now().Add(DressGiveUp)
	for pass := 0; pass < DressPasses; pass++ {
		report, err := s.RestoreOutfit(ctx, 0)
		if err != nil {
			// Tried again rather than given up on: the session an
			// attach lands on can be one about to be replaced.
			// Why: doc/slbotd.md#putting-the-outfit-back-on
			b.errf("cannot put the outfit back on: %v", err)
			if !time.Now().Before(giveUp) || !sleep(ctx, DressRetry) {
				return
			}
			pass--
			continue
		}
		if len(report.Worn) > 0 {
			b.logf("put back on: %s", strings.Join(report.Worn, ", "))
		}
		if report.Unbaked != nil {
			b.errf("not rebaked after putting things on: %v", report.Unbaked)
		}
		if len(report.Doubled) > 0 {
			// Adding is what makes this possible, and it is the
			// trade for not knocking a garment off a shared point.
			// Say so: nothing else will, since two attachments from
			// one item are alike in every field.
			b.errf("worn more than once, which detach undoes: %s",
				strings.Join(report.Doubled, ", "))
		}
		if len(report.Missing) == 0 {
			return
		}
		if len(report.Worn) == 0 {
			// Nothing moved and things are still unaccounted for.
			// Either they are on and the region has not said so, or
			// they are gone; asking again distinguishes neither.  The
			// simulator's list narrows it when it can: it says how
			// many are on that nothing has described.
			switch {
			case report.Unknown:
				b.logf("still not described, so this cannot say whether they are on: %s",
					strings.Join(report.Missing, ", "))
			case report.Undescribed > 0:
				b.logf("not described, and the simulator lists %d attachments nothing has "+
					"described, so some may be on: %s",
					report.Undescribed, strings.Join(report.Missing, ", "))
			default:
				// HUDs are never in the simulator's list, so for a HUD
				// this says nothing either way.
				b.errf("not put back on, and the simulator lists nothing undescribed, so "+
					"unless they are HUDs they are off: %s",
					strings.Join(report.Missing, ", "))
			}
			return
		}
		if !sleep(ctx, DressRetry) {
			return
		}
	}
}
