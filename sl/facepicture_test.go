package sl

// What a face shows: Face.Picture on a picture in hand, and
// Session.FacePicture on an object the fake region describes.
// Why: doc/face-pictures.md

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

func TestAnUntouchedFaceIsTheTexture(t *testing.T) {
	src := nrgba(6, 5, func(x, y int) color.NRGBA {
		return color.NRGBA{R: uint8(x * 30), G: uint8(y * 40), B: uint8(x + y*3), A: uint8(200 + x)}
	})
	got, err := PlainFaces(1)[0].Picture(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Pix, src.Pix) {
		t.Fatal("an untouched face changed the texture")
	}
}

func TestAnOffsetSlidesTheTextureAndWraps(t *testing.T) {
	const n = 4
	src := nrgba(n, n, func(x, y int) color.NRGBA {
		return color.NRGBA{R: uint8(x + 1), G: uint8(y + 1), B: 10, A: 255}
	})
	f := PlainFaces(1)[0]
	f.SetOffsets(0.25, 0.25) // one texel on a four-texel side
	got, err := f.Picture(src)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			// A positive offset samples further along the texture, so
			// the picture slides the other way and the part that goes
			// off one edge comes back on the other.
			want := src.NRGBAAt((x+1)%n, (y+n-1)%n)
			if got.NRGBAAt(x, y) != want {
				t.Fatalf("(%d,%d) = %v, want %v", x, y, got.NRGBAAt(x, y), want)
			}
		}
	}
}

func TestARepeatBelowOneTrimsToTheMiddle(t *testing.T) {
	const n = 8
	src := nrgba(n, n, func(x, y int) color.NRGBA {
		return color.NRGBA{R: uint8(x), G: uint8(y), B: 1, A: 255}
	})
	f := PlainFaces(1)[0]
	f.SetRepeats(0.5, 0.5)
	got, err := f.Picture(src)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bounds().Dx() != n/2 || got.Bounds().Dy() != n/2 {
		t.Fatalf("size %v, want the middle half", got.Bounds())
	}
	for y := 0; y < n/2; y++ {
		for x := 0; x < n/2; x++ {
			want := src.NRGBAAt(x+n/4, y+n/4)
			if got.NRGBAAt(x, y) != want {
				t.Fatalf("(%d,%d) = %v, want %v from the middle", x, y, got.NRGBAAt(x, y), want)
			}
		}
	}
}

func TestRepeatingTilesAboutTheMiddle(t *testing.T) {
	src := nrgba(2, 1, func(x, y int) color.NRGBA {
		return color.NRGBA{R: uint8(x + 1), A: 255}
	})
	f := PlainFaces(1)[0]
	f.SetRepeats(2, 1)
	got, err := f.Picture(src)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bounds().Dx() != 4 || got.Bounds().Dy() != 1 {
		t.Fatalf("size %v, want 4x1", got.Bounds())
	}
	// Two copies, centred, so the row starts on the texture's right half.
	want := []uint8{2, 1, 2, 1}
	for x, r := range want {
		if got.NRGBAAt(x, 0).R != r {
			t.Fatalf("pixel %d red = %d, want %d", x, got.NRGBAAt(x, 0).R, r)
		}
	}
}

func TestAHalfTurnSpinsTheTexture(t *testing.T) {
	src := nrgba(3, 2, func(x, y int) color.NRGBA {
		return color.NRGBA{R: uint8(10*x + y), A: 255}
	})
	f := PlainFaces(1)[0]
	f.Rotation = 16384 // half a turn
	got, err := f.Picture(src)
	if err != nil {
		t.Fatal(err)
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			want := src.NRGBAAt(w-1-x, h-1-y)
			if got.NRGBAAt(x, y) != want {
				t.Fatalf("(%d,%d) = %v, want %v", x, y, got.NRGBAAt(x, y), want)
			}
		}
	}
}

func TestAQuarterTurn(t *testing.T) {
	// A B     a quarter turn, by the viewer's xform,     B D
	// C D                                                A C
	src := nrgba(2, 2, func(x, y int) color.NRGBA {
		return color.NRGBA{R: uint8(2*y + x + 1), A: 255}
	})
	f := PlainFaces(1)[0]
	f.Rotation = 8192
	got, err := f.Picture(src)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bounds().Dx() != 2 || got.Bounds().Dy() != 2 {
		t.Fatalf("size %v", got.Bounds())
	}
	want := [][]uint8{{2, 4}, {1, 3}}
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			if got.NRGBAAt(x, y).R != want[y][x] {
				t.Fatalf("(%d,%d) red = %d, want %d", x, y, got.NRGBAAt(x, y).R, want[y][x])
			}
		}
	}
}

func TestANegativeRepeatFlips(t *testing.T) {
	src := nrgba(4, 3, func(x, y int) color.NRGBA {
		return color.NRGBA{R: uint8(x + 1), G: uint8(y + 1), A: 255}
	})
	f := PlainFaces(1)[0]
	f.SetRepeats(-1, -1)
	got, err := f.Picture(src)
	if err != nil {
		t.Fatal(err)
	}
	w, h := 4, 3
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			want := src.NRGBAAt(w-1-x, h-1-y)
			if got.NRGBAAt(x, y) != want {
				t.Fatalf("(%d,%d) = %v, want %v", x, y, got.NRGBAAt(x, y), want)
			}
		}
	}
}

func TestAWideTextureTurnedOnItsSide(t *testing.T) {
	src := nrgba(20, 10, func(x, y int) color.NRGBA { return color.NRGBA{A: 255} })
	f := PlainFaces(1)[0]
	f.Rotation = 8192
	got, err := f.Picture(src)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bounds().Dx() != 10 || got.Bounds().Dy() != 20 {
		t.Fatalf("size %v, want 10x20", got.Bounds())
	}
}

func TestASubImageUsesItsOwnBounds(t *testing.T) {
	big := nrgba(6, 6, func(x, y int) color.NRGBA {
		return color.NRGBA{R: uint8(x), G: uint8(y), A: 255}
	})
	sub := big.SubImage(image.Rect(1, 2, 5, 5)).(*image.NRGBA)
	got, err := PlainFaces(1)[0].Picture(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bounds().Dx() != 4 || got.Bounds().Dy() != 3 {
		t.Fatalf("size %v, want 4x3", got.Bounds())
	}
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			want := big.NRGBAAt(x+1, y+2)
			if got.NRGBAAt(x, y) != want {
				t.Fatalf("(%d,%d) = %v, want %v", x, y, got.NRGBAAt(x, y), want)
			}
		}
	}
}

func TestTheMappingMatchesTheViewer(t *testing.T) {
	f := PlainFaces(1)[0]
	f.ScaleS, f.ScaleT = 2, 0.5
	f.SetOffsets(0.25, -0.1)
	f.SetRotationRad(math.Pi / 2)
	sin, cos := math.Sincos(float64(f.RotationRad()))
	offS, offT := f.OffsetsF()
	for _, st := range [][2]float64{{0, 0}, {0.5, 0.5}, {1, 1}, {0.25, 0.8}, {0.9, 0.1}} {
		u, v := mapST(st[0], st[1], sin, cos, float64(f.ScaleS), float64(f.ScaleT), float64(offS), float64(offT))
		got := f.SurfaceToTexture(msg.Vector3{X: float32(st[0]), Y: float32(st[1])})
		if math.Abs(u-float64(got.X)) > 1e-4 || math.Abs(v-float64(got.Y)) > 1e-4 {
			t.Fatalf("st %v: uv %v,%v, viewer %v", st, u, v, got)
		}
	}
}

func TestAHugeRepeatStaysWithinTheDecodeSize(t *testing.T) {
	src := nrgba(500, 4, func(x, y int) color.NRGBA {
		return color.NRGBA{R: uint8(x % 256), A: 255}
	})
	f := PlainFaces(1)[0]
	f.SetRepeats(9, 1)
	got, err := f.Picture(src)
	if err != nil {
		t.Fatal(err)
	}
	b := got.Bounds()
	if b.Dx() > MaxDecodeSize || b.Dy() > MaxDecodeSize {
		t.Fatalf("size %v exceeds %d", b, MaxDecodeSize)
	}
	if b.Dx() < MaxDecodeSize-1 || b.Dy() != 4 {
		t.Fatalf("size %v, want the long side at the cap and the short side kept", b)
	}
}

func TestFacePictureRejectsAnEmptyPicture(t *testing.T) {
	if _, err := PlainFaces(1)[0].Picture(nil); err == nil {
		t.Fatal("a nil texture was accepted")
	}
	empty := image.NewNRGBA(image.Rect(0, 0, 0, 4))
	if _, err := PlainFaces(1)[0].Picture(empty); err == nil {
		t.Fatal("an empty texture was accepted")
	}
	f := PlainFaces(1)[0]
	f.ScaleS = float32(math.NaN())
	src := nrgba(2, 2, func(x, y int) color.NRGBA { return color.NRGBA{A: 255} })
	if _, err := f.Picture(src); err == nil {
		t.Fatal("a repeat of NaN was accepted")
	}
}

func nrgba(w, h int, at func(x, y int) color.NRGBA) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, at(x, y))
		}
	}
	return m
}

// aPaintedThing stands a prim in the region whose faces are: 0 wearing
// the texture and moved a quarter, 1 plain white, 2 planar, 3 and 4
// wearing it, with a texture animation running that names face 3 only.
// Its shape is not known, so it has eight faces. The texture is served
// at the asset capability, and fetches counts the requests.
func aPaintedThing(t *testing.T, w *Session, f *fakeBackend, tex image.Image, fetches *int) (*Object, []Face) {
	t.Helper()
	faces := PlainFaces(5)
	for _, i := range []int{0, 2, 3, 4} {
		faces[i].Texture = theOther
	}
	faces[0].SetOffsets(0.25, 0)
	faces[2].Media = 0x02
	te, err := EncodeTextureEntry(faces)
	if err != nil {
		t.Fatal(err)
	}
	anim := make([]byte, 16)
	anim[0], anim[1] = 0x01, 3 // on, face 3 only
	f.mu.Lock()
	f.objects = []*Seen{{Object: Object{ID: thePrim, Local: 4242, Name: "a thing"},
		TextureEntry: te, TextureAnim: anim}}
	f.mu.Unlock()
	body, err := EncodeTexture(tex, TextureOptions{Lossless: true})
	if err != nil {
		t.Fatal(err)
	}
	f.ServeCap(t, AssetCap, func(rw http.ResponseWriter, r *http.Request) {
		*fetches++
		rw.Write(body)
	})
	return aThing(w), faces
}

// TestAFacePictureIsTheTextureAsTheFaceShowsIt, fetched through the
// asset capability and put through Face.Picture.
func TestAFacePictureIsTheTextureAsTheFaceShowsIt(t *testing.T) {
	w, f := newFakeSession(t)
	tex := nrgba(4, 4, func(x, y int) color.NRGBA {
		return color.NRGBA{R: uint8(x*40 + 1), G: uint8(y*40 + 1), B: 9, A: 255}
	})
	var fetches int
	o, faces := aPaintedThing(t, w, f, tex, &fetches)
	got, err := w.FacePicture(context.Background(), o, 0)
	if err != nil {
		t.Fatal(err)
	}
	want, err := faces[0].Picture(tex)
	if err != nil {
		t.Fatal(err)
	}
	g := got.(*image.NRGBA)
	if !bytes.Equal(g.Pix, want.Pix) || g.Bounds() != want.Bounds() {
		t.Fatal("face 0 is not the texture moved by the face's offset")
	}
	if bytes.Equal(g.Pix, tex.Pix) {
		t.Fatal("the offset changed nothing, so this does not tell a face from its texture")
	}
	if fetches != 1 {
		t.Fatalf("fetched the texture %d times", fetches)
	}
}

// TestAFacePictureRefusesWhatItCannotShow: no texture, planar mapping,
// a running animation, and a face the object does not have.
func TestAFacePictureRefusesWhatItCannotShow(t *testing.T) {
	w, f := newFakeSession(t)
	tex := nrgba(2, 2, func(x, y int) color.NRGBA { return color.NRGBA{A: 255} })
	var fetches int
	o, _ := aPaintedThing(t, w, f, tex, &fetches)
	for _, c := range []struct {
		what string
		face int
		is   error
		said string
	}{
		{"no texture", 1, ErrNoTexture, "face 1"},
		{"planar", 2, ErrPlanarFace, "face 2"},
		{"animated", 3, ErrAnimatedFace, "face 3"},
	} {
		_, err := w.FacePicture(context.Background(), o, c.face)
		if !errors.Is(err, c.is) || !strings.Contains(err.Error(), c.said) {
			t.Errorf("%s: error %v, want %v naming %q", c.what, err, c.is, c.said)
		}
	}
	// A face the animation does not name is shown.
	if _, err := w.FacePicture(context.Background(), o, 4); err != nil {
		t.Errorf("a face animating elsewhere: %v", err)
	}
	for _, face := range []int{-1, 8} {
		if _, err := w.FacePicture(context.Background(), o, face); err == nil {
			t.Errorf("face %d, which the object does not have, was shown", face)
		}
	}
	if _, err := w.FacePicture(context.Background(), nil, 0); err == nil {
		t.Error("no object was accepted")
	}
	if fetches != 1 {
		t.Errorf("%d fetches: the refusals fetched, or face 4 did not", fetches)
	}
}

// TestFacePictureWaitsAsLongAsTheOptionSays: the wait for the region to
// name the object is Options.ObjectTimeout, not a number of its own.
// The thing here has no name and nothing answers for one, so the wait
// runs out.  Run out the default way it takes four quiet seconds, and
// with this option about one.
func TestFacePictureWaitsAsLongAsTheOptionSays(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	tex := nrgba(2, 2, func(x, y int) color.NRGBA { return color.NRGBA{R: 200, A: 255} })
	var fetches int
	o, _ := aPaintedThing(t, w, f, tex, &fetches)
	f.mu.Lock()
	f.objects[0].Name = ""
	f.mu.Unlock()
	w.SetOptions(Options{ObjectTimeout: 300 * time.Millisecond})

	start := time.Now()
	if _, err := w.FacePicture(context.Background(), o, 0); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("FacePicture took %v with ObjectTimeout 300ms", took.Round(time.Millisecond))
	}
}
