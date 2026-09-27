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
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var inventoryCommands = map[string]*command{
	"pwd": {
		flags:    func() any { return new(helpOnly) },
		brief:    "where in inventory we are",
		keywords: "current folder directory where am in inventory path",
		man:      "pwd",
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
		params:   "[PATH]",
		flags:    func() any { return new(helpOnly) },
		brief:    "change folder; no path goes to the root, .. goes up",
		keywords: "change folder directory enter go into inventory navigate browse up",
		man:      "cd",
		run:      cmdCd,
	},
	"ls": {
		params:   "[PATH]",
		flags:    func() any { return new(lsOptions) },
		brief:    "list a folder, or what a path names; -l for detail, -t newest first",
		keywords: "list show folder contents inventory directory items what have browse",
		man:      "ls",
		run:      cmdLs,
	},
	"cat": {
		params:   "PATH",
		flags:    func() any { return new(catOptions) },
		brief:    "print a notecard or a script; --in reads one inside a rezzed object",
		keywords: "read view show print display contents text source code script notecard lsl inside object download to disk file",
		man:      "cat",
		run:      cmdCat,
	},
	"save": {
		params:   "FILE PATH",
		flags:    func() any { return new(helpOnly) },
		brief:    "write a local file into a notecard or script that is already there",
		keywords: "upload write edit update notecard script local file disk contents replace",
		man:      "save",
		run:      cmdSave,
	},
	"mkdir": {
		params:   "PATH",
		flags:    func() any { return new(helpOnly) },
		brief:    "make a folder",
		keywords: "create make new folder directory",
		man:      "mkdir",
		run:      cmdMkdir,
	},
	"mv": {
		params:   "PATH DEST",
		flags:    func() any { return new(mvOptions) },
		brief:    "move into a folder, or rename if DEST is a plain name; folders too",
		keywords: "move rename folder item organise organize relocate inventory",
		man:      "mv",
		run:      cmdMv,
	},
	"rm": {
		params:   "PATH ...",
		flags:    func() any { return new(rmOptions) },
		brief:    "delete items, permanently; a name that means several is refused",
		keywords: "delete remove erase destroy item folder inventory permanently",
		man:      "rm",
		run:      cmdRm,
	},
	"emptytrash": {
		flags:    func() any { return new(emptyTrashOptions) },
		brief:    "throw away everything in the trash, permanently",
		keywords: "empty trash delete purge permanently clear deleted items",
		man:      "emptytrash",
		run:      cmdEmptyTrash,
	},
	"find": {
		params:   "TEXT [PATH]",
		flags:    func() any { return new(findOptions) },
		brief:    "look for names containing TEXT, from here down",
		keywords: "search locate look for item name inventory folder path where is",
		man:      "find",
		run:      cmdFind,
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

// folderAt resolves a list of names to a folder id, each name picking
// out one folder through sl.PickNamed.
func (sh *Shell) folderAt(ctx context.Context, names []string) (msg.UUID, error) {
	if len(names) == 0 {
		return sh.s.InventoryRoot(), nil
	}
	parent := names[:len(names)-1]
	es, err := sh.s.ListInventory(ctx, sl.JoinPath(parent...), 0)
	if err != nil {
		return msg.UUID{}, err
	}
	var folders []sl.Entry
	for _, e := range es {
		if e.Folder {
			folders = append(folders, e)
		}
	}
	f, err := sl.PickNamed(folders, names[len(names)-1], "folder", sh.inFolder(parent))
	if err != nil {
		return msg.UUID{}, err
	}
	return f.ID, nil
}

// inFolder is a folder as a refusal names it: "here" for the one the
// shell is in, and its path otherwise.
func (sh *Shell) inFolder(names []string) string {
	sh.mu.Lock()
	here := slices.Equal(names, sh.cwd)
	sh.mu.Unlock()
	if here {
		return "here"
	}
	return "in /" + sl.JoinPath(names...)
}

// entryAt finds one entry, by path or by id, folder or item.
//
// A path has to mean one thing, whatever the command is about to do
// with it: a name that means several is refused through sl.PickNamed,
// listing their ids.  Reading is not let off, because a person is not
// always there to take a second look -- "cat notes > file" in a script
// would write the wrong notecard to disk and say nothing.  ls -l prints
// the id beside the path, and an id is taken wherever a path is.
//
// The index needs no length check, and the reason is one level down:
// entriesIn returns an error for every path that matches nothing, so a
// nil error here carries at least one entry.  Checked rather than
// assumed, because the shape invites the assumption.
func (sh *Shell) entryAt(ctx context.Context, path string) (sl.Entry, error) {
	in, es, err := sh.entriesIn(ctx, path)
	if err != nil {
		return sl.Entry{}, err
	}
	return sl.PickNamed(es, es[0].Name, "", sh.inFolder(in))
}

// entriesAt is entryAt for everything the path names, in listing order,
// matched exactly through sl.AllNamed.
//
// A path can name more than one thing, since a folder may hold a dozen
// items called exactly the same thing -- which is what rm --newest,
// --oldest and --remove-all-copies are for, and what rm refuses without
// one of them.  An id names exactly one, so that form returns the one.
func (sh *Shell) entriesAt(ctx context.Context, path string) ([]sl.Entry, error) {
	_, es, err := sh.entriesIn(ctx, path)
	return es, err
}

// entriesIn is entriesAt, and says as well which folder the things it
// found are in, as the names that lead to it.
//
// That is for ls, which prints a whole path for each line and so needs
// to know what to put in front of the names: a listing of four items
// of one name is four paths that differ only in the id and the
// date beside them, and a path that is short by its folder would not be
// one that could be typed back in.
//
// Each entry's Path is its own name and nothing more, so that the
// caller can put the folder in front of it and get the same shape it
// gets from listing a folder.  The id form is normalised to that too,
// since what it finds carries a path from wherever it was found.
func (sh *Shell) entriesIn(ctx context.Context, path string) ([]string, []sl.Entry, error) {
	if id, err := msg.ParseUUID(strings.TrimSpace(path)); err == nil {
		e, in, err := sh.entryByID(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		e.Path = sl.EscapeName(e.Name)
		return in, []sl.Entry{e}, nil
	}
	names := sl.SplitPath(path)
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("no path given")
	}
	dir := strings.Join([]string{}, "")
	if strings.HasPrefix(path, "/") {
		dir = "/"
	}
	dir += sl.JoinPath(names[:len(names)-1]...)

	in, id, err := sh.resolveDir(ctx, dir)
	if err != nil {
		return nil, nil, err
	}
	es, err := sh.s.ListFolder(ctx, id, 0)
	if err != nil {
		return nil, nil, err
	}
	found, err := sl.AllNamed(es, names[len(names)-1], "", sh.inFolder(in))
	if err != nil {
		return nil, nil, err
	}
	return in, found, nil
}

// entryByID looks for an id here, then four levels down from the root,
// and says which folder it was found in as the names that lead to it.
//
// Here first because that is nearly always where it is, and the whole
// tree is a hundred requests.  The two searches know where they looked
// in different ways: this folder is the one the shell is in, and the
// listing from the root carries each entry's path, whose last name is
// the entry's own.
func (sh *Shell) entryByID(ctx context.Context, id msg.UUID) (sl.Entry, []string, error) {
	sh.mu.Lock()
	cwd, here := sh.cwdID, append([]string(nil), sh.cwd...)
	sh.mu.Unlock()

	if es, err := sh.s.ListFolder(ctx, cwd, 0); err == nil {
		for _, e := range es {
			if e.ID == id {
				return e, here, nil
			}
		}
	}
	es, err := sh.s.ListInventory(ctx, "", 4)
	if err != nil {
		return sl.Entry{}, nil, err
	}
	for _, e := range es {
		if e.ID == id {
			at := sl.SplitPath(e.Path)
			return e, at[:len(at)-1], nil
		}
	}
	return sl.Entry{}, nil, fmt.Errorf("nothing here has the id %s", id)
}

// thingAt is what a path names, followed through a link if it is one.
//
// This and entryAt are the two ways to resolve a path, and which one a
// command wants turns on a single question: is the id about to be sent
// to the grid as a reference to the THING, or is the inventory entry
// itself what is being operated on?
//
// wear, place, drop and give are all the first.  The grid has no idea
// what a link is -- an id it has no object for is answered with silence
// -- so a link's own id sent to it can only fail, and fail in the worst
// way, without a word.  Those commands want thingAt.
//
// rm, mv and cp are the second, and must NOT follow a link.  Deleting a
// link is deleting the link; a link resolved on the way into rm would
// delete the item at the other end of it and leave every other link to
// that item pointing at nothing.  Renaming one renames the link.  Those
// commands want entryAt, and the line between the two is the reason
// this is not simply folded into entryAt for everybody.
//
// ls and find do not follow a link either, for a third reason: a link
// is a thing a listing should show, since the word "link" in the type
// column is the only way anyone can tell one from what it points at.
// Their -L shows what it points at in the columns, on the link's path.
//
// A folder comes back untouched, so a command that refuses folders can
// go on refusing them afterwards.
func (sh *Shell) thingAt(ctx context.Context, path string) (sl.Entry, error) {
	e, err := sh.entryAt(ctx, path)
	if err != nil {
		return sl.Entry{}, err
	}
	return sh.linkTarget(ctx, e)
}

// linkTarget is the entry a link points at, and the entry itself when
// it is not a link.
//
// A link's id is not the id of the thing it names, and its "asset" is
// not an asset: it is the ITEM id of what it points at.  That is how
// the grid delivers one -- a link arrives carrying linked_id where an
// item carries asset_id (agent/inventory.go) -- and how the viewer
// reads one back, by looking the uuid up in inventory rather than
// fetching it (LLViewerInventoryItem::getLinkedItem,
// llviewerinventory.cpp:2674).
//
// Which matters because an outfit folder holds nothing else.  Every
// path under /My Outfits names a link, and links are what a person has
// in front of them when they are reading off the name of something to
// put on.  A command that took the id it found there and sent it would
// be sending an id the simulator has no object for, and the simulator
// answers an id it does not know with silence rather than an error --
// so the whole of what a person sees is their command sitting out its
// timeout and then saying the region never agreed.  Nothing in that
// sentence is true except the last clause, and the thing that went
// wrong is not mentioned anywhere in it.
func (sh *Shell) linkTarget(ctx context.Context, e sl.Entry) (sl.Entry, error) {
	if !e.IsLink {
		return e, nil
	}
	if e.Asset.IsZero() {
		return sl.Entry{}, fmt.Errorf("%s is a link that does not say what it points at; "+
			"name the item itself", e.Name)
	}
	to, _, err := sh.entryByID(ctx, e.Asset)
	if err != nil {
		return sl.Entry{}, fmt.Errorf("%s is a link to the item %s, and %w -- a link outlives "+
			"what it pointed at, so a link whose item has been deleted reads exactly like "+
			"this", e.Name, e.Asset, err)
	}
	// A link to a link is what the viewer warns about and declines to
	// resolve (llviewerinventory.cpp:2678-2683), and following it would
	// mean choosing how many times to follow.
	if to.IsLink {
		return sl.Entry{}, fmt.Errorf("%s is a link to another link; name the item itself", e.Name)
	}
	return to, nil
}

// itemAt is the whole inventory item an entry names, fetched from the
// folder the entry was listed in.
//
// Matched on the id and not the name it was found by, because a folder
// may hold a dozen items called one thing -- the case rm --newest and
// --oldest exist for -- and a lookup by name would answer with
// whichever of them came back first.  The entry already carries the id
// that settles it.
func (sh *Shell) itemAt(ctx context.Context, e sl.Entry) (*sl.Item, error) {
	items, err := sh.s.FolderItems(ctx, e.Parent)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.ID == e.ID {
			return it, nil
		}
	}
	return nil, fmt.Errorf("%s is no longer in the folder it was listed in", e.Name)
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
	Long   bool   `getopt:"-l          the columns: kind, when it was acquired, id and path"`
	Follow bool   `getopt:"-L          the columns, with a link shown as the item it points at"`
	Deep   bool   `getopt:"-r          descend into the folders below"`
	ByTime bool   `getopt:"-t          newest first, rather than by name"`
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
	return o, nil
}

// longFormat is how a long listing renders its rows: whether -L was
// asked for, and the index to follow links with.
//
// Shared by ls and find rather than written twice.  The columns are the
// only way to tell a link from what it points at, or one of four items
// of a name from the other three, so two commands printing them in two
// shapes would be two things to learn for one answer -- and the id
// column is there to be copied into another command, which only works
// if it lands in the same place every time.
type longFormat struct {
	// follow is -L: show what a link points at rather than the link.
	follow bool

	// byID is every entry four levels down from the root, for following
	// those links.  Nil when nothing in the listing is a link, and nil
	// when the walk that would have built it failed.
	byID map[msg.UUID]sl.Entry
}

// line writes one row: kind, when it was acquired, id, and the whole
// path.
//
// Under -L the id column is the id of the THING, whether or not its
// kind could be found out -- a link names what it points at already,
// and that much needs no lookup at all.  Where the item itself could
// not be reached the kind stays "link", which says exactly what
// happened: this is a link, and following it got nowhere.  The path is
// always where the entry was found, since that is what was listed.
func (f longFormat) line(out io.Writer, e sl.Entry, full string) {
	kind, when, id := kindOf(e), lsWhen(e.Created), e.ID
	if f.follow && e.IsLink && !e.Asset.IsZero() {
		id = e.Asset
		if to, ok := f.byID[e.Asset]; ok {
			kind, when = kindOf(to), lsWhen(to.Created)
		}
	}
	fmt.Fprintf(out, "%-10s %-19s %-36s %s\n", kind, when, id, full)
}

// longFormatFor builds the format one listing wants.
//
// One walk of inventory, four levels down from the root, for the lot,
// and only when the listing holds a link at all.  A Current Outfit
// folder is a dozen or more links, and a lookup apiece would be a dozen
// walks of the tree to answer one listing.
//
// A walk that fails is not an error here.  What -L asks for is the kind
// and the id; the id is on the link already, so a failed walk still
// answers most of the question, and refusing a whole listing because
// one extra read did not come back would be worse than a column that
// says "link".
func (sh *Shell) longFormatFor(ctx context.Context, follow bool, es []sl.Entry) longFormat {
	f := longFormat{follow: follow}
	if !follow {
		return f
	}
	any := false
	for _, e := range es {
		if e.IsLink && !e.Asset.IsZero() {
			any = true
			break
		}
	}
	if !any {
		return f
	}
	all, err := sh.s.ListInventory(ctx, "", 4)
	if err != nil {
		return f
	}
	f.byID = make(map[msg.UUID]sl.Entry, len(all))
	for _, e := range all {
		f.byID[e.ID] = e
	}
	return f
}

// lsWhen formats the date column.
//
// It is the whole date: the day something was acquired and the time of
// day as well, joined by a T rather than a space so that the column
// stays one field and a script reading the id out of the third one goes
// on working.  The seconds are there because they settle things -- two
// items of one name, made a minute apart, are told apart by this column
// and by nothing else on the line except the id, and rm --newest picks
// between them by exactly this number.
//
// A folder has no date, and an empty column would move every column
// after it, so it gets a dash.
func lsWhen(created int64) string {
	if created <= 0 {
		return "-"
	}
	return time.Unix(created, 0).Format("2006-01-02T15:04:05")
}

func cmdLs(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	o, err := readLsOptions(out, args)
	if err != nil || o.done {
		return err
	}
	// -L is a long listing whose links are followed, so it implies -l.
	// Asking to see what the links point at and getting bare paths back
	// would be the flag doing nothing at all.
	long, deep, path := o.Long || o.Follow, o.Deep, o.path

	// An object's contents are somewhere else entirely, with ids of
	// their own; see inside.go.
	if o.In != "" {
		if path != "" {
			return fmt.Errorf("ls --in lists one object, and takes no path")
		}
		return sh.listInside(ctx, out, o.In, long)
	}

	names, es, err := sh.toList(ctx, path, deep)
	if err != nil {
		return err
	}

	if o.ByTime {
		sortByTime(es)
	}

	format := sh.longFormatFor(ctx, o.Follow, es)
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
		format.line(out, e, full)
	}
	return nil
}

// toList is what a path given to ls means: a folder's contents where it
// names a folder, and the things it names where it does not.
//
// A path that names items is the answer to a question the columns
// otherwise refuse to answer.  Names are not unique, so /Scripts holding
// four things of one name lists as four lines that differ only in
// the id and the date -- and there was no way to ask about just those
// four without reading a whole folder and picking them out by eye.  It
// is also how ls of a file reads in any shell, and it is what rm's
// refusal sends a person to look at.
//
// A folder wins where a folder and an item share a name, since that is
// the older meaning of the two and the one cd agrees with; the item can
// still be named by its id.  The refusal from the folder attempt is
// dropped rather than reported, because "no folder" is not what went
// wrong when the name was never meant to be one.  Two folders of one
// name land here too, and are listed as themselves: the path cannot say
// which of them to open, and their ids are what can.
func (sh *Shell) toList(ctx context.Context, path string, deep bool) ([]string, []sl.Entry, error) {
	names, id, err := sh.resolveDir(ctx, path)
	if err != nil {
		return sh.entriesIn(ctx, path)
	}
	var depth uint
	if deep {
		depth = 4
	}
	es, err := sh.s.ListFolder(ctx, id, depth)
	if err != nil {
		return nil, nil, err
	}
	return names, es, nil
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
	// "get" is named only for a texture because get is only for
	// textures -- it refuses a sound itself -- so naming it for the
	// rest would forward the reader to a second refusal.
	kind := kindOf(e)
	if sl.AssetType(e.Type) == sl.AssetTexture {
		return fmt.Errorf("%s is not text; \"get\" saves one as a PNG", kind)
	}
	return fmt.Errorf("%s is not text, and no command here fetches one", kind)
}

type catOptions struct {
	In   string `getopt:"--in=OBJECT  read it from inside a rezzed object, not from inventory"`
	Help bool   `getopt:"--help -h    show what this command takes"`
}

func cmdCat(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o catOptions
	args, done, err := subOptions("cat", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 1 {
		return usageError("cat")
	}
	if o.In != "" {
		return sh.catInside(ctx, out, o.In, args[0])
	}
	// thingAt: what is read is the asset at the other end of a link,
	// the way double-clicking a notecard link opens the notecard.
	e, err := sh.thingAt(ctx, args[0])
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
		mayCopy := e.EveryoneMask&sl.PermCopy != 0 ||
			(e.Owner == sh.s.Me() && e.OwnerMask&sl.PermCopy != 0)
		return whyNotRead(err, e.Name, sl.AssetType(e.Type), mayCopy)
	}
	printText(out, sl.AssetType(e.Type), b)
	return nil
}

// catInside prints a notecard or a script from inside an object.
//
// The object's copy is read, not the item it was copied from: they part
// company the moment a script in the object is edited or a notecard in
// it is saved, and the copy is the one the object is running or
// reading.  It is asked for through the object, which is how the
// simulator decides whether this avatar may see it -- a script without
// modify rights for us is refused there, not here.
func (sh *Shell) catInside(ctx context.Context, out io.Writer, what, name string) error {
	obj, err := sh.insideObject(ctx, what, 0)
	if err != nil {
		return err
	}
	it, err := sh.findInside(ctx, obj, name)
	if err != nil {
		return err
	}
	var t sl.AssetType
	switch it.Type {
	case "notecard":
		t = sl.AssetNotecard
	case "lsltext", "lsl":
		t = sl.AssetLSLText
	case "script":
		t = sl.AssetScriptLegacy
	default:
		return fmt.Errorf("%q in %s is %s, not text", it.Name, obj.Name, aKind(it.Type))
	}
	b, err := sh.s.ReadTaskAsset(ctx, obj, it, int32(t), 45*time.Second)
	if err != nil {
		mayCopy := it.EveryoneMask&sl.PermCopy != 0 ||
			(it.OwnerID == sh.s.Me() && it.OwnerMask&sl.PermCopy != 0)
		return whyNotRead(err, it.Name, t, mayCopy)
	}
	printText(out, t, b)
	return nil
}

// whyNotRead says why the region would not hand over a notecard, when
// the reason is one a person can do nothing about by asking again.
//
// A notecard this avatar may not copy may not be read at all: the
// region refuses the transfer, and the viewer does not even ask, saying
// "You do not have permission to view this notecard" instead
// (llpreviewnotecard.cpp, loadAsset).  "insufficient permissions" on
// its own leaves somebody who has full rights over the notecard in
// every other respect wondering which permission is missing.  Only the
// region's refusal is explained, and only for that case: the region
// has the last word, and anything else it refuses for is passed on as
// it said it.
func whyNotRead(err error, name string, t sl.AssetType, mayCopy bool) error {
	if t != sl.AssetNotecard || mayCopy || !errors.Is(err, client.ErrTransferDenied) {
		return err
	}
	return fmt.Errorf("%q may not be copied, and a notecard that may not be copied may not be read either: %w",
		name, err)
}

// printText prints what cat read: a notecard without its wrapper, and a
// script as it is.
func printText(out io.Writer, t sl.AssetType, b []byte) {
	if t == sl.AssetNotecard {
		if text, ok := notecardText(b); ok {
			fmt.Fprintln(out, text)
			return
		}
	}
	fmt.Fprintln(out, strings.TrimRight(string(b), "\n"))
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
	// thingAt: what is written is the notecard or script at the other
	// end of a link.  This used to be a refusal that said to give the
	// real path instead, which was describing the work rather than
	// doing it.
	e, err := sh.thingAt(ctx, strings.Join(args[1:], " "))
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

	// A destination that names a folder is a move, and one that no
	// folder is called, in any case, is a rename in place.  Both are
	// one message for an item, which is why moving and renaming at
	// once is possible at all.  Two folders of the name, or one in
	// another case, is refused as the lookup refused it: a rename
	// there would be a guess at what was meant.
	_, id, err := sh.resolveDir(ctx, args[1])
	if err == nil {
		if e.Folder {
			return sh.s.MoveFolder(ctx, e.ID, id)
		}
		return sh.s.MoveItem(ctx, e.ID, id)
	}
	var ne *sl.NameError
	if !errors.As(err, &ne) || len(ne.IDs) > 0 || len(ne.Near) > 0 {
		return err
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
//
// The three that choose among duplicates are one choice and not three
// flags: --newest, --oldest and --remove-all-copies each answer the same
// question -- a path names several things, which of them is meant --
// with a different answer, so any two of them together is a line that
// means two things at once and is refused.
type rmOptions struct {
	Newest    bool   `getopt:"--newest             of several of a name, delete the one acquired last"`
	Oldest    bool   `getopt:"--oldest             of several of a name, delete the one acquired first"`
	AllCopies bool   `getopt:"--remove-all-copies  delete everything of that name, not just one"`
	In        string `getopt:"--in=OBJECT          delete from inside a rezzed object, not from inventory"`
	Help      bool   `getopt:"--help -h            show what this command takes"`
}

// whichOne says how rm was told to choose among several of a name, for
// the refusal when it was told twice.
func (o rmOptions) whichOne() []string {
	var said []string
	if o.Newest {
		said = append(said, "--newest")
	}
	if o.Oldest {
		said = append(said, "--oldest")
	}
	if o.AllCopies {
		said = append(said, "--remove-all-copies")
	}
	return said
}

// ageWord names the end of the pile that was asked for.
func ageWord(newest bool) string {
	if newest {
		return "newest"
	}
	return "oldest"
}

// pickByAge takes the newest or the oldest of several things of one
// name.
//
// It refuses rather than guesses in the two cases where the dates do not
// settle it.  A tie at the end being chosen is one: inventory dates are
// whole seconds, so a folder copied in one go can hold several items
// stamped alike, and there is no sense in which one of those is the
// newer.  Something with no date at all is the other: a folder has none,
// and undated is not the same as old.
//
// Either way the id names exactly one thing, and ls -l prints it beside
// the date this chooses by -- so the refusal has somewhere to point.
func pickByAge(targets []sl.Entry, newest bool) (sl.Entry, error) {
	word := ageWord(newest)
	for _, e := range targets {
		if e.Created <= 0 {
			return sl.Entry{}, fmt.Errorf(
				"%q has no date on it, so there is no %s: name the one you mean by its id",
				e.Name, word)
		}
	}
	best := targets[0]
	ties := 1
	for _, e := range targets[1:] {
		switch {
		case e.Created == best.Created:
			ties++
		case newest == (e.Created > best.Created):
			best, ties = e, 1
		}
	}
	if ties > 1 {
		return sl.Entry{}, fmt.Errorf(
			"%d of those were acquired at %s, which is the %s: name the one you mean by its id",
			ties, lsWhen(best.Created), word)
	}
	return best, nil
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
	if said := o.whichOne(); len(said) > 1 {
		return fmt.Errorf("%s and %s are two answers to one question; say one of them",
			strings.Join(said[:len(said)-1], ", "), said[len(said)-1])
	}
	if o.In != "" {
		// None of the three has anything to choose among: an object
		// renames a second item of one name, so a name inside one means
		// one item at most.  Why: doc/names.md#measured
		if o.AllCopies {
			return fmt.Errorf("--remove-all-copies does not apply inside an object: " +
				"an object renames a second item of one name, so there is only ever one")
		}
		if said := o.whichOne(); len(said) > 0 {
			return fmt.Errorf("%s does not apply inside an object: what one holds has no dates on it", said[0])
		}
		return sh.removeInside(ctx, out, o.In, args)
	}
	for _, path := range args {
		// One name, and a folder may hold a dozen things wearing it.
		// A plain rm refuses that rather than taking one of them:
		// deleting is permanent here, and which of twelve identically
		// named items went is not a thing to work out afterwards.
		var targets []sl.Entry
		var chose string
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
			var found []sl.Entry
			if found, err = sh.entriesAt(ctx, path); err != nil {
				break
			}
			switch {
			case len(found) == 1:
				targets = found
			case !o.Newest && !o.Oldest:
				return fmt.Errorf("%q is %d things here: say --newest or --oldest to "+
					"delete one of them, --remove-all-copies for all %d, or name one by "+
					"its id, which ls -l prints beside the date",
					path, len(found), len(found))
			default:
				var e sl.Entry
				if e, err = pickByAge(found, o.Newest); err != nil {
					break
				}
				targets = []sl.Entry{e}
				// Which of the several went.  A count would not do it:
				// they share a name, so the date and the id are the only
				// two things on the line that tell them apart, and both
				// are wanted afterwards -- the date to see that the right
				// end of the pile was taken, the id to say so exactly.
				chose = fmt.Sprintf("%s: removed the %s of %d, acquired %s (%s)\n",
					path, ageWord(o.Newest), len(found), lsWhen(e.Created), e.ID)
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
		if chose != "" {
			fmt.Fprint(out, chose)
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

// findOptions is what find was asked for.
//
// -l is ls's flag and prints ls's columns, because the two answers are
// the same answer: a search that turned up four things of one name, or
// a link sitting beside the item it points at, is unreadable as bare
// paths and the columns are what settle it.  Naming it anything else
// would be a second thing to remember for the same question.
type findOptions struct {
	Long   bool `getopt:"-l          the columns: kind, when it was acquired, id and path"`
	Follow bool `getopt:"-L          the columns, with a link shown as the item it points at"`
	Help   bool `getopt:"--help -h   show what this command takes"`
}

func cmdFind(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o findOptions
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
	var hits []sl.Entry
	for _, e := range es {
		if strings.Contains(strings.ToLower(e.Name), want) {
			hits = append(hits, e)
		}
	}
	// Built from what matched rather than from the whole folder: a
	// search that turned up no links wants no walk, however many links
	// it passed over on the way.
	format := sh.longFormatFor(ctx, o.Follow, hits)
	for _, e := range hits {
		full := prefix + "/" + e.Path
		if o.Long || o.Follow {
			format.line(out, e, full)
			continue
		}
		fmt.Fprintln(out, full)
	}
	return nil
}
