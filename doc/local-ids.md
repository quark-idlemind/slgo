# Local ids

Most of the older object messages -- `ObjectSelect`, `ObjectLink`,
`DeRezObject`, `ObjectGrab`, `UpdateTaskInventory` and the rest -- name
an object by its local id, a small number the region hands out, rather
than by its uuid. `sl.Object` carries both. This is why every call in
`sl` that sends a local id takes it from `Session.local`
(`sl/region.go`) instead of from the `Object` it was given.

## A local id is a region's, and a run's

A region numbers its own objects, and nothing stops the next region
using the same numbers for its own -- the viewer's bookkeeping, below,
is built on their doing so. A local id kept across a teleport, a border
crossing or an accepted lure can name something there as readily as it
named something here. `sl.Session` has dropped its own maps of local
ids on a region change since the teleport work
([doc/history/teleport.md](history/teleport.md), stage 4); an `Object`
a caller was still holding went on sending its old number, which is the
hole this closes.

The viewer never keeps the number apart from the region: it looks a
local id up by the simulator's address as well as by the number
(`llviewerobjectlist.cpp:141-159`), and sends a selection's local ids
to each object's own region (`llselectmgr.cpp:6063-6110`, packing them
at 5865-5869). A session here talks to one region, the one the avatar
is in, so the equivalent is to resolve a local id against that region.

A region that restarts keeps its uuid and numbers everything afresh.
What a client hears of one is slgod logging back in, which it announces
as a region change with the detail `session re-established`
(`server/server.go`), and which may land in the same region. So a
re-established session counts as a new run of the region, and every
local id is looked up again. None of this was watched happening: that
a restarted region keeps its uuid and renumbers its objects, and that a
restart reaches a client as a re-established session, are taken as
given here and have not been measured.

## What `Session.local` does

Every `Object` found through the session -- from `ObjectsNamed`,
`ObjectByID`, `AllObjects`, `Rez`, `Build`, `WornObjects` and the rest,
all of which come through `fetch`, and an attachment as the region
reports it going on -- is marked with the *visit* it was found in: the
region's uuid, asked of the backend once per visit, and a number drawn
afresh on every region change. The numbers come from one count for the
whole process, so an `Object` from one `Session` never passes for one
from another.

When a call needs the local id:

1. If the object was found in the visit the avatar is on now, and the
   region was named then, its own `Local` is sent. No round trip.
2. Otherwise the object is looked up by its uuid in the backend's store
   for the region the avatar is in, which is what the daemon's `Objects`
   call answers from. The local id found there is written back into the
   `Object` and sent, so the next call needs no lookup.
3. If the region has not described that uuid, the call is refused with
   `sl.ErrNotHere` and nothing is sent. The old number is never sent.
4. If the answer is marked with a visit that is already over -- the
   avatar moved while it was being looked up, or the backend could not
   name the region -- it is looked up once more. A second such answer
   is refused with `sl.ErrNotHere` too, and the error says which it
   was: the avatar changed region while the object was being looked
   up, or the backend has not said which region the avatar is in.

An `Object` built by hand, like the `&sl.Object{ID: id}` that `slsh`'s
`sit` makes from a uuid, has no visit, so a call that sends its local id
always looks it up first. One with a local id and no uuid cannot be
looked up, and is refused.

`Session.Parent` and `Sit` read local ids rather than send them. They
use the same mark, and for an object from another visit fall back to
what this session has been told since arriving, without a round trip.
`Describe` and `Apply` find a linkset by the object's uuid, and by its
local id only when it has no uuid.

## What it does not close

The session hears that the avatar has moved from the daemon, a moment
after the daemon has moved it. An `Object` found in the old region and
used in that moment still sends its old number to the new region. That
window is the notice's latency, and nothing here can shorten it without
the daemon marking what it says about objects with the region it is
about, which it does not.

Step 4 does not close it either. `Session.local` no longer hands out an
id it can already tell is stale, one whose lookup crossed a move the
session had heard of by the time the answer came back; a move it has
not heard of yet still looks like no move at all.
