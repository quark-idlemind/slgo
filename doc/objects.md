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

## How many faces a prim has

`Shape.Faces` is a port of what the viewer builds. A prim's texture faces
are the records `LLProfile::generate` makes (`indra/llmath/llvolume.cpp`
:751-1023), one `LLVolumeFace` each (`getNumFaces`, :2792, and
`createVolumeFaces`, :2798), so no two profile faces share a texture
face: the outer sides the cut leaves (one for a circle or a half-circle,
one per side touched for a square or a triangle), one inner side when
hollow, two caps when the path is open, and two profile ends when the
profile is open. The comment on `Faces` gives each line and the rule for
when a path or a profile is open.

It was checked against the simulator's own count on 2026-10-01: 340
shapes, the seven kinds crossed with profile cuts, path cuts, dimples,
hollow and the four hole shapes, were set on one prim, counted with
`llGetNumberOfSides`, and read back from the update the region sent. All
340 agree.

A second run the same day measured what opens the path: 366 shapes, 342
distinct once read back, and all of them agree. On the torus, tube and
ring each of twist, taper, skew, radius offset, revolutions, hole size
and top shear was set alone and in a few combinations, and each crossed
with no cut, a path cut, hollow and a profile cut. The sphere was given
twist, and the three line shapes twist, top size and shear.

- A twist whose begin and end differ opens a circular path and adds the
  two ends, even at a difference of 0.02. An equal twist, 0.25 and
  0.25, does not.
- Any taper opens it, down to 0.02, and so do any skew and any radius
  offset, down to 0.02. The quanta the region stores these in keep a
  non-zero value above the rule's 0.001.
- Hole size and top shear alone leave it closed.
- Revolutions could not be measured alone. Asked for more than one
  revolution with no skew, the region stored a skew as well, 0.67 at
  2 revolutions and 0.78 at 3.5, which fit 1 - 1/(revolutions + 1) at
  hole size X 1. A revolved ring therefore always has an open path, by
  its skew.
- On a line path, which is always open, twist, top size and shear
  change nothing.
- A path that is already open, by a cut, adds nothing more.

The region also clamped one asked-for value: a radius offset of 0.5 with
hole size Y 0.5 was stored as 0. Still unmeasured: the lowest levels of
detail, where the viewer builds fewer path points. `llGetNumberOfSides`
is the simulator's count and does not change with the viewer's detail.

A sculpt or a mesh sends the same shape fields as an ordinary prim and
has the faces its asset says, so a count cannot be had from the shape;
`Seen.FaceCount` rules them out first. The store keeps which a prim is
as `Object.Sculpt`, the sculpt or mesh block of the update's extra
parameters with its kind and asset: a full or compressed update without
the block says the prim is neither, and a terse update, or a compressed
one that would not decode, leaves it as it was.

The eLSL simulator's `primNumSides` agrees with the grid for a cylinder
and a plain shape and not for the rest: it adds two for any cut to a box
or prism, where the grid's count follows the sides the cut leaves (a box
cut from 0.25 has seven faces, one cut at 0.5 has six), and it models
nothing of a cut or a hollow on a torus, tube or ring, nor a sphere that
is both hollow and cut.

## Link numbers

`Object.LinkNumber`, `Seen.LinkNumber` and `Session.Linkset` give a
prim's link number as a viewer would, with no script in the prim. It is
0 for a prim with no children and no parent, 1 for the root of a linkset
that has children, and 2 and up for a child.

**How it is derived.** Firestorm numbers a child 2 plus its place in the
root's child list, and the root 1, and a root with no children 0:
`getObjectLinkNumber` in `indra/newview/rlvhelper.cpp:2099` and the same
in `llfloatertools.cpp:625`. The list is built by
`LLViewerObject::addChild` (`llviewerobject.cpp:960`), which appends when
an update gives a child its parent, and does nothing for one it already
holds. A child is taken out when an update changes its parent
(`llviewerobject.cpp:2248-2249`) or it dies. A child whose parent is not
known yet waits, and is attached when the parent turns up, in the order
the children waited (`LLViewerObjectList::findOrphans`,
`llviewerobjectlist.cpp:2333`). `Objects.kids` is that list, per parent
local id, with one difference from the viewer's, measured below: a prim
the store already held that is linked while it watches goes to the
front.

- a prim described with a parent it is new to the store under, as when
  a set is rezzed or first comes into view, is appended: a set
  described afresh arrives in link order;
- a prim the store already held that an update links to a parent goes
  to the front, as link 2;
- it is removed when its parent changes or it is killed, and the later
  numbers close up;
- children of a parent not in the store wait in the same list, in the
  order they arrived, and are numbered once the parent is here (an
  orphan has no number, as in the viewer);
- an update that repeats the parent a prim is already listed under
  changes nothing. The store is shared by every agent in a region, so
  the same update arrives once for each, and anything else would let
  two avatars reorder a linkset;
- killing a parent drops its list, as the viewer's children die with it.

**What was measured (2026-10-01).** Seven prims, each with a script that
said `llGetLinkNumber`, were linked while a store built from this code
watched, and one store was traced packet by packet:

- A link of one, by the client (`ObjectLink` naming one child) or by a
  script (`llCreateLink`), came as one update for the new child, which
  became link 2. Grown one at a time in a mixed order, the store matched
  every prim.
- `llBreakLink` of a child in the middle came as one update unlinking
  it, and the rest closed up. The store matched.
- A link of several in one `ObjectLink` came to the linking session as
  one update with a block per child, in the order the prims were made
  (descending local id), not the link order. Children named in the order they were made happened
  to match; named in a mixed order they did not.
- The order of a link of several is fixed per avatar, and is not the
  link order. The linking session received the children in descending
  local id, and a watching session in ascending local id, sometimes in
  two packets. Neither followed the order the `ObjectLink` named, which
  `llGetLinkNumber` in the prims did, every time: the children were
  numbered 2, 3, 4 and up in the order named.
- A linkset first seen as a whole is numbered exactly. Sets taken into
  inventory and rezzed again matched for all 7 prims in every rez, both
  for a set linked all at once and for one grown one at a time.
- Two avatars in one region shared one store, and moving a set while
  both watched did not reorder it.
- Deleting a child prim deletes the whole linkset, so a kill in the
  middle of one could not be made live; the unit test covers it.

**Known and unknown.** One update that links several prims the store
already held to one parent leaves that set's order unknown
(`Objects.joinedTogether`), since its blocks are not in link order,
unless this session sent the link (below). So do the sets of a store
taken from another agent, whose copies come in no order. `LinkNumber` is still given (0, 1, 2 and up), and `LinkKnown`
says whether to believe it: true for a prim with no parent and no
children, and otherwise the state of its set. `Session.Linkset` returns
`ErrLinkOrderUnknown` for an unknown set rather than a list that may be
in the wrong order, and over gRPC it is `ObjectInfo.link_known`.

**Joins close together.** A link of several does not always reach the
store as one update. A watching session was measured to get one split
over two packets, 75 microseconds apart, and two agents sharing one
region's store each run their own handler, so each may apply a
different child's block first and see the other's copy as a repeat.
Either way each block on its own looks like a link of one, and front
insertion would number the set wrongly as known. So a second live join
to the same parent within `JoinWindow`, 250 ms, of the one before makes
the set unknown and remembers its root as misordered, as a link of
several in one update does; the linking session's record and the
whole-set rule still give the order when they apply. A real link of one
is slower than that, whatever makes it: `llCreateLink` sleeps its
script for a second (Linden's published delay, not measured here), and a
person links by hand. A program that links one prim at a time faster
than that gets an unknown set, which is safe; one that wants the order
should link them all in one `ObjectLink`, which it then knows.

**Known again.** An unknown set becomes known again when every child in
its list arrives fresh, as after a take and rez. Asking the region to
describe a set again without taking it did not restore the link order,
measured on a set linked several at once:

- a flush alone left the set out of the store;
- a flush and `RequestMultipleObjects` for the root and its children,
  asked for in a shuffled order, brought the set back in the same wrong
  order it had before the flush;
- a flush and `RequestMultipleObjects` for the root alone brought back
  only the root;
- a flush and a select of the root brought back nothing;
- a draw distance of 1 m for 50 s did not trim the set, and nothing
  changed;
- a local teleport far below dropped the set, and it had not come back
  10 s after the return.

So a flush is not a way to make a set known, and the region goes on
sending such a set in its wrong order to a session already in the
region. It is not the order the region keeps: a session that logs in
fresh is sent the set in link order (see "A viewer" below). The store therefore remembers the
root, by its full id, of every set that several prims joined in one
update (`Objects.misordered`), and keeps that set unknown however it is
described afresh afterwards, after a flush included, whether the root
or the children come first. A live link of one prim to such a set is
still link 2 and leaves it known, and so does the linking session's own
record (below), but neither clears the memory: the region's fresh
description of the set stays misordered. A flush does not clear the
memory; a kill of the
root does, which is what a delete or a take sends, and a rez gives the
object new ids. Going out of range and being trimmed is not a kill, and
keeps it. The memory does not survive a new store, on a region change,
or a restart of the daemon, after which such a set described fresh would
read as known. An update that repeats a prim's parent changes nothing,
known or not.

**A linkset linked to another.** Measured on 2026-10-02 with two
four-prim sets, A and B, each in a known order, and a script in A's
root calling `llCreateLink`:

- `llCreateLink(B's root, TRUE)`: A's root stays link 1, B's root is
  link 2, B's children follow in B's order, then A's children in A's
  order;
- `llCreateLink(one of B's children, TRUE)`: the same; naming a child
  links its whole set;
- `llCreateLink(B's root, FALSE)`: B's root becomes link 1, A's root is
  link 2, A's children follow in A's order, then B's children.

So the set that joins goes in right after the root that stays, root
first and its children in their own order, ahead of the root's old
children. The same day the two sets were linked from the client side,
the way a viewer links two selected objects: one `ObjectLink` naming A's
root and then B's gave the first order above, and one naming B's root
and then A's gave the third. The store cannot tell a script's link from
a viewer's, and both give the same order.

The region's update for it is one packet with a block per prim that
joined, in descending local id, followed by the root's, so it does not
carry the order. The store takes it from the two sets as they were
(`Objects.planJoins`, worked out before the update is applied): when the
prims that join a parent in one update are exactly one root and all of
its children, and the order of both that set and the parent's is known,
the parent's list becomes that root, its children in their order, and
then the parent's old children, and the set is known. Anything else that
several join in one update stays unknown: part of a set, two sets at
once, a set of unknown order, or a set that arrives over two packets.

**Only the session that linked can know.** A link of several names its
order in the `ObjectLink`, and nothing the region sends afterwards
carries it, so only the session that sent it can know it. Every
`ObjectLink` a session sends passes the agent's send tap, whether a
program sent it, a client of the daemon, or a viewer bridged onto the
circuit. For one that names two or more children the store keeps the
root's local id, the children in the order named and the time
(`Objects.links`). When the region's update for that set arrives within
20 s (`LinkWindow`) and the root's children are exactly the ones named,
the store gives the set the named order and marks it known; the record
is then spent. The root stays in `Objects.misordered`, so a flush and a
fresh description, in the region's own order, read as unknown.

- Children named that have not arrived, or are still loose, leave the
  record and the set unknown: the update can come in two packets, and
  the one that completes the set applies it.
- An extra child in the set, or a named child under another parent,
  drops the record, and the set stays unknown.
- A record older than 20 s is dropped when it is next looked at.
- A link of one is not recorded: the front insertion is already right,
  and an earlier record for that root, which the one is an extra child
  of, is dropped.
- Every other session reads the set as unknown until it is seen fresh,
  since it never saw the order named.

**A viewer.** Firestorm numbers a child by its place in a list built in
the order updates arrive (`addChild`), and editing does not change that:
selecting a set sends `ObjectSelect` in the viewer's own child order
(`selectObjectAndFamily`, `sendSelect`), the region answers with
`ObjectProperties` sorted by local id (measured), and
`processObjectProperties` matches each answer to its prim by id without
touching the child list. The viewer was read on 2026-10-02: one avatar
linked seven prims in one `ObjectLink` naming the children in a mixed
order, and a Firestorm logged in as another avatar read Edit Linked,
against `llGetLinkNumber` in each prim:

- watching the link live, it numbered the children in ascending local
  id, the order a watcher receives them, which was wrong;
- relogged with its object cache, it numbered them in yet another order,
  also wrong;
- relogged with the object cache removed, so that the region described
  the set afresh, it was exactly right.

So a viewer has the gap the store has: a link of several made by
someone else is misnumbered until the set is described afresh, and a
fresh description is in link order. The store marks the first case
unknown instead of showing a wrong number.

The third reading also says where the order is. The set had been linked
live, and the description the region sent a session logging in with no
cache had it in link order. A flush and `RequestMultipleObjects`, asked
of a session already in the region, never did (above). So the region
keeps the link order and sends it with a session's first description of
the scene, not in answer to a request. A new session's store starts with
no `misordered` memory, and takes that first description as fresh. That
was measured for the daemon too, the same day: seven prims linked in one
`ObjectLink` in a mixed order, the daemon stopped while the set stood and
started again, so that its avatar logged in fresh; the new store read all
seven exactly, and as known.

**How good it is.** It is only as good as the updates since the last
flush. `Flush` empties the store and the order rebuilds as the region
describes the set again, so a number read just after one, or for a
linkset described partly while it was out of range and trimmed, can be
wrong or 0 until the rest arrives.

**Worn linksets.** An attachment's parent is the avatar, so the same
rule numbers a worn linkset: the attachment's root is a child of the
avatar, and its own children are numbered under it.

**Measured.** None of local ids, the store's order or positions gives
the numbers; they are the linking history, so the order has to be kept
as updates arrive. Seven prims, each with a script that said
`llGetLinkNumber`, were linked five ways on 2026-10-01: linked all at
once the children took the order the link named them in, and grown one
at a time each new prim became link 2, while local ids followed the rez
order and the store's listing order and the positions followed nothing.
