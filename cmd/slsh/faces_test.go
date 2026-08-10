package main

// Saying what a face looks like.
//
// The reporting half of texture, which is all local: what it prints
// comes from faces already decoded, so none of it needs a grid.

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// TestFaceLabelGathersRuns, so six faces alike are one line rather than
// six, and the numbers read as ranges.
func TestFaceLabelGathersRuns(t *testing.T) {
	for _, c := range []struct {
		nums []int
		want string
	}{
		{[]int{2}, "face 2"},
		{[]int{0, 1}, "faces 0-1"},
		{[]int{0, 1, 3, 4, 5}, "faces 0-1,3-5"},
		{[]int{0, 2, 4}, "faces 0,2,4"},
	} {
		if got := faceLabel(c.nums); got != c.want {
			t.Errorf("faceLabel(%v) = %q, want %q", c.nums, got, c.want)
		}
	}
}

// TestDescribeFaceSaysOnlyWhatWasDone: an untouched face reads as
// plain, and nothing that has not been set to something adds noise to
// the line.
func TestDescribeFaceSaysOnlyWhatWasDone(t *testing.T) {
	plain := describeFace(sl.PlainFaces(1)[0])
	want := "texture none  colour 255,255,255  alpha 255  repeats 1,1"
	if plain != want {
		t.Errorf("a plain face reads as\n\t%q\nwant\n\t%q", plain, want)
	}
	for _, missing := range []string{"offset", "rot", "shiny", "bump", "glow", "fullbright"} {
		if strings.Contains(plain, missing) {
			t.Errorf("a plain face mentions %s: %q", missing, plain)
		}
	}

	f := sl.PlainFaces(1)[0]
	f.Texture = msg.UUID{0x89, 0x55, 0x67, 0x47}
	f.SetColour(255, 80, 80)
	f.SetAlpha(200)
	f.SetRepeats(4, 2)
	f.SetOffsets(0.25, -0.5)
	f.SetFullbright(true)
	f.SetShiny(sl.ShinyHigh)
	f.Glow = 100
	got := describeFace(f)
	for _, part := range []string{
		"texture 90397e57-7e57-c0de-de82-ae6404acf598",
		"colour 255,80,80", "alpha 200", "repeats 4,2",
		"offset 0.25,-0.5", "fullbright", "shiny high", "glow 100",
	} {
		if !strings.Contains(got, part) {
			t.Errorf("describeFace is missing %q:\n\t%s", part, got)
		}
	}
}

// TestChangesNothingIsEveryFlag: the report only happens when nothing
// was asked for, so a flag left out of this test is a flag that would
// silently report instead of changing anything.
func TestChangesNothingIsEveryFlag(t *testing.T) {
	none := textureFlags{Face: sl.AllFaces, Alpha: -1, Shiny: -1, Glow: -1}
	if !none.changesNothing() {
		t.Fatal("no flags at all is not being read as a request to report")
	}
	// --face alone still reports: it says which face to say something
	// about, not what to do to it.
	one := none
	one.Face = 2
	if !one.changesNothing() {
		t.Error("--face on its own should report rather than change")
	}
	for name, set := range map[string]func(*textureFlags){
		"--id":            func(o *textureFlags) { o.ID = "x" },
		"--repeats":       func(o *textureFlags) { o.Repeats = "1,1" },
		"--offset":        func(o *textureFlags) { o.Offset = "0,0" },
		"--rot":           func(o *textureFlags) { o.Rot = "45" },
		"--color":         func(o *textureFlags) { o.Colour = "1,2,3" },
		"--alpha":         func(o *textureFlags) { o.Alpha = 0 },
		"--shiny":         func(o *textureFlags) { o.Shiny = 0 },
		"--glow":          func(o *textureFlags) { o.Glow = 0 },
		"--fullbright":    func(o *textureFlags) { o.Bright = true },
		"--no-fullbright": func(o *textureFlags) { o.Dark = true },
	} {
		o := none
		set(&o)
		if o.changesNothing() {
			t.Errorf("%s alone reads as no change at all", name)
		}
	}
}
