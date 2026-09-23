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
	"math"
	"net/url"
	"strings"
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

	// Recorded says it arrived before this session attached, and is
	// known only because slgod kept it.  See offers.go.
	Recorded bool

	// key is slgod's name for it; see Offer.
	key string
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
func (w *Session) noteLure(im *IM, key string, recorded bool) {
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
		Recorded: recorded, key: key,
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

	// Asked before anything else, so that an offer another client has
	// already taken is refused before a teleport is waited for.  See
	// offers.go.
	undo, err := w.answering(ctx, l.key, "accepted")
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
		undo()
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
	undo, err := w.answering(ctx, l.key, "declined")
	if err != nil {
		return err
	}
	m := w.im(l.From, DialogLureDeclined, "")
	// The lure rather than a fresh conversation: this answers the
	// offer, and the other side matches it by that id.
	m.MessageBlock.ID = l.ID
	if err := w.Send(ctx, m); err != nil {
		undo()
		return err
	}
	w.ForgetLure(l)
	return nil
}

// Teleport requests: the other direction.
//
// Somebody sends an instant message of dialog 26 asking to be brought
// to this avatar (llinstantmessage.h:129, IM_TELEPORT_REQUEST).  It
// carries no id -- the id field is null when the viewer sends one
// (llavataractions.cpp:679) -- so who asked is the whole of it, and
// what answers it is an OFFER going the other way.
//
// That asymmetry is the grid's, not this package's: saying yes to a
// request is teleport_request_callback calling send_lures, the same
// function the menu item calls (llviewermessage.cpp), and saying no
// sends nothing at all.  The person who asked is never told they were
// refused; their dialog simply never resolves.

// TeleportRequest is somebody asking this avatar to offer them a
// teleport.
type TeleportRequest struct {
	At time.Time

	// From is who asked and Name what they are called.
	From msg.UUID
	Name string

	// Text is what they said with it.
	Text string

	// Recorded says it arrived before this session attached, and is
	// known only because slgod kept it.  See offers.go.
	Recorded bool

	// key is slgod's name for it; see Offer.
	key string
}

func (r TeleportRequest) String() string {
	who := r.Name
	if who == "" {
		who = r.From.String()
	}
	if r.Text == "" {
		return who + " asks to be teleported here"
	}
	return fmt.Sprintf("%s asks to be teleported here: %q", who, r.Text)
}

// noteTeleportRequest keeps one, replacing any earlier one from the
// same person: asking twice is asking, not asking for two teleports.
func (w *Session) noteTeleportRequest(im *IM, key string, recorded bool) {
	name := im.FromName
	if name == "" {
		name = w.NameOr(im.From)
	}
	w.mu.Lock()
	if w.tpRequests == nil {
		w.tpRequests = map[msg.UUID]*TeleportRequest{}
	}
	w.tpRequests[im.From] = &TeleportRequest{
		At: im.At, From: im.From, Name: name, Text: im.Text,
		Recorded: recorded, key: key,
	}
	w.mu.Unlock()
}

// TeleportRequests returns the requests waiting for an answer, oldest
// first.
func (w *Session) TeleportRequests() []*TeleportRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*TeleportRequest, 0, len(w.tpRequests))
	for _, r := range w.tpRequests {
		out = append(out, r)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].At.Before(out[j-1].At); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// AnswerTeleportRequest says yes to one, which is offering them a
// teleport here: the request carries no id to accept, and the viewer's
// Yes button does exactly this (see OfferTeleport).  The note goes with
// the offer.
//
// Another client of the same avatar may have answered it already, and
// then nothing is sent and the error is an *AnsweredError; see
// offers.go.
func (w *Session) AnswerTeleportRequest(ctx context.Context, r *TeleportRequest, note string) error {
	if r == nil {
		return fmt.Errorf("sl: no teleport request to answer")
	}
	undo, err := w.answering(ctx, r.key, "answered with a teleport offer")
	if err != nil {
		return err
	}
	if err := w.OfferTeleport(ctx, r.From, note); err != nil {
		undo()
		return err
	}
	w.ForgetTeleportRequest(r)
	return nil
}

// RefuseTeleportRequest says no to one.
//
// Nothing goes to the grid, because there is nothing to send: the
// viewer's No button sends nothing at all, and the person who asked is
// never told.  What it does is settle the question for every client of
// this avatar -- slgod stops keeping it and the others stop listing it --
// which is the difference between refusing one and merely forgetting it.
func (w *Session) RefuseTeleportRequest(ctx context.Context, r *TeleportRequest) error {
	if r == nil {
		return fmt.Errorf("sl: no teleport request to refuse")
	}
	if _, err := w.answering(ctx, r.key, "refused"); err != nil {
		return err
	}
	w.ForgetTeleportRequest(r)
	return nil
}

// ForgetTeleportRequest drops one without answering it.
//
// Which is also what refusing one looks like on the wire, since the
// grid has nothing to send for a refusal.  The difference is only
// whether this session goes on listing it -- and, since slgod began
// keeping them, whether the other clients do: this tells nobody, and
// RefuseTeleportRequest is the one that settles it for all of them.
func (w *Session) ForgetTeleportRequest(r *TeleportRequest) {
	if r == nil {
		return
	}
	w.mu.Lock()
	delete(w.tpRequests, r.From)
	w.mu.Unlock()
}

// OfferTeleport offers somebody a teleport to where this avatar is.
//
// StartLure, with LureType zero -- the viewer sends zero with the
// comment "sim will fill this in" (llviewermessage.cpp, send_lures),
// so it is sent for the sake of matching rather than because it
// decides anything.
//
// The destination is not a field of the message.  What the other side
// sees is whatever is written into the note, which is why the viewer
// appends its own location to it as a SLURL and why this does too: an
// offer without one arrives with nothing to say where it goes.  A
// position that cannot be read is not a reason to refuse to offer, so
// the note goes on its own in that case.
func (w *Session) OfferTeleport(ctx context.Context, to msg.UUID, note string) error {
	if to.IsZero() {
		return fmt.Errorf("offer a teleport to nobody")
	}
	text := strings.TrimSpace(note)
	if where := w.slurl(ctx); where != "" {
		if text == "" {
			text = where
		} else {
			text += "\r\n" + where
		}
	}

	m := &msg.StartLure{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.Info.LureType = 0
	// Terminated, like every other string this tree puts on the wire.
	// Without the NUL the simulator reads the last byte as the
	// terminator and the note arrives one character short -- measured,
	// with a position of 4004 metres arriving as 400.
	m.Info.Message = append([]byte(text), 0)
	m.TargetData = []msg.StartLure_TargetData{{TargetID: to}}
	return w.Send(ctx, m)
}

// slurl is where this avatar is, in the form a viewer writes into a
// teleport offer: the grid's map URL, the region escaped, and the
// position rounded to whole metres (llslurl.cpp, getSLURLString).
//
// Empty when the position cannot be read.  It is decoration on an
// offer that works without it.
func (w *Session) slurl(ctx context.Context) string {
	p, err := w.Where(ctx)
	if err != nil || p.Region == "" {
		return ""
	}
	return fmt.Sprintf("https://maps.secondlife.com/secondlife/%s/%d/%d/%d",
		url.PathEscape(p.Region),
		int(math.Round(float64(p.Position.X))),
		int(math.Round(float64(p.Position.Y))),
		int(math.Round(float64(p.Position.Z))))
}

// RequestTeleport asks somebody to offer this avatar a teleport to
// them, which is the other direction and is an instant message rather
// than a message of its own (llavataractions.cpp:679, dialog 26 with a
// null id).
//
// What answers it is an offer coming back.  There is nothing to wait
// for here and no refusal to expect: a viewer's No button on one of
// these sends nothing at all.
func (w *Session) RequestTeleport(ctx context.Context, to msg.UUID, note string) error {
	if to.IsZero() {
		return fmt.Errorf("ask nobody for a teleport")
	}
	text := strings.TrimSpace(note)
	if text == "" {
		text = "Would you teleport me to you?"
	}
	return w.Send(ctx, w.im(to, DialogTeleportRequest, text))
}
