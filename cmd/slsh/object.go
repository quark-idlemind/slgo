package main

// Objects as files.
//
// One JSON file describes an object to two programs: the eLSL simulator
// runs it with no grid at all, and these commands build the same thing
// in Second Life.  So a probe written against one is a probe against
// the other, and the difference between what the simulator does and
// what the grid does is a diff of two files rather than an argument.
//
//	dump   what is there, as JSON
//	rez    build what a file describes
//	reform make what is there match a file
//
// "edit" was the obvious name for the third and is not used, because a
// shell with an "edit" that is not an editor is a shell that will be
// asked why it did not open one.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var objectFileCommands = map[string]*command{
	"dump": {
		params: "NAME|UUID",
		flags:  func() any { return new(dumpFlags) },
		brief:  "describe an object as the simulator's JSON",
		man:    "dump",
		run:    cmdDump,
	},
	"rez": {
		params: "FILE",
		flags:  func() any { return new(rezFlags) },
		brief:  "build the object a JSON file describes",
		man:    "rez",
		run:    cmdRez,
	},
	"touch": {
		params: "NAME|UUID [POINT ...]",
		flags:  func() any { return new(touchFlags) },
		brief:  "click an object, on a named face at a named point",
		man:    "touch",
		run:    cmdTouch,
	},
	"texture": {
		params: "NAME|UUID",
		flags:  func() any { return new(textureFlags) },
		brief:  "set what a face looks like, or with no flags say what it looks like",
		man:    "texture",
		run:    cmdTexture,
	},
	"reform": {
		params: "NAME|UUID FILE",
		flags:  func() any { return new(reformFlags) },
		brief:  "change an object to match a JSON file; what it omits is left alone",
		man:    "reform",
		run:    cmdReform,
	},
}

type dumpFlags struct {
	Out  string `getopt:"--out -o=FILE  write here, rather than to the terminal"`
	Wait int    `getopt:"--wait -w=SECONDS  how long to let the region describe itself [30]"`
	Help bool   `getopt:"--help -h      show what this command takes"`
}

func cmdDump(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o dumpFlags
	args, done, err := subOptions("dump", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 1 {
		return usageError("dump")
	}

	target, err := sh.objectNamed(ctx, args[0], o.Wait)
	if err != nil {
		return err
	}
	oj, err := sh.s.Describe(ctx, target, waitFor(o.Wait))
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(oj, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	if o.Out == "" {
		_, err = out.Write(b)
		return err
	}
	if err := os.WriteFile(o.Out, b, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %d prims\n", o.Out, len(oj.Prims))
	return nil
}

type rezFlags struct {
	At   string `getopt:"--at=X,Y,Z     where to put it, rather than where the file says"`
	Keep bool   `getopt:"--keep -k      leave it standing even if a script will not compile"`
	Help bool   `getopt:"--help -h      show what this command takes"`
}

func cmdRez(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o rezFlags
	args, done, err := subOptions("rez", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 1 {
		return usageError("rez")
	}

	objs, err := readObjects(args[0])
	if err != nil {
		return err
	}
	if len(objs) != 1 {
		return fmt.Errorf("%s describes %d objects; rez builds one at a time", args[0], len(objs))
	}
	oj := objs[0]

	// Somewhere to put it.  A file written for the simulator names
	// coordinates in a region that is not this one, so --at moves the
	// whole thing without editing the file.
	if o.At != "" {
		at, err := parseVector(o.At)
		if err != nil {
			return err
		}
		moveTo(&oj, at)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	built, err := sh.s.Create(ctx, oj)
	if err != nil {
		if built != nil && !o.Keep {
			// Half a build is worse than none: it leaves prims
			// standing that nobody asked for and nothing owns.
			for _, p := range built.Parts {
				_ = sh.s.Delete(context.WithoutCancel(ctx), p, msg.UUID{})
			}
			return fmt.Errorf("%w (what was built has been removed; -k keeps it)", err)
		}
		return err
	}
	fmt.Fprintf(out, "%s: %d prims, %s\n", built.Root.Name, len(built.Parts), built.Root.ID)
	return nil
}

type reformFlags struct {
	Wait int  `getopt:"--wait -w=SECONDS  how long to let the region describe itself [30]"`
	Help bool `getopt:"--help -h          show what this command takes"`
}

func cmdReform(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o reformFlags
	args, done, err := subOptions("reform", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 2 {
		return usageError("reform")
	}

	objs, err := readObjects(args[1])
	if err != nil {
		return err
	}
	if len(objs) != 1 {
		return fmt.Errorf("%s describes %d objects; reform takes one", args[1], len(objs))
	}

	target, err := sh.objectNamed(ctx, args[0], o.Wait)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := sh.s.Apply(ctx, target, objs[0], waitFor(o.Wait)); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %d prims changed\n", target.Name, len(objs[0].Prims))
	return nil
}

// -- shared ------------------------------------------------------------

func readObjects(path string) ([]sl.ObjectJSON, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	objs, err := sl.ParseObjects(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(objs) == 0 {
		return nil, fmt.Errorf("%s describes no objects", path)
	}
	return objs, nil
}

// objectNamed resolves a uuid or a name to something in the region.
func (sh *Shell) objectNamed(ctx context.Context, what string, wait int) (*sl.Object, error) {
	if id, err := msg.ParseUUID(what); err == nil {
		seen, err := sh.s.ObjectByID(ctx, id, waitFor(wait))
		if err != nil {
			return nil, err
		}
		return &seen.Object, nil
	}
	found, err := sh.s.ObjectsNamed(ctx, what, waitFor(wait))
	if err != nil {
		return nil, err
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("nothing called %q is in range", what)
	case 1:
		return &found[0].Object, nil
	}
	// Several: say so rather than picking, since editing the wrong one
	// is not something that can be taken back.
	var b []byte
	for _, f := range found {
		b = append(b, "\n  "...)
		b = append(b, f.ID.String()...)
	}
	return nil, fmt.Errorf("%d objects are called %q; name one by uuid:%s", len(found), what, b)
}

func waitFor(seconds int) time.Duration {
	if seconds <= 0 {
		return 30 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

// moveTo puts a described object at a position, keeping the prims'
// offsets from each other.
//
// The root is what lands on the given point; every other prim keeps the
// vector from the root it had in the file, which is what makes moving a
// linkset a different thing from putting every prim in one place.
func moveTo(o *sl.ObjectJSON, at msg.Vector3) {
	if len(o.Prims) == 0 {
		return
	}
	root := o.Prims[0].Pos
	for i := range o.Prims {
		p := &o.Prims[i]
		if i == 0 || len(p.Pos) < 3 || len(root) < 3 {
			p.Pos = []float32{at.X, at.Y, at.Z}
			continue
		}
		p.Pos = []float32{
			at.X + p.Pos[0] - root[0],
			at.Y + p.Pos[1] - root[1],
			at.Z + p.Pos[2] - root[2],
		}
	}
}

// parseVector reads "X,Y,Z", which is what --at takes.
func parseVector(s string) (msg.Vector3, error) {
	var v msg.Vector3
	n, err := fmt.Sscanf(s, "%f,%f,%f", &v.X, &v.Y, &v.Z)
	if err != nil || n != 3 {
		return v, fmt.Errorf("--at wants X,Y,Z, not %q", s)
	}
	return v, nil
}

// ------------------------------------------------------------- touching

type touchFlags struct {
	Face  int    `getopt:"--face -f=N        which face, counting as LSL does; -1 for none"`
	UV    string `getopt:"--uv=U,V           where on the face, each 0 to 1 [0.5,0.5]"`
	ST    string `getopt:"--st=S,T           the same point in the texture's coordinates"`
	At    string `getopt:"--at=X,Y,Z         the point touched, in region coordinates"`
	Norm  string `getopt:"--normal=X,Y,Z     the surface direction there [0,0,1]"`
	Press string `getopt:"--press=SECONDS    hold still at the first point before moving"`
	Move  string `getopt:"--move=SECONDS     spend this long travelling through the points"`
	Dwell string `getopt:"--dwell -H=SECONDS rest at the last point before letting go"`
	Rate  int    `getopt:"--rate=N          updates a second while moving [45]"`
	Help  bool   `getopt:"--help -h          show what this command takes"`
}

// parseSeconds reads a duration written as a number of seconds, which
// is how a person types one at a prompt.
func parseSeconds(flag, s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	var secs float64
	if _, err := fmt.Sscanf(s, "%g", &secs); err != nil || secs < 0 {
		return 0, fmt.Errorf("%s wants a number of seconds, not %q", flag, s)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// parseTouchPoint reads one point of a drag, on top of whatever the
// flags already said.
//
// Two numbers are a place on a face and three are a place in the
// region, since that is what tells them apart without a second flag.
// A leading "N:" names the face, so a drag can cross from one to
// another.
func parseTouchPoint(s string, base sl.Touch) (sl.Touch, error) {
	t := base
	if i := strings.Index(s, ":"); i >= 0 {
		var face int
		if _, err := fmt.Sscanf(s[:i], "%d", &face); err != nil {
			return t, fmt.Errorf("%q does not start with a face number", s)
		}
		t.Face, s = face, s[i+1:]
	}
	var x, y, z float32
	if n, err := fmt.Sscanf(s, "%f,%f,%f", &x, &y, &z); err == nil && n == 3 {
		t.Position = msg.Vector3{X: x, Y: y, Z: z}
		return t, nil
	}
	if n, err := fmt.Sscanf(s, "%f,%f", &x, &y); err == nil && n == 2 {
		t.UV = msg.Vector3{X: x, Y: y}
		return t, nil
	}
	return t, fmt.Errorf("a point is U,V or X,Y,Z, optionally after a face and a colon, not %q", s)
}

func cmdTouch(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o touchFlags
	args, done, err := subOptions("touch", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return usageError("touch")
	}

	seen, err := sh.seenNamed(ctx, args[0], 30)
	if err != nil {
		return err
	}
	t := sl.Touch{Face: o.Face, Position: seen.Position}
	for _, c := range []struct {
		text string
		to   *msg.Vector3
	}{{o.UV, &t.UV}, {o.ST, &t.ST}, {o.At, &t.Position}, {o.Norm, &t.Normal}} {
		if c.text == "" {
			continue
		}
		v, err := parsePoint(c.text)
		if err != nil {
			return err
		}
		*c.to = v
	}

	// Where it goes after the first point.  Each one is read on top of
	// the last, so a drag across one face names the face once.
	points := []sl.Touch{t}
	for _, a := range args[1:] {
		p, err := parseTouchPoint(a, points[len(points)-1])
		if err != nil {
			return err
		}
		points = append(points, p)
	}

	d := sl.Drag{Points: points, Rate: o.Rate}
	for _, c := range []struct {
		flag, text string
		to         *time.Duration
	}{{"--press", o.Press, &d.Press}, {"--move", o.Move, &d.Move}, {"--dwell", o.Dwell, &d.Dwell}} {
		v, err := parseSeconds(c.flag, c.text)
		if err != nil {
			return err
		}
		*c.to = v
	}
	// A path with no time to cross it is a jump, which is not what
	// anybody means by a drag.
	if len(points) > 1 && d.Move == 0 {
		return fmt.Errorf("%d points but no --move: say how long the drag takes", len(points))
	}

	target := &seen.Object
	total := d.Press + d.Move + d.Dwell
	if total == 0 && len(points) == 1 {
		if err := sh.s.Touch(ctx, target, t); err != nil {
			return err
		}
		fmt.Fprintf(out, "touched %s, face %d\n", target.Name, t.Face)
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, total+time.Minute)
	defer cancel()
	if err := sh.s.Drag(ctx, target, d); err != nil {
		return err
	}
	fmt.Fprintf(out, "touched %s for %s over %d point(s); the script saw about %.0f touch events\n",
		target.Name, total, len(points), total.Seconds()*sl.TouchEventRate)
	return nil
}

// seenNamed is objectNamed, keeping what the region said about the
// object -- a touch needs its position, and looking it up twice is a
// second sweep of everything in range.
func (sh *Shell) seenNamed(ctx context.Context, what string, wait int) (*sl.Seen, error) {
	if id, err := msg.ParseUUID(what); err == nil {
		return sh.s.ObjectByID(ctx, id, waitFor(wait))
	}
	found, err := sh.s.ObjectsNamed(ctx, what, waitFor(wait))
	if err != nil {
		return nil, err
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("nothing called %q is in range", what)
	case 1:
		return found[0], nil
	}
	var b []byte
	for _, f := range found {
		b = append(b, "\n  "...)
		b = append(b, f.ID.String()...)
	}
	return nil, fmt.Errorf("%d objects are called %q; name one by uuid:%s", len(found), what, b)
}

// parsePoint reads "X,Y" or "X,Y,Z", since a uv coordinate has two and
// a position has three and both are written the same way.
func parsePoint(s string) (msg.Vector3, error) {
	var v msg.Vector3
	if n, err := fmt.Sscanf(s, "%f,%f,%f", &v.X, &v.Y, &v.Z); err == nil && n == 3 {
		return v, nil
	}
	v = msg.Vector3{}
	if n, err := fmt.Sscanf(s, "%f,%f", &v.X, &v.Y); err == nil && n == 2 {
		return v, nil
	}
	return v, fmt.Errorf("wanted X,Y or X,Y,Z, not %q", s)
}

// ------------------------------------------------------------ texturing

type textureFlags struct {
	Face    int    `getopt:"--face -f=N        which face; every face by default"`
	ID      string `getopt:"--id=UUID          the texture to put on it"`
	Repeats string `getopt:"--repeats=S,T      how many times it tiles"`
	Offset  string `getopt:"--offset=S,T       how far it slides, each -1 to 1"`
	Rot     string `getopt:"--rot=DEGREES      how far it turns"`
	Colour  string `getopt:"--color=R,G,B      the tint, each 0 to 255"`
	Alpha   int    `getopt:"--alpha=N          how opaque, 0 clear to 255 solid"`
	Bright  bool   `getopt:"--fullbright       ignore lighting"`
	Dark    bool   `getopt:"--no-fullbright    stop ignoring it"`
	Shiny   int    `getopt:"--shiny=N          shininess, 0 none to 3 high"`
	Glow    int    `getopt:"--glow=N           glow, 0 to 255"`
	Help    bool   `getopt:"--help -h          show what this command takes"`
}

func cmdTexture(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	o := textureFlags{Face: sl.AllFaces, Alpha: -1, Shiny: -1, Glow: -1}
	args, done, err := subOptions("texture", &o, out, args)
	if err != nil || done {
		if done {
			fmt.Fprint(out, "\nGiven none of these, it says what the faces look like now instead\n"+
				"of changing them; -f on its own reports that one face.\n")
		}
		return err
	}
	if len(args) != 1 {
		return usageError("texture")
	}
	if o.Bright && o.Dark {
		return fmt.Errorf("--fullbright and --no-fullbright are opposites")
	}

	// Asked to change nothing, say what is there instead.  A command
	// that took no flags and reported success without having done
	// anything would be worse than useless.
	if o.changesNothing() {
		return sh.showFaces(ctx, out, args[0], o.Face)
	}

	// Everything is parsed before anything is sent, so a typo in the
	// last flag does not leave the object half changed.
	var id msg.UUID
	if o.ID != "" {
		if id, err = msg.ParseUUID(o.ID); err != nil {
			return fmt.Errorf("--id wants a uuid, not %q", o.ID)
		}
	}
	repeats, err := optionalPair("--repeats", o.Repeats)
	if err != nil {
		return err
	}
	offset, err := optionalPair("--offset", o.Offset)
	if err != nil {
		return err
	}
	colour, err := optionalPair("--color", o.Colour)
	if err != nil {
		return err
	}
	var rot *float64
	if o.Rot != "" {
		var deg float64
		if _, err := fmt.Sscanf(o.Rot, "%g", &deg); err != nil {
			return fmt.Errorf("--rot wants degrees, not %q", o.Rot)
		}
		rot = &deg
	}
	var blue float64
	if colour != nil {
		if _, err := fmt.Sscanf(o.Colour, "%g,%g,%g", &colour[0], &colour[1], &blue); err != nil {
			return fmt.Errorf("--color wants R,G,B, not %q", o.Colour)
		}
	}

	target, err := sh.objectNamed(ctx, args[0], 30)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	err = sh.s.SetFace(ctx, target, o.Face, func(f *sl.Face) {
		if o.ID != "" {
			f.Texture = id
		}
		if repeats != nil {
			f.SetRepeats(float32(repeats[0]), float32(repeats[1]))
		}
		if offset != nil {
			f.SetOffsets(float32(offset[0]), float32(offset[1]))
		}
		if rot != nil {
			f.SetRotationRad(float32(*rot * math.Pi / 180))
		}
		if colour != nil {
			f.SetColour(uint8(colour[0]), uint8(colour[1]), uint8(blue))
		}
		if o.Alpha >= 0 {
			f.SetAlpha(uint8(o.Alpha))
		}
		if o.Bright {
			f.SetFullbright(true)
		}
		if o.Dark {
			f.SetFullbright(false)
		}
		if o.Shiny >= 0 {
			f.SetShiny(uint8(o.Shiny))
		}
		if o.Glow >= 0 {
			f.Glow = uint8(o.Glow)
		}
	})
	if err != nil {
		return err
	}
	which := fmt.Sprintf("face %d", o.Face)
	if o.Face == sl.AllFaces {
		which = "every face"
	}
	fmt.Fprintf(out, "%s: %s\n", target.Name, which)
	return nil
}

// changesNothing reports whether the command was given no change to
// make, which is how it is asked to report instead.
func (o textureFlags) changesNothing() bool {
	return o.ID == "" && o.Repeats == "" && o.Offset == "" && o.Rot == "" &&
		o.Colour == "" && o.Alpha < 0 && o.Shiny < 0 && o.Glow < 0 &&
		!o.Bright && !o.Dark
}

// showFaces prints what an object's faces look like.
//
// Faces that look the same are printed once, under all of their
// numbers, because that is how a prim usually is -- five sides of a box
// alike and one different -- and six identical lines say less than one.
func (sh *Shell) showFaces(ctx context.Context, out io.Writer, name string, only int) error {
	target, err := sh.objectNamed(ctx, name, 30)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	faces, err := sh.s.Faces(ctx, target)
	if err != nil {
		return err
	}
	first := 0
	if only != sl.AllFaces {
		if only < 0 || only >= len(faces) {
			return fmt.Errorf("%s has %d faces, so there is no face %d", target.Name, len(faces), only)
		}
		faces, first = faces[only:only+1], only
	}

	// A name is only known if something asked for it, and a cache
	// filled a minute ago has not.  The id is a poor label but it is
	// the object's; a blank one names nothing at all.
	label := target.Name
	if label == "" {
		label = target.ID.String()
	}
	fmt.Fprintf(out, "%s, %d %s\n", label, len(faces), pluralFaces(len(faces)))
	var (
		labels  []string
		details []string
	)
	printed := make([]bool, len(faces))
	for i, f := range faces {
		if printed[i] {
			continue
		}
		// Gather the faces that look alike.  They need not be next to
		// each other, so every later match is collected here.
		d := describeFace(f)
		nums := []int{i + first}
		for j := i + 1; j < len(faces); j++ {
			if !printed[j] && describeFace(faces[j]) == d {
				nums = append(nums, j+first)
				printed[j] = true
			}
		}
		labels = append(labels, faceLabel(nums))
		details = append(details, d)
	}
	width := 0
	for _, l := range labels {
		if len(l) > width {
			width = len(l)
		}
	}
	for i, l := range labels {
		fmt.Fprintf(out, "  %-*s  %s\n", width, l, details[i])
	}
	return nil
}

// pluralFaces is the word to follow the count with.
func pluralFaces(n int) string {
	if n == 1 {
		return "face"
	}
	return "faces"
}

// faceLabel names a run of faces: "face 2", or "faces 0-1,3-5".
func faceLabel(nums []int) string {
	var runs []string
	for i := 0; i < len(nums); {
		j := i
		for j+1 < len(nums) && nums[j+1] == nums[j]+1 {
			j++
		}
		switch {
		case j == i:
			runs = append(runs, strconv.Itoa(nums[i]))
		default:
			runs = append(runs, fmt.Sprintf("%d-%d", nums[i], nums[j]))
		}
		i = j + 1
	}
	if len(nums) == 1 {
		return "face " + runs[0]
	}
	return "faces " + strings.Join(runs, ",")
}

// describeFace is one face in one line.  Everything a face always has
// is always printed; the rest only when it is not the plain value, so
// that what has been done to a prim stands out from what has not.
func describeFace(f sl.Face) string {
	texture := "none"
	if f.Texture != (msg.UUID{}) {
		texture = f.Texture.String()
	}
	parts := []string{
		"texture " + texture,
		fmt.Sprintf("colour %d,%d,%d", f.Colour[0], f.Colour[1], f.Colour[2]),
		fmt.Sprintf("alpha %d", f.Colour[3]),
		fmt.Sprintf("repeats %g,%g", f.ScaleS, f.ScaleT),
	}
	if s, t := f.OffsetsF(); s != 0 || t != 0 {
		parts = append(parts, fmt.Sprintf("offset %.4g,%.4g", s, t))
	}
	if f.Rotation != 0 {
		parts = append(parts, fmt.Sprintf("rot %.4g", float64(f.RotationRad())*180/math.Pi))
	}
	if f.Fullbright() {
		parts = append(parts, "fullbright")
	}
	if s := f.Shiny(); s != 0 {
		parts = append(parts, "shiny "+[...]string{"none", "low", "medium", "high"}[s])
	}
	if b := f.Bumpiness(); b != 0 {
		parts = append(parts, fmt.Sprintf("bump %d", b))
	}
	if f.Glow != 0 {
		parts = append(parts, fmt.Sprintf("glow %d", f.Glow))
	}
	return strings.Join(parts, "  ")
}

// optionalPair reads "A,B", or nil when the flag was not given.
func optionalPair(flag, s string) ([]float64, error) {
	if s == "" {
		return nil, nil
	}
	out := make([]float64, 2)
	if _, err := fmt.Sscanf(s, "%g,%g", &out[0], &out[1]); err != nil {
		return nil, fmt.Errorf("%s wants two numbers separated by a comma, not %q", flag, s)
	}
	return out, nil
}
