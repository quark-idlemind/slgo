package sl

// Fetching an asset's bytes.
//
// There are two routes and they serve different things.  ViewerAsset is
// http and fast: it is the content delivery network, and it answers for
// textures, meshes, sounds, animations, clothing and the rest of what a
// viewer has to load to draw the world.  It answers 403 for a notecard
// or a script, which come over the UDP transfer protocol instead --
// that is ReadAsset, and it needs to know which item is being asked for
// rather than just which asset.
//
// Asset picks neither for you: it is the http one, and it says so when
// the type it was handed is one the network refuses.  Guessing would
// mean a call that is sometimes fast and sometimes not, with no way to
// tell which happened.

import (
	"context"
	"fmt"
	"net/url"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// AssetType says what kind of thing an asset is.  The numbers are
// Linden Lab's, from LLAssetType.
type AssetType int32

const (
	AssetTexture      AssetType = 0
	AssetSound        AssetType = 1
	AssetCallingCard  AssetType = 2
	AssetLandmark     AssetType = 3
	AssetScriptLegacy AssetType = 4 // LL's AT_SCRIPT: the pre-LSL2 kind, long dead
	AssetClothing     AssetType = 5
	AssetObject       AssetType = 6
	AssetNotecard     AssetType = 7
	AssetCategory     AssetType = 8
	AssetLSLText      AssetType = 10 // a script's source, which is what "script" means now
	AssetLSLBytecode  AssetType = 11
	AssetTextureTGA   AssetType = 12
	AssetBodypart     AssetType = 13
	AssetSoundWAV     AssetType = 17
	AssetImageTGA     AssetType = 18
	AssetImageJPEG    AssetType = 19
	AssetAnimation    AssetType = 20
	AssetGesture      AssetType = 21
	AssetLink         AssetType = 24
	AssetLinkFolder   AssetType = 25
	AssetMesh         AssetType = 49
	AssetSettings     AssetType = 56
	AssetMaterial     AssetType = 57
	AssetGLTF         AssetType = 58
	AssetGLTFBin      AssetType = 59
)

// assetNames are the names the capability wants in its query, from the
// viewer's LLAssetType::lookup.  The url is
//
//	<cap>/?<name>_id=<uuid>
//
// so a type with no name here cannot be asked for at all.
var assetNames = map[AssetType]string{
	AssetTexture:      "texture",
	AssetSound:        "sound",
	AssetCallingCard:  "callcard",
	AssetLandmark:     "landmark",
	AssetScriptLegacy: "script",
	AssetClothing:     "clothing",
	AssetObject:       "object",
	AssetNotecard:     "notecard",
	AssetCategory:     "category",
	AssetLSLText:      "lsltext",
	AssetLSLBytecode:  "lslbyte",
	AssetTextureTGA:   "txtr_tga",
	AssetBodypart:     "bodypart",
	AssetSoundWAV:     "snd_wav",
	AssetImageTGA:     "img_tga",
	AssetImageJPEG:    "jpeg",
	AssetAnimation:    "animatn",
	AssetGesture:      "gesture",
	AssetLink:         "link",
	AssetLinkFolder:   "link_f",
	AssetMesh:         "mesh",
	AssetSettings:     "settings",
	AssetMaterial:     "material",
	AssetGLTF:         "gltf",
	AssetGLTFBin:      "glbin",
}

// String names the type the way the protocol does.
func (t AssetType) String() string {
	if n, ok := assetNames[t]; ok {
		return n
	}
	return fmt.Sprintf("assettype(%d)", int32(t))
}

// overTheNetwork is the types the content delivery network serves.
// The rest are refused with a 403 however well formed the request is,
// and have to come over the transfer protocol.
var overTheNetwork = map[AssetType]bool{
	AssetTexture:    true,
	AssetSound:      true,
	AssetSoundWAV:   true,
	AssetClothing:   true,
	AssetBodypart:   true,
	AssetAnimation:  true,
	AssetGesture:    true,
	AssetLandmark:   true,
	AssetMesh:       true,
	AssetSettings:   true,
	AssetMaterial:   true,
	AssetGLTF:       true,
	AssetGLTFBin:    true,
	AssetTextureTGA: true,
	AssetImageTGA:   true,
	AssetImageJPEG:  true,
}

// AssetCap is the capability assets are read through.
const AssetCap = "ViewerAsset"

// Asset fetches an asset's bytes over http, by id and type.
//
// Nothing else is needed: the network serves by asset id, so an asset
// whose id you know can be read without knowing which item it belongs
// to, which inventory it is in, or whether it is in one at all.  That
// is what makes it the right call for a texture on a prim somebody else
// owns.
func (w *Session) Asset(ctx context.Context, id msg.UUID, t AssetType) ([]byte, error) {
	if id.IsZero() {
		return nil, fmt.Errorf("sl: no asset id")
	}
	name, ok := assetNames[t]
	if !ok {
		return nil, fmt.Errorf("sl: %s has no name in the protocol, so it cannot be asked for", t)
	}
	if !overTheNetwork[t] {
		return nil, fmt.Errorf("sl: %s is not served over %s -- it answers 403 for these; "+
			"read it with ReadAsset, which goes over the transfer protocol", t, AssetCap)
	}
	if !w.b.HasCap(AssetCap) {
		return nil, fmt.Errorf("sl: this session did not ask for the %s capability", AssetCap)
	}

	return w.capDo(ctx, agent.CapRequest{
		Cap:    AssetCap,
		Method: "GET",
		Path:   "/?" + name + "_id=" + url.QueryEscape(id.String()),
	})
}

// Texture fetches a texture, which is the common case of Asset.
func (w *Session) Texture(ctx context.Context, id msg.UUID) ([]byte, error) {
	return w.Asset(ctx, id, AssetTexture)
}
