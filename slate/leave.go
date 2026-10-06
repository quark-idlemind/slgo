package slate

// What a test leaves waiting on the tester: the inventory offers objects
// made and the script dialogs that arrived and that no step answered.
// They are declined and ignored at the end of the test, as the viewer's
// Decline and Ignore buttons would, so slgod's waiting list is as the run
// found it.  An offer from a person is that person's to the tester and
// not the run's to answer: it is left waiting, and said.
// Why: doc/slate-language.md#the-tester-is-left-as-found

import (
	"context"
	"fmt"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// leaveAsFound declines each inventory offer an object made to the tester
// that arrived in this run and is still waiting (one an `expect give`
// accepted is not), leaves one from a person waiting and says so,
// and ignores each dialog that arrived in this run and is still waiting,
// printing what it did with each. What was waiting before the run began
// belongs to nobody here and is left alone. A failure to do either is
// printed and does not fail the test.
func (t *testRun) leaveAsFound(ctx context.Context) {
	r := t.r
	for _, o := range r.sess.InventoryOffers() {
		if o.Recorded || o.At.Before(r.began) {
			continue
		}
		if o.Dialog != sl.DialogTaskInventoryOffered {
			// A person's give: the tester is somebody's avatar, and a test
			// has no business answering another person for them.
			r.printf("slate: left offer %q from %s waiting; a person's offer is not the run's to answer", o.Name, o.FromLabel())
			continue
		}
		who := r.whoPrim(msg.UUID{}, func(n string) bool { return n != "" && n == o.FromName })
		if who == "" {
			who = o.FromLabel()
		}
		if err := o.Decline(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			r.printf("slate: the offer %q from %s could not be declined: %v", o.Name, who, err)
			continue
		}
		r.printf("slate: declined offer %q from %s", o.Name, who)
	}
	for _, d := range r.sess.Dialogs() {
		if d.Recorded || d.At.Before(r.began) {
			continue
		}
		who := r.whoPrim(d.Object, func(n string) bool { return n != "" && n == d.ObjectName })
		if who == "" {
			who = sl.SenderObject.Label(d.ObjectName)
		}
		kind := "dialog"
		if d.IsTextBox() {
			kind = "textbox"
		}
		if err := r.sess.IgnoreDialog(ctx, d); err != nil {
			if ctx.Err() != nil {
				return
			}
			r.printf("slate: the %s from %s could not be left: %v", kind, who, err)
			continue
		}
		r.printf("slate: left %s from %s %s unanswered", kind, who, fmt.Sprintf("%q", d.Message))
	}
}
