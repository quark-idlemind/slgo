# A face's GLTF material and the overrides on it

A face of a prim can have a GLTF (PBR) material, and a script or a build
tool can then override single fields of it for that face: the base colour,
the emissive colour, the metallic and roughness factors, the alpha mode and
its cutoff, double sided, a texture for each of four slots and a transform
for each (`PRIM_GLTF_BASE_COLOR`, `PRIM_GLTF_NORMAL`, `PRIM_GLTF_EMISSIVE`
and the rest of LSL). The region does not put the overrides in the object's
update. It says them in a message of their own, to a session that asked for
it. `msg.ParseGLTFOverrideUpdate` reads the message, the object store keeps
the result per object as `agent.Object.GLTF`, and `sl.Seen` offers it with
`Seen.GLTFOverride` and `Session.GLTFOverrides`. The viewer is Firestorm
885631b93a, `indra/` relative, unless a file is named.

Measured on 2026-10-06 with the test avatar, on a test region, with an
invented box whose script set and cleared overrides. What was not measured
says so.

## The capability

**The region sends overrides only to a session that asked the seed for
`ModifyMaterialParams`.** `agent.DefaultCaps` did not, and none came; with
the name added, they came. Two runs of four sets and clears each, without
it: none. Firestorm asks for it (`newview/llviewerregion.cpp:3536`) and
has no handshake flag for this: its `RegionHandshakeReply` sends only
`SUPPORTS_SELF_APPEARANCE`. The capability is the one a viewer later POSTs
to, to change a material (`newview/llgltfmateriallist.cpp:388`); nothing
here posts to it, and asking for it is the whole of its use.

So a session without it has been sent no overrides, and a face of one reads
as having none, which is a wrong answer and not an empty one.
`sl.Session.HoldsOverrides` says whether the session holds it, and
`GLTFOverrides` and `FaceGLTF` are `sl.ErrNoOverridesCap` without it. An
`slgod` older than this release does not ask for it, and a session that
logged in before the upgrade does not hold it: `slate` says so and stops,
in the words of [Slate's runner](slate-runner.md#gltf-materials).

## A face needs a GLTF material

**A face takes an override only once it has a GLTF material.** On a plain
face `PRIM_GLTF_BASE_COLOR` is accepted by the script and reads back blank;
after `PRIM_RENDER_MATERIAL` with the viewer's blank material
(`BLANK_MATERIAL_ASSET_ID`, `llcommon/indra_constants.cpp:92`) it reads back
and is sent. The viewer's own code agrees: it applies an override only to a
face that already has a GLTF material and holds the message back for later
when it has none (`LLViewerObject::setTEGLTFMaterialOverride`,
`newview/llviewerobject.cpp:5898-5927`).

The material a face has is in the object's update, as the render material
extra parameter, type `0x80` (`msg.ExtraRenderMat`): a count, then for each
a face number and an id (`LLRenderMaterialParams::unpack`,
`llprimitive/llprimitive.cpp:2358-2370`, `getMaterial` `:2426`). The texture
entry does not carry it. `msg.RenderMaterialsOf` reads the block, the store
keeps it as `Object.RenderMaterials`, replaced whole by every full or
compressed update as the sculpt mark is, and `Seen.GLTFMaterial(face)` is the
id, the null id for a face with none. A face with no override and a face
with no material are not told apart by the overrides, and are by this.

## The message

A `GenericStreamingMessage`, method `0x4175` (`msg.GenericMethodGLTFMaterialOverride`;
`LLGenericStreamingMessage::METHOD_GLTF_MATERIAL_OVERRIDE`,
`llmessage/llgenericstreamingmessage.h:39`), whose data is LLSD in
**notation**. The viewer reads it in `process_generic_streaming_message`
(`newview/llviewergenericmessage.cpp:97-111`) and
`LLGLTFMaterialList::applyOverrideMessage`
(`newview/llgltfmateriallist.cpp:171-249`). `llsd.DecodeNotation` reads the
notation ([the form](#the-notation)).

Seen, about three seconds after the script's call, for faces 2 and 3 (the
local id is invented here; the rest is as it came):

```text
{'id':i7357001,'od':[{'bc':[r1,r0,r0,r0.5]},{'mf':r0.25,'rf':r0.75}],'te':[i2,i3]}
```

and, 60 ms after the script cleared them:

```text
{'id':i7357001,'od':[!,!],'te':[i2,i3]}
```

`id` is the object's **local** id in the region that sent it; `te` is a list
of face numbers and `od` is parallel to it, the override of each, or undef
for none.

**What a message means**, from the viewer, which the measured messages fit:

- **A message is the object's whole set.** The viewer applies each entry of
  `te`, and then removes the override of every face of the object that `te`
  did not name (`llgltfmateriallist.cpp:235-246`). A face is not kept by
  being left out. `te` empty removes all of them; the viewer's comment says
  that is how the region says "none" (`:205`). The store does the same:
  `setGLTF` replaces the object's overrides with what the message says.
- **No `te` array is malformed** and the viewer does nothing with it
  (`:205`). `ParseGLTFOverrideUpdate` returns an error, and the agent logs it.
- **A face's entry replaces that face's override; keys are not merged
  across messages.** The viewer makes a new default material for each entry
  and applies the keys of the map to it (`:214-222`), so a key absent from the
  entry is not set, whatever an earlier message set.
- **Within an entry, a key present is set and a key absent is not**
  (`applyOverrideLLSD`, `llprimitive/llgltfmaterial.cpp:756-838`). A value
  equal to the material's default is still an override: the viewer nudges
  it by an epsilon so that it is told apart (`:768-835`), so set-ness is the
  key and never the value. A metallic factor of 0 is an override
  (`msg.GLTFOverride.Metallic` is a pointer, nil for unset).
- **The keys and their types.** The viewer tests the type of each value and
  ignores one of the wrong type, which `ParseGLTFOverride` keeps:

| Key | Field | Read as |
|---|---|---|
| `tex` | texture ids by slot, 0 base colour, 1 normal, 2 metallic and roughness, 3 emissive | an array of uuids; a null or undef element leaves that slot alone; `ffffffff-ffff-ffff-ffff-ffffffffffff` sets the slot to no texture (`applyOverrideUUID`, `:597-609`) |
| `bc` | base colour | an array of four reals, red, green, blue, alpha |
| `ec` | emissive colour | an array of three reals |
| `mf` | metallic factor | a real; an integer is ignored |
| `rf` | roughness factor | a real |
| `am` | alpha mode | an integer, 0 opaque, 1 blend, 2 mask |
| `ac` | alpha cutoff | a real |
| `ds` | double sided | a boolean |
| `ti` | texture transforms | an array indexed by the same slots, each a map of `o` (offset, two reals), `s` (scale, two reals) and `r` (rotation in radians, a real) |

At login, on the same test region, the region sent about a hundred
overrides for the objects in view, and every one of them parsed: they
carried `tex` (an array of uuids, one with an undef slot), `bc`, `ec`,
`mf`, `rf` and `ac` as reals or arrays of reals, `am` as an integer, `ds`
as a boolean, and `ti` with `s` only. `ti`'s `o` and `r` and the no-texture
sentinel are from the viewer's reader and the ids it writes
(`getOverrideLLSD`, `:684-754`), and no region was seen to send them. A
colour or offset that is defined and not an array is read by the viewer as
zeros and is skipped here; no region has been seen to send one.

Whether the region always sends the whole set, and so relies on the
viewer's removal of what is not named, was not measured: both messages above
named every face that had had an override, the set naming two and the clear
naming the same two with undef. The store follows the viewer's rule, which
is the same as the region's if the region sends the set whole and the right
thing for a viewer in any case.

## The login burst

**At login the region sends every override in view at once**: 101 messages
in the first five seconds in the test region, the invented box's among
them. A client that attaches later is never sent them again, and a message
may come before the `ObjectUpdate` of its object (the viewer's own comment
says it, `llgltfmateriallist.cpp:191`, and it caches the message for the
object: `LLViewerRegion::cacheFullUpdateGLTFOverride`,
`newview/llviewerregion.cpp:2994-3005`, applied when the object is
described, `applyCacheMiscExtras` `:4104-4121`, and dropped with it,
`:1251`).

So the overrides are kept by the session that holds the connection, from the
start, whether or not anything is watching: they are the same kind of state
as an object's name, said once.

## What is kept

The overrides are on the object: `agent.Object.GLTF` is a map from face to
`*msg.GLTFOverride`, nil for none. A message replaces it whole. Whatever
removes the object removes them: a `KillObject`, `Objects.Trim`, `Flush` on
a change of region. Nothing outlives its object, and an object holds at
most one override for each of 45 faces, so what is held is bounded by what
the store is.

An override that finds no object -- it came first, as the burst allows --
waits in `Objects.pendingGLTF`, by local id, and is adopted by the first
full or compressed update that gives an object that local id
(`adoptGLTFLocked`). The set is bounded two ways, in `agent/gltf.go`:

- **At most 1024 local ids** wait; at the bound the oldest goes first. The
  burst measured was 101, so the bound is ten times it. A region with many
  more overrides in view than that loses the oldest of the ones whose
  objects have not been described yet, which are then said again when the
  object comes into view.
- **Each waits at most a minute**, which is the grace an orphan is given
  (`orphanGrace`): in the burst the objects came in the same few seconds,
  and an override whose object is not described within a minute is for
  something out of view. The age is checked on adoption, and the waiting set
  is swept by `Trim`, every 15 seconds, whatever the draw distance. A
  `KillObject` of a local id drops what waits for it, and a message that
  says none for an id nothing holds drops it.

A session that lives for days therefore holds, besides the store, at most
1024 small waiting entries, none older than a minute and a quarter.
A store taken over by another (`absorb`, when an agent's own store joins
its region's) takes the waiting entries with it, the newer of two for one id.

## How Slate reaches it

Slate reaches the session through `slgod` and reads what a daemon holds by
polling, as every state expectation does. Material **maps** (`#72`) are
read by a capability call from the client, because the region keeps them
and the client has to ask; the **animations** an avatar plays (`#74`) are
relayed, because they are an event stream the daemon does not keep. The
overrides are neither: they are said once and kept by the daemon, like an
object's text or its light, so they travel as a field of the **`Objects`**
call, `ObjectInfo` fields 24 and 25, with the object they belong to
(`internal/server/gltf.go`, `sl/gltf.go`).

A poll, and not a relay, because the expectations are about state --
`is`, `becomes` and `changes` compare readings of a level, not events -- and
because a relay would have to be subscribed to before the burst to see it,
which is the thing a store kept from login makes unnecessary. A client
that attaches an hour in reads what the region said at login. `sl` does
not subscribe to `GenericStreamingMessage`, and the test of it reads the
overrides on a client that never did
(`internal/server/gltf_test.go`).

**Versions.** The fields are new numbers (24 and 25), and the only change on
the wire. An older client sends the same request and ignores the fields.
An older `slgod` sends none, and a newer client would read that as faces
with no override, which is untrue: so a newer client checks that the
session holds `ModifyMaterialParams`, which only a daemon that keeps the
overrides asks for, and says so when it does not (`sl.ErrNoOverridesCap`;
Slate stops with exit 3). A daemon that asks for it and a region that does
not offer it are the same case, correctly.

## The notation

`GenericStreamingMessage` carries LLSD in notation, which `llsd` did not
read. `llsd.DecodeNotation` reads what the viewer's `LLSDNotationParser`
does (`llcommon/llsdserialize.cpp:477`): undef `!`; booleans `1 0 t f T F
true false TRUE FALSE`; `i` integers; `r` reals; `u` uuids; strings in
double quotes, in single quotes and as `s(size)"raw"`, with the escapes
`\a \b \f \n \r \t \v \xHH`; `l` uris; `d` dates; binary as `b16"hex"`,
`b64"base64"` and `b(size)"raw"`; maps and arrays. The values are the Go
values the XML form gives. It departs from the viewer in two places, both
because the data comes off the network: nesting is cut at 256, and text
between a map's entries or an array's elements that is not whitespace or a
comma is an error where the viewer skips it.

## Measured and inferred

Measured: that the capability is what makes the region send overrides; that
a plain face takes no override and a face with the blank material does; the
two messages above and their timing (about 3 s after a set, 60 ms after a
clear); every key and its type but `ti`'s `o` and `r`, from the login
burst, all of which parsed; the login burst.

From the viewer and not measured: `ti`'s `o` and `r`, the whole-set meaning of a message, the waiting for the object, the sentinel
texture, the render material block's layout; that a region sends an
override on a face again after the face's material changes.
Inferred: that `ModifyMaterialParams` is asked for and never used here; that
the region's rounding of a factor is within one millionth, which the
tolerance of Slate's `gltf` readings assumes (use `near` for a script's own
arithmetic).
