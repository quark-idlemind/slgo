package sl

// Objects as JSON, in the format the eLSL simulator reads and writes.
//
// The point is that one file describes an object to both: elslsim can
// run it with no grid at all, and this can build the same thing in
// Second Life, so a probe written for one is a probe for the other.
// The field names and JSON tags are the simulator's, from
// simulator/primjson.go, and are not to be renamed for tidiness --
// they are a wire format shared with another program.
//
// # What was added
//
// The simulator describes a world it invents; this describes one that
// already exists, so a few fields carry what the grid knows and the
// simulator has no use for. They are all optional and the simulator
// ignores what it does not recognise:
//
//	prim.localid    the region handle, which is not stable and is
//	                what most messages take
//	prim.text       floating text
//	prim.perms      the object's permission masks
//	prim.creator    who made it
//	object.name     the linkset's name, which in Second Life belongs
//	                to the root prim and is worth having at the top
//
// # What is deliberately not here
//
// Textures. A prim's faces are a packed TextureEntry that the simulator
// has no notion of, and inventing a representation for it here would be
// inventing it for both programs. Faces reads them for a caller that
// wants them.

import (
	"encoding/json"
	"fmt"

	"github.com/quark-idlemind/slgo/msg"
)

// ObjectJSON is a linkset: its prims, root first.
type ObjectJSON struct {
	// Name is the object's name, which belongs to the root prim.  It
	// is repeated here because that is where somebody looks for it.
	Name string `json:"name,omitempty"`

	Prims []PrimJSON `json:"prims"`

	// LinksetData is the object's persistent datastore.  Nothing here
	// reads or writes it against the grid -- llLinksetData is scripts'
	// business -- and it is carried so that a file round trips.
	LinksetData map[string]LinksetEntryJSON `json:"linksetdata,omitempty"`
}

// PrimJSON is one prim, flat.  Shape fields are omitted when they are
// what a plain prim of that type would have.
type PrimJSON struct {
	Type  string    `json:"type"`
	UUID  string    `json:"uuid,omitempty"`
	Name  string    `json:"name,omitempty"`
	Desc  string    `json:"desc,omitempty"`
	Pos   []float32 `json:"pos,omitempty"`
	Rot   []float32 `json:"rot,omitempty"`
	Size  []float32 `json:"size,omitempty"`
	Owner string    `json:"owner,omitempty"`
	Group string    `json:"group,omitempty"`

	// Box, cylinder and prism.
	HoleShape *int      `json:"holeshape,omitempty"`
	Cut       []float32 `json:"cut,omitempty"`
	Hollow    *float32  `json:"hollow,omitempty"`
	Twist     []float32 `json:"twist,omitempty"`
	TopSize   []float32 `json:"topsize,omitempty"`
	TopShear  []float32 `json:"topshear,omitempty"`

	// Sphere.
	Dimple []float32 `json:"dimple,omitempty"`

	// Torus, tube and ring.
	AdvancedCut  []float32 `json:"advancedcut,omitempty"`
	Taper        []float32 `json:"taper,omitempty"`
	Revolutions  *float32  `json:"revolutions,omitempty"`
	RadiusOffset *float32  `json:"radiusoffset,omitempty"`
	Skew         *float32  `json:"skew,omitempty"`

	// Sculpt.
	SculptMap  string `json:"sculptmap,omitempty"`
	SculptType *int   `json:"sculpttype,omitempty"`

	Inventory []InventoryItemJSON `json:"inventory,omitempty"`

	// Added here; see the file comment.
	LocalID uint32     `json:"localid,omitempty"`
	Text    string     `json:"text,omitempty"`
	Creator string     `json:"creator,omitempty"`
	Perms   *PermsJSON `json:"perms,omitempty"`
}

// PermsJSON is a prim's permission masks, as numbers.  The simulator
// counts them per inventory item; an object in Second Life has its own.
//
// The name carries the JSON suffix because sl.Perms is already the
// bitmask a script asks an avatar for, and the two are unrelated.
type PermsJSON struct {
	Base     uint32 `json:"base"`
	Owner    uint32 `json:"owner"`
	Group    uint32 `json:"group"`
	Everyone uint32 `json:"everyone"`
	Next     uint32 `json:"next"`
}

// InventoryItemJSON is one thing inside a prim.
//
// Data is the simulator's way of carrying a script: a path beginning
// with "/" or ".", or the source itself.  It is what makes a file
// describing an object describe what the object DOES as well.
type InventoryItemJSON struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	UUID     string `json:"uuid,omitempty"`
	Desc     string `json:"desc,omitempty"`
	Creator  string `json:"creator,omitempty"`
	Time     string `json:"time,omitempty"`
	Data     string `json:"data,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Perms    []int  `json:"perms,omitempty"`
}

// LinksetEntryJSON is one entry of the linkset datastore.
type LinksetEntryJSON struct {
	Value string `json:"value"`
	Pass  string `json:"pass,omitempty"`
}

// WorldJSON is the simulator's top-level file: objects, and the avatars
// it invents.  Only the objects mean anything here.
type WorldJSON struct {
	Objects []ObjectJSON `json:"objects"`
	Agents  []any        `json:"agents,omitempty"`
}

// ParseObjects reads either form of the simulator's file: a bare array
// of objects, which is what it wrote first, or the {objects, agents}
// envelope it writes now.  A single object on its own is accepted too,
// since that is what Describe produces and what somebody hand-writing
// one file for one object would type.
func ParseObjects(b []byte) ([]ObjectJSON, error) {
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case '[':
			var out []ObjectJSON
			if err := json.Unmarshal(b, &out); err != nil {
				return nil, fmt.Errorf("sl: reading a list of objects: %w", err)
			}
			return out, nil
		}
		break
	}

	// An envelope and a bare object are told apart by which key is
	// there, since both are JSON objects.
	var probe struct {
		Objects json.RawMessage `json:"objects"`
		Prims   json.RawMessage `json:"prims"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return nil, fmt.Errorf("sl: reading an object: %w", err)
	}
	switch {
	case probe.Objects != nil:
		var wj WorldJSON
		if err := json.Unmarshal(b, &wj); err != nil {
			return nil, fmt.Errorf("sl: reading a world: %w", err)
		}
		return wj.Objects, nil
	case probe.Prims != nil:
		var oj ObjectJSON
		if err := json.Unmarshal(b, &oj); err != nil {
			return nil, fmt.Errorf("sl: reading an object: %w", err)
		}
		return []ObjectJSON{oj}, nil
	}
	return nil, fmt.Errorf("sl: this is neither an object (no \"prims\") nor a world (no \"objects\")")
}

// -- from JSON to something to build ------------------------------------

// BuildList turns a described object into the prims Build takes.
//
// Positions are absolute, as the simulator's are and as Build wants
// them: a prim with no pos of its own is put at the root's, which is
// the only reading that does not require inventing a coordinate frame
// the format does not have.
func (o ObjectJSON) BuildList() ([]Prim, error) {
	if len(o.Prims) == 0 {
		return nil, fmt.Errorf("sl: this object has no prims")
	}
	out := make([]Prim, len(o.Prims))
	var rootPos msg.Vector3
	for i, pj := range o.Prims {
		s, err := pj.Shape()
		if err != nil {
			return nil, fmt.Errorf("sl: prim %d: %w", i, err)
		}
		p := Prim{
			Name:        pj.Name,
			Description: pj.Desc,
			Position:    vec3(pj.Pos),
			Size:        vec3(pj.Size),
			Rotation:    quat(pj.Rot),
			Shape:       s,
		}
		if i == 0 {
			rootPos = p.Position
			if o.Name != "" && p.Name == "" {
				p.Name = o.Name
			}
		} else if len(pj.Pos) < 3 {
			p.Position = rootPos
		}
		out[i] = p
	}
	return out, nil
}

// Shape reads the form out of a described prim.
//
// Everything omitted takes the value a plain prim of that type has, so
// {"type":"sphere"} is a sphere and not a sphere with every dimension
// cut to nothing.
func (p PrimJSON) Shape() (Shape, error) {
	s := DefaultShape()
	if p.Type != "" {
		s.Type = p.Type
	}
	if _, ok := curves[s.Type]; !ok {
		return s, fmt.Errorf("no prim shape called %q; there is %v", s.Type, ShapeNames)
	}
	if p.HoleShape != nil {
		s.HoleShape = *p.HoleShape
	}
	// A sphere's cut is called a dimple, which is the simulator's
	// spelling as well as LSL's.
	cut := p.Cut
	if s.Type == "sphere" && len(p.Dimple) >= 2 {
		cut = p.Dimple
	}
	if len(cut) >= 2 {
		s.CutBegin, s.CutEnd = cut[0], cut[1]
	}
	if p.Hollow != nil {
		s.Hollow = *p.Hollow
	}
	if len(p.Twist) >= 2 {
		s.TwistBegin, s.TwistEnd = p.Twist[0], p.Twist[1]
	}
	if len(p.TopSize) >= 2 {
		s.TopSizeX, s.TopSizeY = p.TopSize[0], p.TopSize[1]
	}
	if len(p.TopShear) >= 2 {
		s.TopShearX, s.TopShearY = p.TopShear[0], p.TopShear[1]
	}
	if len(p.AdvancedCut) >= 2 {
		s.AdvancedCutBegin, s.AdvancedCutEnd = p.AdvancedCut[0], p.AdvancedCut[1]
	}
	if len(p.Taper) >= 2 {
		s.TaperX, s.TaperY = p.Taper[0], p.Taper[1]
	}
	if p.Revolutions != nil {
		s.Revolutions = *p.Revolutions
	}
	if p.RadiusOffset != nil {
		s.RadiusOffset = *p.RadiusOffset
	}
	if p.Skew != nil {
		s.Skew = *p.Skew
	}
	return s, nil
}

// -- from the grid to JSON ----------------------------------------------

// describePrim writes one sighting out.
//
// Shape fields are compared against a plain prim of the same type and
// omitted when they match, so an ordinary box describes as three lines
// rather than twenty.
func describePrim(s *Seen, props *Properties) PrimJSON {
	form, known := s.Form()
	if !known {
		form = DefaultShape()
	}
	plain := DefaultShape()
	plain.Type = form.Type

	p := PrimJSON{
		Type:    form.Type,
		UUID:    s.ID.String(),
		Name:    s.Name,
		LocalID: s.Local,
		Text:    s.Text,
		Pos:     floats(s.Position),
		Size:    floats(s.Scale),
		Rot:     floats4(s.Rotation),
	}
	if p.Type == "" {
		// A mesh or a sculpt: its profile and path are a combination
		// no name here covers.  The field is not optional -- the
		// format always says what a prim is, and something rebuilding
		// from the file has to be given a form to make -- so it says
		// box, which is the same answer an undescribed prim gets from
		// DefaultShape above.  The cost is that the file cannot be
		// told apart from one describing a real box: a mesh written
		// out here comes back as a box.
		p.Type = "box"
	}
	if !s.Owner.IsZero() {
		p.Owner = s.Owner.String()
	}
	if props != nil {
		p.Desc = props.Description
		p.Creator = props.Creator.String()
		if !props.Group.IsZero() {
			p.Group = props.Group.String()
		}
		p.Perms = &PermsJSON{
			Base: props.BaseMask, Owner: props.OwnerMask, Group: props.GroupMask,
			Everyone: props.EveryoneMask, Next: props.NextOwnerMask,
		}
		if p.Name == "" {
			p.Name = props.Name
		}
	}

	if form.HoleShape != plain.HoleShape {
		p.HoleShape = &form.HoleShape
	}
	if form.Hollow != plain.Hollow {
		p.Hollow = &form.Hollow
	}
	cut := []float32{form.CutBegin, form.CutEnd, 0}
	if form.CutBegin != plain.CutBegin || form.CutEnd != plain.CutEnd {
		if form.Type == "sphere" {
			p.Dimple = cut
		} else {
			p.Cut = cut
		}
	}
	if form.TwistBegin != plain.TwistBegin || form.TwistEnd != plain.TwistEnd {
		p.Twist = []float32{form.TwistBegin, form.TwistEnd, 0}
	}
	if form.TopSizeX != plain.TopSizeX || form.TopSizeY != plain.TopSizeY {
		p.TopSize = []float32{form.TopSizeX, form.TopSizeY, 0}
	}
	if form.TopShearX != plain.TopShearX || form.TopShearY != plain.TopShearY {
		p.TopShear = []float32{form.TopShearX, form.TopShearY, 0}
	}
	if form.AdvancedCutBegin != plain.AdvancedCutBegin || form.AdvancedCutEnd != plain.AdvancedCutEnd {
		p.AdvancedCut = []float32{form.AdvancedCutBegin, form.AdvancedCutEnd, 0}
	}
	if form.TaperX != plain.TaperX || form.TaperY != plain.TaperY {
		p.Taper = []float32{form.TaperX, form.TaperY, 0}
	}
	if form.Revolutions != plain.Revolutions {
		p.Revolutions = &form.Revolutions
	}
	if form.RadiusOffset != plain.RadiusOffset {
		p.RadiusOffset = &form.RadiusOffset
	}
	if form.Skew != plain.Skew {
		p.Skew = &form.Skew
	}
	return p
}

// -- small conversions --------------------------------------------------

func vec3(f []float32) msg.Vector3 {
	if len(f) < 3 {
		return msg.Vector3{}
	}
	return msg.Vector3{X: f[0], Y: f[1], Z: f[2]}
}

// quat reads a rotation, which the format writes as four numbers and
// the protocol sends as three.
func quat(f []float32) msg.Quaternion {
	if len(f) < 4 {
		return msg.Quaternion{}
	}
	q := msg.Quaternion{X: f[0], Y: f[1], Z: f[2]}
	if f[3] < 0 {
		// The wire form assumes a non-negative W and recovers it by
		// normalising, so a rotation given with a negative one is the
		// same rotation with every component negated.
		q = msg.Quaternion{X: -f[0], Y: -f[1], Z: -f[2]}
	}
	return q
}

func floats(v msg.Vector3) []float32 { return []float32{v.X, v.Y, v.Z} }

func floats4(q msg.Quaternion) []float32 { return []float32{q.X, q.Y, q.Z, q.W()} }
