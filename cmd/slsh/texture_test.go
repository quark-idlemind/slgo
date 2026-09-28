package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// aTexture is a codestream of the given size, made the way the upload
// path would make one.
func aTexture(t *testing.T, w, h int) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 4), G: uint8(y * 4), B: 0x80, A: 255})
		}
	}
	b, err := sl.EncodeTexture(m, sl.TextureOptions{Lossless: true})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestGetWritesAPng: the grid deals in JPEG 2000 and nothing on a
// desktop opens one, so what "download a texture" has to mean is a
// file that can be looked at.
func TestGetWritesAPng(t *testing.T) {
	x := newTestShell(t)
	body := aTexture(t, 32, 32)
	x.grid.ServeCap(t, sl.AssetCap, func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})

	dir := t.TempDir()
	out := filepath.Join(dir, "wall.png")
	const id = "46f67e57-7e57-c0de-cb58-aff33c6b2282"

	got := x.do(t, "get -o "+out+" "+id)
	if !strings.Contains(got, "32x32") {
		t.Errorf("get said %q", got)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatalf("get wrote no file: %v", err)
	}
	defer f.Close()
	m, err := png.Decode(f)
	if err != nil {
		t.Fatalf("what get wrote is not a png: %v", err)
	}
	if m.Bounds().Dx() != 32 || m.Bounds().Dy() != 32 {
		t.Errorf("the png is %v", m.Bounds())
	}
}

// TestGetRawWritesTheCodestream, for when the point is the asset rather
// than the picture -- comparing what the grid stored with what went up,
// most likely.
func TestGetRawWritesTheCodestream(t *testing.T) {
	x := newTestShell(t)
	body := aTexture(t, 16, 16)
	x.grid.ServeCap(t, sl.AssetCap, func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})

	out := filepath.Join(t.TempDir(), "raw.j2c")
	x.do(t, "get --raw -o "+out+" 46f67e57-7e57-c0de-cb58-aff33c6b2282")

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("--raw wrote %d bytes, want the %d the grid served", len(got), len(body))
	}
}

// TestGetRefusesWhatIsNotATexture: an inventory path can name anything,
// and a notecard fetched as a texture would be a puzzling decode error
// rather than an answer.
func TestGetRefusesWhatIsNotATexture(t *testing.T) {
	x := newTestShell(t)
	if got := x.do(t, "get /readme"); !strings.Contains(got, "get is for textures") {
		t.Errorf("get on a notecard said %q", got)
	}
	if got := x.do(t, "get /Scripts"); !strings.Contains(got, "get is for textures") {
		t.Errorf("get on a folder said %q", got)
	}
}

// TestSafeNameKeepsANameWithoutKeepingItsPath.  Inventory names may
// hold nearly any printable character, slashes included, so one used as
// a filename is a way out of the directory it was meant for.
func TestSafeNameKeepsANameWithoutKeepingItsPath(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"wall", "wall"},
		{"a wall (2)", "a wall (2)"},
		{"../../etc/passwd", ".._.._etc_passwd"},
		{"nasty/name", "nasty_name"},
		{"..", "_.."},
		{".", "_."},
		{"", "texture"},
		{"   ", "texture"},
	} {
		if got := safeName(c.in); got != c.want {
			t.Errorf("safeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// writePNG puts a picture on disk for put to pick up.
func writePNG(t *testing.T, dir string, w, h int) string {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 0x20, A: 255})
		}
	}
	path := filepath.Join(dir, "picture.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, m); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return path
}

// TestPutDryRunSpendsNothing.  An upload costs L$10 or more and cannot
// be undone, so there has to be a way to find out what would happen --
// what size, how many bytes, what fee -- without it happening.
func TestPutDryRunSpendsNothing(t *testing.T) {
	x := newTestShell(t)
	asked := make(chan []byte, 1)
	x.grid.ServeCap(t, sl.UploadCap, func(w http.ResponseWriter, r *http.Request) {
		asked <- []byte("asked")
	})
	file := writePNG(t, t.TempDir(), 300, 200)

	got := x.do(t, "put -N --round up "+file)
	for _, want := range []string{"300x200 -> 512x256", "L$10"} {
		if !strings.Contains(got, want) {
			t.Errorf("put -N said %q, want it to mention %q", got, want)
		}
	}
	select {
	case <-asked:
		t.Error("--dry-run uploaded anyway")
	default:
	}
}

// TestPutRoundsEachWayItIsTold, which is the whole reason the rounding
// is a flag rather than a policy.
func TestPutRoundsEachWayItIsTold(t *testing.T) {
	x := newTestShell(t)
	file := writePNG(t, t.TempDir(), 300, 200)

	for _, c := range []struct{ flags, want string }{
		{"--round up", "512x256"},
		{"--round down", "256x128"},
		{"--round nearest", "256x128"},
		{"--round-x up --round-y down", "512x128"},
		{"", "256x128"}, // the default is nearest
	} {
		got := x.do(t, "put -N "+c.flags+" "+file)
		if !strings.Contains(got, c.want) {
			t.Errorf("put -N %s said %q, want %q", c.flags, got, c.want)
		}
	}
}

// TestPutRefusesNamesItDoesNotKnow, and says what it does know: a
// misspelled filter that silently fell back to the default would be a
// picture resampled the wrong way with nothing to show for it.
func TestPutRefusesNamesItDoesNotKnow(t *testing.T) {
	x := newTestShell(t)
	file := writePNG(t, t.TempDir(), 64, 64)

	if got := x.do(t, "put -N --filter lanzcos "+file); !strings.Contains(got, "lanczos") {
		t.Errorf("a misspelled filter said %q, want the list", got)
	}
	if got := x.do(t, "put -N --round sideways "+file); !strings.Contains(got, "nearest") {
		t.Errorf("an unknown rounding said %q, want the list", got)
	}
}

// TestPutSendsTheCodestream: what goes up is a codestream the grid
// will take, at the picture's size, and it is the picture.  Decoded
// whole rather than read for its header, which is the first 24 bytes
// and says nothing about the rest; a picture this small is stored
// losslessly, so every pixel has to come back as it went.
func TestPutSendsTheCodestream(t *testing.T) {
	x := newTestShell(t)
	up := serveShellUpload(t, x, testLamp)
	file := writePNG(t, t.TempDir(), 64, 64)

	got := x.do(t, "put -f /Objects "+file)
	if strings.Contains(got, "slsh:") {
		t.Fatalf("put failed: %q", got)
	}
	var body []byte
	select {
	case body = <-up:
	default:
		t.Fatalf("put said %q and uploaded nothing", got)
	}
	w, h, err := sl.TextureDims(body)
	if err != nil {
		t.Fatalf("put uploaded something the grid would refuse: %v", err)
	}
	if w != 64 || h != 64 {
		t.Errorf("uploaded %dx%d", w, h)
	}
	m, err := sl.DecodeTexture(body)
	if err != nil {
		t.Fatalf("what was uploaded does not decode: %v", err)
	}
	if b := m.Bounds(); b.Dx() != 64 || b.Dy() != 64 {
		t.Fatalf("what was uploaded decodes to %dx%d", b.Dx(), b.Dy())
	}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			r, g, b, _ := m.At(m.Bounds().Min.X+x, m.Bounds().Min.Y+y).RGBA()
			if r>>8 != uint32(x) || g>>8 != uint32(y) || b>>8 != 0x20 {
				t.Fatalf("pixel %d,%d came back as %d,%d,%d, want %d,%d,32",
					x, y, r>>8, g>>8, b>>8, x, y)
			}
		}
	}
}

// TestPutUploadsACodestreamUntouched: a .j2c is already what the grid
// stores, so decoding and re-encoding it would lose quality for nothing.
func TestPutUploadsACodestreamUntouched(t *testing.T) {
	x := newTestShell(t)
	up := serveShellUpload(t, x, testLamp)

	want := aTexture(t, 32, 32)
	file := filepath.Join(t.TempDir(), "already.j2c")
	if err := os.WriteFile(file, want, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := x.do(t, "put -f /Objects "+file); !strings.Contains(got, "unchanged") {
		t.Errorf("put said %q", got)
	}
	if body := <-up; !bytes.Equal(body, want) {
		t.Errorf("uploaded %d bytes, want the %d in the file", len(body), len(want))
	}
}

// serveShellUpload stands in for the two halves of an asset upload and
// yields whatever was written to the second one.
func serveShellUpload(t *testing.T, x *testShell, made msg.UUID) chan []byte {
	t.Helper()
	body := make(chan []byte, 4)
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		b.ReadFrom(r.Body)
		body <- b.Bytes()
		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprintf(w, `<llsd><map>`+
			`<key>state</key><string>complete</string>`+
			`<key>new_asset</key><string>%s</string>`+
			`<key>new_inventory_item</key><string>%s</string>`+
			`</map></llsd>`, made, made)
	}))
	t.Cleanup(dest.Close)

	x.grid.ServeCap(t, sl.UploadCap, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprintf(w, `<llsd><map><key>state</key><string>upload</string>`+
			`<key>uploader</key><string>%s/upload</string></map></llsd>`, dest.URL)
	})
	return body
}

// TestFiltersGuideNamesEveryFilter.  The guide is written by hand and
// the filters come from a map, so the two can drift -- and a filter the
// command accepts but never mentions is one nobody will ever type.
func TestFiltersGuideNamesEveryFilter(t *testing.T) {
	var b strings.Builder
	helpFilters(&b)
	got := b.String()

	for name := range filters {
		if !strings.Contains(got, name) {
			t.Errorf("--filters never mentions %q", name)
		}
	}
	if strings.Contains(got, "nothing here says what for") {
		t.Error("--filters fell back to listing a filter it has no advice about")
	}
	// Every filter it recommends must be one that actually resolves.
	for _, a := range filterAdvice {
		if filters[a.filter] == nil {
			t.Errorf("the guide recommends %q, which is not a filter", a.filter)
		}
	}
	for _, f := range filterFamilies {
		for _, m := range f.members {
			if filters[m] == nil {
				t.Errorf("the guide lists %q, which is not a filter", m)
			}
		}
	}
}

// TestFiltersNeedsNoFileAndUploadsNothing: it is documentation, and
// asking for it should not require having a picture to hand.
func TestFiltersNeedsNoFileAndUploadsNothing(t *testing.T) {
	x := newTestShell(t)
	got := x.do(t, "put --filters")
	if strings.Contains(got, "usage:") || strings.Contains(got, "slsh:") {
		t.Errorf("put --filters said %q", got)
	}
	if !strings.Contains(got, "a photograph") || !strings.Contains(got, "lanczos") {
		t.Errorf("put --filters printed no guide: %q", got)
	}
	// A power of two over the ceiling is resized, so the guide says
	// which powers of two are left alone.
	if !strings.Contains(got, "powers of two, 2048 or less") {
		t.Errorf("put --filters does not say a picture over 2048 is resized: %q", got)
	}
}

// TestAnUnknownFilterPointsAtTheGuide rather than only listing names:
// the list says what may be typed and the guide says which to type.
func TestAnUnknownFilterPointsAtTheGuide(t *testing.T) {
	x := newTestShell(t)
	file := writePNG(t, t.TempDir(), 300, 200)
	got := x.do(t, "put -N --filter bicubic "+file)
	if !strings.Contains(got, "--filters") {
		t.Errorf("an unknown filter said %q, want it to point at the guide", got)
	}
}

// TestPutOutWritesInsteadOfUploading, in the format the name asks for.
// The point is to look at what the filter and the rounding did before
// paying for it, so nothing may reach the grid.
func TestPutOutWritesInsteadOfUploading(t *testing.T) {
	x := newTestShell(t)
	asked := make(chan []byte, 1)
	x.grid.ServeCap(t, sl.UploadCap, func(w http.ResponseWriter, r *http.Request) {
		asked <- []byte("asked")
	})
	dir := t.TempDir()
	file := writePNG(t, dir, 300, 200)

	for _, c := range []struct{ name, magic string }{
		{"out.png", "\x89PNG"},
		{"out.jpg", "\xff\xd8\xff"},
		{"out.gif", "GIF8"},
		{"out.bmp", "BM"},
		{"out.tiff", "II"},
		{"out.j2c", "\xff\x4f\xff\x51"},
	} {
		path := filepath.Join(dir, c.name)
		got := x.do(t, "put -o "+path+" --round up "+file)
		if !strings.Contains(got, "not uploaded") {
			t.Errorf("put -o %s said %q", c.name, got)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !strings.HasPrefix(string(b), c.magic) {
			t.Errorf("%s starts %x, want %x", c.name, b[:4], c.magic)
		}
	}
	select {
	case <-asked:
		t.Error("-o uploaded anyway")
	default:
	}
}

// TestPutOutWritesWhatTheFilterDid: the file on disk has to be the
// resized picture, or looking at it proves nothing.
func TestPutOutWritesWhatTheFilterDid(t *testing.T) {
	x := newTestShell(t)
	dir := t.TempDir()
	file := writePNG(t, dir, 300, 200)
	path := filepath.Join(dir, "seen.png")

	x.do(t, "put -o "+path+" --round-x up --round-y down --filter nearest "+file)

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if m.Bounds().Dx() != 512 || m.Bounds().Dy() != 128 {
		t.Errorf("wrote %v, want the 512x128 the rounding asked for", m.Bounds())
	}
}

// TestPutOutDecodesACodestream.  A .j2c goes up untouched, but
// somebody asking for a PNG of one wants to see it, not copy it.
func TestPutOutDecodesACodestream(t *testing.T) {
	x := newTestShell(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "already.j2c")
	if err := os.WriteFile(src, aTexture(t, 32, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "seen.png")

	if got := x.do(t, "put -o "+path+" "+src); strings.Contains(got, "slsh:") {
		t.Fatalf("put said %q", got)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := png.Decode(f)
	if err != nil {
		t.Fatalf("what -o wrote is not a png: %v", err)
	}
	if m.Bounds().Dx() != 32 {
		t.Errorf("wrote %v", m.Bounds())
	}
}

// TestPutOutRefusesAFormatItCannotWrite, and says what it can.
func TestPutOutRefusesAFormatItCannotWrite(t *testing.T) {
	x := newTestShell(t)
	dir := t.TempDir()
	file := writePNG(t, dir, 64, 64)

	got := x.do(t, "put -o "+filepath.Join(dir, "out.webp")+" "+file)
	for _, want := range []string{".webp", ".png", ".j2c"} {
		if !strings.Contains(got, want) {
			t.Errorf("put -o out.webp said %q, want it to mention %q", got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "out.webp")); err == nil {
		t.Error("it wrote the file anyway")
	}
}

// TestEveryModeFlagNamesItsModes.  A placeholder is not documentation:
// somebody reading --round-y and finding "this way instead" has to go
// hunting, and the flag they need is not the one they are looking at.
func TestEveryModeFlagNamesItsModes(t *testing.T) {
	x := newTestShell(t)
	got := x.do(t, "put --help")

	for _, flag := range []string{"--round=MODE", "--round-x=MODE", "--round-y=MODE"} {
		i := strings.Index(got, flag)
		if i < 0 {
			t.Errorf("%s is not in the help at all", flag)
			continue
		}
		// Everything from the flag to the next flag is its description,
		// wrapping included.
		rest := got[i+len(flag):]
		if j := strings.Index(rest, "--"); j >= 0 {
			rest = rest[:j]
		}
		for name := range roundings {
			if !strings.Contains(rest, name) {
				t.Errorf("%s does not say %q is a MODE:\n%s", flag, name, rest)
			}
		}
	}
}
