package mesh

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// cube is a box 2 by 1 by 0.5 meters, centered off the origin, so that
// both the scaling and the moving of normalize are seen.
func cube() *Model {
	c := [3]float64{3, -1, 0.5}
	h := [3]float64{1, 0.5, 0.25}
	var f Face
	for i := range 8 {
		p := c
		for k := range 3 {
			if i>>k&1 == 1 {
				p[k] += h[k]
			} else {
				p[k] -= h[k]
			}
		}
		f.Positions = append(f.Positions, p)
		f.Normals = append(f.Normals, [3]float64{0, 0, 1})
		f.UVs = append(f.UVs, [2]float64{float64(i & 1), float64(i >> 1 & 1)})
	}
	f.Indices = []int{0, 1, 3, 0, 3, 2, 4, 6, 7, 4, 7, 5}
	return &Model{Name: "test cube", Faces: []Face{f}}
}

// positions reads a face's positions back to meters, the way the viewer
// does: dequantize into the domain, then scale by what the instance
// carries and move back to the center.
func positions(t *testing.T, face map[string]any, e *Encoded) [][3]float64 {
	t.Helper()
	dom := face["PositionDomain"].(map[string]any)
	lo, hi := dom["Min"].([]any), dom["Max"].([]any)
	raw := face["Position"].([]byte)
	var out [][3]float64
	for i := 0; i+6 <= len(raw); i += 6 {
		var p [3]float64
		for k := range 3 {
			q := float64(binary.LittleEndian.Uint16(raw[i+2*k:]))
			l, h := lo[k].(float64), hi[k].(float64)
			p[k] = (l+q/65535*(h-l))*e.Scale[k] + e.Center[k]
		}
		out = append(out, p)
	}
	return out
}

func TestEncodeRoundTrip(t *testing.T) {
	m := cube()
	e, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if e.Scale != [3]float64{2, 1, 0.5} || e.Center != [3]float64{3, -1, 0.5} {
		t.Errorf("scale %v center %v", e.Scale, e.Center)
	}
	a, err := Decode(e.Asset)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range append(LODs, "physics_convex") {
		if _, ok := a.Sections[name]; !ok {
			t.Errorf("no %s section", name)
		}
	}
	if _, ok := a.Sections["skin"]; ok {
		t.Error("an unrigged cube has a skin")
	}
	face := a.Sections["high_lod"].([]any)[0].(map[string]any)
	got := positions(t, face, e)
	for i, p := range got {
		for k := range 3 {
			if d := math.Abs(p[k] - m.Faces[0].Positions[i][k]); d > 1e-4 {
				t.Errorf("vertex %d axis %d: %v, want %v", i, k, p[k], m.Faces[0].Positions[i][k])
			}
		}
	}
	idx := face["TriangleList"].([]byte)
	for i, want := range m.Faces[0].Indices {
		if g := int(binary.LittleEndian.Uint16(idx[2*i:])); g != want {
			t.Errorf("index %d: %d, want %d", i, g, want)
		}
	}
	if n := len(face["Normal"].([]byte)); n != 8*6 {
		t.Errorf("%d bytes of normals", n)
	}
	if n := len(face["TexCoord0"].([]byte)); n != 8*4 {
		t.Errorf("%d bytes of uvs", n)
	}
}

func TestEncodeRigged(t *testing.T) {
	m := cube()
	f := &m.Faces[0]
	for i := range f.Positions {
		if i%2 == 0 {
			f.Weights = append(f.Weights, [][2]float64{{0, 1}})
		} else {
			f.Weights = append(f.Weights, [][2]float64{{0, 0.25}, {1, 0.75}})
		}
	}
	id := [16]float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}
	m.Skin = &Skin{Joints: []string{"mPelvis", "mTorso"}, BindShape: id, InverseBind: [][16]float64{id, id}}
	e, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Decode(e.Asset)
	if err != nil {
		t.Fatal(err)
	}
	skin := a.Sections["skin"].(map[string]any)
	bind := skin["bind_shape_matrix"].([]any)

	// A normalized vertex times the bind shape matrix, in the viewer's
	// row-vector order, is where the modeler put it.
	face := a.Sections["high_lod"].([]any)[0].(map[string]any)
	dom := face["PositionDomain"].(map[string]any)
	raw := face["Position"].([]byte)
	for i, want := range f.Positions {
		var v [4]float64
		v[3] = 1
		for k := range 3 {
			q := float64(binary.LittleEndian.Uint16(raw[6*i+2*k:]))
			l, h := dom["Min"].([]any)[k].(float64), dom["Max"].([]any)[k].(float64)
			v[k] = l + q/65535*(h-l)
		}
		for j := range 3 {
			s := 0.0
			for k := range 4 {
				s += v[k] * bind[k*4+j].(float64)
			}
			if math.Abs(s-want[j]) > 1e-4 {
				t.Errorf("vertex %d axis %d: %v, want %v", i, j, s, want[j])
			}
		}
	}

	w := face["Weights"].([]byte)
	// Vertex 0: joint 0 at 65535, then 0xFF.  Vertex 1: two, then 0xFF.
	want := []byte{0, 0xff, 0xff, 0xff, 0, 0xff, 0x3f, 1, 0xff, 0xbf, 0xff}
	if string(w[:len(want)]) != string(want) {
		t.Errorf("weights % x\nwant    % x", w[:len(want)], want)
	}
}

func TestReadJSONTransposesCollada(t *testing.T) {
	const in = `{"name":"t","matrix_order":"collada","faces":[{"positions":[[0,0,0],[1,0,0],[0,1,0]],"indices":[0,1,2],
		"weights":[[[0,1]],[[0,1]],[[0,1]]]}],
		"skin":{"joints":["mPelvis"],"bind_shape":[1,0,0,5, 0,1,0,6, 0,0,1,7, 0,0,0,1],
		"inverse_bind":[[1,0,0,0, 0,1,0,0, 0,0,1,0, 0,0,0,1]]}}`
	m, err := ReadJSON(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if b := m.Skin.BindShape; b[12] != 5 || b[13] != 6 || b[14] != 7 || b[3] != 0 {
		t.Errorf("bind shape %v", b)
	}
}

func TestCheckRefuses(t *testing.T) {
	for _, tc := range []struct {
		why  string
		edit func(*Model)
	}{
		{"an index past the end", func(m *Model) { m.Faces[0].Indices[0] = 8 }},
		{"part of a triangle", func(m *Model) { m.Faces[0].Indices = m.Faces[0].Indices[:4] }},
		{"too few normals", func(m *Model) { m.Faces[0].Normals = m.Faces[0].Normals[:3] }},
		{"weights without a skin", func(m *Model) { m.Faces[0].Weights = make([][][2]float64, 8) }},
		{"nine faces", func(m *Model) {
			for range 8 {
				m.Faces = append(m.Faces, m.Faces[0])
			}
		}},
	} {
		m := cube()
		tc.edit(m)
		if err := m.Check(); err == nil {
			t.Errorf("%s was accepted", tc.why)
		}
	}
}
