package main

// Putting an avatar's outfit back on after it has been logged in again.
//
// An avatar logs in wearing its body parts and nothing else.  The
// simulator rezzes no attachments of its own accord: they are named in
// the Current Outfit folder, that folder is the client's own record,
// and putting on what it names is a client's job.  A viewer does it a
// second or two after arriving.
//
// Nothing here did it, so every time slgod restarted -- or
// re-established a session that had dropped -- the avatars it holds
// came back in their skins and stayed that way until somebody noticed
// and dressed them by hand.  Which is how this was found: an avatar
// that had been dressed twice in an afternoon and was undressed again
// within the hour, each time by a restart nobody connected with it.
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
var (
	DressSettle = 15 * time.Second
	DressRetry  = 20 * time.Second
	DressPasses = 3
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
	for pass := 0; pass < DressPasses; pass++ {
		report, err := s.RestoreOutfit(ctx, 0)
		if err != nil {
			b.errf("cannot put the outfit back on: %v", err)
			return
		}
		if len(report.Worn) > 0 {
			b.logf("put back on: %s", strings.Join(report.Worn, ", "))
		}
		if len(report.Missing) == 0 {
			return
		}
		if len(report.Worn) == 0 {
			// Nothing moved and things are still unaccounted for.
			// Either they are on and the region has not said so, or
			// they are gone; asking again distinguishes neither.
			b.logf("still not described, so this cannot say whether they are on: %s",
				strings.Join(report.Missing, ", "))
			return
		}
		if !sleep(ctx, DressRetry) {
			return
		}
	}
}
