package main

// Objects moving between the world and inventory, and who may do what
// with them once they are there.
//
//	take   the world into inventory
//	bring  inventory into the world
//	perms  what the next owner, the group or everyone may do
//
// These were slinv's, in the days when a separate daemon owned the
// session; the operations themselves have been in the sl package all
// along and only wanted a way to be asked for.
//
// "rez" would have been the name for bring, since that is what everyone
// calls it, and it is already the command that builds an object from a
// JSON file.  One word cannot mean both "make what this file describes"
// and "put back what I took": the first invents an object and the
// second restores one, and a person who mixed them up would be told
// their file was not valid JSON.  So the pair is take and bring, which
// at least say which direction they go in.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var carryCommands = map[string]*command{
	"take": {
		usage: "take [--into FOLDER] NAME|UUID",
		brief: "take a rezzed object into inventory",
		run:   cmdTake,
	},
	"bring": {
		usage: "bring [--at X,Y,Z] PATH|UUID",
		brief: "put an inventory object into the world, which is what take undoes",
		run:   cmdBring,
	},
	"perms": {
		usage: "perms [--next LETTERS] [--group LETTERS] [--everyone LETTERS] NAME|UUID",
		brief: "set what others may do with a rezzed object: c copy, m modify, t transfer, v move",
		run:   cmdPerms,
	},
}

type takeFlags struct {
	// slinv had --copy, and this does not: a take copy is a different
	// DeRez destination, the sl package has only the one, and guessing
	// at the enum is how an object ends up somewhere it cannot be got
	// back from.  See Take.
	Into string `getopt:"--into=FOLDER  the folder it lands in [Objects]"`
	Wait int    `getopt:"--wait -w=SECONDS  how long to let the region describe itself [30]"`
	Help bool   `getopt:"--help -h    show what this command takes"`
}

func cmdTake(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o takeFlags
	args, done, err := subOptions("take", "NAME|UUID", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: take [--into FOLDER] NAME|UUID")
	}

	obj, err := sh.objectNamed(ctx, args[0], o.Wait)
	if err != nil {
		return err
	}

	folder, err := sh.takeFolder(ctx, o.Into)
	if err != nil {
		return err
	}

	it, err := sh.s.Take(ctx, obj, folder, 0)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is in inventory as %s\n", obj, it.Name)
	return nil
}

// takeFolder is where a take lands: what --into named, or the Objects
// folder, which is where the viewer puts one and so where a person will
// look for it.
func (sh *Shell) takeFolder(ctx context.Context, into string) (msg.UUID, error) {
	if strings.TrimSpace(into) == "" {
		return sh.s.ObjectsFolder(ctx)
	}
	if id, err := msg.ParseUUID(strings.TrimSpace(into)); err == nil {
		return id, nil
	}
	e, err := sh.entryAt(ctx, into)
	if err != nil {
		return msg.UUID{}, err
	}
	if !e.Folder {
		return msg.UUID{}, fmt.Errorf("%s is an item, not a folder", into)
	}
	return e.ID, nil
}

type bringFlags struct {
	At   string `getopt:"--at=X,Y,Z   where to put it [beside the avatar]"`
	Help bool   `getopt:"--help -h    show what this command takes"`
}

func cmdBring(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o bringFlags
	args, done, err := subOptions("bring", "PATH|UUID", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: bring [--at X,Y,Z] PATH|UUID")
	}

	e, err := sh.entryAt(ctx, args[0])
	if err != nil {
		return err
	}
	if e.Folder {
		return fmt.Errorf("%s is a folder; bring takes one object", args[0])
	}
	// An Entry says where a thing sits; RezFromInventory wants the item
	// itself, with its asset and permissions on it.
	it, err := sh.s.FindItem(ctx, e.Parent, e.Name)
	if err != nil {
		return err
	}

	where, err := sh.s.Where(ctx)
	if err != nil {
		return err
	}
	at := beside(where.Position)
	if o.At != "" {
		if at, err = parsePoint(o.At); err != nil {
			return err
		}
	}

	// The active group is not decoration here: a parcel usually grants
	// "create objects" to a group rather than to a person, and rezzing
	// with no group named is refused with a complaint about the land.
	obj, err := sh.s.RezFromInventory(ctx, it, at, where.ActiveGroup, 0)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is at %.0f, %.0f, %.0f\n", obj, at.X, at.Y, at.Z)
	return nil
}

// beside is a metre in front of the avatar, which is where a person
// standing there would expect a thing they just rezzed to appear.
func beside(at msg.Vector3) msg.Vector3 {
	return msg.Vector3{X: at.X + 1, Y: at.Y, Z: at.Z}
}

type permsFlags struct {
	Owner    string `getopt:"--owner=LETTERS     what the owner may do"`
	Group    string `getopt:"--group=LETTERS     what the group may do"`
	Everyone string `getopt:"--everyone=LETTERS  what anybody may do"`
	Next     string `getopt:"--next=LETTERS      what the next owner may do"`
	Wait     int    `getopt:"--wait -w=SECONDS   how long to let the region describe itself [30]"`
	Help     bool   `getopt:"--help -h           show what this command takes"`
}

func cmdPerms(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o permsFlags
	args, done, err := subOptions("perms", "NAME|UUID", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: perms [--next LETTERS] [--group LETTERS] [--everyone LETTERS] NAME|UUID")
	}

	set := []struct {
		flag  string
		text  string
		who   uint8
		named string
	}{
		{"--owner", o.Owner, sl.WhoOwner, "the owner"},
		{"--group", o.Group, sl.WhoGroup, "the group"},
		{"--everyone", o.Everyone, sl.WhoEveryone, "everyone"},
		{"--next", o.Next, sl.WhoNextOwner, "the next owner"},
	}
	asked := 0
	for _, s := range set {
		if s.text != "" {
			asked++
		}
	}
	if asked == 0 {
		// Saying nothing and doing nothing is the wrong answer to a
		// command that changes things: a person who typed this meant
		// to change something.
		return fmt.Errorf("perms changes permissions: name at least one of --owner, --group, --everyone or --next")
	}

	obj, err := sh.objectNamed(ctx, args[0], o.Wait)
	if err != nil {
		return err
	}
	for _, s := range set {
		if s.text == "" {
			continue
		}
		mask, err := permMask(s.text)
		if err != nil {
			return fmt.Errorf("%s: %w", s.flag, err)
		}
		if err := sh.s.SetObjectPermissions(ctx, obj, s.who, mask); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s may now %s\n", s.named, permWords(mask))
	}
	return nil
}

// permMask reads the letters slinv used, because they are what is in
// the notes and scripts of every experiment run so far.
func permMask(text string) (uint32, error) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "all":
		return sl.PermAll, nil
	case "none", "":
		return 0, nil
	}
	var mask uint32
	for _, c := range strings.ToLower(strings.TrimSpace(text)) {
		switch c {
		case 'c':
			mask |= sl.PermCopy
		case 'm':
			mask |= sl.PermModify
		case 't':
			mask |= sl.PermTransfer
		case 'v':
			mask |= sl.PermMove
		default:
			return 0, fmt.Errorf("%q is not a permission: c copy, m modify, t transfer, v move, or all, or none", string(c))
		}
	}
	return mask, nil
}

// permWords says a mask back in words, so that what was set is legible
// without knowing the letters.
func permWords(mask uint32) string {
	var have []string
	for _, p := range []struct {
		bit  uint32
		word string
	}{
		{sl.PermCopy, "copy"},
		{sl.PermModify, "modify"},
		{sl.PermTransfer, "transfer"},
		{sl.PermMove, "move"},
	} {
		if mask&p.bit != 0 {
			have = append(have, p.word)
		}
	}
	if len(have) == 0 {
		return "nothing"
	}
	return strings.Join(have, ", ")
}
