package main

// Things waiting for an answer.
//
// A viewer draws these as they arrive and leaves them on the screen
// until somebody deals with them: a teleport offered, a script asking
// permission, a blue menu, an item somebody handed over.  A shell has
// no screen to leave them on, so before this they arrived as one line
// of notice and were gone -- and worse, the notice named commands that
// did not exist ("grant with: accept-perms").
//
// So they are gathered here instead.  Each is numbered, the number
// holds still while the thing is waiting, and the prompt says how many
// there are.  Nothing is answered by being looked at.
//
//	waiting             what is waiting, numbered
//	answer N [WHAT]     yes, or a button, or the text a box wants
//	answer --file P N   a text box answer of more than one line
//	answer N L$50       yes to something that costs money
//	no N                decline, and tell whoever asked
//	ignore N            leave it waiting and stop counting it
//
// The kinds do not have a set of commands each, because from where a
// person sits they are the same question -- something wants an answer,
// and the answer is yes, no, one of these, or not now.
//
// The L$ line is the exception, and it exists because one kind can
// spend money: a group invitation may carry a joining fee, and a bare
// "answer 3" that quietly paid it would be the shell deciding to spend
// somebody's money for them.  So the fee is in the listing and has to
// be typed back before it will go.  See agreedFee.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

var waitingCommands = map[string]*command{
	"waiting": {
		flags: func() any { return new(waitingFlags) },
		brief: "what is waiting for an answer: teleports, dialogs, offers, permissions, invitations; -a includes ignored",
		man:   "waiting",
		run:   cmdWaiting,
	},
	"answer": {
		params: "N [BUTTON|TEXT|L$FEE]",
		flags:  func() any { return new(answerFlags) },
		brief:  "answer one of them: yes, a button by name or number, what a text box wants, or the fee a group asks",
		man:    "answer",
		run:    cmdAnswer,
	},
	"no": {
		params: "N",
		flags:  func() any { return new(helpOnly) },
		brief:  "decline one, and tell whoever asked",
		man:    "no",
		run:    cmdNo,
	},
	"ignore": {
		params: "N",
		flags:  func() any { return new(helpOnly) },
		brief:  "leave it waiting, but stop counting it at the prompt",
		man:    "ignore",
		run:    cmdIgnore,
	},
}

// A waiter is one thing that wants an answer.
//
// The kinds are kept apart by what they hold rather than by a tag,
// because each answers through its own part of the sl package and
// nothing here should be tempted to treat them alike beyond the
// listing.
type waiter struct {
	n    int
	at   time.Time
	kind string

	dialog *sl.Dialog
	lure   *sl.Lure
	item   *sl.InventoryOffer
	friend *sl.Offer
	perm   *sl.Permission
	invite *sl.Invitation
}

// key identifies one across a refresh, so that a number a person is
// looking at still means the same thing when they type it.
func (w waiter) key() string {
	switch {
	case w.dialog != nil:
		return fmt.Sprintf("dialog|%s|%d|%d", w.dialog.Object, w.dialog.Channel, w.dialog.At.UnixNano())
	case w.lure != nil:
		return "lure|" + w.lure.ID.String()
	case w.item != nil:
		return "item|" + w.item.Transaction.String()
	case w.friend != nil:
		return "friend|" + w.friend.Transaction.String()
	case w.perm != nil:
		return fmt.Sprintf("perm|%s|%s", w.perm.Object, w.perm.Item)
	case w.invite != nil:
		return "invite|" + w.invite.Transaction.String()
	}
	return ""
}

// what a person sees: who is asking, and what for.
func (w waiter) who() string {
	switch {
	case w.dialog != nil:
		return w.dialog.ObjectName
	case w.lure != nil:
		return w.lure.Name
	case w.item != nil:
		return w.item.FromName
	case w.friend != nil:
		return w.friend.Name
	case w.perm != nil:
		return w.perm.ObjectName
	case w.invite != nil:
		// Whoever invited, or the group itself: the invitation names a
		// person and never the group, so an unnamed one leaves the id
		// as the only handle there is.
		if w.invite.By != "" {
			return w.invite.By
		}
		return w.invite.Group.String()
	}
	return ""
}

func (w waiter) asks() string {
	switch {
	case w.dialog != nil:
		if w.dialog.IsTextBox() {
			return fmt.Sprintf("%q -- type an answer", w.dialog.Message)
		}
		return fmt.Sprintf("%q", w.dialog.Message)
	case w.lure != nil:
		if w.lure.Text != "" {
			return fmt.Sprintf("teleport: %q", w.lure.Text)
		}
		return "a teleport"
	case w.item != nil:
		return fmt.Sprintf("offers %q", w.item.Name)
	case w.friend != nil:
		return "offers friendship"
	case w.perm != nil:
		return "wants " + w.perm.Wants.String()
	case w.invite != nil:
		// The group's name is not a field of an invitation, so the
		// text the simulator wrote is where it appears if it appears
		// at all, and it is quoted rather than trusted to be one line.
		s := "invites you into a group"
		if w.invite.Text != "" {
			s = fmt.Sprintf("%s: %q", s, w.invite.Text)
		}
		return s + " " + feeNote(w.invite)
	}
	return ""
}

// feeNote is what an invitation costs, which is in the listing rather
// than only in the refusal because a person deciding whether to look
// closer should not have to type at it to find out.
func feeNote(i *sl.Invitation) string {
	switch {
	case !i.Stated:
		return "(it did not say what joining costs)"
	case i.Fee > 0:
		return fmt.Sprintf("(L$%d to join)", i.Fee)
	}
	return "(no fee)"
}

// choices is what may be typed at it, which is the part a person needs
// to see and the part that differs most between the kinds.
func (w waiter) choices() string {
	switch {
	case w.dialog != nil && w.dialog.IsTextBox():
		return "answer N (type lines, ^D ends), answer N TEXT for one line, no N discards"
	case w.dialog != nil:
		var b []string
		for i, label := range w.dialog.Buttons {
			b = append(b, fmt.Sprintf("%d %s", i+1, label))
		}
		return strings.Join(b, ", ")
	case w.lure != nil:
		return "answer (takes it, and waits for the arrival), no, ignore"
	case w.invite != nil && !w.invite.Stated:
		return "answer N L$AMOUNT (it did not say what joining costs), no, ignore"
	case w.invite != nil && w.invite.Fee > 0:
		return fmt.Sprintf("answer N L$%d (the fee has to be typed), no, ignore", w.invite.Fee)
	default:
		return "answer, no, ignore"
	}
}

// waiters gathers everything, in the order it arrived, and gives each
// one a number.
//
// The number belongs to the thing, not to its position.  Numbering by
// position looks identical until the moment it matters: answer the
// first of two and the second becomes 1, so the "answer 2" a person had
// already decided on is now either an error or, worse, something else.
// That is not hypothetical -- it is what the first live run of this did.
//
// So a number is handed out when a thing is first seen and kept until
// it goes.  The list can end up sparse, which is the honest picture:
// the thing that was 2 is still 2.
func (sh *Shell) waiters() []waiter {
	var all []waiter
	for _, d := range sh.s.Dialogs() {
		d := d
		kind := "dialog"
		if d.IsTextBox() {
			kind = "text box"
		}
		all = append(all, waiter{at: d.At, kind: kind, dialog: &d})
	}
	for _, l := range sh.s.Lures() {
		all = append(all, waiter{at: l.At, kind: "teleport", lure: l})
	}
	for _, o := range sh.s.InventoryOffers() {
		all = append(all, waiter{at: o.At, kind: "inventory", item: o})
	}
	for _, o := range sh.s.Offers() {
		all = append(all, waiter{at: o.At, kind: "friendship", friend: o})
	}
	for _, q := range sh.s.Asked() {
		all = append(all, waiter{at: q.At, kind: "permission", perm: q})
	}
	for _, i := range sh.s.Invitations() {
		all = append(all, waiter{at: i.At, kind: "group", invite: i})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })

	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.numbers == nil {
		sh.numbers = map[string]int{}
	}
	here := make(map[string]bool, len(all))
	for i := range all {
		here[all[i].key()] = true
	}
	// What has been answered or has expired gives its number back
	// first, so that a list which has emptied starts again at one
	// rather than climbing for the rest of the session.  Pruning after
	// handing out the numbers would be too late: whatever arrived in
	// the same breath would already have taken the higher number.
	for k := range sh.numbers {
		if !here[k] {
			delete(sh.numbers, k)
		}
	}
	if len(sh.numbers) == 0 {
		sh.nextNum = 0
	}
	for i := range all {
		k := all[i].key()
		n, seen := sh.numbers[k]
		if !seen {
			sh.nextNum++
			n = sh.nextNum
			sh.numbers[k] = n
		}
		all[i].n = n
	}
	return all
}

// ignored remembers what a person has set aside.  It is the shell's
// rather than the session's: another client attached to the same avatar
// has its own idea of what it has dealt with.
func (sh *Shell) setIgnored(key string, yes bool) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.ignoring == nil {
		sh.ignoring = map[string]bool{}
	}
	if yes {
		sh.ignoring[key] = true
		return
	}
	delete(sh.ignoring, key)
}

func (sh *Shell) isIgnored(key string) bool {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.ignoring[key]
}

// waitingCount is what the prompt shows: the things not set aside.
func (sh *Shell) waitingCount() int {
	n := 0
	for _, w := range sh.waiters() {
		if !sh.isIgnored(w.key()) {
			n++
		}
	}
	return n
}

// pick finds the waiter a person named by number.
func (sh *Shell) pick(arg string) (waiter, error) {
	n, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil {
		return waiter{}, fmt.Errorf("%q is not a number; waiting lists them", arg)
	}
	all := sh.waiters()
	for _, w := range all {
		if w.n == n {
			return w, nil
		}
	}
	if len(all) == 0 {
		return waiter{}, fmt.Errorf("nothing is waiting")
	}
	return waiter{}, fmt.Errorf("there is no %d; %d %s waiting", n, len(all), plural(len(all), "thing", "things"))
}

type waitingFlags struct {
	All  bool `getopt:"--all -a   include the ones being ignored"`
	Help bool `getopt:"--help -h  show what this command takes"`
}

func cmdWaiting(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o waitingFlags
	_, done, err := subOptions("waiting", &o, out, args)
	if err != nil || done {
		return err
	}

	all := sh.waiters()
	shown := 0
	for _, w := range all {
		ignored := sh.isIgnored(w.key())
		if ignored && !o.All {
			continue
		}
		mark := ""
		if ignored {
			mark = " (ignored)"
		}
		fmt.Fprintf(out, "%d  %-11s %s %s%s\n", w.n, w.kind, w.who(), w.asks(), mark)
		fmt.Fprintf(out, "                %s\n", w.choices())
		shown++
	}
	if shown == 0 {
		if len(all) > 0 {
			fmt.Fprintf(out, "nothing waiting; %d ignored, -a shows them\n", len(all))
			return nil
		}
		fmt.Fprintln(out, "nothing waiting")
	}
	return nil
}

type answerFlags struct {
	// File is how a text box gets more than one line.  A shell reads a
	// line at a time and a text box in the viewer is a text editor, so
	// the shape of the answer and the shape of the prompt do not match;
	// naming a file is the way through that does not involve inventing
	// an escape for the newline and then having to escape the escape.
	File string `getopt:"--file=PATH   take the answer from a file, newlines and all"`
	Help bool   `getopt:"--help -h    show what this command takes"`
}

func cmdAnswer(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o answerFlags
	args, done, err := subOptions("answer", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return usageError("answer")
	}
	w, err := sh.pick(args[0])
	if err != nil {
		return err
	}
	rest := strings.TrimSpace(strings.Join(args[1:], " "))
	if o.File != "" {
		if w.dialog == nil || !w.dialog.IsTextBox() {
			return fmt.Errorf("--file answers a text box; %d is a %s", w.n, w.kind)
		}
		b, err := os.ReadFile(o.File)
		if err != nil {
			return err
		}
		// Kept as it is, including a trailing newline if the file has
		// one: what a person put in the file is the answer.
		rest = string(b)
	}

	switch {
	case w.dialog != nil && w.dialog.IsTextBox():
		if rest == "" {
			// Nothing typed on the line means the long way: lines
			// until a full stop, which is the only way to send more
			// than one from a prompt that reads one at a time.
			fmt.Fprintf(out, "%s asks: %s\n", w.who(), w.dialog.Message)
			fmt.Fprintf(out, "type the answer; ^D ends it, ESC starts again, "+
				"^C sends nothing\n")
			sh.collect(*w.dialog, w.who(), w.n)
			return nil
		}
		if err := sh.s.AnswerText(ctx, *w.dialog, rest); err != nil {
			return err
		}
		fmt.Fprintf(out, "told %s %q\n", w.who(), rest)

	case w.dialog != nil:
		label, err := buttonOf(*w.dialog, rest)
		if err != nil {
			return err
		}
		if err := sh.s.Answer(ctx, *w.dialog, label); err != nil {
			return err
		}
		fmt.Fprintf(out, "pressed %q on %s\n", label, w.who())

	case w.lure != nil:
		// Said before the request goes, because accepting now waits for
		// the arrival rather than returning once the message has gone,
		// and a person watching a shell that has stopped answering
		// deserves to know what it is waiting for.
		fmt.Fprintf(out, "accepting a teleport from %s\n", w.who())

		// The shell's own deadline rather than the ninety seconds
		// AcceptLure defaults to, which were set before a teleport had
		// been timed -- see shellTeleportTimeout.  A context is the whole
		// of it: the wait for the arrival selects on one, so there is
		// nothing missing from sl to add here.
		moving, cancel := context.WithTimeout(ctx, shellTeleportTimeout)
		defer cancel()
		if err := sh.s.AcceptLure(moving, w.lure); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				// A deadline of this shell's is not a fault to report as
				// one: the grid answers a teleport asked for while
				// another is under way with nothing at all, ever, and
				// that is what running out of time here usually means.
				return fmt.Errorf("the teleport %s offered was not answered in %s; "+
					"an offer accepted while another teleport is under way is answered "+
					"with silence, and where says whether the avatar moved",
					w.who(), shellTeleportTimeout)
			}
			return err
		}
		// Where the avatar ended up, read back, because a lure is the
		// one teleport whose destination nobody knew in advance: the
		// offer says who made it and whatever they typed with it, and
		// the region is not in the message at all.
		if p, err := sh.s.Where(ctx); err == nil {
			fmt.Fprintf(out, "arrived in %s\n", positionLine(p))
		}

	case w.item != nil:
		into, err := sh.s.ObjectsFolder(ctx)
		if err != nil {
			return err
		}
		// The offer's own Accept, not the session call underneath it:
		// that one sends the answer and leaves the offer on the books.
		// Answering through it left the item in this listing, under the
		// same number and still counted, so "answer N" a second time
		// sent the grid a second acceptance of an offer already taken.
		if err := w.item.Accept(ctx, into); err != nil {
			return err
		}
		fmt.Fprintf(out, "took %q from %s\n", w.item.Name, w.who())

	case w.friend != nil:
		if err := w.friend.Accept(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s is now a friend\n", w.who())

	case w.perm != nil:
		if err := w.perm.GrantAll(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "granted %s to %s\n", w.perm.Wants, w.who())

	case w.invite != nil:
		if err := agreedFee(*w.invite, w.n, rest); err != nil {
			return err
		}
		// Said before the message goes, because afterwards the money
		// has already moved.
		if !w.invite.Stated {
			fmt.Fprintf(out, "the invitation did not say what joining costs, "+
				"so the group will charge its fee whatever you typed\n")
		}
		if err := sh.s.AcceptInvitation(ctx, w.invite); err != nil {
			return err
		}
		cost := ""
		if w.invite.Fee > 0 {
			cost = fmt.Sprintf(", at L$%d", w.invite.Fee)
		}
		fmt.Fprintf(out, "accepted the group invitation from %s%s; "+
			"nothing answers a join, so groups says whether it worked\n", w.who(), cost)
	}

	sh.setIgnored(w.key(), false)
	return nil
}

// agreedFee decides whether a person has said enough for money to
// move, and is the whole of this shell's answer to a group that charges
// to join.
//
// Nothing else here spends L$ except put, which prints what an upload
// will cost and takes -N to stop before paying it.  An invitation is
// the harder case: the amount is the group's rather than ours, and the
// answer carries no figure at all -- the simulator charges the fee
// whatever we send -- so there is nothing to check the charge against
// except what the invitation said it would be.
//
// Hence the rule.  A fee of zero is the ordinary case and "answer N"
// takes it.  Anything else, including an invitation whose fee could not
// be read, has to have the amount typed back before it will go.  That
// is not a payment instruction and does not cap anything: it is the
// person saying the number out loud, which is the only part of this a
// shell can honestly ask of them.
func agreedFee(i sl.Invitation, n int, said string) error {
	pay := fmt.Sprintf("answer %d L$%d", n, i.Fee)
	if said == "" {
		switch {
		case !i.Stated:
			return fmt.Errorf("the invitation did not say what joining costs; "+
				"type \"answer %d L$0\" if you mean it is free, or the amount you will pay; "+
				"\"no %d\" declines it", n, n)
		case i.Fee > 0:
			return fmt.Errorf("joining costs L$%d; type %q to pay it, or %q to decline",
				i.Fee, pay, fmt.Sprintf("no %d", n))
		}
		return nil
	}
	amount, ok := feeSaid(said)
	if !ok {
		return fmt.Errorf("%q is not an amount; a group invitation takes the fee, as in %q", said, pay)
	}
	// Nothing said, nothing to check it against: the person's figure is
	// all there is, and they have been asked for it, which is as far as
	// this can go.
	if !i.Stated {
		return nil
	}
	if amount != i.Fee {
		if i.Fee == 0 {
			return fmt.Errorf("joining costs nothing; type \"answer %d\" on its own to take it", n)
		}
		return fmt.Errorf("joining costs L$%d, not L$%d; type %q to pay it", i.Fee, amount, pay)
	}
	return nil
}

// feeSaid reads an amount the way a person would write one: L$50, or 50
// from somebody who has already typed the L$ once today.
//
// It is deliberately narrow -- no separators, no decimal point, nothing
// negative -- because its only use is confirming a figure that is
// already on the screen, and anything it fails to understand is
// refused rather than rounded into a payment.
func feeSaid(s string) (int32, bool) {
	s = strings.TrimSpace(s)
	if len(s) > 2 && (s[0] == 'L' || s[0] == 'l') && s[1] == '$' {
		s = s[2:]
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 1<<31-1 {
		return 0, false
	}
	return int32(n), true
}

// buttonOf reads what a person typed at a dialog: a number as the
// position shown, anything else as the label itself.
func buttonOf(d sl.Dialog, text string) (string, error) {
	if text == "" {
		if len(d.Buttons) == 1 {
			return d.Buttons[0], nil
		}
		return "", fmt.Errorf("which button: %s", strings.Join(d.Buttons, ", "))
	}
	if i, err := strconv.Atoi(text); err == nil {
		if i < 1 || i > len(d.Buttons) {
			return "", fmt.Errorf("there is no button %d; it offers %d", i, len(d.Buttons))
		}
		return d.Buttons[i-1], nil
	}
	if _, ok := d.Button(text); !ok {
		return "", fmt.Errorf("%q is not one of the buttons: %s", text, strings.Join(d.Buttons, ", "))
	}
	return text, nil
}

func cmdNo(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("no", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return usageError("no")
	}
	w, err := sh.pick(args[0])
	if err != nil {
		return err
	}

	switch {
	case w.dialog != nil:
		// There is no message for the close box, so the honest thing
		// is to drop it here and let it expire where it was raised.
		sh.s.ForgetDialog(*w.dialog)
		fmt.Fprintf(out, "left %s unanswered; a dialog cannot be declined, only left to expire\n", w.who())
	case w.lure != nil:
		if err := sh.s.DeclineLure(ctx, w.lure); err != nil {
			return err
		}
		fmt.Fprintf(out, "declined the teleport from %s\n", w.who())
	case w.item != nil:
		// The offer's own Decline: the session call underneath it sends
		// the refusal without forgetting the offer, which left it here
		// to be refused again.  See cmdAnswer.
		if err := w.item.Decline(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "declined %q from %s\n", w.item.Name, w.who())
	case w.friend != nil:
		if err := w.friend.Decline(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "declined friendship from %s\n", w.who())
	case w.perm != nil:
		if err := w.perm.Deny(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "refused %s to %s\n", w.perm.Wants, w.who())
	case w.invite != nil:
		if err := sh.s.DeclineInvitation(ctx, w.invite); err != nil {
			return err
		}
		fmt.Fprintf(out, "declined the group invitation from %s\n", w.who())
	}

	sh.setIgnored(w.key(), false)
	return nil
}

func cmdIgnore(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("ignore", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return usageError("ignore")
	}
	w, err := sh.pick(args[0])
	if err != nil {
		return err
	}
	sh.setIgnored(w.key(), true)
	fmt.Fprintf(out, "%d is still waiting, and no longer counted; waiting -a lists it\n", w.n)
	return nil
}
