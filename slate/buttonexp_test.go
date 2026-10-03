package slate

import (
	"image"
	"os/exec"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/imgfind"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

func init() {
	// The letters of Close that button_test.go does not draw.
	glyphs['C'] = [9]string{"..XXX.", ".X...X", "X.....", "X.....", "X.....", "X.....", ".X...X", "..XXX.", "......"}
	glyphs['l'] = [9]string{"XX", ".X", ".X", ".X", ".X", ".X", ".X", "XXX", ".."}
	glyphs['o'] = [9]string{".....", ".....", ".XXX.", "X...X", "X...X", "X...X", ".XXX.", ".....", "....."}
	glyphs['s'] = [9]string{".....", ".....", ".XXXX", "X....", ".XXX.", "....X", "XXXX.", ".....", "....."}
}

func closeBox() image.Image {
	c := newCanvas()
	c.frame(100, 150, 400, 255, 2)
	c.text("Close", 130, 175, 5)
	return c.m
}

func needOCR(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract is not installed")
	}
}

func threeBoxes() image.Image {
	c := newCanvas()
	c.frame(20, 20, 160, 100, 3)
	c.frame(20, 200, 160, 280, 3)
	c.frame(20, 380, 160, 460, 3)
	return c.m
}

// serve adds a picture to the textures the fake answers with.
func (f *fakeGrid) serve(t *testing.T, id msg.UUID, pic image.Image) {
	t.Helper()
	body, err := sl.EncodeTexture(pic, sl.TextureOptions{Lossless: true})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.assets[id.String()] = body
	f.mu.Unlock()
}

// swapOnGo makes the sign's face 0 wear the picture served as textureIDs[7]
// shortly after the test says go.
func swapOnGo(t *testing.T, f *fakeGrid, pic image.Image) {
	t.Helper()
	f.serve(t, textureIDs[7], pic)
	f.whenSaid("go", func() { f.changeAfter(t, 150*time.Millisecond, signLocal, withTexture(0, textureIDs[7])) })
}

func runBtn(t *testing.T, f *fakeGrid, p paintSpec, cfg runCfg, src string) *Result {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	f.paint(t, f.objects[0], p)
	return playWith(t, f, hdr+src, Options{}, cfg)
}

func face0(pic image.Image) paintSpec { return paintSpec{pics: map[int]image.Image{0: pic}} }

func TestButtonCloseBecomesGoneAndOpenBecomesShown(t *testing.T) {
	needOCR(t)
	f := newGrid(t)
	f.paint(t, f.objects[0], face0(closeBox()))
	swapOnGo(t, f, openBox())
	res := playWith(t, f, hdr+`say "go" on 0
expect button sign text "Close" box becomes gone within 5s
expect button sign text "Open" box becomes shown within 5s
`, Options{}, testCfg())
	wantExit(t, res, 0)
	mustHave(t, res, `button sign text "Close" box 1 (faces 0)`, `button sign text "Close" box 0 (faces none)`,
		`button sign text "Open" box 1 (faces 0)`)
	// Close never leaves: becomes gone fails.
	f = newGrid(t)
	res = runBtn(t, f, face0(closeBox()), testCfg(), `expect button sign text "Close" box becomes gone within 600ms
`)
	wantExit(t, res, 1)
	mustHave(t, res, `unmatched button sign text "Close" box becomes gone`)
}

func TestNegativeButtonReadings(t *testing.T) {
	needOCR(t)
	f := newGrid(t)
	f.paint(t, f.objects[0], face0(closeBox()))
	swapOnGo(t, f, openBox())
	res := playWith(t, f, hdr+`say "go" on 0
expect button sign text "Close" box becomes gone within 5s
then expect no button sign text "Close" box is shown within 400ms
`, Options{}, testCfg())
	wantExit(t, res, 0)
	f = newGrid(t)
	res = runBtn(t, f, face0(closeBox()), testCfg(), `expect no button sign text "Close" box is shown within 400ms
`)
	wantExit(t, res, 1)
	mustHave(t, res, `forbidden no button sign text "Close" box is shown`)
}

func TestButtonCountAndChanges(t *testing.T) {
	needOCR(t)
	f := newGrid(t)
	f.paint(t, f.objects[0], face0(twoBoxes()))
	swapOnGo(t, f, threeBoxes())
	res := playWith(t, f, hdr+`expect button sign box is count 2 within 2s
say "go" on 0
expect button sign box becomes count 3 within 5s as $n
`, Options{}, testCfg())
	wantExit(t, res, 0)
	mustHave(t, res, "button sign box 2 (faces 0)", "button sign box 3 (faces 0)", "capture $n = 3")
	f = newGrid(t)
	f.paint(t, f.objects[0], face0(twoBoxes()))
	swapOnGo(t, f, threeBoxes())
	res = playWith(t, f, hdr+`say "go" on 0
expect button sign box changes within 5s
`, Options{}, testCfg())
	wantExit(t, res, 0)
	// A count that is not there fails.
	f = newGrid(t)
	res = runBtn(t, f, face0(twoBoxes()), testCfg(), `expect button sign box is count 3 within 500ms
`)
	wantExit(t, res, 1)
}

func TestButtonOriginalIsTakenAfterBeforeEach(t *testing.T) {
	needOCR(t)
	f := newGrid(t)
	f.paint(t, f.objects[0], face0(twoBoxes()))
	swapOnGo(t, f, threeBoxes())
	res := playWith(t, f, hdr+`before each {
  say "go" on 0
  expect button sign box becomes count 3 within 5s
}
test "t" {
  expect button sign box is original within 1s
  expect button sign box is count 3 within 1s
}
`, Options{}, testCfg())
	wantExit(t, res, 0)
}

func TestAPlanarFaceElsewhereIsNoReadingAndNeverGone(t *testing.T) {
	needOCR(t)
	for _, src := range []string{
		`expect button sign text "Close" box is gone within 500ms`,
		`expect no button sign text "Close" box is shown within 500ms`,
	} {
		f := newGrid(t)
		p := face0(openBox())
		p.planar = []int{3}
		res := runBtn(t, f, p, testCfg(), src+"\n")
		wantExit(t, res, 1)
		mustHave(t, res, `slate: step 1: face 3 of "Example Sign" is planar, so its offset, repeats and rotation are not the picture on it`)
		mustNotHave(t, res, "(faces")
	}
	f := newGrid(t)
	p := face0(openBox())
	p.animated = []int{2}
	res := runBtn(t, f, p, testCfg(), "expect button sign box is shown within 400ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, `face 2 of "Example Sign" has a texture animation`)
}

func TestTheSameEntryIsSearchedOnce(t *testing.T) {
	needOCR(t)
	var calls atomic.Int32
	cfg := testCfg()
	cfg.find = func(m image.Image, r ...*imgfind.Request) ([]*imgfind.Item, error) {
		calls.Add(1)
		return imgfind.Find(m, r...)
	}
	f := newGrid(t)
	f.paint(t, f.objects[0], face0(twoBoxes()))
	swapOnGo(t, f, threeBoxes())
	res := playWith(t, f, hdr+`expect button sign box is count 2 within 2s
then expect button sign box is count 2 within 300ms
say "go" on 0
expect button sign box becomes count 3 within 5s
then expect button sign box is count 3 within 300ms
`, Options{}, cfg)
	wantExit(t, res, 0)
	if n := calls.Load(); n != 2 {
		t.Errorf("the finder ran %d times for two entries", n)
	}
}

func TestAReadingIsStampedWhenItsFaceWasRead(t *testing.T) {
	needOCR(t)
	cfg := testCfg()
	var entered, left atomic.Int64
	cfg.find = func(m image.Image, r ...*imgfind.Request) ([]*imgfind.Item, error) {
		entered.CompareAndSwap(0, time.Now().UnixNano())
		time.Sleep(300 * time.Millisecond)
		left.CompareAndSwap(0, time.Now().UnixNano())
		return imgfind.Find(m, r...)
	}
	f := newGrid(t)
	res := runBtn(t, f, face0(twoBoxes()), cfg, "expect button sign box is count 2 within 5s\n")
	wantExit(t, res, 0)
	m := regexp.MustCompile(`(?m)^(\d\d:\d\d:\d\d\.\d{3}) button sign box 2 \(faces 0\)$`).FindStringSubmatch(res.Transcript)
	if m == nil {
		t.Fatalf("no reading line:\n%s", res.Transcript)
	}
	stamp, err := time.Parse("15:04:05.000", m[1])
	if err != nil {
		t.Fatal(err)
	}
	day := func(ns int64) time.Duration {
		u := time.Unix(0, ns).UTC()
		return u.Sub(time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC))
	}
	at := time.Duration(stamp.Hour())*time.Hour + time.Duration(stamp.Minute())*time.Minute +
		time.Duration(stamp.Second())*time.Second + time.Duration(stamp.Nanosecond())
	if at > day(entered.Load())+time.Millisecond {
		t.Errorf("stamped %v, after the finder was entered at %v", at, day(entered.Load()))
	}
	if day(left.Load())-at < 250*time.Millisecond {
		t.Errorf("stamped %v, within %v of the finder returning at %v", at, 250*time.Millisecond, day(left.Load()))
	}
}

// guarded runs a before each with a guarded touch and its convergence step.
func guarded(t *testing.T, f *fakeGrid, p paintSpec, touch, then string) (*Result, error) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	f.paint(t, f.objects[0], p)
	return tryPlay(t, f, hdr+"before each {\n"+touch+"\nthen expect "+then+"\n}\ntest \"t\" { expect "+then+" }\n", Options{}, testCfg())
}

func TestAGuardedTouchWithNoButtonSendsNothingAndConverges(t *testing.T) {
	needOCR(t)
	f := newGrid(t)
	res, err := guarded(t, f, face0(closeBox()), `touch sign button text "Open" box if shown`, `button sign text "Close" box is shown within 2s`)
	if err != nil {
		t.Fatal(err)
	}
	wantExit(t, res, 0)
	wantNothingSent(t, f)
	mustHave(t, res, `slate: step 1: no text "Open" box button on "Example Sign"; the touch was not sent`, "slate: pass step 1", "slate: pass step 2")
}

func TestAGuardedTouchWithOneButtonTouchesIt(t *testing.T) {
	needOCR(t)
	f := newGrid(t)
	pic := openBox()
	c := find(t, pic, imgfind.Text("Open"), imgfind.Box())
	var ctr imgfind.Point
	for _, it := range c {
		if it.Request.Text != "" {
			ctr = it.Center
		}
	}
	res, err := guarded(t, f, face0(pic), `touch sign button text "Open" box if shown`, `button sign text "Open" box is shown within 2s`)
	if err != nil {
		t.Fatal(err)
	}
	wantExit(t, res, 0)
	wantTouch(t, f, 0, st(ctr.X, ctr.Y))
	mustNotHave(t, res, "the touch was not sent")
}

func TestAGuardedTouchWithTwoButtonsOrARefusedFaceFails(t *testing.T) {
	needOCR(t)
	f := newGrid(t)
	res, _ := guarded(t, f, face0(twoBoxes()), `touch sign button box if shown`, `button sign box is shown within 1s`)
	wantExit(t, res, 1)
	wantNothingSent(t, f)
	mustHave(t, res, `button matches 2 times on "Example Sign"`, "face 0 at ")
	f = newGrid(t)
	p := face0(openBox())
	p.planar = []int{4}
	res, _ = guarded(t, f, p, `touch sign button text "Open" box if shown`, `button sign text "Open" box is shown within 1s`)
	wantExit(t, res, 1)
	wantNothingSent(t, f)
	mustHave(t, res, `face 4 of "Example Sign" is planar`)
	mustNotHave(t, res, "the touch was not sent")
}
