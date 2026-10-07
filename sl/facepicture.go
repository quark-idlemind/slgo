package sl

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"

	"github.com/quark-idlemind/slgo/msg"
)

// ErrNoTexture is a face that wears no texture, so there is no picture
// on it. FacePicture wraps it with the face number.
var ErrNoTexture = errors.New("sl: the face has no texture")

// ErrPlanarFace is a face that maps its texture by planar projection,
// where the entry's offset, repeats and rotation are not the picture on
// it. FacePicture wraps it with the face number.
var ErrPlanarFace = errors.New("sl: the face is planar, so its offset, repeats and rotation are not the picture on it")

// ErrAnimatedFace is a face whose texture animation is running, so no
// one still picture is what it shows. FacePicture wraps it with the
// face number.
var ErrAnimatedFace = errors.New("sl: the face has a texture animation, so one still picture is not what it shows")

// Picture is the picture this face shows of tex.
//
// About the middle of the face the texture is turned by the rotation,
// tiled by the repeats and shifted by the offset, as SurfaceToTexture
// does, and the result is cropped to the face. One repeat, no offset and
// no rotation gives tex back, pixel for pixel. Each pixel is the nearest
// texel, and a side is never longer than MaxDecodeSize: a repeat that
// would pass it is drawn smaller and is still the whole face.
//
// A quarter turn (Rotation 8192) takes A B / C D to B D / A C, and a
// 20 by 10 texture to 10 by 20. A repeat of 2 lays two copies, centred.
// Why: doc/face-pictures.md#the-mapping
func (f Face) Picture(tex image.Image) (*image.NRGBA, error) {
	if tex == nil || tex.Bounds().Empty() {
		return nil, fmt.Errorf("sl: empty texture")
	}
	ss, st := float64(f.ScaleS), float64(f.ScaleT)
	if math.IsNaN(ss) || math.IsNaN(st) || math.IsInf(ss, 0) || math.IsInf(st, 0) {
		return nil, fmt.Errorf("sl: a face repeats its texture %g,%g times", f.ScaleS, f.ScaleT)
	}
	b := tex.Bounds()
	outW, outH := facePixels(b.Dx(), b.Dy(), f)
	dst := image.NewNRGBA(image.Rect(0, 0, outW, outH))
	sin, cos := math.Sincos(float64(f.RotationRad()))
	offS, offT := f.OffsetsF()
	fw, fh := float64(outW), float64(outH)
	for y := 0; y < outH; y++ {
		// T runs up the face and the image's rows run down the page,
		// so the top row is the high end of T.
		stY := 1 - (float64(y)+0.5)/fh
		for x := 0; x < outW; x++ {
			stX := (float64(x) + 0.5) / fw
			u, v := mapST(stX, stY, sin, cos, ss, st, float64(offS), float64(offT))
			dst.SetNRGBA(x, y, texel(tex, b, u, v))
		}
	}
	return dst, nil
}

// mapST is the viewer's xform: about the middle, turn, then repeats,
// then offset (llface.cpp xform, as Face.SurfaceToTexture).
func mapST(stX, stY, sin, cos, scaleS, scaleT, offS, offT float64) (u, v float64) {
	s, t := stX-0.5, stY-0.5
	s, t = s*cos+t*sin, -s*sin+t*cos
	return s*scaleS + offS + 0.5, t*scaleT + offT + 0.5
}

// facePixels is how many texels lie across the face and down it. A
// quarter turn swaps the texture's sides, and a repeat of zero
// collapses that side to one pixel.
func facePixels(w, h int, f Face) (int, int) {
	sin, cos := math.Sincos(float64(f.RotationRad()))
	fw, fh := float64(w), float64(h)
	ss, st := float64(f.ScaleS), float64(f.ScaleT)
	rawW := math.Hypot(cos*ss*fw, sin*st*fh)
	rawH := math.Hypot(sin*ss*fw, cos*st*fh)
	return fitFloat(rawW, rawH, MaxDecodeSize)
}

// fitFloat rounds a face to whole pixels, and shrinks it, keeping its
// shape, so that neither side passes maxSide.
func fitFloat(w, h float64, maxSide int) (int, int) {
	if math.IsNaN(w) || math.IsInf(w, 0) || w < 1 {
		w = 1
	}
	if math.IsNaN(h) || math.IsInf(h, 0) || h < 1 {
		h = 1
	}
	if w > float64(maxSide) || h > float64(maxSide) {
		s := math.Min(float64(maxSide)/w, float64(maxSide)/h)
		w *= s
		h *= s
	}
	nw, nh := int(math.Round(w)), int(math.Round(h))
	return min(max(nw, 1), maxSide), min(max(nh, 1), maxSide)
}

// texel is the nearest texel at a texture coordinate, wrapped. V of 1
// is the top row.
func texel(src image.Image, b image.Rectangle, u, v float64) color.NRGBA {
	w, h := b.Dx(), b.Dy()
	u -= math.Floor(u)
	v -= math.Floor(v)
	x := b.Min.X + int(u*float64(w))
	y := b.Min.Y + int((1-v)*float64(h))
	if x >= b.Max.X {
		x = b.Min.X
	}
	if y >= b.Max.Y {
		y = b.Min.Y
	}
	return color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
}

// FacePicture is the picture face number face of o shows: its texture,
// fetched with TextureImage, put through Face.Picture.
//
// A face with no texture is ErrNoTexture. A planar face is
// ErrPlanarFace, and one whose texture animation is running is
// ErrAnimatedFace, because the entry alone does not say what such a
// face shows. Each error names the face. For the texture as uploaded,
// read Faces and call TextureImage.
// Why: doc/face-pictures.md#which-faces-are-refused
func (w *Session) FacePicture(ctx context.Context, o *Object, face int) (image.Image, error) {
	if o == nil {
		return nil, fmt.Errorf("sl: nothing to look at")
	}
	seen, err := w.ObjectByID(ctx, o.ID, w.objectWait())
	if err != nil {
		return nil, err
	}
	faces, seen, _, err := w.appearance(ctx, o, seen)
	if err != nil {
		return nil, err
	}
	if face < 0 || face >= len(faces) {
		return nil, fmt.Errorf("sl: %s has no face %d; it has %d", o, face, len(faces))
	}
	f := faces[face]
	if err := faceRefusal(f, seen.TextureAnim, face, len(faces), o); err != nil {
		return nil, err
	}
	tex, err := w.TextureImage(ctx, f.Texture)
	if err != nil {
		return nil, fmt.Errorf("sl: face %d of %s: %w", face, o, err)
	}
	pic, err := f.Picture(tex)
	if err != nil {
		return nil, fmt.Errorf("sl: face %d of %s: %w", face, o, err)
	}
	return pic, nil
}

// faceRefusal is the error FacePicture gives for a face whose picture the
// entry does not say, or nil: no texture, planar mapping, or a running
// texture animation, in that order. n is the object's number of faces.
func faceRefusal(f Face, anim []byte, face, n int, o *Object) error {
	switch {
	case f.Texture == (msg.UUID{}):
		return fmt.Errorf("sl: face %d of %s: %w", face, o, ErrNoTexture)
	case f.Planar():
		return fmt.Errorf("sl: face %d of %s: %w", face, o, ErrPlanarFace)
	case animated(anim, face, n):
		return fmt.Errorf("sl: face %d of %s: %w", face, o, ErrAnimatedFace)
	}
	return nil
}

// FaceRefusals is every face of o and, for each, the error FacePicture
// would give for it before fetching anything: ErrNoTexture, ErrPlanarFace
// or ErrAnimatedFace wrapped as it wraps them, or nil. A caller that maps
// a point through a face's entry without its picture asks here which
// faces the entry alone does not describe.
// Why: doc/face-pictures.md#which-faces-are-refused
func (w *Session) FaceRefusals(ctx context.Context, o *Object) ([]Face, []error, error) {
	if o == nil {
		return nil, nil, fmt.Errorf("sl: nothing to look at")
	}
	seen, err := w.ObjectByID(ctx, o.ID, w.objectWait())
	if err != nil {
		return nil, nil, err
	}
	faces, seen, _, err := w.appearance(ctx, o, seen)
	if err != nil {
		return nil, nil, err
	}
	out := make([]error, len(faces))
	for i, f := range faces {
		out[i] = faceRefusal(f, seen.TextureAnim, i, len(faces), o)
	}
	return faces, out, nil
}
