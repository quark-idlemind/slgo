package main

// What the picture has in it.
//
// The drawing is checked as a function of its arguments rather than
// through the command, because this is the part that is easy to get
// subtly wrong and impossible to see: a flipped axis draws a picture
// that looks perfectly reasonable and puts everybody on the wrong side
// of the avatar, and nobody standing in a live region can tell the
// difference without walking about to find out.  So the positions go in
// and the exact characters come out.

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// mapMe is where the avatar stands in every test here: the middle of
// the region, which is also where the fake grid puts it.
var mapMe = msg.Vector3{X: 128, Y: 128, Z: 25}

// at is somebody standing at a position, with the distance sl.Nearby
// would have worked out for them: the picture never uses it, and the
// line naming whoever is outside the picture does.
func at(name string, x, y, z float32) sl.Person {
	dx, dy, dz := float64(x-mapMe.X), float64(y-mapMe.Y), float64(z-mapMe.Z)
	return sl.Person{
		Name:     name,
		Position: msg.Vector3{X: x, Y: y, Z: z},
		Distance: float32(math.Sqrt(dx*dx + dy*dy + dz*dz)),
	}
}

// picture is the framed grid at the top of what drawMap wrote, without
// the scale and the counts underneath: the table below is about where
// the marks land, and the lines under the grid are checked on their
// own.
func picture(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if i > 0 && strings.HasPrefix(l, "+") {
			return strings.Join(lines[:i+1], "\n")
		}
	}
	t.Fatalf("no framed picture in:\n%s", out)
	return ""
}

// drew runs the drawing and hands back everything it wrote.
func drew(g mapGrid, people ...sl.Person) string {
	var b bytes.Buffer
	drawMap(&b, g, mapMe, people)
	return b.String()
}

// TestThePictureIsDrawnWhereThePeopleAre, one case at a time, with the
// exact characters written down.
//
// Four rows and therefore eight columns, over sixteen metres: two
// metres to a column and four to a row, which is small enough to write
// out in full and has the same arithmetic in it as the default.
func TestThePictureIsDrawnWhereThePeopleAre(t *testing.T) {
	g := aroundGrid(4, 16, mapMe)

	cases := []struct {
		name   string
		people []sl.Person
		want   string
	}{{
		// An empty region is still a picture, and the avatar reading it
		// is still in the middle of it.
		name: "an empty region is just you",
		want: "" +
			"+--------+\n" +
			"|        |\n" +
			"|    *   |\n" +
			"|        |\n" +
			"|        |\n" +
			"+--------+",
	}, {
		// North is up.  Four metres north of the avatar is one row up,
		// in the same column, and this is the case a flipped axis gets
		// exactly backwards.
		name:   "somebody to the north is above the star",
		people: []sl.Person{at("Ozu Brantwick", 128, 132, 25)},
		want: "" +
			"+--------+\n" +
			"|    o   |\n" +
			"|    *   |\n" +
			"|        |\n" +
			"|        |\n" +
			"+--------+",
	}, {
		// East is right, in the same row.
		name:   "somebody to the east is to the right of the star",
		people: []sl.Person{at("Odile Marne", 132, 128, 25)},
		want: "" +
			"+--------+\n" +
			"|        |\n" +
			"|    * o |\n" +
			"|        |\n" +
			"|        |\n" +
			"+--------+",
	}, {
		// And the other two directions, which is the rest of the proof
		// that neither axis is turned over.
		name: "south is below and west is to the left",
		people: []sl.Person{
			at("Kerra Hartwood", 128, 120, 25),
			at("Skell Dunmoore", 120, 128, 25),
		},
		want: "" +
			"+--------+\n" +
			"|        |\n" +
			"|o   *   |\n" +
			"|        |\n" +
			"|    o   |\n" +
			"+--------+",
	}, {
		// Height picks the mark.  Five metres up is more than a storey
		// and is drawn as being above, however near it is on the
		// ground.
		name: "higher and lower are different marks",
		people: []sl.Person{
			at("Ozu Brantwick", 128, 132, 30),
			at("Odile Marne", 128, 120, 20),
			at("Kerra Hartwood", 132, 128, 27),
		},
		want: "" +
			"+--------+\n" +
			"|    ^   |\n" +
			"|    * o |\n" +
			"|        |\n" +
			"|    v   |\n" +
			"+--------+",
	}, {
		// Two people in one cell are a count rather than one of them
		// silently standing for both.
		name: "two in one cell are counted",
		people: []sl.Person{
			at("Ozu Brantwick", 132, 132, 25),
			at("Odile Marne", 133, 133, 40),
		},
		want: "" +
			"+--------+\n" +
			"|      2 |\n" +
			"|    *   |\n" +
			"|        |\n" +
			"|        |\n" +
			"+--------+",
	}, {
		// Ten will not fit in a digit, and a nine there would be a
		// lie, so the mark says "more than nine".
		name: "more than nine in one cell is a plus",
		people: []sl.Person{
			at("A One", 132, 132, 25), at("A Two", 132, 132, 25),
			at("A Three", 132, 132, 25), at("A Four", 132, 132, 25),
			at("A Five", 132, 132, 25), at("A Six", 132, 132, 25),
			at("A Seven", 132, 132, 25), at("A Eight", 132, 132, 25),
			at("A Nine", 132, 132, 25), at("A Ten", 132, 132, 25),
		},
		want: "" +
			"+--------+\n" +
			"|      + |\n" +
			"|    *   |\n" +
			"|        |\n" +
			"|        |\n" +
			"+--------+",
	}, {
		// Somebody beyond the edge is not drawn at the edge, because
		// that would be a claim about where they are and it would be
		// false.  They are counted underneath instead.
		name:   "somebody outside is not drawn at the edge",
		people: []sl.Person{at("Ozu Brantwick", 128, 200, 25)},
		want: "" +
			"+--------+\n" +
			"|        |\n" +
			"|    *   |\n" +
			"|        |\n" +
			"|        |\n" +
			"+--------+",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := picture(t, drew(g, c.people...)); got != c.want {
				t.Errorf("the picture is\n%s\nand should be\n%s", got, c.want)
			}
		})
	}
}

// TestTheStarIsWhereTheAvatarReallyIsInTheRegionPicture, rather than in
// the middle of it: the region view is a picture of the region, and an
// avatar in a corner of it belongs in that corner.
func TestTheStarIsWhereTheAvatarReallyIsInTheRegionPicture(t *testing.T) {
	var b bytes.Buffer
	// Ten metres in from the south-west corner, in a picture where a
	// row is 64 metres and a column 32.
	drawMap(&b, regionGrid(4), msg.Vector3{X: 10, Y: 10, Z: 25}, nil)
	want := "" +
		"+--------+\n" +
		"|        |\n" +
		"|        |\n" +
		"|        |\n" +
		"|*       |\n" +
		"+--------+"
	if got := picture(t, b.String()); got != want {
		t.Errorf("the picture is\n%s\nand should be\n%s", got, want)
	}

	// And the middle of the region is the middle of the picture, which
	// is the same claim made the other way round.
	b.Reset()
	drawMap(&b, regionGrid(4), mapMe, nil)
	want = "" +
		"+--------+\n" +
		"|        |\n" +
		"|    *   |\n" +
		"|        |\n" +
		"|        |\n" +
		"+--------+"
	if got := picture(t, b.String()); got != want {
		t.Errorf("the picture is\n%s\nand should be\n%s", got, want)
	}
}

// TestTheRegionAndTheRelativeViewDisagreeAboutTheSameNeighbour, which
// is what having two views is for.
//
// Somebody forty metres north is a neighbour worth seeing in the
// picture around the avatar, and in the region picture they are inside
// the same sixty-four metre row and cannot be told apart from the
// avatar at all.
func TestTheRegionAndTheRelativeViewDisagreeAboutTheSameNeighbour(t *testing.T) {
	them := at("Ozu Brantwick", 128, 168, 25)

	near := drew(aroundGrid(4, 128, mapMe), them)
	want := "" +
		"+--------+\n" +
		"|    o   |\n" +
		"|    *   |\n" +
		"|        |\n" +
		"|        |\n" +
		"+--------+"
	if got := picture(t, near); got != want {
		t.Errorf("the close picture is\n%s\nand should be\n%s", got, want)
	}

	far := drew(regionGrid(4), them)
	want = "" +
		"+--------+\n" +
		"|        |\n" +
		"|    *   |\n" +
		"|        |\n" +
		"|        |\n" +
		"+--------+"
	if got := picture(t, far); got != want {
		t.Errorf("the region picture is\n%s\nand should be\n%s", got, want)
	}
	// Not lost, though: the star is standing on them and says so.
	if !strings.Contains(far, "1 avatar in the picture") ||
		!strings.Contains(far, "under the *") {
		t.Errorf("the region picture should say who is under the star:\n%s", far)
	}
}

// TestRowsChangeBothDimensions: the columns are twice the rows, so
// asking for more rows asks for a wider picture and a finer scale in
// both directions at once.
func TestRowsChangeBothDimensions(t *testing.T) {
	for _, c := range []struct {
		rows           int
		width          int
		perCol, perRow string
	}{
		{rows: 4, width: 8, perCol: "8", perRow: "16"},
		{rows: 8, width: 16, perCol: "4", perRow: "8"},
		{rows: 16, width: 32, perCol: "2", perRow: "4"},
	} {
		out := drew(aroundGrid(c.rows, mapSpan, mapMe))
		lines := strings.Split(out, "\n")
		if got, want := lines[0], "+"+strings.Repeat("-", c.width)+"+"; got != want {
			t.Errorf("%d rows drew a frame %q, want %q", c.rows, got, want)
		}
		if got := strings.Count(picture(t, out), "\n"); got != c.rows+1 {
			t.Errorf("%d rows drew %d lines of picture", c.rows, got)
		}
		want := c.perCol + "m to a column, " + c.perRow + "m to a row"
		if !strings.Contains(out, want) {
			t.Errorf("%d rows should say %q:\n%s", c.rows, want, out)
		}
	}
}

// TestThePictureSaysWhatItIsShowing.  A grid of characters with no
// scale under it is a drawing rather than a map, and the marks mean
// nothing to somebody who has not read the manual.
func TestThePictureSaysWhatItIsShowing(t *testing.T) {
	out := drew(aroundGrid(4, 16, mapMe),
		at("Ozu Brantwick", 132, 128, 25),
		at("Odile Marne", 128, 200, 25),
		at("Kerra Hartwood", 128, 20, 25))
	for _, want := range []string{
		"2m to a column, 4m to a row; north is up, east is right",
		"* you",
		"o within 3m of your height",
		"^ higher",
		"v lower",
		"+ is more than nine",
		"1 avatar in the picture",
		"2 outside it: Odile Marne 72m, Kerra Hartwood 108m",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the picture should say %q:\n%s", want, out)
		}
	}

	// An empty region says so in the words "who" uses, rather than
	// leaving somebody to count the blank cells.
	if got := drew(aroundGrid(4, 16, mapMe)); !strings.Contains(got, "nobody else is in range") {
		t.Errorf("an empty picture should say so:\n%s", got)
	}
}

// TestAVeryLongListOfPeopleOutsideStopsBeingAListing: naming forty
// avatars under the grid is "who" printed again, and who is the command
// for that.
func TestAVeryLongListOfPeopleOutsideStopsBeingAListing(t *testing.T) {
	var far []sl.Person
	for i := 0; i < 12; i++ {
		far = append(far, at("Somebody Faraway", 128, float32(200+i), 25))
	}
	out := drew(aroundGrid(4, 16, mapMe), far...)
	if !strings.Contains(out, "12 outside it:") {
		t.Errorf("the count should be all twelve:\n%s", out)
	}
	if !strings.Contains(out, "and 4 more") {
		t.Errorf("the names should stop and say how many are left:\n%s", out)
	}
}

// TestAnAvatarOutsideTheRegionLeavesNoStar, and the picture says so
// rather than looking like a drawing that failed.
func TestAnAvatarOutsideTheRegionLeavesNoStar(t *testing.T) {
	var b bytes.Buffer
	drawMap(&b, regionGrid(4), msg.Vector3{X: 300, Y: 128, Z: 25}, nil)
	if strings.Contains(picture(t, b.String()), "*") {
		t.Errorf("there should be no star for an avatar outside the picture:\n%s", b.String())
	}
	if !strings.Contains(b.String(), "you are outside the picture") {
		t.Errorf("the missing star should be explained:\n%s", b.String())
	}
}

// TestMapDrawsWhatTheSessionKnows, which is the command end of it: the
// heading, the view it was asked for, and the people the fake region
// has in it.
func TestMapDrawsWhatTheSessionKnows(t *testing.T) {
	x := newTestShell(t)
	north := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000003")
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testSomebody, Local: 2}, PCode: pcodeAvatar,
			Position: msg.Vector3{X: 128, Y: 138, Z: 25}},
		{Object: sl.Object{ID: north, Local: 3}, PCode: pcodeAvatar,
			Position: msg.Vector3{X: 128, Y: 220, Z: 25}},
	}
	x.grid.AnswerNames(t, map[msg.UUID]string{
		testSomebody: "Ozu Brantwick", north: "Odile Marne",
	})

	got := x.do(t, "map")
	for _, want := range []string{
		"the 64m around you in Test Region at 128, 128, 25",
		"2m to a column, 4m to a row",
		"1 avatar in the picture",
		"1 outside it: Odile Marne 92m",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("map should say %q:\n%s", want, got)
		}
	}

	// The region view is the same people over a different square, so
	// the one who was too far away for the close picture is in this
	// one.
	got = x.do(t, "map --region")
	for _, want := range []string{
		"all 256m of Test Region at 128, 128, 25",
		"8m to a column, 16m to a row",
		"2 avatars in the picture",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("map --region should say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "outside it") {
		t.Errorf("nobody is outside a picture of the whole region:\n%s", got)
	}
}

// TestMapRefusesWhatItCannotDraw, and says which of the two things it
// was given it would have had to throw away.
func TestMapRefusesWhatItCannotDraw(t *testing.T) {
	x := newTestShell(t)
	for _, c := range []struct{ line, want string }{
		{"map --rows 1", "--rows wants 2 to 64"},
		{"map --rows 400", "--rows wants 2 to 64"},
		{"map --region --span 100", "--region covers the region"},
		{"map --span 1", "--span is metres across"},
		{"map somewhere", "takes no argument"},
	} {
		if got := x.do(t, c.line); !strings.Contains(got, c.want) {
			t.Errorf("%q should be refused with %q, got:\n%s", c.line, c.want, got)
		}
	}
}

// TestMapSaysWhyItCannotDraw rather than an empty region, which is what
// a picture of a session that cannot answer would look like.
func TestMapSaysWhyItCannotDraw(t *testing.T) {
	x := newTestShell(t)
	x.grid.objectsErr = errors.New("nothing is holding this session")
	if got := x.do(t, "map"); !strings.Contains(got, "nothing is holding this session") {
		t.Errorf("map should report the failure, got %q", got)
	}
}
