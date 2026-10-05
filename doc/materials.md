# A face's material

A texture entry says which material a face has and nothing about it.
`Face.Material` is an id, and what the id names -- the face's normal and
specular maps and, the part this tree reads, its alpha mode -- is kept by
the region, which gives it out on the `RenderMaterials` capability. This
page is what was measured of that, and why `sl` answers as it does.

## The capability

`sl.Session.Materials` asks for a list of ids and `sl.Session.AlphaModeOf`
and `FaceAlphaMode` read one face's mode from the answer. The protocol is
Firestorm's (`llmaterialmgr.cpp`, `llmaterial.cpp`), and it was checked
against a region on 2026-10-04 with the test avatar, in a workshop:

- A POST to the capability carries LLSD XML, a map with one key,
  `Zipped`, whose value is a `<binary>`: the zlib compression of an LLSD
  binary array of the ids, each a 16 byte binary. It answers 200 with the
  same shape, `Zipped` holding a zlib of an array of
  `{ID: <binary 16>, Material: {...}}`. The answer for three ids took
  about 80 ms. "LLSD binary" is what the viewer's
  `LLSDSerialize::toBinary` writes with no header, and is the new
  `internal/llsdbin`.
- A GET answers every material in the region: 131821 bytes in 817 ms
  there. It is not used: a POST names the ids a face has and answers for
  those, and the viewer posts at most 50 ids at a time
  (`MATERIALS_POST_MAX_ENTRIES`), which `Materials` does too.
- A `Material` is a map: `DiffuseAlphaMode` (0 none, 1 blend, 2 mask,
  3 emissive), `AlphaMaskCutoff` (0 to 255), `EnvIntensity`, the two map
  ids `NormMap` and `SpecMap`, the offsets, repeats and rotation of each
  (`NormOffsetX`, `NormOffsetY`, `NormRepeatX`, `NormRepeatY`,
  `NormRotation`, and the same for `Spec`), `SpecColor`, an array of
  four, and `SpecExp`. Offsets, repeats and rotation are integers of
  ten-thousandths on the wire (`MATERIALS_MULTIPLIER`): a repeat of 10000
  is 1.0, as the answer had it, and the viewer's source says the same of
  offsets and rotation, which the measurement did not set. `Material`
  holds them as the float they mean, a rotation in radians.

An id is content-addressed: changing any field of a material gives the
face a different id, so what an id names never changes. `Materials`
therefore keeps what it has read, by id, for the life of the session, and
a second ask for an id sends nothing. An id the region does not answer
for is left out of the result and not kept, since a face can name a
material the region has not stored yet.

A session that does not hold the capability gets `ErrNoMaterialsCap` from
`Materials`. The capability is in `agent.DefaultCaps`, and a region that
does not grant it is a region this cannot read.

## A face with no material

Most faces have none. A zero `Face.Material` is a plain texture, and then
there is nothing to ask for. The mode such a face is drawn in is the
viewer's own choice from the texture -- blended if the texture has an
alpha channel, opaque if not -- which nothing in the texture entry or the
region says, so `sl` does not report it as blend or as none. It is its own
value, `AlphaModeDefault`, and `FaceAlphaMode` answers it for a zero id
without a request.

What makes a face stop being that was measured on 2026-10-04, with a box
whose script set `PRIM_ALPHA_MODE` on four faces, one mode each: none, blend,
mask with a cutoff of 128, and emissive. After a few seconds the texture
entry named a material on the none, mask and emissive faces and none (a zero
id) on the blend face. A POST for the three ids answered a
`DiffuseAlphaMode` of 0, 2 with an `AlphaMaskCutoff` of 128, and 3.

So a script that sets blend and nothing else leaves a face with no
material, and that face reads `default`, not `blend`. The viewer's
constructor for a material does default to blend, which is why a zero id
and a blend material draw alike; but they are not the same fact, and
which of the two a face has is something a test of a script may need to
know.

The first read of the box, three seconds after the script ran, still showed
the old texture entry. A change of material reaches a session as any change
of a face does, with the region's next update of the object, and a test
waits for it as it waits for a texture (`expect alphamode ... becomes`).

## Slate

`expect alphamode OBJ face N is mask` reads this; the language is in
[slate-language.md](slate-language.md#expectations) and the runner's
side in [slate-runner.md](slate-runner.md#alpha-mode).
