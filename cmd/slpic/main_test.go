package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

func TestFacesPrintsTheFilesItWrote(t *testing.T) {
	var got rawCall
	save := func(_ context.Context, addr, agent, name, dir string, raw bool) ([]string, error) {
		got = rawCall{addr, agent, name, dir, raw}
		return []string{filepath.Join(dir, "0.png"), "", filepath.Join(dir, "2.png")}, nil
	}
	var out, errw bytes.Buffer
	code := run(context.Background(), []string{"faces", "-addr", "asked:2", "-agent", "greeter", "-raw", "a sign", "out"}, &out, &errw, save)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errw.String())
	}
	if got != (rawCall{"asked:2", "greeter", "a sign", "out", true}) {
		t.Fatalf("called with %+v", got)
	}
	want := filepath.Join("out", "0.png") + "\n" + filepath.Join("out", "2.png") + "\n"
	if out.String() != want {
		t.Fatalf("printed %q, want %q", out.String(), want)
	}
}

func TestFacesDefaultsToTheShownPicture(t *testing.T) {
	t.Setenv("SLGO_ADDR", "")
	t.Setenv("PATH", t.TempDir())
	var got rawCall
	save := func(_ context.Context, addr, agent, name, dir string, raw bool) ([]string, error) {
		got = rawCall{addr, agent, name, dir, raw}
		return []string{"0.png"}, nil
	}
	var out, errw bytes.Buffer
	code := run(context.Background(), []string{"faces", "a sign", "faces"}, &out, &errw, save)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errw.String())
	}
	if got.addr != "localhost:7807" || got.agent != "" || got.raw {
		t.Fatalf("called with %+v", got)
	}
	if out.String() != "0.png\n" {
		t.Fatalf("printed %q", out.String())
	}
}

func TestFacesAsksSLHost(t *testing.T) {
	t.Setenv("SLGO_ADDR", "")
	fakeSLHost(t, "echo 192.0.2.10")
	var got rawCall
	save := func(_ context.Context, addr, agent, name, dir string, raw bool) ([]string, error) {
		got = rawCall{addr, agent, name, dir, raw}
		return []string{"0.png"}, nil
	}
	var out, errw bytes.Buffer
	code := run(context.Background(), []string{"faces", "-agent", "greeter", "a sign", "faces"}, &out, &errw, save)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errw.String())
	}
	if got.addr != "192.0.2.10:7807" || got.agent != "greeter" {
		t.Fatalf("called with %+v", got)
	}
}

func TestFacesTellsSLHostWhichAvatar(t *testing.T) {
	t.Setenv("SLGO_ADDR", "")
	fakeSLHost(t, `echo "host-for-${SLGO_AGENT:-nobody}"`)
	ask := func(t *testing.T, args ...string) string {
		t.Helper()
		var addr string
		save := func(_ context.Context, a, _, _, _ string, _ bool) ([]string, error) {
			addr = a
			return []string{"0.png"}, nil
		}
		var out, errw bytes.Buffer
		args = append(append([]string{"faces"}, args...), "a sign", "out")
		if code := run(context.Background(), args, &out, &errw, save); code != 0 {
			t.Fatalf("exit %d, stderr %q", code, errw.String())
		}
		return addr
	}
	t.Run("flag", func(t *testing.T) {
		t.Setenv("SLGO_AGENT", "exported")
		if got := ask(t, "-agent", "greeter"); got != "host-for-greeter:7807" {
			t.Fatalf("addr %q", got)
		}
	})
	t.Run("environment", func(t *testing.T) {
		t.Setenv("SLGO_AGENT", "from-env")
		if got := ask(t); got != "host-for-from-env:7807" {
			t.Fatalf("addr %q", got)
		}
	})
	t.Run("neither", func(t *testing.T) {
		t.Setenv("SLGO_AGENT", "")
		if got := ask(t); got != "host-for-nobody:7807" {
			t.Fatalf("addr %q", got)
		}
	})
}

func TestAFailingSLHostDoesNotFetch(t *testing.T) {
	t.Setenv("SLGO_ADDR", "")
	fakeSLHost(t, "echo 'no idea' >&2; exit 1")
	called := false
	save := func(context.Context, string, string, string, string, bool) ([]string, error) {
		called = true
		return []string{"0.png"}, nil
	}
	var out, errw bytes.Buffer
	code := run(context.Background(), []string{"faces", "a sign", "out"}, &out, &errw, save)
	if code != 1 || called || out.Len() != 0 || !strings.Contains(errw.String(), "no idea") {
		t.Fatalf("exit %d, called %v, stdout %q, stderr %q", code, called, out.String(), errw.String())
	}
}

func TestFacesTakesTheAddressFromTheEnvironment(t *testing.T) {
	t.Setenv("SLGO_ADDR", "from-env:9")
	var addr string
	save := func(_ context.Context, a, _, _, _ string, _ bool) ([]string, error) {
		addr = a
		return []string{"0.png"}, nil
	}
	var out, errw bytes.Buffer
	if code := run(context.Background(), []string{"faces", "a sign", "out"}, &out, &errw, save); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errw.String())
	}
	if addr != "from-env:9" {
		t.Fatalf("addr %q", addr)
	}
}

func TestABlankObjectIsAnError(t *testing.T) {
	t.Setenv("SLGO_ADDR", "")
	t.Setenv("PATH", t.TempDir())
	save := func(context.Context, string, string, string, string, bool) ([]string, error) {
		return []string{"", ""}, nil
	}
	var out, errw bytes.Buffer
	code := run(context.Background(), []string{"faces", "a sign", "out"}, &out, &errw, save)
	if code != 1 || out.Len() != 0 || !strings.Contains(errw.String(), "no texture") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out.String(), errw.String())
	}
}

func TestAFailedFetchIsAnError(t *testing.T) {
	t.Setenv("SLGO_ADDR", "")
	t.Setenv("PATH", t.TempDir())
	save := func(context.Context, string, string, string, string, bool) ([]string, error) {
		return nil, errors.New("slpic: down")
	}
	var out, errw bytes.Buffer
	code := run(context.Background(), []string{"faces", "a sign", "out"}, &out, &errw, save)
	if code != 1 || out.Len() != 0 || errw.String() != "slpic: down\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out.String(), errw.String())
	}

	save = func(context.Context, string, string, string, string, bool) ([]string, error) {
		return nil, errors.New("connection refused")
	}
	out.Reset()
	errw.Reset()
	code = run(context.Background(), []string{"faces", "a sign", "out"}, &out, &errw, save)
	if code != 1 || errw.String() != "slpic: connection refused\n" {
		t.Fatalf("exit %d, stderr %q", code, errw.String())
	}
}

func TestFacesUsageDoesNotFetch(t *testing.T) {
	called := false
	save := func(context.Context, string, string, string, string, bool) ([]string, error) {
		called = true
		return nil, nil
	}
	var out, errw bytes.Buffer
	code := run(context.Background(), []string{"faces", "-raw"}, &out, &errw, save)
	if code != 2 || called || !strings.Contains(errw.String(), "usage:") {
		t.Fatalf("exit %d, called %v, stderr %q", code, called, errw.String())
	}
}

func TestFindReadsADrawnPicture(t *testing.T) {
	img := drawnPicture(t)
	var out, errw bytes.Buffer
	code := run(context.Background(), []string{"find", img, "drawing", "ring", "box"}, &out, &errw, nil)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errw.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 1 circle and 1 box\n%s", len(lines), out.String())
	}
	for i, line := range lines {
		kind, query, contents, x, y, w, h, cx, cy, err := splitItem(line)
		if err != nil {
			t.Fatalf("line %d: %v (%q)", i, err, line)
		}
		if cx != x+w/2 || cy != y+h/2 {
			t.Fatalf("center %d,%d for %d,%d %dx%d", cx, cy, x, y, w, h)
		}
		switch i {
		case 0:
			if kind != "drawing" || query != "ring" || contents != "circle" {
				t.Fatalf("line %d: %s %q %q", i, kind, query, contents)
			}
		default:
			if kind != "box" || query != "" || contents != "outlined box" {
				t.Fatalf("line %d: %s %q %q", i, kind, query, contents)
			}
		}
	}
}

func TestFindReadsAWord(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract is not on PATH")
	}
	img := drawnPicture(t)
	var out, errw bytes.Buffer
	code := run(context.Background(), []string{"find", img, "text", "Menu", "pattern", "^Menu$"}, &out, &errw, nil)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errw.String())
	}
	text, pattern := 0, 0
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "text\t\"Menu\"\t\"Menu\"\t"):
			text++
		case strings.HasPrefix(line, "pattern\t\"^Menu$\"\t\"Menu\"\t"):
			pattern++
		default:
			t.Fatalf("unexpected line %q", line)
		}
	}
	if text != 1 || pattern != 1 {
		t.Fatalf("text %d pattern %d\n%s", text, pattern, out.String())
	}
}

// drawnPicture writes a black ring, an outlined frame and the word Menu
// on white, so that no test here needs an image from anywhere else.
func drawnPicture(t *testing.T) string {
	t.Helper()
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
	for y := 40; y <= 140; y++ {
		for x := 40; x <= 140; x++ {
			if d := math.Hypot(float64(x-90), float64(y-90)); math.Abs(d-46) <= 4 {
				set(x, y)
			}
		}
	}
	for y := 200; y < 340; y++ {
		for x := 400; x < 640; x++ {
			if x < 404 || x >= 636 || y < 204 || y >= 336 {
				set(x, y)
			}
		}
	}
	// Menu, five by seven letters drawn at scale 8.
	glyphs := map[rune][7]string{
		'M': {"X...X", "XX.XX", "X.X.X", "X...X", "X...X", "X...X", "X...X"},
		'e': {".....", ".XXX.", "X...X", "XXXXX", "X....", "X....", ".XXX."},
		'n': {".....", "XXXX.", "X...X", "X...X", "X...X", "X...X", "X...X"},
		'u': {".....", "X...X", "X...X", "X...X", "X...X", "X..XX", ".XXX."},
	}
	x0 := 400
	for _, r := range "Menu" {
		for ry, row := range glyphs[r] {
			for rx, c := range row {
				if c != 'X' {
					continue
				}
				for dy := 0; dy < 8; dy++ {
					for dx := 0; dx < 8; dx++ {
						set(x0+rx*8+dx, 380+ry*8+dy)
					}
				}
			}
		}
		x0 += 48
	}
	path := filepath.Join(t.TempDir(), "picture.png")
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
	return path
}

func TestFindReportsAMissingFileAndABadRequest(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such.png")
	var out, errw bytes.Buffer
	code := run(context.Background(), []string{"find", missing, "box"}, &out, &errw, nil)
	if code != 1 || out.Len() != 0 || !strings.Contains(errw.String(), "no-such.png") {
		t.Fatalf("missing: exit %d, stdout %q, stderr %q", code, out.String(), errw.String())
	}

	out.Reset()
	errw.Reset()
	code = run(context.Background(), []string{"find", missing, "pattern", "("}, &out, &errw, nil)
	if code != 1 || !strings.Contains(errw.String(), "pattern") {
		t.Fatalf("pattern: exit %d, stderr %q", code, errw.String())
	}

	out.Reset()
	errw.Reset()
	code = run(context.Background(), []string{"find", missing, "triangle"}, &out, &errw, nil)
	if code != 2 || !strings.Contains(errw.String(), "unknown request") {
		t.Fatalf("unknown: exit %d, stderr %q", code, errw.String())
	}

	out.Reset()
	errw.Reset()
	code = run(context.Background(), []string{"find", missing}, &out, &errw, nil)
	if code != 2 || !strings.Contains(errw.String(), "nothing to look for") {
		t.Fatalf("no request: exit %d, stderr %q", code, errw.String())
	}
}

func TestNoCommandIsUsage(t *testing.T) {
	var out, errw bytes.Buffer
	if code := run(context.Background(), nil, &out, &errw, nil); code != 2 || !strings.Contains(errw.String(), "usage:") {
		t.Fatalf("exit %d, stderr %q", code, errw.String())
	}
	out.Reset()
	errw.Reset()
	if code := run(context.Background(), []string{"-h"}, &out, &errw, nil); code != 0 || !strings.Contains(errw.String(), "usage:") {
		t.Fatalf("help: exit %d, stderr %q", code, errw.String())
	}
	out.Reset()
	errw.Reset()
	if code := run(context.Background(), []string{"-version"}, &out, &errw, nil); code != 0 || !strings.Contains(out.String(), "slpic") {
		t.Fatalf("version: exit %d, stdout %q, stderr %q", code, out.String(), errw.String())
	}
}

// fakeSLHost puts a script called sl-host on $PATH. The test's PATH is
// restored when it ends, which is how the command is asked without
// whatever sl-host this machine really has.
func fakeSLHost(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sl-host"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

type rawCall struct {
	addr, agent, name, dir string
	raw                    bool
}

func splitItem(line string) (kind, query, contents string, x, y, w, h, cx, cy int, err error) {
	f := strings.Split(line, "\t")
	if len(f) != 9 {
		return "", "", "", 0, 0, 0, 0, 0, 0, errors.New("want 9 fields")
	}
	query, err = strconv.Unquote(f[1])
	if err != nil {
		return "", "", "", 0, 0, 0, 0, 0, 0, err
	}
	contents, err = strconv.Unquote(f[2])
	if err != nil {
		return "", "", "", 0, 0, 0, 0, 0, 0, err
	}
	nums := make([]int, 6)
	for i, s := range f[3:] {
		nums[i], err = strconv.Atoi(s)
		if err != nil {
			return "", "", "", 0, 0, 0, 0, 0, 0, err
		}
	}
	return f[0], query, contents, nums[0], nums[1], nums[2], nums[3], nums[4], nums[5], nil
}

// memWorld is the session writeFaces talks to, with nothing behind it.
// FacePicture refuses what the real one refuses, by the same sentinels.
type memWorld struct {
	objects []*sl.Seen
	faces   []sl.Face
	images  map[msg.UUID]image.Image
	anim    map[int]bool // faces whose texture animation is running
	objErr  error
	fetches int
	failAt  int // TextureImage fails on this call, counting from 1
}

func (m *memWorld) ObjectsNamed(_ context.Context, name string, _ time.Duration) ([]*sl.Seen, error) {
	if m.objErr != nil {
		return nil, m.objErr
	}
	var out []*sl.Seen
	for _, s := range m.objects {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *memWorld) Faces(context.Context, *sl.Object) ([]sl.Face, error) { return m.faces, nil }

func (m *memWorld) TextureImage(_ context.Context, id msg.UUID) (image.Image, error) {
	m.fetches++
	if m.failAt > 0 && m.fetches == m.failAt {
		return nil, errors.New("the texture did not arrive")
	}
	img, ok := m.images[id]
	if !ok {
		return nil, errors.New("no texture")
	}
	return img, nil
}

func (m *memWorld) FacePicture(ctx context.Context, _ *sl.Object, face int) (image.Image, error) {
	f := m.faces[face]
	switch {
	case f.Texture == (msg.UUID{}):
		return nil, fmt.Errorf("face %d: %w", face, sl.ErrNoTexture)
	case f.Planar():
		return nil, fmt.Errorf("face %d: %w", face, sl.ErrPlanarFace)
	case m.anim[face]:
		return nil, fmt.Errorf("face %d: %w", face, sl.ErrAnimatedFace)
	}
	tex, err := m.TextureImage(ctx, f.Texture)
	if err != nil {
		return nil, err
	}
	return f.Picture(tex)
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

func readPNG(t *testing.T, path string) *image.NRGBA {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	r := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		for x := 0; x < r.Dx(); x++ {
			out.SetNRGBA(x, y, color.NRGBAModel.Convert(img.At(r.Min.X+x, r.Min.Y+y)).(color.NRGBA))
		}
	}
	return out
}

func aSign(faces []sl.Face, id msg.UUID, tex image.Image) *memWorld {
	return &memWorld{
		objects: []*sl.Seen{{Object: sl.Object{Name: "a sign"}}},
		faces:   faces,
		images:  map[msg.UUID]image.Image{id: tex},
	}
}

func TestWriteFacesWritesWhatEachFaceShows(t *testing.T) {
	id := msg.UUID{0x01}
	plain := nrgba(4, 4, func(x, y int) color.NRGBA {
		return color.NRGBA{R: uint8(x + 1), G: uint8(y + 1), A: 255}
	})
	faces := sl.PlainFaces(3)
	faces[0].Texture = id
	faces[0].SetOffsets(0.25, 0)
	faces[2].Texture = id // face 1 has none
	w := aSign(faces, id, plain)
	dir := t.TempDir()
	paths, err := writeFaces(context.Background(), w, "a sign", dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 || paths[0] != filepath.Join(dir, "0.png") || paths[1] != "" || paths[2] != filepath.Join(dir, "2.png") {
		t.Fatalf("paths %q", paths)
	}
	want0, err := faces[0].Picture(plain)
	if err != nil {
		t.Fatal(err)
	}
	if got := readPNG(t, paths[0]); !bytes.Equal(got.Pix, want0.Pix) {
		t.Fatal("face 0 is not the offset texture")
	}
	if got := readPNG(t, paths[2]); !bytes.Equal(got.Pix, plain.Pix) {
		t.Fatal("face 2 is not the texture")
	}
	if _, err := os.Stat(filepath.Join(dir, "1.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the blank face was written: %v", err)
	}
}

func TestWriteFacesRefusesWhatItCannotShow(t *testing.T) {
	id := msg.UUID{0x01}
	src := nrgba(2, 2, func(x, y int) color.NRGBA { return color.NRGBA{A: 255} })
	two := sl.PlainFaces(2)
	two[0].Texture, two[1].Texture = id, id
	planar := sl.PlainFaces(2)
	planar[0].Texture, planar[1].Texture = id, id
	planar[1].Media = 0x02
	animating := aSign(two, id, src)
	animating.anim = map[int]bool{1: true}
	slow := aSign(two, id, src)
	slow.failAt = 2

	cases := []struct {
		what string
		w    *memWorld
		name string
		dir  string
		want string
	}{
		{"no name", &memWorld{}, "", "d", "no object name"},
		{"no directory", &memWorld{}, "a sign", "", "no directory"},
		{"nothing of that name", &memWorld{objects: []*sl.Seen{{Object: sl.Object{Name: "other"}}}}, "a sign", "d", "not in the region"},
		{"two of that name", &memWorld{objects: []*sl.Seen{
			{Object: sl.Object{Name: "a sign"}}, {Object: sl.Object{Name: "a sign"}},
		}}, "a sign", "d", "names 2 objects"},
		{"the region refuses", &memWorld{objErr: errors.New("down")}, "a sign", "d", "down"},
		{"planar", aSign(planar, id, src), "a sign", "d", "face 1 of a sign is planar"},
		{"animating", animating, "a sign", "d", "face 1 of a sign has a texture animation"},
		{"the fetch fails", slow, "a sign", "d", "face 1 of a sign"},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			dir := c.dir
			if dir != "" {
				dir = t.TempDir()
			}
			_, err := writeFaces(context.Background(), c.w, c.name, dir, false)
			if err == nil || !strings.HasPrefix(err.Error(), "slpic: ") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v, want slpic: and %q", err, c.want)
			}
			left, _ := os.ReadDir(dir)
			for _, e := range left {
				t.Fatalf("left %s behind", e.Name())
			}
		})
	}
}

func TestRawWritesTheUploadedTextureEvenOnAPlanarFace(t *testing.T) {
	id := msg.UUID{0x01}
	plain := nrgba(4, 4, func(x, y int) color.NRGBA {
		return color.NRGBA{R: uint8(x + 1), G: uint8(y + 1), A: 255}
	})
	faces := sl.PlainFaces(3)
	faces[0].Texture = id
	faces[0].SetOffsets(0.25, 0)
	faces[0].Media = 0x02 // planar: the upload is still this texture
	faces[1].Texture = id
	w := aSign(faces, id, plain)
	w.anim = map[int]bool{1: true}
	paths, err := writeFaces(context.Background(), w, "a sign", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if paths[0] == "" || paths[1] == "" || paths[2] != "" {
		t.Fatalf("paths %q", paths)
	}
	if w.fetches != 1 {
		t.Fatalf("fetched the texture %d times", w.fetches)
	}
	for _, p := range paths[:2] {
		if got := readPNG(t, p); !bytes.Equal(got.Pix, plain.Pix) {
			t.Fatalf("%s is not the uploaded texture", p)
		}
	}
}

func TestAFaceAnimatingElsewhereIsStillWritten(t *testing.T) {
	id := msg.UUID{0x01}
	src := nrgba(2, 2, func(x, y int) color.NRGBA { return color.NRGBA{R: 9, A: 255} })
	faces := sl.PlainFaces(2)
	faces[0].Texture = id
	w := aSign(faces, id, src)
	w.anim = map[int]bool{1: true} // face 1 has no texture
	paths, err := writeFaces(context.Background(), w, "a sign", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if paths[0] == "" || paths[1] != "" {
		t.Fatalf("paths %q", paths)
	}
}
