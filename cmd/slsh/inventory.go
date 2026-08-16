package main

// Inventory as a filesystem.
//
// The tree really is a tree, so cd and ls are the right interface for
// it, and the paths they print are the paths they take: a listing can
// be written to a file, edited into a list of moves, and run.  That is
// why ls prints one bare path per line by default and keeps the columns
// for ls -l.
//
// # save, and why it is two plain arguments
//
// Reading an item out has had commands for a long time -- cat prints a
// notecard or a script, get writes a texture to disk -- and so has
// making a new one, which is "new --from FILE PATH".  Writing a file
// into an item that is ALREADY there had none, although the session
// layer has done it all along: sl.SaveNotecard and sl.SaveScript were
// reachable from "new" and from nothing at all respectively.
//
//	save notes.txt readme            a notecard
//	save hello.lsl /Scripts/greeter  a script, which is compiled
//
// The word is the viewer's.  These are the two capabilities behind its
// own Save button -- LLPreviewLSL::saveIfNeeded asks the region for
// UpdateScriptAgent (llpreviewscript.cpp:2569) and
// LLPreviewNotecard::saveIfNeeded for UpdateNotecardAgentInventory
// (llpreviewnotecard.cpp:674) -- so "save" is what somebody who has used
// the viewer already calls this.  "put" was not free to take: it means
// uploading an image, which costs L$ where a notecard and a script cost
// nothing, and one word for both would hide that.
//
// Two positional arguments, and not "save --from FILE PATH" -- which
// would have matched "new --from FILE PATH" word for word, and was the
// other real candidate.  What differs is that new can make an empty
// notecard and save cannot write one: the file is the whole of what
// this command does, and an option that must always be given is a
// positional argument spelled at length.  That is the objection that
// kept the object out of a flag in start and stop.  It is also a trap:
// a --from that may be left off makes "save readme" a legal line that
// empties a notecard, and nothing would have been asked for.
//
// So the source is first and the destination second, in cp's order, and
// which side is which is the shell's own vocabulary rather than a
// convention invented here.  FILE is on this machine wherever it
// appears -- put FILE, . FILE, --from FILE -- and PATH is in inventory
// wherever it appears -- cat PATH, rm PATH, drop OBJECT PATH.  "get -o
// FILE PATH" reads the other way round because its subject is the thing
// on the grid and the file is only where the copy lands; here the file
// is the subject and the item is where it lands.
//
// # What save does not do
//
// It does not start anything, and does not touch the world.  A script
// in inventory is not running and cannot be made to run: an object is
// the only place a script runs at all, and putting one there is "new
// --in OBJECT", which compiles it inside the object and starts it (see
// sl.InstallScript).  Saving compiles too -- the capability answers
// with the verdict -- but what it has changed is the item, and the
// copies already inside objects are untouched.  So the output says
// whether it compiled and says nothing whatever about running, and
// there is no --in here: a second way into an object would be a second
// thing to keep right.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var inventoryCommands = map[string]*command{
	"pwd": {
		flags: func() any { return new(helpOnly) },
		brief: "where in inventory we are",
		run: func(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
			var flags helpOnly
			if _, done, err := subOptions("pwd", &flags, out, args); err != nil || done {
				return err
			}
			fmt.Fprintln(out, sh.Pwd())
			return nil
		},
	},
	"cd": {
		params: "[PATH]",
		flags:  func() any { return new(helpOnly) },
		brief:  "change folder; no path goes to the root, .. goes up",
		run:    cmdCd,
	},
	"ls": {
		params: "[PATH]",
		flags:  func() any { return new(lsOptions) },
		brief:  "list a folder; -l for detail, -T the time as well, -t newest first, -r to descend",
		run:    cmdLs,
	},
	"cat": {
		params: "PATH",
		flags:  func() any { return new(helpOnly) },
		brief:  "print a notecard or a script",
		run:    cmdCat,
	},
	"save": {
		params: "FILE PATH",
		flags:  func() any { return new(helpOnly) },
		brief:  "write a local file into a notecard or script that is already there",
		run:    cmdSave,
	},
	"mkdir": {
		params: "PATH",
		flags:  func() any { return new(helpOnly) },
		brief:  "make a folder",
		run:    cmdMkdir,
	},
	"mv": {
		params: "PATH DEST",
		flags:  func() any { return new(mvOptions) },
		brief:  "move into a folder, or rename if DEST is a plain name; folders too",
		run:    cmdMv,
	},
	"rm": {
		params: "PATH ...",
		flags:  func() any { return new(rmOptions) },
		brief:  "delete items, permanently; --remove-all-copies for every one of a name",
		run:    cmdRm,
	},
	"emptytrash": {
		flags: func() any { return new(emptyTrashOptions) },
		brief: "throw away everything in the trash, permanently",
		run:   cmdEmptyTrash,
	},
	"find": {
		params: "TEXT [PATH]",
		flags:  func() any { return new(helpOnly) },
		brief:  "look for names containing TEXT, from here down",
		run:    cmdFind,
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
	args, done, err := subOptions("cd", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) > 1 {
		return usageError("cd")
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
	rest, done, err := subOptions("ls", &o, out, args)
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

// aKind is a kind with its article, since "a object" in a refusal reads
// as a bug in the refusal rather than as an answer.
func aKind(kind string) string {
	if kind != "" && strings.ContainsRune("aeiou", rune(kind[0])) {
		return "an " + kind
	}
	return "a " + kind
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
	args, done, err := subOptions("cat", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 1 {
		return usageError("cat")
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

// saveTarget says which of the two calls writes to an entry, and why
// neither does when neither does.
//
// The item's own type decides, because the item is already there and
// its type is settled: a notecard takes UpdateNotecardAgentInventory
// and a script takes UpdateScriptAgent.  A flag saying which would be
// asking somebody to repeat what inventory already knows, and to be
// wrong about it half as often as they are right.
//
// What it does NOT look at is the asset id, for the reason catReadable
// gives: the grid leaves a script's asset id out of a listing, so an
// entry with no asset is a script all the same.
//
// A script of the long dead pre-LSL2 kind is written as a script,
// which is what cat does with one too.  Nobody has one to try it on;
// refusing would mean telling somebody that their script is not a
// script, and the capability has the last word either way.
func saveTarget(e sl.Entry) (sl.AssetType, error) {
	// A folder's Type is its preferred contents, so without this a
	// folder reports itself as whatever it likes to hold -- get has the
	// same check for the same reason.
	if e.Folder {
		return 0, fmt.Errorf("%s is a folder, and save writes one notecard or script", e.Name)
	}
	// A link carries the type of what it points at, so it would
	// otherwise look exactly like the notecard it names -- and the
	// write would go to the link's own item id, which is not where the
	// text lives.
	if e.IsLink {
		return 0, fmt.Errorf("%s is a link and not the item itself; save writes the notecard "+
			"or script the link points at, so give it that path", e.Name)
	}
	switch t := sl.AssetType(e.Type); t {
	case sl.AssetNotecard:
		return t, nil
	case sl.AssetLSLText, sl.AssetScriptLegacy:
		return sl.AssetLSLText, nil
	case sl.AssetTexture:
		return 0, fmt.Errorf("%s is a texture; save writes a notecard or a script, "+
			"and an image goes up with \"put\"", e.Name)
	}
	return 0, fmt.Errorf("%s is %s; save writes a notecard or a script, and nothing here uploads %s",
		e.Name, aKind(kindOf(e)), aKind(kindOf(e)))
}

// cmdSave writes a file into a notecard or a script that already
// exists.  The direction is the one thing to be sure of: the file is
// read and the item is written, which is the opposite of cat and get.
func cmdSave(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("save", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 2 {
		return usageError("save")
	}

	// The file first, and the path takes the rest of the line as drop's
	// does: a name with spaces in it is ordinary in inventory and rare
	// on disk, so the quoting falls where it is least often needed.
	//
	// Read before anything is looked up, so that a filename that is not
	// there costs nothing and changes nothing -- and read as it stands.
	// new puts a skeleton in a script with no source, because it is
	// making one and a script with none faults the moment anything runs
	// it; this is writing what somebody pointed at, and inventing source
	// they did not ask for would be worse than the compile error they
	// are about to be shown.
	body, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	e, err := sh.entryAt(ctx, strings.Join(args[1:], " "))
	if err != nil {
		return err
	}
	kind, err := saveTarget(e)
	if err != nil {
		return err
	}

	var res *sl.UploadResult
	switch kind {
	case sl.AssetNotecard:
		res, err = sh.s.SaveNotecard(ctx, e.ID, string(body))
	default:
		res, err = sh.s.SaveScript(ctx, e.ID, string(body))
	}
	if err != nil {
		return err
	}

	// The file's own size, not the asset's: a notecard goes up wrapped
	// in a container nobody asked about, and the number worth printing
	// is the one that matches the file on disk.
	//
	// The verdict is a clause on the same line and never a failure,
	// because the item WAS written either way -- the save succeeds and
	// the compile fails, in that order -- and a command that returned an
	// error would say the opposite.  cmdNew and newInside report a
	// compile the same way for the same reason.
	line := fmt.Sprintf("%s: %d bytes written", e.Name, len(body))
	if kind != sl.AssetNotecard && res != nil {
		verdict := ", and it compiled"
		if !res.Compiled {
			verdict = ", and it did not compile"
		}
		line += verdict
	}
	fmt.Fprintln(out, line)
	if res == nil {
		return nil
	}
	// The compiler reports the first error and stops, so this is one
	// line and worth printing whole.
	for _, complaint := range res.Errors {
		fmt.Fprintf(out, "  %s\n", complaint)
	}
	// What the capability says when it is unhappy without failing.  It
	// is the only explanation there will be, so it is printed rather
	// than left in the reply nobody sees.
	if res.Message != "" {
		fmt.Fprintf(out, "  %s\n", res.Message)
	}
	return nil
}

func cmdMkdir(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("mkdir", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 1 {
		return usageError("mkdir")
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
	args, done, err := subOptions("mv", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 2 {
		return usageError("mv")
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
	args, done, err := subOptions("rm", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) == 0 {
		return usageError("rm")
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
	rest, done, err := subOptions("emptytrash", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) > 0 {
		return usageError("emptytrash")
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
	args, done, err := subOptions("find", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 || len(args) > 2 {
		return usageError("find")
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
