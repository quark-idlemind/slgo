package main

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// TestCatReadable pins what cat will and will not try to read.
//
// The case that matters is a script with no asset id. The grid does not
// put a script's asset id in an inventory listing -- it sends all
// zeroes -- and cat refused on that for a while, which came to "every
// script fails and every notecard works".
func TestCatReadable(t *testing.T) {
	for _, c := range []struct {
		what string
		e    sl.Entry
		want string // a fragment of the refusal, or "" to be readable
	}{
		{"a script whose asset id the grid withheld",
			sl.Entry{Name: "tabprobe", Type: int(sl.AssetLSLText)}, ""},
		{"a script that did come with one",
			sl.Entry{Name: "tabprobe", Type: int(sl.AssetLSLText),
				Asset: msg.MustParseUUID("a69d7e57-7e57-c0de-795d-0af059b13583")}, ""},
		{"a notecard",
			sl.Entry{Name: "readme", Type: int(sl.AssetNotecard)}, ""},
		{"a script of the long dead kind",
			sl.Entry{Name: "old", Type: int(sl.AssetScriptLegacy)}, ""},
		{"a folder",
			sl.Entry{Name: "Scripts", Folder: true}, "is a folder"},
		{"an object, which is not text",
			sl.Entry{Name: "a lamp", Type: int(sl.AssetObject)}, "not text"},
		{"a texture, likewise",
			sl.Entry{Name: "a picture", Type: int(sl.AssetTexture)}, "not text"},
	} {
		err := catReadable(c.e)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: should be readable, got %v", c.what, err)
		case c.want != "" && err == nil:
			t.Errorf("%s: should be refused, was not", c.what)
		case c.want != "" && !strings.Contains(err.Error(), c.want):
			t.Errorf("%s: refusal should mention %q, got %v", c.what, c.want, err)
		}
	}
}
