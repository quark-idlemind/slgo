package main

// Sitting down, and getting up again.
//
// A sit is not a walk: the simulator picks the avatar up and puts it on
// the seat, over whatever is in the way, from as much as ten metres
// off, and standing up does not put it back.  So what these two print
// is not only what they did but where that left the avatar, in the same
// words where and tp say it.
// Why: doc/slsh.md#where-a-sit-leaves-the-avatar
//
// Two commands, three mechanisms.  Sitting on an object is a message,
// AgentRequestSit, answered by this avatar's own object update coming
// back parented to the seat.  Sitting on the ground is a control flag
// and is answered by nothing at all except the animation list.
// Standing is another control flag.  The argument is the only thing
// that tells the first two apart, which is why a bare "sit" means the
// ground rather than being a convenience: they share nothing on the
// wire, and there is nothing else to choose between them with.
//
// See sl/sit.go, which holds the measurements and the three outcomes a
// sit has, and doc/history/sit.md, where they were written down as they
// were made.

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// stand is held in a variable so that two names can share it.  See the
// table below for why the second name is there.
var stand = &command{
	flags:    func() any { return new(postureFlags) },
	brief:    "get up, from either kind of sit",
	keywords: "stand up get up unsit leave seat",
	man:      "stand",
	run:      cmdStand,
}

var postureCommands = map[string]*command{
	"sit": {
		params:   "[NAME|UUID]",
		flags:    func() any { return new(postureFlags) },
		brief:    "sit on an object; with nothing named, sit on the ground",
		keywords: "sit down seat chair ground pose furniture",
		man:      "sit",
		run:      cmdSit,
	},
	// "stand" is the word the world uses -- it is what the button says
	// and what somebody types without thinking -- and "unsit" is what a
	// script calls the function.  The second spelling costs nothing
	// here, because the table is keyed by name and two names may share
	// one command, exactly as quit and exit do; what it buys is that
	// somebody who has been writing LSL all afternoon is not told
	// "no such command" for the word that has been on their screen.
	"stand": stand,
	"unsit": stand,
}

// postureFlags is what sit and stand were asked for.
//
// One struct for both, since they ask the same question of the
// simulator and wait for it the same way.  The wait covers the answer
// and not the name lookup in front of it: that is the region
// describing itself, which gets thirty seconds and no flag, as it does
// for touch.
type postureFlags struct {
	Wait int  `getopt:"--wait -w=SECONDS  how long to wait for the simulator to answer [15]"`
	Help bool `getopt:"--help -h          show what this command takes"`
}

// cmdSit sits the avatar on something, or on the ground.
//
// # What it prints, and why the position is half of it
//
// A sit moves the avatar, so a shell that reported only what it sat on
// would leave somebody holding a position that is now several metres
// wrong.  The position is read back rather than worked out -- the
// simulator decides where the seat is -- and it is printed by the same
// helper tp and where use, so that "where" run afterwards agrees with
// what this said instead of merely being close to it.
//
// It is printed for a ground sit too, where nothing moved.  The
// command has one shape of answer whichever mechanism it used, and a
// line saying the avatar is exactly where it was is the confirmation
// of that rather than noise.
//
// # What it does not do
//
// Nothing is translated.  A refusal comes back in the grid's own words
// -- "No room to sit here, try another spot." for something too far
// off -- and those words go to the screen as they arrived, because
// they are not always accurate and they are always the only thing
// somebody can act on: an id that is no object at all is refused with
// "it is not in the same region as you", and a shell that turned that
// into "no such object here" would have thrown away the detail.  A
// timeout is a third thing and says so; see sl.Sit.
func cmdSit(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o postureFlags
	rest, done, err := subOptions("sit", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) > 1 {
		return usageError("sit", "one object to sit on, or nothing at all for the ground; "+
			"a name with a space in it is one argument")
	}
	wait := time.Duration(o.Wait) * time.Second

	if len(rest) == 0 {
		if err := sh.s.SitOnGround(ctx, wait); err != nil {
			return err
		}
		fmt.Fprintln(out, "sat on the ground")
		return sh.sayPosition(ctx, out)
	}

	// Resolved by objectNamed, which take and dump share and whose
	// rules touch's seenNamed follows, rather than by a resolver of its
	// own: a uuid is taken as itself, a name is looked up among what the
	// region has described, and a word that names two things is refused
	// with both ids rather than guessed at.  Guessing is worse here than
	// it is for touch, since the wrong guess moves the avatar.
	//
	// The zero is that resolver's own default of thirty seconds for the
	// region to describe itself, which is a different question from
	// --wait and is not worth a second flag.
	target, err := sh.objectNamed(ctx, rest[0], 0)
	if err != nil {
		// A uuid needs no listing.  AgentRequestSit carries the id and
		// the SIMULATOR resolves it; what the listing is for here is
		// the local id, which sl.Sit uses for one thing only -- seeing
		// that the avatar is already on that very object -- and can do
		// without.  The listing is not complete, so an id nothing has
		// described is sat on all the same.
		// Why: doc/slsh.md#sitting-on-an-object-nothing-has-described
		id, bad := msg.ParseUUID(rest[0])
		if bad != nil {
			return err
		}
		target = &sl.Object{ID: id}
	}

	seat, err := sh.s.Sit(ctx, target, wait)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "sat on %s\n", seat)
	return sh.sayPosition(ctx, out)
}

// cmdStand gets the avatar up.
//
// It takes nothing, because there is nothing to name: an avatar sits
// on one thing at a time and the simulator knows which, so the only
// question a person could answer here is one they should not have to.
//
// The position is printed for the same reason sit prints it: standing
// does not undo the journey the sit made, so this line is where the
// avatar now is and not where it was before any of this began.
// Why: doc/slsh.md#where-a-sit-leaves-the-avatar
//
// It may take a moment.  A stand is a control flag, and a flag that
// arrives while the avatar is still settling into the last thing it
// was told to do is dropped without a word, so sl holds the flag the
// way a viewer holds the key rather than sending it once; see
// sl.controlUntil for the sequence that measured it.
func cmdStand(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o postureFlags
	rest, done, err := subOptions(typedAs(ctx, "stand"), &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 0 {
		return usageError("stand", "stand takes nothing; the simulator knows what this avatar is on")
	}

	if err := sh.s.Stand(ctx, time.Duration(o.Wait)*time.Second); err != nil {
		return err
	}
	fmt.Fprintln(out, "stood up")
	return sh.sayPosition(ctx, out)
}
