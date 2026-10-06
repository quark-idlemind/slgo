package slate

// The event log and what a step may see of it.
//
// One log serves the whole run. Everything the runner observes goes in it
// with the time it was observed, and a step reads from it by arm point:
// an event is eligible when it was observed at or after the step's arm
// point and no expectation has consumed it.
// Why: doc/slate-runner.md#event-log-and-arming

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

type eventKind int

const (
	evChat eventKind = iota
	evDialog
	evIM
	evPermission
)

// event is one thing observed. Exactly one of line, dialog and im is set,
// by kind (a denied permission request has only text). text is the transcript line without its time, and is empty for
// an event the transcript does not print.
type event struct {
	kind     eventKind
	at       time.Time
	consumed bool
	line     sl.Line
	dialog   sl.Dialog
	im       *sl.IM
	text     string
	to       string // the second avatar a dialog or an instant message came to; "" is the tester

	snd *sl.HeardSound // evSound: the sound heard (sound.go)

	wire wireMessage // evWire: the protocol line (bridge.go)
	prim *probePrim  // evWire: the probe that said it, when a probe did
}

// dialogKey identifies a dialog the way sl.Session.ForgetDialog does.
type dialogKey struct {
	at      time.Time
	object  msg.UUID
	channel int32
	to      string // the recipient's binding; "" is the tester
}

func keyOf(d sl.Dialog, to string) dialogKey { return dialogKey{d.At, d.Object, d.Channel, to} }

// eventLog is the run's one log. Only the runner's goroutine touches it.
type eventLog struct {
	events []*event
	dialog map[dialogKey]bool
}

func newEventLog() *eventLog { return &eventLog{dialog: map[dialogKey]bool{}} }

func (l *eventLog) add(ev *event) { l.events = append(l.events, ev) }

// mark is where a test's events begin in the log, for scans that need not
// look at an earlier test. Eligibility is still decided by the arm point.
func (l *eventLog) mark() int { return len(l.events) }

// armPoint says which events a step may see. A line or an IM is stamped
// with the time the session received it, and the runner drains before it
// takes an arm point, so an event waiting in a subscription's buffer when
// the arm point is taken is stamped before it.
type armPoint = time.Time

// eligible reports whether ev may be offered to a step armed at arm.
func (ev *event) eligible(arm armPoint) bool {
	return !ev.consumed && !ev.at.Before(arm)
}

// later is the latest of the times.
func later(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

// drain takes in everything waiting: the chat and IM subscriptions, the
// dialogs the session holds, and the same of each second avatar's session
// but its chat. It does not block.
func (r *runner) drain() error {
	for {
		select {
		case l, ok := <-r.chat:
			if !ok {
				return r.ended()
			}
			r.observeChat(l)
			continue
		case im, ok := <-r.ims:
			if !ok {
				return r.ended()
			}
			r.observeIM(im)
			continue
		default:
		}
		break
	}
	for _, sc := range r.seconds {
		if err := r.drainSecond(sc); err != nil {
			return err
		}
	}
	r.pollDialogs()
	r.drainSounds()
	return r.denyPermissions()
}

// wait blocks for one wake-up: a heard line, an instant message, the
// dialog poll, the time until, or the end of the run. Whatever woke it is
// taken in by the next drain, or here when it is a line.
func (r *runner) wait(ctx context.Context, until time.Time) error {
	d := max(time.Until(until), 0)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case l, ok := <-r.chat:
		if !ok {
			return r.ended()
		}
		r.observeChat(l)
	case im, ok := <-r.ims:
		if !ok {
			return r.ended()
		}
		r.observeIM(im)
	case <-r.tick.C:
	case <-t.C:
	case <-ctx.Done():
		return ctx.Err()
	case <-r.sess.Done():
		return r.ended()
	}
	return nil
}

// arrived is an event's stamp: when the session received it, or now when
// it does not say. Why: a runner busy for seconds, as in naming a worn
// object, would otherwise stamp what waited in the buffer that late
// (doc/slate-runner.md#ninth-round-wave-2-end-to-end).
func arrived(at time.Time) time.Time {
	if at.IsZero() || at.After(time.Now()) {
		return time.Now()
	}
	return at
}

func (r *runner) observeChat(l sl.Line) {
	if r.observeWire(l) {
		return
	}
	ev := &event{kind: evChat, at: arrived(l.At), line: l}
	ev.text = r.chatText(l)
	r.log.add(ev)
	r.printEvent(ev)
}

// drainSecond takes in the instant messages a second avatar has been sent.
func (r *runner) drainSecond(sc *second) error {
	for {
		select {
		case im, ok := <-sc.ims:
			if !ok {
				return fmt.Errorf("slate: the session of %s ended", sc.name)
			}
			r.observeSecondIM(sc, im)
		default:
			return nil
		}
	}
}

// observeSecondIM logs an instant message to a second avatar, with its
// recipient, and declines an inventory offer at once: the expectation
// still sees it in the log, and the avatar's inventory is left as found.
// Why: doc/slate-language.md#a-second-avatar
func (r *runner) observeSecondIM(sc *second, im *sl.IM) {
	ev := &event{kind: evIM, at: arrived(im.At), im: im, to: sc.name}
	offer := im.Dialog == sl.DialogTaskInventoryOffered
	if offer {
		ev.text = r.giveText(im, sc.name)
	}
	r.log.add(ev)
	r.printEvent(ev)
	if !offer {
		return
	}
	if err := r.decline(sc, im); err != nil {
		r.printf("slate: the offer to %s could not be declined: %v", sc.name, err)
		return
	}
	line := &event{kind: evAccept, at: time.Now(), consumed: true, to: sc.name,
		text: fmt.Sprintf("give to %s declined, transaction %s", sc.name, im.ID)}
	r.log.add(line)
	r.printEvent(line)
}

// decline refuses the offer an instant message carries, through the
// session's own record of it when it has one.
func (r *runner) decline(sc *second, im *sl.IM) error {
	for _, o := range sc.sess.InventoryOffers() {
		if o.Transaction == im.ID && o.From == im.From {
			return o.Decline(r.ctx)
		}
	}
	o, ok := sl.InventoryOfferFrom(im)
	if !ok {
		return fmt.Errorf("the message is not an offer")
	}
	return sc.sess.DeclineInventoryOffer(r.ctx, o)
}

func (r *runner) observeIM(im *sl.IM) {
	ev := &event{kind: evIM, at: arrived(im.At), im: im}
	if im.Dialog == sl.DialogTaskInventoryOffered {
		ev.text = r.giveText(im, "")
	}
	r.log.add(ev)
	r.printEvent(ev)
}

// pollDialogs logs each dialog the sessions hold that the log has not
// seen. A dialog is stamped with the time its session received it. One
// that was already waiting when the run began is logged for nobody and
// not printed: it belongs to no test.
func (r *runner) pollDialogs() {
	r.pollDialogsOf(r.sess, "")
	for _, sc := range r.seconds {
		r.pollDialogsOf(sc.sess, sc.name)
	}
}

func (r *runner) pollDialogsOf(sess *sl.Session, to string) {
	for _, d := range sess.Dialogs() {
		k := keyOf(d, to)
		if r.log.dialog[k] {
			continue
		}
		r.log.dialog[k] = true
		ev := &event{kind: evDialog, at: d.At, dialog: d, to: to}
		if d.At.Before(r.began) {
			ev.consumed = true
			r.log.add(ev)
			continue
		}
		ev.text = r.dialogText(d, to)
		r.log.add(ev)
		r.printEvent(ev)
	}
}

// denyPermissions answers every permission request the session holds and
// prints it: with a refusal, except for the bits an allow permission header
// names for the requesting object, which are granted and the rest refused
// in the same answer (permit.go). Debit is never among them.
// Each request is answered so the script's llRequestPermissions returns
// and its wait does not eat the expectation deadline. It is called with
// the dialog poll, and at the end of a test.
// Why: doc/slate-runner.md#cleanup-and-what-a-failure-leaves-behind
func (r *runner) denyPermissions() error {
	for _, q := range r.sess.Asked() {
		who := r.whoPrim(q.Object, func(n string) bool { return n != "" && n == q.ObjectName })
		if who == "" {
			who = sl.SenderObject.Label(q.ObjectName)
		}
		// The mask never holds debit: pay.Gate and allow pay own spending.
		granted := q.Wants & r.permitMask(q)
		text := fmt.Sprintf("permission denied from %s: %s", who, q.Wants)
		var err error
		if granted != 0 {
			err = q.Grant(r.ctx, granted)
			text = grantedText(who, granted, q.Wants)
		} else {
			err = q.Deny(r.ctx)
		}
		if err != nil {
			if r.ctx.Err() != nil {
				return r.ctx.Err()
			}
			r.printf("slate: permission request from %s could not be answered: %v", who, err)
			continue
		}
		ev := &event{kind: evPermission, at: time.Now(), consumed: true, text: text}
		r.log.add(ev)
		r.printEvent(ev)
	}
	for _, sc := range r.seconds {
		if err := r.denySecond(sc); err != nil {
			return err
		}
	}
	return nil
}

// denySecond refuses every permission request a second avatar holds: allow
// permission is the tester's, and the avatar is left as found.
// Why: doc/slate-language.md#a-second-avatar
func (r *runner) denySecond(sc *second) error {
	for _, q := range sc.sess.Asked() {
		who := r.whoPrim(q.Object, func(n string) bool { return n != "" && n == q.ObjectName })
		if who == "" {
			who = sl.SenderObject.Label(q.ObjectName)
		}
		if err := q.Deny(r.ctx); err != nil {
			if r.ctx.Err() != nil {
				return r.ctx.Err()
			}
			r.printf("slate: permission request to %s from %s could not be answered: %v", sc.name, who, err)
			continue
		}
		ev := &event{kind: evPermission, at: time.Now(), consumed: true, to: sc.name,
			text: fmt.Sprintf("permission denied to %s from %s: %s", sc.name, who, q.Wants)}
		r.log.add(ev)
		r.printEvent(ev)
	}
	return nil
}
