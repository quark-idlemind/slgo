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

	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
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
	"agents": {
		usage: "agents",
		brief: "the sessions this daemon holds, oldest first; * is the default",
		run:   cmdAgents,
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

	var v msg.Vector3
	for i, s := range rest {
		f, err := strconv.ParseFloat(s, 32)
		if err != nil {
			return fmt.Errorf("%q is not a number", s)
		}
		switch i {
		case 0:
			v.X = float32(f)
		case 1:
			v.Y = float32(f)
		case 2:
			v.Z = float32(f)
		}
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

	type sessioner interface {
		Sessions(context.Context) ([]string, error)
	}
	b, ok := sh.s.Backend().(sessioner)
	if !ok {
		fmt.Fprintf(out, "%s (logged in directly, not through a daemon)\n", sh.s.Info().AvatarName)
		return nil
	}
	names, err := b.Sessions(ctx)
	if err != nil {
		return err
	}
	here := sh.s.Info().Name
	for i, n := range names {
		mark := " "
		if i == 0 {
			mark = "*"
		}
		note := ""
		if n == here {
			note = "   <- this shell"
		}
		fmt.Fprintf(out, "%s %s%s\n", mark, n, note)
	}
	return nil
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
		objs, err := session.SetupAuto(ctx, sh.s, o.N)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%d of %d ready\n", len(objs), o.N)
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

// helpOnly is for a command whose only flag is --help.
type helpOnly struct {
	Help bool `getopt:"--help -h  show what this command takes"`
}
