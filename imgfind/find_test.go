package imgfind

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFiguresOnADarkPage(t *testing.T) {
	const w, h = 220, 220
	m := image.NewGray(image.Rect(0, 0, w, h))
	set := func(x, y int) {
		if x >= 0 && y >= 0 && x < w && y < h {
			m.Pix[y*m.Stride+x] = 255
		}
	}
	ring(set, 70, 70, 40, 3)
	frame(set, 20, 140, 180, 200, 3)
	circ := Drawing("circle")
	boxes := Box()
	got, err := Find(m, circ, boxes)
	if err != nil {
		t.Fatal(err)
	}
	by := group(got)
	if len(by[circ]) != 1 || dist(by[circ][0].Center, Point{70, 70}) > 8 {
		t.Fatalf("circle: %s", dump(by[circ]))
	}
	if len(by[boxes]) != 1 {
		t.Fatalf("boxes: %s", dump(by[boxes]))
	}
}

func TestAMissingFileIsAnError(t *testing.T) {
	_, err := FindFile(filepath.Join(t.TempDir(), "no-such.png"), Text("Menu"))
	if err == nil || !strings.HasPrefix(err.Error(), "imgfind:") {
		t.Fatalf("missing image: %v", err)
	}
}

func TestAFileIsDecodedAndFound(t *testing.T) {
	const w, h = 220, 220
	m := image.NewGray(image.Rect(0, 0, w, h))
	set := func(x, y int) {
		if x >= 0 && y >= 0 && x < w && y < h {
			m.Pix[y*m.Stride+x] = 255
		}
	}
	ring(set, 70, 70, 40, 3)
	dir := t.TempDir()
	path := filepath.Join(dir, "ring.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, m); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := FindFile(path, Drawing("circle"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || dist(got[0].Center, Point{70, 70}) > 8 {
		t.Fatalf("circle: %s", dump(got))
	}
	text := filepath.Join(dir, "not-a-picture.png")
	if err := os.WriteFile(text, []byte("not a picture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := FindFile(text, Box()); err == nil {
		t.Fatal("a file that is no picture was decoded")
	}
	// A bad request fails before the file is opened.
	if _, err := FindFile(filepath.Join(dir, "no-such.png"), Drawing("a star")); err == nil ||
		!strings.Contains(err.Error(), "unknown drawing") {
		t.Fatalf("a bad request on a missing file: %v", err)
	}
}

func TestNoImageIsAnError(t *testing.T) {
	if _, err := Find(nil, Box()); err == nil {
		t.Fatal("a nil image was searched")
	}
	if _, err := Find(image.NewGray(image.Rect(0, 0, 0, 5)), Box()); err == nil {
		t.Fatal("an empty image was searched")
	}
}

func TestABadRequestIsAnError(t *testing.T) {
	blank := blankImage(16, 16)

	cases := []*Request{
		nil,
		Text("  "),
		Pattern("["),
		Drawing("a star"),
		{Kind: Kind(9)},
	}
	for _, req := range cases {
		_, err := Find(blank, req)
		if err == nil {
			t.Fatalf("request %+v returned no error", req)
		}
	}
}

func TestNothingFoundIsNotAnError(t *testing.T) {
	requireTesseract(t)
	got, err := Find(blankImage(80, 80), Text("Menu"), Box())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("blank image returned %+v", got)
	}
}

func TestADrawnPicture(t *testing.T) {
	requireTesseract(t)
	scene := drawScene()

	menu := Text("Menu")
	hide := Text("Hide")
	circ := Drawing("circle")
	arrows := Drawing("arrow")
	right := Drawing("Right Arrow")
	boxes := Box()
	pat := Pattern(`^M`)

	got, err := Find(scene, menu, hide, circ, arrows, right, boxes, pat)
	if err != nil {
		t.Fatal(err)
	}
	by := group(got)

	menus := by[menu]
	if len(menus) != 1 || menus[0].Contents != "Menu" || menus[0].Request != menu {
		t.Fatalf("Menu: %s", dump(menus))
	}
	if menus[0].Center != (Point{menus[0].Location.X + menus[0].Width/2, menus[0].Location.Y + menus[0].Height/2}) {
		t.Fatalf("center is not the middle of the box: %+v", menus[0])
	}
	if len(by[hide]) != 0 {
		t.Fatalf("Hide should be absent, got %s", dump(by[hide]))
	}

	if len(by[circ]) != 1 || by[circ][0].Contents != "circle" {
		t.Fatalf("circle: %+v", dump(by[circ]))
	}
	if d := dist(by[circ][0].Center, Point{90, 90}); d > 8 {
		t.Fatalf("circle center %v, want near (90,90)", by[circ][0].Center)
	}

	if len(by[arrows]) != 3 {
		t.Fatalf("arrows: %+v", dump(by[arrows]))
	}
	dirs := map[string]int{}
	for _, it := range by[arrows] {
		dirs[it.Contents]++
		if it.Request != arrows {
			t.Fatalf("arrow request pointer: %+v", it)
		}
	}
	if dirs["right arrow"] != 1 || dirs["left arrow"] != 1 || dirs["up arrow"] != 1 {
		t.Fatalf("directions %v from %+v", dirs, dump(by[arrows]))
	}
	if len(by[right]) != 1 || by[right][0].Contents != "right arrow" {
		t.Fatalf("right arrow: %+v", dump(by[right]))
	}

	if len(by[boxes]) != 1 || by[boxes][0].Contents != "outlined box" {
		t.Fatalf("boxes: %+v", dump(by[boxes]))
	}
	b := by[boxes][0]
	if b.Width < 200 || b.Height < 100 || b.Location.X < 400 {
		t.Fatalf("box geometry %+v", b)
	}

	// ^M is a prefix of the one word on the page.
	if len(by[pat]) != 1 || by[pat][0].Contents != "Menu" || by[pat][0].Request != pat {
		t.Fatalf("pattern: %s", dump(by[pat]))
	}
}

func group(items []*Item) map[*Request][]*Item {
	out := map[*Request][]*Item{}
	for _, it := range items {
		out[it.Request] = append(out[it.Request], it)
	}
	return out
}

func dist(a, b Point) float64 {
	return math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y))
}

func dump(items []*Item) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = fmt.Sprintf("%q %d,%d %dx%d center %d,%d",
			it.Contents, it.Location.X, it.Location.Y, it.Width, it.Height, it.Center.X, it.Center.Y)
	}
	return strings.Join(parts, "; ")
}

func requireTesseract(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not on PATH")
	}
}

func blankImage(w, h int) *image.Gray {
	m := image.NewGray(image.Rect(0, 0, w, h))
	for i := range m.Pix {
		m.Pix[i] = 255
	}
	return m
}

func drawScene() *image.Gray {
	const w, h = 800, 560
	m := image.NewGray(image.Rect(0, 0, w, h))
	for i := range m.Pix {
		m.Pix[i] = 255
	}
	set := func(x, y int) {
		if x >= 0 && y >= 0 && x < w && y < h {
			m.Pix[y*m.Stride+x] = 0
		}
	}
	ring(set, 90, 90, 46, 4)
	triHorizontal(set, 200, 340, 90, 50, true)
	triHorizontal(set, 480, 640, 90, 50, false)
	triVertical(set, 90, 220, 340, 45, true)
	frame(set, 400, 200, 640, 340, 4)
	drawText(m, "Menu", 400, 380, 8)

	return m
}

func ring(set func(int, int), cx, cy, r, stroke int) {
	for y := cy - r - stroke; y <= cy+r+stroke; y++ {
		for x := cx - r - stroke; x <= cx+r+stroke; x++ {
			d := math.Hypot(float64(x-cx), float64(y-cy))
			if math.Abs(d-float64(r)) <= float64(stroke) {
				set(x, y)
			}
		}
	}
}

// triHorizontal draws a filled triangle. tipRight puts the point at x1.
func triHorizontal(set func(int, int), x0, x1, midY, half int, tipRight bool) {
	span := x1 - x0
	if span < 1 {
		return
	}
	for x := x0; x <= x1; x++ {
		t := float64(x-x0) / float64(span)
		if tipRight {
			t = 1 - t
		}
		h := int(float64(half) * t)
		for y := midY - h; y <= midY+h; y++ {
			set(x, y)
		}
	}
}

// triVertical draws a filled triangle. tipUp puts the point at y0,
// the top, and the base at y1.
func triVertical(set func(int, int), midX, y0, y1, half int, tipUp bool) {
	span := y1 - y0
	if span < 1 {
		return
	}
	for y := y0; y <= y1; y++ {
		// 0 at the top. An up arrow is the point, so its width grows
		// downward; a down arrow is the other way around.
		t := float64(y-y0) / float64(span)
		if !tipUp {
			t = 1 - t
		}
		hh := int(float64(half) * t)
		for x := midX - hh; x <= midX+hh; x++ {
			set(x, y)
		}
	}
}

func frame(set func(int, int), x0, y0, x1, y1, stroke int) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if x < x0+stroke || x >= x1-stroke || y < y0+stroke || y >= y1-stroke {
				set(x, y)
			}
		}
	}
}

// glyph5 is a 5-wide, 7-row letter. 'X' is ink. Enough for the words
// the drawn-picture test asks tesseract to read.
var glyph5 = map[rune][7]string{
	'M': {"X...X", "XX.XX", "X.X.X", "X...X", "X...X", "X...X", "X...X"},
	'e': {".....", ".XXX.", "X...X", "XXXXX", "X....", "X....", ".XXX."},
	'n': {".....", "XXXX.", "X...X", "X...X", "X...X", "X...X", "X...X"},
	'u': {".....", "X...X", "X...X", "X...X", "X...X", "X..XX", ".XXX."},
}

func drawText(m *image.Gray, s string, x, y, scale int) {
	for _, r := range s {
		if r == ' ' {
			x += 4 * scale
			continue
		}
		rows, ok := glyph5[r]
		if !ok {
			x += 6 * scale
			continue
		}
		for ry, row := range rows {
			for rx, ch := range row {
				if ch != 'X' {
					continue
				}
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						px := x + rx*scale + dx
						py := y + ry*scale + dy
						if px >= 0 && py >= 0 && px < m.Rect.Dx() && py < m.Rect.Dy() {
							m.Pix[py*m.Stride+px] = 0
						}
					}
				}
			}
		}
		x += 6 * scale
	}
}

// TestPhrasesJoinWordsOnOneLine needs no tesseract: it is the step after
// the reading. Two words a space apart are one line, and two a button
// apart are not.
// Why: doc/imgfind.md#how-text-is-read
func TestPhrasesJoinWordsOnOneLine(t *testing.T) {
	words := []span{
		{text: "Example", x: 100, y: 100, w: 200, h: 50},
		{text: "Menu", x: 320, y: 102, w: 120, h: 50},
		{text: "Close", x: 700, y: 100, w: 120, h: 50},
	}
	got := phrases(words)
	if len(got) != 1 || got[0].text != "Example Menu" {
		t.Fatalf("phrases = %+v, want only Example Menu", got)
	}
	if got[0].x != 100 || got[0].w != 340 {
		t.Errorf("the phrase spans %d wide from %d, want 340 from 100", got[0].w, got[0].x)
	}
}
