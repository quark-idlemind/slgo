package agent

import (
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// offerHold is how long something addressed to the person is worth
// showing to a viewer that turns up afterwards.
//
// Long enough that starting a viewer because somebody has just offered
// you a teleport works -- which is exactly the way this was found -- and
// short enough that a viewer started an hour later is not handed a pile
// of expired offers to dismiss.  The simulator times these out at its
// own end regardless; answering a stale one gets a shrug.
const offerHold = 10 * time.Minute

// offerLimit bounds how many are kept, because a session can be talked
// at all day and nothing here is obliged to remember it.
const offerLimit = 64

// Offers is what was said to the person while no viewer was there to
// show it.
//
// The session is one client of many, and most of what a simulator says
// is state a viewer can be told again -- where things are, what they
// look like.  These are not that.  An instant message, a teleport
// offer, a script asking for permission: each is said once, addressed
// to the person rather than to the client, and a viewer is the only
// thing that can answer.  Dropped before any viewer attached, they are
// simply gone, and a teleport offer disappearing into a daemon is how
// this came to be written.
//
// Kept as the bytes on the wire rather than as decoded messages,
// because every one of these carries slices that point into the receive
// buffer and re-encoding is the honest way to take a copy of all of
// them at once.
type Offers struct {
	mu      sync.Mutex
	kept    []offer
	dropped int
}

type offer struct {
	at  time.Time
	raw []byte
}

// note keeps one message, if it can be encoded again.
func (o *Offers) note(m msg.Message, at time.Time) {
	if at.IsZero() {
		at = time.Now()
	}
	body, err := msg.Marshal(m)
	if err != nil {
		return
	}
	raw := msg.AppendID(nil, msg.IDOf(m))
	raw = append(raw, body...)

	o.mu.Lock()
	defer o.mu.Unlock()
	o.kept = append(o.kept, offer{at: at, raw: raw})
	// Oldest first: the recent ones are the ones still worth
	// answering.
	for len(o.kept) > offerLimit {
		o.kept = o.kept[1:]
		o.dropped++
	}
}

// Take hands over what is still worth showing and forgets it.
//
// Emptied rather than copied because these are shown to a person, and
// the next viewer to attach should not be told about a teleport offer
// somebody has already seen.  A viewer that attaches, detaches and
// comes back within the hold may therefore see one twice; that is the
// right way round -- twice is a nuisance, never is a lost invitation.
func (o *Offers) Take() []msg.Message { return o.take(time.Now()) }

func (o *Offers) take(now time.Time) []msg.Message {
	o.mu.Lock()
	kept := o.kept
	o.kept = nil
	o.mu.Unlock()

	out := make([]msg.Message, 0, len(kept))
	for _, k := range kept {
		if now.Sub(k.at) > offerHold {
			continue
		}
		m, err := msg.DecodeBody(k.raw)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

// Stats is how many are held and how many were forgotten for the limit.
func (o *Offers) Stats() (held, dropped int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.kept), o.dropped
}

// Offers is what was addressed to the person and not yet shown to a
// viewer.
func (a *Agent) Offers() *Offers { return &a.offers }

// keepOffers records the messages a person is meant to answer.
//
// The list is deliberately short.  Anything that describes the world
// belongs in a store that can be replayed in full, and anything sent
// several times a second would bury what matters here; what is left is
// the handful of messages that ask a question and expect a person to
// answer it.
func (a *Agent) keepOffers() {
	for _, name := range []string{
		// Instant messages, and with them teleport offers, inventory
		// offers, group invitations and friendship requests: they all
		// arrive on this one message.
		"ImprovedInstantMessage",
		// The blue menu, which a script puts up and waits on.
		"ScriptDialog",
		// A script asking for permission to animate you, take your
		// money, or attach itself.
		"ScriptQuestion",
	} {
		a.Disp.MustHandle(name, func(p *msg.Packet) {
			if p.Message != nil {
				a.offers.note(p.Message, p.At)
				if im, ok := p.Message.(*msg.ImprovedInstantMessage); ok {
					a.prices.noteInvitation(im)
				}
			}
		}, msg.Inline())
	}
}
