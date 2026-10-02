// Command slpic stores the picture on an object in Second Life, and
// finds a label or a simple figure in a picture.
//
//	slpic faces "a sign" ./faces
//	slpic faces -raw "a sign" ./textures
//	slpic find ./faces/0.png text Menu drawing circle box
//
// faces dials an slgod that is already holding the avatar and writes
// one PNG per face into the directory: 0.png, 1.png, and so on.  A
// face with no texture is skipped.  The picture is what the face
// shows, turned, tiled and shifted.  -raw stores the texture as it
// was uploaded.
//
// find reads one image.  A request is one of:
//
//	text TEXT       exact words, such as "Example Menu"
//	pattern EXPR    a regular expression
//	drawing NAME    circle, arrow, or a direction such as "left arrow"
//	box             every outlined box
//
// Several requests may follow the image.  A match is one tab-separated
// line: the kind, the query, the contents, the top-left, the width and
// height, and the center.  The query and the contents are quoted.
// Nothing found prints no lines, and the command succeeds.  Text and
// patterns need the tesseract binary on PATH.  Drawings and boxes are
// read from the pixels.
//
// -addr is where slgod is, and $SLGO_ADDR is that same answer when the
// flag is empty.  With neither set, where slgod runs is a question for
// sl-host, the same question slsh asks, and a machine without sl-host
// means this one, localhost:7807.  -agent chooses the avatar.  An
// empty one is left for sl.DialWeak, which reads $SLGO_AGENT, or takes
// the avatar slgod has held longest.  sl-host is told that same name.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/imgfind"
	"github.com/quark-idlemind/slgo/internal/slhost"
	"github.com/quark-idlemind/slgo/internal/version"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, saveFaces))
}

// faceSave fetches one object's pictures into dir.  When raw is set
// the picture is the uploaded texture, and otherwise the picture the
// face shows.
type faceSave func(ctx context.Context, addr, agent, name, dir string, raw bool) ([]string, error)

func run(ctx context.Context, args []string, out, errw io.Writer, save faceSave) int {
	if len(args) == 0 {
		usage(errw)
		return 2
	}
	switch args[0] {
	case "faces":
		return runFaces(ctx, args[1:], out, errw, save)
	case "find":
		return runFind(args[1:], out, errw)
	case "-h", "-help", "--help":
		usage(errw)
		return 0
	case "-version", "--version":
		fmt.Fprintln(out, version.String("slpic"))
		return 0
	default:
		fmt.Fprintf(errw, "slpic: unknown command %q\n", args[0])
		usage(errw)
		return 2
	}
}

func usage(errw io.Writer) {
	fmt.Fprint(errw, `usage: slpic faces [-addr ADDR] [-agent NAME] [-raw] OBJECT DIR
       slpic find IMAGE REQUEST...

faces writes one PNG per face of OBJECT into DIR, named 0.png, 1.png,
and so on. A face with no texture is skipped. The picture is what the
face shows: turned, tiled and shifted. -raw stores the texture as it
was uploaded.

find looks through IMAGE. A REQUEST is one of:

  text TEXT       the exact words
  pattern EXPR    a regular expression
  drawing NAME    circle, arrow, left arrow, right arrow, up arrow, down arrow
  box             every outlined box

Each match is one tab-separated line:

  KIND  QUERY  CONTENTS  X  Y  WIDTH  HEIGHT  CENTERX  CENTERY

QUERY and CONTENTS are quoted. Nothing found prints nothing.

-addr is where slgod is. Empty asks sl-host, and a machine without
sl-host uses localhost:7807. $SLGO_ADDR is the same as -addr.
-agent is which avatar (default $SLGO_AGENT, or the one held longest).
sl-host is asked about that avatar.
`)
}

func runFaces(ctx context.Context, args []string, out, errw io.Writer, save faceSave) int {
	fs := flag.NewFlagSet("slpic faces", flag.ContinueOnError)
	fs.SetOutput(errw)
	addr := fs.String("addr", "", "the slgod to attach to; default sl-host, or this machine ($SLGO_ADDR)")
	agent := fs.String("agent", "", "which avatar (default $SLGO_AGENT, or the one held longest)")
	raw := fs.Bool("raw", false, "store the texture as uploaded, before the face turns, tiles and shifts it")
	fs.Usage = func() {
		fmt.Fprintln(errw, "usage: slpic faces [-addr ADDR] [-agent NAME] [-raw] OBJECT DIR")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return 2
	}
	at := *addr
	if at == "" {
		at = os.Getenv("SLGO_ADDR")
	}
	// Nothing on the command line and nothing in the environment leaves
	// the question to sl-host, which is how one binary works on a
	// machine whose slgod is somewhere else.  It is asked about the
	// avatar this command is about to attach to.
	at, err := slhost.ResolveFor(at, sl.AgentName(*agent))
	if err != nil {
		complain(errw, err)
		return 1
	}
	paths, err := save(ctx, at, *agent, fs.Arg(0), fs.Arg(1), *raw)
	if err != nil {
		complain(errw, err)
		return 1
	}
	n := 0
	for _, p := range paths {
		if p == "" {
			continue
		}
		fmt.Fprintln(out, p)
		n++
	}
	if n == 0 {
		fmt.Fprintf(errw, "slpic: %s has no texture\n", fs.Arg(0))
		return 1
	}
	return 0
}

// saveFaces attaches to the avatar and writes the pictures.
//
// The directory is made first, so a path that cannot be written fails
// before the dial.  The dial itself is limited to 30 seconds.  Naming
// the objects and fetching the textures keep the caller's context, and
// the session's own waits.
func saveFaces(ctx context.Context, addr, agent, name, dir string, raw bool) ([]string, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	dial, cancel := context.WithTimeout(ctx, 30*time.Second)
	s, err := sl.DialWeak(dial, addr, agent)
	cancel()
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return writeFaces(ctx, s, name, dir, raw)
}

// world is the part of a session the faces are read through.
type world interface {
	ObjectsNamed(context.Context, string, time.Duration) ([]*sl.Seen, error)
	Faces(context.Context, *sl.Object) ([]sl.Face, error)
	TextureImage(context.Context, msg.UUID) (image.Image, error)
	FacePicture(context.Context, *sl.Object, int) (image.Image, error)
}

// writeFaces writes dir/N.png for each textured face of the one object
// named name, and returns the paths in face order with "" for a face
// with no texture.  The picture is what the face shows, from
// FacePicture, or with raw the texture as uploaded.  A planar face or
// one with a running texture animation is an error unless raw, and
// then no file is left behind.  Faces that wear one texture fetch it
// once with raw, and once each otherwise.
// Why: doc/face-pictures.md#which-faces-are-refused
func writeFaces(ctx context.Context, w world, name, dir string, raw bool) (paths []string, err error) {
	if name == "" {
		return nil, fmt.Errorf("slpic: no object name")
	}
	if dir == "" {
		return nil, fmt.Errorf("slpic: no directory to write the face into")
	}
	found, err := w.ObjectsNamed(ctx, name, 0)
	if err != nil {
		return nil, fmt.Errorf("slpic: %w", err)
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("slpic: %s is not in the region, or is beyond the draw distance", name)
	case 1:
	default:
		return nil, fmt.Errorf("slpic: %s names %d objects", name, len(found))
	}
	obj := &found[0].Object
	faces, err := w.Faces(ctx, obj)
	if err != nil {
		return nil, fmt.Errorf("slpic: %w", err)
	}

	paths = make([]string, len(faces))
	var written []string
	defer func() {
		if err != nil {
			for _, p := range written {
				os.Remove(p)
			}
		}
	}()
	cache := map[msg.UUID]image.Image{}
	for i, f := range faces {
		if f.Texture == (msg.UUID{}) {
			continue
		}
		var pic image.Image
		if raw {
			var ok bool
			if pic, ok = cache[f.Texture]; !ok {
				if pic, err = w.TextureImage(ctx, f.Texture); err != nil {
					return nil, fmt.Errorf("slpic: face %d of %s: %w", i, name, err)
				}
				cache[f.Texture] = pic
			}
		} else {
			pic, err = w.FacePicture(ctx, obj, i)
			switch {
			case errors.Is(err, sl.ErrNoTexture):
				continue
			case errors.Is(err, sl.ErrPlanarFace):
				return nil, fmt.Errorf("slpic: face %d of %s is planar, so its offset, repeats and rotation are not the picture on it", i, name)
			case errors.Is(err, sl.ErrAnimatedFace):
				return nil, fmt.Errorf("slpic: face %d of %s has a texture animation, so one still picture is not what it shows", i, name)
			case err != nil:
				return nil, fmt.Errorf("slpic: face %d of %s: %w", i, name, err)
			}
		}
		path := filepath.Join(dir, strconv.Itoa(i)+".png")
		if err = writePNG(path, pic); err != nil {
			return nil, err
		}
		paths[i] = path
		written = append(written, path)
	}
	return paths, nil
}

// writePNG writes img to path by a temporary name in the same directory
// and then renames it, so a reader never opens a half-written file.
func writePNG(path string, img image.Image) error {
	tmp := path + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	err = png.Encode(f, img)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func runFind(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("slpic find", flag.ContinueOnError)
	fs.SetOutput(errw)
	fs.Usage = func() {
		fmt.Fprint(errw, `usage: slpic find IMAGE REQUEST...

A REQUEST is one of:

  text TEXT       the exact words
  pattern EXPR    a regular expression
  drawing NAME    circle, arrow, left arrow, right arrow, up arrow, down arrow
  box             every outlined box
`)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return 2
	}
	reqs, err := parseRequests(fs.Args()[1:])
	if err != nil {
		fmt.Fprintf(errw, "slpic: %v\n", err)
		fs.Usage()
		return 2
	}
	items, err := imgfind.FindFile(fs.Arg(0), reqs...)
	if err != nil {
		complain(errw, err)
		return 1
	}
	for _, it := range items {
		fmt.Fprintf(out, "%s\t%q\t%q\t%d\t%d\t%d\t%d\t%d\t%d\n",
			kindName(it.Request.Kind), queryOf(it.Request), it.Contents,
			it.Location.X, it.Location.Y, it.Width, it.Height,
			it.Center.X, it.Center.Y)
	}
	return 0
}

func parseRequests(args []string) ([]*imgfind.Request, error) {
	var out []*imgfind.Request
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "text":
			s, err := take(args, &i, "text")
			if err != nil {
				return nil, err
			}
			out = append(out, imgfind.Text(s))
		case "pattern":
			s, err := take(args, &i, "pattern")
			if err != nil {
				return nil, err
			}
			out = append(out, imgfind.Pattern(s))
		case "drawing":
			s, err := take(args, &i, "drawing")
			if err != nil {
				return nil, err
			}
			out = append(out, imgfind.Drawing(s))
		case "box":
			out = append(out, imgfind.Box())
		default:
			return nil, fmt.Errorf("unknown request %q", args[i])
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("nothing to look for")
	}
	return out, nil
}

func take(args []string, i *int, what string) (string, error) {
	*i++
	if *i >= len(args) {
		return "", fmt.Errorf("%s needs a value", what)
	}
	return args[*i], nil
}

func kindName(k imgfind.Kind) string {
	switch k {
	case imgfind.KindText:
		return "text"
	case imgfind.KindPattern:
		return "pattern"
	case imgfind.KindDrawing:
		return "drawing"
	case imgfind.KindBox:
		return "box"
	default:
		return "kind" + strconv.Itoa(int(k))
	}
}

func queryOf(r *imgfind.Request) string {
	switch r.Kind {
	case imgfind.KindText:
		return r.Text
	case imgfind.KindPattern:
		return r.Pattern
	case imgfind.KindDrawing:
		return r.Drawing
	default:
		return ""
	}
}

func complain(errw io.Writer, err error) {
	line := err.Error()
	if !strings.HasPrefix(line, "slpic:") {
		line = "slpic: " + line
	}
	fmt.Fprintln(errw, line)
}
