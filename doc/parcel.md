# Parcels: the land under the avatar

Written 2026-08-18, against `ec9a8f0`. Stage 0 has been run against Agni
and what it measured is folded in below: where this says what happens,
it was watched happening. Nothing else here is built.

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
sends the requests by hand. Two avatars in two regions were used.

### The rich answer arrives unasked, on the event queue

`ParcelProperties` is `UDPDeprecated` in the template, and it behaves
that way: it never appears on the circuit and arrives on the queue as
LLSD, about 4 KB of it. It carries everything a person would want --
name, description, owner, group, area, the AABB the parcel occupies,
the prim counts and their allowances, sale and pass prices, media and
music, the landing point, and the flags.

**Every arrival in a region produced one**, in three of three
teleports, naming the parcel the avatar landed on and no other. So
after any teleport the session knows the parcel it is standing on
without asking, which is the case that matters most.

### But asking is not answered at all

This is the finding that shapes everything else. **Not one parcel query
was ever answered**, over two sessions in two regions:

	ParcelPropertiesRequest      rectangle round the avatar   silent
	  ... the whole region, 0,0 to 256,256                    silent
	  ... a 4m square, snapped as the viewer snaps it         silent
	  ... sequence ids -50000, -10000, -2, -1, 0, 1           silent
	  ... SnapSelection both ways                             silent
	  ... rectangles on four different parcels                silent
	ParcelPropertiesRequestByID  the local id from a push     silent
	ParcelInfoRequest            the id the capability gave   silent
	ParcelDwellRequest           the local id from a push     silent

Waited out to 75 seconds, which is far past the tenth of a second a sit
or a teleport answers in.

The request is not at fault, and that took some proving. The encoder
round trips it, the daemon puts it on the wire under the right number,
and the same client path -- a message forwarded through slgod from a
gRPC client -- carries `AgentRequestSit` and `ObjectAdd` and
`RequestObjectPropertiesFamily`, which are answered in milliseconds.
Medium and Low frequency messages both work through it. What is
different about these four is only that they ask about land.

One thing that misled me for a while and is worth recording: **slgod's
trace cannot decode a message a client forwarded**, so every one of them
appears in the trace as `blocks: {}`. That is a limit of the trace and
not an empty message -- `AgentRequestSit` traces the same way while the
simulator plainly acts on it.

### Identity can be asked for, over HTTP

The `RemoteParcelRequest` capability answers, and it is the only thing
that does. Given a point it returns the id of the parcel covering it:

	POST {"location": [28, 72, 2001],
	      "region_id": <uuid of this region>}
	-> 200 {"parcel_id": <uuid>}

Seven points across one region were asked and all seven answered.

**Sending `region_handle` as well turns it into a 404**, which cost half
an hour to find. The viewer sends the handle computed from a *global*
position; ours was the region-local one, and the capability would rather
refuse than reconcile them. Send `region_id` and `location`, and nothing
else.

What comes back is an identity and not a description: a uuid, no name,
no owner, no flags. Turning it into a description is `ParcelInfoRequest`
-- which is in the silent list above.

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

### Intra-region movement pushes sometimes, and I cannot say when

Four teleports within one region, to four points the capability
confirmed were on four different parcels. Two produced a push, naming
the parcel landed on, within two seconds. Two produced nothing at all,
waited out for eighteen seconds each.

The two that pushed were ordinary residential parcels of 2048 m²; the
two that did not were a larger parcel and the region-wide protected land
that surrounds everything. Neither of the pushing parcels was owned by
this avatar or its group, so "your own land" is not the rule. I do not
know what the rule is, and rather than guess, the design below assumes
the push may not come.

## What this becomes

The shape is forced by the measurements: **the session keeps what the
grid volunteers, and nothing pretends to ask.** That is the same shape
`map` already has -- "an empty corner is a corner nobody has described"
-- and it is honest here for the same reason.

### `agent`: the parcel this session was told about

A handler for the `ParcelProperties` event keeping the last one per
region, and a handler for `ParcelOverlay` keeping the 4096 byte grid.
Both are dropped on a region change beside everything else that is
keyed to a region.

The overlay wants assembling: four packets, each with a sequence number
saying which quarter it is, and a region change can arrive between them.

### `sl` and `slsh`: `parcel`

	parcel            the parcel under the avatar
	parcel --region   every parcel the overlay can see, counted
	parcel --map      the overlay drawn, in the shape "map" draws

What `parcel` prints is what is known, and it says which of the two
sources each part came from, because they answer differently often
enough that a person needs to know. If the push has been heard for this
region it is the whole description. If it has not, what is left is the
overlay's square -- who owns it in the coarsest terms, and where the
boundaries are -- and the capability's id, and the command says so
rather than printing nothing.

The `--map` form is worth having precisely because the overlay is
complete where the properties are not: it can always draw the whole
region's parcels, however few of them have ever been described.

### What is deliberately not built

No `ParcelPropertiesUpdate`, which is the write side and would need the
read side to be trustworthy first. No access lists, no banning, no
buying, no dividing. No dwell.

## Stages

### Stage 1 -- what the session is told, in `agent`

The two handlers, the per-region store, and the drop on a region change.
Tests against the fake sim for: a push kept and read back, four overlay
packets assembled, a partial overlay reported as partial, and both
dropped when the region changes.

### Stage 2 -- `sl`, and the capability

`Session.Parcel(ctx)` for what is known here, `Session.ParcelAt(ctx,
point)` for the capability's identity, and the overlay exposed as
something a picture can be drawn from. `ParcelAt` sends `region_id` and
`location` only, with the 404 measured above recorded in a comment so
that nobody adds the handle back.

### Stage 3 -- `parcel` in `slsh`

The command, its three forms, and the man page. The man page has to say
what cannot be asked for, since a person who types `parcel` on a parcel
nothing has described will otherwise think the command is broken rather
than the protocol quiet.

### Stage 4 -- live, on the grid

Teleport across several parcels and regions and see what the command
says at each. Check the map against a viewer's own parcel overlay, which
is the only independent check available.

## Open questions

- **What decides whether an intra-region move pushes.** Two of four, and
  the pattern is not ownership. Until this is known, `parcel` will
  sometimes have nothing but the overlay to go on.

- **Whether any grid still answers `ParcelPropertiesRequest`.** Both
  sessions here are Agni. An OpenSim grid almost certainly answers it,
  since OpenSim implements the message; if so, the ask path is worth
  building as a fallback that Agni will never use.

- **Whether a viewer attached to slgod gets its About Land filled in.**
  It would send the same request through the same circuit. If it is
  answered and ours is not, something about our request differs after
  all, and this whole design is built on a wrong measurement. It is the
  one experiment that could overturn this, and it costs a viewer launch.

- **Whether the overlay is resent when a parcel is divided or sold.**
  Only arrival was measured.
