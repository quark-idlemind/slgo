package sl

// An avatar's baked textures: the composited skin, clothing and tattoos
// of each body region, made by the grid's baking service from the
// Current Outfit folder.  The avatar's appearance names one per region
// in its texture entry, and the appearance service serves each one at
// a URL made of the avatar, the region and the id.
// Why: doc/outfit-export.md#bakes

import (
	"context"
	"fmt"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// BakeSlot is one baked region of the body: its name, as the
// appearance service spells it in a URL, and the index of its face in
// an appearance's texture entry (ETextureIndex in the viewer's
// llappearance/llavatarappearancedefines.h).
type BakeSlot struct {
	Name  string
	Index int
}

// BakeSlots are the baked regions, in the viewer's order of
// LLAvatarAppearanceDictionary's texture entries.
var BakeSlots = []BakeSlot{
	{"head", 8},
	{"upper", 9},
	{"lower", 10},
	{"eyes", 11},
	{"skirt", 19},
	{"hair", 20},
	{"leftarm", 40},
	{"leftleg", 41},
	{"aux1", 42},
	{"aux2", 43},
	{"aux3", 44},
}

// avatarTextureFaces is TEX_NUM_INDICES: how many faces an avatar's
// texture entry has.
const avatarTextureFaces = 45

// defaultBake is IMG_DEFAULT_AVATAR, the texture a slot holds when
// nothing was baked for it (llcommon/indra_constants.cpp:51).
var defaultBake = msg.MustParseUUID("c228d1cf-4b5d-4ba8-84f4-899a0796aa97")

// Bake is one baked texture an appearance names.
type Bake struct {
	Slot    BakeSlot
	Texture msg.UUID
}

// Bakes reads the baked textures out of an appearance: each slot that
// names a texture other than nothing or the default.
func (s *SimAttachments) Bakes() ([]Bake, error) {
	if s == nil || len(s.TextureEntry) == 0 {
		return nil, fmt.Errorf("sl: no texture entry has been heard for this avatar")
	}
	faces, err := DecodeTextureEntry(s.TextureEntry, avatarTextureFaces)
	if err != nil {
		return nil, err
	}
	var out []Bake
	for _, slot := range BakeSlots {
		if slot.Index >= len(faces) {
			continue
		}
		id := faces[slot.Index].Texture
		if id.IsZero() || id == defaultBake {
			continue
		}
		out = append(out, Bake{Slot: slot, Texture: id})
	}
	return out, nil
}

// BakeURL is where the appearance service serves a bake of avatar's,
// as LLVOAvatar::getImageURL makes it.
func (s *SimAttachments) BakeURL(avatar msg.UUID, b Bake) (string, error) {
	if s == nil || s.AppearanceService == "" {
		return "", fmt.Errorf("sl: the login named no appearance service, so bakes cannot be fetched")
	}
	return fmt.Sprintf("%stexture/%s/%s/%s", s.AppearanceService, avatar, b.Slot.Name, b.Texture), nil
}

// FetchBake downloads one of an avatar's baked textures, as the JPEG
// 2000 codestream the service sends; DecodeTexture makes a picture of
// it.  The zero avatar is this one.
func (w *Session) FetchBake(ctx context.Context, avatar msg.UUID, app *SimAttachments, b Bake) ([]byte, error) {
	if avatar.IsZero() {
		avatar = w.Info().AgentID
	}
	url, err := app.BakeURL(avatar, b)
	if err != nil {
		return nil, err
	}
	return w.capDo(ctx, agent.CapRequest{URL: url, Method: "GET"})
}

// bakedPlaceholders are the textures a face names to be drawn with one
// of the wearer's bakes -- bakes on mesh -- by slot (IMG_USE_BAKED_* in
// llcommon/indra_constants.cpp:80-90).
var bakedPlaceholders = map[msg.UUID]string{
	msg.MustParseUUID("5a9f4a74-30f2-821c-b88d-70499d3e7183"): "head",
	msg.MustParseUUID("ae2de45c-d252-50b8-5c6e-19f39ce79317"): "upper",
	msg.MustParseUUID("24daea5f-0539-cfcf-047f-fbc40b2786ba"): "lower",
	msg.MustParseUUID("52cc6bb6-2ee5-e632-d3ad-50197b1dcb8a"): "eyes",
	msg.MustParseUUID("43529ce8-7faa-ad92-165a-bc4078371687"): "skirt",
	msg.MustParseUUID("09aac1fb-6bce-0bee-7d44-caac6dbb6c63"): "hair",
	msg.MustParseUUID("ff62763f-d60a-9855-890b-0c96f8f8cd98"): "leftarm",
	msg.MustParseUUID("8e915e25-31d1-cc95-ae08-d58a47488251"): "leftleg",
	msg.MustParseUUID("9742065b-19b5-297c-858a-29711d539043"): "aux1",
	msg.MustParseUUID("03642e83-2bd1-4eb9-34b4-4c47ed586d2d"): "aux2",
	msg.MustParseUUID("edd51b77-fc10-ce7a-4b3d-011dfc349e4f"): "aux3",
}

// BakedPlaceholder says which bake a face's texture stands for, if it
// is one of the bakes-on-mesh placeholders.
func BakedPlaceholder(id msg.UUID) (string, bool) {
	s, ok := bakedPlaceholders[id]
	return s, ok
}
