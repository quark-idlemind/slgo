package sl

// Teleport offers.
//
// Somebody offering a teleport sends an instant message of dialog 22
// carrying a lure id, and that id is the only thing that can answer it.
// It is not derivable from anything else and the offer expires, so it
// is kept when it arrives rather than left to whoever happened to be
// listening at that moment.
//
// Accepting sends TeleportLureRequest, which is what the viewer sends
// from its own dialog (llagent.cpp:5218, doTeleportViaLure).  Declining
// sends an instant message back with dialog 24, which is the notice the
// other side is waiting for; saying nothing leaves them watching a
// dialog that never resolves.

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// DialogLureDeclined is the instant message that tells the other side
// no (llinstantmessage.h:127, IM_LURE_DECLINED).
const DialogLureDeclined = 24

// teleportViaLure is the flag the viewer sends when a person accepts an
// ordinary offer (llteleportflags.h:33).  The viewer's own comment says
// the simulator derives this now and the field is legacy, so it is sent
// for the sake of matching rather than because it decides anything.
const teleportViaLure = 1 << 2

// Lure is somebody's offer to teleport this avatar to them.
type Lure struct {
	At time.Time

	// From is who offered and Name what they are called.
	From msg.UUID
	Name string

	// Text is what they said with it, which is where the destination
	// appears if it appears at all: the region is not a field of the
	// message, it is whatever the sender's viewer wrote into the
	// message ("Join me in Pelmar Reach!").
	Text string

	// ID is the lure, and the only thing that can accept it.
	ID msg.UUID
}

func (l Lure) String() string {
	who := l.Name
	if who == "" {
		who = l.From.String()
	}
	if l.Text == "" {
		return who + " offers a teleport"
	}
	return fmt.Sprintf("%s offers a teleport: %q", who, l.Text)
}

// noteLure keeps an offer, replacing any earlier one from the same
// person: a second offer supersedes the first, and answering the stale
// id sends the person somewhere they have stopped waiting.
func (w *Session) noteLure(im *IM) {
	name := im.FromName
	if name == "" {
		name = w.NameOr(im.From)
	}
	w.mu.Lock()
	if w.lures == nil {
		w.lures = map[msg.UUID]*Lure{}
	}
	w.lures[im.From] = &Lure{
		At: im.At, From: im.From, Name: name, Text: im.Text, ID: im.ID,
	}
	w.mu.Unlock()
}

// Lures returns the teleport offers waiting for an answer, oldest
// first.
func (w *Session) Lures() []*Lure {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*Lure, 0, len(w.lures))
	for _, l := range w.lures {
		out = append(out, l)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].At.Before(out[j-1].At); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ForgetLure drops one without answering it, which is what a person
// does by ignoring a dialog.
func (w *Session) ForgetLure(l *Lure) {
	if l == nil {
		return
	}
	w.mu.Lock()
	delete(w.lures, l.From)
	w.mu.Unlock()
}

// AcceptLure takes the teleport, and waits until the avatar is where it
// was invited to.
//
// Where that is cannot be known before it happens.  An offer says who
// made it and whatever they typed with it; the region is not a field of
// the message, and the only place the grid ever names the destination is
// the TeleportFinish that arrives once the teleport is already under
// way.  So this waits for wherever the avatar turns out to be going,
// where Teleport waits for the region it asked for.
//
// A lure to somewhere in the region already occupied is answered with a
// TeleportLocal and no finish at all, which is the ordinary case and
// returns as soon as it arrives rather than waiting out the timeout.
//
// The offer is forgotten as soon as the request has gone, before any of
// the waiting.  A lure id is spent by being used: it answers one offer
// once, whether or not the teleport it asked for succeeds, and keeping
// it while this waits would leave it there for somebody to accept a
// second time.
//
// It costs DefaultTeleportTimeout at worst.  The errors are Teleport's
// and mean the same things.
func (w *Session) AcceptLure(ctx context.Context, l *Lure) error {
	if l == nil {
		return fmt.Errorf("sl: no teleport offer to accept")
	}

	where, err := w.Where(ctx)
	if err != nil {
		return err
	}

	m := &msg.TeleportLureRequest{}
	m.Info.AgentID, m.Info.SessionID = w.agentBlock()
	m.Info.LureID = l.ID
	m.Info.TeleportFlags = teleportViaLure

	// Watching before asking, for teleport.go's reason: the answer is
	// about a third of a second behind the request.  A TeleportLocal
	// counts as one here, where it does not for Teleport: an offer from
	// somebody standing nearby is answered with that and nothing else.
	watch := w.watchTeleport(where.RegionHandle, true)
	defer watch.stop()

	if err := w.Send(ctx, m); err != nil {
		return err
	}
	w.ForgetLure(l)

	who := l.Name
	if who == "" {
		who = l.From.String()
	}
	return watch.arrive(ctx, "the teleport "+who+" offered", DefaultTeleportTimeout)
}

// DeclineLure says no, and tells the person who offered.
func (w *Session) DeclineLure(ctx context.Context, l *Lure) error {
	if l == nil {
		return fmt.Errorf("sl: no teleport offer to decline")
	}
	m := w.im(l.From, DialogLureDeclined, "")
	// The lure rather than a fresh conversation: this answers the
	// offer, and the other side matches it by that id.
	m.MessageBlock.ID = l.ID
	if err := w.Send(ctx, m); err != nil {
		return err
	}
	w.ForgetLure(l)
	return nil
}
