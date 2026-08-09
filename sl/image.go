package sl

// Textures as pictures rather than as bytes.
//
// Everything else here deals in the asset as the grid stores it, which
// for a texture is a JPEG 2000 codestream and no use to a program that
// wants pixels. These four calls are the conversion, and they are kept
// apart from the rest so that what depends on a codec is obvious.
//
// # What this deliberately does not do
//
// It does not resize. Second Life takes a texture only if each side is
// a power of two (see textureSize), and an image that is not gets an
// error naming TextureDim rather than a silent rescale: which resampling
// a picture deserves is the caller's business, and a client that quietly
// halves somebody's artwork is worse than one that refuses it.

import (
	"bytes"
	"context"
	"fmt"
	"image"

	"github.com/mububoki/jpeg2000/j2k"

	"github.com/quark-idlemind/slgo/msg"
)

// LosslessArea is the size below which a texture is stored losslessly.
//
// It is the viewer's LL_IMAGE_REZ_LOSSLESS_CUTOFF squared: a small
// texture is usually a button or a bit of interface where the artefacts
// show and the bytes saved are few.
const LosslessArea = 128 * 128

// TextureOptions says how to compress. The zero value is what
// EncodeTexture does by default, and is meant to be left alone.
type TextureOptions struct {
	// Lossless stores every pixel exactly, at several times the size.
	// Images at or below LosslessArea are lossless anyway.
	Lossless bool

	// Ratio is how much smaller than the raw pixels to aim for, and
	// zero means DefaultRatio. Second Life's own textures sit around
	// 8:1 -- the stock plywood is 98282 bytes for 512x512 in three
	// components -- so that is what an unremarkable texture looks like
	// on this grid.
	Ratio float64
}

// DefaultRatio is the compression aimed for when nothing says
// otherwise. See TextureOptions.Ratio for where the number comes from.
const DefaultRatio = 8

// DecodeTexture turns a codestream into a picture.
func DecodeTexture(b []byte) (image.Image, error) {
	m, err := j2k.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("sl: decoding a texture of %d bytes: %w", len(b), err)
	}
	return m, nil
}

// EncodeTexture turns a picture into a codestream Second Life will
// take, or says why it will not.
//
// The size is checked before anything is compressed, since encoding a
// 300x200 image is work thrown away: the grid refuses it, and refuses
// it after charging.
func EncodeTexture(m image.Image, opt TextureOptions) ([]byte, error) {
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	if err := checkTextureDims(w, h); err != nil {
		return nil, fmt.Errorf("sl: %w", err)
	}

	// Five decomposition levels is what the viewer asks OpenJPEG for,
	// and it is what makes a texture fetchable at lower resolutions:
	// the grid serves a prefix of the codestream for a distant object,
	// and a stream with fewer levels has fewer prefixes to serve.
	opts := j2k.EncodeOptions{Levels: levelsFor(w, h)}
	if !opt.Lossless && w*h > LosslessArea {
		ratio := opt.Ratio
		if ratio == 0 {
			ratio = DefaultRatio
		}
		opts.Lossy = true
		opts.TargetRatio = ratio
		// Quality layers, coarsest first, so that a viewer reading part
		// of the stream gets a whole picture rather than part of one.
		opts.LayerRatios = []float64{ratio * 8, ratio * 4, ratio * 2, ratio}
	}

	var out bytes.Buffer
	if err := j2k.EncodeWithOptions(&out, m, opts); err != nil {
		return nil, fmt.Errorf("sl: encoding a %dx%d texture: %w", w, h, err)
	}
	return out.Bytes(), nil
}

// levelsFor is how many times the image can be halved, up to the five
// the viewer uses. A 4x4 texture cannot be halved five times, and
// asking is an error rather than a smaller number.
func levelsFor(w, h int) int {
	n := 0
	for n < 5 && w>>(n+1) > 0 && h>>(n+1) > 0 {
		n++
	}
	return n
}

// TextureImage fetches a texture and decodes it.
//
// The id is an ASSET id, not the id of an inventory item that points at
// one, and it need not be an asset this avatar owns: the content
// delivery network serves by id alone.
func (w *Session) TextureImage(ctx context.Context, id msg.UUID) (image.Image, error) {
	b, err := w.Texture(ctx, id)
	if err != nil {
		return nil, err
	}
	return DecodeTexture(b)
}

// UploadImage encodes a picture and uploads it as a new texture.
//
// It will not resize: an image whose sides are not powers of two is
// refused, with TextureDim to say what they would have to be.
func (w *Session) UploadImage(ctx context.Context, u Upload, m image.Image, opt TextureOptions) (*Item, *UploadResult, error) {
	if u.Type == 0 {
		u.Type = AssetTexture
	}
	body, err := EncodeTexture(m, opt)
	if err != nil {
		return nil, nil, err
	}
	return w.UploadAsset(ctx, u, body)
}
