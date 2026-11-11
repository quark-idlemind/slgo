package mesh

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/quark-idlemind/slgo/llsd"
)

// LODs are the level-of-detail sections, in the order the viewer writes
// them.  Encode writes the same geometry to each.
var LODs = []string{"lowest_lod", "low_lod", "medium_lod", "high_lod"}

// Encoded is what Encode made, and what an upload says about it besides
// the bytes.
type Encoded struct {
	Asset []byte

	// Scale is the model's size in meters.  The asset holds it shrunk
	// into a unit cube, and an upload's instance carries this to put it
	// back.
	Scale [3]float64

	// Center is where the middle of the model was, in its own frame;
	// the asset holds it moved to the origin.
	Center [3]float64

	Triangles int
	Vertices  int
	Rigged    bool
}

// Encode writes m as a mesh asset.
//
// The asset holds the model shrunk to fit a unit cube centered on the
// origin, as the viewer's normalizeVolumeFaces leaves it; Encoded.Scale
// is what brings it back to size.  For a rigged mesh the shrinking is
// undone in the bind shape matrix instead, as the viewer's DAE loader
// does it, so the skin draws the vertices where the modeler put them.
//
// Every level of detail is the same geometry, and the physics shape is
// the model's bounding box.
func Encode(m *Model) (*Encoded, error) {
	if err := m.Check(); err != nil {
		return nil, err
	}
	lo, hi := m.Bounds()
	var e Encoded
	for k := range 3 {
		e.Center[k] = (lo[k] + hi[k]) / 2
		e.Scale[k] = hi[k] - lo[k]
		// The viewer's guard against a flat model: an axis with no
		// extent is left at 1 rather than divided by.
		if math.Abs(e.Scale[k]) < 1e-6 {
			e.Scale[k] = 1
		}
	}
	e.Triangles = m.Triangles()
	e.Rigged = m.Skin != nil

	faces := make([]any, len(m.Faces))
	for i, f := range m.Faces {
		faces[i] = e.face(f)
		e.Vertices += len(f.Positions)
	}

	sections := map[string]any{}
	for _, l := range LODs {
		sections[l] = faces
	}
	sections["physics_convex"] = boundingHull()
	if m.Skin != nil {
		sections["skin"] = e.skin(m.Skin)
	}

	asset, err := write(sections)
	if err != nil {
		return nil, err
	}
	e.Asset = asset
	return &e, nil
}

// order is the order sections follow the header in, writeModelToStream's.
var order = []string{"skin", "physics_convex", "lowest_lod", "low_lod", "medium_lod", "high_lod", "physics_mesh"}

// write deflates each section and puts the header in front.
func write(sections map[string]any) ([]byte, error) {
	header := map[string]any{}
	var body bytes.Buffer
	for _, name := range order {
		v, ok := sections[name]
		if !ok {
			continue
		}
		z, err := zip(v)
		if err != nil {
			return nil, fmt.Errorf("mesh: %s: %w", name, err)
		}
		header[name] = map[string]any{"offset": body.Len(), "size": len(z)}
		body.Write(z)
	}
	h, err := llsd.EncodeBinary(header)
	if err != nil {
		return nil, err
	}
	return append(h, body.Bytes()...), nil
}

// zip is the viewer's zip_llsd: binary LLSD, deflated at the best
// compression, in a zlib wrapper.
func zip(v any) ([]byte, error) {
	raw, err := llsd.EncodeBinary(v)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	w, err := zlib.NewWriterLevel(&b, zlib.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(raw); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// quantize is a value in [lo, lo+span] as 16 bits, truncated as the
// viewer's cast truncates.  A domain with no span writes zero.
func quantize(v, lo, span float64) uint16 {
	if span == 0 {
		return 0
	}
	q := (v - lo) / span * 65535
	return uint16(math.Max(0, math.Min(65535, q)))
}

func (e *Encoded) normalize(p [3]float64) [3]float64 {
	var n [3]float64
	for k := range 3 {
		n[k] = (p[k] - e.Center[k]) / e.Scale[k]
	}
	return n
}

// face is one face of a level of detail, as writeModel writes it.
func (e *Encoded) face(f Face) map[string]any {
	n := len(f.Positions)
	pos := make([][3]float64, n)
	lo := e.normalize(f.Positions[0])
	hi := lo
	for i, p := range f.Positions {
		pos[i] = e.normalize(p)
		for k := range 3 {
			lo[k] = math.Min(lo[k], pos[i][k])
			hi[k] = math.Max(hi[k], pos[i][k])
		}
	}
	// The domain is the face's own here, where the viewer's is the
	// whole model's.  Both are read back the same way, and a face's
	// own is the finer.
	verts := make([]byte, 0, n*6)
	for _, p := range pos {
		for k := range 3 {
			verts = binary.LittleEndian.AppendUint16(verts, quantize(p[k], lo[k], hi[k]-lo[k]))
		}
	}
	out := map[string]any{
		"PositionDomain":  map[string]any{"Min": vec(lo[:]), "Max": vec(hi[:])},
		"Position":        verts,
		"NormalizedScale": vec(e.Scale[:]),
	}

	if f.Normals != nil {
		norms := make([]byte, 0, n*6)
		for _, v := range f.Normals {
			// A normal moves the opposite way to the positions it is
			// normal to when they are scaled, and is unit length again
			// afterwards.
			var s [3]float64
			l := 0.0
			for k := range 3 {
				s[k] = v[k] * e.Scale[k]
				l += s[k] * s[k]
			}
			l = math.Sqrt(l)
			for k := range 3 {
				c := 0.0
				if l > 0 {
					c = s[k] / l
				}
				norms = binary.LittleEndian.AppendUint16(norms, quantize(c, -1, 2))
			}
		}
		out["Normal"] = norms
	}

	if f.UVs != nil {
		ulo, uhi := f.UVs[0], f.UVs[0]
		for _, t := range f.UVs {
			for k := range 2 {
				ulo[k] = math.Min(ulo[k], t[k])
				uhi[k] = math.Max(uhi[k], t[k])
			}
		}
		tc := make([]byte, 0, n*4)
		for _, t := range f.UVs {
			for k := range 2 {
				tc = binary.LittleEndian.AppendUint16(tc, quantize(t[k], ulo[k], uhi[k]-ulo[k]))
			}
		}
		out["TexCoord0Domain"] = map[string]any{"Min": vec(ulo[:]), "Max": vec(uhi[:])}
		out["TexCoord0"] = tc
	}

	idx := make([]byte, 0, len(f.Indices)*2)
	for _, x := range f.Indices {
		idx = binary.LittleEndian.AppendUint16(idx, uint16(x))
	}
	out["TriangleList"] = idx

	if f.Weights != nil {
		// Up to four of a joint byte and a 16-bit weight per vertex,
		// and 0xFF after fewer than four.
		var w []byte
		for _, ws := range f.Weights {
			count := 0
			for _, jw := range ws {
				if jw[1] <= 0 {
					continue
				}
				w = append(w, byte(jw[0]))
				w = binary.LittleEndian.AppendUint16(w, uint16(math.Min(65535, jw[1]*65535)))
				count++
			}
			if count < 4 {
				w = append(w, 0xFF)
			}
		}
		out["Weights"] = w
	}
	return out
}

func vec(v []float64) []any {
	a := make([]any, len(v))
	for i, x := range v {
		a[i] = x
	}
	return a
}

// boundingHull is the physics shape: the corners of the unit cube the
// normalized model fills, as Decomposition::asLLSD writes a base hull.
func boundingHull() map[string]any {
	var p []byte
	for _, x := range []uint16{0, 65535} {
		for _, y := range []uint16{0, 65535} {
			for _, z := range []uint16{0, 65535} {
				p = binary.LittleEndian.AppendUint16(p, x)
				p = binary.LittleEndian.AppendUint16(p, y)
				p = binary.LittleEndian.AppendUint16(p, z)
			}
		}
	}
	return map[string]any{
		"Min":           vec([]float64{-0.5, -0.5, -0.5}),
		"Max":           vec([]float64{0.5, 0.5, 0.5}),
		"BoundingVerts": p,
	}
}

// skin is the skin section.  The bind shape matrix written is the
// model's own after the one that undoes normalize -- scale, then move
// back to Center -- in the viewer's row-vector order, so that a vertex
// times it is where the modeler put it before the bind shape applied.
func (e *Encoded) skin(s *Skin) map[string]any {
	var undo [16]float64
	undo[0], undo[5], undo[10], undo[15] = e.Scale[0], e.Scale[1], e.Scale[2], 1
	undo[12], undo[13], undo[14] = e.Center[0], e.Center[1], e.Center[2]
	bind := matMul(undo, s.BindShape)

	inv := make([]any, len(s.InverseBind))
	for i, m := range s.InverseBind {
		inv[i] = vec(m[:])
	}
	names := make([]any, len(s.Joints))
	for i, j := range s.Joints {
		names[i] = j
	}
	return map[string]any{
		"joint_names":         names,
		"inverse_bind_matrix": inv,
		"bind_shape_matrix":   vec(bind[:]),
	}
}

// matMul is a times b, both row-major 4x4.
func matMul(a, b [16]float64) [16]float64 {
	var r [16]float64
	for i := range 4 {
		for j := range 4 {
			s := 0.0
			for k := range 4 {
				s += a[i*4+k] * b[k*4+j]
			}
			r[i*4+j] = s
		}
	}
	return r
}

// Asset is a mesh asset read back: the header, and each section it
// names, inflated and decoded.
type Asset struct {
	Header   map[string]any
	Sections map[string]any
}

// Decode reads a mesh asset.
func Decode(b []byte) (*Asset, error) {
	h, n, err := llsd.DecodeBinary(b)
	if err != nil {
		return nil, fmt.Errorf("mesh: header: %w", err)
	}
	hm, ok := h.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("mesh: the header is a %T, not a map", h)
	}
	a := &Asset{Header: hm, Sections: map[string]any{}}
	body := b[n:]
	for name, v := range hm {
		sec, ok := v.(map[string]any)
		if !ok {
			continue
		}
		off, ok1 := sec["offset"].(int64)
		size, ok2 := sec["size"].(int64)
		if !ok1 || !ok2 {
			continue
		}
		if off < 0 || size < 0 || off+size > int64(len(body)) {
			return nil, fmt.Errorf("mesh: %s is at %d+%d, past the end of %d bytes", name, off, size, len(body))
		}
		r, err := zlib.NewReader(bytes.NewReader(body[off : off+size]))
		if err != nil {
			return nil, fmt.Errorf("mesh: %s: %w", name, err)
		}
		raw, err := io.ReadAll(io.LimitReader(r, 64<<20))
		if err != nil {
			return nil, fmt.Errorf("mesh: %s: %w", name, err)
		}
		val, _, err := llsd.DecodeBinary(raw)
		if err != nil {
			return nil, fmt.Errorf("mesh: %s: %w", name, err)
		}
		a.Sections[name] = val
	}
	return a, nil
}
