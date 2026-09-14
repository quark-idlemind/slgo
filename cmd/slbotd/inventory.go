package main

// Inventory, and the two directions between inventory and the world.
//
// Paths here are the sl package's: names separated by slashes from the
// inventory root, with a backslash escaping a slash that is part of a
// name.  An empty path is the root.  Nothing is relative to anything --
// there is no working directory in an instant message, and a daemon
// that remembered one per sender would answer the same command two ways
// depending on what somebody typed an hour ago.
//
// # What is refused, and why
//
// Two things are only done when said in so many words:
//
//	rm on a folder needs -r
//	take needs the object named exactly, never guessed at
//
// Both are irreversible from here.  A folder removed over AIS is gone
// rather than in the trash, and an object taken out of the world is out
// of the world -- and whoever sent the command is not standing there
// watching it happen.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var inventoryCommands = map[string]*command{
	"ls": {
		params: "[PATH]",
		flags:  func() any { return new(lsFlags) },
		brief:  "list a folder; -l for the ids, -R to descend",
		group:  groupInventory,
		run:    cmdLs,
	},
	"cat": {
		params: "PATH",
		flags:  func() any { return new(helpOnly) },
		brief:  "print a notecard or a script",
		group:  groupInventory,
		run:    cmdCat,
	},
	"mkdir": {
		params: "PATH",
		flags:  func() any { return new(helpOnly) },
		brief:  "make a folder",
		group:  groupInventory,
		run:    cmdMkdir,
	},
	"rm": {
		params: "PATH",
		flags:  func() any { return new(rmFlags) },
		brief:  "delete an item; -r for a folder and everything in it",
		group:  groupInventory,
		run:    cmdRm,
	},
	"mv": {
		params: "PATH FOLDER [NAME]",
		flags:  func() any { return new(helpOnly) },
		brief:  "move something into another folder, and rename it on the way",
		group:  groupInventory,
		run:    cmdMv,
	},
	"cp": {
		params: "PATH FOLDER [NAME]",
		flags:  func() any { return new(helpOnly) },
		brief:  "copy an item into another folder",
		group:  groupInventory,
		run:    cmdCp,
	},
	"give": {
		params: "NAME|UUID PATH",
		flags:  func() any { return new(helpOnly) },
		brief:  "offer an inventory item to somebody",
		group:  groupInventory,
		run:    cmdGive,
	},
	"place": {
		params: "PATH",
		flags:  func() any { return new(placeFlags) },
		brief:  "rez an object from inventory beside this avatar",
		group:  groupInventory,
		run:    cmdPlace,
	},
	"take": {
		params: "NAME|UUID",
		flags:  func() any { return new(takeFlags) },
		brief:  "take a rezzed object into inventory; --copy leaves the original",
		group:  groupInventory,
		run:    cmdTake,
	},
	"wear": {
		params: "PATH [POINT]",
		flags:  func() any { return new(helpOnly) },
		brief:  "attach an object from inventory",
		group:  groupInventory,
		run:    cmdWear,
	},
	"detach": {
		params: "NAME",
		flags:  func() any { return new(helpOnly) },
		brief:  "take off something being worn",
		group:  groupInventory,
		run:    cmdDetach,
	},
}

// assetWait is how long an asset fetch is given.  The old UDP transfer
// is what a notecard arrives over and it is not quick.
const assetWait = 45 * time.Second

// lsFlags is what ls was asked for.
type lsFlags struct {
	Long  bool `getopt:"-l          the ids and types as well as the names"`
	Deep  bool `getopt:"-R          descend into the folders below"`
	Depth int  `getopt:"--depth=N   how far to descend; -R is --depth 4"`
	Help  bool `getopt:"--help -h   show what this command takes"`
}

func cmdLs(ctx context.Context, r *req, out io.Writer, args []string) error {
	var o lsFlags
	rest, done, err := subOptions("ls", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) > 1 {
		return usage("ls", "one folder; a name with a space in it is one argument")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}

	depth := uint(0)
	switch {
	case o.Depth > 0:
		depth = uint(o.Depth)
	case o.Deep:
		depth = 4
	}

	path := ""
	if len(rest) == 1 {
		path = rest[0]
	}
	es, err := s.ListInventory(ctx, path, depth)
	if err != nil {
		// The path may name an item rather than a folder, which is a
		// perfectly reasonable thing to list: it is one line.
		if e, ferr := entryAt(ctx, s, path); ferr == nil && !e.Folder {
			printEntry(out, e, o.Long)
			return nil
		}
		return err
	}
	if len(es) == 0 {
		fmt.Fprintln(out, "(empty)")
		return nil
	}
	for _, e := range es {
		printEntry(out, e, o.Long)
	}
	fmt.Fprintf(out, "%d entries\n", len(es))
	return nil
}

func printEntry(out io.Writer, e sl.Entry, long bool) {
	name := e.Path
	if name == "" {
		name = e.Name
	}
	if e.Folder {
		name += string(sl.PathSeparator)
	}
	if !long {
		fmt.Fprintln(out, name)
		return
	}
	kind := "folder"
	if !e.Folder {
		kind = sl.AssetType(e.Type).String()
	}
	fmt.Fprintf(out, "%s  %-10s %s\n", e.ID, kind, name)
}

func cmdCat(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("cat", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 1 {
		return usage("cat", "one notecard or script")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	e, err := entryAt(ctx, s, rest[0])
	if err != nil {
		return err
	}
	if e.Folder {
		return fmt.Errorf("%s is a folder; ls lists one", rest[0])
	}
	switch sl.AssetType(e.Type) {
	case sl.AssetNotecard, sl.AssetLSLText:
	default:
		return fmt.Errorf("%s is a %s, which has no text in it",
			e.Name, sl.AssetType(e.Type))
	}

	b, err := s.ReadAsset(ctx, client.AssetRef{
		Owner: s.Me(), Item: e.ID, Asset: e.Asset, Type: int32(e.Type),
	}, assetWait)
	if err != nil {
		return err
	}
	if sl.AssetType(e.Type) == sl.AssetNotecard {
		if text, ok := notecardText(b); ok {
			fmt.Fprintln(out, text)
			return nil
		}
	}
	fmt.Fprintln(out, strings.TrimRight(string(b), "\n"))
	return nil
}

// notecardText pulls the text out of the notecard container the grid
// stores one in.
func notecardText(b []byte) (string, bool) {
	s := string(b)
	i := strings.Index(s, "Text length ")
	if i < 0 {
		return "", false
	}
	rest := s[i+len("Text length "):]
	j := strings.IndexByte(rest, '\n')
	if j < 0 {
		return "", false
	}
	text := rest[j+1:]
	if k := strings.LastIndex(text, "}"); k >= 0 {
		text = text[:k]
	}
	return strings.TrimRight(text, "\n"), true
}

func cmdMkdir(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("mkdir", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 1 {
		return usage("mkdir", "one folder to make")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	names := sl.SplitPath(rest[0])
	if len(names) == 0 {
		return fmt.Errorf("no folder named")
	}
	parent, err := folderAt(ctx, s, sl.JoinPath(names[:len(names)-1]...))
	if err != nil {
		return err
	}
	id, err := s.CreateFolder(ctx, parent, names[len(names)-1])
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "made %s  %s\n", rest[0], id)
	return nil
}

// rmFlags is what rm was asked for.
type rmFlags struct {
	Recursive bool `getopt:"-r          a folder, and everything inside it"`
	Help      bool `getopt:"--help -h   show what this command takes"`
}

func cmdRm(ctx context.Context, r *req, out io.Writer, args []string) error {
	var o rmFlags
	rest, done, err := subOptions("rm", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 1 {
		return usage("rm", "one thing to delete")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	e, err := entryAt(ctx, s, rest[0])
	if err != nil {
		return err
	}
	if e.Folder {
		if !o.Recursive {
			return fmt.Errorf("%s is a folder; -r deletes it and everything in it, "+
				"and this is an AIS delete rather than a move to the trash", rest[0])
		}
		if err := s.DeleteFolder(ctx, e.ID); err != nil {
			return err
		}
		fmt.Fprintf(out, "deleted the folder %s\n", e.Name)
		return nil
	}
	if err := s.DeleteItem(ctx, e.ID); err != nil {
		return err
	}
	fmt.Fprintf(out, "deleted %s\n", e.Name)
	return nil
}

func cmdMv(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("mv", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	if len(rest) < 2 || len(rest) > 3 {
		return usage("mv", "something to move, a folder to move it into, and a new name if it is to have one")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	e, err := entryAt(ctx, s, rest[0])
	if err != nil {
		return err
	}
	into, err := folderAt(ctx, s, rest[1])
	if err != nil {
		return err
	}
	name := ""
	if len(rest) == 3 {
		name = rest[2]
	}

	if e.Folder {
		if err := s.MoveFolder(ctx, e.ID, into); err != nil {
			return err
		}
		if name != "" {
			if err := s.RenameFolder(ctx, e.ID, name); err != nil {
				return err
			}
		}
	} else if name != "" {
		if err := s.MoveItem(ctx, e.ID, into, name); err != nil {
			return err
		}
	} else if err := s.MoveItem(ctx, e.ID, into); err != nil {
		return err
	}
	fmt.Fprintf(out, "moved %s into %s\n", e.Name, rest[1])
	return nil
}

func cmdCp(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("cp", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	if len(rest) < 2 || len(rest) > 3 {
		return usage("cp", "an item to copy, a folder to copy it into, and a new name if it is to have one")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	e, err := entryAt(ctx, s, rest[0])
	if err != nil {
		return err
	}
	if e.Folder {
		return fmt.Errorf("%s is a folder; only items are copied", rest[0])
	}
	into, err := folderAt(ctx, s, rest[1])
	if err != nil {
		return err
	}
	name := e.Name
	if len(rest) == 3 {
		name = rest[2]
	}
	it, err := s.CopyItem(ctx, e.ID, into, name, 60*time.Second)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "copied to %s/%s  %s\n", rest[1], it.Name, it.ID)
	return nil
}

func cmdGive(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("give", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 2 {
		return usage("give", "somebody to give to, and one thing to give")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	to, err := personNamed(ctx, s, rest[0])
	if err != nil {
		return err
	}
	e, err := entryAt(ctx, s, rest[1])
	if err != nil {
		return err
	}
	if e.Folder {
		return fmt.Errorf("%s is a folder; only items are given", rest[1])
	}
	if err := s.GiveToAvatar(ctx, to, e.ID, e.Name, int8(e.Type)); err != nil {
		return err
	}
	name := s.NameOr(to)
	if name == "" {
		name = to.String()
	}
	fmt.Fprintf(out, "offered %s to %s; it is theirs when they accept\n", e.Name, name)
	return nil
}

// placeFlags is what place was asked for.
type placeFlags struct {
	Away float64 `getopt:"--away=METRES  how far in front of the avatar to put it [2]"`
	Help bool    `getopt:"--help -h      show what this command takes"`
}

// cmdPlace rezzes something from inventory beside the avatar.
//
// The active group is sent with it, because a parcel usually grants
// "create objects" to a GROUP rather than to individuals: rezzed as
// nobody's group the simulator applies the rule for strangers and
// refuses, blaming the land rather than the request.
func cmdPlace(ctx context.Context, r *req, out io.Writer, args []string) error {
	var o placeFlags
	rest, done, err := subOptions("place", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 1 {
		return usage("place", "one object to rez")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	e, err := entryAt(ctx, s, rest[0])
	if err != nil {
		return err
	}
	if e.Folder {
		return fmt.Errorf("%s is a folder; only an object is rezzed", rest[0])
	}
	if sl.AssetType(e.Type) != sl.AssetObject {
		return fmt.Errorf("%s is a %s; only an object can be rezzed",
			e.Name, sl.AssetType(e.Type))
	}

	where, err := s.Where(ctx)
	if err != nil {
		return err
	}
	away := float32(2)
	if o.Away > 0 {
		away = float32(o.Away)
	}
	at := where.Position
	at.X += away

	group, err := s.ActiveGroup(ctx)
	if err != nil {
		group = msg.UUID{}
	}
	obj, err := s.RezFromInventory(ctx, itemOf(e), at, group, 60*time.Second)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "rezzed %s at %.0f, %.0f, %.0f\n", obj.ID, at.X, at.Y, at.Z)
	return nil
}

// takeFlags is what take was asked for.
type takeFlags struct {
	Copy bool `getopt:"--copy -c   take a copy and leave the original standing"`
	Help bool `getopt:"--help -h   show what this command takes"`
}

func cmdTake(ctx context.Context, r *req, out io.Writer, args []string) error {
	var o takeFlags
	rest, done, err := subOptions("take", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) == 0 {
		return usage("take", "one object to take")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	obj, err := objectNamed(ctx, s, strings.Join(rest, " "))
	if err != nil {
		return err
	}
	folder, err := s.ObjectsFolder(ctx)
	if err != nil {
		return err
	}
	take := s.Take
	if o.Copy {
		take = s.TakeCopy
	}
	it, err := take(ctx, obj, folder, 60*time.Second)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "took %s into Objects  %s\n", it.Name, it.ID)
	return nil
}

func cmdWear(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("wear", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	if len(rest) < 1 || len(rest) > 2 {
		return usage("wear", "one object, and where to attach it if it is not to go where it last was")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	e, err := entryAt(ctx, s, rest[0])
	if err != nil {
		return err
	}
	if e.Folder {
		return fmt.Errorf("%s is a folder; only an object is worn", rest[0])
	}
	point := 0
	if len(rest) == 2 {
		p, ok := sl.AttachPointNamed(rest[1])
		if !ok {
			return fmt.Errorf("no attachment point called %q", rest[1])
		}
		point = p
	}
	at, err := s.Wear(ctx, itemOf(e), point, 60*time.Second)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "wearing %s on %s\n", e.Name, sl.AttachPointName(at.Point))
	return nil
}

func cmdDetach(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("detach", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	if len(rest) == 0 {
		return usage("detach", "one worn object")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	want := strings.Join(rest, " ")
	worn, err := s.WornObjects(ctx)
	if err != nil {
		return err
	}
	var found []*sl.Attached
	for _, w := range worn {
		if strings.EqualFold(w.Object.Name, want) {
			found = append(found, w)
		}
	}
	switch len(found) {
	case 0:
		return fmt.Errorf("nothing called %q is being worn", want)
	case 1:
	default:
		return fmt.Errorf("%d worn objects are called %q", len(found), want)
	}
	if err := s.TakeOff(ctx, found[0].Item); err != nil {
		return err
	}
	fmt.Fprintf(out, "took off %s\n", found[0].Object.Name)
	return nil
}

// ------------------------------------------------------------- resolving

// entryAt is the one thing a path names, and refuses a name that means
// more than one.
//
// A folder may hold several items of one name -- inventory has no rule
// against it -- and every command here acts on what it is given, so the
// ambiguity is reported rather than resolved.  Which of two identical
// names somebody meant is not something to guess at when the answer is
// a delete.
func entryAt(ctx context.Context, s *sl.Session, path string) (sl.Entry, error) {
	names := sl.SplitPath(path)
	if len(names) == 0 {
		return sl.Entry{}, fmt.Errorf("no path given")
	}
	parent := sl.JoinPath(names[:len(names)-1]...)
	want := names[len(names)-1]

	kids, err := s.ListInventory(ctx, parent, 0)
	if err != nil {
		return sl.Entry{}, err
	}
	return pickEntry(kids, want, parent)
}

// pickEntry is the choosing, kept apart from the listing so that the
// rule can be read and tested without an inventory to list.
//
// Matching ignores case, as every name this daemon matches does, and a
// name that means more than one thing is refused with the ids rather
// than resolved.  Which of two identical names somebody meant is not
// something to guess at when the answer is a delete.
func pickEntry(kids []sl.Entry, want, parent string) (sl.Entry, error) {
	var found []sl.Entry
	for _, e := range kids {
		if strings.EqualFold(e.Name, want) {
			found = append(found, e)
		}
	}
	where := parent
	if where == "" {
		where = "the inventory root"
	}
	switch len(found) {
	case 0:
		return sl.Entry{}, fmt.Errorf("nothing called %q in %s", want, where)
	case 1:
		return found[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d things in %s are called %q:", len(found), where, want)
	for _, e := range found {
		fmt.Fprintf(&b, "\n  %s", e.ID)
	}
	return sl.Entry{}, fmt.Errorf("%s", b.String())
}

// folderAt is the folder a path names, including the root for an empty
// path.
func folderAt(ctx context.Context, s *sl.Session, path string) (msg.UUID, error) {
	if len(sl.SplitPath(path)) == 0 {
		return s.InventoryRoot(), nil
	}
	e, err := entryAt(ctx, s, path)
	if err != nil {
		return msg.UUID{}, err
	}
	if !e.Folder {
		return msg.UUID{}, fmt.Errorf("%s is not a folder", path)
	}
	return e.ID, nil
}

// itemOf is a listing entry as the item the operations take.
//
// A listing and an item are the same thing said twice: an Entry is what
// AIS listed and an Item is what a message carries, and the fields that
// matter to a rez or a wear -- the permissions, the flags, the folder
// -- are in both.  Filled out in full rather than in part because a rez
// sends every one of them and an item that lost its masks on the way
// through would be rezzed with permissions nobody asked for.
func itemOf(e sl.Entry) *sl.Item {
	return &sl.Item{
		ID:            e.ID,
		ParentID:      e.Parent,
		AssetID:       e.Asset,
		Name:          e.Name,
		Desc:          e.Desc,
		Type:          e.Type,
		InvType:       e.InvType,
		Created:       e.Created,
		CreatorID:     e.Creator,
		OwnerID:       e.Owner,
		BaseMask:      e.BaseMask,
		OwnerMask:     e.OwnerMask,
		GroupMask:     e.GroupMask,
		EveryoneMask:  e.EveryoneMask,
		NextOwnerMask: e.NextOwnerMask,
		SaleType:      e.SaleType,
		SalePrice:     e.SalePrice,
		IsLink:        e.IsLink,
	}
}
