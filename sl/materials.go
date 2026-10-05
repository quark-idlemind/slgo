package sl

// A face's legacy material: what the texture entry names only by id.
//
// The texture entry says which material a face has and nothing about it
// (Face.Material).  The region keeps the materials -- the normal and
// specular maps, and the face's alpha mode -- and gives them out on the
// RenderMaterials capability, one POST for the ids asked about.
// Why: doc/materials.md

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/internal/llsdbin"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// MaterialsCap is the capability that serves materials.  It is in
// agent.DefaultCaps, and a region that does not grant it cannot be asked.
const MaterialsCap = "RenderMaterials"

// ErrNoMaterialsCap is a session that holds no RenderMaterials
// capability: the region did not grant it, or the daemon the session
// is attached to was not asked for it.
var ErrNoMaterialsCap = errors.New("sl: this session holds no " + MaterialsCap + " capability, so a face's material cannot be read")

// materialsPerRequest is how many ids one POST carries, which is the
// viewer's own limit (MATERIALS_POST_MAX_ENTRIES in llmaterialmgr.cpp).
const materialsPerRequest = 50

// AlphaMode is how a face is drawn where its texture has alpha.  The
// first four are the protocol's own values, DiffuseAlphaMode in a
// material and the PRIM_ALPHA_MODE of LSL; the fifth is a face with no
// material at all.
type AlphaMode int

const (
	AlphaModeNone     AlphaMode = iota // opaque: the texture's alpha is ignored
	AlphaModeBlend                     // blended by the texture's alpha
	AlphaModeMask                      // cut at AlphaMaskCutoff: opaque above it, gone below
	AlphaModeEmissive                  // the texture's alpha says how much the face glows

	// AlphaModeDefault is a face with no material: the viewer decides
	// from the texture, blended if it has an alpha channel and opaque if
	// not.  Which of the two it is cannot be known from the entry, so it
	// is not reported as either.
	// Why: doc/materials.md#a-face-with-no-material
	AlphaModeDefault
)

func (m AlphaMode) String() string {
	switch m {
	case AlphaModeNone:
		return "none"
	case AlphaModeBlend:
		return "blend"
	case AlphaModeMask:
		return "mask"
	case AlphaModeEmissive:
		return "emissive"
	case AlphaModeDefault:
		return "default"
	}
	return fmt.Sprintf("AlphaMode(%d)", int(m))
}

// A Material is the legacy material of a face, as the region keeps it.
// Its id is content-addressed: change any field and the face has a
// different material id, so what an id names never changes.
type Material struct {
	AlphaMode AlphaMode // never AlphaModeDefault: that is a face with no Material
	// AlphaMaskCutoff is the level, 0 to 255, below which AlphaModeMask
	// drops a pixel.
	AlphaMaskCutoff uint8

	// EnvIntensity is how much of the environment the face reflects, 0
	// to 255.
	EnvIntensity uint8

	// The normal map and the specular map, by texture id, with how each
	// is laid on the face.  Offsets and repeats are as the build tools
	// show them (a repeat of 1 is one copy over the face); a rotation is
	// in radians.  On the wire all of them are whole numbers of
	// ten-thousandths.
	NormMap                  msg.UUID
	NormOffsetX, NormOffsetY float32
	NormRepeatX, NormRepeatY float32
	NormRotation             float32
	SpecMap                  msg.UUID
	SpecOffsetX, SpecOffsetY float32
	SpecRepeatX, SpecRepeatY float32
	SpecRotation             float32
	SpecColor                [4]uint8 // red, green, blue, alpha
	SpecExp                  uint8    // the specular exponent
}

// materialCache is what the session has read, by id.  An id is
// content-addressed -- a change of mode is a new id -- so an entry is
// never stale and is never dropped.
type materialCache struct {
	mu sync.Mutex
	by map[msg.UUID]*Material
}

// Materials reads the materials the ids name, with one POST to
// RenderMaterials for the ones not read before.  A zero id names no
// material and is skipped.
//
// An id the region does not answer for is left out of the map, rather
// than being an error: a face can name a material the region has not
// stored yet, and asking again later may find it.  Nothing is cached for
// it.  The Materials returned are copies.
//
// A session without the capability is ErrNoMaterialsCap.
// Why: doc/materials.md#the-capability
func (w *Session) Materials(ctx context.Context, ids []msg.UUID) (map[msg.UUID]*Material, error) {
	out := make(map[msg.UUID]*Material, len(ids))
	var need []msg.UUID
	w.materials.mu.Lock()
	for _, id := range ids {
		if id.IsZero() {
			continue
		}
		if m, ok := w.materials.by[id]; ok {
			c := *m
			out[id] = &c
		} else if _, dup := out[id]; !dup && !contains(need, id) {
			need = append(need, id)
		}
	}
	w.materials.mu.Unlock()
	if len(need) == 0 {
		return out, nil
	}
	if !w.b.HasCap(MaterialsCap) {
		return nil, ErrNoMaterialsCap
	}
	for len(need) > 0 {
		n := min(len(need), materialsPerRequest)
		got, err := w.askMaterials(ctx, need[:n])
		if err != nil {
			return nil, err
		}
		need = need[n:]
		w.materials.mu.Lock()
		if w.materials.by == nil {
			w.materials.by = map[msg.UUID]*Material{}
		}
		for id, m := range got {
			w.materials.by[id] = m
			c := *m
			out[id] = &c
		}
		w.materials.mu.Unlock()
	}
	return out, nil
}

func contains(ids []msg.UUID, id msg.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// askMaterials is one POST: the ids zipped as LLSD binary inside an LLSD
// XML map, and the answer the same way round.
func (w *Session) askMaterials(ctx context.Context, ids []msg.UUID) (map[msg.UUID]*Material, error) {
	list := make([]any, len(ids))
	for i, id := range ids {
		list[i] = []byte(id[:])
	}
	z, err := llsdbin.EncodeZipped(list)
	if err != nil {
		return nil, fmt.Errorf("sl: materials: %w", err)
	}
	body, err := llsd.Encode(map[string]any{"Zipped": z})
	if err != nil {
		return nil, fmt.Errorf("sl: materials: %w", err)
	}
	answer, err := w.capDo(ctx, agent.CapRequest{
		Cap: MaterialsCap, Method: "POST", Body: body, Type: "application/llsd+xml",
	})
	if err != nil {
		return nil, err
	}
	v, err := llsd.Decode(bytes.NewReader(answer))
	if err != nil {
		return nil, fmt.Errorf("sl: materials: the answer is not LLSD: %w", err)
	}
	m := llsd.Map(v)
	zipped := llsd.Bytes(m, "Zipped")
	if zipped == nil {
		return nil, errors.New("sl: materials: the answer has no Zipped binary")
	}
	inner, err := llsdbin.DecodeZipped(zipped)
	if err != nil {
		return nil, fmt.Errorf("sl: materials: %w", err)
	}
	entries, ok := inner.([]any)
	if !ok {
		return nil, fmt.Errorf("sl: materials: the answer is %T, wanted an array", inner)
	}
	got := map[msg.UUID]*Material{}
	for _, e := range entries {
		em, _ := e.(map[string]any)
		raw, _ := em["ID"].([]byte)
		if len(raw) != 16 {
			return nil, errors.New("sl: materials: an entry has no 16 byte ID")
		}
		var id msg.UUID
		copy(id[:], raw)
		mat, err := materialOf(em["Material"])
		if err != nil {
			return nil, fmt.Errorf("sl: materials: %s: %w", id, err)
		}
		got[id] = mat
	}
	return got, nil
}

// materialOf reads one Material map, as llmaterial.cpp's fromLLSD does:
// the numbers are integers, and the viewer refuses a material missing one.
// A missing field here is the zero of its kind, except the alpha mode,
// without which the answer is not about what was asked.
func materialOf(v any) (*Material, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("the Material is not a map")
	}
	mode, ok := m["DiffuseAlphaMode"].(int64)
	if !ok {
		return nil, errors.New("the Material has no DiffuseAlphaMode")
	}
	if mode < int64(AlphaModeNone) || mode > int64(AlphaModeEmissive) {
		return nil, fmt.Errorf("DiffuseAlphaMode %d is not a mode", mode)
	}
	num := func(k string) int64 { n, _ := m[k].(int64); return n }
	level := func(k string) uint8 { return uint8(max(0, min(255, num(k)))) }
	id := func(k string) msg.UUID {
		var u msg.UUID
		if x, ok := m[k].(llsdbin.UUID); ok {
			copy(u[:], x[:])
		}
		return u
	}
	ten := func(k string) float32 { return float32(num(k)) / 10000 }
	mat := &Material{
		AlphaMode: AlphaMode(mode), AlphaMaskCutoff: level("AlphaMaskCutoff"),
		EnvIntensity: level("EnvIntensity"), SpecExp: level("SpecExp"),
		NormMap: id("NormMap"), NormOffsetX: ten("NormOffsetX"), NormOffsetY: ten("NormOffsetY"),
		NormRepeatX: ten("NormRepeatX"), NormRepeatY: ten("NormRepeatY"), NormRotation: ten("NormRotation"),
		SpecMap: id("SpecMap"), SpecOffsetX: ten("SpecOffsetX"), SpecOffsetY: ten("SpecOffsetY"),
		SpecRepeatX: ten("SpecRepeatX"), SpecRepeatY: ten("SpecRepeatY"), SpecRotation: ten("SpecRotation"),
	}
	if c, ok := m["SpecColor"].([]any); ok {
		for i := 0; i < len(c) && i < 4; i++ {
			n, _ := c[i].(int64)
			mat.SpecColor[i] = uint8(max(0, min(255, n)))
		}
	}
	return mat, nil
}

// AlphaModeOf is the alpha mode of a face already read, and for a mask
// its cutoff (zero for the others).  A face with no material is
// AlphaModeDefault, with no request; one with a material is read by
// Materials, and so is cached.
//
// A material the region does not answer for is an error rather than a
// guess: the face names one the region has not yet stored.
// Why: doc/materials.md#a-face-with-no-material
func (w *Session) AlphaModeOf(ctx context.Context, f Face) (AlphaMode, uint8, error) {
	if f.Material.IsZero() {
		return AlphaModeDefault, 0, nil
	}
	got, err := w.Materials(ctx, []msg.UUID{f.Material})
	if err != nil {
		return 0, 0, err
	}
	m := got[f.Material]
	if m == nil {
		return 0, 0, fmt.Errorf("sl: the region has no material %s, which the face names", f.Material)
	}
	if m.AlphaMode != AlphaModeMask {
		return m.AlphaMode, 0, nil
	}
	return m.AlphaMode, m.AlphaMaskCutoff, nil
}

// FaceAlphaMode reads an object's face and says how it is drawn
// where its texture has alpha: AlphaModeDefault for a face with no
// material, else its material's mode, and for a mask the cutoff.
//
// The face is read as Faces reads it, and a number past the object's
// faces is an error.  A script that has just set the mode is seen when
// the region's next update says so, as with any change of a face.
// Why: doc/materials.md
func (w *Session) FaceAlphaMode(ctx context.Context, o *Object, face int) (AlphaMode, uint8, error) {
	faces, err := w.Faces(ctx, o)
	if err != nil {
		return 0, 0, err
	}
	if face < 0 || face >= len(faces) {
		return 0, 0, fmt.Errorf("sl: %s has %d faces, so there is no face %d", o, len(faces), face)
	}
	return w.AlphaModeOf(ctx, faces[face])
}
