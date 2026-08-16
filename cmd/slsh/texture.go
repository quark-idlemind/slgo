package main

// Getting a texture out of Second Life and onto the disk as something
// that can be looked at.
//
// The grid deals in JPEG 2000 codestreams and nothing on this machine
// opens one, so what a person asking for a texture wants is a PNG.  The
// conversion is in sl; this is the command that asks for it.
//
// An asset id is accepted as readily as an inventory path, and that is
// not a convenience -- it is most of the point.  The content delivery
// network serves textures by id alone, so a texture on somebody else's
// object, named nowhere in this avatar's inventory, is fetchable the
// moment its id is known.  Verified against the beta grid: textures
// belonging to two other avatars, fetched and decoded.

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/disintegration/imaging"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var textureCommands = map[string]*command{
	"get": {
		params: "PATH|UUID",
		flags:  func() any { return new(getFlags) },
		brief:  "save a texture as a PNG, by inventory path or by asset id",
		man:    "get",
		run:    cmdGet,
	},
	"put": {
		params: "FILE",
		flags:  func() any { return new(putFlags) },
		brief:  "upload an image as a texture; costs L$, so -N says what it would do",
		man:    "put",
		run:    cmdPut,
	},
}

type getFlags struct {
	Out  string `getopt:"--out -o=FILE  write here, rather than to the texture's name"`
	Raw  bool   `getopt:"--raw -r       write the codestream as the grid stores it, undecoded"`
	Help bool   `getopt:"--help -h      show what this command takes"`
}

func cmdGet(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o getFlags
	args, done, err := subOptions("get", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 1 {
		return usageError("get")
	}

	// An id is an asset id here, not an item id: it is what the content
	// delivery network answers to, and it need not be ours or be in any
	// inventory at all.
	asset, name := msg.UUID{}, ""
	if id, err := msg.ParseUUID(args[0]); err == nil {
		asset, name = id, id.String()
	} else {
		e, err := sh.entryAt(ctx, args[0])
		if err != nil {
			return err
		}
		if e.Folder {
			// A folder's Type is its preferred contents, so without
			// this a folder reports itself as whatever it holds.
			return fmt.Errorf("%s is a folder, and get is for textures", e.Name)
		}
		if sl.AssetType(e.Type) != sl.AssetTexture {
			return fmt.Errorf("%s is a %s, and get is for textures", e.Name, sl.AssetType(e.Type))
		}
		if e.Asset.IsZero() {
			return fmt.Errorf("%s names no asset, so there is nothing to fetch", e.Name)
		}
		asset, name = e.Asset, e.Name
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	b, err := sh.s.Texture(ctx, asset)
	if err != nil {
		return err
	}

	if o.Raw {
		path := o.Out
		if path == "" {
			path = safeName(name) + ".j2c"
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %d bytes\n", path, len(b))
		return nil
	}

	img, err := sl.DecodeTexture(b)
	if err != nil {
		return fmt.Errorf("%s: %w (--raw writes it undecoded)", name, err)
	}
	path := o.Out
	if path == "" {
		path = safeName(name) + ".png"
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %dx%d, from %d bytes of jpeg 2000\n",
		path, img.Bounds().Dx(), img.Bounds().Dy(), len(b))
	return nil
}

// safeName turns an inventory name into a filename.
//
// Inventory names may hold very nearly any printable character -- see
// the README -- so a name used as a path can escape the directory it
// was meant for.  Only the separators are a danger, and replacing them
// keeps a name recognisable where sanitising to alphanumerics would
// not.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == filepath.Separator || r == '/' || r == '\\' || r == 0:
			return '_'
		case r < 0x20:
			return '_'
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	switch s {
	case "":
		return "texture"
	case ".", "..":
		// Harmless as text and a directory as a path, which is the one
		// case where replacing the separators is not enough.
		return "_" + s
	}
	return s
}

// ------------------------------------------------------------- putting

// filters are the resampling filters put will name, from imaging.
//
// Every filter that package offers is here, because which one suits a
// picture is not something a shell can decide: a photograph wants
// lanczos, a diagram wants catmullrom, and pixel art wants nearest,
// which is the one case where any smoothing is simply wrong.
var filters = map[string]*sl.Filter{
	"nearest":    &imaging.NearestNeighbor,
	"box":        &imaging.Box,
	"linear":     &imaging.Linear,
	"hermite":    &imaging.Hermite,
	"mitchell":   &imaging.MitchellNetravali,
	"catmullrom": &imaging.CatmullRom,
	"bspline":    &imaging.BSpline,
	"gaussian":   &imaging.Gaussian,
	"bartlett":   &imaging.Bartlett,
	"lanczos":    &imaging.Lanczos,
	"hann":       &imaging.Hann,
	"hamming":    &imaging.Hamming,
	"blackman":   &imaging.Blackman,
	"welch":      &imaging.Welch,
	"cosine":     &imaging.Cosine,
}

// filterAdvice answers the question somebody actually has.
//
// A list of filters with a description each answers "what is
// MitchellNetravali for", which nobody asked: the person at the prompt
// has a picture in front of them and wants to know what to type. So the
// kind of picture is the key and the filter is the answer, and the
// order is how likely each is rather than anything alphabetical.
//
// Everything not named here is a variation on lanczos and is listed
// after, because a guide that hides options is as bad as one that
// ranks none.
var filterAdvice = []struct {
	kind   string
	filter string
	why    string
}{
	{"a photograph", "lanczos", "the default; the best of the slow ones"},
	{"pixel art, or an icon with hard edges", "nearest", "invents no colours, and any smoothing ruins these"},
	{"a mask, or anything read as data", "nearest", "same reason: a blended value is a wrong value"},
	{"a diagram, text, or a screenshot", "catmullrom", "sharp, and quicker than lanczos"},
	{"a picture lanczos leaves haloed", "mitchell", "smooth, with much less ringing at edges"},
	{"a big reduction, a quarter or less", "box", "plain averaging, which is what a reduction that size wants"},
	{"something you want softer on purpose", "gaussian", "blurs as it resamples"},
	{"a preview, where speed is the point", "linear", "fast and unremarkable"},
}

// filterFamilies are the rest, grouped by what they are rather than
// listed flat.
//
// The grouping is not decoration: "try another one of these" is only
// useful advice if the alternatives are actually alike, and a cubic is
// not a windowed sinc however alphabetically adjacent.
var filterFamilies = []struct {
	what    string
	members []string
}{
	{"other cubics, as mitchell is; bspline is the softest of them",
		[]string{"bspline", "hermite"}},
	{"windowed sincs, as lanczos is; worth trying when it rings",
		[]string{"bartlett", "blackman", "cosine", "hamming", "hann", "welch"}},
}

// helpFilters prints the guide.
func helpFilters(out io.Writer) {
	fmt.Fprintf(out, "Which --filter to use, by what you are uploading:\n\n")
	for _, a := range filterAdvice {
		fmt.Fprintf(out, "  %-38s %-11s %s\n", a.kind, a.filter, a.why)
	}

	fmt.Fprintf(out, "\nAlso accepted:\n")
	for _, f := range filterFamilies {
		fmt.Fprintf(out, "  %s\n      %s\n", strings.Join(f.members, ", "), f.what)
	}

	// Anything the guide forgot. It should never print, and a test says
	// it does not -- but a filter added to the map and nowhere else
	// should still be findable rather than silently unmentioned.
	named := map[string]bool{}
	for _, a := range filterAdvice {
		named[a.filter] = true
	}
	for _, f := range filterFamilies {
		for _, m := range f.members {
			named[m] = true
		}
	}
	var rest []string
	for _, n := range namesOf(filters) {
		if !named[n] {
			rest = append(rest, n)
		}
	}
	if len(rest) > 0 {
		fmt.Fprintf(out, "  %s\n      accepted, and nothing here says what for\n", strings.Join(rest, ", "))
	}

	fmt.Fprintf(out, "\nThe filter only matters when the picture is resized at all.\n"+
		"Nothing is resized if both sides are already powers of two.\n")
}

// roundings are what --round takes.
var roundings = map[string]sl.Rounding{
	"nearest": sl.RoundNearest,
	"up":      sl.RoundUp,
	"down":    sl.RoundDown,
}

func namesOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type putFlags struct {
	Name   string `getopt:"--name -n=NAME     call it this in inventory, rather than the file's name"`
	Desc   string `getopt:"--desc -d=TEXT     the item's description"`
	Folder string `getopt:"--folder -f=PATH   put it here, rather than in Textures"`

	// Every one of these names its own values.  A reader who lands on
	// --round-y and finds "this way instead" has been told nothing, and
	// the flag above it is not where they were looking.
	Round string `getopt:"--round=MODE       round both sides to a power of two: nearest, up or down"`
	RX    string `getopt:"--round-x=MODE     round the width: nearest, up or down"`
	RY    string `getopt:"--round-y=MODE     round the height: nearest, up or down"`

	Filter   string  `getopt:"--filter=NAME      resample with this; --filters says which to pick"`
	Filters  bool    `getopt:"--filters         list the filters, and what each is good for"`
	Lossless bool    `getopt:"--lossless -l      store every pixel, at several times the size"`
	Ratio    float64 `getopt:"--ratio=N          compress about N:1, rather than the default 8"`

	Out    string `getopt:"--out -o=FILE      write the result here instead of uploading; the extension picks the format"`
	DryRun bool   `getopt:"--dry-run -N       say what it would upload and what it would cost"`
	Help   bool   `getopt:"--help -h          show what this command takes"`
}

// resize turns the flags into what sl.Resize wants, complaining about a
// name it does not know rather than quietly using the default.
func (o putFlags) resize() (sl.ResizeOptions, error) {
	var r sl.ResizeOptions
	pick := func(name string) (sl.Rounding, error) {
		v, ok := roundings[name]
		if !ok {
			return 0, fmt.Errorf("no rounding called %q; there is %s",
				name, strings.Join(namesOf(roundings), ", "))
		}
		return v, nil
	}
	if o.Round != "" {
		v, err := pick(o.Round)
		if err != nil {
			return r, err
		}
		r.Horizontal, r.Vertical = v, v
	}
	for _, c := range []struct {
		name string
		to   *sl.Rounding
	}{{o.RX, &r.Horizontal}, {o.RY, &r.Vertical}} {
		if c.name == "" {
			continue
		}
		v, err := pick(c.name)
		if err != nil {
			return r, err
		}
		*c.to = v
	}
	if o.Filter != "" {
		f, ok := filters[strings.ToLower(o.Filter)]
		if !ok {
			return r, fmt.Errorf("no filter called %q; there is %s.  \"put --filters\" says which to pick",
				o.Filter, strings.Join(namesOf(filters), ", "))
		}
		r.Filter = f
	}
	return r, nil
}

// codestreamExt are the files that are already a texture and go up as
// they are.  Re-encoding one would cost quality for nothing.
var codestreamExt = map[string]bool{".j2c": true, ".j2k": true, ".jpc": true, ".jp2": true}

func cmdPut(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o putFlags
	args, done, err := subOptions("put", &o, out, args)
	if err != nil || done {
		return err
	}
	if o.Filters {
		helpFilters(out)
		return nil
	}
	if len(args) != 1 {
		return usageError("put")
	}
	file := args[0]

	name := o.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	}

	// Where it lands.  Zero means the system Textures folder, which is
	// what sl.UploadAsset resolves for itself.
	var folder msg.UUID
	if o.Folder != "" {
		e, err := sh.entryAt(ctx, o.Folder)
		if err != nil {
			return err
		}
		if !e.Folder {
			return fmt.Errorf("%s is not a folder", o.Folder)
		}
		folder = e.ID
	}

	p, err := o.prepare(file)
	if err != nil {
		return err
	}
	w, h, err := sl.TextureDims(p.body)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}

	fmt.Fprintf(out, "%s: %s, %d bytes, L$%d\n",
		filepath.Base(file), p.note, len(p.body), sl.UploadFee(w, h))

	// Writing it out instead of uploading, so that what the filter and
	// the rounding did can be looked at before any of it is paid for.
	if o.Out != "" {
		if err := p.write(o.Out); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %dx%d, not uploaded\n", o.Out, w, h)
		return nil
	}
	if o.DryRun {
		fmt.Fprintln(out, "not uploaded: --dry-run")
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	it, _, err := sh.s.UploadAsset(ctx, sl.Upload{
		Name: name, Desc: o.Desc, Folder: folder, Type: sl.AssetTexture,
	}, p.body)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %s\n", it.Name, it.ID)
	return nil
}

// prepared is a file turned into something the grid would take.
//
// It carries the picture as well as the bytes because -o may want to
// write either one, and because decoding a codestream to write a PNG of
// it is work worth doing once.
type prepared struct {
	body []byte      // the codestream
	img  image.Image // what it was made from, or nil for one read off disk
	note string      // what happened, for the caller to print
}

// prepare reads the file and works out what to upload.
//
// A codestream goes up untouched: it is already what the grid stores,
// and decoding and re-encoding one would lose quality to no purpose.
func (o putFlags) prepare(file string) (prepared, error) {
	if codestreamExt[strings.ToLower(filepath.Ext(file))] {
		b, err := os.ReadFile(file)
		return prepared{body: b, note: "a codestream, uploaded unchanged"}, err
	}

	// imaging.Open handles png, jpeg, gif, bmp and tiff, and applies
	// the EXIF orientation a camera leaves behind -- which matters
	// here, since a texture that arrives sideways stays sideways.
	m, err := imaging.Open(file, imaging.AutoOrientation(true))
	if err != nil {
		return prepared{}, err
	}
	r, err := o.resize()
	if err != nil {
		return prepared{}, err
	}
	from := m.Bounds().Size()

	body, resized, err := sl.EncodeResized(m, r, sl.TextureOptions{
		Lossless: o.Lossless, Ratio: o.Ratio,
	})
	if err != nil {
		return prepared{}, err
	}
	p := prepared{body: body, img: resized}
	if to := resized.Bounds().Size(); to != from {
		p.note = fmt.Sprintf("%dx%d -> %dx%d", from.X, from.Y, to.X, to.Y)
	} else {
		p.note = fmt.Sprintf("%dx%d, a size the grid already takes", from.X, from.Y)
	}
	return p, nil
}

// writable are the picture formats -o can produce, which are the ones
// imaging.Save knows.
var writable = []string{".png", ".jpg", ".jpeg", ".gif", ".tif", ".tiff", ".bmp"}

// write puts the result on disk, in the format the name asks for.
//
// The extension decides, because that is the only thing a person typing
// a filename has already said about the format they want, and a flag
// saying it again is a flag that can disagree with the name.
func (p prepared) write(path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	if codestreamExt[ext] {
		return os.WriteFile(path, p.body, 0o644)
	}
	if !slices.Contains(writable, ext) {
		return fmt.Errorf("nothing here writes %q; -o takes %s, or %s for the codestream",
			ext, strings.Join(writable, ", "), strings.Join(namesOf(codestreamExt), ", "))
	}

	// A codestream read off disk was never decoded, so decode it now:
	// somebody asking for a PNG of a .j2c wants to look at it.
	m := p.img
	if m == nil {
		d, err := sl.DecodeTexture(p.body)
		if err != nil {
			return err
		}
		m = d
	}
	return imaging.Save(m, path)
}
