package slate

// Touching an area of a texture by its name, from a plain record.
// Why: doc/slate-language.md#element-records

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var (
	idElA = msg.MustParseUUID("3d4a7e57-7e57-c0de-7a43-b42a03e482bd") // a plain panel
	idElB = msg.MustParseUUID("5e557e57-7e57-c0de-d92c-9524fe83dc23") // an atlas of two versions
	idElC = msg.MustParseUUID("af8d7e57-7e57-c0de-8a8e-838be59c48ab") // recorded, never worn
	idElD = msg.MustParseUUID("fa667e57-7e57-c0de-314a-03ab9b732a8d") // worn, never recorded
)

// panelRecord is texture A: two buttons side by side, centres (0.275, 0.70)
// and (0.725, 0.70). Texture B is an atlas: the left half of the texture is
// one version of a panel and the right half another, with an ok button in
// each, a cancel button only in the left and a menu only in the right.
const panelRecord = `# invented textures for the element tests
3d4a7e57-7e57-c0de-7a43-b42a03e482bd  open-button   0.10 0.62 0.45 0.78  Open
3d4a7e57-7e57-c0de-7a43-b42a03e482bd  close-button  0.55 0.62 0.90 0.78  Close
5e557e57-7e57-c0de-d92c-9524fe83dc23  ok-button     0.05 0.40 0.20 0.60
5e557e57-7e57-c0de-d92c-9524fe83dc23  cancel-button 0.30 0.40 0.45 0.60
5e557e57-7e57-c0de-d92c-9524fe83dc23  ok-button     0.55 0.40 0.70 0.60
5e557e57-7e57-c0de-d92c-9524fe83dc23  menu          0.80 0.80 0.95 0.95
af8d7e57-7e57-c0de-8a8e-838be59c48ab  unworn        0.10 0.10 0.20 0.20
`

// writeRecord puts a record file in a directory.
func writeRecord(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// playElements runs src as a file in dir, which holds the records.
func playElements(t *testing.T, f *fakeGrid, dir, src string) *Result {
	t.Helper()
	s, err := Parse(filepath.Join(dir, "t.slate"), []byte(src))
	if err == nil {
		err = Check(s)
	}
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := run(ctx, f.session(t), s, Options{}, testCfg())
	if err != nil && res.Exit != 3 {
		t.Fatalf("Run: %v\n%s", err, res.Transcript)
	}
	return res
}

// elementWorld is a sign with the panel A on face 1 and the atlas B on face 3.
func elementWorld(t *testing.T) (*fakeGrid, string) {
	t.Helper()
	f := newGrid(t)
	f.change(101, withTexture(1, idElA))
	f.change(101, withTexture(3, idElB))
	dir := t.TempDir()
	writeRecord(t, dir, "panels.tsv", panelRecord)
	return f, dir
}

const elementsHdr = "elements \"panels.tsv\"\n"

// touchedAt is the face and the point the one touch sent.
func touchedAt(t *testing.T, f *fakeGrid, face int32, s, tt float64) {
	t.Helper()
	_, got, st := touchOf(t, f)
	if got != face || math.Abs(float64(st.X)-s) > 2e-3 || math.Abs(float64(st.Y)-tt) > 2e-3 {
		t.Errorf("touched face %d at %v %v, want face %d at %v %v", got, st.X, st.Y, face, s, tt)
	}
}

func TestAnElementOnAOneToOneFaceIsTouchedAtItsCentre(t *testing.T) {
	for _, c := range []struct {
		step string
		face int32
		s, t float64
	}{
		{`touch sign element "open-button"`, 1, 0.275, 0.70},
		{`touch sign face 1 element "close-button"`, 1, 0.725, 0.70},
	} {
		f, dir := elementWorld(t)
		res := playElements(t, f, dir, hdr+elementsHdr+c.step+"\n")
		wantExit(t, res, 0)
		touchedAt(t, f, c.face, c.s, c.t)
	}
}

func TestTheTranscriptSaysWhatWasTouched(t *testing.T) {
	f, dir := elementWorld(t)
	// A passing step prints no line of its own; a failing one prints what was sent.
	res := playElements(t, f, dir, hdr+elementsHdr+`touch sign element "open-button"`+"\nexpect say \"never\" on public from anyone within 100ms\n")
	wantExit(t, res, 1)
	re := regexp.MustCompile(`touched sign face 1 element "open-button" at 0\.27\d* 0\.7\d*`)
	if !re.MatchString(res.Transcript) {
		t.Errorf("no line matches %v:\n%s", re, res.Transcript)
	}
}

func TestAnElementOnAChildLinkIsTouchedThere(t *testing.T) {
	f, vendor, lid, _ := storeWorld(t)
	_ = vendor
	f.change(201, withTexture(0, idElA))
	_ = lid
	dir := t.TempDir()
	writeRecord(t, dir, "panels.tsv", panelRecord)
	res := playElements(t, f, dir, hdr+elementsHdr+`touch vendor link 2 face 0 element "close-button"`+"\n")
	wantExit(t, res, 0)
	if got := grabbed(f); len(got) != 1 || got[0] != 201 {
		t.Fatalf("grabbed %v", got)
	}
	touchedAtNoCount(t, f, 0, 0.725, 0.70)
}

func touchedAtNoCount(t *testing.T, f *fakeGrid, face int32, s, tt float64) {
	t.Helper()
	si := sentOf[*msg.ObjectGrab](f)[0].SurfaceInfo[0]
	if si.FaceIndex != face || math.Abs(float64(si.STCoord.X)-s) > 2e-3 || math.Abs(float64(si.STCoord.Y)-tt) > 2e-3 {
		t.Errorf("face %d at %v, want face %d at %v %v", si.FaceIndex, si.STCoord, face, s, tt)
	}
}

// The atlas on face 3 shows the left half of texture B at offset -0.25 and
// the right half at 0.25, through a repeat of 0.5 across.
func atlasFace(offS int16) func(*sl.Seen) {
	return withFace(3, func(f *sl.Face) { f.ScaleS, f.ScaleT, f.OffsetS = 0.5, 1, offS })
}

func TestAnAtlasFaceSwitchedByOffsetShowsOneVersionAtATime(t *testing.T) {
	const left, right, wrapped = int16(-8192), int16(8192), int16(24575)
	for _, c := range []struct {
		version string
		off     int16
		name    string
		s       float64 // where it is touched; 0 when it is not showing
	}{
		{"left", left, "ok-button", 0.25},
		{"left", left, "cancel-button", 0.75},
		{"left", left, "menu", 0},
		{"right", right, "ok-button", 0.25},
		{"right", right, "menu", 0.75},
		{"right", right, "cancel-button", 0},
		// An offset of 0.75 shows 1.0 to 1.5, which is the left half again.
		{"wrapped", wrapped, "ok-button", 0.25},
		{"wrapped", wrapped, "cancel-button", 0.75},
		{"wrapped", wrapped, "menu", 0},
	} {
		f, dir := elementWorld(t)
		f.change(101, atlasFace(c.off))
		res := playElements(t, f, dir, hdr+elementsHdr+`touch sign face 3 element "`+c.name+"\"\n")
		if c.s == 0 {
			wantExit(t, res, 1)
			mustHave(t, res, `element "`+c.name+`" is not showing on "Example Sign" face 3 (face 3: repeats 0.5 by 1`)
			noTouch(t, f)
			continue
		}
		wantExit(t, res, 0)
		// ok-button is at v 0.5 and menu at v 0.875.
		tt := 0.5
		if c.name == "menu" {
			tt = 0.875
		}
		touchedAt(t, f, 3, c.s, tt)
	}
}

func TestAMirroredRepeatIsMappedAsTheViewerDoes(t *testing.T) {
	for _, c := range []struct {
		scaleS, scaleT float32
		name           string
		s, t           float64
	}{
		{-1, 1, "open-button", 0.725, 0.70},
		{1, -1, "open-button", 0.275, 0.30},
		{-1, -1, "close-button", 0.275, 0.30},
		// -0.5 across shows u 0.25 to 0.75, flipped: s = (0.75-u)/0.5.
		{-0.5, 1, "open-button", 0.95, 0.70},
		{-0.5, 1, "close-button", 0.05, 0.70},
	} {
		f, dir := elementWorld(t)
		f.change(101, withFace(1, func(fc *sl.Face) { fc.ScaleS, fc.ScaleT = c.scaleS, c.scaleT }))
		res := playElements(t, f, dir, hdr+elementsHdr+`touch sign face 1 element "`+c.name+"\"\n")
		wantExit(t, res, 0)
		touchedAt(t, f, 1, c.s, c.t)
	}
}

func TestARepeatLargerThanOneInSizeIsRefused(t *testing.T) {
	for _, c := range [][2]float32{{2, 1}, {1, 2}, {-1.5, 1}, {1, -1.5}, {2.5, 2.5}} {
		f, dir := elementWorld(t)
		f.change(101, withFace(1, func(fc *sl.Face) { fc.ScaleS, fc.ScaleT = c[0], c[1] }))
		res := playElements(t, f, dir, hdr+elementsHdr+`touch sign element "open-button"`+"\n")
		wantExit(t, res, 1)
		mustHave(t, res, `face 1 of "Example Sign" repeats its texture`, "a repeat larger than 1 in size")
		noTouch(t, f)
	}
}

func TestARotatedFaceIsMappedThroughItsRotation(t *testing.T) {
	f, dir := elementWorld(t)
	// A quarter turn: the viewer's uv is (t, 1-s), so s = 1-v and t = u.
	f.change(101, withFace(1, func(fc *sl.Face) { fc.Rotation = 8192 }))
	res := playElements(t, f, dir, hdr+elementsHdr+`touch sign element "open-button"`+"\n")
	wantExit(t, res, 0)
	touchedAt(t, f, 1, 0.30, 0.275)

	// Half a turn swaps both ends.
	f, dir = elementWorld(t)
	f.change(101, withFace(1, func(fc *sl.Face) { fc.Rotation = 16384 }))
	res = playElements(t, f, dir, hdr+elementsHdr+`touch sign element "open-button"`+"\n")
	wantExit(t, res, 0)
	touchedAt(t, f, 1, 0.725, 0.30)
}

func TestElementRefusalsSendNothing(t *testing.T) {
	for _, c := range []struct {
		what  string
		setup func(*fakeGrid)
		step  string
		want  string
	}{
		{"no face has a record", func(f *fakeGrid) { f.change(101, withTexture(1, idElD)); f.change(101, withTexture(3, idElD)) },
			`touch sign element "open-button"`,
			`no face of "Example Sign" shows a texture with an element record`},
		{"a recorded texture nothing wears", func(f *fakeGrid) { f.change(101, withTexture(1, idElD)); f.change(101, withTexture(3, idElD)) },
			`touch sign face 1 element "unworn"`,
			`face 1 of "Example Sign" does not show a texture with an element record`},
		{"the name is missing", func(*fakeGrid) {},
			`touch sign face 1 element "no-such-button"`,
			`element "no-such-button" is not in the record of ` + idElA.String() + `, the texture shown on "Example Sign" face 1`},
		{"the name is missing from one texture on two faces, named once", func(f *fakeGrid) { f.change(101, withTexture(4, idElA)) },
			`touch sign element "no-such-button"`,
			`element "no-such-button" is not in the record of ` + idElA.String() + ` or ` + idElB.String() + `, the textures shown on "Example Sign" faces 1, 3, 4`},
		{"the name is on another texture", func(*fakeGrid) {},
			`touch sign face 1 element "menu"`,
			`element "menu" is not in the record of ` + idElA.String()},
		{"none is visible", func(f *fakeGrid) { f.change(101, atlasFace(-8192)) },
			`touch sign face 3 element "menu"`,
			`element "menu" is not showing on "Example Sign" face 3 (face 3: repeats 0.5 by 1, offset -0.25 0, rotation 0 degrees)`},
		{"two faces have the name", func(f *fakeGrid) { f.change(101, withTexture(4, idElA)) },
			`touch sign element "open-button"`,
			`element "open-button" is showing in 2 places on "Example Sign", faces 1, 4; write face F to choose one`},
		{"two faces have the name, one is named but has none", func(f *fakeGrid) { f.change(101, withTexture(4, idElA)) },
			`touch sign face 2 element "open-button"`,
			`face 2 of "Example Sign" does not show a texture with an element record`},
		{"a face the prim lacks", func(*fakeGrid) {},
			`touch sign face 9 element "open-button"`,
			`"Example Sign" has no face 9; it has `},
		{"planar", func(f *fakeGrid) { f.change(101, withFace(1, func(fc *sl.Face) { fc.Media = 0x02 })) },
			`touch sign element "open-button"`,
			`face 1 of "Example Sign" is planar, so its offset, repeats and rotation are not the picture on it`},
		{"animated", func(f *fakeGrid) {
			f.change(101, func(s *sl.Seen) { anim := make([]byte, 16); anim[0], anim[1] = 1, 1; s.TextureAnim = anim })
		},
			`touch sign element "open-button"`,
			`face 1 of "Example Sign" has a texture animation, so one still picture is not what it shows`},
	} {
		f, dir := elementWorld(t)
		c.setup(f)
		res := playElements(t, f, dir, hdr+elementsHdr+c.step+"\n")
		wantExit(t, res, 1)
		if !has(res, "slate: step 1: "+c.want) {
			t.Errorf("%s: no line %q:\n%s", c.what, c.want, res.Transcript)
		}
		noTouch(t, f)
	}
}

func TestOneNameInTheWindowMoreThanOnceIsAmbiguous(t *testing.T) {
	f, dir := elementWorld(t)
	writeRecord(t, dir, "panels.tsv", panelRecord+"3d4a7e57-7e57-c0de-7a43-b42a03e482bd open-button 0.1 0.1 0.2 0.2\n")
	res := playElements(t, f, dir, hdr+elementsHdr+`touch sign face 1 element "open-button"`+"\n")
	wantExit(t, res, 1)
	mustHave(t, res, `element "open-button" is showing in 2 places on "Example Sign" face 1`)
	noTouch(t, f)
}

func TestFaceChoosesBetweenTwoFacesThatShowTheName(t *testing.T) {
	f, dir := elementWorld(t)
	f.change(101, withTexture(4, idElA))
	f.change(101, withFace(4, func(fc *sl.Face) { fc.ScaleS, fc.OffsetS = -1, 0 }))
	res := playElements(t, f, dir, hdr+elementsHdr+`touch sign face 4 element "open-button"`+"\n")
	wantExit(t, res, 0)
	touchedAt(t, f, 4, 0.725, 0.70)
}

func TestElementHeadersAddUpAndTheSameTextureTwiceIsASetupError(t *testing.T) {
	f, dir := elementWorld(t)
	writeRecord(t, dir, "more.tsv", "af8d7e57-7e57-c0de-8a8e-838be59c48ab extra 0.1 0.1 0.2 0.2\n")
	sub := filepath.Join(dir, "recs")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRecord(t, sub, "b.tsv", "fa667e57-7e57-c0de-314a-03ab9b732a8d b-one 0.1 0.1 0.2 0.2\n")
	writeRecord(t, sub, "a.tsv", "fa667e57-7e57-c0de-314a-03ab9b732a8d a-one 0.3 0.3 0.4 0.4\n")
	writeRecord(t, sub, "ignored.txt", "this is not read\n")
	// Two files of one directory record one texture.
	res := playElements(t, f, dir, hdr+elementsHdr+"elements \"recs\"\n"+`touch sign face 1 element "open-button"`+"\n")
	wantExit(t, res, 3)
	mustHave(t, res, "slate: setup: elements \"recs\": texture "+idElD.String()+" is recorded in both "+filepath.Join(dir, "recs", "a.tsv")+" and "+filepath.Join(dir, "recs", "b.tsv"))

	// A texture in the file and in another header is the same error.
	f, dir = elementWorld(t)
	writeRecord(t, dir, "more.tsv", "3d4a7e57-7e57-c0de-7a43-b42a03e482bd extra 0.1 0.1 0.2 0.2\n")
	res = playElements(t, f, dir, hdr+elementsHdr+"elements \"more.tsv\"\n"+`touch sign face 1 element "open-button"`+"\n")
	wantExit(t, res, 3)
	mustHave(t, res, "is recorded in both ")

	// Different textures in two headers add up, and a directory reads its *.tsv files.
	f, dir = elementWorld(t)
	f.change(101, withTexture(2, idElD))
	sub = filepath.Join(dir, "recs")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRecord(t, sub, "d.tsv", "fa667e57-7e57-c0de-314a-03ab9b732a8d side-tab 0.2 0.2 0.4 0.4\n")
	writeRecord(t, sub, "ignored.txt", "not a record\n")
	res = playElements(t, f, dir, hdr+elementsHdr+"elements \"recs\"\n"+`touch sign element "side-tab"`+"\n")
	wantExit(t, res, 0)
	touchedAt(t, f, 2, 0.3, 0.3)
}

func TestARecordThatCannotBeReadIsASetupErrorNamingTheLine(t *testing.T) {
	f, dir := elementWorld(t)
	writeRecord(t, dir, "bad.tsv", "# one good line, then one bad\n3d4a7e57-7e57-c0de-7a43-b42a03e482bd fine 0.1 0.1 0.2 0.2\n3d4a7e57-7e57-c0de-7a43-b42a03e482bd broken 0.1 0.1 0.2\n")
	res := playElements(t, f, dir, hdr+"elements \"bad.tsv\"\n"+`touch sign element "fine"`+"\n")
	wantExit(t, res, 3)
	mustHave(t, res, filepath.Join(dir, "bad.tsv")+":3: ")
	noTouch(t, f)

	f, dir = elementWorld(t)
	res = playElements(t, f, dir, hdr+"elements \"missing.tsv\"\n"+`touch sign element "fine"`+"\n")
	wantExit(t, res, 3)
	mustHave(t, res, `slate: setup: elements "missing.tsv": `)
}

func TestTheElementStepAndHeaderCheckStatically(t *testing.T) {
	const pre = "slate 1\nobject a is \"A\"\n"
	mustCheck(t, pre+"elements \"r.tsv\"\ntouch a element \"x\"\n")
	mustCheck(t, pre+"elements \"r.tsv\"\ntouch a link 2 face 1 element \"x\"\n")
	mustCheck(t, pre+"elements \"r.tsv\"\nelements \"s\"\ntouch a face 0 element \"x\"\n")
	checkErr(t, pre+"touch a element \"x\"\n", "element needs an elements header")
	checkErr(t, pre+"elements \"r.tsv\"\ntouch a element \"\"\n", "an element name is empty")
	checkErr(t, pre+"elements \"\"\ntouch a anywhere\n", "an elements path is empty")
	checkErr(t, pre+"elements \"r.tsv\"\ntouch a face 0 at 0.5 0.5 element \"x\"\n", "element cannot be combined with at")
	checkErr(t, pre+"elements \"r.tsv\"\ntouch a button box element \"x\"\n", "element cannot be combined with button")
	checkErr(t, pre+"elements \"r.tsv\"\ntouch a anywhere element \"x\"\n", "element cannot be combined with anywhere")
	checkErr(t, pre+"elements \"r.tsv\"\ntouch a showing 3d4a7e57-7e57-c0de-7a43-b42a03e482bd element \"x\"\n", "showing cannot be combined with element")
	parseErr(t, pre+"elements \"r.tsv\"\ntouch a element x\n", "expected a string")
	parseErr(t, pre+"elements \"r.tsv\"\ntouch a element matching \"x\"\n", "matching is not legal here")
}

func TestReadingRecordLines(t *testing.T) {
	// Lines in the shape a template tool writes, tabs and all: one tab
	// between fields, six decimals, a field quoted only when it has a space.
	// The areas and numbers are invented.
	const tool = "# a template tool 0.1.0 example.png (420x300)\n" +
		"1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8\tstart\t0.100000\t0.700000\t0.300000\t0.900000\tGo\n" +
		"1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8\t\"Back Arrow\"\t0.050000\t0.400000\t0.150000\t0.600000\t<\n" +
		"1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8\t\"Info Strip\"\t0.050000\t0.050000\t0.950000\t0.250000\t\"Info Strip\"\n"
	id := msg.MustParseUUID("1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8")
	got, err := parseElements("tool.tsv", []byte(tool))
	if err != nil {
		t.Fatal(err)
	}
	as := got[id]
	var seen []string
	for _, a := range as {
		seen = append(seen, a.name+"/"+a.text)
	}
	if len(as) != 3 || strings.Join(seen, "|") != "start/Go|Back Arrow/<|Info Strip/Info Strip" ||
		as[1].u0 != 0.05 || as[1].v1 != 0.6 || as[2].line != 4 || as[2].file != "tool.tsv" {
		t.Errorf("read %+v", as)
	}
	if u, v := as[0].centre(); math.Abs(u-0.2) > 1e-9 || math.Abs(v-0.8) > 1e-9 {
		t.Errorf("centre %v %v", u, v)
	}

	for _, c := range []struct {
		what, line string
		name, text string
	}{
		{"spaces", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8   a   0.1  0.2   0.3 0.4", "a", ""},
		{"no text", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8\ta\t0\t0\t1\t1", "a", ""},
		{"an upper case id", "1B2B7E57-7E57-C0DE-E90F-3D68AD60C7A8 a 0.1 0.1 0.2 0.2", "a", ""},
		{"integers and long decimals", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a 0 0.123456789012 1 1", "a", ""},
		{"a hash in a name", `1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 "#1 button" 0.1 0.1 0.2 0.2 "# one"`, "#1 button", "# one"},
		{"a hash in bare text", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a 0.1 0.1 0.2 0.2 #1", "a", "#1"},
		{"escapes", `1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 "say \"hi\"" 0.1 0.1 0.2 0.2 "a\\b"`, `say "hi"`, `a\b`},
		{"utf-8 text", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a 0.1 0.1 0.2 0.2 Öffnen→", "a", "Öffnen→"},
		{"a leading space", "   1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a 0.1 0.1 0.2 0.2 \r", "a", ""},
	} {
		got, err := parseElements("x.tsv", []byte(c.line+"\n"))
		if err != nil {
			t.Errorf("%s: %v", c.what, err)
			continue
		}
		as := got[id]
		if len(as) != 1 || as[0].name != c.name || as[0].text != c.text {
			t.Errorf("%s: read %+v", c.what, as)
		}
	}

	for _, c := range []struct{ what, line, want string }{
		{"too few fields", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a 0.1 0.1 0.2", "the line ends before the v1"},
		{"only an id", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8", "the line ends before the element name"},
		{"a bad id", "1b2b7e57 a 0.1 0.1 0.2 0.2", "is not a UUID"},
		{"the null id", "00000000-0000-0000-0000-000000000000 a 0.1 0.1 0.2 0.2", "null key"},
		{"a quoted id", `"1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8" a 0.1 0.1 0.2 0.2`, "not quoted"},
		{"an empty name", `1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 "" 0.1 0.1 0.2 0.2`, "element name is empty"},
		{"not a number", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a 0.1 x 0.2 0.2", `v0 is "x"`},
		{"over one", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a 0.1 0.1 1.2 0.2", `u1 is "1.2"`},
		{"negative", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a -0.1 0.1 0.2 0.2", `u0 is "-0.1"`},
		{"a backwards box", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a 0.5 0.1 0.2 0.2", "empty or backwards"},
		{"an empty box", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a 0.1 0.2 0.2 0.2", "empty or backwards"},
		{"text with spaces unquoted", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a 0.1 0.1 0.2 0.2 two words", "6 or 7 fields"},
		{"a name with spaces unquoted", "1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 Left Arrow 0.1 0.1 0.2 0.2", `u0 is "Arrow"`},
		{"an unclosed quote", `1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 "a 0.1 0.1 0.2 0.2`, "a quote is not closed"},
		{"text after a quote", `1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 "a"b 0.1 0.1 0.2 0.2`, "ends at white space"},
		{"a bad escape", `1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 "a\n" 0.1 0.1 0.2 0.2`, "backslash"},
		{"a bare quote", `1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a"b 0.1 0.1 0.2 0.2`, "is quoted"},
		{"a bare backslash", `1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a\b 0.1 0.1 0.2 0.2`, "is quoted"},
		{"a quoted number", `1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8 a "0.1" 0.1 0.2 0.2`, `u0 is "0.1"`},
	} {
		_, err := parseElements("bad.tsv", []byte("# first\n\n"+c.line+"\n"))
		if err == nil || !strings.HasPrefix(err.Error(), "bad.tsv:3: ") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want bad.tsv:3 and %q", c.what, err, c.want)
		}
	}
}

func TestANameMayRepeatInATextureAndAFileMayHoldSeveral(t *testing.T) {
	got, err := parseElements("x.tsv", []byte(panelRecord))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || len(got[idElA]) != 2 || len(got[idElB]) != 4 || len(got[idElC]) != 1 {
		t.Fatalf("read %d textures: %d %d", len(got), len(got[idElA]), len(got[idElB]))
	}
	n := 0
	for _, a := range got[idElB] {
		if a.name == "ok-button" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d ok-buttons", n)
	}
}

// The handbook's example file is read as the page says.
func TestTheHandbookExampleIsARecordTheReaderReads(t *testing.T) {
	page, err := os.ReadFile("../handbook/testable-products.html")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)<div class="name">test-textures\.tsv[^<]*</div><pre>(.*?)</pre>`).FindSubmatch(page)
	if m == nil {
		t.Fatal("the page has no test-textures.tsv example")
	}
	text := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&amp;", "&").Replace(string(m[1]))
	got, err := parseElements("test-textures.tsv", []byte(text))
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	as := got[msg.MustParseUUID("1b2b7e57-7e57-c0de-e90f-3d68ad60c7a8")]
	if len(as) != 3 || as[0].name != "open-button" || as[1].text != "Close" || as[2].name != "status-strip" {
		t.Errorf("read %+v", as)
	}
}
