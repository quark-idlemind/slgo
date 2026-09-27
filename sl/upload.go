package sl

// Uploading a file as a new inventory item.
//
// This is the one asset path that costs money, and the only one where
// the grid makes the item rather than the client: SaveScript and
// SaveNotecard write to an item that already exists, and CreateItem
// makes an item with no asset behind it.  Neither shape works for a
// texture, because there is no capability that fills in a texture
// somebody else created -- the bytes and the item arrive together or
// not at all.
//
// What the grid wants is the two step upload every capability uses,
// with a description that includes what is being paid.  The fee is the
// part worth care: the simulator checks expected_upload_cost against
// its own price and refuses a request that names the wrong one, so a
// caller that guesses is told the right answer rather than charged a
// surprise.  See UploadCost.

import (
	"bytes"
	"context"
	"fmt"

	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// UploadCost is what Second Life charges for a file upload, and what
// UploadFee names for anything but a texture over LargeTextureArea.
//
// It is a constant here and a setting there.  The grid publishes the
// real price in its economy data, which this package does not read, so
// a price change shows up as a refusal naming the right figure rather
// than as a wrong bill.  Set Upload.Cost when that happens.
const UploadCost = 10

// UploadCap is the capability a file upload goes through.
const UploadCap = "NewFileAgentInventory"

// Upload says what to make from a file's bytes.
//
// The zero value is not usable: an upload needs at least a name and a
// type, since the grid decides nothing for itself here.
type Upload struct {
	Name string
	Desc string

	// Type is what the bytes are.  Texture, Sound and Animation are
	// what the grid charges for and what this has been tried with.
	Type AssetType

	// InvType is what kind of inventory entry to make, which is not
	// always the asset type -- a mesh is an object in inventory.  Zero
	// means the one that goes with Type.
	InvType AssetType

	// Folder is where the item lands.  Zero means the system folder
	// for the type, which is also what the grid would choose, named
	// explicitly so that the item can be read back afterwards.
	Folder msg.UUID

	// NextOwner, Group and Everyone are the permissions the new item
	// carries.  NextOwner zero means PermAll: an upload of one's own
	// file, kept unrestricted unless the caller says otherwise.
	NextOwner uint32
	Group     uint32
	Everyone  uint32

	// Cost is what the caller expects to pay, and zero means UploadFee
	// for the texture's size -- UploadCost for anything that is not a
	// texture.  It is sent, not merely believed: naming a figure the
	// simulator disagrees with is refused.
	Cost int
}

// systemFolder is where the grid files each kind of upload, by the name
// of the folder rather than its preferred-type number, because that is
// what Folder looks one up by.
var systemFolder = map[AssetType]string{
	AssetTexture:   "Textures",
	AssetSound:     "Sounds",
	AssetAnimation: "Animations",
	AssetMesh:      "Objects",
}

// inventoryNames are the words the capability wants for inventory_type,
// from the viewer's LLInventoryType::lookup.  Only the types an upload
// can make are here; anything else has to be named by the caller.
var inventoryNames = map[AssetType]string{
	AssetTexture:   "texture",
	AssetSound:     "sound",
	AssetAnimation: "animation",
	AssetMesh:      "object",
}

// UploadAsset creates a new inventory item from a file's bytes.
//
// It returns the item as inventory holds it, having found it there:
// the capability answers with an id, and an id is not evidence the item
// exists to anything that goes looking for it by folder.  The
// UploadResult is returned even when the readback fails, since the
// asset is uploaded and paid for by then and the caller has a right to
// know its id.
func (w *Session) UploadAsset(ctx context.Context, u Upload, body []byte) (*Item, *UploadResult, error) {
	if u.Name == "" {
		return nil, nil, fmt.Errorf("sl: an upload needs a name")
	}
	if len(body) == 0 {
		return nil, nil, fmt.Errorf("sl: %s: nothing to upload", u.Name)
	}
	assetWord, ok := assetNames[u.Type]
	if !ok {
		return nil, nil, fmt.Errorf("sl: %s has no name in the protocol, so it cannot be uploaded", u.Type)
	}
	invType := u.InvType
	if invType == 0 {
		invType = u.Type
	}
	invWord, ok := inventoryNames[invType]
	if !ok {
		return nil, nil, fmt.Errorf(
			"sl: nothing here knows what kind of inventory entry a %s makes; set InvType", invType)
	}
	// A texture's dimensions decide both whether the grid will take it
	// and what it charges, so they are read here rather than guessed.
	texW, texH := 0, 0
	if u.Type == AssetTexture {
		w, h, err := TextureDims(body)
		if err != nil {
			return nil, nil, fmt.Errorf("sl: %s: %w", u.Name, err)
		}
		texW, texH = w, h
	}

	folder := u.Folder
	if folder.IsZero() {
		name, ok := systemFolder[u.Type]
		if !ok {
			return nil, nil, fmt.Errorf("sl: nothing here knows which folder a %s is filed in; set Folder", u.Type)
		}
		id, err := w.Folder(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		folder = id
	}

	next := u.NextOwner
	if next == 0 {
		next = PermAll
	}
	cost := u.Cost
	if cost == 0 {
		cost = UploadFee(texW, texH)
	}

	res, err := w.upload(ctx, UploadCap, map[string]any{
		"folder_id":      llsd.UUID(folder.String()),
		"asset_type":     assetWord,
		"inventory_type": invWord,
		"name":           u.Name,
		"description":    u.Desc,
		// LLSD has one integer type and it is signed 32-bit, which is
		// how the viewer sends a mask too: the bits are what matter
		// and the sign is read by nobody.  The round trip through
		// int32 is what makes a mask with the top bit set encode as
		// the negative the format can hold rather than failing.
		"next_owner_mask":      int(int32(next)),
		"group_mask":           int(int32(u.Group)),
		"everyone_mask":        int(int32(u.Everyone)),
		"expected_upload_cost": cost,
	}, body)
	if err != nil {
		return nil, nil, err
	}
	if res.NewItem.IsZero() {
		// The second half refuses the same way the first does -- 200,
		// a state of "failure", and the reason in a field -- and the
		// reasons are worth reading rather than burying in a dump of
		// LLSD: "Invalid width: Value not a power of 2" is the whole
		// answer to why a texture would not upload.
		if res.Message != "" {
			return nil, res, fmt.Errorf("sl: %s: the grid refused the asset: %s", u.Name, res.Message)
		}
		return nil, res, fmt.Errorf("sl: %s: %s named no new inventory item: %s",
			u.Name, UploadCap, snippet(res.Body))
	}

	// Read it back.  An id from a capability says the asset server
	// took it, and says nothing about inventory having caught up.
	items, err := w.FolderItems(ctx, folder)
	if err != nil {
		return nil, res, fmt.Errorf("sl: %s uploaded as %s, but reading %s back failed: %w",
			u.Name, res.NewItem, u.Name, err)
	}
	for _, it := range items {
		if it.ID == res.NewItem {
			return it, res, nil
		}
	}
	return nil, res, fmt.Errorf("sl: %s uploaded as item %s, asset %s, but it is not in the folder it was uploaded to",
		u.Name, res.NewItem, res.NewAsset)
}

// UploadTexture uploads a texture, which is the common case of
// UploadAsset.  The bytes are a JPEG 2000 codestream; see TextureDims
// for why nothing else will do, and Resize for getting a picture to a
// size that will.
func (w *Session) UploadTexture(ctx context.Context, name, desc string, folder msg.UUID, j2c []byte) (*Item, *UploadResult, error) {
	return w.UploadAsset(ctx, Upload{
		Name: name, Desc: desc, Folder: folder, Type: AssetTexture,
	}, j2c)
}

// jpeg2000SOC is the start of every JPEG 2000 codestream: the SOC
// marker, then SIZ.
var jpeg2000SOC = []byte{0xff, 0x4f, 0xff, 0x51}

// MaxTextureSize is the largest either side of a texture may be.
//
// Measured, not assumed: 2048x256 uploads and 4096x256 is refused with
// "Invalid width: Value too large".  The viewer never offers more
// either -- its own cap is MAX_IMAGE_SIZE_DEFAULT, which is 2048.
const MaxTextureSize = 2048

// MaxDecodeSize is the viewer's MAX_IMAGE_SIZE, llimage/llimage.h:56.
const MaxDecodeSize = 4096

// MinTextureSize is the smallest a viewer will resize a dimension to.
//
// The grid itself goes lower -- 1x1 and 2x2 both upload -- so this is a
// floor on TextureDim and not on what checkTextureDims will accept. It is
// the viewer's MIN_IMAGE_SIZE, and there is no reason to make a texture
// smaller than the one Second Life's own client would.
const MinTextureSize = 4

// LargeTextureArea is where a texture stops costing UploadCost.
//
// Above 1024x1024 the fee is the account's "2K" price rather than the
// flat one, which the viewer reads from its benefits package and this
// package cannot: LargeUploadCost is what the beta grid wanted on
// 2026-08-09, and Upload.Cost overrides it when an account differs.
const (
	LargeTextureArea = 1024 * 1024
	LargeUploadCost  = 50
)

// UploadFee is what the grid charges for a texture of this size, and
// what UploadAsset offers when Upload.Cost is zero.
//
// Zero dimensions mean "not a texture", which is the flat fee: sounds
// and animations are charged the same UploadCost, and only a texture
// has a size to be charged by.
func UploadFee(w, h int) int {
	if w*h > LargeTextureArea {
		return LargeUploadCost
	}
	return UploadCost
}

// TextureDims reads a codestream's dimensions and says whether Second
// Life will take them.
//
// All three checks are here rather than at the grid because the grid
// charges before it looks: the capability takes the bytes, and a
// refusal after that has still cost the fee. The rules are the grid's
// own, in its own words -- "Invalid width: Value not a power of 2" and
// "Invalid width: Value too large" -- both measured against the beta
// grid rather than inferred from the viewer.
//
// Second Life stores textures as J2C and converts nothing: a viewer
// turns the PNG somebody chose into J2C before it uploads it. Sending
// the PNG instead is taken, charged for, and stored as a texture
// nothing can decode.
func TextureDims(b []byte) (w, h int, err error) {
	if !bytes.HasPrefix(b, jpeg2000SOC) {
		return 0, 0, fmt.Errorf("not a JPEG 2000 codestream (starts %#x, wanted %#x): "+
			"Second Life stores textures as J2C and converts nothing",
			head(b, 4), jpeg2000SOC)
	}
	// SIZ, from its length: Lsiz, Rsiz, then the image and its origin.
	// The size is what is left after the origin is taken off, which is
	// zero for everything a viewer makes and not worth assuming.
	if len(b) < 24 {
		return 0, 0, fmt.Errorf("the codestream stops inside its SIZ marker, at %d bytes", len(b))
	}
	x, y := be32(b[8:]), be32(b[12:])
	ox, oy := be32(b[16:]), be32(b[20:])
	w, h = int(x-ox), int(y-oy)

	return w, h, checkTextureDims(w, h)
}

// checkTextureDims applies the grid's two rules about size.
//
// Its own words, from the beta grid: "Invalid width: Value not a power
// of 2" and "Invalid width: Value too large". They are worth applying
// early wherever a size is known, since by the time the grid says
// either one it has been paid.
func checkTextureDims(w, h int) error {
	for _, d := range []struct {
		name string
		v    int
	}{{"width", w}, {"height", h}} {
		switch {
		case d.v <= 0:
			return fmt.Errorf("%s is %d", d.name, d.v)
		case d.v&(d.v-1) != 0:
			return fmt.Errorf("%s %d is not a power of two, which the grid requires: "+
				"resize %dx%d to %dx%d first; sl.Resize will, when asked",
				d.name, d.v, w, h, TextureDim(w), TextureDim(h))
		case d.v > MaxTextureSize:
			return fmt.Errorf("%s %d is larger than the %d the grid allows",
				d.name, d.v, MaxTextureSize)
		}
	}
	return nil
}

// TextureDim is the size Second Life would store a dimension of n as.
//
// It is the viewer's own rounding, from LLImageRaw::biasedDimToPowerOfTwo:
// the nearest power of two, biased downwards -- a dimension goes up only
// when it is more than 1.75 times the power of two below it, since the
// bandwidth saved is worth more than the detail lost. So 800 becomes
// 512 and 1000 becomes 1024, both of them a shade either side of the
// same threshold, and 1800 becomes 2048.
//
// Both dimensions round independently, so any power of two by any other
// is a texture: 1024x64 uploads as happily as 512x512.
func TextureDim(n int) int {
	larger, smaller := MaxTextureSize, MaxTextureSize
	for smaller > n && smaller > MinTextureSize {
		larger = smaller
		smaller >>= 1
	}
	if float64(n)/float64(smaller) > 1.75 {
		return larger
	}
	return smaller
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func head(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}
