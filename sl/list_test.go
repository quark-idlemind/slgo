package sl

import (
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
	id := mustUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")

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
