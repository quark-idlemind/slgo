package main

// Walking, turning and stopping, by hand.
//
// These are the shell's way into sl.Session.Move, Face and Halt, which
// are there for programs that drive an avatar round a region; the shell
// has them so that a person can see what a walk does before writing one
// of those, and so that an avatar can be put a few metres to one side
// without a teleport.
//
// A walk is steered by whoever holds the circuit -- slgod, for a shell
// attached to one -- and the shell only waits for it and says how it
// went.  Interrupting the command ends the walk and stops the avatar,
// because a walk's stream ending is what stops it; see the Move rpc in
// proto/slgo.proto.
//
// The walk is a straight line and walks round nothing.  That is the
// daemon's decision and not a shortcoming of the command: what is in the
// way is for whoever is steering to go round, and here that is the
// person typing.

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var walkCommands = map[string]*command{
	"walk": {
		params:   "X Y [Z] | NAME",
		flags:    func() any { return new(walkFlags) },
		brief:    "walk the avatar to a place in this region, or up to somebody or something",
		keywords: "walk go move run step over there approach come closer goto head towards stroll",
		man:      "walk",
		run:      cmdWalk,
	},
	"face": {
		params:   "X Y [Z] | NAME",
		flags:    func() any { return new(faceFlags) },
		brief:    "turn the avatar toward a place, somebody or something, without moving",
		keywords: "face turn look towards rotate heading direction point facing",
		man:      "face",
		run:      cmdFace,
	},
	"halt": {
		flags:    func() any { return new(helpOnly) },
		brief:    "stop the avatar where it is, ending any walk",
		keywords: "stop walking halt freeze stand still cancel walk abort",
		man:      "halt",
		run:      cmdHalt,
	},
}

// walkFlags is what walk was asked for.
type walkFlags struct {
	Within   float64       `getopt:"--within=METRES -w  how close is close enough [0.5; 1.5 for a name]"`
	Run      bool          `getopt:"--run -r            run rather than walk"`
	Timeout  time.Duration `getopt:"--timeout=D -t      give up after this long; default no limit"`
	Progress bool          `getopt:"--progress -p       print how it is going four times a second"`
	Help     bool          `getopt:"--help -h           show what this command takes"`
}

// faceFlags is what face was asked for.
type faceFlags struct {
	Yaw  string `getopt:"--yaw=DEGREES -y  a heading instead: anticlockwise from east, so 90 is north"`
	Help bool   `getopt:"--help -h         show what this command takes"`
}

// nameWithin is how close a walk to somebody or something gets unless
// told otherwise.  A person or an object is where its middle is, and
// half a metre from somebody's middle is walking into them.
const nameWithin = 1.5

// cmdWalk walks the avatar and waits for it to get there.
//
// The end is printed with the position to the centimetre rather than in
// the whole metres where prints, because how close a walk got is the
// question it answers; the position is the simulator's own figure for
// this avatar and is good to that.  An end that is not an arrival is
// the command's failure, so that a script can tell.
func cmdWalk(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o walkFlags
	rest, done, err := subOptions("walk", &o, out, args)
	if err != nil || done {
		return err
	}
	target, what, err := sh.walkTarget(ctx, "walk", rest)
	if err != nil {
		return err
	}
	within := float32(o.Within)
	if what != "" && o.Within == 0 {
		within = nameWithin
	}

	r := sl.MoveRequest{Target: target, Within: within, Run: o.Run, Timeout: o.Timeout}
	var progress func(sl.MoveProgress)
	if o.Progress {
		progress = func(p sl.MoveProgress) { fmt.Fprintln(out, progressLine(p)) }
	}
	end, err := sh.s.Move(ctx, r, progress)
	if err != nil {
		return err
	}
	if what == "" {
		what = fmt.Sprintf("%.2f, %.2f", target.X, target.Y)
	}
	line := endLine(end, what)
	if end.State != sl.Arrived {
		return fmt.Errorf("%s", line)
	}
	fmt.Fprintln(out, line)
	return nil
}

// progressLine is one report: when, where, how fast, which way, how far.
func progressLine(p sl.MoveProgress) string {
	speed := math.Hypot(float64(p.Velocity.X), float64(p.Velocity.Y))
	return fmt.Sprintf("%6.2fs  %.2f, %.2f, %.2f  %.2f m/s  facing %s  %.2f m to go",
		p.Elapsed.Seconds(), p.Position.X, p.Position.Y, p.Position.Z,
		speed, heading(p.Yaw), p.Remaining)
}

// endLine is how a walk ended, in one line.
func endLine(p sl.MoveProgress, what string) string {
	at := fmt.Sprintf("%.2f, %.2f, %.2f", p.Position.X, p.Position.Y, p.Position.Z)
	took := p.Elapsed.Round(100 * time.Millisecond)
	switch p.State {
	case sl.Arrived:
		return fmt.Sprintf("arrived at %s, %.2f m from %s, in %s", at, p.Remaining, what, took)
	case sl.Blocked:
		return fmt.Sprintf("blocked at %s, %.2f m short of %s: no closer for %s",
			at, p.Remaining, what, p.SinceProgress.Round(100*time.Millisecond))
	case sl.Cancelled:
		return fmt.Sprintf("cancelled (%s) at %s, %.2f m from %s, after %s", p.Reason, at, p.Remaining, what, took)
	case sl.OutOfRegion, sl.Refused:
		return p.Reason
	}
	return fmt.Sprintf("%s at %s", p.State, at)
}

// heading is a yaw as a person reads one: degrees anticlockwise from
// east, and the nearest compass point.
func heading(yaw float32) string {
	deg := float64(yaw) * 180 / math.Pi
	if deg < 0 {
		deg += 360
	}
	points := []string{"E", "NE", "N", "NW", "W", "SW", "S", "SE"}
	return fmt.Sprintf("%.0f° %s", deg, points[int(math.Round(deg/45))%8])
}

// cmdFace turns the avatar.
func cmdFace(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o faceFlags
	rest, done, err := subOptions("face", &o, out, args)
	if err != nil || done {
		return err
	}
	var yaw float32
	switch {
	case o.Yaw != "" && len(rest) != 0:
		return usageError("face", "a place, somebody or something, or --yaw, and not both")
	case o.Yaw != "":
		deg, err := strconv.ParseFloat(o.Yaw, 64)
		if err != nil {
			return fmt.Errorf("--yaw %q is not a number of degrees", o.Yaw)
		}
		yaw = float32(math.Remainder(deg, 360) * math.Pi / 180)
		if err := sh.s.FaceYaw(ctx, yaw); err != nil {
			return err
		}
	case len(rest) == 0:
		return usageError("face", "a place, somebody or something to face, or --yaw")
	default:
		target, _, err := sh.walkTarget(ctx, "face", rest)
		if err != nil {
			return err
		}
		if yaw, err = sh.s.Face(ctx, target); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "facing %s\n", heading(yaw))
	return nil
}

// cmdHalt stops the avatar.
func cmdHalt(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	rest, done, err := subOptions("halt", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 0 {
		return usageError("halt", "halt takes nothing")
	}
	walking, err := sh.s.Halt(ctx)
	if err != nil {
		return err
	}
	if walking {
		fmt.Fprintln(out, "halted a walk")
	} else {
		fmt.Fprintln(out, "nothing was walking; sent a stop anyway")
	}
	return nil
}

// walkTarget reads where walk and face were pointed: two or three
// numbers, or one word naming somebody nearby or an object in the
// region.  What comes back beside the place is the name, for a target
// that had one.
//
// People are tried first, by the start of any part of their name, the
// way who and im take them; an object is taken by the whole of its
// name, the way touch and sit take one.  A word that fits two people,
// or two objects, is refused rather than guessed at -- walking to the
// wrong one is a thing somebody watching then has to explain.
func (sh *Shell) walkTarget(ctx context.Context, name string, rest []string) (msg.Vector3, string, error) {
	switch len(rest) {
	case 2, 3:
		var v msg.Vector3
		into := []*float32{&v.X, &v.Y, &v.Z}
		for i, s := range rest {
			f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), ","), 32)
			if err != nil {
				return v, "", fmt.Errorf("%q is not a number", s)
			}
			*into[i] = float32(f)
		}
		return v, "", nil
	case 1:
	default:
		return msg.Vector3{}, "", usageError(name, "a place is X Y, or X Y Z; a name with a space in it is one argument")
	}

	want := strings.ToLower(rest[0])
	people, err := sh.s.Nearby(ctx)
	if err != nil {
		return msg.Vector3{}, "", err
	}
	var found []sl.Person
	for _, p := range people {
		if nameStarts(strings.ToLower(p.Name), want) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 1:
		if found[0].Distance < 0 {
			return msg.Vector3{}, "", fmt.Errorf("%s is sitting on something this session has not been shown, so where is not known", found[0].Name)
		}
		return found[0].Position, found[0].Name, nil
	case 0:
	default:
		names := make([]string, len(found))
		for i, p := range found {
			names[i] = p.Name
		}
		return msg.Vector3{}, "", fmt.Errorf("%q is %d people here: %s", rest[0], len(found), strings.Join(names, ", "))
	}

	objects, err := sh.s.ObjectsNamed(ctx, rest[0], 5*time.Second)
	if err != nil {
		return msg.Vector3{}, "", err
	}
	var roots []*sl.Seen
	for _, o := range objects {
		if o.IsRoot() {
			roots = append(roots, o)
		}
	}
	switch len(roots) {
	case 1:
		return roots[0].Position, roots[0].Name, nil
	case 0:
		return msg.Vector3{}, "", fmt.Errorf("nobody and nothing here is called %q", rest[0])
	}
	return msg.Vector3{}, "", fmt.Errorf("%d objects here are called %q; give a place instead", len(roots), rest[0])
}

// nameStarts says whether a name, or any word of it, begins with what
// was typed.
func nameStarts(name, typed string) bool {
	if strings.HasPrefix(name, typed) {
		return true
	}
	for _, w := range strings.Fields(name) {
		if strings.HasPrefix(w, typed) {
			return true
		}
	}
	return false
}
