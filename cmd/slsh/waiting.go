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
//	no N                decline, and tell whoever asked
//	ignore N            leave it waiting and stop counting it
//
// The four kinds do not have four sets of commands, because from where
// a person sits they are the same question -- something wants an
// answer, and the answer is yes, no, one of these, or not now.

import (
	"context"
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
		usage: "waiting [-a]",
		brief: "what is waiting for an answer: teleports, dialogs, offers, permissions; -a includes ignored",
		run:   cmdWaiting,
	},
	"answer": {
		usage: "answer [--file PATH] N [BUTTON|TEXT]",
		brief: "answer one of them: yes, a button by name or number, or what a text box wants",
		run:   cmdAnswer,
	},
	"no": {
		usage: "no N",
		brief: "decline one, and tell whoever asked",
		run:   cmdNo,
	},
	"ignore": {
		usage: "ignore N",
		brief: "leave it waiting, but stop counting it at the prompt",
		run:   cmdIgnore,
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
	}
	return ""
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
		return "answer (ends this session unless it is the same region), no, ignore"
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
	_, done, err := subOptions("waiting", "", &o, out, args)
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
	args, done, err := subOptions("answer", "N [BUTTON|TEXT]", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: answer N [BUTTON|TEXT]")
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
		// The warning is printed before the message goes, because
		// after it there may be no session to print anything with.
		fmt.Fprintf(out, "accepting a teleport from %s\n", w.who())
		if err := sh.s.AcceptLure(ctx, w.lure); err != nil {
			return err
		}
		fmt.Fprintf(out, "if that is another region this session ends: a crossing needs a circuit slgo does not open yet\n")

	case w.item != nil:
		into, err := sh.s.ObjectsFolder(ctx)
		if err != nil {
			return err
		}
		if err := sh.s.AcceptInventoryOffer(ctx, w.item, into); err != nil {
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
	}

	sh.setIgnored(w.key(), false)
	return nil
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
	args, done, err := subOptions("no", "N", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: no N")
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
		if err := sh.s.DeclineInventoryOffer(ctx, w.item); err != nil {
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
	}

	sh.setIgnored(w.key(), false)
	return nil
}

func cmdIgnore(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("ignore", "N", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: ignore N")
	}
	w, err := sh.pick(args[0])
	if err != nil {
		return err
	}
	sh.setIgnored(w.key(), true)
	fmt.Fprintf(out, "%d is still waiting, and no longer counted; waiting -a lists it\n", w.n)
	return nil
}
