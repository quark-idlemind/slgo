package main

import (
	"regexp"
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

// TestCatRefusalNamesACommandThatExists.
//
// The refusal for a thing that is not text points at the command that
// will fetch it, and pointed at "asset" for a while, which is not a
// command in this shell and never was -- so somebody who typed cat on a
// texture was sent to a prompt that could only say no. Reading the name
// back out of the message and looking it up in the command table is
// what makes the next rename fail here rather than in front of a user.
//
// The sound is here for the other half of it: get is for textures and
// refuses anything else, so naming get outside the texture case would
// send the reader to a second refusal, which is the same fault wearing
// a real command's name.
func TestCatRefusalNamesACommandThatExists(t *testing.T) {
	// The house spelling for a command named inside an error is the
	// name in double quotes; see man.go and inventory.go.
	quoted := regexp.MustCompile(`"([a-z.]+)"`)
	for _, c := range []struct {
		what string
		e    sl.Entry
		want bool // whether a command should be named
	}{
		{"a texture", sl.Entry{Name: "a picture", Type: int(sl.AssetTexture)}, true},
		{"a sound", sl.Entry{Name: "a chime", Type: int(sl.AssetSound)}, false},
		{"an animation", sl.Entry{Name: "a wave", Type: int(sl.AssetAnimation)}, false},
	} {
		err := catReadable(c.e)
		if err == nil {
			t.Errorf("%s: cat should refuse it", c.what)
			continue
		}
		named := quoted.FindAllStringSubmatch(err.Error(), -1)
		if len(named) == 0 && c.want {
			t.Errorf("%s: the refusal names no command to use instead: %v", c.what, err)
		}
		for _, m := range named {
			if _, ok := commands[m[1]]; !ok {
				t.Errorf("%s: the refusal sends the reader to %q, which is not a command: %v", c.what, m[1], err)
				continue
			}
			if !c.want {
				t.Errorf("%s: the refusal names %q, which refuses %s in its turn: %v", c.what, m[1], c.what, err)
			}
		}
	}
}
