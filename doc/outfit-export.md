# Exporting what an avatar looks like

`export-outfit` in slsh writes an avatar's appearance to a directory for
a program that draws it offline: the shape, the bakes, the wearables
and the worn objects, each as the grid stores it. This page is where
each part comes from. The viewer is Firestorm 885631b93a, `indra/`
relative.

## The appearance

The simulator sends `AvatarAppearance` about an avatar when it comes into
view, and about this one when it is baked. slgod keeps the last one for
each avatar (agent/appearance.go) and the `Attachments` call hands a
client its visual parameters, its texture entry and its attachment list.
There is no message that asks for it again; an avatar that has not been
baked since it logged in has not been sent one, and `Session.Rebake`
asks for a bake, which changes nothing worn and is answered with one.

The visual parameters are one byte each, in the order the simulator
sent them, which is the order of the viewer's appearance parameters
marked to go over the wire (`avatar_lad.xml`); what each means is the
reader's business.

## Bakes

The texture entry has one face for each of the viewer's 45 avatar
texture indices (`ETextureIndex`, `llappearance/llavatarappearancedefines.h`),
and eleven of them are bakes: head 8, upper 9, lower 10, eyes 11, skirt
19, hair 20, leftarm 40, leftleg 41, aux1 to aux3 42 to 44. A slot not
baked holds `IMG_DEFAULT_AVATAR`.

A bake is not fetched like other textures. The viewer asks the
appearance service the login names, `agent_appearance_service`, at
`SERVICE texture/AVATAR/SLOT/ID` (`LLVOAvatar::getImageURL`,
`newview/llvoavatar.cpp:6869`), where SLOT is the slot's name above.
slgod only fetches from URLs a simulator or the login gave, and accepts
URLs under that service for this.

Measured on Aditi on 2026-11-11: both test avatars there wore nothing
and their bake slots all held `IMG_DEFAULT_AVATAR`, so no bake has yet
been fetched this way. That the URL is right is from the viewer's
source.

### The order of a face set

Reading those slots found a mistake in how slgo read every texture
entry. A face set is a bit per face, written seven bits a byte, and the
viewer writes the most significant seven first
(`llprimitive/llprimitive.cpp:1110-1126`, read at `:1165-1177`). slgo
read and wrote them least significant first, which agrees for faces 0 to
6, the only ones a one-byte set can name, and puts every face from the
eighth on in the wrong place. On an avatar that scattered the bakes
across slots no bake is kept in; read the viewer's way, the faces that
are not blank on a beta-grid avatar are exactly the eleven bake slots.
Both directions are fixed, so `texture` on a face from the eighth on of
a mesh changes the face it names.

## Wearables

Each link in the Current Outfit folder to a body part or clothing item
is followed to its item, and the item's asset is fetched with the asset
type of the item. The asset is the wearable's text form, which the
viewer reads with `LLWearable::importStream`.

## Attachments

What is worn comes from the region's descriptions of the attached
objects (`Session.WornObjects`), so it is what the region has described
and not the simulator's list; doc/outfit.md says where the two differ.
Each one is described as `dump` describes it, which puts each prim in
the attachment point's frame, and each prim's faces are read as
`texture` reads them. A mesh prim is one whose sculpt block is of the
mesh kind; its sculpt id is its mesh asset, fetched as is. `dump` writes
a mesh as a box, so the export reads the block from the region's
description rather than from `dump`'s.

The block may arrive as extra-parameter type `0x60` rather than the
sculpt type `0x30`; the viewer reads both the same way
(`newview/llviewerobject.cpp:6736-6739`). slgo read only `0x30`, and so
recognized no mesh at all in the outfit this was measured with on Agni
on 2026-11-11: every mesh prim of it arrived as `0x60`, in
`ObjectUpdateCompressed`.

A face's texture may be one of the bakes-on-mesh placeholders
`IMG_USE_BAKED_*` (`llcommon/indra_constants.cpp:80-90`), which tells
the viewer to draw the face with one of the wearer's bakes. Those are
written as the id they are, which a reader of the manifest knows, and
not fetched.

How a face uses its texture's alpha is in its legacy material, not its
texture entry: `alpha_mode` and `alpha_cutoff` are read from the
material the face names (doc/materials.md), and left out for a face
with none, which the viewer draws by its texture. It matters most for a
mesh body. Measured on Agni on 2026-11-11: every bakes-on-mesh face of
the body measured was a mask at a cutoff of 128 of 255, so a bake whose
alpha a worn alpha layer had cleared hid the body with it.

## Items, without wearing them

`export-items` writes inventory items one folder each, for a viewer that
dresses an avatar in what it has rather than in what it wears.

A body part or piece of clothing is its wearable asset, which the grid
serves by its id whether or not the item is worn, and the textures that
asset names (its `textures` block, as `LLWearable::importStream` reads
it, `llappearance/llwearable.cpp:409-433`). Measured on Agni on
2026-11-11: an unworn skin, a tattoo layer of three textures, came down
with all three.

An object does not. Measured the same day, on three worn attachments
from a store, each no-modify (owner permissions `0x88000`): inventory
names no asset for any of them, so there is nothing to ask for. That
the grid withholds the asset id of an object its owner may not modify
is inferred from those three; a modifiable one was not tried. What an
object is can be read only from the region while it is worn, so
`export-items` captures an attachment while it is on and keeps the
folder afterwards.
