# Parcels: the land under the avatar

Written 2026-08-18, against `ec9a8f0`, and rewritten the same day
against `e686c99` after the first stage 0 turned out to have measured
the probe rather than the grid. Where this says what happens, it was
watched happening. Stages 1 to 4 were built and run the same day; what
is left is under "Open questions" at the end.

## Why a parcel is worth knowing about

A region is one place to the protocol and several places to everybody
standing in it. What decides whether an avatar may build, fly, run a
script, hear a stream, or stay at all is not the region: it is the
parcel, and a region holds dozens of them. `man group` in `slsh` already
raises the question this answers -- "why has building stopped working
here" -- and today nothing in the program can answer it. `where` says
the region and the coordinates, and the other half of where you are is
missing.

`sl.Subscriptions` has carried `ParcelProperties` since before any of
this, and nothing has ever read it.

## What stage 0 measured

Run 2026-08-18 on Agni, against the session slgod holds, with a probe
attached over gRPC that watches the circuit and the event queue and
sends the requests by hand. Two avatars in two regions were used, and
one region -- Pelmar Reach -- was mapped parcel by parcel.

### The rich answer arrives unasked, on the event queue

`ParcelProperties` is `UDPDeprecated` in the template, and it behaves
that way: it never appears on the circuit and arrives on the queue as
LLSD, about 4 KB of it. It carries everything a person would want --
name, description, owner, group, area, the AABB the parcel occupies,
the prim counts and their allowances, sale and pass prices, media and
music, the landing point, and the flags.

**Every arrival produced one**, naming the parcel landed on and no
other: three of three region crossings, and five of five moves within
one region. The intra-region five included a teleport the parcel
refused -- `tp 60 60 2002` failed, and the push named La Cardonna, the
parcel that would not have this avatar. The push explains the refusal.

So after any move the session knows the parcel it is standing on
without asking, which is the case that matters most.

### Asking is answered too, in about a tenth of a second

All four ways of asking work. Measured against Pelmar Reach with no viewer
attached anywhere:

	ParcelPropertiesRequest      4m square, any parcel   90-110ms
	ParcelPropertiesRequestByID  the local id            122ms
	ParcelDwellRequest           the local id            118ms
	ParcelInfoRequest            the id the cap gave     127ms

The first two are answered by `ParcelProperties` on the event queue,
the same 4 KB the push carries. The other two are answered on the
circuit: `ParcelDwellReply` gives the parcel's global uuid and its
dwell, and `ParcelInfoReply` gives name, owner, area, the region's name
and the parcel's *global* grid coordinates.

Asking works for a parcel the avatar is nowhere near. Six queries
across Pelmar Reach named six different parcels correctly, which is how
the region got mapped: 1 Protected Land (40640 m²), 2 Thrushmoor Home,
3 Windlestraw Lodge, 4 Zorrim, 5 Thrushmoor, 6 OMEN, 7 La Cardonna,
9 Quill Lodge.

**The sequence id is the only correlator.** A reply carries back the id
the request asked with; a push carries a small rising counter of the
simulator's own -- 0 for the first of a session, one more for each
arrival after. Ask with a distinctive negative id, as the viewer does
(-50000 hovering, -10000 selecting), and a reply can never be mistaken
for a push, nor one reply for another.

### What the first stage 0 measured instead

The first run of stage 0 recorded that not one parcel query was ever
answered, over two sessions, nine request shapes and waits out to 75
seconds. Every one of those requests was answered. The answers were
thrown away inside the probe.

`sl.Session.read` is, as its comment says, "the one goroutine that
consumes both relays": it reads `conn.Events()` and `conn.Messages()`.
The probe took a `Session` for `Where`, and then ranged over those same
two channels itself. Two consumers of one channel do not each get a
copy -- they split what arrives, and `client.Conn` delivers with a
non-blocking send, so the loser gets nothing and is told nothing. The
`Session`'s tight select loop won essentially every time.

It was proved by instrumenting slgod: six queries in a row, each
logged arriving on the queue with our own `seq=-10000`, each logged as
relayed (`1 of 1 clients want it`), and the probe printing none of
them. Taking the `Session` only where one is needed made it three of
three, and then everything.

This is the same hazard `viewer/caps.go` describes for the event queue
one layer down, and it is worth stating in the client's own
documentation: **a program that holds an `sl.Session` must not also
read the `Conn` underneath it.** The measurement it corrupts looks
exactly like a grid that does not answer.

### Identity, over HTTP or over the circuit

The `RemoteParcelRequest` capability turns a point into a parcel id:

	POST {"location": [28, 72, 2001],
	      "region_id": <uuid of this region>}
	-> 200 {"parcel_id": <uuid>}

Seven points across one region were asked and all seven answered. A GET
is 405. **Sending `region_handle` as well turns it into a 404**, which
cost half an hour to find: the viewer sends the handle computed from a
*global* position, ours was the region-local one, and the capability
would rather refuse than reconcile them. Send `region_id` and
`location`, and nothing else.

What comes back is an identity and not a description. Two things turn
it into one: `ParcelInfoRequest`, measured above, and `ParcelDwellReply`
which carries the same uuid beside the local id and so ties the two
namings of a parcel together. The local id is per region and short
lived; the uuid is the grid-wide name.

### The layout arrives too, and nobody was reading it

`ParcelOverlay` does arrive on the circuit, unasked, at every arrival:
four packets of 1024 bytes, sequence 0 to 3, together a 64 by 64 grid of
4 metre squares covering the whole region. slgod counts them as
unhandled and drops them.

One byte a square, and the viewer's own constants say what it means
(`indra/llinventory/llparcel.h`): the low three bits are who owns it --
0 public, 1 owned by somebody, 2 the avatar's group, 3 the avatar
itself, 4 for sale, 5 at auction -- and the high bits are 0x10 avatars
hidden, 0x20 sound kept local, 0x40 a property line on the west edge,
0x80 one on the south edge.

That is a whole region's parcel *layout* for four packets nobody asked
for, and it is how a viewer draws boundaries without asking about each
parcel in turn. It says nothing about names or owners.

## What this becomes

The session keeps what the grid volunteers, and asks for the rest. The
push covers the parcel underfoot; asking covers every other parcel in
the region, and the overlay covers the shape of all of them at once.

### `agent`: what the session was told, and what it asked

A handler for the `ParcelProperties` event keeping the last one per
region, and a handler for `ParcelOverlay` keeping the 4096 byte grid.
Both are dropped on a region change beside everything else keyed to a
region.

The overlay wants assembling: four packets, each with a sequence number
saying which quarter it is, and a region change can arrive between them.

Asking wants a small correlator: a request takes the next id from a
descending counter starting well below anything a simulator sends,
records what was asked, and hands the answer to whoever waited for that
id. A push -- a non-negative id nobody is waiting for -- updates the
store instead. One place decides which of the two an arriving
`ParcelProperties` is, and it decides on the sequence id alone.

### `sl` and `slsh`: `parcel`

	parcel            the parcel under the avatar
	parcel X,Y        the parcel covering a point in this region
	parcel --region   every parcel the overlay can see, counted
	parcel --map      the region's parcels drawn, a mark each

`parcel` and `parcel X,Y` both ask, because asking is a tenth of a
second and what it answers is true now, where the push is only as new as
the last arrival. An unanswered ask falls back to the push and says so
in as many words, so a remembered answer can never pass for a fresh one.

`--region` and `--map` read the overlay, which is complete where the
descriptions are not: it draws the whole region's parcels however few of
them have ever been named. The overlay carries no names, so `--region`
cuts the region along the property lines and then asks about a point
inside each piece -- fourteen parcels in two seconds, measured on Pelmar
Reach. That also corrects the count: one parcel can be two pieces of
ground with a road between them, and the local id the answers carry is
the only thing that says so.

### What is deliberately not built

No `ParcelPropertiesUpdate`, which is the write side and would need the
read side to be trustworthy first. No access lists, no banning, no
buying, no dividing. Dwell is one message away and `parcel` may as well
print it, since it is the "Traffic" a person recognises from a viewer.

## Stages

### Stage 1 -- what the session is told, in `agent` (built)

`agent/parcel.go`: the `ParcelProperties` handler and the store it
fills, the `ParcelOverlay` handler and the four packets assembled, the
drop on a region change beside the terrain, and the sequence-id split
between a reply and a push. A push updates the store; a reply is left
for whoever asked, because it describes the parcel they named.

### Stage 2 -- `sl`, the capability, and one new RPC (built)

`Session.ParcelAt` and `Session.ParcelByID` ask and wait for the answer
with their own sequence id on it, `Session.Parcel` is `Where` and then
`ParcelAt`, `Session.Dwell` is the traffic and the parcel's uuid in one
reply, and `Session.ParcelID` is the capability -- `region_id` and
`location` only, with the 404 recorded in a comment so that nobody adds
the handle back.

`Land` is the new RPC, and the overlay is why it exists: four packets on
arrival and none afterwards, so a client that attached later can get it
from the session that heard them or from nowhere. It carries the
overlay's squares, a bitmask of which quarters arrived, and the name and
local id of the parcel the session was pushed.

The sequence ids count down from -2^20, well away from the viewer's own
-50000 and -10000: a viewer attached to the same session asks on the
same circuit and its answers arrive on the same queue.

### Stage 3 -- `parcel` in `slsh` (built)

The command, its four forms, and the man page.

### Stage 4 -- live, on the grid (run)

Run on Agni 2026-08-18 against Pelmar Reach. `parcel` named Thrushmoor with
its owner, group, area, box, prim counts and permissions; `parcel
200,200` named Quill Lodge, a parcel the avatar has never stood on;
`--region` found 14 pieces whose areas sum to exactly 65536 m², which is
the region, and named all fourteen in two seconds -- including the 320
m² piece that turned out to be "Protected Land - Rez zone" rather than
an artefact of the flood fill; `--map` drew them.

**The flags word was checked against a viewer's own panel**, which is
the only independent reading of it there is. Thrushmoor's `0x56a4800b`
against Firestorm's About Land, Options tab: fly on, build off for
everyone and on for the group, object entry the same, scripts on for
both, edit terrain off, damage off, search listing off -- every
checkbox agreed with what `parcel` printed. That is what says the word
is read most significant byte first; the other reading is a parcel
nobody may fly over. The prim counts were checked the same way and
agreed to the prim.

It also caught two bits this code had wrong. 29 is voice chat and 30 is
the estate's voice channel, where they had been written as 28 and 29,
and there is no "group fly" flag at all -- 28 is group object entry.
Nothing but the panel could have caught that: every one of those bits
decodes to a plausible-looking answer.

Two more things that came out of running it:

**The ownership classes alone draw nothing.** Every square of Pelmar Reach
reads "owned", so the first picture was one character from corner to
corner. Drawing the boundaries over it was the second attempt and
readable; drawing a mark per parcel is the third and is what the
command does, since which parcel is which is the question a map of
parcels answers. The mark is a letter and the colour is a colour: the
letter is what survives the picture going down a pipe.

Linden's protected land is drawn as ground rather than as a parcel --
blank for the roads, `.` for the rez zones in them. It is 40640 of Pelmar
Reach's 65536 square metres and none of it is anybody's, so a letter of
its own is the loudest mark in the picture on the one parcel nobody is
asking about.

**Opening About Land in an attached viewer shows the parcel the session
last asked about**, not the one underfoot: a query moves the
simulator's idea of the agent's selected parcel, and the viewer redraws
to follow it. That is how this whole investigation started.

## Open questions

- **Why two queries out of about a dozen were never answered.** Both
  were measured after the instrument was fixed and the daemon logged no
  event at all for them; one was three seconds after a teleport
  arrival. Everything else answered inside 130ms, and nothing in stage
  4 reproduced it -- `parcel` has not failed to get an answer yet. The
  command falls back to the push and says so, which is the right
  behaviour whatever the cause turns out to be.

- **Whether the overlay is resent when a parcel is divided or sold.**
  Only arrival was measured.

- **What a viewer's About Land takes its "Parcel ID" field from.** On a
  session standing on Thrushmoor (local 5, `3c8f7e57`) it showed
  `bbb14d12`, which is Protected Land, local 1 -- a different parcel
  from the one the rest of the panel described. Nothing here depends on
  it, but it means that field is not a check on our own identity work.
