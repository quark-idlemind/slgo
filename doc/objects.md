# What the session keeps of a region's objects

A region describes its objects once, when the avatar arrives, and then
mentions only what changes, so `agent.Objects` keeps what it was told
for a client that asks later, and `Objects.Trim` drops what is out of
everybody's range. The comments in `agent/objects.go`,
`agent/presence.go` and `agent/agent.go` say what the store does. This
page is why: what went wrong before each rule, and what was watched
happening.

## Before Trim

The store did not always forget what it had heard. Measured before
`Trim` was written: raising the draw distance from 128 to 256 metres
took the count from 195 to 217, and dropping it to 32 left it at 193 --
the store kept everything the far view had brought in. Those numbers
are what it did then, and are written down because they turn up
elsewhere and would otherwise read as current. `Trim` now drops what is
out of range, and the agent calls it every `TrimInterval`, so a store
does shrink when the camera pulls in -- a little behind the camera
rather than with it.

## Out of range, for a while

Out of range is judged against cameras, and a camera can be wrong for a
moment. At login the camera is put on the avatar before anything has
said where the avatar is, so for a second or so it looks out from the
region's corner; everything described in that second -- and the region
describes the most in the seconds after arriving -- was judged from
there and thrown away, and the region describes each object once.
Measured on Agni: an avatar sitting on a chair whose description
arrived in that window stayed "sitting on something not described
here" for as long as the session lasted. That is why an object out of
range is put on notice for `OutOfRangeGrace` rather than dropped.

## People are kept whatever the distance

A region describes each avatar once, and a standing avatar sends
nothing afterwards, so one dropped for distance is dropped for good.
Measured on Agni. Quark logged in at ground level while two others
stood on a skybox 1977m up; each session threw the others away as out
of range at that moment, and after Quark teleported up to join them,
all three were within six metres and none could see any of the others.
Logging in already beside them worked perfectly, which is what made it
look like a viewer problem.

## What a seated avatar wears

A seated avatar's parent is its seat, and a seat is a prim like any
other, so `Trim`'s walk up from an attachment carries on up into it --
and when the seat had not been described, every attachment the avatar
wore used to count as an orphan and go a minute later. Measured on
Agni: an avatar sitting on a chair nothing had described, all ten
attachments described at 19:39 and none of them by 20:00, with the
avatar never having moved.

## Orphans

An orphan is timed from the last word about it; timing it from when it
was first heard was the wrong clock. A region goes on describing a prim
whose root it never describes to us, and that is an update that cannot
be judged, so the prim was taken back in as soon as it was dropped:
measured on a live region, one such prim was deleted and re-created
every minute for hours, losing its name each time and costing a fresh
name lookup to get it back.

## The click action

A full `ObjectUpdate` carries a prim's click action byte (`ClickAction`),
and a compressed update carries it in its fixed header; a terse update
does not. The store keeps the last byte either kind gave as
`Object.Click`, with `Object.ClickKnown` set. Zero is a real value, the
touch action, so it cannot also mean that nothing has said:
`ClickKnown` false is "no update has said", and a zero `Click` with it
true is touch. Forgetting an appearance does not forget the byte. The
values are named beside `Seen.Click` in sl (`ClickTouch` and the rest).

## The camera, as soon as it moves

`lookFrom` tells the store where an agent is looking whenever the
camera moves, and not only on the trim tick. Before it did, an agent
joining a store the others were already trimming was invisible to them
until its own first tick, up to `TrimInterval` later. One of them
trimming in that window, from a camera somewhere else, threw away
everything around the newcomer, its own attachments included, and the
region describes each object once. Measured on Agni: an avatar logged
in last of four, the other three thousands of metres above it, came up
with every attachment the simulator had restored missing from the
store for good -- and only what was put on after its first tick was
ever described.

## A saturated height

A `CoarseLocationUpdate` height is one byte of four metre steps, so it
stops at 1020, and 255 means "higher than this can say". Taking it
literally put the camera a kilometre below an avatar on a skybox, and
everything the region then described was judged against a place the
avatar was not -- the objects around it, and the other avatars standing
beside it, arrived already out of range and were dropped. Nothing
describes them twice, so the session never recovered.

Measured: three avatars at about 2001m were all reported at exactly
1020, and none of them could see any of the others, nor its own avatar.

## What the region believes we already hold

`ObjectUpdateCached` is the simulator saying "you have these already".
Ignoring it costs almost everything that was in the region before the
session arrived, while leaving freshly rezzed objects working perfectly
-- those arrive as full `ObjectUpdate`s. That asymmetry is why it went
unnoticed: every experiment written here rezzes the object it works
on. Pointed at a prim that was already there, the session could not
see it at all.

It is also why counting unhandled *messages* made this look trivial.
`ObjectData` is a variable block, so the whole region can arrive in one
packet, and one packet is what the count showed.

## An appearance after ObjectImage

`ObjectImage` replaces every face of an object at once, and the region
does not describe the result. Measured on a live region, on a box of
six faces: every face coloured red, and forty seconds later the reading
had not moved -- it still described the box as it had been before the
red. Worse, the next change starts from that reading, since a client
must send every face and so reads the appearance first: colouring every
face blue and then face 0 white left face 0 white and the other five
back at the red they had been before the blue. So the store forgets an
appearance it has seen changed, rather than keeping one it knows is
wrong.

`sl.SetFace` then asks the region for the appearance again, with the
request a viewer sends for a cache miss, before it builds the change.
Measured on a live region: the answer came back in 100 to 200ms, and
two changes in a row then composed, every face red followed by face 2
green leaving five red faces and a green one. Without the asking they
did not: the red was gone from all six. The composing was watched with
a build that asked on every read rather than only on a forgotten one;
the request that goes out and the answer that comes back are the same
either way.

What those measurements do not settle, read from the viewer's source
since and not measured: the store did not then read the texture entry a
terse update can carry (see the next section), so "the region does not
describe the result" may have been a description it sent and the store
dropped. Forgetting stays right either way.

## An appearance on a terse update

`ImprovedTerseObjectUpdate` carries, beside each object's placement, a
`TextureEntry` field, and the viewer takes a prim's new appearance from
it when it is not empty (`newview/llvovolume.cpp:650-668`, reached from
`processObjectUpdate` for `OUT_TERSE_IMPROVED`,
`llviewerobjectlist.cpp:554-566`). The field holds the entry behind a
four byte length, as the compressed update's does, since the viewer
reads it with `unpackBinaryData` (`llmessage/lldatapacker.cpp:292-320`).
An avatar's is not read there. Until 2026-09-27 the store read only the
placement, so an appearance changed and announced this way was never
seen, and every read of it said what it was before.

That is the shape of what a session reported on 2026-09-27, observed
live and not explained: a script retextured the faces of two child
prims and of a separate root prim, a full viewer showed the change, and
`slsh texture`, `sl.Session.Faces` and the store all went on giving the
old texture ids over several reads seconds apart. Asking for the
objects with `RequestMultipleObjects` brought the new ids within a
second, and changes after that were seen without asking. Whether the
region sent those changes on terse updates is inferred from the source
and not yet watched; the second half, later changes seen without
asking, is not explained by it. A trace of the four object update
messages, with their bodies, taken while the script retextures, is what
would settle both.

A compressed update that does not decode whole is a second way in, found
reading the code: it was stored as if what followed the point it went
wrong had said nothing, which kept the old appearance, took the text
down and could take a half-read shape. Now only its header is kept, the
appearance is forgotten, and the object is asked for again, as
Firestorm asks for one it finds bogus when enforcing strict object
checks (`llvovolume.cpp:538` and `585`). Whether the region ever sends
such a blob is not known; none has been recorded.
