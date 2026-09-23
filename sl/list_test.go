package sl

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

func TestSplitPath(t *testing.T) {
	cases := map[string][]string{
		"":                    nil,
		"/":                   nil,
		"Objects":             {"Objects"},
		"/Objects":            {"Objects"},
		"/Objects/":           {"Objects"},
		"Objects/Probes":      {"Objects", "Probes"},
		"//Objects//Probes//": {"Objects", "Probes"},

		// Nothing is trimmed.  An inventory name may begin or end
		// with a space, and trimming here would make a listing that
		// cannot be read back.
		"  Objects / Probes  ": {"  Objects ", " Probes  "},

		// The escapes.
		`a\/b`:         {"a/b"},
		`a\\b`:         {`a\b`},
		`one/a\/b/two`: {"one", "a/b", "two"},
		`\/`:           {"/"},
		`\\`:           {`\`},

		// A backslash before anything else is a backslash, so a name
		// written without knowing the rules still means what it says.
		`a\nb`:   {`a\nb`},
		`trail\`: {`trail\`},
	}
	for in, want := range cases {
		got := SplitPath(in)
		if len(got) != len(want) {
			t.Errorf("SplitPath(%q) = %q, want %q", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("SplitPath(%q) = %q, want %q", in, got, want)
				break
			}
		}
	}
}

func TestJoinPath(t *testing.T) {
	cases := []struct {
		names []string
		want  string
	}{
		{[]string{"Objects"}, "Objects"},
		{[]string{"Objects", "Probes"}, "Objects/Probes"},
		{[]string{"a/b"}, `a\/b`},
		{[]string{`a\b`}, `a\\b`},
		{[]string{"one", "a/b"}, `one/a\/b`},
		{[]string{" spaced "}, " spaced "},
	}
	for _, c := range cases {
		if got := JoinPath(c.names...); got != c.want {
			t.Errorf("JoinPath(%q) = %q, want %q", c.names, got, c.want)
		}
	}
}

// TestPathRoundTrip is the property the escaping exists for: a listing
// written out and read back has to give the names it was built from,
// whatever they contain.  Inventory names may hold very nearly every
// printable character, so that is what is tried.
func TestPathRoundTrip(t *testing.T) {
	var names []string
	for c := byte(' '); c <= '~'; c++ {
		names = append(names,
			string(c),           // the character alone
			"a"+string(c)+"b",   // and inside a name
			string(c)+string(c), // twice
		)
	}
	// And one name holding every printable character at once.
	var every []byte
	for c := byte(' '); c <= '~'; c++ {
		every = append(every, c)
	}
	names = append(names,
		" leading", "trailing ", "  ", "a/b", `a\b`, `\`, "/", `\/`,
		string(every),
	)

	for _, n := range names {
		got := SplitPath(JoinPath(n))
		if len(got) != 1 || got[0] != n {
			t.Errorf("round trip of %q gave %q", n, got)
		}
	}

	// And a path of several such names at once.
	for i := 0; i+2 < len(names); i += 3 {
		want := names[i : i+3]
		got := SplitPath(JoinPath(want...))
		if len(got) != len(want) {
			t.Errorf("round trip of %q gave %q", want, got)
			continue
		}
		for j := range want {
			if got[j] != want[j] {
				t.Errorf("round trip of %q gave %q", want, got)
				break
			}
		}
	}
}

// TestSortEntries: a listing reads like a tree -- each folder's
// contents under it, folders before items, then by name.
func TestSortEntries(t *testing.T) {
	es := []Entry{
		{Name: "zebra", Path: "zebra"},
		{Name: "Alpha", Path: "Alpha", Folder: true},
		{Name: "beta", Path: "beta"},
		{Name: "inner", Path: "Alpha/inner"},
		{Name: "Deep", Path: "Alpha/Deep", Folder: true},
		{Name: "deepest", Path: "Alpha/Deep/deepest"},
		// A sibling whose name sorts inside the other folder's
		// contents if paths are compared as plain strings, because
		// a space is below a slash.
		{Name: "Alpha Two", Path: "Alpha Two", Folder: true},
	}
	sortEntries(es)

	var got []string
	for _, e := range es {
		got = append(got, e.Path)
	}
	want := []string{
		"Alpha",              // the folder
		"Alpha/Deep",         // its subfolder
		"Alpha/Deep/deepest", // and what is in that
		"Alpha/inner",        // then the items in Alpha
		"Alpha Two",          // only then the sibling
		"beta",
		"zebra",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order:\n got %v\nwant %v", got, want)
	}
}

// TestEntryString: a folder is shown with a trailing separator, which
// is how a listing says which is which at a glance.
func TestEntryString(t *testing.T) {
	if got := (Entry{Path: "Objects", Folder: true}).String(); got != "Objects/" {
		t.Errorf("folder = %q", got)
	}
	if got := (Entry{Path: "Objects/thing"}).String(); got != "Objects/thing" {
		t.Errorf("item = %q", got)
	}
}

func TestAssetTypeNames(t *testing.T) {
	if got := AssetTexture.String(); got != "texture" {
		t.Errorf("texture = %q", got)
	}
	if got := AssetLSLText.String(); got != "lsltext" {
		t.Errorf("lsltext = %q", got)
	}
	// A number nobody has named still prints as something.
	if got := AssetType(999).String(); !strings.Contains(got, "999") {
		t.Errorf("unknown = %q", got)
	}
}

// TestAssetRefusesWhatTheNetworkWillNot: the capability answers 403 for
// notecards and scripts, and saying so beats a 403 with no explanation.
func TestAssetRefusesWhatTheNetworkWillNot(t *testing.T) {
	w := &Session{b: capless{}}
	id := mustUUID("3ac37e57-7e57-c0de-5607-527da8fa08de")

	for _, t2 := range []AssetType{AssetNotecard, AssetLSLText, AssetObject} {
		_, err := w.Asset(nil, id, t2)
		if err == nil {
			t.Errorf("%s should have been refused", t2)
			continue
		}
		if !strings.Contains(err.Error(), "ReadAsset") {
			t.Errorf("%s: the error should point at the other route: %v", t2, err)
		}
	}

	// A type the network does serve gets as far as looking for the
	// capability, which this session does not have.
	if _, err := w.Asset(nil, id, AssetTexture); err == nil {
		t.Error("expected a complaint about the missing capability")
	} else if !strings.Contains(err.Error(), "ViewerAsset") {
		t.Errorf("texture: %v", err)
	}

	if _, err := w.Asset(nil, msgZero(), AssetTexture); err == nil {
		t.Error("a zero asset id should be refused")
	}
}

// capless is a backend with no capabilities at all.
type capless struct{ Backend }

func (capless) HasCap(string) bool { return false }

func mustUUID(s string) msg.UUID { return msg.MustParseUUID(s) }
func msgZero() msg.UUID          { return msg.UUID{} }

// TestListingPathsSplitBack is the property a listing has to have if it
// is to be written to a file, edited, and read back: every path in it
// splits into the names it was built from.
//
// This is what a live listing caught and the unit tests had not: the
// prefixing step was escaping paths that were already escaped, so every
// separator in them became a literal character and "Notecards/thing"
// came out as "Notecards\\/thing".
func TestListingPathsSplitBack(t *testing.T) {
	// Paths as the walk builds them, from names that need escaping.
	entries := []Entry{
		{Name: "Notecards", Path: join("", "Notecards"), Folder: true},
		{Name: "a/b", Path: join(join("", "Notecards"), "a/b")},
		{Name: `back\slash`, Path: join(join("", "Notecards"), `back\slash`)},
	}
	// And the prefixing a listing under a path does.
	const at = "Objects"
	for i := range entries {
		entries[i].Path = at + string(PathSeparator) + entries[i].Path
	}

	want := [][]string{
		{"Objects", "Notecards"},
		{"Objects", "Notecards", "a/b"},
		{"Objects", "Notecards", `back\slash`},
	}
	for i, e := range entries {
		got := SplitPath(e.Path)
		if len(got) != len(want[i]) {
			t.Errorf("%q split to %q, want %q", e.Path, got, want[i])
			continue
		}
		for j := range got {
			if got[j] != want[i][j] {
				t.Errorf("%q split to %q, want %q", e.Path, got, want[i])
				break
			}
		}
	}
}

// TestListingAFolderByPathWalksTheNamesDown: a path is names and AIS
// answers about ids, so there is no call that takes a path -- every
// segment is a fetch, and the id form exists because a deep path costs
// one request per level.
func TestListingAFolderByPathWalksTheNamesDown(t *testing.T) {
	w, f := newFakeSession(t)
	inner := mustUUID("61f67e57-7e57-c0de-56cf-6e25f6190210")

	f.ServeInventoryTree(t, func(id msg.UUID) ([]*Folder, []*Item) {
		switch id {
		case testInvRoot:
			return []*Folder{{ID: aFolder, ParentID: testInvRoot, Name: "Objects", Type: 6}}, nil
		case aFolder:
			return []*Folder{{ID: inner, ParentID: aFolder, Name: "Tools", Type: -1}},
				[]*Item{anItem(theChild, "workbench")}
		case inner:
			return nil, []*Item{anItem(theOther, "anvil")}
		}
		return nil, nil
	})

	// The root itself, which is what an empty path means.
	got, err := w.ListInventory(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("ListInventory: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Objects" || !got[0].IsFolder() {
		t.Fatalf("the root holds %v", got)
	}

	// A folder named rather than the root, with the path of the folder
	// that was asked about on the front of everything inside it, and in
	// tree order: the folder, then what is in it, then its siblings.
	got, err = w.ListInventory(context.Background(), "/Objects/", 1)
	if err != nil {
		t.Fatalf("ListInventory: %v", err)
	}
	want := []string{"Objects/Tools/", "Objects/workbench"}
	if len(got) != len(want) {
		t.Fatalf("listed %v, want %v", paths(got), want)
	}
	for i, p := range want {
		if got[i].String() != p {
			t.Errorf("entry %d is %q, want %q", i, got[i].String(), p)
		}
	}
	// The depth counts from the folder that was asked about, not from
	// the root.
	if got[0].Depth != 0 || got[1].Depth != 0 {
		t.Errorf("depths came out as %d and %d", got[0].Depth, got[1].Depth)
	}
	if got[1].ID != theChild || got[1].IsFolder() || got[1].InvType != 6 {
		t.Errorf("the item came out as %+v", got[1])
	}
	if got[0].Parent != aFolder || got[0].Type != -1 {
		t.Errorf("the folder came out as %+v", got[0])
	}
}

// TestListingByIdIsForFoldersAPathCannotName: an inventory name may hold
// a separator, and a folder called "a/b" cannot be reached by walking
// names -- so the id form is the way in rather than a convenience.
func TestListingByIdIsForFoldersAPathCannotName(t *testing.T) {
	w, f := newFakeSession(t)
	f.ServeInventory(t, func(id msg.UUID) []*Item {
		if id != aFolder {
			return nil
		}
		return []*Item{anItem(theChild, "workbench")}
	})

	got, err := w.ListFolder(context.Background(), aFolder, 0)
	if err != nil {
		t.Fatalf("ListFolder: %v", err)
	}
	// No prefix, because nothing said where this folder sits.
	if len(got) != 1 || got[0].Path != "workbench" {
		t.Errorf("listed %v", paths(got))
	}
}

// TestListingSaysWhichPartOfThePathWasNotThere: a path that stops
// halfway is the ordinary mistake, and a caller told only "no such
// folder" would not know how far it got.
func TestListingSaysWhichPartOfThePathWasNotThere(t *testing.T) {
	w, f := newFakeSession(t)
	f.ServeInventoryTree(t, func(id msg.UUID) ([]*Folder, []*Item) {
		if id != testInvRoot {
			return nil, nil
		}
		return []*Folder{{ID: aFolder, ParentID: testInvRoot, Name: "Objects", Type: 6}}, nil
	})

	_, err := w.ListInventory(context.Background(), "Landmarks", 0)
	if err == nil || !strings.Contains(err.Error(), "the inventory root") {
		t.Errorf("ListInventory = %v, want it to say it looked in the root", err)
	}
	_, err = w.ListInventory(context.Background(), "Objects/Tools", 0)
	if err == nil || !strings.Contains(err.Error(), `"Tools"`) ||
		!strings.Contains(err.Error(), "Objects") {
		t.Errorf("ListInventory = %v, want it to say where it got to", err)
	}

	// A folder that is not a folder: a path segment matching an item is
	// not a way down, since only folders have children.
	if _, err := w.ListFolder(context.Background(), msgZero(), 0); err == nil {
		t.Error("ListFolder listed a folder with no id")
	}
}

// TestListingPassesOnAnInventoryItCouldNotRead: a folder that answered
// nothing and a folder with nothing in it are different, and a listing
// that returned empty for both would have a caller believe the grid.
func TestListingPassesOnAnInventoryItCouldNotRead(t *testing.T) {
	w, f := newFakeSession(t)

	// No capability at all, which is what a session that never got one
	// looks like.
	if _, err := w.ListFolder(context.Background(), aFolder, 0); err == nil {
		t.Error("ListFolder listed a folder it could not read")
	}
	if _, err := w.ListInventory(context.Background(), "Objects", 0); err == nil {
		t.Error("ListInventory walked a path it could not read")
	}

	f.mu.Lock()
	f.capErr = errors.New("the capability is gone")
	f.mu.Unlock()
	if _, err := w.ListFolder(context.Background(), aFolder, 0); err == nil {
		t.Error("ListFolder listed a folder whose read failed")
	}
}

// TestASessionWithNoInventoryRootCannotWalkAPath: the root is where
// every path starts, and a session that was given none would otherwise
// ask the grid about the zero uuid.
func TestASessionWithNoInventoryRootCannotWalkAPath(t *testing.T) {
	f := newFake(t)
	f.info.InventoryRoot = msg.UUID{}
	w, err := New(f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	if _, err := w.ListInventory(context.Background(), "Objects", 0); err == nil {
		t.Error("ListInventory walked down from a root the session does not have")
	}
}

// paths is what a listing looks like, for a failure that has to show the
// shape it came out in.
func paths(es []Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.String())
	}
	return out
}

// TestTreeOrderIsSegmentBySegmentAndNeverATie: comparing whole strings
// sorts "Alpha Two" between "Alpha" and "Alpha/Deep", because a space is
// below a slash -- so a sibling folder lands in the middle of another
// folder's contents.  And AIS hands a folder's contents over in a map,
// which has no order, so anything left tied comes out differently on
// every listing.
func TestTreeOrderIsSegmentBySegmentAndNeverATie(t *testing.T) {
	older := mustUUID("13787e57-7e57-c0de-3700-d712e533b4ec")
	newer := mustUUID("1e117e57-7e57-c0de-504c-02f254bdc125")

	// Two names differing only in case sort by the case, but only after
	// the case-insensitive comparison has said they are the same word.
	es := []Entry{
		{Path: "beta", Name: "beta"},
		{Path: "Beta", Name: "Beta"},
	}
	sortEntries(es)
	if es[0].Path != "Beta" {
		t.Errorf("case order came out as %v", paths(es))
	}

	// The same path twice, which a folder and an item of one name can
	// be: the folder first, so its contents follow it.
	es = []Entry{
		{Path: "thing", Name: "thing"},
		{Path: "thing", Name: "thing", Folder: true},
	}
	sortEntries(es)
	if !es[0].Folder {
		t.Error("an item of the same name sorted above the folder")
	}

	// Two items of one name in one folder, which is allowed: newest
	// first, and then by id so that two listings of an unchanged folder
	// match.
	es = []Entry{
		{Path: "note", ID: newer, Created: 100},
		{Path: "note", ID: older, Created: 200},
	}
	sortEntries(es)
	if es[0].Created != 200 {
		t.Errorf("the older copy sorted first: %+v", es)
	}
	es = []Entry{
		{Path: "note", ID: newer, Created: 100},
		{Path: "note", ID: older, Created: 100},
	}
	sortEntries(es)
	if es[0].ID != older {
		t.Error("two entries tied on everything came out in map order")
	}
}
