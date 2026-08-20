# Landmarks: a place kept, and gone back to

Written 2026-08-18, against `2d204b8`. Stage 0 has been run against Agni
with qi and what it measured is folded in below: where this says what
happens, it was watched happening. Nothing else here is built.

## Why a landmark is worth having

`tp` can already reach anywhere on the grid, given a region name or a
handle and a position. What it cannot do is remember: a place found
once has to be written down outside the program, and typed back in as
three numbers and a name that has to be spelled right. A landmark is
the grid's own answer to that, it is an ordinary inventory item, and
qi's inventory has had one in it the whole time nothing could read it.

Today `ls` says `landmark` and nothing else in the program knows what
one is. `cat` refuses it in as many words -- "landmark is not text;
asset fetches it" -- and nothing calls `asset` on one.

## What stage 0 measured

Run 2026-08-18 on Agni as Quark Idlemind, from a probe holding one
`sl.Session` and nothing else. Four questions, and the grid answered
all four in about a second each.

### A landmark is 96 bytes of text

`sl.Asset` fetches it with `AssetLandmark`, and this is the whole of it:

	Landmark version 2
	region_id 5cf27e57-7e57-c0de-e8ce-271cf9bf3385
	local_pos 32.00 70.00 1000.09

A region **id** and a position within it. Not a handle, not a region
name, and nothing about the parcel: what a landmark remembers is a
point, and everything a person would recognise about the place has to
be asked for again when they get there.

The asset id is not the item id. `ls -l` prints the item's, and the
asset is a second uuid the listing carries in `Entry.Asset`.

### Making one is a single message, and the simulator fills it in

`sl.CreateItem` already sends it: `CreateInventoryItem` with asset type
and inventory type both 3, and **no folder named**. The simulator makes
the asset out of where the avatar is standing, files the item under the
folder preferred for the type -- Landmarks, without being asked -- and
answers with the item in 300ms:

	standing in Pelmar Reach at 28.0, 72.0, 2001.2
	made item=fc617e57-… asset=27987e57-… parent=46957e57-…
	asset is 96 bytes: … local_pos 28.00 71.95 2001.20

The asset was readable 200ms after the item arrived, so nothing has to
wait and hope. The position is where the avatar was, to a couple of
centimetres, which is the drift of a standing avatar rather than a
rounding.

### Going to one is a single message too

`TeleportLandmarkRequest` carrying the **asset** id. Arrival inside a
second and a half, three times out of three:

	within one region   Pelmar Reach 28,72,2001 -> 32,70,1000   1.0s
	across two          Daskwell 134,146,76 -> Pelmar Reach 28,72,2002  1.0s
	to one just made    back to where the probe started       1.0s

Nothing had to turn the region id into a handle: the simulator does
that. That matters, because a handle is what `tp` needs and the only
way this program has of getting one is a world map lookup by NAME --
which a landmark does not carry.

The daemon followed across the region boundary without being told
anything: the session's own crossing machinery reads the arrival, so a
landmark teleport is a teleport like the ones `tp` already makes.

### A wrong id is silence

Three ways of being wrong, and the grid says nothing to any of them:

	the ITEM id instead of the asset id      no answer, no move
	a uuid that is nothing at all            no answer, no move
	(waited out to 12 seconds each)

This is the same silence a refused parcel gives, and it is the thing
the command has to answer for: a person who typed a landmark's name and
watched nothing happen has no way to tell a wrong id from a grid that
is thinking about it.

**The null id is not an error -- it means home.** Sent one, the avatar
went to Pelmar Reach 26, 66, 24, which is where qi's home is set:

	00000000-0000-0000-0000-000000000000  ->  home, in 1.0s

So "go home" is free, and it is the one landmark nobody has to own.

### Arriving in mid air is arriving in mid air

The Workspace landmark points at 1000 metres, where there is nothing to
stand on. The avatar arrived there and then fell: a minute later it was
at 24 metres, on the ground. Nothing failed and nothing said anything.
A landmark is a point and not a platform, and a `land` under it is the
thing that may have been taken away since.

## What this becomes

### `sl`: read one, make one, go to one

	Session.Landmark(ctx, item)      the region id and position in it
	Session.MakeLandmark(ctx, name)  a landmark here, and the item back
	Session.GoTo(ctx, landmark)      TeleportLandmarkRequest, and wait
	Session.GoHome(ctx)              the same message with the null id

`GoTo` waits the way `Teleport` waits, on the teleport answer the
session already reads, rather than polling for the avatar to be
somewhere else as the probe did. `MakeLandmark` is `CreateItem` with
the two type numbers filled in, which is worth a name of its own only
because "3, 3" at a call site says nothing.

Reading is `sl.Asset` and a parse of four lines. The parse is small and
it is worth being strict about: a landmark whose region id will not
parse is a landmark that would send a teleport to the null region,
which is where home is, and quietly going home instead of failing is
the worst answer available.

### `slsh`: `landmark`

	landmark                     the landmarks in inventory
	landmark NAME                where one goes, without going
	landmark --make NAME         a landmark here
	landmark --go NAME           go there
	landmark --home              go home

`tp` is the command for a place said in numbers and this is the command
for a place said by name; `--go` is deliberately not a bare `landmark
NAME`, because the difference between reading a landmark and being
somewhere else afterwards is not a difference to leave to a typo.

Where one goes is the region id, which is not a name. Turning it into
one costs a world map lookup by handle, and a landmark carries no
handle -- so the honest first version prints the id and the position,
and the name comes later or not at all. `parcel` is the command that
says what is there once you have arrived.

## Stages

### Stage 1 -- `sl`

`Landmark`, `MakeLandmark`, `GoTo` and `GoHome`, with the parse and its
refusals. Tests against the fake backend for: an asset parsed, a
version line nobody has seen refused, a region id that will not parse
refused rather than turned into home, the create carrying both type
numbers and no folder, and `GoTo` waiting on the same answer `Teleport`
waits on.

### Stage 2 -- `landmark` in `slsh`

The command, its forms, and the man page. The man page has to say that
a landmark is a point rather than a place: the parcel may have been
sold, the platform taken away, and the grid will say none of that.

### Stage 3 -- live, on the grid

Make one somewhere, go away, come back by it. Check a landmark made by
a viewer and one made by this against each other. Check what a
landmark into a parcel that refuses this avatar does, which stage 0
could not: making one there needs standing there, and standing there is
what the parcel refuses.

## Open questions

- **What a landmark into a region that is gone does.** No region was
  available to remove for the experiment. The silence a wrong id gives
  suggests it is the same silence, which would mean the command cannot
  tell the two apart and should not pretend to.

- **Whether `local_pos` is the avatar's or the parcel's landing point.**
  Measured only where the two agree. A parcel with a landing point set
  would answer it, and Thrushmoor does not have one.

- **Version 2.** Every landmark read was version 2, including one made
  in 2026 and one made by a viewer in July. Nothing here has seen a
  version 1, and a parser that accepted anything would be guessing.
