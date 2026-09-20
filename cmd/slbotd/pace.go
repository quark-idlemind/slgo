package main

// How long a person would have taken.
//
// A model answers in a second or two whatever it was asked, and an
// avatar that replies to "how long have you worked these berths?"
// before the question has finished arriving is not a person however
// well it writes.  The tell is not the words, it is the clock.
//
// So a reply waits.  When the remark arrived is noted; what a person
// would spend reading it and writing the answer is worked out from the
// two lengths; and the reply is held until that much time has passed
// since it arrived.  Held, not delayed -- the model's own seconds count
// towards it, so a slow model on a loaded machine may owe nothing at
// all.
//
//	read  = len(what arrived) / read-cps
//	type  = len(the answer)   / type-cps
//
// A 46 character remark read at 23 characters a second takes two
// seconds, and a 32 character answer typed at 3.2 takes ten more: the
// far end sees nothing for two seconds, then "typing..." for ten, then
// the reply.
//
// The pause is spent visibly, and that is not decoration.  Twenty
// seconds of silence reads as nobody there; twenty seconds of typing
// reads as somebody thinking what to say.  Without the notice this
// would make an avatar seem LESS alive rather than more.
//
// Characters a second rather than words a minute because it is what
// somebody setting this is actually judging -- how fast this avatar
// should seem -- and a word is a fiction of five characters that only
// exists to make typing tests comparable.  The numbers are nobody in
// particular: they are the speed of a person who does not exist.

import (
	"context"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Speeds is how fast one avatar reads and types, in characters a
// second.  A zero turns that half off.
type Speeds struct {
	Read float64
	Type float64
}

// Pace is how long a person would have taken over one exchange, split
// where the far end can see the join: the first part is silence and the
// second is "typing...".
type Pace struct {
	Read time.Duration
	Type time.Duration
}

// Total is the whole of it, and Before is everything up to the first
// keystroke.
func (p Pace) Total() time.Duration  { return p.Read + p.Type }
func (p Pace) Before() time.Duration { return p.Read }

// pace works out what one exchange would have cost the avatar named.
//
// Named, because avatars are not all the same person: one may be a
// dockhand who types with two fingers and another somebody who answers
// the instant they have read it, and that difference is more of what
// makes them separate people than the backstory is.
func (c *Chatter) pace(avatar, said, reply string) Pace {
	s := c.cfg.SpeedsFor(avatar)
	p := Pace{Read: at(len(said), s.Read), Type: at(len(reply), s.Type)}

	// The ceiling is off by default, because the arithmetic above is
	// the whole point and a cap quietly contradicts it.  It is here for
	// the operator who would rather not have an avatar typing for four
	// minutes because the model felt expansive -- which a 160 token
	// answer at 3.2 characters a second is.
	if max := c.cfg.PaceMax; max > 0 && p.Total() > max {
		// Taken off the typing, which is the part that runs away.
		// Reading is bounded by what somebody else wrote.
		if over := p.Total() - max; p.Type > over {
			p.Type -= over
		} else {
			p.Type = 0
			p.Read = max
		}
	}
	return p
}

// at is how long n characters take at a rate in characters a second.
func at(n int, cps float64) time.Duration {
	if cps <= 0 || n <= 0 {
		return 0
	}
	return time.Duration(float64(n) / cps * float64(time.Second))
}

// wait holds a reply back until a person could have written it, showing
// the far end that somebody is typing while it does.
//
// arrived is when the remark came in, NOT when this was called: the
// whole point is that the model's own time counts towards the wait.  A
// reply that took the model longer than a person would have taken to
// write goes out at once.
//
// The typing notice is sent on a best effort and its failures are not
// reported.  It is decoration on a reply that is going out regardless,
// and an avatar that refused to answer because it could not say it was
// typing would have the priorities exactly backwards.
func (b *bot) wait(ctx context.Context, s *sl.Session, to msg.UUID, arrived time.Time, p Pace) {
	if p.Total() <= 0 {
		return
	}
	elapsed := time.Since(arrived)

	// Reading: silence, if any of it is left.
	if rest := p.Before() - elapsed; rest > 0 {
		if !sleep(ctx, rest) {
			return
		}
		elapsed = time.Since(arrived)
	}

	// And the rest, spent looking like typing.
	rest := p.Total() - elapsed
	if rest <= 0 {
		return
	}
	_ = s.Typing(ctx, to, true)
	sleep(ctx, rest)
	// Stopped even when the wait was cut short, or the far end is left
	// showing "typing..." until it times the indicator out itself.
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	_ = s.Typing(stop, to, false)
	cancel()
}

// sleep waits, and says whether it got to the end.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
