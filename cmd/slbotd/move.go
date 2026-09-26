package main

// Moving: teleporting, landmarks, sitting and touching.
//
// These are the commands that change where the avatar is, and touch,
// which acts on something where it is.  Every one that moves the avatar
// ends by saying where it ended up.  A person sending commands over
// instant messages cannot see the avatar, so "it worked" is not an
// answer -- the position is.

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

var moveCommands = map[string]*command{
	"tp": {
		params: "REGION [X Y Z] | X Y Z | home",
		flags:  func() any { return new(waitFlags) },
		brief:  "teleport: to a region, to a position in this one, or home",
		group:  groupMoving,
		run:    cmdTP,
	},
	"landmark": {
		params: "[NAME]",
		flags:  func() any { return new(landmarkFlags) },
		brief:  "the landmarks in inventory; with a name, teleport to it",
		group:  groupMoving,
		run:    cmdLandmark,
	},
	"sit": {
		params: "[NAME|UUID]",
		flags:  func() any { return new(waitFlags) },
		brief:  "sit on an object; with nothing named, sit on the ground",
		group:  groupMoving,
		run:    cmdSit,
	},
	"stand": {
		flags: func() any { return new(waitFlags) },
		brief: "get up",
		group: groupMoving,
		run:   cmdStand,
	},
	"touch": {
		params: "NAME|UUID",
		flags:  func() any { return new(helpOnly) },
		brief:  "touch an object, the way a click does",
		group:  groupMoving,
		run:    cmdTouch,
	},
}

// waitFlags is how long to believe in something the simulator has to
// confirm.  Shared by the commands that wait for the same kind of
// answer, since they are all asking one question: how long before a
// silence is a failure.
type waitFlags struct {
	Wait int  `getopt:"--wait -w=SECONDS  how long to wait for the simulator to answer"`
	Help bool `getopt:"--help -h          show what this command takes"`
}

// teleportWait is what a teleport is given when nothing says otherwise.
// A teleport across the grid has been measured at five seconds; a
// minute is a silence rather than a slow one.
const teleportWait = 60 * time.Second

func (o waitFlags) after(def time.Duration) time.Duration {
	if o.Wait > 0 {
		return time.Duration(o.Wait) * time.Second
	}
	return def
}

// cmdTP teleports the avatar.
//
// Three forms, told apart by what was typed rather than by a flag:
//
//	tp home            the account's home position
//	tp 128 128 25      a position in the region the avatar is in
//	tp Example 128 128 25   a position in another region
//
// A region is never chosen on somebody's behalf.  The map searches by
// prefix, so a name typed in full comes back beside every longer name
// that begins with it; an exact match wins outright and anything else
// that matched twice is listed and refused.  Guessing would put the
// avatar somewhere nobody asked for, several minutes away from whoever
// is watching.
func cmdTP(ctx context.Context, r *req, out io.Writer, args []string) error {
	var o waitFlags
	rest, done, err := subOptions("tp", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) == 0 {
		return usage("tp", "a region, three numbers, or home")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	wait := o.after(teleportWait)

	if len(rest) == 1 && strings.EqualFold(rest[0], "home") {
		if err := s.GoHome(ctx, wait); err != nil {
			return err
		}
		fmt.Fprintln(out, "went home")
		return sayPosition(ctx, s, out)
	}

	// Three numbers at the end are a position, and whatever is in front
	// of them is a region.  Nothing else is a position, so "tp Example"
	// is a region and lands wherever the parcel sends it.
	region, at, err := teleportTarget(rest)
	if err != nil {
		return err
	}

	if region == "" {
		if err := s.TeleportLocal(ctx, at, wait); err != nil {
			return err
		}
		return sayPosition(ctx, s, out)
	}

	found, err := regionNamed(ctx, s, region)
	if err != nil {
		return err
	}
	if err := s.Teleport(ctx, found.Handle, at, wait); err != nil {
		return err
	}
	return sayPosition(ctx, s, out)
}

// teleportTarget reads a region name and a position out of what was
// typed.  Either may be missing; a position with no region is a move
// inside this one, and a region with no position is the middle of it,
// which is what a viewer's map does.
func teleportTarget(args []string) (region string, at msg.Vector3, err error) {
	n := len(args)
	if n >= 3 && allNumbers(args[n-3:]) {
		x, _ := strconv.ParseFloat(args[n-3], 32)
		y, _ := strconv.ParseFloat(args[n-2], 32)
		z, _ := strconv.ParseFloat(args[n-1], 32)
		at = msg.Vector3{X: float32(x), Y: float32(y), Z: float32(z)}
		args = args[:n-3]
	} else {
		// The middle of the region, at ground level, which is where a
		// teleport with no position is sent anyway.
		at = msg.Vector3{X: 128, Y: 128, Z: 25}
	}
	if len(args) == 0 {
		if at == (msg.Vector3{X: 128, Y: 128, Z: 25}) {
			return "", at, fmt.Errorf("nothing to teleport to")
		}
		return "", at, nil
	}
	return strings.Join(args, " "), at, nil
}

func allNumbers(ss []string) bool {
	for _, s := range ss {
		if _, err := strconv.ParseFloat(strings.TrimSuffix(s, ","), 64); err != nil {
			return false
		}
	}
	return true
}

// regionNamed finds the one region a name means, and refuses the name
// that means several.
func regionNamed(ctx context.Context, s *sl.Session, name string) (sl.MapRegion, error) {
	found, err := s.FindRegions(ctx, name, 15*time.Second)
	if err != nil {
		return sl.MapRegion{}, err
	}
	return pickRegion(found, name)
}

// pickRegion is the choosing, kept apart from the asking so that the
// rule can be read and tested without a map to ask.
//
// An exact name wins outright.  Anything else that matched twice is
// listed and refused: the map searches by prefix, so a name typed in
// full comes back beside every longer name beginning with it, and
// choosing between them would put the avatar somewhere nobody asked
// for.
func pickRegion(found []sl.MapRegion, name string) (sl.MapRegion, error) {
	switch len(found) {
	case 0:
		return sl.MapRegion{}, fmt.Errorf("the map knows no region beginning with %q", name)
	case 1:
		return found[0], nil
	}
	for _, f := range found {
		if strings.EqualFold(f.Name, name) {
			return f, nil
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d regions begin with %q; say which:", len(found), name)
	for _, f := range found {
		fmt.Fprintf(&b, "\n  %s", f.Name)
	}
	return sl.MapRegion{}, fmt.Errorf("%s", b.String())
}

// landmarkFlags is what landmark was asked for.
type landmarkFlags struct {
	Go   bool `getopt:"--go -g            teleport to the landmark named"`
	Wait int  `getopt:"--wait -w=SECONDS  how long to wait for the teleport"`
	Help bool `getopt:"--help -h          show what this command takes"`
}

// landmarkDepth is how far down inventory landmarks are looked for.
// Four levels reaches the Landmarks folder and a couple of levels of
// sorting inside it, which is where people keep them, without fetching
// a whole inventory to answer a question about eight items.
const landmarkDepth = 4

// cmdLandmark lists the landmarks in inventory, or goes to one.
//
// Listing is the default and going needs --go, which is the other way
// round from the obvious.  It is deliberate: a name typed at a shell
// prompt is a name somebody is looking at, and a name sent to a daemon
// over an instant message may be a name somebody half remembered.
// Moving the avatar across the grid on a half-remembered word is the
// mistake worth making impossible, and --go is one flag.
//
// A landmark in the trash is not a landmark.  A viewer's delete moves
// things there and leaves them working, so without this a deleted
// landmark would be as reachable as a kept one and would make a kept
// one ambiguous with its own corpse.  A link is not one either: a
// link's asset is the ITEM id of what it points at, and an item id sent
// to the grid as a landmark is answered with silence.
func cmdLandmark(ctx context.Context, r *req, out io.Writer, args []string) error {
	var o landmarkFlags
	rest, done, err := subOptions("landmark", &o, out, args)
	if err != nil || done {
		return err
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	kept, err := landmarksHeld(ctx, s)
	if err != nil {
		return err
	}

	want := strings.Join(rest, " ")
	if want == "" {
		if len(kept) == 0 {
			fmt.Fprintln(out, "no landmarks in inventory")
			return nil
		}
		for _, e := range kept {
			fmt.Fprintln(out, e.Path)
		}
		return nil
	}

	e, err := pickLandmark(kept, want)
	if err != nil {
		return err
	}
	lm, err := s.Landmark(ctx, e.Asset)
	if err != nil {
		return err
	}
	if !o.Go {
		fmt.Fprintf(out, "%s\n", e.Path)
		fmt.Fprintf(out, "region %s at %.0f, %.0f, %.0f\n",
			lm.Region, lm.Position.X, lm.Position.Y, lm.Position.Z)
		fmt.Fprintf(out, "%slandmark --go %s teleports there\n", r.d.cfg.Prefix, e.Name)
		return nil
	}

	wait := teleportWait
	if o.Wait > 0 {
		wait = time.Duration(o.Wait) * time.Second
	}
	if err := s.GoTo(ctx, e.Asset, e.Name, wait); err != nil {
		return err
	}
	return sayPosition(ctx, s, out)
}

// pickLandmark is the one kept landmark a word means.
//
// The whole name first, through sl.PickNamed: matched exactly, in the
// case it has, and refused with the ids when several landmarks have it.
// A name nothing has but something has in another case is refused with
// that as the hint, rather than handed to the search below, which would
// find it and take it as if it had been typed.  Only then is the word
// searched for as part of a name, ignoring case, as a search does; one
// hit is the answer, and several are listed and refused.
func pickLandmark(kept []sl.Entry, want string) (sl.Entry, error) {
	e, err := sl.PickNamed(kept, want, "landmark", "")
	if err == nil {
		return e, nil
	}
	if ne := (*sl.NameError)(nil); errors.As(err, &ne) && (len(ne.IDs) > 0 || len(ne.Near) > 0) {
		return sl.Entry{}, err
	}
	lower := strings.ToLower(want)
	var matched []sl.Entry
	for _, e := range kept {
		if strings.Contains(strings.ToLower(e.Name), lower) {
			matched = append(matched, e)
		}
	}
	switch len(matched) {
	case 0:
		return sl.Entry{}, fmt.Errorf("no landmark called %q", want)
	case 1:
		return matched[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d landmarks answer to %q:", len(matched), want)
	for _, e := range matched {
		fmt.Fprintf(&b, "\n  %s", e.Path)
	}
	return sl.Entry{}, fmt.Errorf("%s", b.String())
}

// landmarksHeld is the landmarks in inventory that are real, kept and
// followable.  See cmdLandmark for what is left out and why.
func landmarksHeld(ctx context.Context, s *sl.Session) ([]sl.Entry, error) {
	all, err := s.ListInventory(ctx, "", landmarkDepth)
	if err != nil {
		return nil, err
	}
	var bins []string
	for _, e := range all {
		// By the folder's preferred type and not by its name: the
		// trash is the trash because of its type, it can be renamed,
		// and an account made through a viewer in another language
		// never called it "Trash" at all.
		if e.Folder && e.Type == sl.FolderTrash {
			bins = append(bins, e.Path+string(sl.PathSeparator))
		}
	}
	var kept []sl.Entry
	for _, e := range all {
		if e.Folder || e.IsLink || sl.AssetType(e.Type) != sl.AssetLandmark {
			continue
		}
		binned := false
		for _, b := range bins {
			if strings.HasPrefix(e.Path, b) {
				binned = true
				break
			}
		}
		if !binned {
			kept = append(kept, e)
		}
	}
	return kept, nil
}

func cmdSit(ctx context.Context, r *req, out io.Writer, args []string) error {
	var o waitFlags
	rest, done, err := subOptions("sit", &o, out, args)
	if err != nil || done {
		return err
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	wait := o.after(15 * time.Second)

	if len(rest) == 0 {
		if err := s.SitOnGround(ctx, wait); err != nil {
			return err
		}
		fmt.Fprintln(out, "sat on the ground")
		return sayPosition(ctx, s, out)
	}
	target, err := objectNamed(ctx, s, strings.Join(rest, " "))
	if err != nil {
		return err
	}
	seat, err := s.Sit(ctx, target, wait)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "sat on %s\n", seat)
	return sayPosition(ctx, s, out)
}

func cmdStand(ctx context.Context, r *req, out io.Writer, args []string) error {
	var o waitFlags
	rest, done, err := subOptions("stand", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 0 {
		return usage("stand", "stand takes nothing; the simulator knows what this avatar is on")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	if err := s.Stand(ctx, o.after(15*time.Second)); err != nil {
		return err
	}
	fmt.Fprintln(out, "stood up")
	return sayPosition(ctx, s, out)
}

func cmdTouch(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("touch", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	if len(rest) == 0 {
		return usage("touch", "an object to touch")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	target, err := objectNamed(ctx, s, strings.Join(rest, " "))
	if err != nil {
		return err
	}
	if err := s.Touch(ctx, target, sl.Touch{}); err != nil {
		return err
	}
	fmt.Fprintf(out, "touched %s\n", target.Name)
	return nil
}

// objectNamed turns what somebody typed into an object in the region.
//
// A uuid is taken as itself and a name is looked up among what the
// region has described.  A word that names two things is refused with
// both ids rather than guessed at: the commands that take an object sit
// on it, touch it and take it away, and the wrong guess is not
// something that can be undone from an instant message.
func objectNamed(ctx context.Context, s *sl.Session, what string) (*sl.Object, error) {
	if id, err := msg.ParseUUID(what); err == nil {
		seen, err := s.ObjectByID(ctx, id, objectWait)
		if err != nil {
			return nil, err
		}
		return &seen.Object, nil
	}
	found, err := s.ObjectsNamed(ctx, what, objectWait)
	if err != nil {
		return nil, err
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("nothing called %q is in range", what)
	case 1:
		return &found[0].Object, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d objects are called %q; name one by uuid:", len(found), what)
	for _, f := range found {
		fmt.Fprintf(&b, "\n  %s", f.ID)
	}
	return nil, fmt.Errorf("%s", b.String())
}
