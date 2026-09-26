package agent

import (
	"math"
	"math/rand"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// packer writes bits the way LLBitPack::bitPack does: most significant
// first, a value wider than a byte a byte at a time from its low byte,
// and the last byte padded with zeros at the bottom.
type packer struct {
	out  []byte
	load byte
	n    int
}

func (p *packer) put(v uint32, bits int) {
	for shift := 0; bits > 0; shift += 8 {
		d := min(bits, 8)
		bits -= d
		c := byte(v >> shift)
		for k := d - 1; k >= 0; k-- {
			p.load = p.load<<1 | (c>>k)&1
			if p.n++; p.n == 8 {
				p.out = append(p.out, p.load)
				p.load, p.n = 0, 0
			}
		}
	}
}

func (p *packer) bytes() []byte {
	if p.n > 0 {
		return append(p.out, p.load<<(8-p.n))
	}
	return p.out
}

// testPatch is one patch to encode: where it goes, its header, and its
// coefficients in the order they are sent.
type testPatch struct {
	x, y   int
	dc     float32
	rng    uint16
	quant  uint8 // the prequant, less 2: the high nibble of quant_wbits
	coeffs []int32
}

// encodeLand builds a LayerData body as code_patch_group_header,
// code_patch_header and code_patch lay one out (patch_code.cpp), with
// every value in 8 bits.
func encodeLand(size int, large bool, patches ...testPatch) []byte {
	const wbits = 8
	p := &packer{}
	p.put(uint32(size), 16)
	p.put(uint32(size), 8)
	p.put('L', 8)
	for _, t := range patches {
		p.put(uint32(t.quant)<<4|(wbits-2), 8)
		p.put(math.Float32bits(t.dc), 32)
		p.put(uint32(t.rng), 16)
		if large {
			p.put(uint32(t.x)<<16|uint32(t.y), 32)
		} else {
			p.put(uint32(t.x)<<5|uint32(t.y), 10)
		}
		last := -1
		for i, c := range t.coeffs {
			if c != 0 {
				last = i
			}
		}
		for i := 0; i < size*size; i++ {
			if i > last {
				p.put(1, 1) // 10: the rest are zero
				p.put(0, 1)
				break
			}
			c := t.coeffs[i]
			switch {
			case c == 0:
				p.put(0, 1)
			case c < 0:
				p.put(0x7, 3)
				p.put(uint32(-c), wbits)
			default:
				p.put(0x6, 3)
				p.put(uint32(c), wbits)
			}
		}
	}
	p.put(endOfPatches, 8)
	return p.bytes()
}

func landMessage(typ uint8, body []byte) *msg.LayerData {
	m := &msg.LayerData{}
	m.LayerID.Type = typ
	m.LayerData.Data = body
	return m
}

func nearly(a, b, by float64) bool { return math.Abs(a-b) <= by }

// TestAFlatPatchIsItsOffsetAndHalfItsRange: with only the DC term, the
// transform is zero and every height is decompress_patch's addval, dc
// plus range over two.
func TestAFlatPatchIsItsOffsetAndHalfItsRange(t *testing.T) {
	var tr Terrain
	if _, ok := tr.HeightAt(50, 90); ok {
		t.Fatal("a height is known before any land arrived")
	}
	tr.note(landMessage('L', encodeLand(16, false,
		testPatch{x: 3, y: 5, dc: 20.5, rng: 2})))

	for _, p := range [][2]float32{{48, 80}, {48.3, 80.7}, {55.5, 88.25}, {62.9, 94.9}} {
		h, ok := tr.HeightAt(p[0], p[1])
		if !ok || h != 21.5 {
			t.Errorf("height at %v = %v, %v; want 21.5", p, h, ok)
		}
	}
	// Past the last grid point of the patch the next one is needed, and
	// it has not come.
	if h, ok := tr.HeightAt(63.5, 85); ok {
		t.Errorf("height at 63.5,85 = %v, known without the patch to the east", h)
	}
	if h, ok := tr.HeightAt(10, 10); ok {
		t.Errorf("height at 10,10 = %v, known in a patch that never came", h)
	}
}

// TestAShapedPatchDecodesAsTheTransformSays: coefficients in the first
// zigzag places, checked against the inverse DCT written out in float64
// with the frequencies placed by hand.  The zigzag starts (0,0), (1,0),
// (0,1), (0,2), (1,1), with the first figure along x
// (build_decopy_matrix, patch_idct.cpp:73), so this also checks that x
// and y are the right way round.
func TestAShapedPatchDecodesAsTheTransformSays(t *testing.T) {
	const quant = 6 // prequant 8
	const dc, rng = 37.25, 12
	sent := []int32{-40, 150, -90, 0, 35}
	freq := map[[2]int]float64{{0, 0}: -40, {1, 0}: 150, {0, 1}: -90, {1, 1}: 35}

	var tr Terrain
	tr.note(landMessage('L', encodeLand(16, false,
		testPatch{x: 3, y: 5, dc: dc, rng: rng, quant: quant, coeffs: sent},
		// Neighbours to the east, north and north east, so the last
		// row and column of the first are readable.
		testPatch{x: 4, y: 5, dc: 30, rng: 2},
		testPatch{x: 3, y: 6, dc: 30, rng: 2},
		testPatch{x: 4, y: 6, dc: 30, rng: 2},
	)))

	prequant := quant + 2
	mult := float64(rng) / float64(int(1)<<prequant)
	addval := mult*float64(int(1)<<(prequant-1)) + dc
	c := func(k int) float64 {
		if k == 0 {
			return 1 / math.Sqrt2
		}
		return 1
	}
	varies := false
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			sum := 0.0
			for f, q := range freq {
				u, v := f[0], f[1]
				sum += c(u) * c(v) * q * float64(1+2*(u+v)) *
					math.Cos(float64(2*x+1)*float64(u)*math.Pi/32) *
					math.Cos(float64(2*y+1)*float64(v)*math.Pi/32)
			}
			want := sum/8*mult + addval
			got, ok := tr.HeightAt(float32(48+x), float32(80+y))
			if !ok || !nearly(float64(got), want, 1e-3) {
				t.Fatalf("height at %d,%d = %v, %v; want %.4f", 48+x, 80+y, got, ok, want)
			}
			if !nearly(want, dc+float64(rng)/2, 0.5) {
				varies = true
			}
		}
	}
	if !varies {
		t.Fatal("the patch is flat to within half a metre, so it tests nothing about the transform")
	}
}

// TestTheLargeLayerHasWideIDs: the M layer's patch ids are 32 bits, x in
// the high half (llsurface.cpp:858).
func TestTheLargeLayerHasWideIDs(t *testing.T) {
	var tr Terrain
	tr.note(landMessage('M', encodeLand(16, true,
		testPatch{x: 15, y: 2, dc: 4, rng: 2})))
	for _, p := range [][2]float32{{240, 32}, {250.5, 40.5}, {255.5, 46.5}, {256, 36}} {
		if h, ok := tr.HeightAt(p[0], p[1]); !ok || h != 5 {
			t.Errorf("height at %v = %v, %v; want 5", p, h, ok)
		}
	}
	if _, ok := tr.HeightAt(256.5, 36); ok {
		t.Error("a height is known outside the region")
	}
}

// TestA32PatchCoversFourCells: the viewer decodes patches of 32 as well
// as 16, and places them by the same 16 metre ids.
func TestA32PatchCoversFourCells(t *testing.T) {
	var tr Terrain
	tr.note(landMessage('L', encodeLand(32, false,
		testPatch{x: 2, y: 2, dc: 9, rng: 2})))
	for _, p := range [][2]float32{{32, 32}, {47.5, 47.5}, {62.5, 62.5}} {
		if h, ok := tr.HeightAt(p[0], p[1]); !ok || h != 10 {
			t.Errorf("height at %v = %v, %v; want 10", p, h, ok)
		}
	}
}

// TestABadPatchEndsTheBodyAndKeepsWhatCameBefore: the viewer stops at a
// patch id outside the region, and has already laid down the patches
// before it.
func TestABadPatchEndsTheBodyAndKeepsWhatCameBefore(t *testing.T) {
	var tr Terrain
	tr.note(landMessage('L', encodeLand(16, false,
		testPatch{x: 1, y: 1, dc: 1, rng: 2},
		testPatch{x: 20, y: 0, dc: 1, rng: 2},
		testPatch{x: 2, y: 2, dc: 1, rng: 2},
	)))
	if h, ok := tr.HeightAt(20, 20); !ok || h != 2 {
		t.Errorf("height in the patch before = %v, %v; want 2", h, ok)
	}
	if _, ok := tr.HeightAt(36, 36); ok {
		t.Error("the patch after the bad one was decoded")
	}
	if len(tr.Patches()) != 1 {
		t.Error("a body that would not all decode was not kept for a viewer")
	}

	// A body cut short keeps its whole patches, and does not read on.
	body := encodeLand(16, false,
		testPatch{x: 5, y: 5, dc: 1, rng: 2},
		testPatch{x: 6, y: 5, dc: 1, rng: 2, coeffs: []int32{3, 4, 5}})
	var cut Terrain
	cut.note(landMessage('L', body[:len(body)-4]))
	if h, ok := cut.HeightAt(85, 85); !ok || h != 2 {
		t.Errorf("height in the whole patch = %v, %v; want 2", h, ok)
	}
	if _, ok := cut.HeightAt(100, 85); ok {
		t.Error("the patch the body ended inside was decoded")
	}
}

// grid is a Terrain with every patch arrived and the heights set by f.
func grid(f func(x, y int) float32) *Terrain {
	tr := &Terrain{heights: make([]float32, RegionWidth*RegionWidth)}
	for i := range tr.have {
		tr.have[i] = true
	}
	for y := 0; y < RegionWidth; y++ {
		for x := 0; x < RegionWidth; x++ {
			tr.heights[y*RegionWidth+x] = f(x, y)
		}
	}
	return tr
}

// TestASquareIsSplitFromSouthWestToNorthEast: resolveHeightRegion
// (llsurface.cpp:948-984).  A square whose south west and north east
// corners are low and the other two high is a valley along that
// diagonal; split the other way, or read bilinearly, its centre would be
// a ridge or a saddle.
func TestASquareIsSplitFromSouthWestToNorthEast(t *testing.T) {
	tr := grid(func(x, y int) float32 {
		if (x == 11 && y == 10) || (x == 10 && y == 11) {
			return 1
		}
		return 0
	})
	for _, c := range []struct {
		x, y, want float32
	}{
		{10.5, 10.5, 0},     // on the diagonal
		{10.25, 10.75, 0.5}, // north west triangle: lb + dy(lt-lb) + dx(rt-lt)
		{10.75, 10.25, 0.5}, // south east: lb + dx(rb-lb) + dy(rt-rb)
		{10.2, 10.6, 0.4},
		{10.6, 10.2, 0.4},
		{10, 10, 0},
		{11, 10, 1},
	} {
		h, ok := tr.HeightAt(c.x, c.y)
		if !ok || !nearly(float64(h), float64(c.want), 1e-6) {
			t.Errorf("height at %v,%v = %v, %v; want %v", c.x, c.y, h, ok, c.want)
		}
	}
}

// TestHighestIsTheHighestAnywhere: against the ground sampled every
// centimetre, on rough land, for rectangles large and small.
func TestHighestIsTheHighestAnywhere(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	tr := grid(func(x, y int) float32 { return 30 + 4*r.Float32() })

	for i := 0; i < 40; i++ {
		w := r.Float32() * 240
		s := r.Float32() * 240
		e := w + r.Float32()*3
		n := s + r.Float32()*3
		got, ok := tr.Highest(w, s, e, n)
		if !ok {
			t.Fatalf("highest in %v,%v to %v,%v is not known", w, s, e, n)
		}
		sampled := float32(math.Inf(-1))
		for x := w; x <= e; x += 0.01 {
			for y := s; y <= n; y += 0.01 {
				h, _ := tr.HeightAt(x, y)
				sampled = max(sampled, h)
			}
			h, _ := tr.HeightAt(x, n)
			sampled = max(sampled, h)
		}
		// Sampling can only miss the top, and by no more than a
		// centimetre's worth of the steepest slope here.
		if got < sampled-1e-4 || got > sampled+0.1 {
			t.Errorf("highest in %v,%v to %v,%v = %v, sampled %v", w, s, e, n, got, sampled)
		}
	}

	h, _ := tr.HeightAt(100.3, 42.8)
	if got, ok := tr.Highest(100.3, 42.8, 100.3, 42.8); !ok || got != h {
		t.Errorf("highest at a point = %v, %v; want its height %v", got, ok, h)
	}
	if _, ok := tr.Highest(250, 10, 257, 12); ok {
		t.Error("a rectangle reaching into the next region is known")
	}
	if _, ok := tr.Highest(12, 10, 10, 12); ok {
		t.Error("a rectangle with west east of east is known")
	}
}

// TestTheHighestNeedsAllTheLand: a rectangle over a patch that has not
// arrived is not known, even where part of it has.
func TestTheHighestNeedsAllTheLand(t *testing.T) {
	var tr Terrain
	if _, ok := tr.Highest(1, 1, 2, 2); ok {
		t.Error("highest known before any land")
	}
	tr.note(landMessage('L', encodeLand(16, false,
		testPatch{x: 0, y: 0, dc: 1, rng: 2})))
	if h, ok := tr.Highest(1, 1, 2, 2); !ok || h != 2 {
		t.Errorf("highest inside the patch = %v, %v; want 2", h, ok)
	}
	if _, ok := tr.Highest(10, 10, 20, 12); ok {
		t.Error("highest known over a patch that never came")
	}
}

// TestACrossingForgetsTheHeights: the land of the region left describes
// somewhere else.
func TestACrossingForgetsTheHeights(t *testing.T) {
	var tr Terrain
	tr.note(landMessage('L', encodeLand(16, false,
		testPatch{x: 0, y: 0, dc: 1, rng: 2})))
	tr.forget()
	if _, ok := tr.HeightAt(5, 5); ok {
		t.Error("a height survived the crossing")
	}
	tr.note(landMessage('L', encodeLand(16, false,
		testPatch{x: 1, y: 0, dc: 1, rng: 2})))
	if _, ok := tr.HeightAt(5, 5); ok {
		t.Error("a patch of the region left came back with the next region's land")
	}
}

// TestWeatherIsNotGround: wind and cloud come on the same message.
func TestWeatherIsNotGround(t *testing.T) {
	var tr Terrain
	tr.note(landMessage('7', encodeLand(16, false,
		testPatch{x: 0, y: 0, dc: 1, rng: 2})))
	if _, ok := tr.HeightAt(5, 5); ok {
		t.Error("wind was read as land")
	}
}
