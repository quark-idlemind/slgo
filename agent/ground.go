package agent

// The land as heights.  A LayerData body is a group header and then
// patches, each a square block of DCT coefficients -- 16 on a side on
// Second Life -- quantised and bit packed.  This decodes them as Firestorm does -- llmessage/patch_code.cpp
// for the bits, patch_idct.cpp for the transform -- and reads a height
// between the grid points as newview/llsurface.cpp does.
// Why: doc/ground.md

import (
	"errors"
	"fmt"
	"math"
)

const (
	// groundPatch is how far apart patch ids place their patches, in
	// metres: WORLD_PATCH_SIZE (newview/llworld.cpp:82), whatever size
	// the patches themselves are.
	groundPatch = 16

	// groundPatches is how many of those a region is across.  A patch
	// placed further out is refused, as the viewer refuses one
	// (llsurface.cpp:870); slgo takes every region to be RegionWidth.
	groundPatches = RegionWidth / groundPatch

	// endOfPatches is the quant_wbits value that ends a body
	// (patch_dct.h).
	endOfPatches = 97
)

// groundBits reads a packed body the way LLBitPack::bitUnpack does
// (llcommon/llbitpack.h:141): most significant bit first, and a value
// wider than a byte a byte at a time, low byte first.
type groundBits struct {
	body []byte
	next int
	load byte
	left int
}

var errGroundShort = errors.New("the body ends inside a patch")

func (r *groundBits) read(n int) (uint32, error) {
	var v uint32
	for shift := 0; n > 0; shift += 8 {
		d := min(n, 8)
		n -= d
		var c uint32
		for ; d > 0; d-- {
			if r.left == 0 {
				if r.next >= len(r.body) {
					return 0, errGroundShort
				}
				r.load = r.body[r.next]
				r.next++
				r.left = 8
			}
			c = c<<1 | uint32(r.load>>7)
			r.load <<= 1
			r.left--
		}
		v |= c << shift
	}
	return v, nil
}

// patchTables are the viewer's decompression tables for one patch size
// (patch_idct.cpp:40-135).
type patchTables struct {
	size    int
	dequant []float32
	icos    []float32
	decopy  []int
}

var (
	patchTables16 = newPatchTables(16)
	patchTables32 = newPatchTables(32)
)

func newPatchTables(size int) *patchTables {
	t := &patchTables{
		size:    size,
		dequant: make([]float32, size*size),
		icos:    make([]float32, size*size),
		decopy:  make([]int, size*size),
	}
	for j := 0; j < size; j++ {
		for i := 0; i < size; i++ {
			t.dequant[j*size+i] = 1 + 2*float32(i+j)
		}
	}
	oosob := float32(math.Pi) * 0.5 / float32(size)
	for u := 0; u < size; u++ {
		for n := 0; n < size; n++ {
			t.icos[u*size+n] = float32(math.Cos(float64((2*float32(n) + 1) * float32(u) * oosob)))
		}
	}
	// The zigzag the coefficients are sent in, from the corner out.
	i, j, count := 0, 0, 0
	diag, right := false, true
	for i < size && j < size {
		t.decopy[j*size+i] = count
		count++
		switch {
		case !diag && right:
			if i < size-1 {
				i++
			} else {
				j++
			}
			right, diag = false, true
		case !diag:
			if j < size-1 {
				j++
			} else {
				i++
			}
			right, diag = true, true
		case right:
			i++
			j--
			if i == size-1 || j == 0 {
				diag = false
			}
		default:
			i--
			j++
			if i == 0 || j == size-1 {
				diag = false
			}
		}
	}
	return t
}

// ooSqrt2 is the viewer's OO_SQRT2 (llmath.h:62).
const ooSqrt2 = float32(0.7071067811865475244008443621049)

// decompress turns one patch's coefficients into heights, row by row
// from the south west: decompress_patch (patch_idct.cpp:590).
func (t *patchTables) decompress(cp []int32, rng uint16, quantWBits uint8, dc float32) []float32 {
	size := t.size
	block := make([]float32, size*size)
	for k := range block {
		block[k] = float32(cp[t.decopy[k]]) * t.dequant[k]
	}
	temp := make([]float32, size*size)
	for col := 0; col < size; col++ {
		for n := 0; n < size; n++ {
			total := ooSqrt2 * block[col]
			for u := 1; u < size; u++ {
				total += block[u*size+col] * t.icos[u*size+n]
			}
			temp[n*size+col] = total
		}
	}
	oosob := 2 / float32(size)
	for line := 0; line < size; line++ {
		ls := line * size
		for n := 0; n < size; n++ {
			total := ooSqrt2 * temp[ls]
			for u := 1; u < size; u++ {
				total += temp[ls+u] * t.icos[u*size+n]
			}
			block[ls+n] = total * oosob
		}
	}

	prequant := int(quantWBits>>4) + 2
	mult := float32(rng) / float32(int(1)<<prequant)
	addval := mult*float32(int(1)<<(prequant-1)) + dc
	for k := range block {
		block[k] = block[k]*mult + addval
	}
	return block
}

// decodeLand reads one land body and hands each patch to put, with
// where its south west corner goes in patches.  large is the M layer,
// whose patch ids are 32 bits, x in the high half (llsurface.cpp:858).
//
// It stops at the first thing it cannot read, and the patches before
// that stand, as they do in the viewer.
func decodeLand(body []byte, large bool, put func(px, py int, size int, h []float32)) error {
	r := &groundBits{body: body}
	if _, err := r.read(16); err != nil { // stride, which the viewer overrides
		return err
	}
	size, err := r.read(8)
	if err != nil {
		return err
	}
	if _, err := r.read(8); err != nil { // layer type, taken from the message instead
		return err
	}
	var tables *patchTables
	switch size {
	case 16:
		tables = patchTables16
	case 32:
		tables = patchTables32
	default:
		return fmt.Errorf("patches of %d, where the viewer decodes 16 and 32", size)
	}

	cp := make([]int32, tables.size*tables.size)
	for {
		qw, err := r.read(8)
		if err != nil {
			return err
		}
		if qw == endOfPatches {
			return nil
		}
		dc, err := r.read(32)
		if err != nil {
			return err
		}
		rng, err := r.read(16)
		if err != nil {
			return err
		}
		var px, py int
		if large {
			ids, err := r.read(32)
			if err != nil {
				return err
			}
			px, py = int(ids>>16), int(ids&0xffff)
		} else {
			ids, err := r.read(10)
			if err != nil {
				return err
			}
			px, py = int(ids>>5), int(ids&0x1f)
		}
		if px >= groundPatches || py >= groundPatches {
			return fmt.Errorf("patch %d,%d is outside a %d metre region", px, py, RegionWidth)
		}
		if err := readCoefficients(r, cp, int(qw&0xf)+2); err != nil {
			return err
		}
		put(px, py, tables.size,
			tables.decompress(cp, uint16(rng), uint8(qw), math.Float32frombits(dc)))
	}
}

// readCoefficients is decode_patch (patch_code.cpp:312): 0 is a zero,
// 10 ends the patch, 11 is a value, its sign bit and then wbits of it.
func readCoefficients(r *groundBits, cp []int32, wbits int) error {
	for i := range cp {
		cp[i] = 0
	}
	for i := range cp {
		b, err := r.read(1)
		if err != nil {
			return err
		}
		if b == 0 {
			continue
		}
		if b, err = r.read(1); err != nil {
			return err
		}
		if b == 0 {
			return nil
		}
		neg, err := r.read(1)
		if err != nil {
			return err
		}
		v, err := r.read(wbits)
		if err != nil {
			return err
		}
		cp[i] = int32(v)
		if neg == 1 {
			cp[i] = -cp[i]
		}
	}
	return nil
}

// decode lays one land body into the height grid.  Called with the lock
// held.
func (t *Terrain) decode(typ uint8, body []byte) {
	if t.heights == nil {
		t.heights = make([]float32, RegionWidth*RegionWidth)
	}
	// An error ends the body; what came before it is kept.
	_ = decodeLand(body, typ == layerLandVar, func(px, py, size int, h []float32) {
		for j := 0; j < size; j++ {
			y := py*groundPatch + j
			if y >= RegionWidth {
				break
			}
			for i := 0; i < size; i++ {
				x := px*groundPatch + i
				if x >= RegionWidth {
					break
				}
				t.heights[y*RegionWidth+x] = h[j*size+i]
				t.have[(y/groundPatch)*groundPatches+x/groundPatch] = true
			}
		}
	})
}

// HeightAt is the height of the ground at a point in the region, in
// metres from its south west corner, and whether the land there has
// arrived.
//
// Between the grid points it is read as the viewer reads it
// (LLSurface::resolveHeightRegion, llsurface.cpp:927): each square is
// split into two triangles from its south west corner to its north east
// one, and the ground is flat across each.  Past the last grid point, at
// 255 to 256 metres, it is held level, where the viewer would read the
// edge of the region beyond.
func (t *Terrain) HeightAt(x, y float32) (float32, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.heightLocked(x, y)
}

func (t *Terrain) heightLocked(x, y float32) (float32, bool) {
	if t.heights == nil || !onGround(x) || !onGround(y) {
		return 0, false
	}
	left, bottom := min(int(x), RegionWidth-1), min(int(y), RegionWidth-1)
	right, top := left+1, bottom+1
	if right > RegionWidth-1 {
		right = left
	}
	if top > RegionWidth-1 {
		top = bottom
	}
	if !t.haveLocked(left, bottom) || !t.haveLocked(right, bottom) ||
		!t.haveLocked(left, top) || !t.haveLocked(right, top) {
		return 0, false
	}
	lb := t.heights[bottom*RegionWidth+left]
	rb := t.heights[bottom*RegionWidth+right]
	lt := t.heights[top*RegionWidth+left]
	rt := t.heights[top*RegionWidth+right]
	dx, dy := x-float32(left), y-float32(bottom)
	if dy > dx {
		return lb + dy*(lt-lb) + dx*(rt-lt), true
	}
	return lb + dx*(rb-lb) + dy*(rt-rb), true
}

func onGround(v float32) bool { return v >= 0 && v <= RegionWidth }

func (t *Terrain) haveLocked(x, y int) bool {
	return t.have[(y/groundPatch)*groundPatches+x/groundPatch]
}

// Highest is the highest the ground comes anywhere in a rectangle of
// the region, and whether all of the land under it has arrived.  A
// rectangle that reaches outside the region is not known: the ground
// there is another region's.
//
// It is exact for the ground HeightAt reads.  That ground is flat on
// each triangle, so its highest point on the rectangle is at a corner
// of the rectangle, a grid point inside it, or where one of the
// rectangle's edges crosses a grid line or a triangle's diagonal -- and
// those are the points asked about.
func (t *Terrain) Highest(west, south, east, north float32) (float32, bool) {
	if !(west <= east && south <= north) || !onGround(west) || !onGround(east) ||
		!onGround(south) || !onGround(north) {
		return 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	best := float32(math.Inf(-1))
	known := true
	at := func(x, y float32) {
		h, ok := t.heightLocked(x, y)
		if !ok {
			known = false
		} else if h > best {
			best = h
		}
	}
	xs := groundLines(west, east)
	ys := groundLines(south, north)
	for _, y := range ys {
		for _, x := range xs {
			if at(x, y); !known {
				return 0, false
			}
		}
	}
	// The diagonals are the lines x - y = k, for whole k.
	for _, y := range []float32{south, north} {
		for k := math.Floor(float64(west - y)); float32(k)+y <= east; k++ {
			if x := float32(k) + y; x > west && x < east {
				at(x, y)
			}
		}
	}
	for _, x := range []float32{west, east} {
		for k := math.Floor(float64(x - north)); x-float32(k) >= south; k++ {
			if y := x - float32(k); y > south && y < north {
				at(x, y)
			}
		}
	}
	if !known {
		return 0, false
	}
	return best, true
}

// groundLines is the two ends of a span and every whole metre between.
func groundLines(from, to float32) []float32 {
	out := []float32{from}
	for v := float32(math.Floor(float64(from))) + 1; v < to; v++ {
		out = append(out, v)
	}
	if to != from {
		out = append(out, to)
	}
	return out
}
