package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// TestEveryPathResolverIsTheRightOne.
//
// There are two ways to turn a path into an entry and the difference
// between them is invisible at the call site -- both return an
// sl.Entry, both read the same folder, and the wrong one works
// perfectly on everything except a link.  Which is the whole trouble:
// the fault it causes cannot be found by using the command, because
// the grid answers an id it has no object for with silence, so what a
// person sees is the command's own timeout and a sentence about the
// region.
//
// So the choice is written down here rather than left to whoever adds
// the next command.  thingAt follows a link, because the id is about to
// be sent to the grid as a reference to the thing.  entryAt does not,
// because the inventory entry itself is what is being operated on, and
// a link followed on the way into rm would delete the item at the far
// end and leave every other link to it pointing at nothing.
//
// A new command using entryAt fails this test until its name is added
// with a reason, which is the point: the reason is the part that is
// easy to skip.
func TestEveryPathResolverIsTheRightOne(t *testing.T) {
	// Why each of these operates on the entry and not on the thing.
	keepsTheEntry := map[string]string{
		"takeFolder": "names a destination folder, not a thing",
		"cmdMv":      "renaming a link renames the link",
		"cmdRm":      "deleting a link deletes the link, not what it points at",
		"cmdCopy":    "copying a link makes another link, which is what cp means",
		"cmdPut":     "names a destination folder, not a thing",

		// The two that are the resolvers.
		"entryAt": "is the resolver",
		"thingAt": "is the resolver, and calls entryAt to do the reading",
	}

	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var followed, kept []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "thingAt":
					followed = append(followed, fn.Name.Name)
				case "entryAt", "entriesAt":
					kept = append(kept, fn.Name.Name)
				}
				return true
			})
		}
	}

	if len(followed) == 0 {
		t.Fatal("nothing follows a link at all; this test is looking in the wrong place")
	}
	for _, name := range kept {
		if _, ok := keepsTheEntry[name]; !ok {
			t.Errorf("%s resolves a path with entryAt, which does not follow a link. "+
				"If its id goes to the grid as the thing itself, it wants thingAt -- a link's "+
				"id names no object there, and the grid answers that with silence. If the "+
				"inventory entry really is the subject, add %q here with the reason why.",
				name, name)
		}
	}
}

// TestGivingThroughALinkOffersTheItem.
//
// The wire is the only place this can be seen.  An offer carries the id
// in its bucket with the asset type in front of it, and a link offered
// as itself would go out as the link type, naming an id that points
// into an inventory the other person cannot see.  Nothing on this side
// would report anything wrong: give sends one message and waits for
// nothing.
func TestGivingThroughALinkOffersTheItem(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000a5")
	folder := msg.MustParseUUID("ae947e57-7e57-c0de-f972-5468543f22f5")
	link := msg.MustParseUUID("e5967e57-7e57-c0de-8c54-125cf6bbfa2c")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	addOutfitFolder(x, folder, link, "a hat", hat)
	x.setListed([]person{{ID: testSomebody, Name: "Some Body"}})

	if got := x.do(t, "give 1 An outfit/a hat"); !strings.Contains(got, `offered "a hat"`) {
		t.Fatalf("give printed %q", got)
	}
	var im *msg.ImprovedInstantMessage
	for _, m := range x.grid.Sent() {
		if o, ok := m.(*msg.ImprovedInstantMessage); ok {
			im = o
		}
	}
	if im == nil {
		t.Fatal("give sent no offer")
	}
	var got msg.UUID
	copy(got[:], im.MessageBlock.BinaryBucket[1:])
	if got == link {
		t.Fatal("give offered the link's own id, which names nothing the far end can reach")
	}
	if got != hat {
		t.Errorf("give offered %v, want the item the link points at, %v", got, hat)
	}
	if kind, want := im.MessageBlock.BinaryBucket[0], byte(sl.AssetObject); kind != want {
		t.Errorf("the offer went out as asset type %d, want %d", kind, want)
	}
}

// TestRemovingALinkRemovesTheLink.
//
// The other half of the rule, and the one with teeth: an item may have
// links to it from several outfits, and resolving on the way into rm
// would delete the item and leave all of them pointing at nothing.
func TestRemovingALinkRemovesTheLink(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000a6")
	folder := msg.MustParseUUID("aeae7e57-7e57-c0de-a2a9-6d83d86e9c05")
	link := msg.MustParseUUID("e7587e57-7e57-c0de-263c-1fa2ce00347b")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	addOutfitFolder(x, folder, link, "a hat", hat)

	// Quoted, because rm takes a list of paths and would otherwise be
	// given two.
	x.do(t, `rm "An outfit/a hat"`)

	if inFake(x, link) {
		t.Error("rm left the link it was asked to delete")
	}
	if !inFake(x, hat) {
		t.Fatal("rm followed the link and deleted the item every link to it points at")
	}
}

// inFake says whether an item is still in the fake's inventory.
func inFake(x *testShell, id msg.UUID) bool {
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	var walk func(d *invDir) bool
	walk = func(d *invDir) bool {
		for _, it := range d.Items {
			if it.ID == id {
				return true
			}
		}
		for _, sub := range d.Dirs {
			if walk(sub) {
				return true
			}
		}
		return false
	}
	return walk(x.grid.inv)
}

// TestSaveWritesThroughALink.
//
// A link to a notecard is not the notecard, and the capability is
// addressed by item id: writing to the link's own id would put the
// text nowhere the notecard could be read from.  save used to refuse
// this outright with a sentence telling the reader to give the real
// path, which was a description of the work rather than the work.
func TestSaveWritesThroughALink(t *testing.T) {
	folder := msg.MustParseUUID("af447e57-7e57-c0de-7fdb-ee56e516df06")
	link := msg.MustParseUUID("e7a67e57-7e57-c0de-82a5-6838e2fd973e")

	x := newTestShell(t)
	addLinkFolder(x, folder, link, "readme", testNote, sl.AssetNotecard)
	u := serveItemWrite(t, x, "UpdateNotecardAgentInventory", savedOK)

	const body = "hello\n"
	if got := x.do(t, "save "+aFile(t, "notes.txt", body)+" An outfit/readme"); !strings.Contains(got, "written") {
		t.Fatalf("save through a link printed %q", got)
	}
	asked, wrote := u.seen()
	if strings.Contains(asked, link.String()) {
		t.Fatal("save addressed the link's own id, where no text can be read back from")
	}
	if !strings.Contains(asked, testNote.String()) {
		t.Errorf("save addressed %q, which does not name the notecard the link points at", asked)
	}
	// A notecard goes up inside the Linden container rather than as
	// bare text, so the body is looked for within it.
	if !strings.Contains(wrote, body) {
		t.Errorf("the notecard was written with %q, which does not contain %q", wrote, body)
	}
}

// TestListingFollowsLinksWithDashL.
//
// The question -L answers is the one a Current Outfit folder asks: a
// folder of links, every one of them named after the item it points
// at, where the only thing distinguishing a worn shirt from a worn HUD
// is the kind of the thing at the far end.  Without it the kind column
// says "link" on every row and the id column is an id that names
// nothing outside this inventory.
func TestListingFollowsLinksWithDashL(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000b1")
	folder := msg.MustParseUUID("b3d87e57-7e57-c0de-9a46-e136f491ad31")
	link := msg.MustParseUUID("e8167e57-7e57-c0de-cccc-0eaff4aff638")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	addOutfitFolder(x, folder, link, "a hat", hat)

	// -l describes the link, which is what is actually in the folder.
	plain := x.do(t, `ls -l "An outfit"`)
	if !strings.HasPrefix(plain, "link ") || !strings.Contains(plain, link.String()) {
		t.Errorf("ls -l should describe the link itself:\n%s", plain)
	}

	// -L describes what it points at, in the same columns.
	got := x.do(t, `ls -L "An outfit"`)
	if !strings.HasPrefix(got, "object ") {
		t.Errorf("ls -L should give the kind of the item, not %q", got)
	}
	if strings.Contains(got, link.String()) {
		t.Errorf("ls -L printed the link's own id, which names nothing outside inventory:\n%s", got)
	}
	if !strings.Contains(got, hat.String()) {
		t.Errorf("ls -L should print the id of the item it points at:\n%s", got)
	}
	// The path is where the entry was found, since that is what was
	// listed: the item itself lives somewhere else entirely.
	if !strings.Contains(got, "/An outfit/a hat") {
		t.Errorf("ls -L moved the path:\n%s", got)
	}

	// -L is a long listing.  Bare paths from a flag asking what the
	// links point at would be the flag doing nothing.
	if strings.Count(got, " ") < 3 {
		t.Errorf("ls -L should imply -l, got %q", got)
	}

	// find -L is the same row for the same reason.
	if found := x.do(t, `find -L "a hat"`); !strings.Contains(found, hat.String()) {
		t.Errorf("find -L should follow a link too:\n%s", found)
	}
}

// TestFollowingALinkThatGoesNowhereSaysSo.
//
// A link outlives what it pointed at.  -L still prints the id it points
// at, which the link carries and which needs no lookup, and leaves the
// kind as "link" -- which is the truthful answer to what was asked:
// this is a link, and following it got nowhere.
func TestFollowingALinkThatGoesNowhereSaysSo(t *testing.T) {
	gone := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000b2")
	folder := msg.MustParseUUID("b3ed7e57-7e57-c0de-f722-a07eb8d21f29")
	link := msg.MustParseUUID("e81e7e57-7e57-c0de-fa89-246b704bb675")

	x := newTestShell(t)
	addOutfitFolder(x, folder, link, "a hat", gone)

	got := x.do(t, `ls -L "An outfit"`)
	if !strings.HasPrefix(got, "link ") {
		t.Errorf("a link that goes nowhere should still say it is a link:\n%s", got)
	}
	if !strings.Contains(got, gone.String()) {
		t.Errorf("the id column should be the id it points at even unfollowed:\n%s", got)
	}
	if strings.Contains(got, link.String()) {
		t.Errorf("the id column should not be the link's own id under -L:\n%s", got)
	}
}
