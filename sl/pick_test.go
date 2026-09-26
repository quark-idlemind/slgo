package sl

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// Placeholders, one per thing a test below tells apart.
var (
	pickA = msg.MustParseUUID("f3a47e57-7e57-c0de-be8b-000000000001")
	pickB = msg.MustParseUUID("f3a47e57-7e57-c0de-be8b-000000000002")
	pickC = msg.MustParseUUID("f3a47e57-7e57-c0de-be8b-000000000003")
	pickD = msg.MustParseUUID("f3a47e57-7e57-c0de-be8b-000000000004")
)

// TestPickNamedTakesTheOneSpeltThatWay: "Script" and "script" are two
// names on the grid, so each is the one spelt that way, and the other
// sitting beside it is neither a second meaning nor a stand-in.
func TestPickNamedTakesTheOneSpeltThatWay(t *testing.T) {
	es := []Entry{
		{ID: pickA, Name: "Object"},
		{ID: pickB, Name: "object"},
		{ID: pickC, Name: "objectT"},
	}
	for name, want := range map[string]msg.UUID{"Object": pickA, "object": pickB, "objectT": pickC} {
		got, err := PickNamed(es, name, "", "here")
		if err != nil || got.ID != want {
			t.Errorf("PickNamed(%q) = %v, %v; want %s", name, got.ID, err, want)
		}
	}

	// The same for what an object holds, which is the other thing a
	// name is looked for among.
	items := []TaskItem{{ID: pickA, Name: "Script"}, {ID: pickB, Name: "script"}}
	if got, err := PickNamed(items, "script", "", "in a box"); err != nil || got.ID != pickB {
		t.Errorf("PickNamed among an object's items = %v, %v", got.ID, err)
	}
}

// TestPickNamedRefusesANameThatMeansSeveral: agent inventory lets a
// folder hold any number of one exact name, and the first of them is
// not an answer.  The refusal lists every id, since each names one.
func TestPickNamedRefusesANameThatMeansSeveral(t *testing.T) {
	es := []Entry{
		{ID: pickA, Name: "Probe", Folder: true},
		{ID: pickB, Name: "probe", Folder: true},
		{ID: pickC, Name: "Probe", Folder: true},
	}
	_, err := PickNamed(es, "Probe", "folder", "in Objects")
	var ne *NameError
	if !errors.As(err, &ne) {
		t.Fatalf("PickNamed = %v; want a *NameError", err)
	}
	if len(ne.IDs) != 2 || ne.IDs[0] != pickA || ne.IDs[1] != pickC {
		t.Errorf("the refusal carries %v; want the two called Probe, in order", ne.IDs)
	}
	want := fmt.Sprintf("2 folders in Objects are called \"Probe\":\n  %s\n  %s", pickA, pickC)
	if err.Error() != want {
		t.Errorf("the refusal reads\n%s\nwant\n%s", err, want)
	}

	// AllNamed is for the commands that act on every one of a name,
	// and gives them back rather than refusing.
	all, err := AllNamed(es, "Probe", "folder", "in Objects")
	if err != nil || len(all) != 2 || all[0].ID != pickA || all[1].ID != pickC {
		t.Errorf("AllNamed = %v, %v", all, err)
	}
}

// TestPickNamedRefusesANameThatMeansNothingWithTheNearMisses: asked for
// "object" among "Object" and "objectT", taking the first would take
// what was not asked for.  What differs only in case is offered
// instead, which is the likely typo.
func TestPickNamedRefusesANameThatMeansNothingWithTheNearMisses(t *testing.T) {
	es := []Entry{
		{ID: pickA, Name: "Object"},
		{ID: pickB, Name: "objectT"},
		{ID: pickC, Name: "OBJECT"},
		{ID: pickD, Name: "Object"},
	}
	for _, c := range []struct {
		name, what, where string
		items             []Entry
		want              string
	}{
		{"object", "", "here", es[:2], `nothing called "object" here; did you mean "Object"?`},
		{"object", "", "here", es[:3], `nothing called "object" here; did you mean "Object" or "OBJECT"?`},
		{"Objects", "", "in /Stuff", es, `nothing called "Objects" in /Stuff`},
		{"objectt", "folder", "in the inventory root", es,
			`no folder "objectt" in the inventory root; did you mean "objectT"?`},
		{"x", "", "", nil, `nothing called "x"`},
	} {
		_, err := PickNamed(c.items, c.name, c.what, c.where)
		if err == nil || err.Error() != c.want {
			t.Errorf("PickNamed(%q) = %v; want %s", c.name, err, c.want)
		}
		if _, err := AllNamed(c.items, c.name, c.what, c.where); err == nil || err.Error() != c.want {
			t.Errorf("AllNamed(%q) = %v; want %s", c.name, err, c.want)
		}
	}

	// Three near misses, each once however many things carry it.
	three := append(es, Entry{ID: pickD, Name: "oBJECT"})
	_, err := PickNamed(three, "object", "", "here")
	if want := `nothing called "object" here; did you mean "Object", "OBJECT" or "oBJECT"?`; err == nil || err.Error() != want {
		t.Errorf("PickNamed = %v; want %s", err, want)
	}
}

// TestAFolderPathIsWalkedByTheExactName: each segment of a path picks
// out one folder the way PickNamed does, so a folder is reached by its
// name as spelt, never through a case variant beside it, and a segment
// that two folders answer to stops the walk.
func TestAFolderPathIsWalkedByTheExactName(t *testing.T) {
	w, f := newFakeSession(t)
	f.ServeInventoryTree(t, func(id msg.UUID) ([]*Folder, []*Item) {
		switch id {
		case testInvRoot:
			return []*Folder{
				{ID: pickA, ParentID: testInvRoot, Name: "Probe", Type: -1},
				{ID: pickB, ParentID: testInvRoot, Name: "probe", Type: -1},
				{ID: pickC, ParentID: testInvRoot, Name: "Twice", Type: -1},
				{ID: pickD, ParentID: testInvRoot, Name: "Twice", Type: -1},
			}, nil
		case pickA:
			return nil, []*Item{anItem(theChild, "in Probe")}
		case pickB:
			return nil, []*Item{anItem(theOther, "in probe")}
		}
		return nil, nil
	})

	for path, want := range map[string]msg.UUID{"Probe": theChild, "probe": theOther} {
		got, err := w.ListInventory(context.Background(), path, 0)
		if err != nil || len(got) != 1 || got[0].ID != want {
			t.Errorf("ListInventory(%q) = %v, %v", path, paths(got), err)
		}
	}

	_, err := w.ListInventory(context.Background(), "PROBE", 0)
	if err == nil || !strings.Contains(err.Error(), `did you mean "Probe" or "probe"?`) {
		t.Errorf("ListInventory(PROBE) = %v; want the two near misses offered", err)
	}

	_, err = w.ListInventory(context.Background(), "Twice", 0)
	if err == nil || !strings.Contains(err.Error(), pickC.String()) || !strings.Contains(err.Error(), pickD.String()) {
		t.Errorf("ListInventory(Twice) = %v; want both folders' ids", err)
	}
}

// caseBlind is somewhere a name read off something is compared with
// strings.EqualFold.
type caseBlind struct {
	at, fn string
}

// caseBlindNamesIn finds every strings.EqualFold with an argument that
// ends in .Name or .Path, which is what matching an inventory entry or
// an item inside an object ignoring case looks like.  It cannot tell
// an Entry's name from a person's, so what it finds is checked against
// a list that says which is which.
func caseBlindNamesIn(fset *token.FileSet, f *ast.File) []caseBlind {
	var out []caseBlind
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "EqualFold" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "strings" {
				return true
			}
			for _, arg := range call.Args {
				if a, ok := arg.(*ast.SelectorExpr); ok && (a.Sel.Name == "Name" || a.Sel.Name == "Path") {
					p := fset.Position(call.Pos())
					out = append(out, caseBlind{at: fmt.Sprintf("%s:%d", p.Filename, p.Line), fn: fn.Name.Name})
					break
				}
			}
			return true
		})
	}
	return out
}

// TestNoInventoryNameIsMatchedIgnoringCase: the grid keeps "Script" and
// "script" as two names, so a name looked for among inventory entries
// or the items in an object goes through PickNamed or AllNamed, which
// match it exactly.  A strings.EqualFold on a name here, in slsh or in
// slbotd fails this test until it is either moved to those or listed
// below with what it is matching, which is the part that is easy to
// skip.  An entry here that no longer matches anything fails too, so
// that the list does not outlive what it describes.
func TestNoInventoryNameIsMatchedIgnoringCase(t *testing.T) {
	// What each of these matches ignoring case.
	matching := map[string]string{
		"sl.InventoryOffersFor": "an offer, by what is offered or who offered it, whole or in part: a search",
		"slsh.chooseGroup":      "a group's name",
		"slsh.regionNamed":      "a region's name, which the map matches ignoring case",
		"slsh.printFound":       "a person's display name against their name",
		"slsh.whoOrSearch":      "a person's name or username",
		"slsh.namedExactly":     "a person's name, among what a search found",
		"slbotd.personNamed":    "a person's name or username",
		"slbotd.pickRegion":     "a region's name, which the map matches ignoring case",

		// Inventory, and not yet moved to exact names.
		"slsh.matchLandmarks":   "a landmark, by name or by path",
		"slsh.sharePath":        "whether landmarks share a path, as matchLandmarks compares one",
		"slsh.detachFromOutfit": "a link in the Current Outfit folder, by name",
		"slbotd.cmdDetach":      "a worn object, by name",
		"slbotd.cmdLandmark":    "a landmark, by name",
	}

	seen := map[string]bool{}
	fset := token.NewFileSet()
	for dir, pkg := range map[string]string{".": "sl", "../cmd/slsh": "slsh", "../cmd/slbotd": "slbotd"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("no Go files in %s; this test is looking in the wrong place", dir)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range caseBlindNamesIn(fset, f) {
				key := pkg + "." + c.fn
				seen[key] = true
				if _, ok := matching[key]; !ok {
					t.Errorf("%s: %s compares a name ignoring case.  An inventory entry's name or "+
						"an object item's is matched exactly, through sl.PickNamed or sl.AllNamed "+
						"(pick.go).  If this is some other kind of name, add %q to the list in "+
						"this test with what it matches.", c.at, c.fn, key)
				}
			}
		}
	}
	var stale []string
	for key := range matching {
		if !seen[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t.Errorf("%s is listed as matching some other kind of name ignoring case, and no longer "+
			"compares one at all; take it off the list", key)
	}
}

// TestTheCaseBlindCheckFindsOne: a check that found nothing would pass
// on a tree that did it everywhere.
func TestTheCaseBlindCheckFindsOne(t *testing.T) {
	const src = `package p

func a(es []Entry, want string) {
	for _, e := range es {
		if strings.EqualFold(e.Name, want) {
		}
		_ = strings.EqualFold(want, e.Path)
		_ = strings.EqualFold(want, "home")
		_ = e.Name == want
	}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range caseBlindNamesIn(fset, f) {
		got = append(got, c.at+" "+c.fn)
	}
	if want := "p.go:5 a,p.go:7 a"; strings.Join(got, ",") != want {
		t.Errorf("found %q, want %q", strings.Join(got, ","), want)
	}
}
