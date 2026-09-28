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
// It does not resize on its own. Second Life takes a texture only if
// each side is a power of two (see checkTextureDims), and an image that
// is not gets an error saying what TextureDim would make of it rather
// than a silent rescale: which resampling a picture deserves is the
// caller's business, and a client that quietly halves somebody's artwork
// is worse than one that refuses it. Resize, in resize.go, is for a
// caller that has decided.

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

// MaxDecodeTiles is the most tiles a codestream may be cut into and
// still be decoded.
//
// The decoder makes a record for every tile as it reads the header, so
// a header claiming a million tiles of four pixels costs over 200 MB
// before a pixel is read -- even to DecodeConfig, which is asked only
// for the size.  Its own ceiling is 1<<20.  The viewer's encoder writes
// one tile for the whole image (llimagej2coj.cpp sets no tile size), and
// 4096 is a tile of 64 pixels a side over the largest texture decoded.
const MaxDecodeTiles = 4096

// DecodeTexture turns a codestream into a picture.
//
// It is liberal where EncodeTexture is strict: any shape decodes, power
// of two or not, up to MaxDecodeSize on each side and MaxDecodeTiles
// tiles. A codestream claiming more is refused before anything is
// allocated for it, since the decoder sizes its buffers from the header
// and the header says whatever the bytes say. The size is checked in
// every SIZ, and again as the decoder's own parser reads it, so it is
// the size the decoder would have believed.
func DecodeTexture(b []byte) (image.Image, error) {
	if err := checkSIZs(b); err != nil {
		return nil, fmt.Errorf("sl: a texture of %d bytes %w", len(b), err)
	}
	c, err := j2k.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("sl: decoding a texture of %d bytes: %w", len(b), err)
	}
	if c.Width > MaxDecodeSize || c.Height > MaxDecodeSize {
		return nil, fmt.Errorf("sl: a texture of %d bytes claims to be %dx%d, "+
			"and nothing larger than %d a side is decoded", len(b), c.Width, c.Height, MaxDecodeSize)
	}
	m, err := j2k.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("sl: decoding a texture of %d bytes: %w", len(b), err)
	}
	return m, nil
}

// checkSIZs refuses a codestream any of whose SIZ markers claims more
// than MaxDecodeSize a side or MaxDecodeTiles tiles.
//
// Every SIZ, because the decoder takes each one it meets as the image
// afresh: a second in the main header, or one after a tile-part, where
// DecodeConfig has stopped reading.  The markers are walked by their
// lengths and the tile-parts by Psot, as the standard lays them out and
// the decoder reads them; a SIZ is read for as long as the decoder reads
// it, which is its 38 bytes and three a component whatever its length
// says.  Where the stream stops making sense the walk stops, and leaves
// the decoder to refuse it.
func checkSIZs(b []byte) error {
	if len(b) < 2 || b[0] != 0xff || b[1] != 0x4f {
		return nil
	}
	for i := 2; i+4 <= len(b); {
		at := i
		mk := uint16(b[i])<<8 | uint16(b[i+1])
		i += 2
		switch {
		case mk >= 0xff30 && mk <= 0xff3f:
			continue // a delimiter, with no segment
		case mk == 0xffd9:
			return nil // EOC
		}
		l := int(b[i])<<8 | int(b[i+1])
		switch mk {
		case 0xff51: // SIZ
			if i+38 > len(b) {
				return nil
			}
			if err := checkSIZ(b[i:]); err != nil {
				return err
			}
			comps := int(b[i+36])<<8 | int(b[i+37])
			i += max(l, 38+3*comps)
		case 0xff90: // SOT: the tile-part runs Psot bytes from its marker
			if i+8 > len(b) {
				return nil
			}
			psot := int(be32(b[i+4:]))
			if psot == 0 {
				return nil // to the end of the stream
			}
			i = at + psot
		default:
			i += max(l, 2) // the decoder reads a length below 2 and goes on
		}
	}
	return nil
}

// checkSIZ is checkSIZs for one SIZ segment, from its length on, with
// the decoder's own arithmetic for the size and the tiles.
func checkSIZ(siz []byte) error {
	w := int64(be32(siz[4:])) - int64(be32(siz[12:]))
	h := int64(be32(siz[8:])) - int64(be32(siz[16:]))
	if w > MaxDecodeSize || h > MaxDecodeSize {
		return fmt.Errorf("claims to be %dx%d, and nothing larger than %d a side is decoded",
			w, h, MaxDecodeSize)
	}
	tw, th := int64(be32(siz[20:])), int64(be32(siz[24:]))
	if w <= 0 || h <= 0 || tw <= 0 || th <= 0 {
		return nil // the decoder refuses it
	}
	across := (int64(be32(siz[4:])) - int64(be32(siz[28:])) + tw - 1) / tw
	down := (int64(be32(siz[8:])) - int64(be32(siz[32:])) + th - 1) / th
	if across > 0 && down > 0 && across*down > MaxDecodeTiles {
		return fmt.Errorf("is cut into %dx%d tiles of %dx%d, and nothing of more than %d tiles is decoded",
			across, down, tw, th, MaxDecodeTiles)
	}
	return nil
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
