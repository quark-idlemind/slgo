package sl

// A file upload against a live grid, and what comes back down.
//
// Skipped unless SLGO_TEST_PROFILE names a profile to log in with.
// Run it on the BETA GRID: an upload costs L$10 every time, and this
// test spends it.  A profile reaches Aditi by naming its login URI:
//
//	url = https://login.aditi.lindenlab.com/cgi-bin/login.cgi
//
// # Why there are three round trips
//
// The first uses no codec at all: a texture already on the grid is a
// valid codestream, so downloading one and sending it back up exercises
// both directions with nothing of ours in the middle.  What comes down
// is then compared with what went up, which is how the one difference
// was found -- see below.
//
// The second encodes a picture here and uploads that, which is the
// question the first cannot answer: whether what this package writes is
// a conformant codestream or merely one it can read itself.  Second
// Life's own pipeline is the second implementation, and it has an
// opinion.
//
// The third starts from 300x200 -- the shape real pictures arrive in,
// and one the grid refuses outright -- so the resizing is under test
// too, and not only in the arithmetic.
//
// It CHANGES THINGS: each test creates one texture in the avatar's
// Textures folder and deletes it again in a Cleanup.

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"os"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// plywoodTexture is Second Life's default prim texture.  Every grid has
// it and every account can read it, which is what makes it the thing to
// test with rather than a file somebody has to supply.
var plywoodTexture = msg.MustParseUUID("89556747-24cb-43ed-920b-47caed15465f")

func TestLiveTextureRoundTrip(t *testing.T) {
	name := os.Getenv("SLGO_TEST_PROFILE")
	if name == "" {
		t.Skip("set SLGO_TEST_PROFILE to a beta grid profile; an upload costs L$10")
	}
	login, err := agent.LoadProfile(name)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	w, err := LoginDirect(ctx, login)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer w.Close()

	up, err := w.Texture(ctx, plywoodTexture)
	if err != nil {
		t.Fatalf("reading the default texture: %v", err)
	}
	w0, h0, err := TextureDims(up)
	if err != nil {
		t.Fatalf("the grid served something it would not take back: %v", err)
	}
	t.Logf("the default texture is %dx%d", w0, h0)

	made := "slgo round trip " + time.Now().UTC().Format("20060102-150405")
	it, res, err := w.UploadTexture(ctx, made, "written by TestLiveTextureRoundTrip", msg.UUID{}, up)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := w.DeleteItem(ctx, it.ID); err != nil {
			t.Errorf("leaving %s behind: %v", made, err)
		}
	})
	if res.NewAsset.IsZero() || it.AssetID != res.NewAsset {
		t.Errorf("item asset %s, capability said %s", it.AssetID, res.NewAsset)
	}
	if it.NextOwnerMask != PermAll {
		t.Errorf("next owner mask %#x, want %#x", it.NextOwnerMask, PermAll)
	}

	down, err := w.Texture(ctx, it.AssetID)
	if err != nil {
		t.Fatalf("reading back what was uploaded: %v", err)
	}

	// The one difference, and it is deliberate: Second Life rewrites the
	// codestream's comment marker on the way in, replacing whatever the
	// encoder left there with a record of who uploaded it and how big it
	// is -- "a=<agent>&h=512&z=<when>&w=512".  Everything on either side
	// of that segment is byte for byte what went up, so the image is
	// untouched and only the provenance is added.
	upBody, upCom := withoutComment(up)
	downBody, downCom := withoutComment(down)
	if !bytes.Equal(upBody, downBody) {
		t.Errorf("the codestream came back changed outside its comment: %d bytes up, %d down",
			len(upBody), len(downBody))
	}
	if bytes.Equal(upCom, downCom) {
		t.Logf("the comment survived unchanged: %q", upCom)
	} else {
		t.Logf("comment rewritten on upload:\n  up   %q\n  down %q", upCom, downCom)
	}
	if !bytes.Contains(downCom, []byte(w.Me().String())) {
		t.Errorf("the comment does not name the uploader: %q", downCom)
	}
}

// withoutComment splits a JPEG 2000 codestream into everything that is
// not its first comment segment, and the text of that segment.
//
// A comparison of two codestreams has to ignore it or ignore nothing:
// the marker is metadata that Second Life writes for itself, and it is
// the only part of an uploaded texture that does not come back as sent.
func withoutComment(b []byte) (rest, text []byte) {
	i := bytes.Index(b, []byte{0xff, 0x64})
	if i < 0 || i+4 > len(b) {
		return b, nil
	}
	n := int(b[i+2])<<8 | int(b[i+3])
	if i+2+n > len(b) {
		return b, nil
	}
	rest = append(append([]byte{}, b[:i]...), b[i+2+n:]...)
	return rest, b[i+4 : i+2+n]
}

// TestLiveEncodedTextureIsAccepted: a codestream no viewer made, taken
// by the grid and read back unchanged.
//
// This is the test that says the Go codec is conformant rather than
// merely self-consistent: Second Life's own pipeline is the second
// implementation, and it has an opinion.
func TestLiveEncodedTextureIsAccepted(t *testing.T) {
	name := os.Getenv("SLGO_TEST_PROFILE")
	if name == "" {
		t.Skip("set SLGO_TEST_PROFILE to a beta grid profile; an upload costs L$10")
	}
	login, err := agent.LoadProfile(name)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	w, err := LoginDirect(ctx, login)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer w.Close()

	// 128x128 is at LosslessArea, so this goes up lossless and every
	// pixel has to come back.
	want := image.NewNRGBA(image.Rect(0, 0, 128, 128))
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			a := uint8(255)
			if (x/16+y/16)%2 == 0 {
				a = 0
			}
			want.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 2), G: uint8(y * 2), B: 0x30, A: a})
		}
	}

	made := "slgo encoded " + time.Now().UTC().Format("20060102-150405")
	it, _, err := w.UploadImage(ctx, Upload{Name: made, Desc: "encoded by slgo"}, want, TextureOptions{})
	if err != nil {
		t.Fatalf("the grid would not take what we encoded: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := w.DeleteItem(ctx, it.ID); err != nil {
			t.Errorf("leaving %s behind: %v", made, err)
		}
	})

	got, err := w.TextureImage(ctx, it.AssetID)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if got.Bounds() != want.Bounds() {
		t.Fatalf("came back %v, want %v", got.Bounds(), want.Bounds())
	}
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			wr, wg, wb, wa := want.At(x, y).RGBA()
			gr, gg, gb, ga := got.At(x, y).RGBA()
			if wr != gr || wg != gg || wb != gb || wa != ga {
				t.Fatalf("pixel %d,%d came back %v, want %v -- alpha %d vs %d",
					x, y, got.At(x, y), want.At(x, y), ga, wa)
			}
		}
	}
}

// TestLiveResizedTextureIsAccepted: a picture at a size the grid
// refuses outright, resized here and taken there.
//
// 300x200 is the shape almost everything real arrives in, and the
// rounding is the caller's -- up, in this case, so nothing is lost.
func TestLiveResizedTextureIsAccepted(t *testing.T) {
	name := os.Getenv("SLGO_TEST_PROFILE")
	if name == "" {
		t.Skip("set SLGO_TEST_PROFILE to a beta grid profile; an upload costs L$10")
	}
	login, err := agent.LoadProfile(name)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	w, err := LoginDirect(ctx, login)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer w.Close()

	odd := image.NewNRGBA(image.Rect(0, 0, 300, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 300; x++ {
			odd.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 0x80, A: 255})
		}
	}

	// What it would be, before it is anything.
	wantW, wantH := TextureSizeFor(300, 200, ResizeOptions{Horizontal: RoundUp, Vertical: RoundUp})
	if wantW != 512 || wantH != 256 {
		t.Fatalf("rounding up 300x200 gives %dx%d", wantW, wantH)
	}

	// And what the grid says about it unresized, which is the reason
	// any of this exists.
	if _, _, err := EncodeResized(odd, ResizeOptions{}, TextureOptions{}); err != nil {
		t.Fatalf("even resized, it would not encode: %v", err)
	}
	if _, err := EncodeTexture(odd, TextureOptions{}); err == nil {
		t.Error("300x200 encoded without being resized")
	}

	fit := Resize(odd, ResizeOptions{Horizontal: RoundUp, Vertical: RoundUp})
	made := "slgo resized " + time.Now().UTC().Format("20060102-150405")
	it, _, err := w.UploadImage(ctx, Upload{Name: made, Desc: "resized by slgo"}, fit, TextureOptions{})
	if err != nil {
		t.Fatalf("the grid would not take the resized picture: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := w.DeleteItem(ctx, it.ID); err != nil {
			t.Errorf("leaving %s behind: %v", made, err)
		}
	})

	got, err := w.TextureImage(ctx, it.AssetID)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if b := got.Bounds(); b.Dx() != wantW || b.Dy() != wantH {
		t.Errorf("came back %v, want %dx%d", b, wantW, wantH)
	}
}
