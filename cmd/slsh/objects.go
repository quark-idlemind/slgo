package main

// Moving things and moving about: giving inventory away, taking what is
// offered, duplicating an item, going somewhere, and the sessions the
// daemon holds.

import (
	"context"
	"fmt"
	"io"
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
		usage: "give WHO PATH",
		brief: "offer an inventory item to somebody",
		run:   cmdGive,
	},
	"cp": {
		usage: "cp PATH NAME",
		brief: "copy an inventory item under a new name",
		run:   cmdCopy,
	},
	"tp": {
		usage: "tp X Y Z",
		brief: "move to a position in this region",
		run:   cmdTP,
	},
	"place": {
		usage: "place NAME X Y Z",
		brief: "move a rezzed object to a position",
		run:   cmdPlace,
	},
	"agents": {
		usage: "agents",
		brief: "the sessions this daemon holds, oldest first; * is the default",
		run:   cmdAgents,
	},
	"host": {
		usage: "host [-f] NAME",
		brief: "bring an avatar up that is not running",
		run:   cmdHost,
	},
	"logout": {
		usage: "logout [-f] NAME",
		brief: "log an avatar out and keep it out until asked for by name",
		run:   cmdLogout,
	},
	"auto": {
		usage: "auto [-n N]",
		brief: "the objects benchmarks run in; -n sets up that many",
		run:   cmdAuto,
	},
}

// cmdGive offers an item to somebody.
//
// An offer, not a transfer: nothing moves until they accept, and
// nothing here can tell whether they did.  Saying "offered" rather than
// "gave" is the honest report.
func cmdGive(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	rest, done, err := subOptions("give", "WHO PATH", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("give WHO PATH")
	}

	who, name, err := sh.who(ctx, rest[0])
	if err != nil {
		return err
	}

	e, err := sh.entryAt(ctx, strings.Join(rest[1:], " "))
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
	rest, done, err := subOptions("cp", "PATH NAME", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("cp PATH NAME")
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

// cmdTP moves within the region.
//
// Only within: another region means another simulator, a new circuit
// and a new set of capabilities, which is the daemon's work and is not
// built.  Saying so is better than a command that silently does nothing
// when given a region name.
func cmdTP(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	rest, done, err := subOptions("tp", "X Y Z", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 3 {
		return fmt.Errorf("tp X Y Z -- a position in this region.\n" +
			"Another region needs a new circuit and new capabilities, which is\n" +
			"slgod's to do and is not built; log in there instead (slgod -start).")
	}

	v, err := position(rest)
	if err != nil {
		return err
	}

	if err := sh.s.TeleportLocal(ctx, v, 30*time.Second); err != nil {
		return err
	}
	p, err := sh.s.Where(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s at %.0f, %.0f, %.0f\n", p.Region, p.Position.X, p.Position.Y, p.Position.Z)
	return nil
}

// cmdPlace moves a rezzed object.
//
// It exists because taking an object and rezzing it again does not put
// it back: a rez happens where you ask, and "where it was" is not
// something Second Life remembers for you.  Anything that takes an
// object as part of a round trip has to note where it stood and put it
// back itself.
func cmdPlace(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var flags helpOnly
	rest, done, err := subOptions("place", "NAME X Y Z", &flags, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 4 {
		return fmt.Errorf("place NAME X Y Z -- an object in this region, and where to put it")
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

	// Its own rotation and scale.  Place sets all three at once, so
	// inventing the other two would quietly reshape whatever it was
	// pointed at.
	if err := sh.s.Place(ctx, &o.Object, at, o.Rotation, o.Scale); err != nil {
		return err
	}

	// Wait for it to have moved, rather than read it back once.
	//
	// Place is fire and forget -- the simulator answers with an
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
// the first is what a command that names no agent gets.  That order is
// the information -- it is also the order a run looks for free objects
// in.
func cmdAgents(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	_, done, err := subOptions("agents", "", &o, out, args)
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
		// The mark is the daemon's first choice, which is only
		// meaningful among the ones it is actually holding.
		mark := " "
		if a.GetState() == pb.AgentInfo_HOSTED && first {
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

// cmdHost brings an avatar up.
//
// It has to be named.  Starting an avatar puts it in the world -- an
// arrival, a presence, a notice to whoever watches for it -- so it
// follows from somebody asking rather than from a default.
func cmdHost(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o forceOptions
	rest, done, err := subOptions("host", "[-f] NAME", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("host [-f] NAME -- which avatar to start; flags come before the name")
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

// cmdLogout puts one down and keeps it down.
func cmdLogout(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o forceOptions
	rest, done, err := subOptions("logout", "[-f] NAME", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("logout [-f] NAME -- which avatar to log out; flags come before the name")
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
	fmt.Fprintf(out, "%s logged out; it will not come back until asked for by name\n", rest[0])
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
	_, done, err := subOptions("auto", "", &o, out, args)
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
	fmt.Fprintf(out, "%d auto objects worn; %d groups of %d, so %d runs at once\n",
		have, have/session.AutoGroupSize, session.AutoGroupSize,
		have/session.AutoGroupSize)
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

// helpOnly is for a command whose only flag is --help.
type helpOnly struct {
	Help bool `getopt:"--help -h  show what this command takes"`
}
