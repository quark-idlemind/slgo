package main

// What is inside a rezzed object.
//
// An object holds inventory of its own -- scripts, notecards, whatever
// was dropped in it -- and it is a different place from the avatar's
// inventory, with its own ids.  The copy of a script inside a prim is
// not the item it came from, and every operation on it wants the copy's
// id rather than the original's.
//
// So the same three verbs take --in OBJECT and work there instead:
//
//	ls --in Box1              what the object holds
//	rm --in Box1 hello.lsl    delete one of them
//	mv --in Box1 old new      rename one of them
//
// A flag rather than three more words, because these are not different
// operations: they are listing, deleting and renaming, in a container
// that happens to be a prim.  What is not the same is putting something
// in, and that has its own command: "give" offers an item to a person
// and waits for them to accept, while dropping one into your own object
// happens at once and asks nobody.

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/quark-idlemind/slgo/sl"
)

var insideCommands = map[string]*command{
	"drop": {
		usage: "drop OBJECT PATH",
		brief: "put an inventory item inside a rezzed object",
		run:   cmdDrop,
	},
	"new": {
		usage: "new [--kind notecard|script] [--from FILE] PATH",
		brief: "make a notecard or a script in inventory, empty or from a file",
		run:   cmdNew,
	},
}

// insideObject resolves what --in named, and says so in the error when
// it is not there: a mistyped object name and an empty object are very
// different answers to "why did nothing happen".
func (sh *Shell) insideObject(ctx context.Context, what string) (*sl.Object, error) {
	o, err := sh.objectNamed(ctx, strings.TrimSpace(what), 30)
	if err != nil {
		return nil, err
	}
	return o, nil
}

// listInside prints an object's contents, in the columns ls uses: name
// last, so that a name with spaces in it cannot run into anything.
func (sh *Shell) listInside(ctx context.Context, out io.Writer, what string, long bool) error {
	o, err := sh.insideObject(ctx, what)
	if err != nil {
		return err
	}
	items, err := sh.s.TaskInventory(ctx, o)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintf(out, "%s holds nothing\n", o.Name)
		return nil
	}
	// The columns ls uses: kind first, id where ls puts it, and the
	// name last so that a name with spaces cannot run into anything.
	for _, it := range items {
		if long {
			fmt.Fprintf(out, "%-10s %-36s %s\n", it.Type, it.ID, it.Name)
			continue
		}
		fmt.Fprintf(out, "%-10s %s\n", it.Type, it.Name)
	}
	return nil
}

// removeInside deletes items from inside an object.
//
// Deleting, not taking: the message says remove and the copy is gone.
// Anything wanted back has to come from the original in inventory,
// which is why this says what it deleted rather than reporting a count.
func (sh *Shell) removeInside(ctx context.Context, out io.Writer, what string, names []string) error {
	o, err := sh.insideObject(ctx, what)
	if err != nil {
		return err
	}
	for _, name := range names {
		it, err := sh.s.FindInObject(ctx, o, name)
		if err != nil {
			return err
		}
		if err := sh.s.RemoveFromObject(ctx, o, it.ID); err != nil {
			return err
		}
		fmt.Fprintf(out, "deleted %q from %s\n", it.Name, o.Name)
	}
	return nil
}

// renameInside renames one item inside an object.
func (sh *Shell) renameInside(ctx context.Context, out io.Writer, what string, from, to string) error {
	o, err := sh.insideObject(ctx, what)
	if err != nil {
		return err
	}
	it, err := sh.s.FindInObject(ctx, o, from)
	if err != nil {
		return err
	}
	if err := sh.s.RenameInObject(ctx, o, *it, to); err != nil {
		return err
	}
	fmt.Fprintf(out, "%q in %s is now %q\n", from, o.Name, to)
	return nil
}

func cmdDrop(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("drop", "OBJECT PATH", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: drop OBJECT PATH")
	}

	obj, err := sh.insideObject(ctx, args[0])
	if err != nil {
		return err
	}
	e, err := sh.entryAt(ctx, strings.Join(args[1:], " "))
	if err != nil {
		return err
	}
	if e.Folder {
		return fmt.Errorf("%s is a folder; drop takes one item", args[1])
	}
	// The item itself, not the entry: what goes over the wire is every
	// field of it, and anything left out is set to zero -- which for a
	// permission mask means taking the rights away.
	it, err := sh.s.FindItem(ctx, e.Parent, e.Name)
	if err != nil {
		return err
	}
	if err := sh.s.PutInObject(ctx, obj, it); err != nil {
		return err
	}
	fmt.Fprintf(out, "put %q in %s\n", it.Name, obj.Name)
	return nil
}

type newFlags struct {
	Kind string `getopt:"--kind=KIND   notecard or script [notecard]"`
	From string `getopt:"--from=FILE   its contents; without this it is empty"`
	Help bool   `getopt:"--help -h     show what this command takes"`
}

// cmdNew makes a notecard or a script.
//
// Both are free, and that is not a detail: an upload of a texture costs
// L$ and this does not, because a notecard and a script go up through
// their own capability rather than through the asset upload that
// charges.
func cmdNew(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o newFlags
	args, done, err := subOptions("new", "PATH", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: new [--kind notecard|script] [--from FILE] PATH")
	}

	body := ""
	if o.From != "" {
		b, err := os.ReadFile(o.From)
		if err != nil {
			return err
		}
		body = string(b)
	}

	path := strings.Join(args, " ")
	names := sl.SplitPath(path)
	if len(names) == 0 {
		return fmt.Errorf("new: no name given")
	}
	name := names[len(names)-1]
	parent, err := sh.folderAt(ctx, names[:len(names)-1])
	if err != nil {
		return err
	}

	switch strings.ToLower(o.Kind) {
	case "", "notecard":
		it, err := sh.s.CreateItem(ctx, name, "", int8(sl.AssetNotecard), int8(sl.AssetNotecard))
		if err != nil {
			return err
		}
		if err := sh.s.MoveItem(ctx, it.ID, parent); err != nil {
			return err
		}
		if body != "" {
			if _, err := sh.s.SaveNotecard(ctx, it.ID, body); err != nil {
				return err
			}
		}
		fmt.Fprintf(out, "%s %s\n", it.Name, it.ID)
	case "script":
		// NewScript makes the item and writes the source in one go,
		// because a script with no source is a compile error waiting
		// to happen the moment anything runs it.
		if body == "" {
			body = "default\n{\n    state_entry()\n    {\n    }\n}\n"
		}
		it, res, err := sh.s.NewScript(ctx, name, body)
		if err != nil {
			return err
		}
		if err := sh.s.MoveItem(ctx, it.ID, parent); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s %s\n", it.Name, it.ID)
		if res != nil && !res.Compiled {
			fmt.Fprintf(out, "it did not compile\n")
		}
	default:
		return fmt.Errorf("--kind is notecard or script, not %q", o.Kind)
	}
	return nil
}
