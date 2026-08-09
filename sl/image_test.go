package sl

import (
	"context"
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// picture is a gradient with a transparent hole, which is what most
// textures in Second Life are: colour with an alpha channel that
// matters.
func picture(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if x > w/3 && x < 2*w/3 && y > h/3 && y < 2*h/3 {
				a = 0
			}
			m.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 0x40, A: a})
		}
	}
	return m
}

// TestATextureSurvivesLosslessly, alpha included.  A texture is not a
// photograph: an interface element with a transparent middle is the
// common case, and a codec that drops the fourth component looks right
// until something is put in front of something else.
func TestATextureSurvivesLosslessly(t *testing.T) {
	want := picture(64, 64)
	b, err := EncodeTexture(want, TextureOptions{})
	if err != nil {
		t.Fatalf("EncodeTexture: %v", err)
	}
	if _, _, err := TextureDims(b); err != nil {
		t.Errorf("we encoded something the grid would refuse: %v", err)
	}

	got, err := DecodeTexture(b)
	if err != nil {
		t.Fatalf("DecodeTexture: %v", err)
	}
	if got.Bounds() != want.Bounds() {
		t.Fatalf("decoded %v, want %v", got.Bounds(), want.Bounds())
	}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			wr, wg, wb, wa := want.At(x, y).RGBA()
			gr, gg, gb, ga := got.At(x, y).RGBA()
			if wr != gr || wg != gg || wb != gb || wa != ga {
				t.Fatalf("pixel %d,%d came back %v, want %v", x, y, got.At(x, y), want.At(x, y))
			}
		}
	}
}

// noise is a picture with detail in it, which is what compression is
// for.  A gradient is the wrong test: it compresses so well losslessly
// that asking for 8:1 achieves nothing and the quality layers cost more
// than they save -- measured, and worth knowing before setting Ratio on
// something flat.
func noise(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	seed := uint32(12345)
	next := func() uint8 {
		seed = seed*1664525 + 1013904223
		return uint8(seed >> 24)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, color.NRGBA{R: next(), G: next(), B: next(), A: 255})
		}
	}
	return m
}

// TestALargeTextureIsCompressed: above LosslessArea the point is to be
// small, and roughly the size Second Life's own textures are.
func TestALargeTextureIsCompressed(t *testing.T) {
	m := noise(256, 256)
	raw := 256 * 256 * 3

	small, err := EncodeTexture(m, TextureOptions{})
	if err != nil {
		t.Fatalf("EncodeTexture: %v", err)
	}
	big, err := EncodeTexture(m, TextureOptions{Lossless: true})
	if err != nil {
		t.Fatalf("EncodeTexture lossless: %v", err)
	}
	if len(small) >= len(big) {
		t.Errorf("the lossy encoding is %d bytes and the lossless one %d", len(small), len(big))
	}
	if got := float64(raw) / float64(len(small)); got < DefaultRatio/2 {
		t.Errorf("compressed %.1f:1, and the default asks for %d:1", got, DefaultRatio)
	}
	if _, err := DecodeTexture(small); err != nil {
		t.Errorf("what we encoded does not decode: %v", err)
	}
}

// TestEncodingWillNotResize is the whole policy in one test.  Which
// resampling a picture deserves is not this package's decision, so an
// image the grid would refuse is refused here, with the size it would
// have to be.
func TestEncodingWillNotResize(t *testing.T) {
	_, err := EncodeTexture(picture(300, 200), TextureOptions{})
	if err == nil {
		t.Fatal("a 300x200 image encoded, and the grid would have refused it")
	}
	for _, want := range []string{"power of two", "256x128", "will not do for you"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

// TestUploadingAPictureStopsBeforeItCosts: encoding happens first, so a
// picture the grid would refuse never reaches the capability that
// charges.
func TestUploadingAPictureStopsBeforeItCosts(t *testing.T) {
	w, f := newFakeSession(t)
	up := serveUpload(t, f, UploadCap, func() (int, string) {
		return 200, completeUpload(theChild, theOther)
	})
	f.ServeInventory(t, func(msg.UUID) []*Item {
		return []*Item{{ID: theChild, ParentID: thePrim, AssetID: theOther}}
	})

	ctx := context.Background()
	if _, _, err := w.UploadImage(ctx, Upload{Name: "wrong", Folder: thePrim}, picture(300, 200), TextureOptions{}); err == nil {
		t.Error("a 300x200 picture was uploaded")
	}
	select {
	case b := <-up.asked:
		t.Errorf("the capability was asked anyway: %s", b)
	default:
	}

	it, _, err := w.UploadImage(ctx, Upload{Name: "right", Folder: thePrim}, picture(64, 64), TextureOptions{})
	if err != nil {
		t.Fatalf("UploadImage: %v", err)
	}
	if it.ID != theChild {
		t.Errorf("item = %s, want %s", it.ID, theChild)
	}
	// What went up is a codestream, not the pixels.
	if body := <-up.body; !strings.HasPrefix(string(body[:4]), "\xff\x4f\xff\x51") {
		t.Errorf("uploaded %x..., which is not a codestream", body[:4])
	}
}

// TestLevelsForSmallTextures: five decompositions is what the viewer
// asks for and what makes a texture fetchable at lower resolutions, but
// a 4x4 cannot be halved five times.
func TestLevelsForSmallTextures(t *testing.T) {
	for _, c := range []struct {
		w, h, want int
	}{
		{512, 512, 5}, {1024, 64, 5}, {32, 32, 5}, {16, 16, 4}, {4, 4, 2}, {1, 1, 0},
	} {
		if got := levelsFor(c.w, c.h); got != c.want {
			t.Errorf("levelsFor(%d, %d) = %d, want %d", c.w, c.h, got, c.want)
		}
	}
}
