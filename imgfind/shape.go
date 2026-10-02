package imgfind

import (
	"image"
	"math"
)

// Two thresholds, because one cannot see both figures.
//
// ringThreshold is dark enough that a circular button falls apart into
// its ring and the arrow drawn inside the ring. boxThreshold is light
// enough that the pale lower edge of a button stays in the same
// component as its darker top, so the box is the whole button and not
// only the arc along the top.
// Why: doc/imgfind.md#how-figures-are-read
const (
	ringThreshold = 170
	boxThreshold  = 205
)

const (
	figCircle = iota
	figArrow
	figBox
)

type figure struct {
	kind     int
	contents string
	x, y     int
	w, h     int
}

type comp struct {
	id     int
	n      int
	x0, y0 int
	x1, y1 int
}

func (c comp) w() int { return c.x1 - c.x0 + 1 }
func (c comp) h() int { return c.y1 - c.y0 + 1 }

func readFigures(img image.Image) []figure {
	w, h, lum := luminance(img)
	rings, ringLabel := components(lum, w, h, ringThreshold)
	boxes, _ := components(lum, w, h, boxThreshold)

	var out []figure
	var circles []comp
	for _, c := range rings {
		if isCircle(c, ringLabel, w) {
			circles = append(circles, c)
			out = append(out, figure{kind: figCircle, contents: "circle", x: c.x0, y: c.y0, w: c.w(), h: c.h()})
		}
	}
	for _, c := range rings {
		if dir, ok := arrowOf(c, circles, ringLabel, w); ok {
			out = append(out, figure{kind: figArrow, contents: dir, x: c.x0, y: c.y0, w: c.w(), h: c.h()})
		}
	}
	for _, c := range boxes {
		if isOutlinedBox(c, lum, w) {
			out = append(out, figure{kind: figBox, contents: "outlined box", x: c.x0, y: c.y0, w: c.w(), h: c.h()})
		}
	}
	return out
}

func components(lum []byte, w, h, thr int) ([]comp, []int) {
	ink := make([]byte, w*h)
	for i, v := range lum {
		if int(v) < thr {
			ink[i] = 1
		}
	}
	label := make([]int, w*h)
	var comps []comp
	dirs := [8][2]int{{-1, -1}, {-1, 0}, {-1, 1}, {0, -1}, {0, 1}, {1, -1}, {1, 0}, {1, 1}}
	stack := make([]int, 0, 1024)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if ink[i] == 0 || label[i] != 0 {
				continue
			}
			id := len(comps) + 1
			c := comp{id: id, x0: x, y0: y, x1: x, y1: y}
			stack = append(stack[:0], i)
			label[i] = id
			for len(stack) > 0 {
				p := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				c.n++
				px, py := p%w, p/w
				if px < c.x0 {
					c.x0 = px
				}
				if py < c.y0 {
					c.y0 = py
				}
				if px > c.x1 {
					c.x1 = px
				}
				if py > c.y1 {
					c.y1 = py
				}
				for _, d := range dirs {
					nx, ny := px+d[0], py+d[1]
					if nx < 0 || ny < 0 || nx >= w || ny >= h {
						continue
					}
					j := ny*w + nx
					if ink[j] == 1 && label[j] == 0 {
						label[j] = id
						stack = append(stack, j)
					}
				}
			}
			if c.n < 40 {
				continue
			}
			comps = append(comps, c)
		}
	}
	return comps, label
}

// isCircle reports a ring or a disk large enough not to be a letter.
// A ring has a steady radius and empty corners; a disk fills its box
// and still leaves the corners empty. Letters are about 30px in the
// sample HUD texture, which is not in the repository, and its rings are
// about 100, so 48 keeps them.
// Why: doc/imgfind.md#how-figures-are-read
func isCircle(c comp, label []int, stride int) bool {
	bw, bh := c.w(), c.h()
	if bw < 48 || bh < 48 {
		return false
	}
	aspect := float64(bw) / float64(bh)
	if aspect < 0.85 || aspect > 1.18 {
		return false
	}
	if cornerFraction(c, label, stride) > 0.08 {
		return false
	}
	if radialCV(c, label, stride) < 0.15 {
		return true
	}
	return float64(c.n)/float64(c.w()*c.h()) > 0.65
}

// arrowOf reports a component that comes to a point.
//
// The point is the end with less ink: the base of an arrow is the wide
// end. A component inside a circle is the mark drawn on that button.
// Anything else has to be larger than a letter and clearly tapered, so
// a stem of text is not an arrow.
// Why: doc/imgfind.md#how-figures-are-read
func arrowOf(c comp, circles []comp, label []int, stride int) (string, bool) {
	bw, bh := c.w(), c.h()
	if bw < 24 || bh < 24 {
		return "", false
	}
	aspect := float64(bw) / float64(bh)
	if aspect > 3.5 || aspect < 1/3.5 {
		return "", false
	}
	dir, _ := pointDirection(c, label, stride)
	inCircle := false
	for _, circ := range circles {
		if circ.id == c.id {
			return "", false
		}
		cx := circ.x0 + circ.w()/2
		cy := circ.y0 + circ.h()/2
		r := min(circ.w(), circ.h()) / 2
		dx := c.x0 + bw/2 - cx
		dy := c.y0 + bh/2 - cy
		if dx*dx+dy*dy <= (r*7/10)*(r*7/10) {
			inCircle = true
			break
		}
	}
	if dir == "" {
		return "", false
	}
	// A letter is smaller than a drawn arrow. The mark inside a circle
	// is allowed to be smaller, because the circle is what makes it a
	// symbol rather than a piece of text.
	if !inCircle && min(bw, bh) < 70 {
		return "", false
	}
	return dir, true
}

// pointDirection finds which way a figure points.
//
// The base of an arrow is the end whose edge the ink runs along. The
// point is the other end: a filled triangle has its long edge at the
// base, and a chevron drawn inside a round button does too, even
// though the two ends hold about the same number of pixels.
// Why: doc/imgfind.md#how-figures-are-read
func pointDirection(c comp, label []int, stride int) (string, float64) {
	left, right, top, bot := edgeFractions(c, label, stride)
	hRatio := sideRatio(left, right)
	vRatio := sideRatio(top, bot)
	if hRatio >= vRatio && hRatio >= 1.6 {
		if left > right {
			return "right arrow", hRatio
		}
		return "left arrow", hRatio
	}
	if vRatio > hRatio && vRatio >= 1.6 {
		if top > bot {
			return "down arrow", vRatio
		}
		return "up arrow", vRatio
	}
	return "", max(hRatio, vRatio)
}

func edgeFractions(c comp, label []int, stride int) (left, right, top, bot float64) {
	bw, bh := c.w(), c.h()
	band := max(2, min(bw, bh)/10)
	lHit, rHit, tHit, bHit := 0, 0, 0, 0
	for y := c.y0; y <= c.y1; y++ {
		l, r := false, false
		for k := 0; k < band; k++ {
			if c.x0+k <= c.x1 && label[y*stride+c.x0+k] == c.id {
				l = true
			}
			if c.x1-k >= c.x0 && label[y*stride+c.x1-k] == c.id {
				r = true
			}
		}
		if l {
			lHit++
		}
		if r {
			rHit++
		}
	}
	for x := c.x0; x <= c.x1; x++ {
		t, b := false, false
		for k := 0; k < band; k++ {
			if c.y0+k <= c.y1 && label[(c.y0+k)*stride+x] == c.id {
				t = true
			}
			if c.y1-k >= c.y0 && label[(c.y1-k)*stride+x] == c.id {
				b = true
			}
		}
		if t {
			tHit++
		}
		if b {
			bHit++
		}
	}
	if bh > 0 {
		left = float64(lHit) / float64(bh)
		right = float64(rHit) / float64(bh)
	}
	if bw > 0 {
		top = float64(tHit) / float64(bw)
		bot = float64(bHit) / float64(bw)
	}
	return left, right, top, bot
}

func sideRatio(a, b float64) float64 {
	if a < b {
		a, b = b, a
	}
	if b < 0.02 {
		b = 0.02
	}
	return a / b
}

// isOutlinedBox reports a button-shaped outline or a rectangular frame.
//
// A button is much wider than it is tall. A frame has ink along each
// edge and an empty middle, which is what a drawn rectangle is, and
// which a filled blob is not.
// Why: doc/imgfind.md#how-figures-are-read
func isOutlinedBox(c comp, lum []byte, stride int) bool {
	bw, bh := c.w(), c.h()
	// A solid bar fills its box. An outline, even a thick one, does not.
	fill := float64(c.n) / float64(bw*bh)
	if bw >= 90 && bh >= 28 && bh <= 90 && float64(bw)/float64(bh) >= 2.8 && c.n >= bw && fill < 0.85 {
		return true
	}
	if bw < 40 || bh < 30 {
		return false
	}
	if edgeCoverage(c, lum, stride, true, c.y0) < 0.55 ||
		edgeCoverage(c, lum, stride, true, c.y1-2) < 0.55 ||
		edgeCoverage(c, lum, stride, false, c.x0) < 0.55 ||
		edgeCoverage(c, lum, stride, false, c.x1-2) < 0.55 {
		return false
	}
	return interiorFraction(c, lum, stride) < 0.2
}

func cornerFraction(c comp, label []int, stride int) float64 {
	cs := max(2, min(c.w(), c.h())/6)
	total, ink := 0, 0
	corners := [4][2]int{{c.x0, c.y0}, {c.x1 - cs + 1, c.y0}, {c.x0, c.y1 - cs + 1}, {c.x1 - cs + 1, c.y1 - cs + 1}}
	for _, p := range corners {
		for y := 0; y < cs; y++ {
			for x := 0; x < cs; x++ {
				total++
				if label[(p[1]+y)*stride+p[0]+x] == c.id {
					ink++
				}
			}
		}
	}
	if total == 0 {
		return 1
	}
	return float64(ink) / float64(total)
}

func radialCV(c comp, label []int, stride int) float64 {
	cx := float64(c.x0+c.x1) / 2
	cy := float64(c.y0+c.y1) / 2
	var sum, sum2 float64
	n := 0
	for y := c.y0; y <= c.y1; y++ {
		for x := c.x0; x <= c.x1; x++ {
			if label[y*stride+x] != c.id {
				continue
			}
			r := math.Hypot(float64(x)-cx, float64(y)-cy)
			sum += r
			sum2 += r * r
			n++
		}
	}
	if n == 0 {
		return 1
	}
	mean := sum / float64(n)
	if mean < 1 {
		return 1
	}
	v := sum2/float64(n) - mean*mean
	if v < 0 {
		v = 0
	}
	return math.Sqrt(v) / mean
}

// edgeCoverage is how much of one side of the box is dark, in a band a
// few pixels deep. horiz is a horizontal side (the top or the bottom)
// and at is its outer coordinate.
func edgeCoverage(c comp, lum []byte, stride int, horiz bool, at int) float64 {
	// The component was built at boxThreshold. Use the same cutoff so
	// the edge is the outline and not a lighter halo.
	const thr = boxThreshold
	hit, tot := 0, 0
	if horiz {
		y0 := max(at, c.y0)
		y1 := min(at+3, c.y1+1)
		for x := c.x0; x <= c.x1; x++ {
			tot++
			for y := y0; y < y1; y++ {
				if int(lum[y*stride+x]) < thr {
					hit++
					break
				}
			}
		}
	} else {
		x0 := max(at, c.x0)
		x1 := min(at+3, c.x1+1)
		for y := c.y0; y <= c.y1; y++ {
			tot++
			for x := x0; x < x1; x++ {
				if int(lum[y*stride+x]) < thr {
					hit++
					break
				}
			}
		}
	}
	if tot == 0 {
		return 0
	}
	return float64(hit) / float64(tot)
}

func interiorFraction(c comp, lum []byte, stride int) float64 {
	const thr = boxThreshold
	ix0 := c.x0 + c.w()/5
	iy0 := c.y0 + c.h()/5
	ix1 := c.x1 - c.w()/5
	iy1 := c.y1 - c.h()/5
	if ix1 <= ix0 || iy1 <= iy0 {
		return 1
	}
	ink, tot := 0, 0
	for y := iy0; y <= iy1; y++ {
		for x := ix0; x <= ix1; x++ {
			tot++
			if int(lum[y*stride+x]) < thr {
				ink++
			}
		}
	}
	if tot == 0 {
		return 1
	}
	return float64(ink) / float64(tot)
}

// luminance is per-pixel darkness, 0 for white. On a dark page the
// values are inverted, so ink is the low end either way and the
// thresholds above stay put.
func luminance(img image.Image) (w, h int, lum []byte) {
	b := img.Bounds()
	w, h = b.Dx(), b.Dy()
	lum = make([]byte, w*h)
	switch m := img.(type) {
	case *image.RGBA:
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := m.PixOffset(b.Min.X+x, b.Min.Y+y)
				lum[y*w+x] = byte((3*int(m.Pix[i]) + 6*int(m.Pix[i+1]) + int(m.Pix[i+2])) / 10)
			}
		}
	case *image.NRGBA:
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := m.PixOffset(b.Min.X+x, b.Min.Y+y)
				lum[y*w+x] = byte((3*int(m.Pix[i]) + 6*int(m.Pix[i+1]) + int(m.Pix[i+2])) / 10)
			}
		}
	case *image.Gray:
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				lum[y*w+x] = m.GrayAt(b.Min.X+x, b.Min.Y+y).Y
			}
		}
	default:
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				lum[y*w+x] = byte((3*int(r>>8) + 6*int(g>>8) + int(bl>>8)) / 10)
			}
		}
	}
	if w == 0 || h == 0 {
		return w, h, lum
	}
	bg := (int(lum[0]) + int(lum[w-1]) + int(lum[(h-1)*w]) + int(lum[h*w-1])) / 4
	if bg < 128 {
		for i, v := range lum {
			lum[i] = 255 - v
		}
	}
	return w, h, lum
}
