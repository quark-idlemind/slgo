package sl

// Offers kept by the daemon.
//
// The offers a person answers -- a teleport, a request for one, an item
// handed over, friendship, a group -- are kept by this package as they
// arrive (im.go, lure.go, invite.go), because each is answered by quoting
// an id that arrives once.  That kept them for exactly as long as one
// client was listening.  An offer made while nobody was attached was
// never seen by anybody, and a shell started afterwards said "nothing
// waiting" about an avatar with two group invitations sitting on it.
//
// slgod keeps them now, whether or not anybody is attached, and a
// hosted session is handed what is still waiting when it attaches: the
// offers above, an item an object gives as well as one an avatar hands
// over, and a script's dialog or text box (dialog.go).  A script's
// permission request is the one thing a person answers that it does not
// keep.  They go through the same reading as a live one, so nothing
// about what an offer is has to be understood twice; they are marked
// Recorded, and are neither delivered to IMs subscribers nor announced
// to OnDialog, because they are history rather than news.
//
// Dealing with one goes through the daemon too.  Before this session
// answers an offer the daemon keeps, it says so, and the daemon either
// agrees -- and tells every other client, which drops the offer from its
// lists -- or says who got there first, and then nothing is sent.  That
// is what stops two clients on one avatar both answering one offer
// without knowing: whichever asks second is told, and the listing in the
// other has already lost the offer by the time a person looks at it.
//
// A direct session has none of this and needs none of it.  It holds the
// grid session itself, so everything that arrived since it logged in
// went through its own reader, and there is nobody else attached to
// tell.  Its backend is not an OfferKeeper, and every call below is then
// the answer being sent with nobody asked first.

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// An OfferKeeper is a backend with something other than this process
// keeping the offers made to the avatar: slgod.
//
// Only a hosted session is one; see the head of this file for why a
// direct one is not.  A backend that is not, or a daemon too old to keep
// a record, leaves a session knowing only what it saw arrive.
type OfferKeeper interface {
	// KeptOffers is what the keeper said was waiting when this session
	// attached, as the messages the offers arrived in, and false when
	// it said nothing at all.
	KeptOffers() (*OfferRecord, []*Message, bool)

	// Handled says this session is about to deal with the offer the
	// keeper calls key.  claimed is true when the keeper agrees;
	// earlier, when it is not nil, is who dealt with it first, and
	// then nothing should be sent.  False with no earlier means the
	// keeper was not holding it, which says nothing either way.
	Handled(ctx context.Context, key, how string) (claimed bool, earlier *Handled, err error)

	// Unhandled puts back an offer this session said it would deal
	// with and then could not, because its answer never went out.
	Unhandled(ctx context.Context, key string) error

	// HandledOffers is the keeper saying an offer has been dealt with,
	// by this session or another.  Closed when the session ends.
	HandledOffers() <-chan *Handled
}

// OfferRecord is what slgod said about the offers it keeps for this
// avatar, as it stood when the session attached.
type OfferRecord struct {
	// Since is when it began keeping them.  Anything offered before
	// then -- while the daemon was down, or before it logged the
	// avatar in -- is not in the record and cannot be.
	Since time.Time

	// Kept is how many it handed over.
	Kept int

	// Evicted is how many unanswered offers it had dropped to make
	// room, and Limit how many it keeps.  A record that has dropped
	// some is not the whole story, and a listing built from it should
	// not read as though it were.
	Evicted int
	Limit   int
}

// Handled is an offer somebody dealt with.
type Handled struct {
	// Key is the daemon's name for it, and empty for a permission
	// request, which the daemon does not keep.
	Key string

	// What is the offer, for a person: "the teleport Example Resident
	// offered".  Empty when this session was not holding it.
	What string

	// How is what was done, in the words of whoever did it and shaped
	// to follow "was": "accepted", "declined", "refused".
	How string

	// By is the program that did it, as it named itself to the daemon.
	// Empty when the daemon saw the answer go out without being told
	// first, and so cannot say which program sent it.  "this session"
	// for a dialog or a permission request this session dropped
	// unanswered; see UnansweredFor.
	By string

	At time.Time
}

// Who is By for a sentence.
func (h Handled) Who() string {
	if h.By == "" {
		return "another program attached to this avatar"
	}
	return h.By
}

// AnsweredError is an offer somebody else dealt with first.  Nothing
// was sent: answering it again would answer it twice.
type AnsweredError struct {
	Handled
}

func (e *AnsweredError) Error() string {
	what := e.What
	if what == "" {
		what = "that offer"
	}
	return fmt.Sprintf("%s was already %s by %s at %s, so nothing was sent",
		what, e.How, e.Who(), e.At.Local().Format("15:04:05"))
}

// String is the sentence a program tells a person: what it was, what was
// done and by whom.
func (h Handled) String() string {
	what := h.What
	if what == "" {
		what = "an offer"
	}
	return fmt.Sprintf("%s was %s by %s", what, h.How, h.Who())
}

// OfferRecord is what slgod said about the offers it keeps, and false
// when there is no such record: a direct session, or a daemon too old
// to keep one.  Either way the session then knows only what it saw
// arrive, and a listing should say so rather than read as complete.
func (w *Session) OfferRecord() (OfferRecord, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.record == nil {
		return OfferRecord{}, false
	}
	return *w.record, true
}

// loadKept reads what the keeper was holding when this session attached.
//
// Called from New, before the reader starts, so that a session is
// holding every offer the daemon kept by the time anybody can ask it
// what is waiting -- rather than at some moment afterwards, which is
// what reading them off the relay would have made it.
func (w *Session) loadKept() {
	k, ok := w.b.(OfferKeeper)
	if !ok {
		return
	}
	rec, msgs, ok := k.KeptOffers()
	if !ok {
		return
	}
	w.mu.Lock()
	w.record = rec
	w.mu.Unlock()
	for _, m := range msgs {
		v, err := m.Decode()
		if err != nil {
			continue
		}
		switch t := v.(type) {
		case *msg.ImprovedInstantMessage:
			w.instantMessage(m, t)
		case *msg.ScriptDialog:
			w.dialog(m, t)
		}
	}
}

// answering tells the keeper this session is about to answer an offer,
// and says whether to.
//
// A non-nil error is an *AnsweredError: somebody else dealt with it
// first, and it has been dropped from this session's lists as well.
// Otherwise the answer should go, and undo is what to call if it then
// cannot -- it puts the offer back for everybody, since a claim nobody
// followed with an answer would hide an offer that is still waiting.
//
// An offer the daemon never named, a backend that keeps nothing, and a
// keeper that could not be asked all mean go ahead.  The last is a
// judgement: a daemon that cannot answer this is one the answer is not
// going to reach either, and refusing on its behalf would turn a fault
// in the bookkeeping into an offer nobody can accept.
func (w *Session) answering(ctx context.Context, key, how string) (undo func(), err error) {
	undo = func() {}
	k, ok := w.b.(OfferKeeper)
	if !ok || key == "" {
		return undo, nil
	}

	// Marked before asking, because the keeper's notice that this
	// session has dealt with it can arrive before its answer to the
	// question does; see offerHandled.
	w.mu.Lock()
	if w.claims == nil || len(w.claims) > 256 {
		w.claims = map[string]bool{}
	}
	w.claims[key] = true
	w.mu.Unlock()

	claimed, earlier, err := k.Handled(ctx, key, how)
	if claimed {
		return func() {
			// Its own deadline: the one the answer failed under may
			// be the reason it failed.
			put, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			k.Unhandled(put, key)
		}, nil
	}

	// Not this session's, so no notice is coming to clear the mark.
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.claims, key)
	if err != nil || earlier == nil {
		return undo, nil
	}
	e := &AnsweredError{Handled: *earlier}
	e.Key = key
	e.What = w.forgetKeyLocked(key)
	return undo, e
}

// offerHandled is the keeper saying an offer is no longer waiting.
//
// One this session is dealing with itself is left alone: it is answered
// and forgotten by whoever is answering it, and if the answer fails and
// is put back, forgetting it here would lose it.  Anything else is
// dropped, and whoever wanted to know is told.
//
// The key is also remembered, because the notice and the offer come on
// different relays and are read in whichever order they are ready in: an
// offer read after the notice that it has been dealt with must not be
// kept as waiting.  See instantMessage.
func (w *Session) offerHandled(h *Handled) {
	w.mu.Lock()
	if w.claims[h.Key] {
		delete(w.claims, h.Key)
		w.mu.Unlock()
		return
	}
	if w.gone == nil || len(w.gone) > 256 {
		w.gone = map[string]bool{}
	}
	w.gone[h.Key] = true
	h.What = w.forgetKeyLocked(h.Key)
	fn := w.OnHandled
	w.mu.Unlock()
	if fn != nil && h.What != "" {
		fn(*h)
	}
}

// forgetKeyLocked drops whatever offer the keeper calls key, and says
// what it was for a person; empty when this session was not holding it.
// Called with mu held.
func (w *Session) forgetKeyLocked(key string) string {
	if key == "" {
		return ""
	}
	for from, o := range w.offers {
		if o.key == key {
			delete(w.offers, from)
			return "the offer of friendship from " + o.Name
		}
	}
	for t, o := range w.invOffers {
		if o.key == key {
			delete(w.invOffers, t)
			return fmt.Sprintf("%q from %s", o.Name, o.FromName)
		}
	}
	for from, l := range w.lures {
		if l.key == key {
			delete(w.lures, from)
			return "the teleport " + orID(l.Name, l.From) + " offered"
		}
	}
	for from, r := range w.tpRequests {
		if r.key == key {
			delete(w.tpRequests, from)
			return orID(r.Name, r.From) + "'s request for a teleport"
		}
	}
	for g, i := range w.invites {
		if i.key == key {
			delete(w.invites, g)
			by := i.By
			if by == "" {
				by = "somebody"
			}
			return "the invitation from " + by + " into group " + i.Group.String()
		}
	}
	for i, d := range w.dialogs {
		if d.key == key {
			w.dialogs = append(w.dialogs[:i], w.dialogs[i+1:]...)
			what := "the dialog from "
			if d.IsTextBox() {
				what = "the text box from "
			}
			return what + SenderObject.Label(orID(d.ObjectName, d.Object))
		}
	}
	return ""
}

func orID(name string, id msg.UUID) string {
	if name != "" {
		return name
	}
	return id.String()
}

// keepableLocked says whether an offer that has just been read should be
// kept as waiting, and settles the bookkeeping either way.  Called with
// mu held, from the reader.
//
// No, for a live one the keeper has already said was dealt with: the
// notice overtook it.  Yes, and the notice forgotten, for one out of
// the record, because the only record that arrives after a notice is an
// offer put back.
func (w *Session) keepableLocked(key string, recorded bool) bool {
	if key == "" {
		return true
	}
	if recorded {
		delete(w.gone, key)
		return true
	}
	return !w.gone[key]
}
