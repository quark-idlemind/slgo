package main

// Putting an object on, and taking it off again.
//
//	wear    attach an inventory object
//	detach  take a worn one off
//	dress   put on what the Current Outfit folder names and is not on
//
// wear takes an inventory path, following a link to its item, and
// detach takes a name matched against what is worn: what it sends is
// the id of the item an attachment was worn from, which the region's
// answer carries.  A key typed at detach is looked for as the item or
// as the worn object, whose key is new at every attach and every login;
// the item is what goes on the wire.
// Why: doc/slsh.md#what-wear-and-detach-are-called-and-what-they-take
//
// wear adds rather than replaces.  The point travels in one byte with
// 0x80, ATTACHMENT_ADD (indra_constants.h:193), laid over it, and this
// command lays it on unless --replace is given -- not sl.Wear, whose
// other callers pass the byte they mean.  A bare wear sends 0x80 alone:
// add, wherever the object itself says (attachWhereItSays).  Adding
// makes it possible to wear one item twice, and wear refuses to, since
// two attachments from one item agree in every field detach could name
// one by.  What a --replace displaced is confirmed afterwards rather
// than predicted, because a replace takes off one attachment and not
// the point's worth.
// Why: doc/slsh.md#why-wear-adds-rather-than-replaces
//
// Nothing replies to a detach, so cmdDetach polls until the region
// stops listing the attachment; sl.TakeOff does not wait.

import (
	"context"
	"errors"
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
		params:   "PATH|UUID",
		flags:    func() any { return new(wearFlags) },
		brief:    "put an inventory object on, alongside whatever is already on that point",
		keywords: "wear put on attach clothing outfit accessory hud dress item",
		man:      "wear",
		run:      cmdWear,
	},
	"detach": {
		params:   "NAME|PATH|UUID",
		flags:    func() any { return new(detachFlags) },
		brief:    "take a worn object off, by the name of the item it was worn from",
		keywords: "remove take off unwear attachment clothing hat hair worn object",
		man:      "detach",
		run:      cmdDetach,
	},
	"dress": {
		flags:    func() any { return new(dressFlags) },
		brief:    "put on everything the Current Outfit folder names that is not on",
		keywords: "outfit current outfit wear everything put on clothes restore appearance",
		man:      "dress",
		run:      cmdDress,
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
// (indra_constants.h:193), and that is why the points stop at 127.
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
	At      string `getopt:"--at=POINT   the point to wear it on, by name or number [wherever the object itself says]"`
	Replace bool   `getopt:"--replace    take off whatever is on that point, instead of wearing alongside it"`
	Help    bool   `getopt:"--help -h    show what this command takes"`
}

// cmdWear puts an inventory object on.
//
// The point it reports is the one the region gave back rather than the
// one asked for, and that is the whole reason the report is worth
// printing: with no --at nobody here knows where the thing went until
// the simulator says so.  It is also what makes the displaced
// attachment nameable, since until then there is no point to look up.
func cmdWear(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o wearFlags
	args, done, err := subOptions("wear", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return usageError("wear")
	}
	point, err := attachPointArg(o.At)
	if err != nil {
		return err
	}

	path := strings.Join(args, " ")
	// thingAt and not entryAt: the id is going to the grid as the thing
	// to put on, so a link is followed to what it points at.
	// Why: doc/slsh.md#a-links-id-is-not-the-items
	e, err := sh.thingAt(ctx, path)
	if err != nil {
		return err
	}
	if e.Folder {
		return fmt.Errorf("%s is a folder; wear takes one object", path)
	}
	// What it is decides how it goes on.  An object is attached; a
	// shirt or a skin is not an object and is not attached at all --
	// see wearWearable, and sl/wearable.go for why.
	if sl.IsWearable(sl.AssetType(e.Type)) {
		return sh.wearWearable(ctx, out, e, o.Replace)
	}
	// The item rather than the entry, since sl.Wear takes an item and
	// sends its name, description, flags and permission masks.
	it := e.Item()

	// What is on now, asked before anything changes it.  An add has to
	// know whether this item is already worn, since a second copy of one
	// item is a thing detach cannot pick apart; a replace needs it as the
	// first half of a comparison, because what it displaced is what is in
	// this answer and not in the one taken afterwards.  One question
	// serves both.
	//
	// A failure here is returned rather than stepped over.  It is the
	// call detach makes and treats as fatal, and going on without the
	// answer means either wearing blind into the state that gets
	// refused, or reporting a replace with the same silence about what
	// came off that the flag exists to end.  The read AFTER the wear is
	// the one that may fail quietly -- by then the wear has happened.
	// Why: doc/slsh.md#why-wear-will-not-put-one-item-on-twice
	before, err := sh.s.WornObjects(ctx)
	if err != nil {
		return err
	}
	if !o.Replace {
		if a, ok := wornFrom(before, it.ID); ok {
			return fmt.Errorf("%s is already worn on %s; wearing it again would put on a second copy "+
				"that nothing here could tell from the first, since an attachment is known by the item "+
				"it came from and both would answer to this one -- \"wear --replace\" puts it on in "+
				"place of what is there, and \"detach %s\" takes it off",
				it.Name, sl.AttachPointName(a.Point), it.Name)
		}
	}

	// The byte the message carries, which is the point with the add bit
	// over it, composed here and not inside sl.Wear.
	// Why: doc/slsh.md#why-wear-adds-rather-than-replaces
	send := point
	if !o.Replace {
		send |= sl.AttachAdd
	}
	a, err := sh.s.Wear(ctx, it, send, 0)
	if err != nil {
		return err
	}
	// One line, built and then written: the terminal takes a write with
	// no newline in it as a line of its own (termWriter), so a sentence
	// printed in two halves would arrive as two.
	line := fmt.Sprintf("%s is worn on %s", it.Name, sl.AttachPointName(a.Point))
	if o.Replace {
		if off := cameOff(ctx, sh, before, a.Point, it.ID); off != "" {
			line += fmt.Sprintf("; %s came off", off)
		}
	}
	// And recorded, so that it comes back at the next login.  The
	// Current Outfit folder is the client's record of what is worn,
	// writing it is the client's job, and it is what a viewer -- and
	// dress, and slbotd -- read to put back whatever the simulator
	// did not.
	//
	// Then baked, as a viewer bakes after every change to the folder.
	// That is what brings the simulator's own list of what is worn up
	// to date, and worn and dress check against that list.
	// Why: doc/outfit.md#a-bake-after-every-change-to-the-folder
	//
	// A failure of either is not a failure to wear -- the thing is on
	// the avatar -- so it is said on the same line rather than
	// returned.  Losing the whole report of a successful wear because
	// the bookkeeping did not go through would be the wrong way round.
	if err := sh.s.RememberWorn(ctx, it); err != nil {
		line += fmt.Sprintf("; it is not in the Current Outfit folder, so it will not come "+
			"back at the next login: %v", err)
	} else if err := sh.s.UpdateAppearance(ctx); err != nil {
		line += fmt.Sprintf("; the appearance was not rebaked, so the simulator's list "+
			"of what is worn does not show it yet: %v", err)
	}
	fmt.Fprintln(out, line)
	return nil
}

// cameOff names what a wear displaced: what was on the point before,
// asked for again afterwards, and gone.
//
// Three conditions, and each of them is load bearing.  On that point,
// because a replace reaches no further and something that vanished
// elsewhere in the meantime is not this command's doing -- the point is
// the one from the region's answer rather than the one requested, since
// with no --at nothing here knows it until then.  Gone, because a
// replace takes off one attachment and leaves any others where they are;
// that was measured.  And not the item just worn: a replace of something
// already on that point takes the old copy off and puts a new one on, so
// which of the two the region has got round to describing when this asks
// decides whether the item looks worn -- an ordering nothing here
// controls, and "X is worn on chest; X came off" is a sentence arguing
// with itself whichever way it falls out.
//
// Only a replacing wear displaces anything, which is why the caller asks
// only for one.  An add landing on an occupied point leaves what is
// there, and this would then cost a read to confirm that nothing
// happened.
//
// A read that fails is empty rather than an error: the wear has already
// happened and been reported, and the caller's line is better without
// its clause than the whole command is as a failure.
//
// Names are looked up only when there is something to name, since that
// costs a walk of inventory.  A name that is not there falls back to the
// item's id, the way detach's does: an id is poor but it is true, and it
// is what "worn -l" prints.
// Why: doc/slsh.md#naming-what-a-replace-took-off
func cameOff(ctx context.Context, sh *Shell, before []*sl.Attached, point int, item msg.UUID) string {
	now, err := sh.s.WornObjects(ctx)
	if err != nil {
		return ""
	}
	var ids []msg.UUID
	for _, a := range before {
		if a.Point != point || a.Item == item {
			continue
		}
		if _, on := wornFrom(now, a.Item); !on {
			ids = append(ids, a.Item)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	byID := sh.itemNames(ctx)
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		name := byID[id]
		if name == "" {
			name = id.String()
		}
		names = append(names, name)
	}
	return strings.Join(names, " and ")
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
		// something else entirely.  Somebody typing a number means a
		// point by it and nothing more; the add bit is laid over what
		// comes back from here, by the caller that knows whether it is
		// adding.
		if n < 0 || n >= sl.AttachAdd {
			return 0, fmt.Errorf("--at: %d is not an attachment point; they run from 1 to %d, "+
				"and 0 asks for wherever the object itself says", n, sl.AttachAdd-1)
		}
		return n, nil
	}
	if p, ok := sl.AttachPointNamed(text); ok {
		return p, nil
	}
	return 0, fmt.Errorf("--at: %q is not an attachment point; name one the way the viewer does, "+
		"as in \"left hand\" or \"HUD top right\", or give its number", text)
}

// wearWearable puts on something that is not an object.
//
// A wearable is worn by linking it into the Current Outfit folder and
// asking for a rebake; there is no attaching and no attachment point,
// so there is no point to report.  What is reported instead is the slot
// -- "worn as shirt" -- because that is the thing a person is choosing
// between when they have four shirts and can wear one of each layer.
//
// The slot is also why a body part replaces whether or not --replace
// was given.  Two skins is not a state an avatar can be in, so a bare
// wear of one is a replace however it is worded, and saying what came
// off is the honest way to do that rather than the quiet way.
func (sh *Shell) wearWearable(ctx context.Context, out io.Writer, e sl.Entry, replace bool) error {
	it := e.Item()
	slot, ok := sl.SlotOf(sl.AssetType(it.Type), it.Flags)
	if !ok {
		return fmt.Errorf("%s is %s and has no wearable slot", it.Name, aKind(kindOf(e)))
	}

	off, err := sh.s.WearWearable(ctx, it, replace)
	if errors.Is(err, sl.ErrAlreadyWorn) {
		return fmt.Errorf("%s is already worn as %s; wearing it again would put a second "+
			"link to one item in the Current Outfit folder, which nothing here could then "+
			"tell apart -- \"detach %s\" takes it off",
			it.Name, slot, it.Name)
	}
	if err != nil {
		return err
	}

	line := fmt.Sprintf("%s is worn as %s", it.Name, slot)
	if len(off) > 0 {
		line += "; " + strings.Join(off, ", ") + " came off"
	}
	fmt.Fprintln(out, line)
	return nil
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
	args, done, err := subOptions("detach", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return usageError("detach")
	}
	want := strings.Join(args, " ")

	worn, err := sh.s.WornObjects(ctx)
	if err != nil {
		return err
	}
	a, name, notAttached := findWorn(ctx, sh, worn, want)
	if notAttached != nil {
		// Not an attachment.  A system wearable is not worn on a point
		// at all -- it is a link in the Current Outfit folder -- so it
		// is looked for there before the refusal is printed.  Second
		// rather than first, so that the ordinary case pays for no
		// extra reads.
		done, err := sh.detachFromOutfit(ctx, out, want)
		if done || err != nil {
			return err
		}
		return notAttached
	}
	if err := sh.s.TakeOff(ctx, a.Item); err != nil {
		return err
	}
	waited := sh.waitOff(ctx, a.Item, name, o.Wait)
	// Out of the folder as well as off the avatar, whether or not the
	// region has caught up: the request went, and a link left behind
	// would put the thing back on at the next login.  Then baked, as a
	// viewer bakes after taking an attachment's link out.
	// Why: doc/outfit.md#a-bake-after-every-change-to-the-folder
	var left string
	var leftErr error
	if _, err := sh.s.ForgetWorn(ctx, a.Item); err != nil {
		left, leftErr = "its link is still in the Current Outfit folder, so it will "+
			"come back at the next login", err
	} else if err := sh.s.UpdateAppearance(ctx); err != nil {
		left, leftErr = "the appearance was not rebaked, so the simulator's list "+
			"of what is worn still shows it", err
	}
	if waited != nil {
		// Without the "no longer worn" half, which the wait did not
		// confirm, but not without what was left behind.
		if leftErr != nil {
			return errors.Join(waited, fmt.Errorf("%s: %s: %w", name, left, leftErr))
		}
		return waited
	}
	line := fmt.Sprintf("%s is no longer worn on %s", name, sl.AttachPointName(a.Point))
	if leftErr != nil {
		line += fmt.Sprintf("; %s: %v", left, leftErr)
	}
	fmt.Fprintln(out, line)
	return nil
}

// detachFromOutfit takes something off using the Current Outfit folder,
// and says whether it found anything to take off.
//
// This is the answer to two different failures that look the same from
// the prompt.  A system wearable is not worn on a point at all, so it
// was never in the list detach was searching.  And an attachment the
// region has not described to this session is not in that list either,
// although it is plainly on the avatar -- which is the ordinary state
// of affairs after a reconnect, and was the report that prompted this:
// an object visible in world, named in the folder, and refused here as
// not worn.
//
// The folder is enough to take either off.  A wearable comes off by
// dropping its link; an attachment comes off by
// DetachAttachmentIntoInv, which takes the INVENTORY item id and needs
// to know nothing about the object -- so not being able to see the
// object is no impediment at all.
//
// Matched on the name, the item id or the link id, because all three
// are things a listing here prints and any of them may be what got
// copied.  The name goes through sl.PickNamedFunc: exactly, in the case
// it has.  Several links of it are refused with their item ids, and a
// name only a link in another case has is refused with that as the
// hint, which says more than "not worn" would.
//
// A folder that cannot be read comes back as an error and not as "no
// such thing", because not knowing is not the same as knowing there is
// none, and the caller is about to print a refusal saying the thing is
// not worn.
func (sh *Shell) detachFromOutfit(ctx context.Context, out io.Writer, want string) (bool, error) {
	outfit, err := sh.s.Outfit(ctx)
	if err != nil {
		return false, err
	}
	var links []sl.OutfitLink
	for _, l := range outfit {
		if !l.Folder {
			links = append(links, l)
		}
	}

	var l sl.OutfitLink
	if id, err := msg.ParseUUID(strings.TrimSpace(want)); err == nil {
		var found []sl.OutfitLink
		for _, x := range links {
			if x.Item == id || x.Link == id {
				found = append(found, x)
			}
		}
		switch len(found) {
		case 0:
			return false, nil
		case 1:
			l = found[0]
		default:
			return true, fmt.Errorf("%q is %d things in the Current Outfit folder, and this "+
				"cannot tell them apart -- name one by its item id, which \"worn -l\" prints",
				want, len(found))
		}
	} else {
		var err error
		l, err = sl.PickNamedFunc(links, want, "", "in the Current Outfit folder",
			func(l sl.OutfitLink) (string, msg.UUID) { return l.Name, l.Item })
		var ne *sl.NameError
		switch {
		case err == nil:
		case errors.As(err, &ne) && (len(ne.IDs) > 0 || len(ne.Near) > 0):
			return true, err
		default:
			return false, nil
		}
	}

	if l.Kind == sl.AssetBodypart {
		// "no eyes", "no hair": the slots a body part can occupy are
		// all words that take no article, which is why the sentence
		// is shaped to avoid needing one.
		return true, fmt.Errorf("%s is a body part, and a body part does not come off: there "+
			"is no such thing as an avatar with no %s.  Wearing another one puts it in place "+
			"of this, which is the way out of it", l.Name, l.Slot)
	}
	if l.Wearable {
		if err := sh.s.TakeOffWearable(ctx, l.Link); err != nil {
			return true, err
		}
		fmt.Fprintf(out, "%s is no longer worn as %s\n", l.Name, l.Slot)
		return true, nil
	}

	// An object.  Both halves: the request that takes it off the
	// avatar, and the link that says it should be on at the next
	// login.  Then baked, as cmdDetach and cmdWear bake after the same
	// change to the folder: a viewer asks for a bake after it takes an
	// attachment's link out, and that is what brings the simulator's
	// list of what is worn up to date.
	// Why: doc/outfit.md#a-bake-after-every-change-to-the-folder
	if err := sh.s.TakeOff(ctx, l.Item); err != nil {
		return true, err
	}
	if err := sh.s.DeleteItem(ctx, l.Link); err != nil {
		return true, fmt.Errorf("%s was asked to come off, and its link is still in the "+
			"Current Outfit folder: %w", l.Name, err)
	}
	// Not waited for.  waitOff watches the region stop listing the
	// object, and this path is reached precisely because the region
	// never listed it here in the first place, so the wait could only
	// time out and report not knowing.
	line := fmt.Sprintf("%s was asked to come off, and is out of the outfit", l.Name)
	if err := sh.s.UpdateAppearance(ctx); err != nil {
		line += fmt.Sprintf("; the appearance was not rebaked, so the simulator's list "+
			"of what is worn may still show it: %v", err)
	}
	fmt.Fprintln(out, line)
	return true, nil
}

// waitOff waits until the region stops listing an attachment.
//
// A detach is answered by nothing at all, so the only evidence it worked
// is the object's absence from what is worn, and that arrives some time
// after the request.  Printing the sentence without waiting is the
// mistake TeleportLocal's doc comment was written about: a caller told a
// thing happened, when what actually happened was that the request went
// out.
//
// A failure here is not a failure to detach.  It is not knowing, and the
// message says so: the request went, and the region has not agreed.
// Why: doc/slsh.md#why-detach-waits-and-takeoff-does-not
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
		_, on := wornFrom(worn, item)
		switch {
		case err != nil:
			last = err
		case !on:
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

// wornFrom is the attachment an item is on as, if it is on at all.  The
// item and not the object: the object is what goes away, and the item is
// what both callers hold -- one waiting for it to stop being listed, the
// other refusing to put a second copy of it on.
func wornFrom(worn []*sl.Attached, item msg.UUID) (*sl.Attached, bool) {
	for _, a := range worn {
		if a.Item == item {
			return a, true
		}
	}
	return nil, false
}

// findWorn is which attachment a word means, and what to call it.
//
// A path is allowed and only its last name is used.  Two items of one
// name in two folders are told apart by where they sit, and nothing
// about a worn object says which folder its item came from, so honouring
// the rest of the path would mean walking inventory to answer a question
// the answer cannot settle.  The name is its item's, picked out through
// sl.PickNamedFunc: exactly, in the case it has, and a name that is worn
// twice is refused with the item ids, which settle it.
func findWorn(ctx context.Context, sh *Shell, worn []*sl.Attached, want string) (*sl.Attached, string, error) {
	// Nothing is named until something has to be: a key is matched
	// without reading inventory, which is read only to name what it
	// matched.
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
	a, err := sl.PickNamedFunc(worn, leaf, "", "on the avatar",
		func(a *sl.Attached) (string, msg.UUID) { return byID[a.Item], a.Item })
	var ne *sl.NameError
	switch {
	case err == nil:
		return a, byID[a.Item], nil
	case !errors.As(err, &ne):
		return nil, "", err
	case len(ne.IDs) > 0:
		// The points as well as the ids, since where each is worn is
		// what a person can see.
		var where []string
		for _, id := range ne.IDs {
			if a, ok := wornFrom(worn, id); ok {
				where = append(where, sl.AttachPointName(a.Point))
			}
		}
		return nil, "", fmt.Errorf("%q is worn on %s; say which by its item id, which \"worn -l\" "+
			"prints beside the point:\n  %s", leaf, strings.Join(where, " and on "),
			strings.Join(idStrings(ne.IDs), "\n  "))
	case len(ne.Near) > 0:
		return nil, "", err
	}
	return nil, "", notWorn(leaf)
}

// idStrings is ids as text, for a list in a refusal.
func idStrings(ids []msg.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
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

type dressFlags struct {
	Wait int  `getopt:"--wait -w=SECONDS  how long to give the region to describe what it rezzed [20]"`
	Help bool `getopt:"--help -h          show what this command takes"`
}

// cmdDress puts back on whatever the Current Outfit folder names and
// the avatar is not wearing.
//
// The fault it is for: the simulator puts most of an avatar's
// attachments back at login, but not reliably all of them.  A viewer
// puts on whatever the folder names and is still missing, a second or
// two after arriving, and nobody sees it happen.  Nothing here did, so
// an avatar dressed from this shell could come back from a login
// missing part of its outfit and stay that way.
//
// The report is by name and in three parts, because "dressed" is not
// one outcome.  What was already on is worth saying so that a person
// who runs this twice is not told the second run did nothing; what
// went on is the answer; and what was asked for and never confirmed is
// neither a success nor a failure -- the confirmation is the region
// describing the new object, which is the thing most likely to be
// lost.
func cmdDress(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o dressFlags
	rest, done, err := subOptions("dress", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) > 0 {
		return usageError("dress", "nothing; it puts on what the outfit names")
	}

	report, err := sh.s.RestoreOutfit(ctx, time.Duration(o.Wait)*time.Second)
	if err != nil {
		return err
	}
	// A line per thing that happened, and nothing for the things that
	// did not.  The count of what was already on is said whenever
	// anything else is: without it, a run that asked for six and
	// confirmed none reads as an outfit that has gone, when in fact
	// most of it was on the whole time.
	if len(report.Worn) == 0 && len(report.Missing) == 0 && len(report.Doubled) == 0 {
		fmt.Fprintf(out, "already wearing all %d of them\n", len(report.Already))
		sayUndescribed(out, report)
		return nil
	}
	if n := len(report.Already); n > 0 {
		fmt.Fprintf(out, "%d already on\n", n)
	}
	if len(report.Worn) > 0 {
		fmt.Fprintf(out, "put on %s\n", strings.Join(report.Worn, ", "))
	}
	if len(report.Missing) > 0 {
		fmt.Fprintf(out, "asked for and not described: %s\n", strings.Join(report.Missing, ", "))
	}
	if len(report.Doubled) > 0 {
		fmt.Fprintf(out, "worn more than once, which detach undoes: %s\n",
			strings.Join(report.Doubled, ", "))
	}
	sayUndescribed(out, report)
	if report.Unbaked != nil {
		fmt.Fprintf(out, "not rebaked afterwards, so the simulator's list of what is worn "+
			"is behind: %v\n", report.Unbaked)
	}
	return nil
}

// sayUndescribed adds what the simulator's list says that the region's
// descriptions do not: that something is on which nothing has
// described.  That is the one case where "asked for and not described"
// may mean "on" -- and it is said only when it is so, since a line
// saying all is well on every run is a line nobody reads.
func sayUndescribed(out io.Writer, r *sl.OutfitReport) {
	if r.Unknown || r.Undescribed == 0 {
		return
	}
	fmt.Fprintf(out, "the simulator lists %d %s that nothing here has described\n",
		r.Undescribed, plural(r.Undescribed, "attachment", "attachments"))
}
