// Command slgo-inv exercises every inventory operation against the live
// grid, in one pass, and cleans up after itself.
//
// It CHANGES THINGS: it makes a folder, a notecard and a script, takes
// the object it is pointed at and rezzes it again. So it does nothing at
// all without -run, because a name like "inv" reads as "show me the
// inventory" and running it to find out what it does is exactly the
// mistake the flag prevents. To LIST inventory, use slinv ls.
//
// It exists to answer one question: can this client do everything the
// C# one built on LibreMetaverse does? Each step is a thing that took a
// day to get right over there, so each is checked here rather than
// assumed -- and each reads back what it did, because almost nothing in
// this protocol confirms itself.
//
//	slgo-inv -object Box1
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

const notecardText = "Hello from slgo.\nSecond line.\n"

func main() {
	server := flag.String("server", "127.0.0.1:7807", "slgod to attach to")
	agent := flag.String("agent", "example", "which hosted agent")
	object := flag.String("object", "Box1", "a rezzed object to work with")
	give := flag.String("give-to", "", "avatar uuid to offer an item to, if any")
	groupID := flag.String("group", "", "group to activate first; a parcel usually grants building to one")
	clean := flag.String("clean", "", "remove items whose name starts with this from the object, and stop")
	run := flag.Bool("run", false, "actually run the battery, which creates and deletes things")
	flag.Parse()

	if !*run && *clean == "" {
		fmt.Fprintln(os.Stderr,
			"slgo-inv changes things: it creates a folder, a notecard and a script,\n"+
				"and takes and re-rezzes the object it is pointed at.\n\n"+
				"  -run           do that\n"+
				"  -clean PREFIX  only remove matching items from the object\n\n"+
				"To LIST an avatar's inventory, use:  slinv ls -l -R")
		os.Exit(2)
	}

	ctx := context.Background()
	w, err := sl.Dial(ctx, *server, *agent)
	if err != nil {
		die("attach: %v", err)
	}
	defer w.Close()

	where, err := w.Where(ctx)
	if err != nil {
		die("where: %v", err)
	}
	step("attached", "%s at %v, group %s", where.Region, where.Position, group(where.ActiveGroup))

	if *groupID != "" {
		g, err := msg.ParseUUID(*groupID)
		if err != nil {
			die("bad group uuid: %v", err)
		}
		if err := w.ActivateGroup(ctx, g, 20*time.Second); err != nil {
			die("activate group: %v", err)
		}
		where, _ = w.Where(ctx)
		step("group", "active: %s", group(where.ActiveGroup))
	}

	// Cleaning only. An object keeps every copy of an item put into it
	// and renames the duplicates, so a test run that puts "README" in
	// twice leaves "README 1" behind -- and a removal matching the exact
	// name will not catch it.
	if *clean != "" {
		found, err := w.ObjectsNamed(ctx, *object, 60*time.Second)
		if err != nil || len(found) == 0 {
			die("no object named %q: %v", *object, err)
		}
		target := &found[0].Object
		contents, err := w.TaskInventory(ctx, target)
		if err != nil {
			die("task inventory: %v", err)
		}
		n := 0
		for _, t := range contents {
			if !strings.HasPrefix(t.Name, *clean) {
				continue
			}
			if err := w.RemoveFromObject(ctx, target, t.ID); err != nil {
				step("rm", "FAILED on %s: %v", t.Name, err)
			} else {
				step("rm", "%s", t.Name)
				n++
			}
		}
		step("clean", "%d removed from %s", n, target.Name)
		return
	}

	// ---- folders

	folder, err := w.CreateFolder(ctx, msg.UUID{}, "slgo test")
	if err != nil {
		die("create folder: %v", err)
	}
	step("mkdir", "slgo test = %s", folder)

	// ---- items with content

	note, err := w.CreateItem(ctx, "Notes", "made by slgo-inv", 7, 7) // notecard
	if err != nil {
		die("create notecard: %v", err)
	}
	if _, err := w.SaveNotecard(ctx, note.ID, notecardText); err != nil {
		die("save notecard: %v", err)
	}
	step("new", "notecard %s", note.ID)

	script, up, err := w.NewScript(ctx, "slgo-inv script",
		"default { state_entry() { llOwnerSay(\"hi\"); } }")
	if err != nil {
		die("create script: %v", err)
	}
	step("new", "script %s, compiled=%v", script.ID, up.Compiled)

	// ---- renaming and permissions

	after, err := w.SetItem(ctx, note.ID, "README", "", u32(sl.PermCopy|sl.PermModify|sl.PermTransfer))
	if err != nil {
		die("set item: %v", err)
	}
	step("set", "name %q, next-owner mask %#x", after.Name, after.NextOwnerMask)

	// ---- an object: put something in it, list it, take it out

	found, err := w.ObjectsNamed(ctx, *object, 60*time.Second)
	if err != nil || len(found) == 0 {
		die("no object named %q: %v", *object, err)
	}
	target := &found[0].Object
	step("object", "%s %s", target.Name, target.ID)

	if err := w.PutInObject(ctx, target, note); err != nil {
		die("put in object: %v", err)
	}
	// Settle first. Nothing acknowledges an item going into an object,
	// and asking for the contents straight away gets the contents from
	// before it arrived -- which reads exactly like the put failing.
	if err := w.Settle(ctx, 6*time.Second); err != nil {
		die("settle: %v", err)
	}
	contents, err := w.TaskInventory(ctx, target)
	if err != nil {
		die("task inventory: %v", err)
	}
	names := ""
	for _, t := range contents {
		names += fmt.Sprintf(" %s(%s)", t.Name, t.Type)
	}
	step("give --into", "%s now holds:%s", target.Name, names)

	// RemoveScripts is for scripts, as its name says -- pointed at a
	// notecard it correctly removes nothing. RemoveFromObject is the
	// general one and takes the id the object knows it by, which is not
	// the id of the inventory item it was copied from.
	removed := 0
	for _, t := range contents {
		if t.Name != note.Name && t.Name != after.Name {
			continue
		}
		if err := w.RemoveFromObject(ctx, target, t.ID); err != nil {
			step("rm --object", "FAILED on %s: %v", t.Name, err)
		} else {
			removed++
		}
	}
	step("rm --object", "%d removed", removed)

	// ---- object name, description, permissions

	if err := w.SetDescription(ctx, target, "touched by slgo-inv"); err != nil {
		die("set description: %v", err)
	}
	if err := w.SetObjectPermissions(ctx, target, sl.WhoNextOwner,
		sl.PermCopy|sl.PermModify|sl.PermTransfer); err != nil {
		die("set object permissions: %v", err)
	}
	props, err := w.Properties(ctx, target, 20*time.Second)
	if err != nil {
		die("properties: %v", err)
	}
	step("setobj", "desc %q, next-owner mask %#x", props.Description, props.NextOwnerMask)

	// ---- take and rez, the round trip

	// Where it stands, so it can be put back. A rez happens where you
	// ask; Second Life does not remember where the thing used to be, so
	// a round trip that does not record this MOVES the object.
	stood := found[0].Position
	rot, scale := found[0].Rotation, found[0].Scale

	taken, err := w.Take(ctx, target, folder, 60*time.Second)
	if err != nil {
		step("take", "FAILED: %v", err)
	} else {
		step("take", "%s -> %s", taken.Name, taken.ID)

		at := msg.Vector3{X: where.Position.X + 2, Y: where.Position.Y, Z: where.Position.Z}
		back, err := w.RezFromInventory(ctx, taken, at, where.ActiveGroup, 60*time.Second)
		if err != nil {
			step("rez", "FAILED: %v", err)
		} else {
			step("rez", "%s back in world as %s", back.Name, back.ID)
			if err := w.Place(ctx, back, stood, rot, scale); err != nil {
				step("place", "FAILED to put it back: %v", err)
			} else {
				step("place", "returned to %v", stood)
			}
		}
	}

	// ---- giving to an avatar

	if *give != "" {
		to, err := msg.ParseUUID(*give)
		if err != nil {
			die("bad avatar uuid: %v", err)
		}
		if err := w.GiveToAvatar(ctx, to, note.ID, after.Name, 7); err != nil {
			step("give --to", "FAILED: %v", err)
		} else {
			step("give --to", "offered %q to %s", after.Name, to)
		}
	}

	// ---- delete, which is the only cleanup there is

	if err := w.DeleteItem(ctx, script.ID); err != nil {
		step("rm", "FAILED on the script: %v", err)
	}
	if err := w.DeleteFolder(ctx, folder); err != nil {
		step("rm", "FAILED on the folder: %v", err)
	} else {
		step("rm", "folder and contents deleted")
	}

	fmt.Println("\nall inventory operations exercised")
}

func u32(v uint32) *uint32 { return &v }

func group(id msg.UUID) string {
	if id.IsZero() {
		return "none"
	}
	return id.String()
}

func step(name, format string, v ...any) {
	fmt.Printf("%-12s %s\n", name, fmt.Sprintf(format, v...))
}

func die(format string, v ...any) {
	fmt.Fprintf(os.Stderr, "slgo-inv: "+format+"\n", v...)
	os.Exit(1)
}
