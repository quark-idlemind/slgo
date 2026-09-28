package sl

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mububoki/jpeg2000/j2k"

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
	for _, want := range []string{"power of two", "256x128", "sl.Resize"} {
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

// claims is a codestream that is all header: the SOC and SIZ codestream
// writes, finished with three 8-bit components, then EOC.  One tile
// covers the image, so reading the header costs the same whatever size
// it claims, and a decoder that believes it allocates for that size.
func claims(w, h int) []byte {
	b := slices.Clip(codestream(w, h)[:40])
	b = append(b, 0x00, 0x03)
	for range 3 {
		b = append(b, 0x07, 0x01, 0x01) // 8 bits, unsigned, not subsampled
	}
	return append(b, 0xff, 0xd9)
}

// allocated is how many bytes f allocates, averaged over n calls so
// that whatever else the process does meanwhile is lost in the average.
func allocated(n int, f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range n {
		f()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(n)
}

// TestAnOversizedTextureIsRefusedBeforeItIsDecoded: the decoder sizes
// its buffers from the header, and a header claiming 60000x60000 is
// tens of gigabytes of planes.  So the size is read first and anything
// over the viewer's own limit is refused, quickly and without the
// allocation.  A second SIZ is what the decoder believes, so it is what
// the check believes too.
func TestAnOversizedTextureIsRefusedBeforeItIsDecoded(t *testing.T) {
	small, huge := claims(64, 64), claims(60000, 60000)
	for _, c := range []struct {
		what string
		b    []byte
	}{
		{"a header claiming 60000x60000", huge},
		{"one side over the limit", claims(MaxDecodeSize+1, 8)},
		{"the other side over the limit", claims(8, MaxDecodeSize+1)},
		{"a second SIZ claiming 60000x60000", append(slices.Clip(small[:len(small)-2]), huge[2:]...)},
	} {
		start := time.Now()
		_, err := DecodeTexture(c.b)
		if took := time.Since(start); took > time.Second {
			t.Errorf("%s: took %v to refuse", c.what, took)
		}
		if err == nil || !strings.Contains(err.Error(), strconv.Itoa(MaxDecodeSize)) {
			t.Errorf("%s: error = %v, want it refused at %d a side", c.what, err, MaxDecodeSize)
			continue
		}
		if n := allocated(100, func() { DecodeTexture(c.b) }); n > 64<<10 {
			t.Errorf("%s: refusing it allocated %d bytes", c.what, n)
		}
	}
}

// tiled is claims with the image cut into tiles of tw by th.
func tiled(w, h, tw, th int) []byte {
	b := claims(w, h)
	for i, v := range []int{tw, th} {
		b[24+4*i], b[25+4*i], b[26+4*i], b[27+4*i] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
	}
	return b
}

// afterATilePart is a header that passes, one tile-part of four bytes,
// and then the SIZ of next, which the decoder takes afresh once it has
// read the tile-part -- after DecodeConfig has stopped reading.
func afterATilePart(next []byte) []byte {
	b := slices.Clip(claims(64, 64))
	b = b[:len(b)-2] // its EOC
	// SOT: Lsot 10, tile 0, Psot 18 from the marker to the end of the
	// data, part 0 of 1; then SOD and the four bytes.
	b = append(b, 0xff, 0x90, 0x00, 0x0a, 0x00, 0x00, 0x00, 0x00, 0x00, 18, 0x00, 0x01)
	b = append(b, 0xff, 0x93, 0x01, 0x02, 0x03, 0x04)
	return append(b, next[2:]...)
}

// TestATextureOfTinyTilesIsRefusedBeforeItIsRead: the decoder makes a
// record for every tile while it reads the header, so a million tiles
// of four pixels, which it allows, cost over 200 MB to be told the size.
// So the tiles are counted first, in every SIZ the decoder would read,
// and more than MaxDecodeTiles is refused without the allocation.
func TestATextureOfTinyTilesIsRefusedBeforeItIsRead(t *testing.T) {
	small := claims(64, 64)
	tiny := tiled(MaxDecodeSize, MaxDecodeSize, 4, 4)
	for _, c := range []struct {
		what string
		b    []byte
	}{
		{"a million tiles of four pixels", tiny},
		{"one tile over the limit", tiled(MaxDecodeSize, MaxDecodeSize, 64, 63)},
		{"a second SIZ of tiny tiles", append(slices.Clip(small[:len(small)-2]), tiny[2:]...)},
		{"a SIZ of tiny tiles after a tile-part", afterATilePart(tiny)},
	} {
		_, err := DecodeTexture(c.b)
		if err == nil || !strings.Contains(err.Error(), "tiles") ||
			!strings.Contains(err.Error(), strconv.Itoa(MaxDecodeTiles)) {
			t.Errorf("%s: error = %v, want it refused at %d tiles", c.what, err, MaxDecodeTiles)
			continue
		}
		if n := allocated(10, func() { DecodeTexture(c.b) }); n > 64<<10 {
			t.Errorf("%s: refusing it allocated %d bytes", c.what, n)
		}
	}

	// The limit itself is decoded as far as the tiles go, and so is the
	// one tile everything the viewer makes has.
	for _, b := range [][]byte{tiled(MaxDecodeSize, MaxDecodeSize, 64, 64), small} {
		if err := checkSIZs(b); err != nil {
			t.Errorf("refused %d bytes: %v", len(b), err)
		}
	}
}

// TestASecondSizeAfterATilePartIsChecked: the size is read by the
// decoder's own parser too, but only as far as the first tile-part, and
// the decoder goes on to take a SIZ it meets after one.
func TestASecondSizeAfterATilePartIsChecked(t *testing.T) {
	_, err := DecodeTexture(afterATilePart(claims(MaxDecodeSize+1, 8)))
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(MaxDecodeSize)) {
		t.Errorf("error = %v, want it refused at %d a side", err, MaxDecodeSize)
	}
}

// TestAnyShapeDecodes: the grid's rules are for what is sent to it, and
// a codestream from anywhere else -- a file on disk, a texture uploaded
// by some other client -- decodes whatever its shape, up to the limit.
// The upload check still refuses what the grid would.
func TestAnyShapeDecodes(t *testing.T) {
	for _, c := range []struct {
		w, h    int
		uploads bool
	}{
		{300, 200, false},          // not a power of two
		{MaxDecodeSize, 4, false},  // wider than the grid takes
		{MaxTextureSize, 64, true}, // as wide as it takes
	} {
		var b bytes.Buffer
		if err := j2k.Encode(&b, picture(c.w, c.h)); err != nil {
			t.Fatalf("%dx%d: encoding: %v", c.w, c.h, err)
		}
		if _, _, err := TextureDims(b.Bytes()); (err == nil) != c.uploads {
			t.Errorf("%dx%d: the upload check says %v", c.w, c.h, err)
		}
		m, err := DecodeTexture(b.Bytes())
		if err != nil {
			t.Errorf("%dx%d: %v", c.w, c.h, err)
			continue
		}
		if got := m.Bounds(); got != image.Rect(0, 0, c.w, c.h) {
			t.Errorf("%dx%d: decoded %v", c.w, c.h, got)
		}
	}
}
