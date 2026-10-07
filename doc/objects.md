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
out of range, and the agent calls it every `trimInterval`, so a store
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
range is put on notice for a grace period rather than dropped.

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
until its own first tick, up to `trimInterval` later. One of them
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

## Naming an object

An `ObjectUpdate` carries no name, so a name comes only by asking, and
there are two ways to ask. `RequestObjectPropertiesFamily` takes one
object and is answered with its name and owner; `ObjectSelect` takes
many and is answered with `ObjectProperties`, and the selection is given
back with `ObjectDeselect` straight after.

**The family request is not answered for a child.** Measured on Agni: a
child prim of a linkset stayed nameless through the whole of a resolve,
and one `ObjectSelect` named it at once. Measured again on 2026-10-01
([the ninth round](slate-runner.md#ninth-round-wave-2-end-to-end)): the
first `Faces` or `ObjectByID` on an attachment the avatar had just worn
took 4.517 s on both of two tries -- the region did not answer the
properties request, four quiet seconds went by, and then selection named
it -- and 2 ms on the next call. An attachment's root prim has the
avatar's local id for a parent, so it is not a root in the world either.
That the request is answered only for a root standing in the world is
inferred from those two; no other kind of object was tried.

**What slgo does.** `resolve` reads the store first. An object with a
parent that is not an avatar -- a child of a linkset, or an attachment --
is selected at once and sent no family request. A root, an avatar, and an
object the store does not hold are asked by the family request, as
before, and selected after the wait only if that went unanswered; so is
a child the selection did not name. This is what Firestorm does: it
sends the family request only for a root it is selecting ("request
properties on root objects", `if (objectp->isRootEdit())`,
`indra/newview/llselectmgr.cpp:1394`; hover asks for `getRootEdit()`,
lines 1199-1216) and names what it selects from the `ObjectProperties`
the selection brings.

**Measured.** On 2026-10-02, through slgod, a probe made a 0.1 m prim,
took it into inventory, wore it on the HUD point Center 2 (31), and
timed the first `ObjectByID` on the worn object; then it took it off and
deleted the item. Twice with the family request first and twice with
the selection first, each with a fresh prim:

| resolve | first `ObjectByID` | second |
|---|---|---|
| family request, then the wait, then selection | 4.516 s, 4.514 s | 0 ms, 1 ms |
| selection at once | 0.515 s, 0.516 s | 1 ms, 1 ms |

The half second is the time a selection is given to be answered
before it is given back. The tests in `sl/names_by_select_test.go` show
the same against a fake that never answers a family request for a
child.

## A name a script changed

**Measured on 2026-10-03.** A script that renames its object
(`llSetObjectName`), or changes its description, puts nothing on the
wire: no `ObjectUpdate`, no properties. (Floating text does send a full
`ObjectUpdate`. The CRC field moves for a rename too, but nothing is
sent with it, so it is no signal.) A name in the store is therefore the
name as last asked, for ever. With the test avatar standing by two
invented boxes of one name, each renamed by a script, `ObjectsNamed` of
the old name still found one of them minutes later, `ObjectsNamed` of the
new name found nothing, and a Slate run's setup refused the old name as
naming two objects.

**What the viewer does.** It keeps no name to go stale: it asks for an
object's properties again every time the mouse hovers onto a different
object (`LLSelectMgr::setHoverObject`, `llselectmgr.cpp:1194-1216` at
Firestorm 885631b93a) and on every selection.

**What it costs to ask again for every name.** Measured in a busy
region with the batching `resolve` uses: about 1,000 objects (275 roots,
763 children, 19 attachment roots) took about 300 messages and 8 s --
the roots 1.9 s, the children 5.8 s, mostly the 500 ms selection holds,
the attachments 0.3 s. A 472-object run took about 3.6 s. Every root
answered every time; in one run 79 of 684 children did not answer within
four quiet seconds.

**What slgo does.** A lookup by name, `Session.ObjectsNamed` -- which
every name lookup in slsh, slbotd, slpic, Slate's object bindings and
`internal/session` goes through -- does this:

1. It names whatever is unnamed, as `AllObjects` does.
2. It asks again for the names of the objects the store says have that
   name, the candidates, and waits for answers that arrived *after* the
   asking, not for a name being present; then it looks again. A match a
   script renamed drops out, and a count is right. This is `resolve`
   with its `again` flag: family requests for roots in batches of
   `resolveBatch`, selection for children and attachments, and its quiet
   rounds for giving up. The session keeps when each name last arrived
   (`nameAt`, set where `objectNames` is) so that "newer than the asking"
   can be told. An object that never answers keeps the name it had: it
   is still found by the old name.
3. If nothing then has the name, it asks again for every object in range
   (avatars excepted: a script does not rename an avatar) and looks once
   more, so that a new name is found.

The names `fetch` returns are the backend's store's, which the same
answers update (`Objects.named` in `agent/objects.go` overwrites, from
`ObjectPropertiesFamily` and `ObjectProperties`), so nothing there
changes.

**The window.** Step 3 is the dear one, and a caller that looks a name up
in a loop and keeps missing must not ask the whole region on every
round. So it is bounded per session: at most one full re-ask in
`Options.NamesAskedAgainEvery`, 30 s by default, counted from when the
last one finished. Lookups that miss while one is in flight wait for it
and share it; a lookup that misses inside the window after one does not
start another and looks at what that one left. At about 8 s for 1,000
objects, 30 s keeps a lookup that keeps missing to no more than about a
quarter of its time re-asking. The bound is not a promise that the name
is fresh to the second: a name changed just after a full re-ask is not
found by a miss for up to the window.

**Who polls, and what they get.** Nothing in the tree loops on
`ObjectsNamed` by itself; what repeats is the caller.

- Slate's setup looks each header name up once per run. A run whose
  names have all been renamed or are new pays one full re-ask, on the
  first miss; the names after it look at what that left.
- slsh commands that take an object by name (`touch`, `take`, `dump`,
  `move`, `walk`, `link` and the rest) look it up once per command; a
  script that repeats one until it works, or waits for an object to
  appear, gets a full re-ask at most every 30 s and the candidates asked
  again each time.
- slbotd's `objects` with a name, and its commands that take an object by
  name, are the same, once per command; so are `slpic` and
  `internal/session.RunIn`, which runs once per script run.

A lookup with a short timeout (slsh `walk` gives it 5 s) gets as much of
this as the timeout allows: the re-ask stops at the deadline, and counts
as made.

**A listing is as last asked.** `AllObjects` and slsh's `objects` do not
ask again: they show each name as it was last asked, and an object a
script renamed is listed under its old name until a lookup by name, or
anything else that asks, brings the new one.

The tests are in `sl/names_again_test.go`.

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
  a set is rezzed or first comes into view, is placed by the packet that
  listed it (after the children of earlier packets, by sequence number
  and block): a set described afresh is sent in link order, and not
  always received in it ([Ordering by the packet](#ordering-by-the-packet));
- a prim the store already held that an update links to a parent goes
  to the front, as link 2;
- it is removed when its parent changes or it is killed, and the later
  numbers close up;
- children of a parent not in the store wait in the same list, in the
  order of their packets, and are numbered once the parent is here (an
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
- A linkset first seen as a whole is numbered exactly, when its updates
  are taken in the order the region sent them. Sets taken into
  inventory and rezzed again matched for all 7 prims in every rez, both
  for a set linked all at once and for one grown one at a time. Taken
  in the order they arrived, a set was wrong once in a while: see
  [Ordering by the packet](#ordering-by-the-packet).
- Two avatars in one region shared one store, and moving a set while
  both watched did not reorder it.
- Deleting a child prim deletes the whole linkset, so a kill in the
  middle of one could not be made live; the unit test covers it.

**Sitters.** An avatar sitting on a set is a child of it (the store lists it under the parent the update gives), and the
viewer appends every child, avatar or not (`LLViewerObject::addChild`,
`llviewerobject.cpp:960`, Firestorm 885631b93a), so sitters are numbered
after all of the prims, in the order they sat: on a set of N prims the
first sitter is link N+1, the second N+2, and one that stands lets the
later ones close up. A sitter is link 2 only on a single prim, which is
then link 1. A prim linked while somebody sits goes in among the prims,
as link 2, and the sitters stay after all of them. `Objects.sitters`
keeps them apart from `Objects.kids`, so a sitter takes no part in the
prims' order, the front insertion or `joinWindow`.

Measured on 2026-10-03 on an invented three-prim seat (root, prim2,
prim3), linked by the test avatar, with a script in the root listing
`llGetLinkKey` for every link at each change:

| stage | LSL | the store, first version |
|---|---|---|
| built | root 1, prim2 2, prim3 3 | 1, 2, 3 |
| the test avatar sits on the root | prims 1-3, the avatar 4 | root 1, avatar 2, prim2 3, prim3 4 |
| a second avatar sits on prim2 | the test avatar 4, the second 5 | second 2, test 3, prim2 4, prim3 5 |
| the test avatar stands | prims 1-3, the second avatar 4 | second 2, prim2 3, prim3 4 |
| the second avatar stands | 1, 2, 3 | 1, 2, 3 |
| the test avatar sits, then a fourth prim is linked 2.1 s later | root 1, new prim 2, prim2 3, prim3 4, avatar 5 | new prim 2, avatar 3, prim2 4, prim3 5 |

The store's column is what it said before sitters were kept apart: it
took a sitter for a prim linked live and put it at the front.

A sitter's `LinkKnown` is true unless several sitters were described
already seated (login, or a store taken from another agent), since the
region describes those in no order we know; sitters that sat while the
store watched are in the order they sat. It does not depend on the
prims' order, only on how many prims there are, so a set whose prim
order is unknown can still have known sitters. An avatar described
already seated is appended to the sitters, and so comes after the prims
whichever description arrives first. A root with only sitters is link 1.
`Session.Linkset` lists prims only, so its invariant holds with
sitters present.

**Known and unknown.** A set is known only when the store ordered it by
the region's own sending ([When a set is known](#when-a-set-is-known)),
and every other way a set came to be is unknown. Beside that, one update that links several prims the store
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
to the same parent within `joinWindow`, 250 ms, of the one before makes
the set unknown and remembers its root as misordered, as a link of
several in one update does; the linking session's record and the
whole-set rule still give the order when they apply. A real link of one
is slower than that, whatever makes it: `llCreateLink` sleeps its
script for a second (Linden's published delay, not measured here), and a
person links by hand. A program that links one prim at a time faster
than that gets an unknown set, which is safe; one that wants the order
should link them all in one `ObjectLink`, which it then knows.

**Known again.** An unknown set becomes known again when its root is
killed and the set is described afresh, as after a take and rez, or a
worn set taken off and put on again, however many packets it takes: it
is ordered by them and is known unless a child came as an answer to a
request (see below). Asking the region to
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
20 s (`linkWindow`) and the root's children are exactly the ones named,
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

**Worn linksets.** An attachment's parent is the avatar, so the store
lists the attachment's root under the avatar and its own children under
it. The avatar is not a prim of the worn set, so a worn root is link 1
(0 when it has no children) whatever its parent is, and its children are
2 and up in the order they were listed under it; `Session.Linkset` gives
them the same way as for any other root. The store used to number the
worn root as a child of the avatar, 2 plus its place among what the
avatar wears, and the avatar itself 1 once it wore anything: a worn root
read as 6 or 9, the number of attachments before it. An avatar is not the
root of a linkset either, and is 0 unless it sits (then it is a sitter of
what it sits on, numbered after the prims). A command that asks whether an object is a root or a part of one (`slrun
--object`, `walk`, the listing of `look`) asks `Seen.IsRootIn` or
`Session.Roots`, which count a prim under an avatar as a root; `Parent == 0`
alone means the prim stands in the region, which a worn root does not.
What an avatar sitting on a
set wears is numbered the same way, as sets of their own, and does not
take a place in the seat's numbers.

**Measured.** None of local ids, the store's order or positions gives
the numbers; they are the linking history, so the order has to be kept
as updates arrive. The evidence is in
[doc/slate-runner.md](slate-runner.md#twelfth-round-link-numbers).

## When a set is known

`LinkKnown` is true for a set when the store ordered its children by the
region's own sending, as one circuit sent them. The rule, as built:

1. **The root was described.** By any kind of update, and an update that
   answers a request is fine for a root: the root says nothing of the
   order. A child listed before its root is described waits (it has no
   number while it is an orphan), and the set is known once the root
   comes, in the packet of the children, before or after.
2. **Every child was listed by the region's own sending.** By a full
   (`ObjectUpdate`) or compressed (`ObjectUpdateCompressed`) update that
   did not follow an `ObjectUpdateCached` notice for it, nor a
   `RequestMultipleObjects` this session sent for it, within a minute (a
   *refill*). A refill gives that circuit no key for the child, so that
   circuit has not described the set completely, and a set whose every
   circuit is short of a child that way is unknown. The region
   answers a request in whatever order the viewer asked: Firestorm asks
   for the cache misses in the order it found them
   (`LLViewerRegion::addCacheMiss`, a list appended in turn;
   `requestCacheMisses`, `llviewerregion.cpp:3033`, `3149`), which is the
   order the cached notices came in or the cache's own, never the link
   order, and the answer is taken in the order it arrives. The region's
   side of that is not in Firestorm's source; what is measured is above
   under *Known again*: a flush and a `RequestMultipleObjects` asked for
   in a shuffled order gave the shuffled set back in a wrong order. A
   store that has had more than 65536 such notices at once stops keeping
   them and treats every update within a minute as a refill.
3. **A circuit described it completely, and the circuits agree.** See
   [Ordering by the packet](#ordering-by-the-packet): each circuit's
   order is kept apart, and sequence numbers of different circuits are
   never compared.
4. **Not mixed with a link that came whole.** The set is known only if
   nothing linked several prims to it in one update while the store
   watched, which the region does not send in link order
   ([Link numbers](#link-numbers)), and it did not come from another
   agent's store.

There is no condition on how many messages the set came in, and none on
the sequence numbers between the first child's packet and the last: a
sequence number is the circuit's, shared by every message, so a gap
between two of a set's packets is the other messages the region sent
meanwhile, and is normal. A set that came over three or four packets,
root first, children in link order across them, is the ordinary login of
a worn linkset of a dozen prims or more (measured 2026-10-06: a worn HUD
of that size in three or four consecutive packets, all full updates, root
first, children in link order across them, at each plain login).

A live link of one prim goes to the front, link 2, and is right whatever
the set's history, so it leaves a known set known and an unknown set
unknown; into a parent that is not here, or answered from a request, it
is unknown. A live link has no key, so it keeps its place at the front,
whichever circuit's order the rest follow. How it combines with the
packets' order is under
[Ordering by the packet](#ordering-by-the-packet). The linking session's
own record of an `ObjectLink`, and the joining of one whole known set to
another, are as before.

### Ordering by the packet

The region sends a set's children in link order, in packets of rising
sequence number, root first. The packets can arrive in another order:
on 2026-10-06 a worn linkset was described in packet 126 (the root, then
two children, blocks 2 to 4) and packet 127 (one child, block 1), sent in
that order and arrived the other way round, 127 a millisecond ahead. The
store, which listed the children as they arrived, numbered the later
packet's child link 2 and the two earlier ones 3 and 4; the prims' own
scripts, asked which link each was, said the earlier two were 2 and 3
and the later one 4. Two of nine logins that day were reordered, and
the rest were in order. That packet's root had been described as an
answer to a request, which is what rule 1 allows.

So the store places each child by the sequence number of the packet that
listed it and then its block's place in the packet, not by arrival
(`Objects.orderLocked`), after the children with a smaller one of the same circuit. The
sequence number is the circuit's, 24 bits wide: the viewer takes a
difference of more than half of `LL_MAX_OUT_PACKET_ID`, 0x01000000, as a
wrap (`llcircuit.h:52`, `LLCircuitData::checkPacketInID`,
`llcircuit.cpp:660`), and the store compares modulo that (`seqBefore`),
so packet 0 follows packet 0xffffff.

**One ordering per circuit.** The store is shared by every agent in a
region, and each agent's circuit numbers its packets by itself, so a
sequence number says something only against another from the same
circuit, and on a daemon with several avatars in one region every object
is described on several circuits. Each child keeps, for every circuit
that described it afresh, that circuit's key (packet sequence, block);
the keys of two circuits are never compared or merged. Each circuit
therefore has an ordering of its own of the set's children, by its keys
(`Objects.orderLocked`, `agent/order.go`).

- The store numbers the set by the circuit that has described the most
  of its children (the lowest circuit number on a tie). A child only
  another circuit has described goes after the ones this one has.
- A circuit has described the set *completely* when it has a key for
  every child the store holds under the parent, not counting a live link
  or a listing that came by no packet, which have no key and keep their
  place. A refill gives no key. The root says nothing of how many
  children it has, so complete means every child seen so far: a child the
  region sent to no circuit leaves them all short alike.
- The set is **known** when at least one circuit has described it
  completely and every circuit that has agrees on the order; one
  circuit that saw it in full is enough, and the circuits that saw part
  of it are not consulted.
- If two circuits have each described it completely and disagree, the
  set is unknown, and `slsh objects --how` says which two ("circuits 1
  and 2 each described every child and disagree on the order") and shows
  each update's circuit. If no circuit has described all of it, it is
  unknown too.
- The keys are bounded. They live on the child and go with it, and a
  circuit's keys are dropped from every child when its agent leaves the
  region or logs out (`Objects.Leave`, with the agent's viewpoint): a set
  that only that circuit described completely is then unknown until
  another does. A circuit is one agent's handlers, so a reconnect is a
  new circuit.
- A set given an order outright (a link this session sent, a whole set
  joined to another) is neither reordered nor judged by the keys.

How it combines with a live link, which goes to the front without a
packet key: only the children that have a key are reordered, among the
places they hold, so a live link stays in front of the sequenced ones
however late a packet of the original burst arrives, and a child listed
with no packet (a store a test fills by hand, one taken from another
agent) has none and keeps its place, which is the arrival order. A set the store marked unknown stays unknown whatever the packets
say.

**What is claimed of the last child.** The root does not say how many
children it has, so the store cannot tell the last child from one yet to
come. It claims this, and no more:

- A child sent later is placed correctly by its sequence, whenever it
  arrives, and the numbers of the children before it do not change.
- A packet lost on the way is resent with its original sequence number
  when it was sent reliably, so sequence order covers loss as well as
  reordering. Measured 2026-10-06, in the packet trace of one login
  (`slgod --trace`): every object message from the region came with the
  reliable flag, 276 `ObjectUpdate` (256 of them also zerocoded), 431
  `ObjectUpdateCompressed`, 23 `ObjectUpdateCached`, 406
  `ImprovedTerseObjectUpdate` and 1 `KillObject`. One login is one
  sample, and a region could choose otherwise for a packet, so this is
  not a proof; and a retransmission arrives late, which is what the
  ordering by sequence is for. A child the region sent is therefore not
  lost for good while the circuit lives. What the root does not say is
  whether a set is complete: a child the region never sent at all, or
  sent on a circuit that went away, would leave the later numbers short.
  Firestorm's handler reads no sequence number (`processObjectUpdate`),
  and the circuit acknowledges what carries the flag (`message.cpp:598`);
  the flag is the sender's choice per packet (`message.cpp:1221`).
- So `LinkKnown` means: ordered by the region's sending, every child
  seen so far from that sending. It does not say the set is complete.
  Slate's `link N` says the same in its docs and in the sentence for
  an unknown order: the number is the store's best reading of the
  region's order, and only a probe is the script's own count.

**Why this and not less.** The strict rule this replaced (known only
when every child came in one message, with no gap in the sequence)
called the ordinary login of a worn HUD unknown every time, which
refused every link-numbered Slate step on it, and the arrival order it
replaced was wrong when packets were reordered. The rule was measured for
a set described to a session logging in with no cache (every prim exact,
7 of 7, and sets taken and rezzed again) and for the caught login above;
that a refill is in no useful order is read from the flush and request
measurement and from how Firestorm asks.

**What a program can do about an unknown set.** Name the prim, not its
number (`touch` by name or id reaches the prim whatever its number is);
give the object a probe in a Slate file (`probe NAME`), whose scripts
report the link numbers the prims have, which is the truth the store
only estimates; or have the set described again: take it and rez it, or
take it off and wear it again.

**What Firestorm does.** It numbers a child by its place in its parent's
child list, appended in the order updates give it a parent
(`LLViewerObject::addChild`, `llviewerobject.cpp:960`), that is by
arrival, so it has the weakness above: a viewer that gets a set's packets
in the wrong order numbers it wrongly. slgo departs from it on the
measurement of 2026-10-06 (the caught case), and says so rather than
copying the number. For objects the region says the viewer's cache holds
(`ObjectUpdateCached`,
`LLViewerObjectList::processCachedObjectUpdate`,
`llviewerobjectlist.cpp:766`) it creates them from the cache by scene
contribution, the larger on-screen area first, ties by pointer
(`LLVOCacheEntry::CompareVOCacheEntry`, `llvocache.h:77`;
`LLViewerRegion::createVisibleObjects`, `llviewerregion.cpp:1587`),
which has nothing to do with link order; the ones it misses are asked
for in one `RequestMultipleObjects` (`requestCacheMisses`,
`llviewerregion.cpp:3149`) and arrive when the region sends them. A
child that precedes its root waits and is attached when the root comes
(`findOrphans`, `llviewerobjectlist.cpp:2425`), in the order it waited.
That the viewer would be as wrong is read from the source, not measured.

## How an object was described

Each object keeps the last four ways it was described, dropped with the
object, and the description that put it in its parent's list as well
when that has gone out of the last four: at most five entries per
object however long the session runs. An entry says:

- the kind: `full` (an `ObjectUpdate` block), `compressed`, `terse` (an
  `ImprovedTerseObjectUpdate` block, which only moves a prim; a run of
  them is one entry with a count and the last time), `cached` (an
  `ObjectUpdateCached` block, the region saying it believes the session
  holds the object), `requested` (a `RequestMultipleObjects` block this
  session sent), and `killed` (a `KillObject` for the local id before
  the object that now has it was described; a kill ends the object it
  names, so its own record goes with it);
- the parent the update carried, the packet's sequence number and the
  circuit it came on (a number the store gives each agent's circuit;
  sequence numbers compare only within one), which
  message the store heard it in and the block's place in it and how many
  blocks the message held, and the time;
- `refill`, for a full or compressed update that followed a cached or
  requested notice, and `listed`, for the one that fixed the prim's
  place in its parent's list.

The notices about a local id with no object yet wait for the object, a
minute at most and at most 65536 of them, and go on its ring when it is
described. The store is the region's: a region change makes a new store
and the entries are not carried over, so no region is recorded.

`Seen.How` carries it to a client, filled only when asked (`slsh
objects --how`), over `ObjectsRequest.how` and `ObjectInfo.how` (new
fields; an older daemon says none and an older client does not ask).
`slsh objects --how TEXT` prints, for each prim a search finds, its link
number, whether it is known, its local id, its parent and its entries.
