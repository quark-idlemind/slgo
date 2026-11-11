// Package mesh writes and reads Second Life's mesh asset.
//
// A mesh asset is a binary LLSD header followed by sections, each a
// binary LLSD value deflated with zlib: the levels of detail, the skin
// of a rigged mesh, and a physics hull.  The header names each section
// with its offset, counted from the end of the header, and its size.
// What is written here is what LLModel::writeModel and
// writeModelToStream in the viewer write (llprimitive/llmodel.cpp).
//
// A Model is the geometry before any of that: positions in meters,
// normals, texture coordinates, triangles, and for a rigged mesh the
// weights and the skin.  Encode turns one into an asset; Decode reads an
// asset back into its header and inflated sections.
//
// Why: doc/mesh.md
package mesh

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
)

// MaxFaces is how many faces a mesh may have, one per material.
const MaxFaces = 8

// MaxJoints is how many joints a skin may name: a weight's joint is one
// byte, and 0xFF ends a vertex's list of them.
const MaxJoints = 255

// Model is one mesh, as the file a modeler hands over describes it.
type Model struct {
	Name  string `json:"name"`
	Faces []Face `json:"faces"`
	Skin  *Skin  `json:"skin,omitempty"`

	// MatrixOrder is "collada" when the skin's matrices are written as
	// Collada writes them, translation in elements 3, 7 and 11; empty
	// or "ll" is the viewer's LLMatrix4 order, translation in 12-14.
	MatrixOrder string `json:"matrix_order,omitempty"`
}

// Face is one material's share of the mesh.
type Face struct {
	Positions [][3]float64 `json:"positions"`
	Normals   [][3]float64 `json:"normals,omitempty"`
	UVs       [][2]float64 `json:"uvs,omitempty"`
	Indices   []int        `json:"indices"`

	// Weights, for a rigged mesh, is each vertex's joints: up to four
	// pairs of a joint index into Skin.Joints and a weight.
	Weights [][][2]float64 `json:"weights,omitempty"`
}

// Skin is what binds a rigged mesh to the avatar's skeleton.  Matrices
// are LLMatrix4 order unless Model.MatrixOrder says otherwise.
type Skin struct {
	Joints      []string      `json:"joints"`
	BindShape   [16]float64   `json:"bind_shape"`
	InverseBind [][16]float64 `json:"inverse_bind"`
}

// ReadJSON reads a Model from JSON and checks it.  Collada-ordered
// matrices come back in LLMatrix4 order.
func ReadJSON(r io.Reader) (*Model, error) {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	var m Model
	if err := d.Decode(&m); err != nil {
		return nil, fmt.Errorf("mesh: %w", err)
	}
	switch m.MatrixOrder {
	case "", "ll":
	case "collada":
		if m.Skin != nil {
			m.Skin.BindShape = transpose(m.Skin.BindShape)
			for i := range m.Skin.InverseBind {
				m.Skin.InverseBind[i] = transpose(m.Skin.InverseBind[i])
			}
		}
		m.MatrixOrder = "ll"
	default:
		return nil, fmt.Errorf("mesh: matrix_order %q is neither ll nor collada", m.MatrixOrder)
	}
	if err := m.Check(); err != nil {
		return nil, err
	}
	return &m, nil
}

func transpose(a [16]float64) [16]float64 {
	var t [16]float64
	for i := range 4 {
		for j := range 4 {
			t[i*4+j] = a[j*4+i]
		}
	}
	return t
}

// Check says what is wrong with a model that would make an asset the
// grid refuses or a viewer draws wrongly, or nil.
func (m *Model) Check() error {
	if len(m.Faces) == 0 {
		return fmt.Errorf("mesh: %q has no faces", m.Name)
	}
	if len(m.Faces) > MaxFaces {
		return fmt.Errorf("mesh: %q has %d faces, and a mesh may have %d", m.Name, len(m.Faces), MaxFaces)
	}
	if m.Skin != nil {
		s := m.Skin
		if len(s.Joints) == 0 {
			return fmt.Errorf("mesh: %q has a skin naming no joints", m.Name)
		}
		if len(s.Joints) > MaxJoints {
			return fmt.Errorf("mesh: %q names %d joints, and a skin may name %d", m.Name, len(s.Joints), MaxJoints)
		}
		if len(s.InverseBind) != len(s.Joints) {
			return fmt.Errorf("mesh: %q has %d joints and %d inverse bind matrices", m.Name, len(s.Joints), len(s.InverseBind))
		}
	}
	for i, f := range m.Faces {
		n := len(f.Positions)
		where := fmt.Sprintf("mesh: %q face %d", m.Name, i)
		if n < 3 {
			return fmt.Errorf("%s has %d vertices", where, n)
		}
		if n > 65536 {
			return fmt.Errorf("%s has %d vertices, and an index is 16 bits", where, n)
		}
		if f.Normals != nil && len(f.Normals) != n {
			return fmt.Errorf("%s has %d vertices and %d normals", where, n, len(f.Normals))
		}
		if f.UVs != nil && len(f.UVs) != n {
			return fmt.Errorf("%s has %d vertices and %d uvs", where, n, len(f.UVs))
		}
		if len(f.Indices) == 0 || len(f.Indices)%3 != 0 {
			return fmt.Errorf("%s has %d indices, which is not whole triangles", where, len(f.Indices))
		}
		for _, x := range f.Indices {
			if x < 0 || x >= n {
				return fmt.Errorf("%s has index %d, and %d vertices", where, x, n)
			}
		}
		for _, p := range f.Positions {
			for _, c := range p {
				if math.IsNaN(c) || math.IsInf(c, 0) {
					return fmt.Errorf("%s has a position that is not a number", where)
				}
			}
		}
		switch {
		case m.Skin == nil && f.Weights != nil:
			return fmt.Errorf("%s has weights and the model no skin", where)
		case m.Skin != nil && len(f.Weights) != n:
			return fmt.Errorf("%s has %d vertices and %d weight lists", where, n, len(f.Weights))
		}
		for v, ws := range f.Weights {
			if len(ws) == 0 || len(ws) > 4 {
				return fmt.Errorf("%s vertex %d has %d weights; it needs one to four", where, v, len(ws))
			}
			sum := 0.0
			for _, w := range ws {
				j := int(w[0])
				if float64(j) != w[0] || j < 0 || j >= len(m.Skin.Joints) {
					return fmt.Errorf("%s vertex %d names joint %v, and the skin has %d", where, v, w[0], len(m.Skin.Joints))
				}
				if w[1] < 0 || w[1] > 1 {
					return fmt.Errorf("%s vertex %d has weight %v", where, v, w[1])
				}
				sum += w[1]
			}
			if math.Abs(sum-1) > 0.01 {
				return fmt.Errorf("%s vertex %d's weights add up to %v, not 1", where, v, sum)
			}
		}
	}
	return nil
}

// Bounds is the box every position of every face is inside.
func (m *Model) Bounds() (lo, hi [3]float64) {
	lo = m.Faces[0].Positions[0]
	hi = lo
	for _, f := range m.Faces {
		for _, p := range f.Positions {
			for k := range 3 {
				lo[k] = math.Min(lo[k], p[k])
				hi[k] = math.Max(hi[k], p[k])
			}
		}
	}
	return lo, hi
}

// Triangles is how many triangles the model has, over every face.
func (m *Model) Triangles() int {
	n := 0
	for _, f := range m.Faces {
		n += len(f.Indices) / 3
	}
	return n
}
