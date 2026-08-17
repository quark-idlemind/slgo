# Child circuits to the neighbouring regions

Written 2026-08-16, against `d0584c2`. Nothing here is built.

`doc/teleport.md` ends with a measurement and a wall. Stage 7 built the
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
eight elsewhere. A daemon running `automate` in one region wants none of
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
lost packet and a signal worth watching: once a circuit is open the
repetition should stop, and if it does not, the offer was not taken up
the way the simulator expected.

**`EstablishAgentCommunication`** carries the neighbour's seed
capability. It has never been seen on this grid: two minutes of watching
on 2026-08-16 recorded `EnableSimulator` within a second of login and
not one `EstablishAgentCommunication`. The likeliest reading is that it
follows the circuit rather than preceding it -- the simulator introduces
a neighbour properly once the client has taken the address up -- which
stage 0 can confirm in a minute. If that is right, the current filter in
`viewer/caps.go` has never had anything to catch.

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

### Stage 0 -- take one offer up by hand

Before any of it: a throwaway probe that reads one `EnableSimulator` off
the relay, opens a UDP socket to the address it names, sends
`UseCircuitCode` with this session's own ids, and watches. It answers
nearly every open question in an afternoon:

- Does `RegionHandshake` come back? What else arrives unasked?
- Does `EstablishAgentCommunication` then appear on the ROOT's event
  queue, as predicted above?
- Do the repeated `EnableSimulator` offers stop?
- **Does the avatar then walk over the border?** Walk him at it with the
  same `AgentUpdate` probe that measured the wall.
- Does the neighbour expect anything else -- an `AgentThrottle`, an
  `AgentUpdate` -- before it will describe itself?

Whatever this measures, the rest of the plan is written against it
rather than against a reading of viewer source.

### Stage 1 -- the option, and the neighbours we know of

`Options.Neighbours` and `slgod -neighbours`, both off by default, and
an `EnableSimulator` handler that records handle and address in a list
without connecting to anything. Plus a way to see it: `slsh neighbours`,
or a line in `look`.

Standing on its own this is worth having -- it says what is around this
region, which nothing can answer today.

### Stage 2 -- a circuit that stays up

The `child` type, opened for each neighbour on the list when the option
is on: `UseCircuitCode`, answer `RegionHandshake` and the pings, and
nothing else. Everything the neighbour says is counted and dropped.

That is a stage worth stopping at, because it is the whole of what a
border crossing needs from us if stage 0 says so.

### Stage 3 -- the crossing, by promotion

`CrossedRegion` names a handle. If it is a child we hold, the crossing
is not `moveTo`: it is `CompleteAgentMovement` on the circuit that is
already open, and then swapping which circuit is the root -- the region
just left becomes a child in its turn. If it is not a child we hold,
fall back to `moveTo` and take the pause.

Stage 7 of `doc/teleport.md` becomes reachable here, and its handler
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
already is. This is `doc/teleport.md`'s "follow" in another guise and is
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
