package msg

// A face's GLTF material, and the overrides a region puts on it.
//
// A face has a GLTF material when its object's RenderMaterial extra
// parameter names one for it (RenderMaterialsOf).  A script (PRIM_GLTF_*)
// or a viewer's build tool then overrides single fields of that material
// for the face, and the region tells every viewer in range with a
// GenericStreamingMessage whose method is GenericMethodGLTFMaterialOverride
// and whose data is LLSD in notation.  The viewer reads it in
// LLGLTFMaterialList::applyOverrideMessage and each face's map in
// LLGLTFMaterial::applyOverrideLLSD (Firestorm 885631b93a:
// newview/llviewergenericmessage.cpp:97-111, newview/llgltfmateriallist.cpp
// :171-243, llprimitive/llgltfmaterial.cpp:756-838).
// Why: doc/gltf.md

import (
	"fmt"

	"github.com/quark-idlemind/slgo/llsd"
)

// GenericMethodGLTFMaterialOverride is the Method of a
// GenericStreamingMessage that carries material overrides
// (LLGenericStreamingMessage::METHOD_GLTF_MATERIAL_OVERRIDE).
const GenericMethodGLTFMaterialOverride = 0x4175

// MaxGLTFFaces is how many faces of an object an override message is read
// for: the viewer reads no more than MAX_TES, 45, entries of its te array
// (llgltfmateriallist.cpp:196).
const MaxGLTFFaces = 45

// The four texture slots of a GLTF material, in the order the override's
// tex and ti arrays number them (LLGLTFMaterial::TextureInfo,
// llgltfmaterial.h:95-109).  Occlusion shares the metallic-roughness
// slot.
const (
	GLTFSlotBase = iota
	GLTFSlotNormal
	GLTFSlotMetallicRoughness
	GLTFSlotEmissive
	GLTFSlots
)

// GLTFOverrideNullTexture is what an override says for a texture slot it
// sets to no texture at all: the null id would mean the slot is not
// overridden (GLTF_OVERRIDE_NULL_UUID, llgltfmaterial.cpp:48 and
// applyOverrideUUID :596-609).
var GLTFOverrideNullTexture = MustParseUUID("ffffffff-ffff-ffff-ffff-ffffffffffff")

// GLTFAlphaMode is a GLTF material's alpha mode, in the numbers of
// LLGLTFMaterial::AlphaMode (llgltfmaterial.h:82-87).
type GLTFAlphaMode int

const (
	GLTFAlphaOpaque GLTFAlphaMode = iota
	GLTFAlphaBlend
	GLTFAlphaMask
)

func (m GLTFAlphaMode) String() string {
	switch m {
	case GLTFAlphaOpaque:
		return "opaque"
	case GLTFAlphaBlend:
		return "blend"
	case GLTFAlphaMask:
		return "mask"
	}
	return fmt.Sprintf("GLTFAlphaMode(%d)", int(m))
}

// A GLTFTransform is the part of a texture's transform an override sets:
// each field is nil when the override leaves it alone.  The offset and
// scale are two numbers, the rotation is in radians.
type GLTFTransform struct {
	Offset   *[2]float32
	Scale    *[2]float32
	Rotation *float32
}

// A GLTFOverride is what an override changes on one face's GLTF material.
// A field is nil, or for the arrays the pointer in it is, when the
// override says nothing of it, which is not the same as saying zero: a
// metallic factor of zero is an override, and a nil one is not.
//
// Whether a field is set is whether its key is in the message, and never
// whether its value differs from the material's default: the viewer nudges
// a default value by an epsilon so that it still counts as set
// (applyOverrideLLSD, llgltfmaterial.cpp:768-838), and the key is the
// whole of that.
//
// The values are never written through after they are made, so a copy of
// a GLTFOverride may share them.
type GLTFOverride struct {
	// Textures are the override's texture ids by slot (GLTFSlot*); nil
	// for a slot that is not overridden.  GLTFOverrideNullTexture is a
	// slot overridden to no texture.
	Textures [GLTFSlots]*UUID

	// BaseColour is red, green, blue and alpha, each 0 to 1 and linear as
	// the material holds them; Emissive is red, green and blue.
	BaseColour *[4]float32
	Emissive   *[3]float32

	Metallic    *float32
	Roughness   *float32
	AlphaMode   *GLTFAlphaMode
	AlphaCutoff *float32
	DoubleSided *bool

	// Transforms are by texture slot.
	Transforms [GLTFSlots]GLTFTransform
}

// Empty is whether the override sets nothing: a face with an empty one is
// one the viewer holds a default material override for, which changes
// nothing.
func (o *GLTFOverride) Empty() bool {
	if o == nil {
		return true
	}
	for _, t := range o.Textures {
		if t != nil {
			return false
		}
	}
	for _, t := range o.Transforms {
		if t.Offset != nil || t.Scale != nil || t.Rotation != nil {
			return false
		}
	}
	return o.BaseColour == nil && o.Emissive == nil && o.Metallic == nil && o.Roughness == nil &&
		o.AlphaMode == nil && o.AlphaCutoff == nil && o.DoubleSided == nil
}

// ParseGLTFOverride reads one face's override map, as applyOverrideLLSD
// does.  Which keys replace and which merge is the caller's business and
// not in the map: every key present is set, every key absent is not.
//
// The viewer's own type tests are kept: mf, rf, ac and a transform's r
// are read only when they are reals, am only when an integer, ds only
// when a boolean, so an integer metallic factor is not an override.  A
// colour, emissive colour, offset or scale that is not an array is
// skipped here, where the viewer would read it as zeros; no region has
// been seen to send one.  A nil v, the undef a clear sends, is the empty
// override.
func ParseGLTFOverride(v any) *GLTFOverride {
	m := llsd.Map(v)
	o := &GLTFOverride{}
	if tex, ok := m["tex"].([]any); ok {
		for i := 0; i < len(tex) && i < GLTFSlots; i++ {
			s, _ := tex[i].(string)
			if id, err := ParseUUID(s); err == nil && !id.IsZero() {
				o.Textures[i] = &id
			}
		}
	}
	if f, ok := reals(m["bc"], 4); ok {
		c := [4]float32{f[0], f[1], f[2], f[3]}
		o.BaseColour = &c
	}
	if f, ok := reals(m["ec"], 3); ok {
		c := [3]float32{f[0], f[1], f[2]}
		o.Emissive = &c
	}
	o.Metallic = real32(m["mf"])
	o.Roughness = real32(m["rf"])
	if n, ok := m["am"].(int64); ok {
		am := GLTFAlphaMode(n)
		o.AlphaMode = &am
	}
	o.AlphaCutoff = real32(m["ac"])
	if b, ok := m["ds"].(bool); ok {
		o.DoubleSided = &b
	}
	if ti, ok := m["ti"].([]any); ok {
		for i := 0; i < len(ti) && i < GLTFSlots; i++ {
			t := llsd.Map(ti[i])
			if f, ok := reals(t["o"], 2); ok {
				o.Transforms[i].Offset = &[2]float32{f[0], f[1]}
			}
			if f, ok := reals(t["s"], 2); ok {
				o.Transforms[i].Scale = &[2]float32{f[0], f[1]}
			}
			o.Transforms[i].Rotation = real32(t["r"])
		}
	}
	return o
}

// real32 is a value that is a real, as a float32, and nil for any other
// type: LLSD::isReal.
func real32(v any) *float32 {
	f, ok := v.(float64)
	if !ok {
		return nil
	}
	x := float32(f)
	return &x
}

// reals reads an array of n numbers as the viewer's setValue does: each
// element as a real, an integer or a boolean counting as one, and one
// missing as zero.  It is false for a value that is not an array.
func reals(v any, n int) ([]float32, bool) {
	a, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]float32, n)
	for i := 0; i < n && i < len(a); i++ {
		switch x := a[i].(type) {
		case float64:
			out[i] = float32(x)
		case int64:
			out[i] = float32(x)
		case bool:
			if x {
				out[i] = 1
			}
		}
	}
	return out, true
}

// A GLTFOverrideUpdate is one material override message: the object it is
// about and the whole of that object's overrides as it says them.
type GLTFOverrideUpdate struct {
	// Local is the object's local id in the region that sent it.
	Local uint32

	// Faces are the overrides of the faces the message names that set
	// anything, by face.  A face the message names with undef, or with a
	// map that sets nothing, is not in it, and a face it does not name is
	// not either.
	Faces map[int]*GLTFOverride
}

// ParseGLTFOverrideUpdate reads the data of a GenericStreamingMessage whose
// method is GenericMethodGLTFMaterialOverride: a map with the object's
// local id, id, and two parallel arrays, te the faces and od the override
// of each.
//
// The message is the object's whole state and not a change to it: the
// viewer clears the override of every face of the object that the te
// array does not name (llgltfmateriallist.cpp:224-232), and an empty te
// array clears them all.  A message with no te array is malformed, and the
// viewer ignores it (:191); here it is an error.  A face past MaxGLTFFaces
// entries of te, or numbered outside 0 to 44, is left out.
func ParseGLTFOverrideUpdate(data []byte) (*GLTFOverrideUpdate, error) {
	v, err := llsd.DecodeNotation(data)
	if err != nil {
		return nil, err
	}
	m := llsd.Map(v)
	if m == nil {
		return nil, fmt.Errorf("msg: material override is a %T, want a map", v)
	}
	te, ok := m["te"].([]any)
	if !ok {
		return nil, fmt.Errorf("msg: material override has no te array")
	}
	od, _ := m["od"].([]any)
	u := &GLTFOverrideUpdate{Local: uint32(llsd.Int(m, "id")), Faces: map[int]*GLTFOverride{}}
	for i := 0; i < len(te) && i < MaxGLTFFaces; i++ {
		face, ok := te[i].(int64)
		if !ok || face < 0 || face >= MaxGLTFFaces {
			continue
		}
		var one any
		if i < len(od) {
			one = od[i]
		}
		// The viewer replaces what a face had with each mention of it,
		// so a later one wins.
		if o := ParseGLTFOverride(one); !o.Empty() {
			u.Faces[int(face)] = o
		} else {
			delete(u.Faces, int(face))
		}
	}
	return u, nil
}

// RenderMaterialsOf returns the GLTF material id each face of an object
// has, by face, from the render material block of its extra parameters,
// and nil when there is no block or it names none.  The block is a count
// and, for each, a face and an id (LLRenderMaterialParams::unpack,
// llprimitive.cpp:2358-2370; getMaterial :2426).  A face the block does
// not name has none, and a null id names none.
func RenderMaterialsOf(params []ExtraParam) map[int]UUID {
	for _, p := range params {
		if p.Type != ExtraRenderMat || len(p.Data) < 1 {
			continue
		}
		var out map[int]UUID
		n := int(p.Data[0])
		for i := 0; i < n && 1+17*(i+1) <= len(p.Data); i++ {
			e := p.Data[1+17*i:]
			var id UUID
			copy(id[:], e[1:17])
			if id.IsZero() {
				continue
			}
			if out == nil {
				out = map[int]UUID{}
			}
			out[int(e[0])] = id
		}
		return out
	}
	return nil
}
