package slate

// touch OBJ button against the fake grid: pictures drawn here are served
// as the textures of the faces, the way sl's tests serve one.
// Why: doc/slate-runner.md#buttons

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/imgfind"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented texture ids, one for each face of a prim that has eight.
var textureIDs = []msg.UUID{
	msg.MustParseUUID("04b77e57-7e57-c0de-0c56-7a1aa8d6713e"),
	msg.MustParseUUID("1eb07e57-7e57-c0de-a05c-16dec5414d89"),
	msg.MustParseUUID("3f217e57-7e57-c0de-0433-55d23785a7d6"),
	msg.MustParseUUID("42b87e57-7e57-c0de-ebfb-2134341a2be6"),
	msg.MustParseUUID("48d77e57-7e57-c0de-7614-bd8259225378"),
	msg.MustParseUUID("aef77e57-7e57-c0de-1c59-36deb6052bc5"),
	msg.MustParseUUID("b6547e57-7e57-c0de-7978-0cdf57a5bcf2"),
	msg.MustParseUUID("d8fd7e57-7e57-c0de-2853-21ca60d50fea"),
}

const picSide = 512

// hasAssetCap and assetCap are the fake's asset capability: it answers a
// texture it holds, by the id in the query, and 404 for another.
func (f *fakeGrid) hasAssetCap(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return name == sl.AssetCap && f.assets != nil
}

func (f *fakeGrid) assetCap(r agent.CapRequest) (*agent.CapResponse, bool) {
	if r.Cap != sl.AssetCap {
		return nil, false
	}
	q, err := url.ParseQuery(strings.TrimPrefix(r.Path, "/?"))
	if err != nil {
		return &agent.CapResponse{Status: 400}, true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if body, ok := f.assets[q.Get("texture_id")]; ok {
		return &agent.CapResponse{Status: 200, Body: body}, true
	}
	return &agent.CapResponse{Status: 404}, true
}

// paintSpec says what a prim's faces wear: pics by face, and faces that
// wear a texture without a picture to serve, planar or animated.
type paintSpec struct {
	pics     map[int]image.Image
	planar   []int
	animated []int
}

// paint dresses a prim. Every face it does not mention wears no texture.
func (f *fakeGrid) paint(t *testing.T, s *sl.Seen, p paintSpec) {
	t.Helper()
	faces := sl.PlainFaces(8)
	// A box with a cut has eight faces, which is what is painted here.
	eight := sl.DefaultShape()
	eight.CutBegin, eight.CutEnd = 0.2, 0.8
	withShape(eight)(s)
	f.mu.Lock()
	f.assets = map[string][]byte{}
	f.mu.Unlock()
	for i, pic := range p.pics {
		faces[i].Texture = textureIDs[i]
		body, err := sl.EncodeTexture(pic, sl.TextureOptions{Lossless: true})
		if err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		f.assets[textureIDs[i].String()] = body
		f.mu.Unlock()
	}
	for _, i := range p.planar {
		faces[i].Texture = textureIDs[i]
		faces[i].Media = 0x02
	}
	for _, i := range p.animated {
		faces[i].Texture = textureIDs[i]
		anim := make([]byte, 16)
		anim[0], anim[1] = 0x01, byte(i)
		s.TextureAnim = anim
	}
	te, err := sl.EncodeTextureEntry(faces)
	if err != nil {
		t.Fatal(err)
	}
	s.TextureEntry = te
}

// ----------------------------------------------------------- pictures

type canvas struct {
	m *image.Gray
}

func newCanvas() *canvas {
	m := image.NewGray(image.Rect(0, 0, picSide, picSide))
	for i := range m.Pix {
		m.Pix[i] = 255
	}
	return &canvas{m}
}

func (c *canvas) set(x, y int) {
	if x >= 0 && y >= 0 && x < picSide && y < picSide {
		c.m.Pix[y*c.m.Stride+x] = 0
	}
}

func (c *canvas) ring(cx, cy, r, stroke int) {
	for y := cy - r - stroke; y <= cy+r+stroke; y++ {
		for x := cx - r - stroke; x <= cx+r+stroke; x++ {
			if math.Abs(math.Hypot(float64(x-cx), float64(y-cy))-float64(r)) <= float64(stroke) {
				c.set(x, y)
			}
		}
	}
}

// rightArrow is a filled triangle with its point at x1.
func (c *canvas) rightArrow(x0, x1, midY, half int) {
	for x := x0; x <= x1; x++ {
		h := int(float64(half) * (1 - float64(x-x0)/float64(x1-x0)))
		for y := midY - h; y <= midY+h; y++ {
			c.set(x, y)
		}
	}
}

func (c *canvas) frame(x0, y0, x1, y1, stroke int) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if x < x0+stroke || x >= x1-stroke || y < y0+stroke || y >= y1-stroke {
				c.set(x, y)
			}
		}
	}
}

// glyphs are 9 rows high and 5 wide (the O is 7, round enough that the
// finder does not take it for a box), 'X' being ink: the word Open, with the
// tail of the p below the line.
var glyphs = map[rune][9]string{
	'O': {"..XXX..", ".X...X.", "X.....X", "X.....X", "X.....X", "X.....X", ".X...X.", "..XXX..", "......."},
	'p': {".....", "XXXX.", "X...X", "X...X", "X...X", "XXXX.", "X....", "X....", "X...."},
	'e': {".....", ".XXX.", "X...X", "XXXXX", "X....", "X....", ".XXX.", ".....", "....."},
	'n': {".....", "XXXX.", "X...X", "X...X", "X...X", "X...X", "X...X", ".....", "....."},
}

func (c *canvas) text(s string, x, y, scale int) {
	for _, r := range s {
		for ry, row := range glyphs[r] {
			for rx, ch := range row {
				if ch != 'X' {
					continue
				}
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						c.set(x+rx*scale+dx, y+ry*scale+dy)
					}
				}
			}
		}
		x += (len(glyphs[r][0]) + 1) * scale
	}
}

func oneBox() image.Image {
	c := newCanvas()
	c.frame(120, 200, 360, 340, 4)
	return c.m
}

func twoBoxes() image.Image {
	c := newCanvas()
	c.frame(60, 40, 300, 180, 4)
	c.frame(60, 280, 300, 420, 4)
	return c.m
}

// ringArrow is a ring with a right arrow inside it, and a box apart.
func ringArrow() image.Image {
	c := newCanvas()
	c.ring(150, 150, 60, 4)
	c.rightArrow(120, 185, 150, 28)
	c.frame(260, 330, 480, 450, 4)
	return c.m
}

// openBox is the word Open inside a wide box. A thin outline: the OCR
// does not read a word inside a thick one.
func openBox() image.Image {
	c := newCanvas()
	c.frame(100, 150, 400, 255, 2)
	c.text("Open", 150, 175, 5)
	return c.m
}

// ------------------------------------------------------------ the runs

// button runs one step on the sign, with the temp directory a fresh one.
func button(t *testing.T, f *fakeGrid, p paintSpec, step string) (*Result, string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	f.paint(t, f.objects[0], p)
	return play(t, f, hdr+step+"\n"), tmp
}

// find is what the finder says about a picture, for the expected click.
func find(t *testing.T, pic image.Image, reqs ...*imgfind.Request) []*imgfind.Item {
	t.Helper()
	items, err := imgfind.Find(pic, reqs...)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// st is the surface coordinate of the middle of a pixel, worked out
// again here from the formula of doc/slate-runner.md#buttons.
func st(x, y int) msg.Vector3 {
	return msg.Vector3{
		X: float32((float64(x) + 0.5) / picSide),
		Y: float32(1 - (float64(y)+0.5)/picSide),
	}
}

// wantTouch is the one touch the fake received.
func wantTouch(t *testing.T, f *fakeGrid, face int32, want msg.Vector3) {
	t.Helper()
	grabs, degrabs := sentOf[*msg.ObjectGrab](f), sentOf[*msg.ObjectDeGrab](f)
	if len(grabs) != 1 || len(degrabs) != 1 || len(grabs[0].SurfaceInfo) != 1 {
		t.Fatalf("%d grabs, %d degrabs", len(grabs), len(degrabs))
	}
	si := grabs[0].SurfaceInfo[0]
	if si.FaceIndex != face || si.STCoord != want {
		t.Errorf("touched face %d st %v, want face %d st %v", si.FaceIndex, si.STCoord, face, want)
	}
}

func dump(items []*imgfind.Item) string {
	var parts []string
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%q %v", it.Contents, it.Location))
	}
	return strings.Join(parts, "; ")
}

func wantNothingSent(t *testing.T, f *fakeGrid) {
	t.Helper()
	if n := len(sentOf[*msg.ObjectGrab](f)); n != 0 {
		t.Errorf("%d grabs were sent", n)
	}
}

// leftOver is what is in the temp directory.
func leftOver(t *testing.T, tmp string) []string {
	t.Helper()
	var out []string
	filepath.Walk(tmp, func(p string, _ os.FileInfo, _ error) error {
		if p != tmp {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func wantClean(t *testing.T, tmp string) {
	t.Helper()
	if got := leftOver(t, tmp); len(got) != 0 {
		t.Errorf("left in the temp directory: %v", got)
	}
}

// ----------------------------------------------------------- the tests

func TestASingleBoxIsClickedAtItsCentre(t *testing.T) {
	f := newGrid(t)
	pic := oneBox()
	items := find(t, pic, imgfind.Box())
	if len(items) != 1 {
		t.Fatalf("the finder sees %d boxes", len(items))
	}
	res, tmp := button(t, f, paintSpec{pics: map[int]image.Image{0: pic}}, "touch sign button box")
	wantExit(t, res, 0)
	c := items[0].Center
	if c.X < 238 || c.X > 242 || c.Y < 268 || c.Y > 272 {
		t.Fatalf("centre %v is not the middle of the box drawn", c)
	}
	wantTouch(t, f, 0, st(c.X, c.Y))
	mustHave(t, res, "slate: pass step 1")
	wantClean(t, tmp)
}

func TestACircleWithAnArrowInsideClicksTheIntersection(t *testing.T) {
	f := newGrid(t)
	pic := ringArrow()
	by := find(t, pic, imgfind.Drawing("circle"), imgfind.Drawing("right arrow"))
	var ring, arrow *imgfind.Item
	for _, it := range by {
		if it.Request.Drawing == "circle" {
			ring = it
		} else {
			arrow = it
		}
	}
	if ring == nil || arrow == nil {
		t.Fatalf("the finder sees no ring or arrow: %v", by)
	}
	// The arrow lies inside the ring, so the intersection is the arrow.
	res, tmp := button(t, f, paintSpec{pics: map[int]image.Image{0: pic}}, `touch sign button circle symbol "right arrow"`)
	wantExit(t, res, 0)
	wantTouch(t, f, 0, st(arrow.Center.X, arrow.Center.Y))
	wantClean(t, tmp)

	// Written the other way round it is the same tuple and the same click.
	f = newGrid(t)
	res, _ = button(t, f, paintSpec{pics: map[int]image.Image{0: pic}}, `touch sign button symbol "right arrow" circle`)
	wantExit(t, res, 0)
	wantTouch(t, f, 0, st(arrow.Center.X, arrow.Center.Y))
}

func TestTwoBoxesWithoutANumberFailListingBothAndSendNothing(t *testing.T) {
	f := newGrid(t)
	pic := twoBoxes()
	items := find(t, pic, imgfind.Box())
	if len(items) != 2 {
		t.Fatalf("the finder sees %d boxes", len(items))
	}
	res, tmp := button(t, f, paintSpec{pics: map[int]image.Image{0: pic}}, "touch sign button box")
	wantExit(t, res, 1)
	wantNothingSent(t, f)
	for _, it := range items {
		mustHave(t, res, strings.Join([]string{"face 0 at", itoa(it.Center.X) + "," + itoa(it.Center.Y)}, " "))
	}
	mustHave(t, res, "slate: step 1: button matches 2 times")
	mustNotHave(t, res, "slate: pass step 1")
	checkKept(t, res, tmp, 0)
}

// checkKept: the failure printed the path of each face's picture, which
// is a PNG of the picture's size in a directory made for it.
func checkKept(t *testing.T, res *Result, tmp string, faces ...int) {
	t.Helper()
	re := regexp.MustCompile(`face (\d) picture: (\S+)`)
	got := map[int]string{}
	for _, m := range re.FindAllStringSubmatch(res.Transcript, -1) {
		got[int(m[1][0]-'0')] = m[2]
	}
	if len(got) != len(faces) {
		t.Fatalf("paths printed for %v, want faces %v:\n%s", got, faces, res.Transcript)
	}
	for _, face := range faces {
		path := got[face]
		if !strings.HasPrefix(path, tmp) || filepath.Base(path) != itoa(face)+".png" {
			t.Errorf("face %d picture at %q, want N.png under %s", face, path, tmp)
		}
		fh, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := png.DecodeConfig(fh)
		fh.Close()
		if err != nil || cfg.Width != picSide || cfg.Height != picSide {
			t.Errorf("face %d: %+v, %v", face, cfg, err)
		}
	}
}

func TestANumberPicksThatMatchInReadingOrder(t *testing.T) {
	pic := twoBoxes()
	items := find(t, pic, imgfind.Box())
	for n, want := range []*imgfind.Item{items[0], items[1]} {
		f := newGrid(t)
		step := "touch sign button " + itoa(n+1) + " box"
		res, tmp := button(t, f, paintSpec{pics: map[int]image.Image{0: pic}}, step)
		wantExit(t, res, 0)
		wantTouch(t, f, 0, st(want.Center.X, want.Center.Y))
		wantClean(t, tmp)
	}
	// Past the last one is a failure that says how many there were.
	f := newGrid(t)
	res, tmp := button(t, f, paintSpec{pics: map[int]image.Image{0: pic}}, "touch sign button 3 box")
	wantExit(t, res, 1)
	wantNothingSent(t, f)
	mustHave(t, res, "button 3:", "has 2 matches")
	checkKept(t, res, tmp, 0)
}

func TestFaceRestrictsAndNumbersRunAcrossFaces(t *testing.T) {
	one, two := oneBox(), twoBoxes()
	p := paintSpec{pics: map[int]image.Image{1: one, 4: two}}
	oneC := find(t, one, imgfind.Box())[0].Center
	twoC := find(t, two, imgfind.Box())

	// Three boxes on the prim, so none without a number.
	f := newGrid(t)
	res, _ := button(t, f, p, "touch sign button box")
	wantExit(t, res, 1)
	mustHave(t, res, "button matches 3 times")
	wantNothingSent(t, f)

	f = newGrid(t)
	res, tmp := button(t, f, p, "touch sign button box face 1")
	wantExit(t, res, 0)
	wantTouch(t, f, 1, st(oneC.X, oneC.Y))
	wantClean(t, tmp)

	f = newGrid(t)
	res, _ = button(t, f, p, "touch sign button 2 box face 4")
	wantExit(t, res, 0)
	wantTouch(t, f, 4, st(twoC[1].Center.X, twoC[1].Center.Y))

	// Faces are in index order and their tuples are joined: the second
	// is the first of face 4, and the third its second.
	f = newGrid(t)
	res, _ = button(t, f, p, "touch sign button 2 box")
	wantExit(t, res, 0)
	wantTouch(t, f, 4, st(twoC[0].Center.X, twoC[0].Center.Y))
	f = newGrid(t)
	res, _ = button(t, f, p, "touch sign button 3 box")
	wantExit(t, res, 0)
	wantTouch(t, f, 4, st(twoC[1].Center.X, twoC[1].Center.Y))

	// A face the prim has none of.
	f = newGrid(t)
	res, _ = button(t, f, p, "touch sign button box face 9")
	wantExit(t, res, 1)
	mustHave(t, res, `"Example Sign" has no face 9; it has 8`)
	wantNothingSent(t, f)
}

func TestAFaceWithNoTextureIsSkipped(t *testing.T) {
	f := newGrid(t)
	pic := oneBox()
	c := find(t, pic, imgfind.Box())[0].Center
	res, tmp := button(t, f, paintSpec{pics: map[int]image.Image{3: pic}}, "touch sign button box")
	wantExit(t, res, 0)
	wantTouch(t, f, 3, st(c.X, c.Y))
	wantClean(t, tmp)

	// None textured: nothing to search, and nothing to write.
	f = newGrid(t)
	res, tmp = button(t, f, paintSpec{}, "touch sign button box")
	wantExit(t, res, 1)
	mustHave(t, res, `no face of "Example Sign" has a texture`)
	wantNothingSent(t, f)
	wantClean(t, tmp)
}

func TestAPlanarOrAnimatedFaceElsewhereFailsEverySearch(t *testing.T) {
	pic := oneBox()
	for _, c := range []struct {
		what string
		spec paintSpec
		want string
	}{
		{"planar", paintSpec{pics: map[int]image.Image{0: pic}, planar: []int{2}},
			`slate: step 1: face 2 of "Example Sign" is planar, so its offset, repeats and rotation are not the picture on it`},
		{"animated", paintSpec{pics: map[int]image.Image{0: pic}, animated: []int{2}},
			`slate: step 1: face 2 of "Example Sign" has a texture animation, so one still picture is not what it shows`},
	} {
		for _, step := range []string{"touch sign button box", "touch sign button box face 0", "touch sign button 1 box face 0"} {
			f := newGrid(t)
			res, tmp := button(t, f, c.spec, step)
			wantExit(t, res, 1)
			wantNothingSent(t, f)
			if !has(res, c.want) {
				t.Errorf("%s, %q: no line %q:\n%s", c.what, step, c.want, res.Transcript)
			}
			wantClean(t, tmp)
		}
	}
}

func TestImageAndOvalFailAtOnceWithTheirSentences(t *testing.T) {
	const (
		img  = "slate: step 1: button part image is allowed, and the finder does not match one; it matches text, a pattern, a drawing (circle, arrow, and the four directions), and an outlined box"
		oval = "slate: step 1: button part oval is allowed, and the finder does not match one; a round ring is circle, and a wide outlined control is box"
	)
	for _, c := range []struct{ step, want string }{
		{`touch sign button image "logo"`, img},
		{`touch sign button oval`, oval},
		{`touch sign button box image "logo"`, img}, // the box would have matched
		{`touch sign button 1 box oval`, oval},
		{`touch sign button oval image "logo"`, oval},
	} {
		f := newGrid(t)
		res, tmp := button(t, f, paintSpec{pics: map[int]image.Image{0: oneBox()}}, c.step)
		wantExit(t, res, 1)
		wantNothingSent(t, f)
		if !has(res, c.want) {
			t.Errorf("%s: no line %q:\n%s", c.step, c.want, res.Transcript)
		}
		for _, l := range lines(res) {
			if strings.HasPrefix(l, "slate: step") && !strings.Contains(img+oval, strings.TrimPrefix(l, "slate: step 1: ")) {
				t.Errorf("%s: another sentence: %s", c.step, l)
			}
		}
		wantClean(t, tmp)
	}
}

func TestNoMatchNamesTheMissingPart(t *testing.T) {
	f := newGrid(t)
	pic := ringArrow()
	res, tmp := button(t, f, paintSpec{pics: map[int]image.Image{0: pic}}, `touch sign button circle symbol "up arrow"`)
	wantExit(t, res, 1)
	wantNothingSent(t, f)
	mustHave(t, res, `button part symbol "up arrow" matched nothing on "Example Sign"`)
	checkKept(t, res, tmp, 0)

	// Every part is there and no two overlap: the ring and the box apart.
	f = newGrid(t)
	res, _ = button(t, f, paintSpec{pics: map[int]image.Image{0: pic}}, "touch sign button circle box")
	wantExit(t, res, 1)
	wantNothingSent(t, f)
	mustHave(t, res, "every button part matched", "no items of all the parts overlap")

	// A drawing the finder does not know is the finder's error.
	f = newGrid(t)
	res, tmp = button(t, f, paintSpec{pics: map[int]image.Image{0: pic}}, `touch sign button symbol "square"`)
	wantExit(t, res, 1)
	wantNothingSent(t, f)
	mustHave(t, res, "unknown drawing")
	checkKept(t, res, tmp, 0)
}

func TestPicturesAreKeptOnlyOnFailureOnEveryTexturedFace(t *testing.T) {
	f := newGrid(t)
	p := paintSpec{pics: map[int]image.Image{0: twoBoxes(), 5: oneBox()}}
	res, tmp := button(t, f, p, "touch sign button symbol \"left arrow\"")
	wantExit(t, res, 1)
	checkKept(t, res, tmp, 0, 5)
	// One fresh directory under the temp directory, holding the PNGs.
	if got := leftOver(t, tmp); len(got) != 3 {
		t.Errorf("left %v, want a directory and two pictures", got)
	}

	f = newGrid(t)
	res, tmp = button(t, f, p, "touch sign button box face 5")
	wantExit(t, res, 0)
	wantClean(t, tmp)
}

func TestAMissingTesseractFailsATextPartWithItsMessage(t *testing.T) {
	f := newGrid(t)
	t.Setenv("PATH", t.TempDir())
	res, _ := button(t, f, paintSpec{pics: map[int]image.Image{0: openBox()}}, `touch sign button text "Open"`)
	wantExit(t, res, 1)
	wantNothingSent(t, f)
	mustHave(t, res, "tesseract")
}

func TestTextInsideABoxClicksTheIntersection(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not on PATH")
	}
	pic := openBox()
	by := find(t, pic, imgfind.Text("Open"), imgfind.Box())
	var word *imgfind.Item
	for _, it := range by {
		if it.Request.Kind == imgfind.KindText {
			word = it
		}
	}
	if word == nil {
		t.Fatalf("tesseract does not read the drawn word: %s", dump(by))
	}
	// The word lies inside the box, so the intersection is the word's rectangle.
	f := newGrid(t)
	res, tmp := button(t, f, paintSpec{pics: map[int]image.Image{0: pic}}, `touch sign button text "Open" box`)
	wantExit(t, res, 0)
	wantTouch(t, f, 0, st(word.Center.X, word.Center.Y))
	wantClean(t, tmp)

	f = newGrid(t)
	res, _ = button(t, f, paintSpec{pics: map[int]image.Image{0: pic}}, `touch sign button pattern "^Op" box`)
	wantExit(t, res, 0)
	wantTouch(t, f, 0, st(word.Center.X, word.Center.Y))

	// The box alone is a different click.
	f = newGrid(t)
	res, _ = button(t, f, paintSpec{pics: map[int]image.Image{0: pic}}, "touch sign button box")
	wantExit(t, res, 0)
	boxC := find(t, pic, imgfind.Box())[0].Center
	wantTouch(t, f, 0, st(boxC.X, boxC.Y))
}

// The step says which face was clicked and where, in its transcript.
func TestAButtonStepReportsWhereItClicked(t *testing.T) {
	f := newGrid(t)
	pic := oneBox()
	c := find(t, pic, imgfind.Box())[0].Center
	f.paint(t, f.objects[0], paintSpec{pics: map[int]image.Image{2: pic}})
	s := mustCheck(t, hdr+"touch sign button box\nexpect say \"never\" on public from anyone within 100ms\n")
	t.Setenv("TMPDIR", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 20e9)
	defer cancel()
	res, err := run(ctx, f.session(t), s, Options{}, testCfg())
	if err != nil {
		t.Fatal(err)
	}
	wantExit(t, res, 1)
	mustHave(t, res, "touched sign face 2 button at "+itoa(c.X)+","+itoa(c.Y))
}
