package main

// Putting an object on, and taking it off again.
//
//	wear    attach an inventory object
//	detach  take a worn one off
//
// These finish what "worn" started.  Listing the attachments has been
// possible since there was a shell, and changing them has been possible
// in the sl package for just as long -- autobench has been hanging HUDs
// on an avatar with Wear and TakeOff all along -- so the only thing
// missing was a way to ask for it from the prompt.
//
// # Why the argument is not the same on both sides
//
// wear names something in inventory and detach names something worn,
// and those are different places even when they hold the same word.  So
// wear takes a path, the way bring does, and looks the item up; detach
// takes a name and matches it against what is actually on, because the
// thing it has to send is the id of the item an attachment was worn
// from and that is what the region's answer carries.
//
// It could have gone the other way -- detach resolving a path to an item
// and sending that id without ever asking what is worn.  It would work,
// and it would be silent about the case that matters: a name that is in
// inventory and not on the avatar would be detached with every
// appearance of success and nothing would happen.  Asking first costs
// one call and turns that into a sentence.
//
// # Why the object's key is not what detach takes
//
// A worn object is rezzed afresh, with a key nobody has seen before,
// every time it goes on and again at every login, so its key is worth
// nothing the moment it comes off.  The inventory item does not change.
// A key typed at detach is therefore looked for as either -- both are
// in the answer already, so neither costs anything -- but the item is
// what goes on the wire.
//
// # Wearing takes off whatever was there
//
// A point holds one attachment, and putting something on an occupied
// point knocks off what was on it.  Measured on Agni, in Pelmar Reach:
// holt was wearing "auto 11" on HUD bottom right, and
//
//	wear Objects/auto 3
//
// with no --at at all put auto 3 on HUD bottom right and took auto 11
// off.  Nothing was said about auto 11 by anybody -- it simply stopped
// being worn.  That is the viewer's Wear and it is the right behaviour,
// but the person most likely to type this is already wearing something,
// so the command's brief says it rather than leaving it to be found out.
//
// The protocol can add instead of replacing -- sl.AttachAdd, the 0x80
// bit laid over the point, which is what the viewer's "Add" menu item
// sends -- and it is deliberately not offered here.  Adding is the
// unusual half of the pair, nothing has wanted it yet, and a flag that
// changes what comes off is worth naming carefully when somebody does.
//
// # Why detach waits and TakeOff does not
//
// Nothing replies to a detach.  sl.TakeOff puts the message on the wire
// and returns, and what says the thing came off is the object no longer
// being among what is worn -- which happens some time later.  Live, that
// gap is visible: a "worn" run straight after a detach still listed the
// attachment on the point it had just been taken off, and only the run
// after that showed it gone.
//
// So cmdDetach polls until the region agrees, and sl.TakeOff is left as
// it was.  Its callers there, Worn and EnsureAttached, take a thing off
// in order to put it straight back on and do their own settling; making
// TakeOff wait would slow both of them for a confirmation they throw
// away.  What is at stake is this command's last line -- "is no longer
// worn" is a claim the SHELL makes, and the shell is what should have
// established it before printing it.
//
// # Why the names are wear and detach
//
// They are the viewer's words, from the menu somebody will have used
// before they came here.  "attach" and "remove" were the alternative and
// were worse in both halves: attach is what the protocol calls it rather
// than what a person does, and remove sits one letter from rm, which
// deletes things.

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var wearCommands = map[string]*command{
	"wear": {
		usage: "wear [--at POINT] PATH|UUID",
		brief: "put an inventory object on; whatever is on that point comes off",
		run:   cmdWear,
	},
	"detach": {
		usage: "detach NAME|PATH|UUID",
		brief: "take a worn object off, by the name of the item it was worn from",
		run:   cmdDetach,
	},
}

// attachWhereItSays is the point that means "the one the object itself
// carries", which is what somebody picking Wear rather than a point off
// the Attach To menu asks for.
//
// It is zero, and zero here is a value rather than a missing one.  The
// message template says so where it declares the field --
// "AttachmentPt U8 // 0 for default" (message_template.msg:8609) -- and
// the viewer says it in words at llviewermenu.cpp:9284, "interpret 0 as
// default location".  It is also what the viewer sends: the menu item a
// person actually uses ends at rez_attachment(item, NULL, replace),
// where a null attachment leaves attach_pt at its initial 0
// (llinventorybridge.cpp:8152), and the outfit path passes the same
// literal for the reason given above it at llappearancemgr.cpp:1686 --
// "we have no way to determine attachment point from inventory item".
// Only the Attach To menu, at llviewerattachmenu.cpp:135, names a point.
//
// So it cannot be confused with "unset": zero is what makes the
// simulator use the point stored on the object, which is how a thing
// goes back where its maker put it without anybody having to know where
// that is.  The one number it must not collide with is 0x80, the ADD bit
// (indra_constants.h:192), and that is why the points stop at 127.
//
// One thing the viewer does differently: it has not sent
// RezSingleAttachmentFromInv for years and packs
// RezMultipleAttachmentsFromInv instead (llattachmentsmgr.cpp:231), one
// item at a time in Firestorm.  The AttachmentPt field is declared
// identically in both, and the sl package's single-item message is still
// accepted, so this is a note for whoever next reads the viewer looking
// for the sender rather than a difference that matters here.
const attachWhereItSays = 0

type wearFlags struct {
	At   string `getopt:"--at=POINT   the point to wear it on, by name or number [wherever the object itself says]"`
	Help bool   `getopt:"--help -h    show what this command takes"`
}

// cmdWear puts an inventory object on.
//
// The point it reports is the one the region gave back rather than the
// one asked for, and that is the whole reason the report is worth
// printing: with no --at nobody here knows where the thing went until
// the simulator says so.
func cmdWear(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o wearFlags
	args, done, err := subOptions("wear", "PATH|UUID", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: wear [--at POINT] PATH|UUID")
	}
	point, err := attachPointArg(o.At)
	if err != nil {
		return err
	}

	path := strings.Join(args, " ")
	e, err := sh.entryAt(ctx, path)
	if err != nil {
		return err
	}
	if e.Folder {
		return fmt.Errorf("%s is a folder; wear takes one object", path)
	}
	// The item rather than the entry: what goes over the wire is the
	// name, the description and every permission mask, and an entry
	// carries none of them.
	it, err := sh.s.FindItem(ctx, e.Parent, e.Name)
	if err != nil {
		return err
	}

	a, err := sh.s.Wear(ctx, it, point, 0)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is worn on %s\n", it.Name, sl.AttachPointName(a.Point))
	return nil
}

// attachPointArg is the point --at named.
//
// A name or a number, because the two audiences want different things:
// somebody following the viewer's menu has a name and nothing else, and
// somebody working from a script or from "worn -l" has the number a
// point arrives as.
func attachPointArg(text string) (int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return attachWhereItSays, nil
	}
	if n, err := strconv.Atoi(text); err == nil {
		// The point travels in one byte with 0x80 meaning "add rather
		// than replace", so a bigger number is not a point that this
		// grid has not heard of -- it is a number that would arrive as
		// something else entirely.
		if n < 0 || n >= sl.AttachAdd {
			return 0, fmt.Errorf("--at: %d is not an attachment point; they run from 1 to %d", n, sl.AttachAdd-1)
		}
		return n, nil
	}
	if p, ok := sl.AttachPointNamed(text); ok {
		return p, nil
	}
	return 0, fmt.Errorf("--at: %q is not an attachment point; name one the way the viewer does, "+
		"as in \"left hand\" or \"HUD top right\", or give its number", text)
}

type detachFlags struct {
	Wait int  `getopt:"--wait -w=SECONDS  how long to give the region to agree it is off [15]"`
	Help bool `getopt:"--help -h          show what this command takes"`
}

// cmdDetach takes a worn object off.
//
// What it sends is the id of the INVENTORY ITEM the attachment was worn
// from, which is what DetachAttachmentIntoInv takes, so the whole of the
// work here is turning a word into that id.  A key typed instead of a
// name is looked for as either the item or the worn object, since the
// answer that has to be fetched anyway holds both and matching against
// one more field costs nothing.
func cmdDetach(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o detachFlags
	args, done, err := subOptions("detach", "NAME|PATH|UUID", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: detach NAME|PATH|UUID")
	}
	want := strings.Join(args, " ")

	worn, err := sh.s.WornObjects(ctx)
	if err != nil {
		return err
	}
	a, name, err := findWorn(ctx, sh, worn, want)
	if err != nil {
		return err
	}
	if err := sh.s.TakeOff(ctx, a.Item); err != nil {
		return err
	}
	if err := sh.waitOff(ctx, a.Item, name, o.Wait); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is no longer worn on %s\n", name, sl.AttachPointName(a.Point))
	return nil
}

// waitOff waits until the region stops listing an attachment.
//
// A detach is answered by nothing at all, so the only evidence it worked
// is the object's absence from what is worn, and that arrives some time
// after the request -- see the head of this file for the measurement.
// Printing the sentence without waiting is the mistake TeleportLocal's
// doc comment was written about: a caller told a thing happened, when
// what actually happened was that the request went out.
//
// A failure here is not a failure to detach.  It is not knowing, and the
// message says so: the request went, and the region has not agreed.
func (sh *Shell) waitOff(ctx context.Context, item msg.UUID, name string, seconds int) error {
	if seconds <= 0 {
		seconds = 15
	}
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)

	// A read that fails is remembered rather than returned.  One
	// question going unanswered in the middle of the wait is not the
	// detach failing, and a person who typed detach wants to hear about
	// the detach -- but if it is still failing at the deadline, that is
	// the reason and it should be the one printed.
	var last error
	for {
		worn, err := sh.s.WornObjects(ctx)
		switch {
		case err != nil:
			last = err
		case !stillWorn(worn, item):
			return nil
		default:
			last = nil
		}
		if time.Now().After(deadline) {
			if last != nil {
				return fmt.Errorf("%s was asked to come off, and nothing here can say whether it did: %w", name, last)
			}
			return fmt.Errorf("%s was asked to come off and the region still lists it as worn %ds later; "+
				"the request went out, so it may yet happen -- \"worn\" says what the region thinks now", name, seconds)
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// stillWorn reports whether an item is among what is worn.  The item and
// not the object: the object is what goes away.
func stillWorn(worn []*sl.Attached, item msg.UUID) bool {
	for _, a := range worn {
		if a.Item == item {
			return true
		}
	}
	return false
}

// findWorn is which attachment a word means, and what to call it.
//
// A path is allowed and only its last name is used.  Two items of one
// name in two folders are told apart by where they sit, and nothing
// about a worn object says which folder its item came from, so honouring
// the rest of the path would mean walking inventory to answer a question
// the answer cannot settle.  A name that is worn twice is refused
// instead, with the points printed, since "worn -l" gives the item ids
// that do settle it.
func findWorn(ctx context.Context, sh *Shell, worn []*sl.Attached, want string) (*sl.Attached, string, error) {
	// Nothing is named until something has to be: a key given for a key
	// is answered without reading inventory at all.
	if id, err := msg.ParseUUID(strings.TrimSpace(want)); err == nil {
		for _, a := range worn {
			if a.Item == id || a.Object.ID == id {
				name := sh.itemNames(ctx)[a.Item]
				if name == "" {
					name = a.Item.String()
				}
				return a, name, nil
			}
		}
		return nil, "", notWorn(want)
	}

	names := sl.SplitPath(want)
	if len(names) == 0 {
		return nil, "", fmt.Errorf("detach: no name given")
	}
	leaf := names[len(names)-1]

	byID := sh.itemNames(ctx)
	var found []*sl.Attached
	for _, a := range worn {
		if strings.EqualFold(byID[a.Item], leaf) {
			found = append(found, a)
		}
	}
	switch len(found) {
	case 0:
		return nil, "", notWorn(leaf)
	case 1:
		return found[0], byID[found[0].Item], nil
	}
	var where []string
	for _, a := range found {
		where = append(where, sl.AttachPointName(a.Point))
	}
	return nil, "", fmt.Errorf("%q is worn on %s; say which by its item id, which \"worn -l\" prints",
		leaf, strings.Join(where, " and on "))
}

// notWorn is the answer to a word that names nothing worn.
//
// The caveat is not padding.  What is worn is what the region has
// described, and it describes an attachment when the thing goes on and
// at login and never again -- so an avatar that was dressed before this
// session began can be wearing something that nothing here has ever
// heard of, and "no such thing" on its own would read as a denial that
// it is on at all.
func notWorn(want string) error {
	return fmt.Errorf("nothing worn is called %q; \"worn\" lists what can be seen, and the region "+
		"describes an attachment only when it goes on, so something put on before this session "+
		"started may be missing from it", want)
}
