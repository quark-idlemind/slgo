package sl

// Describing an object as JSON, and making one from it.
//
// These are the three verbs a person wants: say what is there, build
// what a file describes, and make what is there match a file.  The
// format is the eLSL simulator's, so the same file works in world and
// in the simulator, which is the whole point of using its format
// rather than inventing one.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// taskTypeNames turn the words an object's contents file uses into the
// words the simulator's JSON uses.  They agree about most of it.
var taskTypeNames = map[string]string{
	"texture": "texture", "sound": "sound", "landmark": "landmark",
	"clothing": "clothing", "object": "object", "notecard": "notecard",
	"lsltext": "script", "lsl": "script", "script": "script",
	"bodypart": "bodypart", "animation": "animation", "animatn": "animation",
	"gesture": "gesture", "settings": "setting", "material": "material",
}

// Describe writes an object out in the simulator's JSON.
//
// It describes the whole linkset, root first, which is what the format
// means by an object -- so naming any prim of one describes all of it.
// Properties are asked for per prim, because description, creator and
// the permission masks are on no update and arrive only when asked.
//
// Every prim comes out with a shape, because the format gives no way
// to leave one unsaid.  Two kinds of prim have none to give: one that
// nothing has described yet, and a mesh or a sculpt, whose profile and
// path name no shape this package knows.  Both are written as a box.
// That is what it costs a reader of the output -- a mesh described
// here rebuilds as a box, and nothing in the file says it was ever
// anything else.
func (w *Session) Describe(ctx context.Context, o *Object, timeout time.Duration) (*ObjectJSON, error) {
	all, err := w.AllObjects(ctx, timeout)
	if err != nil {
		return nil, err
	}

	parts, err := linkset(all, o)
	if err != nil {
		return nil, err
	}

	// A child prim's update describes it in the ROOT's frame: its
	// position is an offset from the root and its rotation is relative
	// to the root's.  The file says where things are in the region,
	// because that is what Build takes and what somebody reading the
	// file expects, so the root's frame is composed back out here.
	rootPos, rootRot := parts[0].Position, parts[0].Rotation

	out := &ObjectJSON{}
	for i, s := range parts {
		obj := &Object{ID: s.ID, Local: s.Local, Name: s.Name, from: s.from}
		props, err := w.Properties(ctx, obj, 20*time.Second)
		if err != nil {
			// A prim that will not answer is still worth describing
			// with what arrived on its update.
			props = nil
		}
		at, rot := s.Position, s.Rotation
		if i > 0 {
			at = add3(rootPos, rootRot.Rotate(at))
			rot = rootRot.Mul(rot)
		}
		p := describePrim(s, props)
		p.Pos, p.Rot = floats(at), floats4(rot)
		if items, err := w.TaskInventory(ctx, obj); err == nil {
			for _, it := range items {
				p.Inventory = append(p.Inventory, InventoryItemJSON{
					Name: it.Name,
					Type: jsonTypeOf(it.Type),
					UUID: it.ID.String(),
				})
			}
		}
		if i == 0 {
			out.Name = p.Name
		}
		out.Prims = append(out.Prims, p)
	}
	return out, nil
}

// jsonTypeOf translates the word an object's contents file uses.  An
// unknown one is passed through: the simulator will refuse it by name,
// which is a better complaint than one about a type nobody wrote.
func jsonTypeOf(word string) string {
	if s, ok := taskTypeNames[strings.ToLower(word)]; ok {
		return s
	}
	if word == "" {
		return "object"
	}
	return word
}

// Create builds what a description describes.
//
// The prims are rezzed and linked, named and described, and each one's
// inventory scripts are installed.  What it does not do is take the
// object into inventory: an object being built is usually about to be
// looked at.
//
// A failure part way returns what was made with the error, as Build
// does, so the caller can take it away.
func (w *Session) Create(ctx context.Context, o ObjectJSON) (*Built, error) {
	prims, err := o.BuildList()
	if err != nil {
		return nil, err
	}
	built, err := w.Build(ctx, prims)
	if err != nil {
		return built, err
	}

	// Names and descriptions are set after the link, since linking is
	// what decides which prim is the root and the root's name is the
	// object's.
	for i, part := range built.Parts {
		pj := o.Prims[i]
		name := pj.Name
		if i == 0 && o.Name != "" {
			name = o.Name
		}
		if name != "" {
			if err := w.SetName(ctx, part, name); err != nil {
				return built, fmt.Errorf("sl: naming prim %d: %w", i, err)
			}
		}
		if pj.Desc != "" {
			if err := w.SetDescription(ctx, part, pj.Desc); err != nil {
				return built, fmt.Errorf("sl: describing prim %d: %w", i, err)
			}
		}
		if err := w.fillPrim(ctx, part, pj); err != nil {
			return built, fmt.Errorf("sl: prim %d: %w", i, err)
		}
	}
	return built, nil
}

// fillPrim puts a described prim's inventory into a real one.
//
// Only scripts and notecards can be made from a description: the rest
// name an asset this avatar may not have, and a texture named by uuid
// is not something a client can conjure into an object it does not
// already own a copy of.
func (w *Session) fillPrim(ctx context.Context, o *Object, pj PrimJSON) error {
	for _, it := range pj.Inventory {
		switch it.Type {
		case "script":
			src, err := scriptSource(it)
			if err != nil {
				return err
			}
			if src == "" {
				continue
			}
			res, err := w.InstallScript(ctx, o, it.Name, src, !it.Disabled)
			if err != nil {
				return fmt.Errorf("installing %s: %w", it.Name, err)
			}
			if !res.Compiled && len(res.Errors) > 0 {
				return fmt.Errorf("%s does not compile: %s", it.Name, strings.Join(res.Errors, "; "))
			}
		case "notecard", "":
			// Nothing to do without content; a notecard named and
			// empty is not worth a round trip.
		}
	}
	return nil
}

// scriptSource reads an inventory item's script: a path, or the source
// itself.  The rule for telling them apart is the simulator's, so that
// one file means the same thing to both.
func scriptSource(it InventoryItemJSON) (string, error) {
	d := it.Data
	if len(d) >= 2 && (d[0] == '.' || (d[0] == '/' && d[1] != '/')) {
		b, err := os.ReadFile(d)
		if err != nil {
			return "", fmt.Errorf("%s: %w", it.Name, err)
		}
		return string(b), nil
	}
	return d, nil
}

// Apply makes an object that already exists match a description.
//
// Prims are matched by position in the list: the first described prim
// is the root, the second is the first child, and so on.  A description
// with fewer prims than the object leaves the rest alone; one with more
// is an error rather than a partial build, since the extra prims would
// have to be rezzed and linked and that is what Create is for.
//
// Every field the description omits is left as it is.  That is what
// makes a two-line file a useful edit rather than a demolition.
func (w *Session) Apply(ctx context.Context, o *Object, oj ObjectJSON, timeout time.Duration) error {
	all, err := w.AllObjects(ctx, timeout)
	if err != nil {
		return err
	}
	parts, err := linkset(all, o)
	if err != nil {
		return err
	}
	if len(oj.Prims) > len(parts) {
		return fmt.Errorf("sl: the description has %d prims and %s has %d; "+
			"editing cannot add prims", len(oj.Prims), o, len(parts))
	}

	for i, pj := range oj.Prims {
		part := &Object{ID: parts[i].ID, Local: parts[i].Local, Name: parts[i].Name, from: parts[i].from}
		name := pj.Name
		if i == 0 && oj.Name != "" {
			name = oj.Name
		}
		if name != "" {
			if err := w.SetName(ctx, part, name); err != nil {
				return fmt.Errorf("sl: naming prim %d: %w", i, err)
			}
		}
		if pj.Desc != "" {
			if err := w.SetDescription(ctx, part, pj.Desc); err != nil {
				return fmt.Errorf("sl: describing prim %d: %w", i, err)
			}
		}

		// Position, rotation and scale go in one message, so anything
		// the description leaves out has to be filled in from what the
		// prim already is rather than left zero.
		if len(pj.Pos) >= 3 || len(pj.Rot) >= 4 || len(pj.Size) >= 3 {
			at, rot, size := parts[i].Position, parts[i].Rotation, parts[i].Scale
			if len(pj.Pos) >= 3 {
				at = vec3(pj.Pos)
				if i > 0 {
					// The file is in region coordinates and a child
					// is moved in its root's frame, so undo the root.
					rootRot := parts[0].Rotation
					at = rootRot.Conjugate().Rotate(sub3(at, parts[0].Position))
				}
			}
			if len(pj.Rot) >= 4 {
				rot = quat(pj.Rot)
				if i > 0 {
					rot = parts[0].Rotation.Conjugate().Mul(rot)
				}
			}
			if len(pj.Size) >= 3 {
				size = vec3(pj.Size)
			}
			if err := w.Place(ctx, part, at, rot, size); err != nil {
				return fmt.Errorf("sl: placing prim %d: %w", i, err)
			}
		}

		// The shape, when the description says anything about it.  It
		// is all or nothing: the protocol has no message for "change
		// the hollow and leave the rest", so what is not described is
		// taken from what the prim is now.
		if saysShape(pj) {
			s, err := shapeFor(parts[i], pj)
			if err != nil {
				return fmt.Errorf("sl: prim %d: %w", i, err)
			}
			if err := w.SetShape(ctx, part, s); err != nil {
				return fmt.Errorf("sl: reshaping prim %d: %w", i, err)
			}
		}

		if err := w.fillPrim(ctx, part, pj); err != nil {
			return fmt.Errorf("sl: prim %d: %w", i, err)
		}
	}
	return nil
}

// shapeFor is the shape to send when editing: what the prim is now,
// with whatever the description says laid over it.
func shapeFor(s *Seen, pj PrimJSON) (Shape, error) {
	now, known := s.Form()
	if !known || now.Type == "" {
		return pj.Shape()
	}
	if pj.Type == "" {
		// Fill the type in so that the described fields land on the
		// shape the prim already has rather than on a box.
		pj.Type = now.Type
	}
	described, err := pj.Shape()
	if err != nil {
		return described, err
	}
	if pj.Type != now.Type {
		// A different kind of prim entirely: what is left unsaid is a
		// plain one of the new kind, not the old one's numbers.
		return described, nil
	}
	// Same kind, so keep every number the description did not mention.
	plain := DefaultShape()
	plain.Type = now.Type
	out := now
	out.Type = described.Type
	if described.HoleShape != plain.HoleShape {
		out.HoleShape = described.HoleShape
	}
	if described.CutBegin != plain.CutBegin || described.CutEnd != plain.CutEnd {
		out.CutBegin, out.CutEnd = described.CutBegin, described.CutEnd
	}
	if described.Hollow != plain.Hollow {
		out.Hollow = described.Hollow
	}
	if described.TwistBegin != plain.TwistBegin || described.TwistEnd != plain.TwistEnd {
		out.TwistBegin, out.TwistEnd = described.TwistBegin, described.TwistEnd
	}
	if described.TopSizeX != plain.TopSizeX || described.TopSizeY != plain.TopSizeY {
		out.TopSizeX, out.TopSizeY = described.TopSizeX, described.TopSizeY
	}
	if described.TopShearX != plain.TopShearX || described.TopShearY != plain.TopShearY {
		out.TopShearX, out.TopShearY = described.TopShearX, described.TopShearY
	}
	if described.AdvancedCutBegin != plain.AdvancedCutBegin || described.AdvancedCutEnd != plain.AdvancedCutEnd {
		out.AdvancedCutBegin, out.AdvancedCutEnd = described.AdvancedCutBegin, described.AdvancedCutEnd
	}
	if described.TaperX != plain.TaperX || described.TaperY != plain.TaperY {
		out.TaperX, out.TaperY = described.TaperX, described.TaperY
	}
	if described.Revolutions != plain.Revolutions {
		out.Revolutions = described.Revolutions
	}
	if described.RadiusOffset != plain.RadiusOffset {
		out.RadiusOffset = described.RadiusOffset
	}
	if described.Skew != plain.Skew {
		out.Skew = described.Skew
	}
	return out, nil
}

// saysShape reports whether a described prim mentions its form at all.
func saysShape(p PrimJSON) bool {
	return p.Type != "" || p.HoleShape != nil || p.Hollow != nil ||
		p.Revolutions != nil || p.RadiusOffset != nil || p.Skew != nil ||
		len(p.Cut) > 0 || len(p.Dimple) > 0 || len(p.Twist) > 0 ||
		len(p.TopSize) > 0 || len(p.TopShear) > 0 ||
		len(p.AdvancedCut) > 0 || len(p.Taper) > 0
}

// linkset finds an object's prims, root first.
//
// An object with an id is found by it and by nothing else: its local id
// may be another region's, and would find a stranger here first.
func linkset(all []*Seen, o *Object) ([]*Seen, error) {
	var root *Seen
	for _, s := range all {
		if s.ID == o.ID || (o.ID.IsZero() && o.Local != 0 && s.Local == o.Local) {
			root = s
			break
		}
	}
	if root == nil {
		return nil, fmt.Errorf("sl: %s is not among the objects in range", o)
	}
	if root.Parent != 0 {
		for _, s := range all {
			if s.Local == root.Parent {
				root = s
				break
			}
		}
	}
	parts := []*Seen{root}
	for _, s := range all {
		if s.Parent == root.Local && s.ID != root.ID {
			parts = append(parts, s)
		}
	}
	return parts, nil
}

func add3(a, b msg.Vector3) msg.Vector3 {
	return msg.Vector3{X: a.X + b.X, Y: a.Y + b.Y, Z: a.Z + b.Z}
}

func sub3(a, b msg.Vector3) msg.Vector3 {
	return msg.Vector3{X: a.X - b.X, Y: a.Y - b.Y, Z: a.Z - b.Z}
}
