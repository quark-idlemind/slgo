package main

// Inventory as a filesystem.
//
// The tree really is a tree, so cd and ls are the right interface for
// it, and the paths they print are the paths they take: a listing can
// be written to a file, edited into a list of moves, and run.  That is
// why ls prints one bare path per line by default and keeps the columns
// for ls -l.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var inventoryCommands = map[string]*command{
	"pwd": {
		usage: "pwd",
		brief: "where in inventory we are",
		run: func(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
			var flags helpOnly
			if _, done, err := subOptions("pwd", "", &flags, out, args); err != nil || done {
				return err
			}
			fmt.Fprintln(out, sh.Pwd())
			return nil
		},
	},
	"cd": {
		usage: "cd [PATH]",
		brief: "change folder; no path goes to the root, .. goes up",
		run:   cmdCd,
	},
	"ls": {
		usage: "ls [-l] [-r] [-t] [-T] [PATH]",
		brief: "list a folder; -l for detail, -T the time as well, -t newest first, -r to descend",
		run:   cmdLs,
	},
	"cat": {
		usage: "cat PATH",
		brief: "print a notecard or a script",
		run:   cmdCat,
	},
	"mkdir": {
		usage: "mkdir PATH",
		brief: "make a folder",
		run:   cmdMkdir,
	},
	"mv": {
		usage: "mv PATH DEST",
		brief: "move into a folder, or rename if DEST is a plain name; folders too",
		run:   cmdMv,
	},
	"rm": {
		usage: "rm [--remove-all-copies] PATH...",
		brief: "delete items, permanently; --remove-all-copies for every one of a name",
		run:   cmdRm,
	},
	"emptytrash": {
		usage: "emptytrash",
		brief: "throw away everything in the trash, permanently",
		run:   cmdEmptyTrash,
	},
	"find": {
		usage: "find TEXT [PATH]",
		brief: "look for names containing TEXT, from here down",
		run:   cmdFind,
	},
}

// resolveDir turns a path into a folder, relative to where we are.
//
// A leading / is from the root, .. goes up, and . stays.  The names are
// walked rather than looked up whole, because a path is names and the
// grid answers about ids.
func (sh *Shell) resolveDir(ctx context.Context, path string) ([]string, msg.UUID, error) {
	sh.mu.Lock()
	cur := append([]string(nil), sh.cwd...)
	sh.mu.Unlock()

	if strings.HasPrefix(path, "/") {
		cur = nil
	}
	for _, name := range sl.SplitPath(path) {
		switch name {
		case ".":
		case "..":
			if len(cur) > 0 {
				cur = cur[:len(cur)-1]
			}
		default:
			cur = append(cur, name)
		}
	}

	id, err := sh.folderAt(ctx, cur)
	if err != nil {
		return nil, msg.UUID{}, err
	}
	return cur, id, nil
}

// folderAt resolves a list of names to a folder id.
func (sh *Shell) folderAt(ctx context.Context, names []string) (msg.UUID, error) {
	if len(names) == 0 {
		return sh.s.InventoryRoot(), nil
	}
	es, err := sh.s.ListInventory(ctx, sl.JoinPath(names[:len(names)-1]...), 0)
	if err != nil {
		return msg.UUID{}, err
	}
	want := names[len(names)-1]
	for _, e := range es {
		if e.Folder && strings.EqualFold(e.Name, want) {
			return e.ID, nil
		}
	}
	return msg.UUID{}, fmt.Errorf("no folder %q", want)
}

// entryAt finds one entry, by path or by id, folder or item.
//
// An id is worth taking because names are not unique: a folder can hold
// eighteen things called the same thing, and a path names the first of
// them.  ls -l prints the id beside the path for exactly this, so that
// a listing of duplicates can still be edited into commands that mean
// one each.
func (sh *Shell) entryAt(ctx context.Context, path string) (sl.Entry, error) {
	es, err := sh.entriesAt(ctx, path)
	if err != nil {
		return sl.Entry{}, err
	}
	return es[0], nil
}

// entriesAt is entryAt for everything the path names, in listing order.
//
// A path can name more than one thing, since a folder may hold a dozen
// items called the same thing -- which is what rm --remove-all-copies
// is for.  An id names exactly one, so that form returns the one.
func (sh *Shell) entriesAt(ctx context.Context, path string) ([]sl.Entry, error) {
	if id, err := msg.ParseUUID(strings.TrimSpace(path)); err == nil {
		e, err := sh.entryByID(ctx, id)
		if err != nil {
			return nil, err
		}
		return []sl.Entry{e}, nil
	}
	names := sl.SplitPath(path)
	if len(names) == 0 {
		return nil, fmt.Errorf("no path given")
	}
	dir := strings.Join([]string{}, "")
	if strings.HasPrefix(path, "/") {
		dir = "/"
	}
	dir += sl.JoinPath(names[:len(names)-1]...)

	_, id, err := sh.resolveDir(ctx, dir)
	if err != nil {
		return nil, err
	}
	es, err := sh.s.ListFolder(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	want := names[len(names)-1]
	found := matchName(es, want)
	if len(found) == 0 {
		return nil, fmt.Errorf("nothing called %q here", want)
	}
	return found, nil
}

// matchName picks out everything of a name, keeping the listing order
// so that the first is the one a path without --remove-all-copies
// means.
//
// The comparison ignores case, as the rest of the shell does: the grid
// keeps the case a name was given but does not make two names that
// differ only in case into two different names worth telling apart at a
// prompt.
func matchName(es []sl.Entry, want string) []sl.Entry {
	var found []sl.Entry
	for _, e := range es {
		if strings.EqualFold(e.Name, want) {
			found = append(found, e)
		}
	}
	return found
}

// entryByID looks for an id here, then anywhere below the root.
//
// Here first because that is nearly always where it is, and the whole
// tree is a hundred requests.
func (sh *Shell) entryByID(ctx context.Context, id msg.UUID) (sl.Entry, error) {
	sh.mu.Lock()
	cwd := sh.cwdID
	sh.mu.Unlock()

	if es, err := sh.s.ListFolder(ctx, cwd, 0); err == nil {
		for _, e := range es {
			if e.ID == id {
				return e, nil
			}
		}
	}
	es, err := sh.s.ListInventory(ctx, "", 4)
	if err != nil {
		return sl.Entry{}, err
	}
	for _, e := range es {
		if e.ID == id {
			return e, nil
		}
	}
	return sl.Entry{}, fmt.Errorf("nothing here has the id %s", id)
}

func cmdCd(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("cd", "[PATH]", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) > 1 {
		return fmt.Errorf("usage: cd [PATH]")
	}
	// No path is the root, the way cd with no argument is home in a
	// shell, and it has to be said outright: the empty path resolves
	// to wherever you already are, so bare cd did nothing at all.
	path := "/"
	if len(args) > 0 {
		path = args[0]
	}
	names, id, err := sh.resolveDir(ctx, path)
	if err != nil {
		return err
	}
	sh.mu.Lock()
	sh.cwd, sh.cwdID = names, id
	sh.mu.Unlock()
	sh.prompt()
	return nil
}

// lsOptions is what ls was asked for.
//
// The path is not an option and is unexported so that options leaves it
// alone: it registers every field it can set, and a field with no tag
// would become a flag named after itself.
type lsOptions struct {
	Long   bool   `getopt:"-l          the columns: kind, date, id and path"`
	Deep   bool   `getopt:"-r          descend into the folders below"`
	ByTime bool   `getopt:"-t          newest first, rather than by name"`
	Exact  bool   `getopt:"-T          the time of day as well as the date"`
	In     string `getopt:"--in=OBJECT what a rezzed object holds, rather than inventory"`
	Help   bool   `getopt:"--help -h   show what this command takes"`

	path string
	done bool
}

func readLsOptions(out io.Writer, args []string) (lsOptions, error) {
	var o lsOptions
	rest, done, err := subOptions("ls", "[PATH]", &o, out, args)
	if err != nil {
		return o, err
	}
	if o.done = done; done {
		return o, nil
	}
	if len(rest) > 1 {
		return o, fmt.Errorf("only one folder at a time")
	}
	if len(rest) == 1 {
		o.path = rest[0]
	}
	// The time is detail, and detail is what -l is, so asking for it
	// asks for the long form too.
	if o.Exact {
		o.Long = true
	}
	return o, nil
}

// lsWhen formats the date column.
//
// -T widens this column rather than adding one, and joins the time to
// the date with a T rather than a space, so that a listing has four
// columns whether or not the time was asked for: a script that reads
// the id out of the third field goes on working either way, which is
// the whole reason the listing is laid out in columns at all.
//
// A folder has no date, and an empty column would move every column
// after it, so it gets a dash.
func lsWhen(created int64, exact bool) string {
	layout := "2006-01-02"
	if exact {
		layout = "2006-01-02T15:04:05"
	}
	if created <= 0 {
		return "-"
	}
	return time.Unix(created, 0).Format(layout)
}

func cmdLs(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	o, err := readLsOptions(out, args)
	if err != nil || o.done {
		return err
	}
	long, deep, exact, path := o.Long, o.Deep, o.Exact, o.path

	// An object's contents are somewhere else entirely, with ids of
	// their own; see inside.go.
	if o.In != "" {
		if path != "" {
			return fmt.Errorf("ls --in lists one object, and takes no path")
		}
		return sh.listInside(ctx, out, o.In, long)
	}

	names, id, err := sh.resolveDir(ctx, path)
	if err != nil {
		return err
	}
	var depth uint
	if deep {
		depth = 4
	}
	es, err := sh.s.ListFolder(ctx, id, depth)
	if err != nil {
		return err
	}

	if o.ByTime {
		sortByTime(es)
	}

	prefix := "/" + sl.JoinPath(names...)
	if len(names) == 0 {
		prefix = ""
	}
	for _, e := range es {
		full := prefix + "/" + e.Path
		if !long {
			// One bare path per line, so a listing can be edited
			// into commands that take paths.
			fmt.Fprintln(out, full)
			continue
		}
		width := 10
		if exact {
			width = 19
		}
		fmt.Fprintf(out, "%-10s %-*s %-36s %s\n",
			kindOf(e), width, lsWhen(e.Created, exact), e.ID, full)
	}
	return nil
}

// sortByTime puts a listing newest first, and things made in the same
// second by name.
//
// This is a flat order and not a tree one: it is asked for to see what
// was made recently, and grouping by folder would bury a thing made a
// minute ago under whichever folder it happens to live in.  Without it
// a listing is in tree order, siblings by name.
//
// A folder has no date and sorts as the oldest thing there is, which
// puts folders at the end where they are out of the way.
func sortByTime(es []sl.Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.Created != b.Created {
			return a.Created > b.Created
		}
		if x, y := strings.ToLower(a.Name), strings.ToLower(b.Name); x != y {
			return x < y
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		// So that the same folder listed twice reads the same twice.
		return bytes.Compare(a.ID[:], b.ID[:]) < 0
	})
}

// kindOf names what an entry is, in a word.
func kindOf(e sl.Entry) string {
	if e.Folder {
		return "folder"
	}
	if e.IsLink {
		return "link"
	}
	switch sl.AssetType(e.Type) {
	case sl.AssetTexture:
		return "texture"
	case sl.AssetSound, sl.AssetSoundWAV:
		return "sound"
	case sl.AssetLandmark:
		return "landmark"
	case sl.AssetClothing:
		return "clothing"
	case sl.AssetObject:
		return "object"
	case sl.AssetNotecard:
		return "notecard"
	case sl.AssetLSLText:
		return "script"
	case sl.AssetBodypart:
		return "bodypart"
	case sl.AssetAnimation:
		return "animation"
	case sl.AssetGesture:
		return "gesture"
	case sl.AssetMesh:
		return "mesh"
	case sl.AssetSettings:
		return "settings"
	case sl.AssetMaterial:
		return "material"
	}
	return fmt.Sprintf("type%d", e.Type)
}

// catReadable says whether cat can read an entry, and why not when it
// cannot.
//
// What it does NOT look at is the asset id, and that is the point. The
// grid leaves a script's asset id out of an inventory listing -- it
// sends all zeroes, whatever the permissions say -- and an asset is
// asked for by ITEM anyway, with the simulator resolving it. Refusing
// an entry for having no asset id therefore refused every script there
// is, which is what this used to do.
func catReadable(e sl.Entry) error {
	if e.Folder {
		return fmt.Errorf("%q is a folder", e.Name)
	}
	// Notecards and scripts come over the transfer protocol; the
	// content delivery network refuses them. Anything it does serve is
	// not text, so this only offers the ones that are.
	switch sl.AssetType(e.Type) {
	case sl.AssetNotecard, sl.AssetLSLText, sl.AssetScriptLegacy:
		return nil
	}
	return fmt.Errorf("%s is not text; asset fetches it", kindOf(e))
}

func cmdCat(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("cat", "PATH", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: cat PATH")
	}
	e, err := sh.entryAt(ctx, args[0])
	if err != nil {
		return err
	}
	if err := catReadable(e); err != nil {
		return err
	}

	b, err := sh.s.ReadAsset(ctx, client.AssetRef{
		Owner: sh.s.Me(), Item: e.ID, Asset: e.Asset, Type: int32(e.Type),
	}, 45*time.Second)
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

// notecardText pulls the text out of the notecard container.
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

func cmdMkdir(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("mkdir", "PATH", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: mkdir PATH")
	}
	names := sl.SplitPath(args[0])
	if len(names) == 0 {
		return fmt.Errorf("no name given")
	}
	parentPath := strings.Join([]string{}, "")
	if strings.HasPrefix(args[0], "/") {
		parentPath = "/"
	}
	parentPath += sl.JoinPath(names[:len(names)-1]...)

	_, parent, err := sh.resolveDir(ctx, parentPath)
	if err != nil {
		return err
	}
	id, err := sh.s.CreateFolder(ctx, parent, names[len(names)-1])
	if err != nil {
		return err
	}
	fmt.Fprintln(out, id)
	return nil
}

type mvOptions struct {
	In   string `getopt:"--in=OBJECT  rename inside a rezzed object, not in inventory"`
	Help bool   `getopt:"--help -h    show what this command takes"`
}

func cmdMv(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o mvOptions
	args, done, err := subOptions("mv", "PATH DEST", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 2 {
		return fmt.Errorf("usage: mv PATH DEST")
	}
	// Inside an object there is nowhere to move to -- an object holds
	// no folders -- so this is a rename and nothing else.
	if o.In != "" {
		return sh.renameInside(ctx, out, o.In, args[0], args[1])
	}
	e, err := sh.entryAt(ctx, args[0])
	if err != nil {
		return err
	}

	// A destination that names a folder is a move; anything else is a
	// rename in place.  Both are one message for an item, which is
	// why moving and renaming at once is possible at all.
	if _, id, err := sh.resolveDir(ctx, args[1]); err == nil {
		if e.Folder {
			return sh.s.MoveFolder(ctx, e.ID, id)
		}
		return sh.s.MoveItem(ctx, e.ID, id)
	}

	dest := sl.SplitPath(args[1])
	if len(dest) != 1 {
		return fmt.Errorf("no folder %q, and %q is not a plain name to rename to", args[1], args[1])
	}
	if e.Folder {
		return sh.s.RenameFolder(ctx, e.ID, dest[0])
	}
	_, err = sh.s.SetItem(ctx, e.ID, dest[0], "", nil)
	return err
}

// allItems is the path that means every item in this folder.
//
// It is not a glob: the shell has no pattern matching, and this is the
// one pattern rm needs.  Folders are never included -- emptying a
// folder of its items is a thing to want, and taking its subfolders
// with them is not.
//
// An item may be named "*", since the grid allows nearly any printable
// character in a name.  Such an item cannot be named at a prompt any
// more; its id still names it, which is what ls -l prints ids for.
const allItems = "*"

// rmOptions is what rm was asked for.
type rmOptions struct {
	AllCopies bool   `getopt:"--remove-all-copies  delete everything of that name, not just the first"`
	In        string `getopt:"--in=OBJECT          delete from inside a rezzed object, not from inventory"`
	Help      bool   `getopt:"--help -h            show what this command takes"`
}

func cmdRm(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o rmOptions
	args, done, err := subOptions("rm", "PATH ...", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: rm [--remove-all-copies] PATH...")
	}
	if o.In != "" {
		return sh.removeInside(ctx, out, o.In, args)
	}
	for _, path := range args {
		// One name, and a folder may hold a dozen things wearing it.
		// Without the flag rm takes the first, which is the careful
		// thing to do by default: deleting is permanent here, and a
		// path that turns out to mean twelve items is not something to
		// discover afterwards.
		var targets []sl.Entry
		switch {
		case path == allItems:
			// Refused rather than guessed at.  Taking it to mean one
			// arbitrary item would be surprising, and taking it to mean
			// all of them without being asked is not something to
			// discover after the fact.
			if !o.AllCopies {
				return fmt.Errorf("%s means every item in this folder: "+
					"say rm --remove-all-copies %s if that is what you want", allItems, allItems)
			}
			targets, err = sh.itemsHere(ctx)
		case o.AllCopies:
			targets, err = sh.entriesAt(ctx, path)
		default:
			var e sl.Entry
			if e, err = sh.entryAt(ctx, path); err == nil {
				targets = []sl.Entry{e}
			}
		}
		if err != nil {
			return err
		}

		// What they are called, which is not "copies" when the path
		// was * and they only have being here in common.
		noun := "copies"
		if path == allItems {
			noun = "items"
		}
		// Six hundred deletions is six hundred round trips and takes
		// minutes.  Counting up in place says it is working and roughly
		// how much longer, which the alternative -- a silent terminal
		// and then one line -- does not.  It goes to the terminal and
		// not to out: it is not output, and a redirect must catch the
		// result rather than a flickering count.
		many := len(targets) > 1
		for i, e := range targets {
			if e.Folder {
				if err := sh.s.DeleteFolder(ctx, e.ID); err != nil {
					sh.term.Status("")
					return fmt.Errorf("%s: %w", path, err)
				}
			} else if err := sh.s.DeleteItem(ctx, e.ID); err != nil {
				sh.term.Status("")
				return fmt.Errorf("%s: %w", path, err)
			}
			if many {
				sh.term.Status(fmt.Sprintf("removed %d of %d %s", i+1, len(targets), noun))
			}
		}
		// Say so when one path meant many things: the count is the
		// only evidence that the flag did what was wanted.
		if many {
			sh.term.Status("")
			fmt.Fprintf(out, "%s: removed %d %s\n", path, len(targets), noun)
		}
	}
	return nil
}

// itemsHere is every item in the working folder, and no folders.
func (sh *Shell) itemsHere(ctx context.Context) ([]sl.Entry, error) {
	sh.mu.Lock()
	cwd := sh.cwdID
	sh.mu.Unlock()

	es, err := sh.s.ListFolder(ctx, cwd, 0)
	if err != nil {
		return nil, err
	}
	var items []sl.Entry
	for _, e := range es {
		if !e.Folder {
			items = append(items, e)
		}
	}
	return items, nil
}

// emptyTrashOptions is what emptytrash was asked for, which is nothing
// but the usual --help.
type emptyTrashOptions struct {
	Help bool `getopt:"--help -h  show what this command takes"`
}

func cmdEmptyTrash(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o emptyTrashOptions
	rest, done, err := subOptions("emptytrash", "", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("usage: emptytrash")
	}

	trash, err := sh.s.TrashFolder(ctx)
	if err != nil {
		return err
	}
	// Counted first, because the count is the only report there will
	// be: afterwards there is nothing left to count.
	es, err := sh.s.ListFolder(ctx, trash, 0)
	if err != nil {
		return err
	}
	if len(es) == 0 {
		fmt.Fprintln(out, "the trash is already empty")
		return nil
	}
	if err := sh.s.PurgeFolder(ctx, trash); err != nil {
		return err
	}
	fmt.Fprintf(out, "emptied the trash: %d\n", len(es))
	return nil
}

func cmdFind(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("find", "TEXT [PATH]", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: find TEXT [PATH]")
	}
	want := strings.ToLower(args[0])
	path := ""
	if len(args) == 2 {
		path = args[1]
	}
	names, id, err := sh.resolveDir(ctx, path)
	if err != nil {
		return err
	}
	es, err := sh.s.ListFolder(ctx, id, 4)
	if err != nil {
		return err
	}

	prefix := "/" + sl.JoinPath(names...)
	if len(names) == 0 {
		prefix = ""
	}
	for _, e := range es {
		if strings.Contains(strings.ToLower(e.Name), want) {
			fmt.Fprintln(out, prefix+"/"+e.Path)
		}
	}
	return nil
}
