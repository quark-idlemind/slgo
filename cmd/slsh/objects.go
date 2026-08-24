package main

// Moving things and moving about: giving inventory away, taking what is
// offered, duplicating an item, going somewhere, and the sessions the
// daemon holds.

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

var objectCommands = map[string]*command{
	"give": {
		params: "WHO PATH",
		flags:  func() any { return new(helpOnly) },
		brief:  "offer an inventory item to somebody",
		man:    "give",
		run:    cmdGive,
	},
	"cp": {
		params: "PATH NAME",
		flags:  func() any { return new(helpOnly) },
		brief:  "copy an inventory item under a new name",
		man:    "cp",
		run:    cmdCopy,
	},
	"tp": {
		params: "REGION [X Y Z] | X Y Z",
		flags:  func() any { return new(tpOptions) },
		brief:  "move to another region by name, or to a position: X Y Z here, outside the region for the next one along, ~N to move by",
		man:    "tp",
		run:    cmdTP,
	},
	"move": {
		params: "NAME X Y Z",
		flags:  func() any { return new(helpOnly) },
		brief:  "move a rezzed object to a position",
		man:    "move",
		run:    cmdMove,
	},
	"agents": {
		flags: func() any { return new(helpOnly) },
		brief: "the sessions this daemon holds, oldest first; * is the default",
		man:   "agents",
		run:   cmdAgents,
	},
	"login": {
		params: "NAME",
		flags:  func() any { return new(forceOptions) },
		brief:  "bring an avatar up that is not running",
		man:    "login",
		run:    cmdLogin,
	},
	"logout": {
		params: "NAME",
		flags:  func() any { return new(forceOptions) },
		brief:  "log NAME out until it is logged back in via login",
		man:    "logout",
		run:    cmdLogout,
	},
	"auto": {
		flags: func() any { return new(autoOptions) },
		brief: "the objects benchmarks run in; -n sets up that many",
		man:   "auto",
		run:   cmdAuto,
	},
}

// cmdGive offers an item to somebody.
//
// An offer, not a transfer: nothing moves until they accept, and
// nothing here can tell whether they did.  Saying "offered" rather than
// "gave" is the honest report.
//
// The name is read by whoAndRest rather than off the first word, which
// matters more here than anywhere: a two-word name read as one word
// leaves the last name at the front of the path, so the item is either
// not found at all or is the wrong one, offered to the right person.
func cmdGive(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	rest, done, err := subOptions("give", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) < 2 {
		return usageError("give")
	}

	who, name, path, err := sh.whoAndRest(ctx, rest)
	if err != nil {
		return err
	}
	if len(path) == 0 {
		return usageError("give", "which item to offer; a name with a space in it is one argument")
	}

	e, err := sh.entryAt(ctx, strings.Join(path, " "))
	if err != nil {
		return err
	}
	// A folder goes as AssetCategory, which is how the protocol offers
	// a whole folder; anything else goes as what it is.
	kind := sl.AssetType(e.Type)
	if e.Folder {
		kind = sl.AssetCategory
	}
	if err := sh.s.GiveToAvatar(ctx, who, e.ID, e.Name, int8(kind)); err != nil {
		return err
	}
	fmt.Fprintf(out, "offered %q to %s\n", e.Name, name)
	return nil
}

// cmdCopy duplicates an item.
//
// The point is not tidiness.  An avatar that may not rez cannot make an
// object at all, but it can copy one it already owns, because copying
// asks the land nothing.
func cmdCopy(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	rest, done, err := subOptions("cp", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) < 2 {
		return usageError("cp")
	}

	e, err := sh.entryAt(ctx, rest[0])
	if err != nil {
		return err
	}
	if e.Folder {
		return fmt.Errorf("%q is a folder; only items can be copied", rest[0])
	}
	name := strings.Join(rest[1:], " ")

	// Into the folder the shell is in, which is what cp with a bare
	// name means everywhere else.
	_, folder, err := sh.resolveDir(ctx, ".")
	if err != nil {
		return err
	}
	copied, err := sh.s.CopyItem(ctx, e.ID, folder, name, 60*time.Second)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n", copied.ID)
	return nil
}

// tpOptions is what tp was asked for.
type tpOptions struct {
	Wait int  `getopt:"--wait -w=SECONDS  how long to wait for the avatar to arrive [30]"`
	Help bool `getopt:"--help -h          show what this command takes"`
}

// shellTeleportTimeout is how long this shell waits for a teleport to
// another region before saying nothing answered.
//
// Thirty seconds, where sl.DefaultTeleportTimeout is ninety.  That
// constant was set before a teleport had ever been timed; forty moves
// between Pelmar Reach and Sandbox Goguen have since been measured at
// 355 milliseconds to 4.95 seconds, so ninety is two orders of magnitude
// above what it covers.  Thirty is six times the slowest move measured,
// which leaves room for a grid having a bad day, and it is what a
// teleport inside the region has been given all along.
//
// The other end of the argument is what waiting costs.  A shell that
// inherited the ninety would sit silent for a minute and a half over an
// offer the grid was never going to answer, and the third failure --
// a request answered with nothing whatever -- is exactly the one a
// person meets when they accept a second lure while the first is still
// under way.
const shellTeleportTimeout = 30 * time.Second

// tpMiddle is where a teleport with no position lands.
//
// The middle of the region, which is 256 metres square, because that is
// where a viewer puts an avatar that typed a name into the world map and
// said nothing about where in it.
//
// The height is left at zero rather than guessed at, and zero is not
// arbitrary: measured on Agni, the avatar arrives at whichever is higher
// of the height asked for and the ground under the point, plus about a
// metre -- 30 came back as 31, 60 as 61, and 0 as the ground.  So zero
// is how a client asks for ground level without knowing where the ground
// is, and there is no cheap way to ask that about a region this session
// has never been to.
//
// What it does not promise is dry land.  A region's middle can be under
// water, and Sandbox Goguen's is, so this arrives there submerged.  That
// is the region rather than the default, and the man page says so.
var tpMiddle = msg.Vector3{X: 128, Y: 128}

// cmdTP moves the avatar: to a position in this region, or to another
// region by name.
//
// # Which of the two a line means
//
// Three numbers and nothing else is a position here, and it stays the
// cheap thing it has always been: no map lookup, no circuit moving under
// the session, nothing but the request and waiting for the position to
// agree.  Anything else is a region name, joined with spaces for the
// reason regions joins them -- a region name has spaces in it and
// quoting one at a prompt is a thing to have to remember -- and the last
// three words are the position when all three are numbers.
//
// So a region whose name ends in three numbers cannot be reached from
// here.  That is written down in the man page rather than defended
// against, because the defence is a quoting rule everybody would have to
// remember for a region nobody has met.
//
// Nothing but numbers, and not three of them, is refused rather than
// looked up.  "tp 128 128" is a position typed short, and asking the
// grid's map about a region called "128 128" would answer a question
// nobody asked and take a round trip over it.
//
// # Why the name is not narrowed to one region here
//
// The map's search is by prefix, so a name typed in full comes back
// beside every longer name beginning with it and several matches are the
// ordinary case.  See regionNamed: an exact name wins outright and
// anything else that matched twice is listed and refused, because
// choosing on somebody's behalf is how an avatar ends up in the wrong
// place.
//
// # Why an arrival is printed twice
//
// The notice from the shell's watcher says the avatar is in another
// region, and the line this prints says where it ended up.  Both are
// wanted and they say different things: one is the session's news, which
// arrives whoever asked for the move, and the other is this command's
// answer to the person who typed it.  Suppressing the notice for a
// change this command asked for would need state shared between the two,
// and would silently swallow a second change that arrived at the same
// moment.
func cmdTP(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o tpOptions
	rest, done, err := subOptions("tp", &o, out, endOptionsAtANegativeNumber(args))
	if err != nil || done {
		return err
	}
	if len(rest) == 0 {
		return usageError("tp", "a region to go to, or three numbers for a position in this one")
	}

	region, at, err := teleportTarget(rest)
	if err != nil {
		return err
	}
	// One deadline for both, because it is one question -- how long to
	// believe in an arrival -- and a flag that quietly did nothing to a
	// move inside the region would be worse than no flag.
	wait := time.Duration(o.Wait) * time.Second
	if wait == 0 {
		wait = shellTeleportTimeout
	}

	if region == "" {
		return sh.tpNearby(ctx, out, at, wait)
	}

	found, err := sh.regionNamed(ctx, out, region)
	if err != nil {
		return err
	}

	// Said before the waiting starts.  A teleport to another region takes
	// about four hundred milliseconds and has been measured at five
	// seconds, and a shell that has silently stopped answering is
	// indistinguishable from one that has hung.
	pos := msg.Vector3{X: at[0].v, Y: at[1].v, Z: at[2].v}
	fmt.Fprintf(out, "teleporting to %s at %.0f, %.0f, %.0f\n",
		found.Name, pos.X, pos.Y, pos.Z)
	if err := sh.s.Teleport(ctx, found.Handle, pos, wait); err != nil {
		return err
	}
	return sh.sayPosition(ctx, out)
}

// endOptionsAtANegativeNumber puts a "--" in front of the position,
// when the position has a negative number in it and nothing before it
// has ended the options already.
//
// Option parsing would otherwise eat one: "-10" is the option -1 with
// the value 0 as far as getopt is concerned, so "tp -10 128 25" answered
// "unknown option: -1" and a position west of this region's corner could
// not be typed at all.  A "--" says the rest are operands, which is what
// it means everywhere; putting it there rather than making somebody
// remember to is what keeps the obvious line working.
//
// In front of the LAST THREE arguments rather than the first negative
// one, because that is where a position is in every form tp takes, and
// because put ahead of the whole position it survives "tp --wait 60 -10
// 128 25", where the flag and its value are parsed before it.
//
// Only when nothing in front of the position is an operand, which is to
// say only when there is no region name -- the "tp X Y Z" form.  getopt
// stops reading options at the first operand, so a negative coordinate
// after a name was never at risk and needs no help; and a "--" put
// there is not an end-of-options mark at all, it is a word, so it was
// joined onto the name.  "tp Example Landing -10 128 25" was refused as
// a region called "Example Landing --" for as long as this inserted one
// whatever came before.
//
// Only when one of the three really begins with a minus, so that a
// mistyped option is still reported as one rather than handed on as a
// region called "-wiat".
func endOptionsAtANegativeNumber(args []string) []string {
	n := len(args)
	if n < 3 {
		return args
	}
	last := args[n-3:]
	if !allCoords(last) {
		return args
	}
	negative := false
	for _, s := range last {
		negative = negative || strings.HasPrefix(s, "-")
	}
	if !negative {
		return args
	}
	for _, s := range args[:n-3] {
		if s == "--" {
			return args // already said, and said first
		}
	}
	if sawOperand("tp", new(tpOptions), args[:n-3]) {
		return args // a region's name, where getopt has already stopped
	}

	out := make([]string, 0, n+1)
	out = append(out, args[:n-3]...)
	out = append(out, "--")
	return append(out, last...)
}

// tilde marks a coordinate given relative to where the avatar is
// standing rather than to the region's corner.
//
// "~" on its own is that axis left alone, "~10" is ten metres further
// along it and "~-10" ten metres back.  The character is borrowed from
// the game consoles that have wanted the same thing, and it is here
// because the obvious spelling is taken: a bare -10 already means ten
// metres west of THIS REGION's corner, which is a real place in the
// region next door, so a leading minus cannot also mean "ten back".
//
// It is per axis, which is the point of using a prefix rather than a
// flag: "tp ~10 ~ ~" is ten metres east of here at the same height and
// the same y, and "tp 128 128 ~" is the middle of the region without
// changing altitude.  A flag would make all three relative or none.
const tilde = "~"

// coord is one of tp's three numbers, which may be absolute or relative.
type coord struct {
	v   float32
	rel bool
}

// resolve is the coordinate as a position in the region, given where the
// avatar is standing now.
func (c coord) resolve(from float32) float32 {
	if c.rel {
		return from + c.v
	}
	return c.v
}

// parseCoord reads one of tp's numbers.  See tilde.
func parseCoord(s string) (coord, error) {
	s = strings.TrimSuffix(strings.TrimSpace(s), ",")
	if rest, ok := strings.CutPrefix(s, tilde); ok {
		if rest == "" {
			// "~" alone: this axis, unchanged.
			return coord{rel: true}, nil
		}
		f, err := strconv.ParseFloat(rest, 32)
		if err != nil {
			return coord{}, fmt.Errorf("%q is not a number of metres to move by", s)
		}
		return coord{v: float32(f), rel: true}, nil
	}
	f, err := strconv.ParseFloat(s, 32)
	if err != nil {
		return coord{}, fmt.Errorf("%q is not a number", s)
	}
	return coord{v: float32(f)}, nil
}

// isCoord is whether a word is one of tp's three numbers, which is what
// tells a position from the end of a region's name.
func isCoord(s string) bool {
	_, err := parseCoord(s)
	return err == nil
}

// coords reads three of them, and says whether any was relative.
func coords(args []string) (c [3]coord, rel bool, err error) {
	if len(args) != 3 {
		return c, false, fmt.Errorf("a position is three numbers: X Y Z")
	}
	for i, s := range args {
		if c[i], err = parseCoord(s); err != nil {
			return c, false, err
		}
		rel = rel || c[i].rel
	}
	return c, rel, nil
}

// tpNearby teleports to a position given in THIS region's metres, which
// may not be in this region.
//
// A region is 256 metres square and a position outside that is not an
// error: it is somewhere else, and which somewhere is arithmetic.  The
// grid is regions laid edge to edge, so 300 on the x axis is 44 metres
// into the region to the east and -10 is 246 metres into the one to the
// west, in the same way that the 25th hour of Monday is one in the
// morning on Tuesday.  Both spellings name one point and the shell can
// work out which region owns it, so it does, rather than making somebody
// look a handle up to say "just over there".
//
// It generalises as far as the arithmetic does.  There is nothing
// special about one region over: the position is turned into a place on
// the grid, the region containing that place is worked out, and what is
// left over is where in it.  1000 is three regions east and 232 metres
// in, and it costs exactly what 300 does.  A region nothing is standing
// on answers no_host, which is the grid's own way of saying there is
// nothing there and is reported as it arrives.
//
// The same point given from two different regions is the same
// teleport, which is the property that makes this worth having: a
// script that knows where something is in one region's metres can say
// so from anywhere nearby without knowing which region it is in.
func (sh *Shell) tpNearby(ctx context.Context, out io.Writer, at [3]coord, wait time.Duration) error {
	here, err := sh.s.Where(ctx)
	if err != nil {
		return err
	}
	if here.RegionHandle == 0 {
		return fmt.Errorf("this session does not know which region it is in yet")
	}

	// Anything relative is resolved against where the avatar is standing
	// NOW, before any of the grid arithmetic, so that "~300" is three
	// hundred metres from here and lands wherever that is rather than
	// meaning something different depending on which region it is.
	asked := msg.Vector3{
		X: at[0].resolve(here.Position.X),
		Y: at[1].resolve(here.Position.Y),
		Z: at[2].resolve(here.Position.Z),
	}

	handle, local, err := gridPoint(here.RegionHandle, asked)
	if err != nil {
		return err
	}

	// Still this region, which is the ordinary case and stays the cheap
	// one: no handle, no waiting for a region to change, nothing moved
	// but the avatar.
	if handle == here.RegionHandle {
		if err := sh.s.TeleportLocal(ctx, local, wait); err != nil {
			return err
		}
		return sh.sayPosition(ctx, out)
	}

	// Named by its square rather than by its name, because there is no
	// name to hand: this went from a position to a region without ever
	// asking the map about one, which is the whole saving.  The line
	// after the arrival says what the place is called.
	x, y := msg.GridCoords(handle)
	fmt.Fprintf(out, "teleporting to grid square (%d, %d) at %.0f, %.0f, %.0f\n",
		x, y, local.X, local.Y, local.Z)
	if err := sh.s.Teleport(ctx, handle, local, wait); err != nil {
		return err
	}
	return sh.sayPosition(ctx, out)
}

// gridPoint turns a position in one region's metres into the region that
// really owns it and the position inside that one.
//
// The two steps are worth naming.  A place on the grid is the region's
// own corner plus the offset -- that is what makes the axes continuous
// across a border -- and the region containing it is that divided by
// 256, rounded DOWN.  Rounding down rather than towards zero is the
// whole of the negative case: -10 is in the region to the west at 246,
// where truncation would put it in this one at -10 again and change
// nothing.
//
// Height is not touched.  Regions are stacked edge to edge and not on
// top of one another, so z means the same thing on both sides of a
// border, and 2001 metres up is 2001 metres up wherever the avatar
// stands.
func gridPoint(from uint64, at msg.Vector3) (handle uint64, local msg.Vector3, err error) {
	const width = 256.0

	fx, fy := msg.GridCoords(from)
	// In metres from the grid's own corner, which is where the axes are
	// continuous and the arithmetic is ordinary.
	wx := float64(fx)*width + float64(at.X)
	wy := float64(fy)*width + float64(at.Y)

	// Off the grid entirely, which is not a region that could answer
	// no_host: there is no square west of the first one, and a handle
	// built from a negative coordinate would wrap into somewhere real
	// and teleport the avatar to a place nobody asked for.
	if wx < 0 || wy < 0 {
		return 0, msg.Vector3{}, fmt.Errorf(
			"%.0f, %.0f is off the edge of the grid: the region this avatar is in "+
				"is square (%d, %d), so x may go down to %.0f and y to %.0f",
			at.X, at.Y, fx, fy, -float64(fx)*width, -float64(fy)*width)
	}

	gx, gy := math.Floor(wx/width), math.Floor(wy/width)
	if gx > math.MaxUint32/width || gy > math.MaxUint32/width {
		return 0, msg.Vector3{}, fmt.Errorf("%.0f, %.0f is further than the grid goes", at.X, at.Y)
	}

	return msg.RegionHandle(uint32(gx), uint32(gy)), msg.Vector3{
		X: float32(wx - gx*width),
		Y: float32(wy - gy*width),
		Z: at.Z,
	}, nil
}

// teleportTarget reads what tp was asked for: the region to go to, or ""
// for a move inside this one, and where to arrive.  See cmdTP for the
// rule and for what it costs.
func teleportTarget(args []string) (region string, at [3]coord, err error) {
	if allCoords(args) {
		if len(args) != 3 {
			return "", at, usageError("tp",
				"a position is three numbers; a region is a name")
		}
		at, _, err = coords(args)
		return "", at, err
	}

	at = absolute(tpMiddle)
	if n := len(args); n > 3 && allCoords(args[n-3:]) {
		var rel bool
		if at, rel, err = coords(args[n-3:]); err != nil {
			return "", at, err
		}
		if rel {
			// "~" is "from where the avatar is standing", and the
			// avatar is not standing in the region just named.  There
			// is no honest answer -- a metre east of here is not a
			// place in somewhere else -- so this is refused rather
			// than resolved against a position in the wrong region.
			return "", at, usageError("tp",
				"~ is a position relative to where the avatar is standing, "+
					"so it cannot be used with the name of another region")
		}
		args = args[:n-3]
	}
	return strings.Join(args, " "), at, nil
}

// absolute is a plain position as three coordinates, for the paths that
// have one already.
func absolute(v msg.Vector3) [3]coord {
	return [3]coord{{v: v.X}, {v: v.Y}, {v: v.Z}}
}

// allCoords is whether every word is one of tp's numbers, which is what
// tells a position from the end of a region's name.
//
// It parses the same way position does, trailing comma and all, so that
// a position copied off the screen is read as one here too rather than
// being sent to the map as a name -- and it takes the ~ forms as well,
// so that "tp ~10 ~ ~" is a position and not a region nobody has heard
// of.
func allCoords(args []string) bool {
	for _, s := range args {
		if !isCoord(s) {
			return false
		}
	}
	return len(args) > 0
}

// regionNamed is the one region a name means, or a listing and a
// refusal.
//
// The map's search is by prefix and ignores case, so a name typed in
// full comes back beside every longer name that begins with it: "Pelmar
// Reach" and "Pelmar Reach Annexe" are one answer to one question.  An exact
// name is therefore taken outright, which is what whoOrSearch does with
// a person's and for the same reason -- a name typed in full is not an
// ambiguous name.  One row and no exact match is taken as well, since
// there is nothing to choose between.
//
// Anything else is printed the way regions prints it and refused.  This
// command moves an avatar, and a wrong guess here is not a listing to
// read again: it is an avatar somewhere else, with everything the shell
// knew about the region it left thrown away on the way.
func (sh *Shell) regionNamed(ctx context.Context, out io.Writer, name string) (*sl.MapRegion, error) {
	// The map's own deadline rather than --wait, which is the teleport's
	// budget: this is regions' question, asked before the teleport is,
	// and it answers in about a tenth of a second or not at all.
	found, err := sh.s.FindRegions(ctx, name, 0)
	if err != nil {
		return nil, err
	}

	var exact []sl.MapRegion
	for _, r := range found {
		if strings.EqualFold(r.Name, name) {
			exact = append(exact, r)
		}
	}
	switch {
	case len(exact) == 1:
		return &exact[0], nil
	case len(exact) == 0 && len(found) == 1:
		return &found[0], nil
	case len(exact) > 1:
		// Two regions may share a name.  The handles differ and nothing
		// else does, so there is no more of the name to type and this is
		// as far as a name can be taken.
		printRegions(out, exact)
		return nil, fmt.Errorf("%d regions on this grid are called %q, and nothing "+
			"here can tell which was meant", len(exact), name)
	}
	printRegions(out, found)
	return nil, fmt.Errorf("%d regions begin with %q; teleporting to one of them "+
		"needs its name in full", len(found), name)
}

// sayPosition prints where the avatar is now, read back rather than
// assumed: the simulator stands it on whatever is under the point asked
// for, so the position to believe is the one it reports afterwards.
func (sh *Shell) sayPosition(ctx context.Context, out io.Writer) error {
	p, err := sh.s.Where(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, positionLine(p))
	return nil
}

// positionLine is where the avatar is, in the one wording where, tp and
// an accepted lure all say it in.  A region and a position to the metre:
// see man/where.md for why no more than that is honest.
func positionLine(p *sl.Presence) string {
	return fmt.Sprintf("%s at %.0f, %.0f, %.0f",
		p.Region, p.Position.X, p.Position.Y, p.Position.Z)
}

// cmdMove moves a rezzed object.
//
// It exists because taking an object and rezzing it again does not put
// it back: a rez happens where you ask, and "where it was" is not
// something Second Life remembers for you.  Anything that takes an
// object as part of a round trip has to note where it stood and put it
// back itself.
//
// This was "place" until the word was wanted for putting an inventory
// object into the world, which is what "place" sounds like it means.
// "move" says what this one does without any of that argument, since
// the thing is already in the world and all that changes is where.
func cmdMove(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var flags helpOnly
	rest, done, err := subOptions("move", &flags, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 4 {
		return usageError("move", "an object in this region, and where to put it")
	}

	at, err := position(rest[1:])
	if err != nil {
		return err
	}

	found, err := sh.s.ObjectsNamed(ctx, rest[0], 60*time.Second)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return fmt.Errorf("no object named %q in this region", rest[0])
	}
	if len(found) > 1 {
		return fmt.Errorf("%d objects are called %q; rename one, or move it by hand", len(found), rest[0])
	}
	o := found[0]

	// Its own rotation and scale.  sl.Place sets all three at once, so
	// inventing the other two would quietly reshape whatever it was
	// pointed at.
	if err := sh.s.Place(ctx, &o.Object, at, o.Rotation, o.Scale); err != nil {
		return err
	}

	// Wait for it to have moved, rather than read it back once.
	//
	// sl.Place is fire and forget -- the simulator answers with an
	// ObjectUpdate whenever it gets round to it -- so an immediate
	// re-read returns the position the object had BEFORE the move and
	// reports it with total confidence.  Observed: "place-probe is at
	// 33.0, 73.0, 1000.2" for an object that was by then at 36, 78,
	// 1002.  A stale answer is worse than none, because nothing about
	// it looks wrong.
	deadline := time.Now().Add(20 * time.Second)
	for {
		again, err := sh.s.ObjectByID(ctx, o.ID, 20*time.Second)
		if err != nil {
			return err
		}
		if near(again.Position, at) {
			fmt.Fprintf(out, "%s is at %.1f, %.1f, %.1f\n",
				again.Name, again.Position.X, again.Position.Y, again.Position.Z)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not move; it is still at %.1f, %.1f, %.1f\n"+
				"        (a parcel that will not have objects moved refuses silently)",
				again.Name, again.Position.X, again.Position.Y, again.Position.Z)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// cmdAgents lists what the daemon is holding.
//
// In the daemon's order, which is not alphabetical: oldest first, and
// the first session it still holds is what a command that names no
// agent gets.  That is what the order says.
//
// It is NOT the order objects are found in.  A run asks the daemon's
// pool for a number and the pool answers out of every avatar at once;
// there is no walk from one to the next for it to be the order of.
func cmdAgents(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	_, done, err := subOptions("agents", &o, out, args)
	if err != nil || done {
		return err
	}

	conn, ok := sh.conn()
	if !ok {
		fmt.Fprintf(out, "%s (logged in directly, not through a daemon)\n", sh.s.Info().AvatarName)
		return nil
	}
	agents, err := conn.ListAgents(ctx)
	if err != nil {
		return err
	}

	here := sh.s.Info().Name
	first := true
	for _, a := range agents {
		// The mark is the daemon's first choice, and it has to be the
		// daemon's rule for that and not one that looks like it.
		mark := " "
		if first && canBeTheDefault(a.GetState()) {
			mark, first = "*", false
		}
		note := ""
		if a.GetName() == here {
			note = "   <- this shell"
		}

		where := a.GetRegion()
		if a.GetState() != pb.AgentInfo_HOSTED {
			where = strings.ToLower(a.GetState().String())
			if d := a.GetDetail(); d != "" {
				where += ": " + d
			}
		}
		fmt.Fprintf(out, "%s %-10s  %-24s  %s%s\n",
			mark, a.GetName(), a.GetAvatarName(), where, note)
	}
	return nil
}

// canBeTheDefault says whether a listed agent is one a command that
// names no avatar could be given, which is the whole of what the star
// means.
//
// The state is all the listing has to go on and it is enough, because
// the daemon's rule (Server.defaultLocked) is "the first session I hold
// that was not deliberately stopped" and the two halves of that are
// both readable here: the listing arrives in the daemon's own order,
// held sessions first and stopped ones at the end of that group, and
// only a held session is ever HOSTED or CONNECTING.  CONFIGURED and
// FAILED are profiles the daemon holds no session for at all, so they
// can no more be the default than a name it has never heard of.
//
// CONNECTING is the one that matters and it was the bug: a circuit that
// dropped and is being rebuilt never costs a session its place, so the
// daemon still answers with it, while a star drawn on the first HOSTED
// row moved to the second session for as long as the reconnection took
// -- the listing and the daemon disagreeing exactly when somebody is
// reading the listing to find out what is going on.
func canBeTheDefault(state pb.AgentInfo_State) bool {
	return state == pb.AgentInfo_HOSTED || state == pb.AgentInfo_CONNECTING
}

// cmdLogin brings an avatar up.
//
// It has to be named.  Logging an avatar in puts it in the world -- an
// arrival, a presence, a notice to whoever watches for it -- so it
// follows from somebody asking rather than from a default.
//
// This was "host" until the word was measured against what a person
// asking for it has in mind.  Hosting is what the daemon does with a
// session once it exists, which is slgod's half of the arrangement and
// not a thing anybody types at a prompt; what the person wants is the
// avatar logged in, and logout was already the word for the other
// direction.  Nothing answers to "host" now: the pair reads login and
// logout, and a half-renamed pair would be worse than either.
func cmdLogin(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o forceOptions
	rest, done, err := subOptions("login", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 1 {
		return usageError("login", "which avatar to log in; flags come before the name")
	}
	conn, ok := sh.conn()
	if !ok {
		return fmt.Errorf("this session was logged in directly; there is no daemon to ask")
	}

	r, err := conn.Host(ctx, rest[0], o.Force)
	if err != nil {
		return err
	}
	if r.GetAlready() {
		fmt.Fprintf(out, "%s was already up: %s in %s\n",
			rest[0], r.Agent.GetAvatarName(), r.Agent.GetRegion())
		return nil
	}
	fmt.Fprintf(out, "%s: %s in %s\n", rest[0], r.Agent.GetAvatarName(), r.Agent.GetRegion())
	return nil
}

// cmdLogout logs one out and keeps it out until login asks for it.
func cmdLogout(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o forceOptions
	rest, done, err := subOptions("logout", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 1 {
		return usageError("logout", "which avatar to log out; flags come before the name")
	}
	conn, ok := sh.conn()
	if !ok {
		return fmt.Errorf("this session was logged in directly; quitting logs it out")
	}

	r, err := conn.Logout(ctx, rest[0], o.Force)
	if err != nil {
		if cs := r.GetClients(); len(cs) > 0 {
			return fmt.Errorf("%w\n        attached: %s", err, strings.Join(cs, ", "))
		}
		return err
	}
	fmt.Fprintf(out, "%s logged out; it will not come back until \"login %s\" asks for it\n", rest[0], rest[0])
	return nil
}

// forceOptions is for the two commands that overrule something
// deliberate.
type forceOptions struct {
	Force bool `getopt:"--force -f  overrule: start one that was stopped, or log out one in use"`
	Help  bool `getopt:"--help -h   show what this command takes"`
}

// conn is the daemon connection behind this session, when there is one.
func (sh *Shell) conn() (*client.Conn, bool) {
	type conner interface{ Conn() *client.Conn }
	b, ok := sh.s.Backend().(conner)
	if !ok {
		return nil, false
	}
	return b.Conn(), true
}

// autoOptions is what auto was asked for.
type autoOptions struct {
	N    int  `getopt:"-n=COUNT   set up this many, rather than only reporting"`
	Help bool `getopt:"--help -h  show what this command takes"`
}

// cmdAuto reports or sets up the objects benchmarks run in.
func cmdAuto(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o autoOptions
	_, done, err := subOptions("auto", &o, out, args)
	if err != nil || done {
		return err
	}

	if o.N > 0 {
		start := time.Now()
		objs, err := session.SetupAuto(ctx, sh.s, o.N)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%d of %d ready in %v\n",
			len(objs), o.N, time.Since(start).Round(time.Second))
		// Which slot went where.  Past the eighth two objects share a
		// point, so the listing is the only way to see that the pool
		// is arranged the way it was meant to be.
		for i, obj := range objs {
			fmt.Fprintf(out, "  %2d  %-10s %s  on %s\n",
				i, session.AutoName(i), obj.ID, sl.AttachPointName(session.AutoPoints[i]))
		}
		if len(objs) < o.N {
			fmt.Fprintf(out, "\nonly %d of %d: fewer objects means less can run at once, "+
				"not that anything is broken\n", len(objs), o.N)
		}
		return nil
	}

	worn, err := sh.s.WornObjects(ctx)
	if err != nil {
		return err
	}
	names := sh.itemNames(ctx)
	have := 0
	for _, a := range worn {
		if strings.HasPrefix(names[a.Item], session.AutoObject) {
			have++
		}
	}
	// What is worn is what can run at once, one object per script, and
	// that is the whole of what this avatar's share buys.  It used to
	// offer a count of benchmarks alongside, worked out from
	// session.AutoGroupSize, and that number was wrong: a benchmark
	// leases one object per division of each of its searches and three
	// besides, which is nineteen at slbench's defaults and moves with
	// --parts and --extra, and it halves that again when the pool
	// cannot grant it.  Only slbench can say it, and it says it when it
	// settles for less.  A figure printed here could only go stale
	// again, which is worse than not printing one.
	fmt.Fprintf(out, "%d auto objects worn, so %d scripts at once\n", have, have)
	if have < len(session.AutoPoints) {
		fmt.Fprintf(out, "auto -n %d sets up the rest\n", len(session.AutoPoints))
	}
	return nil
}

// near is whether an object has arrived where it was sent.  Loose
// enough for the rounding a position makes on its way through a
// float32 and back, tight enough that a metre's difference is not
// "arrived".
func near(a, b msg.Vector3) bool {
	const tol = 0.25
	d := func(x, y float32) float32 {
		if x > y {
			return x - y
		}
		return y - x
	}
	return d(a.X, b.X) < tol && d(a.Y, b.Y) < tol && d(a.Z, b.Z) < tol
}

// position reads three numbers as a place in the region.
func position(args []string) (msg.Vector3, error) {
	var v msg.Vector3
	if len(args) != 3 {
		return v, fmt.Errorf("a position is three numbers: X Y Z")
	}
	into := []*float32{&v.X, &v.Y, &v.Z}
	for i, s := range args {
		f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), ","), 32)
		if err != nil {
			return msg.Vector3{}, fmt.Errorf("%q is not a number", s)
		}
		*into[i] = float32(f)
	}
	return v, nil
}
