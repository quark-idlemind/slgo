package main

// Inventory as a filesystem.
//
// The tree really is a tree, so cd and ls are the right interface for
// it, and the paths they print are the paths they take: a listing can
// be written to a file, edited into a list of moves, and run.  That is
// why ls prints one bare path per line by default and keeps the columns
// for ls -l.

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
	"pwd": {
		usage: "pwd",
		brief: "where in inventory we are",
		run: func(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
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
		usage: "ls [-l] [-r] [PATH]",
		brief: "list a folder; -l for detail, -r to descend",
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
		brief: "move into a folder, or rename if DEST is a plain name",
		run:   cmdMv,
	},
	"rm": {
		usage: "rm PATH...",
		brief: "delete items, permanently",
		run:   cmdRm,
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
	if id, err := msg.ParseUUID(strings.TrimSpace(path)); err == nil {
		return sh.entryByID(ctx, id)
	}
	names := sl.SplitPath(path)
	if len(names) == 0 {
		return sl.Entry{}, fmt.Errorf("no path given")
	}
	dir := strings.Join([]string{}, "")
	if strings.HasPrefix(path, "/") {
		dir = "/"
	}
	dir += sl.JoinPath(names[:len(names)-1]...)

	_, id, err := sh.resolveDir(ctx, dir)
	if err != nil {
		return sl.Entry{}, err
	}
	es, err := sh.s.ListFolder(ctx, id, 0)
	if err != nil {
		return sl.Entry{}, err
	}
	want := names[len(names)-1]
	for _, e := range es {
		if strings.EqualFold(e.Name, want) {
			return e, nil
		}
	}
	return sl.Entry{}, fmt.Errorf("nothing called %q here", want)
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
	path := ""
	if len(args) > 0 {
		path = args[0]
	}
	if len(args) > 1 {
		return fmt.Errorf("usage: cd [PATH]")
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

func cmdLs(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	long, deep := false, false
	var path string
	for _, a := range args {
		switch {
		case a == "-l":
			long = true
		case a == "-r":
			deep = true
		case a == "-lr" || a == "-rl":
			long, deep = true, true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q", a)
		default:
			path = a
		}
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
		kind := kindOf(e)
		// A folder has no date, and an empty column would move
		// every column after it -- which matters, because the
		// point of this listing is that it can be cut up by a
		// program.
		when := "-"
		if e.Created > 0 {
			when = time.Unix(e.Created, 0).Format("2006-01-02")
		}
		fmt.Fprintf(out, "%-10s %-10s %-36s %s\n", kind, when, e.ID, full)
	}
	return nil
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

func cmdCat(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: cat PATH")
	}
	e, err := sh.entryAt(ctx, args[0])
	if err != nil {
		return err
	}
	if e.Folder {
		return fmt.Errorf("%q is a folder", e.Name)
	}
	if e.Asset.IsZero() {
		return fmt.Errorf("%q has no asset to read", e.Name)
	}

	// Notecards and scripts come over the transfer protocol; the
	// content delivery network refuses them.  Anything it does serve
	// is not text, so this only offers the two that are.
	switch sl.AssetType(e.Type) {
	case sl.AssetNotecard, sl.AssetLSLText:
	default:
		return fmt.Errorf("%s is not text; %s fetches it", kindOf(e), "asset")
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

func cmdMv(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: mv PATH DEST")
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
		return fmt.Errorf("renaming a folder is not implemented; make one and move things into it")
	}
	_, err = sh.s.SetItem(ctx, e.ID, dest[0], "", nil)
	return err
}

func cmdRm(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: rm PATH...")
	}
	for _, path := range args {
		e, err := sh.entryAt(ctx, path)
		if err != nil {
			return err
		}
		if e.Folder {
			if err := sh.s.DeleteFolder(ctx, e.ID); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			continue
		}
		if err := sh.s.DeleteItem(ctx, e.ID); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

func cmdFind(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
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
