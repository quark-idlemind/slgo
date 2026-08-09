package sl

// Getting a picture to a size Second Life will take.
//
// The grid's rule is narrow -- each side a power of two, at most 2048 --
// and almost nothing anybody wants to upload already satisfies it. So
// something has to resample, and the two decisions in resampling are
// which way to round and which filter to do it with.
//
// Both are the caller's. There is no one right answer: a photograph
// rounded down loses detail nobody misses, a screenshot of text rounded
// down becomes unreadable, and a pixel-art texture wants
// NearestNeighbor where everything else wants Lanczos. So Resize asks,
// with defaults that suit a photograph because that is the common case.
//
// The one thing that is not a choice is the ceiling. Over 2048 a
// dimension comes down to 2048 whatever the rounding says, because the
// alternative is an image the grid refuses.

import (
	"fmt"
	"image"

	"github.com/disintegration/imaging"
)

// Filter is a resampling filter.
//
// It is imaging's own type, so every filter that package offers works
// here and no list needs keeping in step: NearestNeighbor, Box, Linear,
// Hermite, MitchellNetravali, CatmullRom, BSpline, Gaussian, Bartlett,
// Lanczos, Hann, Hamming, Blackman, Welch and Cosine, or one of your
// own made with imaging.ResampleFilter.
type Filter = imaging.ResampleFilter

// DefaultFilter is what Resize uses when none is named.
//
// Lanczos is imaging's recommendation for photographic images and is
// the slowest of the sensible ones, which is the right trade here: a
// texture is resized once and looked at for as long as it exists.
// NearestNeighbor is the one to reach for deliberately, for pixel art
// that any smoothing ruins.
var DefaultFilter = imaging.Lanczos

// Rounding says which power of two a dimension goes to.
type Rounding int

const (
	// RoundNearest is the viewer's own rule, biased downwards: up only
	// past 1.75x the power of two below. See TextureDim.
	RoundNearest Rounding = iota

	// RoundUp never loses detail and never shrinks an image, at the
	// cost of a texture as much as twice the size in each direction.
	RoundUp

	// RoundDown never grows one. It is the cheaper choice and the
	// lossier: 1023 becomes 512.
	RoundDown
)

func (r Rounding) String() string {
	switch r {
	case RoundUp:
		return "up"
	case RoundDown:
		return "down"
	}
	return "nearest"
}

// ResizeOptions says how to reach a size the grid will take. The zero
// value rounds each side to the nearest power of two, the way the
// viewer does, using DefaultFilter.
type ResizeOptions struct {
	// Filter resamples. Nil means DefaultFilter.
	Filter *Filter

	// Horizontal and Vertical round each side, and are separate
	// because an image is not obliged to want the same of both: a wide
	// banner rounded up in one direction and down in the other is a
	// reasonable thing to ask for.
	Horizontal Rounding
	Vertical   Rounding
}

// TextureSizeFor is the size Resize would produce, without doing the
// work. Useful for saying what will happen before it happens.
func TextureSizeFor(w, h int, o ResizeOptions) (int, int) {
	return roundDim(w, o.Horizontal), roundDim(h, o.Vertical)
}

// roundDim takes one dimension to a power of two.
//
// The ceiling is applied last and applies to every rounding: over
// MaxTextureSize the grid refuses the image outright, so rounding up to
// 4096 would only mean failing later.
func roundDim(n int, r Rounding) int {
	if n < 1 {
		n = 1
	}
	var out int
	switch r {
	case RoundUp:
		out = 1
		for out < n {
			out <<= 1
		}
	case RoundDown:
		out = 1
		for out<<1 <= n {
			out <<= 1
		}
	default:
		out = TextureDim(n)
	}
	if out > MaxTextureSize {
		out = MaxTextureSize
	}
	return out
}

// Resize returns m at a size Second Life will take.
//
// An image already at an acceptable size is returned untouched, so
// resizing what does not need it costs nothing and changes nothing --
// resampling an image to its own dimensions is not free and is not
// lossless.
func Resize(m image.Image, o ResizeOptions) image.Image {
	b := m.Bounds()
	w, h := TextureSizeFor(b.Dx(), b.Dy(), o)
	if w == b.Dx() && h == b.Dy() {
		return m
	}
	filter := DefaultFilter
	if o.Filter != nil {
		filter = *o.Filter
	}
	return imaging.Resize(m, w, h, filter)
}

// EncodeResized resizes a picture and encodes it, which is the whole
// path from a file somebody has to bytes the grid will take.
//
// It says what it did: a caller that resized without meaning to should
// be able to find out, and the sizes are the only evidence.
func EncodeResized(m image.Image, r ResizeOptions, t TextureOptions) (body []byte, resized image.Image, err error) {
	resized = Resize(m, r)
	body, err = EncodeTexture(resized, t)
	if err != nil {
		b := m.Bounds()
		return nil, resized, fmt.Errorf("%w (from %dx%d, rounding %s and %s)",
			err, b.Dx(), b.Dy(), r.Horizontal, r.Vertical)
	}
	return body, resized, nil
}
