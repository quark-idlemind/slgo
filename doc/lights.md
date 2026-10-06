# A prim's point light and projector

A prim's point light and its projector are two of the extra parameters of
its object update, beside the sculpt block: the light, type `0x20`, and
the light image, type `0x40` (`msg.ExtraLight`, `msg.ExtraLightImage`).
`msg.DecodeExtraParams` splits the blocks, `msg.LightOf` and
`msg.LightImageOf` read these two, the store keeps them per prim as
`Object.Light` and `Object.Projector` (`agent/objects.go`), and `sl.Seen`
offers them under the same names. The viewer is Firestorm 885631b93a,
`indra/llprimitive/llprimitive.cpp` unless a file is named.

## The light

The block is sixteen bytes. `LLLightParams::unpack` (`llprimitive.cpp:1747`)
reads a colour as four bytes, then three float32: the radius, the cutoff
and the falloff. The fourth colour byte is not an alpha: the viewer keeps
the four bytes over 255 as one `LLColor4` (`LLColor4(const LLColor4U&)`,
`llmath/v4color.cpp:144`) and uses the fourth as the intensity
(`LLVOVolume::setLightIntensity`, `llvovolume.cpp:3390`; the light's
colour in a shader is the colour times it, `getLightLinearColor`,
`llvovolume.cpp:3473`). So `msg.Light` has `Colour` as the three bytes,
`Intensity` as the fourth over 255, and the three floats as sent. The
viewer clamps radius to 0 to 20, falloff to 0 to 2 and cutoff to 0 to 180
(`llprimitive.cpp:82-90`); the store does not clamp what the region sent.

**On and off.** There is no flag. The viewer clears every parameter's
"in use" mark before it reads an update's blocks and sets the mark for the
ones it finds (`llviewerobject.cpp:1569-1572` and `1603-1610` for a full
update, `1917-1922` for a compressed one, and `unpackParameterEntry`,
`6734-6752`), so a light is on while its block is in the update and off
when the update has none. A light "switched off" by a script keeps no
block, and one with zero intensity is a block, which is on. The store
therefore says a light is on by `Object.Light` being non-nil and replaces
it whole from every full or compressed update, as it does the sculpt
mark. A terse update carries no extra parameters and leaves it. The
viewer sends `ObjectExtraParams` (`llviewerobject.cpp:6901`) and has no
handler for one coming in, so the store does not look for it; that the
region never sends it to a viewer is inferred from that, not seen.

**The colour space.** The wire carries the linear colour: `unpack` hands
the bytes to `setLinearColor` (`llprimitive.h:168-169`), whose comment calls it
"the value as it appears in shaders", while the swatch in the build
floater shows `getSRGBColor` (`llprimitive.h:177`, `llpanelvolume.cpp:326`)
and sets the light with `setLightSRGBColor`, which converts to linear
first (`llvovolume.cpp:3370`). Slate reads the wire's bytes over 255 and
does no conversion, so what a test writes is the linear value. Whether
LSL's `PRIM_POINT_LIGHT` colour is that same linear value, as it reads
from the wiki, was not checked against the grid here and is the one thing
a live measurement has to settle: if the region converted a script's
colour on the way, a test would be off by that curve, and `light colour`
would say how far.

## The projector

The block is a texture id and three float32, a vector the viewer calls its
params: `LLLightImageParams::unpack` (`llprimitive.cpp:2213`). The order
of the three is field of view, focus and ambiance, which the build floater
shows in that order (`llpanelvolume.cpp:349-352`). The field of view is in
radians (read from the viewer's own default, a half pi, `llprimitive.cpp:2199`);
measured, a field of view of 1.5 asked of a box's script read back as
1.5. A projector is a spotlight when its texture
is not the null key (`isLightSpotlight`, `llprimitive.h:353`), and a block
with the null key is a projector that projects nothing. The store keeps
the block as it came, and Slate reads a null texture as off.

The light and the light image are separate blocks, and the store reads
each alone.

## What Slate reads

[Light and projector](slate-language.md#light-and-projector) is the
syntax, and [Light and projector](slate-runner.md#light-and-projector) the
reading and its tolerances.
