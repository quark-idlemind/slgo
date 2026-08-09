package sl

import (
	"image"
	"image/color"
	"testing"

	"github.com/disintegration/imaging"
)

// TestRoundingIsPerAxis: the two sides are rounded separately, because
// a wide picture may want one thing of its width and another of its
// height.
func TestRoundingIsPerAxis(t *testing.T) {
	for _, c := range []struct {
		w, h   int
		hz, vt Rounding
		wantW  int
		wantH  int
	}{
		{300, 200, RoundUp, RoundUp, 512, 256},
		{300, 200, RoundDown, RoundDown, 256, 128},
		{300, 200, RoundUp, RoundDown, 512, 128},
		{300, 200, RoundDown, RoundUp, 256, 256},
		{300, 200, RoundNearest, RoundNearest, 256, 128}, // 300/256 is 1.17 and 200/128 is 1.56, both under 1.75
		{512, 512, RoundUp, RoundUp, 512, 512},           // already a power of two: unchanged
		{512, 512, RoundDown, RoundDown, 512, 512},
	} {
		w, h := TextureSizeFor(c.w, c.h, ResizeOptions{Horizontal: c.hz, Vertical: c.vt})
		if w != c.wantW || h != c.wantH {
			t.Errorf("%dx%d rounding %s/%s = %dx%d, want %dx%d",
				c.w, c.h, c.hz, c.vt, w, h, c.wantW, c.wantH)
		}
	}
}

// TestNothingExceedsTheCeiling.  Over 2048 the grid refuses the image,
// so rounding up to 4096 would only mean failing later -- the ceiling
// beats every rounding, including the one that asked to grow.
func TestNothingExceedsTheCeiling(t *testing.T) {
	for _, r := range []Rounding{RoundNearest, RoundUp, RoundDown} {
		for _, n := range []int{2049, 3000, 4096, 10000} {
			if got := roundDim(n, r); got != MaxTextureSize {
				t.Errorf("%d rounded %s = %d, want %d", n, r, got, MaxTextureSize)
			}
		}
		// And what is already the ceiling stays there.
		if got := roundDim(MaxTextureSize, r); got != MaxTextureSize {
			t.Errorf("%d rounded %s = %d", MaxTextureSize, r, got)
		}
	}
}

// TestResizingProducesSomethingTheGridTakes, which is the only reason
// any of this exists.
func TestResizingProducesSomethingTheGridTakes(t *testing.T) {
	for _, c := range []struct{ w, h int }{
		{300, 200}, {1, 1}, {4095, 3}, {1000, 1000}, {2048, 2048}, {17, 4096},
	} {
		for _, r := range []Rounding{RoundNearest, RoundUp, RoundDown} {
			m := picture(c.w, c.h)
			out := Resize(m, ResizeOptions{Horizontal: r, Vertical: r})
			b := out.Bounds()
			if err := checkTextureDims(b.Dx(), b.Dy()); err != nil {
				t.Errorf("%dx%d rounded %s gave %dx%d, which the grid refuses: %v",
					c.w, c.h, r, b.Dx(), b.Dy(), err)
			}
		}
	}
}

// TestResizingWhatNeedsNoResizingLeavesItAlone.  Resampling an image to
// its own size is neither free nor lossless, and a texture that is
// already right should come out bit for bit.
func TestResizingWhatNeedsNoResizingLeavesItAlone(t *testing.T) {
	m := picture(64, 64)
	got := Resize(m, ResizeOptions{})
	if got != image.Image(m) {
		t.Error("a 64x64 picture was resampled to 64x64")
	}
}

// TestTheFilterIsTheCallersChoice: the reason imaging is here rather
// than one hard-wired resampler.  NearestNeighbor keeps the original
// pixel values and Lanczos does not, which is exactly why pixel art
// wants the first and a photograph the second.
func TestTheFilterIsTheCallersChoice(t *testing.T) {
	// A hard checkerboard: every pixel is black or white, and a
	// smoothing filter has to invent greys where a nearest-neighbour
	// one cannot.
	m := image.NewNRGBA(image.Rect(0, 0, 300, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 300; x++ {
			v := uint8(0)
			if (x/3+y/3)%2 == 0 {
				v = 255
			}
			m.SetNRGBA(x, y, color.NRGBA{R: v, G: v, B: v, A: 255})
		}
	}

	nearest := Resize(m, ResizeOptions{Filter: &imaging.NearestNeighbor})
	smooth := Resize(m, ResizeOptions{Filter: &imaging.Lanczos})

	greys := func(im image.Image) int {
		n := 0
		b := im.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, _, _, _ := im.At(x, y).RGBA()
				if r != 0 && r != 0xffff {
					n++
				}
			}
		}
		return n
	}
	if got := greys(nearest); got != 0 {
		t.Errorf("NearestNeighbor invented %d intermediate values", got)
	}
	if got := greys(smooth); got == 0 {
		t.Error("Lanczos produced no intermediate values, so the filter was ignored")
	}
}

// TestEncodeResizedIsTheWholePath, from a size the grid would refuse to
// bytes it will take.
func TestEncodeResizedIsTheWholePath(t *testing.T) {
	body, resized, err := EncodeResized(picture(300, 200),
		ResizeOptions{Horizontal: RoundUp, Vertical: RoundDown}, TextureOptions{})
	if err != nil {
		t.Fatalf("EncodeResized: %v", err)
	}
	if got := resized.Bounds(); got.Dx() != 512 || got.Dy() != 128 {
		t.Errorf("resized to %v, want 512x128", got)
	}
	w, h, err := TextureDims(body)
	if err != nil {
		t.Fatalf("encoded something the grid refuses: %v", err)
	}
	if w != 512 || h != 128 {
		t.Errorf("the codestream says %dx%d", w, h)
	}
}
