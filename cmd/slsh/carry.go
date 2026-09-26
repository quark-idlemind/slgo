package main

// Objects moving between the world and inventory, and who may do what
// with them once they are there.
//
//	take   the world into inventory
//	place  inventory into the world
//	perms  what the next owner, the group or everyone may do
//
// These were slinv's, in the days when a separate daemon owned the
// session; the operations themselves have been in the sl package all
// along and only wanted a way to be asked for.
//
// # Why the pair is take and place
//
// "rez" is what everyone calls this, and it is already the command that
// builds an object from a JSON file.  One word cannot mean both "make
// what this file describes" and "put back what I took": the first
// invents an object and the second restores one, and a person who mixed
// them up would be told their file was not valid JSON.  That has not
// changed and is not going to.
//
// What did change is that "place" became free.  It used to be the
// command that repositioned something already rezzed, and that is now
// "move", which is the plainer word for shifting a thing that is
// already there and leaves "place" to mean what it sounds like: putting
// a thing into the world.  So the pair is take and place, and each of
// them says which direction it goes in.
//
// # The names these two used to have
//
// This command was called "bring", and nothing answers to that now.  A
// script that says it stops with an unknown command, which is loud,
// immediate and costs a re-run, so there is no alias for it: an alias
// would keep the word in circulation, and the word being a poor
// description of the act is the whole reason for the rename.
//
// "place" is the half worth being careful about, because it did not
// disappear -- it changed meaning, and both meanings are spelt the same
// way.  See the argument count in cmdPlace for what that costs and what
// is done about it.

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
		params:   "NAME|UUID",
		flags:    func() any { return new(takeFlags) },
		brief:    "take a rezzed object into inventory; --copy tries to leave the original",
		keywords: "pick up take into inventory object ground world copy back up",
		man:      "take",
		run:      cmdTake,
	},
	"place": {
		params:   "PATH|UUID",
		flags:    func() any { return new(placeFlags) },
		brief:    "put an inventory object into the world, which is what take undoes",
		keywords: "rez put out down object from inventory world ground put back set down",
		man:      "place",
		run:      cmdPlace,
	},
	"perms": {
		params:   "NAME|UUID",
		flags:    func() any { return new(permsFlags) },
		brief:    "set what others may do with a rezzed object: c copy, m modify, t transfer, v move",
		keywords: "permissions next owner copy modify transfer move owner group everyone allow others rights",
		man:      "perms",
		run:      cmdPerms,
	},
}

type takeFlags struct {
	Copy bool   `getopt:"--copy       take a copy and try to leave the original standing"`
	Into string `getopt:"--into=FOLDER  the folder it lands in [Objects]"`
	Wait int    `getopt:"--wait -w=SECONDS  how long to let the region describe itself [30]"`
	Help bool   `getopt:"--help -h    show what this command takes"`
}

func cmdTake(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o takeFlags
	args, done, err := subOptions("take", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return usageError("take")
	}

	obj, err := sh.objectNamed(ctx, args[0], o.Wait)
	if err != nil {
		return err
	}

	folder, err := sh.takeFolder(ctx, o.Into)
	if err != nil {
		return err
	}

	if o.Copy {
		it, err := sh.s.TakeCopy(ctx, obj, folder, 0)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s is in inventory as %s, and still where it was\n", obj, it.Name)
		return nil
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

type placeFlags struct {
	At   string `getopt:"--at=X,Y,Z   where to put it [beside the avatar]"`
	Help bool   `getopt:"--help -h    show what this command takes"`
}

func cmdPlace(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o placeFlags
	args, done, err := subOptions("place", &o, out, args)
	if err != nil || done {
		return err
	}
	// Exactly one, counted rather than taken from the front, because of
	// what this name used to mean.  "place NAME X Y Z" repositioned a
	// rezzed object, and the first word of such a line is very often the
	// name of an inventory item as well: an object taken into inventory
	// keeps its name, and one rezzed from an item was named by it.
	// Reading args[0] and ignoring the rest would answer that line
	// by rezzing a second copy beside the avatar and reporting success,
	// leaving the object the script meant to move exactly where it was.
	// A usage error costs a re-run; a stray object costs finding it.
	if len(args) != 1 {
		return usageError("place",
			"one object, quoted if its name has spaces",
			"to move something already rezzed, that is \"move NAME X Y Z\"")
	}

	// thingAt and not entryAt: the id is going to the grid as the thing
	// to rez, so a link is followed to what it points at.
	e, err := sh.thingAt(ctx, args[0])
	if err != nil {
		return err
	}
	if e.Folder {
		return fmt.Errorf("%s is a folder; place takes one object", args[0])
	}
	// An Entry says where a thing sits; RezFromInventory wants the item
	// itself, with its asset and permissions on it.
	it, err := sh.itemAt(ctx, e)
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

// beside is a metre EAST of the avatar, near enough to reach and clear
// of the avatar itself.
//
// Not a metre in front, which is what this said until somebody measured
// it: the rotation is never consulted, so where the thing appears has
// nothing to do with which way the avatar is facing.
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
	args, done, err := subOptions("perms", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return usageError("perms")
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
		// What the mask was read back as, which the permission rules
		// may have adjusted from what was typed.
		got, err := sh.s.SetObjectPermissions(ctx, obj, s.who, mask)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s may now %s\n", s.named, sl.PermWords(got))
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
