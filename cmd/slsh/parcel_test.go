package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// answerParcel has the fake simulator answer a parcel question with the
// sequence id it was asked with, which is the only thing pairing an
// answer with its question.
func answerParcel(t *testing.T, x *testShell, body func(seq int32) string) {
	t.Helper()
	x.grid.onSend = func(m msg.Message) {
		var seq int32
		switch r := m.(type) {
		case *msg.ParcelPropertiesRequest:
			seq = r.ParcelData.SequenceID
		case *msg.ParcelPropertiesRequestByID:
			seq = r.ParcelData.SequenceID
		default:
			return
		}
		x.grid.RelayEvent(t, "ParcelProperties", body(seq))
	}
}

// Thrushmoor is a parcel with the numbers Pelmar Reach really answered with.
func Thrushmoor(seq int32) string {
	return fmt.Sprintf(`<llsd><map><key>ParcelData</key><array><map>
	  <key>Name</key><string>Thrushmoor</string>
	  <key>Desc</key><string></string>
	  <key>LocalID</key><integer>5</integer>
	  <key>SequenceID</key><integer>%d</integer>
	  <key>Area</key><integer>2048</integer>
	  <key>OwnerID</key><string>e34a7e57-7e57-c0de-432d-e2701ced4688</string>
	  <key>MaxPrims</key><integer>937</integer>
	  <key>TotalPrims</key><integer>486</integer>
	  <key>OwnerPrims</key><integer>485</integer>
	  <key>GroupPrims</key><integer>1</integer>
	  <key>ParcelFlags</key><binary encoding="base64">VqSACw==</binary>
	  <key>AABBMin</key><array><real>12</real><real>48</real><real>0</real></array>
	  <key>AABBMax</key><array><real>44</real><real>112</real><real>50</real></array>
	</map></array></map></llsd>`, seq)
}

// TestParcelAsksAndSaysWhatItGot: the command's whole reason to exist
// is that "where" cannot say which of a region's parcels the avatar is
// on.
func TestParcelAsksAndSaysWhatItGot(t *testing.T) {
	x := newTestShell(t)
	answerParcel(t, x, Thrushmoor)

	got := x.do(t, "parcel")
	for _, want := range []string{
		"Thrushmoor",
		"local    5",
		"e34a7e57-7e57-c0de-432d-e2701ced4688",
		"area     2048 m²",
		"prims    486 of 937",
		// The flags word decoded.  Read the other way round this
		// parcel would allow neither flying nor scripts, which is
		// the failure this asserts against.
		"allows   group build, fly",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("parcel should say %q:\n%s", want, got)
		}
	}
}

// TestParcelTakesAPointInTheRegion: a parcel the avatar is not standing
// on is the case the remembered answer can never cover.
func TestParcelTakesAPointInTheRegion(t *testing.T) {
	x := newTestShell(t)
	answerParcel(t, x, Thrushmoor)

	got := x.do(t, "parcel 60,60")
	if !strings.Contains(got, "asked    the parcel at 60, 60") {
		t.Errorf("parcel should say which point it asked about:\n%s", got)
	}

	var asked *msg.ParcelPropertiesRequest
	for _, m := range x.grid.Sent() {
		if r, ok := m.(*msg.ParcelPropertiesRequest); ok {
			asked = r
		}
	}
	if asked == nil {
		t.Fatal("nothing was asked")
	}
	if asked.ParcelData.West != 60 || asked.ParcelData.South != 60 {
		t.Errorf("asked about %v,%v", asked.ParcelData.West, asked.ParcelData.South)
	}
}

// TestParcelRefusesAPointOutsideTheRegion: 300,300 is another region,
// and asking this one about it would answer confidently about the wrong
// land.
func TestParcelRefusesAPointOutsideTheRegion(t *testing.T) {
	x := newTestShell(t)
	if got := x.do(t, "parcel 300,300"); !strings.Contains(got, "inside the region") {
		t.Errorf("parcel should refuse a point outside the region, got %q", got)
	}
	if got := x.do(t, "parcel 60"); !strings.Contains(got, "X,Y") {
		t.Errorf("parcel should say what a point looks like, got %q", got)
	}
}

// TestParcelFallsBackToWhatTheSessionWasTold: an unanswered ask is not
// the same as knowing nothing, and the difference has to be visible or
// the remembered answer would pass for a fresh one.
func TestParcelFallsBackToWhatTheSessionWasTold(t *testing.T) {
	x := newTestShell(t)
	x.grid.land = &sl.Land{
		Told:    &sl.Told{Name: "Thrushmoor", LocalID: 5},
		Overlay: agent.OverlayFrom(nil, 0),
	}

	// Nothing answers, so the ask times out.
	got := x.do(t, "parcel")
	if !strings.Contains(got, "Thrushmoor") || !strings.Contains(got, "local    5") {
		t.Errorf("parcel should fall back to what the session was told:\n%s", got)
	}
	if !strings.Contains(got, "when it arrived") {
		t.Errorf("parcel should say the answer is the remembered one:\n%s", got)
	}
}

// overlayOf builds an overlay: the west half owned by a group, the east
// half public, with a property line down the middle.
func overlayOf() *agent.Overlay {
	sq := make([]byte, agent.OverlaySquares)
	for y := 0; y < agent.OverlayEdge; y++ {
		for x := 0; x < agent.OverlayEdge; x++ {
			b := byte(agent.OverlayPublic)
			if x < agent.OverlayEdge/2 {
				b = agent.OverlayGroup
			}
			if x == agent.OverlayEdge/2 {
				b |= agent.OverlayWestLine
			}
			sq[y*agent.OverlayEdge+x] = b
		}
	}
	return agent.OverlayFrom(sq, 0b1111)
}

// TestParcelRegionNamesWhatTheOverlayCanOnlyShape: the overlay carries
// no names, so the shapes come from it and the names from a question
// apiece.  Without them the listing is a column of areas, which answers
// "how is this region divided" and not "whose is that".
func TestParcelRegionNamesWhatTheOverlayCanOnlyShape(t *testing.T) {
	x := newTestShell(t)
	x.grid.land = &sl.Land{Overlay: overlayOf()}

	// The simulator answers about whichever half was asked: the west
	// one is the group's, the east one public.
	x.grid.onSend = func(m msg.Message) {
		r, ok := m.(*msg.ParcelPropertiesRequest)
		if !ok {
			return
		}
		name, local := "The East Half", 2
		if r.ParcelData.West < 128 {
			name, local = "The West Half", 1
		}
		x.grid.RelayEvent(t, "ParcelProperties", fmt.Sprintf(
			`<llsd><map><key>ParcelData</key><array><map>`+
				`<key>Name</key><string>%s</string>`+
				`<key>LocalID</key><integer>%d</integer>`+
				`<key>SequenceID</key><integer>%d</integer>`+
				`</map></array></map></llsd>`, name, local, r.ParcelData.SequenceID))
	}

	got := x.do(t, "parcel --region")
	if !strings.Contains(got, "2 parcels") {
		t.Errorf("a region cut in two should count two parcels:\n%s", got)
	}
	if !strings.Contains(got, "The West Half") || !strings.Contains(got, "The East Half") {
		t.Errorf("each piece should be named:\n%s", got)
	}
	if !strings.Contains(got, "group") || !strings.Contains(got, "public") {
		t.Errorf("each piece should say who holds it:\n%s", got)
	}
	// 128 by 256 metres each.
	if !strings.Contains(got, "32768 m²") {
		t.Errorf("a half of a region is 32768 m²:\n%s", got)
	}
}

// TestTwoPiecesOfOneParcelAreOneParcel: land bought either side of a
// road is one parcel, and nothing in the overlay says so -- the local
// id the answers come back with is the only thing that does.
func TestTwoPiecesOfOneParcelAreOneParcel(t *testing.T) {
	x := newTestShell(t)
	x.grid.land = &sl.Land{Overlay: overlayOf()}

	// Both halves answer with the same local id, so they are one
	// parcel that the boundary happens to run through.
	x.grid.onSend = func(m msg.Message) {
		r, ok := m.(*msg.ParcelPropertiesRequest)
		if !ok {
			return
		}
		x.grid.RelayEvent(t, "ParcelProperties", fmt.Sprintf(
			`<llsd><map><key>ParcelData</key><array><map>`+
				`<key>Name</key><string>Both Halves</string>`+
				`<key>LocalID</key><integer>7</integer>`+
				`<key>SequenceID</key><integer>%d</integer>`+
				`</map></array></map></llsd>`, r.ParcelData.SequenceID))
	}

	got := x.do(t, "parcel --region")
	if !strings.Contains(got, "1 parcels, in 2 pieces") {
		t.Errorf("two pieces of one parcel are one parcel:\n%s", got)
	}
	if !strings.Contains(got, "65536 m²") {
		t.Errorf("the pieces' areas should add up:\n%s", got)
	}
}

// TestARegionThatWillNotAnswerIsStillListed: the shapes are worth
// having on their own, and a command that refused to print them
// because the names went missing would answer nothing at all.
func TestARegionThatWillNotAnswerIsStillListed(t *testing.T) {
	x := newTestShell(t)
	x.grid.land = &sl.Land{Overlay: overlayOf()}

	got := x.do(t, "parcel --region")
	if !strings.Contains(got, "32768 m²") || !strings.Contains(got, "(no answer)") {
		t.Errorf("the shapes should still be listed:\n%s", got)
	}
	if !strings.Contains(got, "went unanswered") {
		t.Errorf("it should say the names are missing:\n%s", got)
	}
}

// answerHalves has the fake simulator name the two halves of the test
// overlay, so that a picture of it has two parcels in it.
func answerHalves(t *testing.T, x *testShell) {
	t.Helper()
	x.grid.onSend = func(m msg.Message) {
		r, ok := m.(*msg.ParcelPropertiesRequest)
		if !ok {
			return
		}
		name, local := "The East Half", 2
		if r.ParcelData.West < 128 {
			name, local = "The West Half", 1
		}
		x.grid.RelayEvent(t, "ParcelProperties", fmt.Sprintf(
			`<llsd><map><key>ParcelData</key><array><map>`+
				`<key>Name</key><string>%s</string>`+
				`<key>LocalID</key><integer>%d</integer>`+
				`<key>SequenceID</key><integer>%d</integer>`+
				`</map></array></map></llsd>`, name, local, r.ParcelData.SequenceID))
	}
}

// TestTheMapDrawsAParcelPerMark: drawing the ownership instead was the
// first attempt and it drew nothing -- every square of a region of
// Linden Homes reads "owned", so the picture was one character from
// corner to corner.  Which parcel is which is the thing a map of
// parcels is for.
func TestTheMapDrawsAParcelPerMark(t *testing.T) {
	x := newTestShell(t)
	x.grid.land = &sl.Land{Overlay: overlayOf()}
	answerHalves(t, x)

	got := x.do(t, "parcel --map --rows 8")
	if strings.Count(got, "\n") < 8 {
		t.Errorf("--map should draw the rows it was asked for:\n%s", got)
	}
	// Two parcels, two marks, and neither drawn as the other.
	first := strings.SplitN(got, "\n", 2)[0]
	if !strings.Contains(first, "a") || !strings.Contains(first, "b") {
		t.Errorf("the two halves should be drawn as different marks:\n%s", got)
	}
	for _, want := range []string{"a  The West Half", "b  The East Half", "2 parcels"} {
		if !strings.Contains(got, want) {
			t.Errorf("the key should say %q:\n%s", want, got)
		}
	}
}

// TestTheMapPaintsOnlyWhereThereIsColour: the mark has to survive the
// colour being gone -- a picture down a pipe or under NO_COLOR has
// nothing but the shape left, and one that told parcels apart by colour
// alone would then be a wash of one character.
func TestTheMapPaintsOnlyWhereThereIsColour(t *testing.T) {
	x := newTestShell(t)
	x.grid.land = &sl.Land{Overlay: overlayOf()}
	answerHalves(t, x)

	var painted, plain strings.Builder
	if err := parcelMap(context.Background(), x.Shell, &painted, 6, true); err != nil {
		t.Fatalf("parcelMap: %v", err)
	}
	if err := parcelMap(context.Background(), x.Shell, &plain, 6, false); err != nil {
		t.Fatalf("parcelMap: %v", err)
	}

	green, off := mapColours["green"], mapColourOff
	if !strings.Contains(painted.String(), green+"a"+off) {
		t.Errorf("a coloured picture should paint its marks:\n%q", painted.String())
	}
	if strings.Contains(plain.String(), "\x1b[") {
		t.Errorf("a picture with no colour should carry no escapes:\n%q", plain.String())
	}
	// The same picture either way, once the paint is taken off.
	if stripped := strings.NewReplacer(green, "", mapColours["yellow"], "",
		off, "").Replace(painted.String()); stripped != plain.String() {
		t.Errorf("colour changed the picture:\n%q\n%q", stripped, plain.String())
	}
}

// TestParcelSaysWhenTheOverlayNeverArrived: it cannot be asked for, so
// a session that missed it has missed it for good -- and an empty
// picture would look like a region with nothing in it.
func TestParcelSaysWhenTheOverlayNeverArrived(t *testing.T) {
	x := newTestShell(t)

	for _, line := range []string{"parcel --region", "parcel --map"} {
		got := x.do(t, line)
		if !strings.Contains(got, "cannot be asked for") {
			t.Errorf("%s should say the overlay is unrepeatable:\n%s", line, got)
		}
	}

	x.grid.landErr = errors.New("nothing is holding this session")
	if got := x.do(t, "parcel --region"); !strings.Contains(got, "nothing is holding this session") {
		t.Errorf("parcel should report the failure, got %q", got)
	}
}

// TestParcelRefusesTwoPicturesAtOnce.
func TestParcelRefusesTwoPicturesAtOnce(t *testing.T) {
	x := newTestShell(t)
	if got := x.do(t, "parcel --region --map"); !strings.Contains(got, "ask for one") {
		t.Errorf("got %q", got)
	}
	if got := x.do(t, "parcel --map 60,60"); !strings.Contains(got, "no point") {
		t.Errorf("got %q", got)
	}
	if got := x.do(t, "parcel --rows 8"); !strings.Contains(got, "--rows is for") {
		t.Errorf("got %q", got)
	}
}
