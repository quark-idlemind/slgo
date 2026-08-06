package sl

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

func TestSplitPath(t *testing.T) {
	cases := map[string][]string{
		"":                     nil,
		"/":                    nil,
		"Objects":              {"Objects"},
		"/Objects":             {"Objects"},
		"/Objects/":            {"Objects"},
		"Objects/Probes":       {"Objects", "Probes"},
		"//Objects//Probes//":  {"Objects", "Probes"},
		"  Objects / Probes  ": {"Objects", "Probes"},
	}
	for in, want := range cases {
		got := SplitPath(in)
		if len(got) != len(want) {
			t.Errorf("SplitPath(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("SplitPath(%q) = %v, want %v", in, got, want)
				break
			}
		}
	}
}

func TestJoinPath(t *testing.T) {
	if got := join("", "Objects"); got != "Objects" {
		t.Errorf("join at the root = %q", got)
	}
	if got := join("Objects", "Probes"); got != "Objects/Probes" {
		t.Errorf("join = %q", got)
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
