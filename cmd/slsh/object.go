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
	"os"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var objectFileCommands = map[string]*command{
	"dump": {
		usage: "dump [-o FILE] NAME|UUID",
		brief: "describe an object as the simulator's JSON",
		run:   cmdDump,
	},
	"rez": {
		usage: "rez [-at X,Y,Z] FILE",
		brief: "build the object a JSON file describes",
		run:   cmdRez,
	},
	"touch": {
		usage: "touch [-f FACE] [--uv U,V] [--at X,Y,Z] [-H SECONDS] NAME|UUID",
		brief: "click an object, on a named face at a named point",
		run:   cmdTouch,
	},
	"reform": {
		usage: "reform NAME|UUID FILE",
		brief: "change an object to match a JSON file; what it omits is left alone",
		run:   cmdReform,
	},
}

type dumpFlags struct {
	Out  string `getopt:"--out -o=FILE  write here, rather than to the terminal"`
	Wait int    `getopt:"--wait -w=SECONDS  how long to let the region describe itself [30]"`
	Help bool   `getopt:"--help -h      show what this command takes"`
}

func cmdDump(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o dumpFlags
	args, done, err := subOptions("dump", "NAME|UUID", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: dump [-o FILE] NAME|UUID")
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
	args, done, err := subOptions("rez", "FILE", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: rez [-at X,Y,Z] FILE")
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
	args, done, err := subOptions("reform", "NAME|UUID FILE", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 2 {
		return fmt.Errorf("usage: reform NAME|UUID FILE")
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
	Face int    `getopt:"--face -f=N        which face, counting as LSL does; -1 for none"`
	UV   string `getopt:"--uv=U,V           where on the face, each 0 to 1 [0.5,0.5]"`
	ST   string `getopt:"--st=S,T           the same point in the texture's coordinates"`
	At   string `getopt:"--at=X,Y,Z         the point touched, in region coordinates"`
	Norm string `getopt:"--normal=X,Y,Z     the surface direction there [0,0,1]"`
	Hold string `getopt:"--hold -H=SECONDS  keep touching for this long, sending touch events"`
	Help bool   `getopt:"--help -h          show what this command takes"`
}

func cmdTouch(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o touchFlags
	args, done, err := subOptions("touch", "NAME|UUID", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: touch [-f FACE] [--uv U,V] [-H SECONDS] NAME|UUID")
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

	target := &seen.Object
	if o.Hold == "" {
		if err := sh.s.Touch(ctx, target, t); err != nil {
			return err
		}
		fmt.Fprintf(out, "touched %s, face %d\n", target.Name, t.Face)
		return nil
	}

	var secs float64
	if _, err := fmt.Sscanf(o.Hold, "%g", &secs); err != nil || secs <= 0 {
		return fmt.Errorf("--hold wants a number of seconds, not %q", o.Hold)
	}
	held := time.Duration(secs * float64(time.Second))
	ctx, cancel := context.WithTimeout(ctx, held+30*time.Second)
	defer cancel()
	if err := sh.s.TouchHold(ctx, target, t, held); err != nil {
		return err
	}
	fmt.Fprintf(out, "touched %s for %s, face %d\n", target.Name, held, t.Face)
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
