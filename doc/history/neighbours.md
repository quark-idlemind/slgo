# Child circuits to the neighbouring regions

> **A plan, not a description of what slgo does.**  Written before the
> work and kept for the measurements folded into it; the stage headings
> below record what was found, including where the plan turned out to be
> wrong.  For current behaviour see `man neighbours` and
> `agent/neighbour.go`.  [doc/history/README.md](README.md) says why
> this is here.

Written 2026-08-16, against `d0584c2`. Stage 0 has been run and what it
measured is folded in below: where this says what happens, it was
watched happening. Nothing else here is built.

`doc/history/teleport.md` ends with a measurement and a wall. Stage 7 built the
handler for `CrossedRegion`, and then a live walk showed the message
never arrives: an avatar walked twelve metres to Pelmar Reach's west edge,
stopped dead at x=0, and stayed there. From the other side he walked to
x=255 in Pelmar Mill and was pinned for twenty-four seconds. The
neighbour was not the problem -- Pelmar Mill is a live region that a
teleport reached in 1.4 seconds and that described 2258 objects. **A
simulator will not hand an avatar over the border to a client that holds
no child circuit**, and slgod holds none by choice (`c1e9d11`).

This is the plan for holding them.

## Optional, and off unless asked for

Neighbours cost sockets, bandwidth and object memory, multiplied by
however many regions surround this one -- four for Pelmar Reach, up to
eight elsewhere. A daemon running `slrun` in one region wants none of
that, and the whole design so far has been one region at a time on
purpose.

So this is an option and it is off by default: `agent.Options.Neighbours`
and an `slgod -neighbours` flag. Off, nothing changes, and a border
crossing goes on being the wall measured above. On, the daemon takes up
the offers the simulator has been making all along.

Nothing above `agent` should have to know which it got, except where it
plainly must -- an object in another region is a fact a client cannot be
handed without being told which region it is in.

## What the grid has been offering all along

Both of these arrive on the event queue and both are `UDPBlackListed`.

**`EnableSimulator`** carries `Handle`, `IP` and `Port` -- a neighbour's
address and nothing else. Measured on Agni: **57 of them in one 200
second run, naming only 4 distinct regions.** The simulator repeats the
offer for as long as it goes untaken, which is both how it survives a
lost packet and a signal worth watching. Stage 0 confirmed it: take the
offer up and it is made **once**.

**`EstablishAgentCommunication`** carries the neighbour's seed
capability, and **it follows the circuit rather than preceding it**. Two
minutes of watching recorded `EnableSimulator` within a second of login
and not one of these; stage 0 then opened a circuit and it arrived 1.04
seconds later. So the simulator introduces a neighbour properly once the
client has taken the address up, and the filter that has been in
`viewer/caps.go` since the front end was built has never had anything to
catch.

Both are withheld from an attached viewer (`d0584c2`). That stays true
while slgod holds the circuits: what a viewer may eventually be given is
slgod's *own* child ports, which is the last stage here and not the
first.

## What a child agent is

A child agent is the same avatar, known to a simulator it is not
standing in, so that the region can describe itself before the avatar
arrives. The circuit is opened with the **same circuit code, session id
and agent id** as the root -- that is the whole of what `UseCircuitCode`
carries -- and the difference between a child and a root is one message:
`CompleteAgentMovement` is what makes a circuit the root, and a child
never sends it until it becomes one.

	client                     neighbour sim
	  |      UseCircuitCode       |
	  |-------------------------->|
	  |      RegionHandshake      |
	  |<--------------------------|
	  |   RegionHandshakeReply    |
	  |-------------------------->|
	  |   LayerData, ObjectUpdate |
	  |<--------------------------|
	  |   StartPingCheck ...      |
	  |<--------------------------|

and, when the avatar walks over the border into it:

	  |   CompleteAgentMovement   |
	  |-------------------------->|   <- the child becomes the root
	  |  AgentMovementComplete    |
	  |<--------------------------|

That last exchange is why holding the circuit is worth more than making
a crossing merely possible: the region is already described when the
avatar arrives, so a crossing is a step rather than the 400ms pause a
teleport measured.

## The hard part: an Agent with more than one circuit

Everything about `Agent` says "one". One `sock`, one `Send`, one `Recv`,
one `Disp`, one `Caps`, one event queue, one object store in
`a.objects`. Six places outside the package reach through the middle
three and none of them may break.

The shape that keeps them safe is the one stage 2 of the teleport used:
**leave the root exactly where it is** and put the children somewhere
else entirely.

	type child struct {
	    handle uint64
	    addr   *net.UDPAddr
	    sock   *socket           // the same holder moveTo swaps
	    send   *msg.Sender
	    recv   *msg.Receiver
	    disp   *msg.Dispatcher
	    store  *Objects          // from the same Cache
	    caps   Caps              // if a child needs any
	}

`msg.NewSender` takes a `PacketWriter` and `msg.NewReceiver` a
`PacketSource`, so a child costs one socket and three objects. What it
does not get is a share of the root's anything: each circuit has its own
sequence numbers, its own acknowledgements, its own duplicate ring.

**A child's dispatcher registers a different, smaller set of handlers,
and choosing that set is most of the design.** There are 22 handlers
registered today and they divide cleanly into two kinds:

- **About the region**, which a child must handle: `RegionHandshake`,
  `StartPingCheck`, `LayerData`, `ObjectUpdate`, `ObjectUpdateCompressed`,
  `ImprovedTerseObjectUpdate`, `ObjectUpdateCached`, `KillObject`,
  `ObjectProperties`, `ObjectPropertiesFamily`, `AvatarAppearance`,
  `CoarseLocationUpdate`.
- **About the avatar**, which a child must NOT handle:
  `AgentMovementComplete`, `TeleportLocal`, `AgentDataUpdate`,
  `AgentGroupDataUpdate`, `LogoutReply`, `KickUser`,
  `OnlineNotification`, `OfflineNotification`, `CrossedRegion`.

The second list is the interesting one. A child that answered
`AgentMovementComplete` would move the session's idea of where the
avatar is to a region it is not in; a child that acted on `KickUser`
would end the session on a neighbour's say-so. The rule is that a child
speaks for a place and the root speaks for the avatar.

## Stages

### Stage 0 -- take one offer up by hand (done)

Run 2026-08-16 against Agni, twice, with a throwaway probe that logs in
directly -- so that it has the circuit code, which a gRPC client is
never given -- opens a socket to the address `EnableSimulator` names,
sends `UseCircuitCode` with this session's own three ids, answers the
handshake and the pings, and then walks the avatar at the border.

**It crosses.** Standing at Pelmar Reach (12, 128) with one child circuit
open to Pelmar Mill, the avatar walked west and was in Pelmar Mill at
x=251 **two seconds later** -- where the same walk with no child circuit
had been pinned at x=0 for twelve seconds, and at x=255 for
twenty-four from the other side. `CrossedRegion` arrived on the event
queue exactly once, and stage 7 of `doc/history/teleport.md` followed it
correctly without a line of change. Reproduced in a second run.

**A circuit is all it takes.** No `AgentThrottle`, no `AgentUpdate` to
the neighbour, no capability fetch, nothing sent but `UseCircuitCode`
and an answer to what came back. That makes stage 2 below sufficient
for crossings on its own, and everything after it an improvement rather
than a prerequisite.

The rest of what it measured, in the order it happened:

- **The offer follows the avatar to the edge.** Logging in at (12, 128),
  twelve metres from the west border, was offered **one** neighbour --
  Pelmar Mill -- where standing at (28, 72, 2001), twenty-eight metres in
  from the west edge and seventy-two from the south, had been offered
  **three**: the regions across those two edges and the one across the
  corner between them. The simulator introduces what is near.
- **Taking it up stops the repetition.** Offered **once**, against 57
  offers of four regions in a 200 second run where nothing ever
  answered. The repeat was a retry, not a heartbeat.
- **`RegionHandshake` comes back in about a second** (1.02s), and comes
  back **twice** -- a retransmission, since nothing else changed -- so a
  child has to be as idempotent about it as the root is.
- **`EstablishAgentCommunication` follows the circuit**, as predicted:
  it arrived 1.04 seconds after `UseCircuitCode` and had never been seen
  in any run before. So the filter that has been in `viewer/caps.go`
  since the front end was built never had anything to catch.
- **Its shape is not the others'.** Where `TeleportFinish` and
  `CrossedRegion` carry blocks as arrays of one map with binary fields,
  this is a flat map with hyphenated lowercase keys and no binary at
  all:

	{"agent-id": "<uuid string>",
	 "seed-capability": "https://simhost-....agni.secondlife.io:12043/cap/...",
	 "sim-ip-and-port": "203.0.113.10:13009"}

  The address arrives as a **`host:port` string**, which is a third
  spelling of the same fact -- four binary bytes in `CrossedRegion`, a
  `U32` and a `U16` in the message template, text here. Stage 5 will
  want that written down.
- **What a child hears, unasked, in thirty seconds**: `ObjectUpdate` 59,
  `LayerData` 51, `ObjectUpdateCached` 40, `AttachedSound` 61,
  `CoarseLocationUpdate` 21, `ImprovedTerseObjectUpdate` 17,
  `StartPingCheck` 5, `SoundTrigger` 4, `ParcelOverlay` 4,
  `RegionHandshake` 2. It describes itself fully and unprompted, which
  is the point of holding the circuit.
- **And what it does not send**: not one `ObjectUpdateCompressed`, where
  the root had 1267 in the same half minute. Worth knowing before
  anything concludes a neighbour is quiet from a handler that only
  counts the compressed kind.
- **The pings are real.** Five `StartPingCheck` in thirty seconds, so a
  child that does not answer will be dropped like any other circuit.

### Stage 1 -- the option, and the neighbours we know of (done)

`Options.Neighbours` and `slgod -neighbours`, both off by default, and
an `EnableSimulator` handler that records handle and address in a list
without connecting to anything. Plus a way to see it: `slsh neighbours`,
or a line in `look`.

Standing on its own this is worth having -- it says what is around this
region, which nothing can answer today.

### Stage 2 -- a circuit that stays up (done)

The `child` type, opened for each neighbour on the list when the option
is on: `UseCircuitCode`, answer `RegionHandshake` and the pings, and
nothing else. Everything the neighbour says is counted and dropped.

That is a stage worth stopping at, because it is the whole of what a
border crossing needs from us if stage 0 says so.

Run 2026-08-16 against Agni, with `slgod -neighbours` doing the holding
rather than a probe. Logged in at Pelmar Reach (12, 128) it opened a
circuit to Pelmar Mill and had its handshake a second later; the avatar
walked west and **was in Pelmar Mill four and a half seconds after the key
went down**, with the region-change notice reaching a client on the way.
The whole chain ran without a hand on it: offer, circuit, walk,
`CrossedRegion`, `moveTo`, the notice, the viewer told nothing.

Three things it showed that stage 0 could not:

- **The set follows the avatar and rebuilds itself.** Walking toward the
  corner, two more were offered and taken -- Orrick Gate (43648, 43647)
  and Kelva Sands (43647, 43647) -- and on arriving in Pelmar Mill all
  three were dropped and its own west neighbour (43646, 43648) was open
  within a second.
- **A teleport drops them the same way**, and from the skybox at 2001m
  in the middle of the region **none is offered at all**: a simulator
  introduces what is near, and nothing is near the middle. (Dropping
  them on a move was undone on 2026-09-26: see
  [Kept across a move](#kept-across-a-move).)
- **`EstablishAgentCommunication` follows the circuit here too**, which
  is now three runs saying it.

What is still true after this stage: a crossing costs a fresh dial,
because `moveTo` does not know the circuit it wants is already open.
That is stage 3.

### Kept across a move

Measured on Agni on 2026-09-26, with `slgod -neighbours` and one avatar
making three round trips by teleport between a region and a Linden
sandbox. At login, and on the first arrival in the sandbox, the
simulator offered three neighbours each time and three child circuits
opened. **On all five arrivals after that** -- each within about ten
seconds of the avatar last leaving that region -- **no
`EnableSimulator` came at all**, and no child circuit opened.
`EstablishAgentCommunication` still came for each neighbour.

At the time every move dropped every child by closing its socket, and
told the simulator nothing. Inferred, not measured: the neighbours'
simulators still counted those child agents as connected, and a
simulator does not offer a child it believes it already has. Stage 0 of
`doc/history/teleport.md` measured the region left behind keeping its
circuit about fifty seconds before sending `DisableSimulator`; how long
a neighbour keeps a child was not measured. So the reason the code gave
for dropping them -- that the new region makes its own offers within
seconds -- holds for a region the avatar has not just left, and is false
for one it comes back to soon: that avatar held no child circuit, and,
going by stage 0, a walk to a border would have met the wall this plan
began with.

The viewer never lets a child go of its own accord while its simulator
is still there. Firestorm adds a region on `EnableSimulator`
(`newview/llworld.cpp:1584`) and removes one only on a
`DisableSimulator` from that region's simulator
(`llworld.cpp:1715`), on an `EnableSimulator` that replaces a stale
entry for the same handle (`LLWorld::addRegion`, `llworld.cpp:520-559`),
or at logout (`LLWorld::resetClass`, `llworld.cpp:132`). It sends
nothing to say it has let a circuit go, and applies
`EstablishAgentCommunication` only to a region it already holds.

So slgo now does what the viewer does:

- **A child is kept across a move**, and closed when `DisableSimulator`
  arrives on its own circuit. The message has no body; the circuit is
  what names the region, as the viewer reads it off the sender. The
  template does not mark it `UDPBlackListed`. The viewer would also
  take one off an event queue -- any templated message is dispatched
  from one with the queue's region as the sender
  (`newview/lleventpoll.cpp:103-128`, `llmessage/message.cpp:113-137`),
  and each region it holds gets its own poll once its seed has given it
  an `EventQueueGet` (`newview/llviewerregion.cpp:3643`) -- but slgo
  polls only the root's, so a child's own circuit is the only road one
  can reach it by.
- **A move closes the child to the region moved into**, which would
  otherwise be a second circuit to the root's simulator. Promoting it
  instead is stage 3.
- **The root circuit to the region left is closed on a move**, as it
  was.
- **At the cap of eight**, an offer first closes a child whose region
  does not touch the avatar's, the lowest handle first, and is turned
  down only when all eight are neighbours.
- Turning neighbours off, and the session ending, still close every
  child.

A simulator can also go away without a `DisableSimulator`, and a child
kept across moves would then be held for the rest of the session, where
before the next move cleared it. The viewer has two answers and slgo
now has both:

- **A circuit silent for 100 seconds is closed.** The viewer's message
  layer drops a circuit whose last answer to one of its own pings is
  older than its circuit timeout (`llmessage/llcircuit.cpp:1065`, the
  figure from `newview/llstartup.cpp:916`). slgo sends no pings of its
  own on any circuit, so a child counts from the last packet of any
  kind, as the root's idle watchdog does and on the same loop. A live
  neighbour is far from that quiet: stage 0 counted five
  `StartPingCheck` on a child in thirty seconds.
- **An offer of a held region replaces a stale child**: one held at
  another address, or silent past that timeout, as `LLWorld::addRegion`
  replaces a region whose host changed or whose circuit died
  (`llworld.cpp:520-559`). An offer at the same address to a child still
  heard from remains a repeat.

Neither has been seen on the grid; both follow the viewer.

Checked on Agni on 2026-09-26, after the change, with `slgod
-neighbours` and one avatar:

- Login opened three child circuits. After 120 seconds with the avatar
  standing still, all three were still held, and each had gone on
  hearing from its simulator at between two and five packets a second,
  so the silence timeout came nowhere near.
- The avatar teleported to a Linden sandbox and back, six seconds
  apart. On the return the three children from before were still held
  and still counting packets. The sandbox had offered its own three
  neighbours on arrival, and those were held too, six in all.
- Fifty seconds after the avatar left the sandbox, a `DisableSimulator`
  came on each of the sandbox's three children, within a second of one
  another, and each was closed. The region's own three stayed.

This was one round trip. It is the case that failed before: a quick
return, with the circuits still up.

### A DisableSimulator on the root circuit

The viewer takes a `DisableSimulator` the same way whichever circuit
brings it: `process_disable_simulator` (`newview/llworld.cpp:1715`)
hands the sender to `LLWorld::removeRegion`, and when that is the
region the agent is in, `removeRegion` removes nothing and
force-disconnects, "You have been disconnected"
(`llworld.cpp:688-709`).

slgo says so and ends nothing. One on the root circuit is logged in one
line -- the region's name and square, the simulator's address, and
that a viewer would end the session here -- and the session goes on,
with no logout, no reconnect, and its children left alone. The message
is still relayed as before, to a client that subscribed to it and to an
attached viewer, whose own handler, reading its code, would then
disconnect it. A simulator that has really let go stops sending, and
the idle watchdog ends the session then, as for any circuit gone quiet.

None has been seen from the region the avatar is in, so what one would
mean there is not known, and nothing is ended until one has been. The
one seen on a root circuit is stage 0's in `doc/history/teleport.md`,
from the region left behind by a teleport slgo did not then follow; a
viewer, having followed it, would have removed that region quietly. A
move now swaps the root's socket as it begins, and nothing more is read
from the region left. Inferred from the code rather than seen: a
session that does not poll the event queue follows no teleport, and
would log that one here.

### Walking back over a border soon after crossing it

Measured on Agni on 2026-09-26, with `slgod -neighbours` and a scratch
build whose `walk` was allowed a target a few metres past the edge and
kept pushing for twelve seconds before calling itself blocked.

- **A quick return crosses.** The avatar was teleported just inside a
  region's east edge, walked east into the next region, and crossed in
  2.6 seconds. It walked straight back west and crossed again in 1.7
  seconds. Neither time did slgo hold a circuit to the region walked
  into: a crossing closes the root it leaves, as it did before
  `Kept across a move`, and the region walked back into had not
  offered itself as a neighbour. Inferred: each simulator still held
  the avatar as a child agent from the moment before, which is what a
  crossing needs, so the problem stage 3's root swap was to solve for a
  walk back does not arise within those few seconds. How long a
  simulator keeps that child was not measured; the region left behind
  by a teleport kept its circuit about fifty seconds.
- **A child opened at login did not carry a crossing.** At the same
  border, after a login with slgo holding a child circuit to the
  western neighbour, open and busy, a walk west was held at the line
  -- x between -0.26 and -0.97 -- for twelve seconds, three times, and
  never crossed. A build from before `Kept across a move` did the same,
  and so did the earliest build `walk` can drive, from 2026-09-24.
- **A child opened on arriving by teleport did.** Between two Linden
  sandboxes, the child slgo opened to Sandbox Newcomb on arrival in
  Sandbox Goguen carried the avatar west in 4.4 seconds, and in 5 after
  a teleport within Goguen first. Back at the border that failed, with
  the region's neighbours offered afresh on a teleport arrival -- the
  avatar had been away long enough for the old ones to be disabled --
  the same walk west crossed in 4.7 seconds. So the land lets the
  avatar over, and slgo's child circuits can carry it; the ones opened
  at login cannot -- or rather, not after what those three runs did
  next.
- **The trigger is a teleport within the region after login.** Logged
  in on the ground at x=12 and walking west straight away, the avatar
  crossed in 5.1 seconds on the child opened at login. Logged in on
  the ground at x=40 and teleported within the region to x=12 first,
  it was held at the line for twelve seconds, as the three runs above
  had been (each began with a login in the region and a teleport within
  it to x=12). A login inside a Linden sandbox crossed in 4.5 seconds.
  So a child opened at login carries a crossing until the avatar
  teleports within the region, and a child opened on a teleport arrival
  still carries one after it. Why is not yet known.
- **What was tried, and did not hold.** The same afternoon, logging on
  a scratch build showed that after such a teleport the neighbour's
  `EstablishAgentCommunication` sometimes comes again every five to
  eight seconds, where it had come once. The viewer answers one by
  asking that neighbour's seed and polling its event queue, and slgo
  does neither. A scratch build doing both crossed after a 28-metre
  teleport twice, and was then held five times in a row, the build
  that would have been merged among them. After an 87-metre teleport
  it never crossed. It was not kept. A walk to the same spot crossed
  every time, with or without it. Whether a real viewer is held after
  a teleport within the region was not tried.

### Open: the crossing after a teleport within the region

Parked on 2026-09-27 for a machine that can run a viewer. What is
established is in the section above: a teleport within the region
breaks the next walk over a border on the child circuits slgo holds,
reliably after a jump of 87 metres. A walk to the same spot always
crosses, and so does a return within seconds of leaving. The one
question left is whether a real viewer is held the same way.

**The experiment.** Pick a border that admits the avatar on both
sides. Two Linden sandboxes side by side do best, since nobody can
refuse entry there; Sandbox Goguen and Sandbox Newcomb, west of it,
were used for the sandbox runs above. Then, from a fresh login:

1. With Firestorm alone, no slgod, log in about 90 metres from the
   west border, and teleport within the region, by double-click or
   the map, to about 12 metres from it. Walk west, holding the arrow
   key, for fifteen seconds. Note whether the avatar crosses, and how
   long it takes. Do it three times, and three more times walking to
   the same spot instead of teleporting, as the control.
2. With slgo, do the same from the same spots: `slgod -neighbours`,
   then `slsh tp X Y Z` and `walk`. `walk` refuses a point outside
   the region and gives up after two seconds of no progress, so the
   runs above used a scratch build, not committed, with two changes to
   `agent/walk.go` through `go build -overlay`: `inRegion` widened to
   -16..272, and `DefaultStallTime` raised to twelve seconds. Then a
   `walk -- -5 Y` walks west over the border.

**What the answer decides.**

- If Firestorm is held too, it is the grid, and slgo is doing what a
  viewer does. Write that up here, and close this.
- If Firestorm crosses and slgo does not, capture what each sends
  after the teleport within the region and before the crossing, and
  compare them. For slgo, `slgod -trace` records every packet on
  every circuit. For Firestorm, use its own message logging, or a
  viewer attached through slgod, whose circuit slgod records under
  `-trace` as well. The things to compare first: the `AgentUpdate`s
  sent to the root after `TeleportLocal` (camera centre, draw
  distance, flags), anything sent on a child circuit, and seed and
  event-queue requests made to the neighbour.

**What was already tried and did not hold.** The instrumented build
and the two candidate fixes are on the branch `fix-login-children`,
which is local to the machine that took these measurements and was
not pushed. Commit 2d4ebca logs what each circuit hears around a
teleport within the region; 9962ef2 asks a neighbour's seed on each
`EstablishAgentCommunication`; e87f089 polls the neighbour's own event
queue; 10c3336 and d70e029 were tried and reverted. Neither kept
fix made the crossing reliable.

A measurement written here follows `CLAUDE.md`: a Linden sandbox may
be named, and any other region, parcel or avatar may not.

### Stage 3 -- the crossing, by promotion

Stage 0 crossed without this, because `moveTo` dialled the new simulator
afresh even though a child circuit to it was already open. So this stage
is the difference between a crossing that works and one that is
seamless, and it should be measured against the two seconds stage 0 took
rather than assumed to be better.

`CrossedRegion` names a handle. If it is a child we hold, the crossing
is not `moveTo`: it is `CompleteAgentMovement` on the circuit that is
already open, and then swapping which circuit is the root -- the region
just left becomes a child in its turn. If it is not a child we hold,
fall back to `moveTo` and take the pause.

Stage 7 of `doc/history/teleport.md` becomes reachable here, and its handler
should need no change: what changes is what happens after it.

### Stage 4 -- the neighbour's own region state

Terrain and objects from a child into that region's store, through the
`Cache` that already hands one store per region uuid. The awkward part
is named in the open questions: local ids are per region and nothing
above `agent` currently carries a region alongside one.

### Stage 5 -- capabilities, if a child needs them

Only if stage 0 says one does. A child with its own seed has its own
capability set and, in a viewer, its own event queue poll. That is N
long polls for N neighbours and wants a reason before it is built.

### Stage 6 -- what clients are told

Objects in another region reaching `sl` at all. This needs a region
dimension on everything keyed by local id, and it is the stage most
likely to be left undone: a client that only ever acts in the region the
avatar is standing in needs none of it.

### Stage 7 -- a viewer that can see across the border

The reason `EnableSimulator` is withheld today is that a viewer handed
one opens its own circuit to a simulator slgod is not part of. Once
slgod holds that circuit itself, the answer changes: the viewer can be
offered slgod's own port for the neighbour, the way the root circuit
already is. This is `doc/history/teleport.md`'s "follow" in another guise and is
the last thing to build, not the first.

## Testing: Pelmar Reach and Pelmar Mill

The measurement that motivated all of this is the test.

- **The wall.** Ground level, twelve metres from the west edge, walk
  west. Today: pinned at x=0. The stage is done when he is in Pelmar Mill.
- **Both directions**, since the west edge and the east edge are
  different simulators' opinions.
- **The offers stop repeating** once taken up -- 57 in 200 seconds is
  the number to beat.
- **A crossing costs less than a teleport**, or holding the circuit
  bought nothing. A teleport measured 355ms to 4.95s; a crossing into a
  region already described should beat that.
- **Two avatars, one daemon**, in adjacent regions, which is what the
  `Cache` reference counting was built for and has still never done.
- **The option off** must leave every one of the numbers above exactly
  as they are today.

## What this does not do

- **Neighbours of neighbours.** One ring, the regions this one names.
- **Anything for a viewer** until stage 7, which may not be built at
  all.
- **Objects across the border for clients** until stage 6, same.
- **Making `sl` region-aware** beyond what stage 6 needs.
