# Cross-region teleport

Written 2026-08-16, against `09b3366` (v0.3.0). Nothing here is built.
Stage 0 has been run, and what it measured is folded in below: where
this says what happens, it was watched happening.

`sl/teleport.go`, `cmd/slsh/man/tp.txt` and `doc/viewer-frontend.md`
each stop at the same sentence -- another region is a different
simulator, and following the avatar there is the daemon's work rather
than a client's. This is the plan for doing it.

## What exists today

**Within a region it works.** `Session.TeleportLocal` sends
`TeleportLocationRequest` at the region the avatar is already in and
waits for the position to agree, because a refused teleport produces no
error and no reply. `slsh tp X Y Z` is that, and it refuses a region
name rather than half-accepting one.

**The store already expects crossings.** `Agent.objects` is an
`atomic.Pointer[Objects]` and `enterRegion` swaps it on every
`RegionHandshake` (`agent/agent.go:484`, `agent/regions.go:132`), giving
back the old region's store to the `Cache` and taking the new one --
which may be a store other agents on this daemon are already keeping.
The terrain and the appearances are dropped at the same point. None of
that has ever run, because a handshake has only ever arrived once per
session.

**The event queue already carries the arrival.** `TeleportFinish` is
`UDPBlackListed` in the template (`msg/messages_gen.go:2151`), so it
comes over the event queue and not the circuit. slgod is the only
poller and fans out (`agent/eventqueue.go`), and `sl.Subscriptions`
already names `TeleportFinish`, `TeleportFailed` and `TeleportLocal`.

**And there is a fault sitting in it, worse than it reads.**
`Session.AcceptLure` sends `TeleportLureRequest` and returns. Nothing
stops a person accepting an offer to another region from `slsh waiting`;
the request is granted and the avatar leaves. Measured, what follows is:

- for **50 seconds** the origin behaves as though nothing happened --
  object updates, coarse locations, land, and a `StartPingCheck` every
  five seconds which slgod dutifully answers;
- then one `DisableSimulator`, which nothing in the tree handles, and
  silence for good;
- **116 seconds** after the teleport the idle watchdog gives up, logging
  `connection ended: agent: simulator silent for 1m6s`;
- eight seconds later slgod reconnects and the avatar is **back where it
  started**, because no root agent was ever established at the
  destination.

Through all of that `where`, `status` and `Presence` answer with the old
region and the old position, and the client stream reports `DISCONNECTED`
and then `REGION_CHANGED: session re-established` -- the same pair any
lost circuit produces. Nothing anywhere names the teleport. To a person
it looks like nothing happened, except that the session blinked two
minutes later.

## What a teleport is, on the wire

	client                     old sim              new sim
	  |  TeleportLocationRequest  |                    |
	  |-------------------------->|                    |
	  |       TeleportStart       |                    |
	  |<--------------------------|                    |
	  |      TeleportProgress     |                    |
	  |<--------------------------|                    |
	  |  TeleportFinish (event queue: address + seed)  |
	  |<--------------------------|                    |
	  |     ... or TeleportFailed, also on the queue   |
	  |            UseCircuitCode                      |
	  |----------------------------------------------->|
	  |            CompleteAgentMovement               |
	  |----------------------------------------------->|
	  |            RegionHandshake                     |
	  |<-----------------------------------------------|
	  |            AgentMovementComplete               |
	  |<-----------------------------------------------|

`TeleportFinish.Info` carries `SimIP`, `SimPort`, `RegionHandle`,
`SeedCapability` and `TeleportFlags` (`msg/messages_gen.go:2131`). The
session id and the circuit code do not change: the same
`Account.CircuitCode` opens the circuit at the new simulator, which is
why this is a teleport and not a relog. Everything after `UseCircuitCode`
is the handshake `agent.Connect` already performs (`agent/agent.go:602`)
-- against a different address.

`TeleportStart` and `TeleportProgress` arrive on UDP; `TeleportFinish`
and `TeleportFailed` both arrive on the **event queue**, measured, even
though the template marks only the first of them `UDPBlackListed`.
Decoding the event is not "read integers out of a map": `LocationID`,
`RegionHandle`, `SimIP` and `TeleportFlags` come as LLSD *binary*, while
`SimPort` and `SimAccess` are integers and `SeedCapability` a string.

The whole exchange takes about 300ms:

	+0.000  --> TeleportLocationRequest  handle=1094014069892352
	+0.101  <-- TeleportStart            flags=0x10
	+0.101  <-- TeleportProgress         "resolving"
	+0.209  <-- TeleportProgress         "Sending to destination."
	+0.298  <== TeleportFinish           (event queue)

The three failure shapes, all of which have to be told apart.
`TeleportFailed` instead of `TeleportFinish` -- it carries both a
machine key and human text, in different places depending on the
failure: a handle that is no region gives `AlertInfo[0].Message` and
`Info[0].Reason` both `"no_host"`, while a region that refuses gives
`"MustHaveVIPStatus"` in the first and a sentence in the second.
`TeleportFinish` and then nothing from the new address. And a request
that is simply ignored, which is what a second teleport looks like: once
the agent has been handed off the origin answers no more teleport
requests at all -- not a rate limit, and not a wait that clears.

## The hard part: the circuit moves under everything

An `Agent` has one `Conn`, one `Send`, one `Recv`, one `Disp`, and six
places outside the package reach through it -- `server/grpc.go:389`,
`server/group.go:92`, `sl/direct.go:161` and `:303`,
`viewer/circuit.go:353`, `server/grpc.go:431` for the stats. A teleport
must not invalidate any of them, so the objects stay and the socket
underneath them moves.

That is already possible: `msg.NewSender` takes a `PacketWriter` and
`msg.NewReceiver` a `PacketSource` (`msg/send.go:24`,
`msg/receive.go:116`), both single-method interfaces. A holder with an
atomic pointer to the current socket satisfies both, and moving means
storing a new one.

**What must be forgotten when it moves** is the interesting half, and
this codebase has met it once already one level up. The new simulator
starts its sequence numbers at 1. The dispatcher's duplicate ring still
holds the old simulator's 1, so the first packets from the new region
are dropped as retransmissions -- which is exactly the bug `abaa2af`
fixed for a second viewer re-attaching, and the reason
`Dispatcher.Forget` and `Sender.Forget` exist (`msg/dispatch.go:421`,
`msg/send.go:206`). They are the primitives this needs. The sender's
unacknowledged packets must go the same way: a reliable message still
in flight to the old simulator will never be acknowledged, and
retransmitting it into the new region would be worse than dropping it.

Beside the socket, three things are per region and all of them are held
as though they were per session:

1. **Capabilities.** `a.Caps` is fetched once from the login response's
   seed (`agent/agent.go:323`). `TeleportFinish` carries the new seed;
   the whole set has to be fetched again and swapped, and anything
   holding a URL across the move is holding a URL into the region the
   avatar left.
2. **The event queue.** One long poll, against `EventQueueCap`, keeping
   an ack sequence that cannot be restarted without losing what arrived
   in the gap. It has to be closed against the old region -- the `done:
   true` post `closeEventQueue` already sends -- and started fresh
   against the new one, with no ack.
3. **The region's own facts.** `regionName`, `regionFlags`, `handle`,
   the handshake, the terrain, the appearances. `enterRegion` and
   `setRegion` already do this from the `RegionHandshake`, which the new
   simulator sends. This part should need nothing new.

## Stages

Each stage ends somewhere it can be left standing.

### Stage 0 -- watch one happen (done)

Run 2026-08-16 against Agni, with a throwaway probe and `slgod -trace`.
What it answered is above and in the stages below; what it changed is
worth stating plainly, since each was written the other way round first:

- `TeleportFailed` is an event-queue message too, not a UDP one.
- `TeleportFinish`'s numeric fields are LLSD binary, not integers.
- The origin gives a **50 second grace period**, then `DisableSimulator`.
  `moveTo` has a generous window, and `DisableSimulator` is a real
  signal that the old region has let go rather than a message to count.
- The default reconnect already lands the avatar back at the origin, so
  the fallback for a botched teleport is better than assumed: do
  nothing, wait two minutes.
- `MapNameRequest` is a prefix search with a sentinel, which stage 1 has
  to cope with rather than treat as an exception.

### Stage 1 -- a region name becomes a handle

`TeleportLocationRequest` takes a `RegionHandle`, and a person types a
name. `MapNameRequest` is answered by `MapBlockReply`, whose blocks
carry `X` and `Y` as grid coordinates (`msg/messages_gen.go:13575`);
the handle is those multiplied by 256 and packed,
`uint64(x*256)<<32 | uint64(y*256)`. There is no handle arithmetic in
the tree yet, so it arrives with this stage. Stage 0 confirmed it:
Pelmar Reach is (43648, 43648) and the session reports handle
47991483540340736, which is what that expression gives. The reply takes
about 110ms.

`MapBlockReply` is not the tidy list the field names suggest, and stage 0
measured all four of these:

- **It is a case-insensitive PREFIX search.** `"Pelm"` returns eleven
  regions; an exact name is just one row among them. A name that matches
  several is the normal case, not the exception -- `lookup`'s numbered
  listing is the shape to follow.
- **Every reply ends with a sentinel block** whose `Name` is the query
  lowercased with its last character removed, `X=Y=0` and `Access=255`.
  It is not a region and must be dropped.
- **A name that matches nothing returns the sentinel and nothing else.**
  That, and not an empty reply, is how "no such region" arrives.
- **The reply can be split across several packets** -- `"Sandbox"` came
  back as 26 blocks and then 8 -- so a caller accumulates until the
  sentinel arrives rather than until the first reply.

`Agents`, `RegionFlags` and `WaterHeight` came back zero for every
region on every run, so `slsh region NAME` cannot say whether a region
is up or how busy it is. `Access` -- 13 general, 21 moderate, 42 adult
-- is the only usable datum besides the position.

Standing on its own this is worth having: `slsh region NAME` printing
where a region is and whether it is up.

### Stage 2 -- the circuit can move

The core, and the only stage that touches `msg`.

- A `socket` holder implementing `PacketWriter` and `PacketSource` over
  an atomic `*net.UDPConn`; `Connect` builds one instead of passing the
  conn directly.
- `Agent.moveTo(ctx, addr, seed)`: dial the new address, swap the
  socket, `Disp.Forget()` and `Send.Forget(ctx)`, re-run the handshake
  (`UseCircuitCode`, `CompleteAgentMovement`), refetch caps from `seed`,
  restart the event queue, close the old socket.
- The signals `inRegion` and `handshook` are one-shot (`agent/agent.go`,
  `signal.once`). A second arrival has to be waitable, so they become
  something that can be re-armed -- or `moveTo` waits on a fresh signal
  it installs itself, which is smaller.
- `AgentThrottle` and the presence updates are per circuit and have to
  be sent again at the new simulator, or it streams nothing.

Testable offline in full: the fake grid already answers a handshake, so
a test can stand up two fake simulators and move between them. The
duplicate-ring bug is a test that fails without `Forget` -- write it
that way round.

### Stage 3 -- Teleport, in sl (done)

`Session.Teleport(ctx, handle, position, timeout)`, alongside the
`TeleportLocal` that is already there, plus `AcceptLure` growing the
same arrival contract. It returns when the avatar is in the new region
and the session is usable there, or with an error naming which of the
three failures happened.

Waiting for arrival is not waiting for `TeleportFinish` -- that says the
old simulator has let go, not that the new one has us. The condition is
`AgentMovementComplete` from the new address, which is what
`Agent.moveTo` already waits for; `sl` waits for the daemon to say the
move is done.

Run 2026-08-16 against Agni: **twenty round trips between Pelmar Reach and
Sandbox Goguen, forty moves, none of them failed.** What that measured:

- **A move costs about 400 milliseconds** end to end -- asked for to
  usable in the new region. Median 425ms over the forty, fastest 355ms,
  and one outlier of **4.95 seconds** that arrived correctly like the
  rest. So the tail is seconds rather than sub-second, which is worth
  knowing before anything treats a teleport as instant.
- **The capabilities really are the new region's.** `SimulatorFeatures`
  answered from `simhost-0526923bb...` in Goguen and
  `simhost-0aaaaaaaa...` in Pelmar Reach, every time, which is the seed out
  of `TeleportFinish` being fetched and used rather than the login one
  being kept.
- **The object stores do not leak.** 58 objects described in Goguen and
  41 in Pelmar Reach, the same two numbers on all twenty visits. The store
  swap and the `Cache` reference counting hold up under repetition,
  which is what the loop was for.
- **The event queue restarts.** Not separately checked, because it
  cannot be faked: `TeleportFinish` arrives on the queue, so hop N+1
  only happens if the queue started against the region hop N arrived in.
  Forty hops is forty of those.
- **The height survives.** Back at 28, 72, **2002** every time, from a
  request for 2001 -- the simulator stands the avatar a metre higher, as
  expected. A landing by coarse location would have been 1020.
- **A refusal leaves the session standing.** A handle that is no region
  came back `no_host` in 223ms and the avatar had not moved.
- Nothing in the daemon's log for the whole run: no reconnect, no
  `simulator silent`, no `DisableSimulator` -- we leave each region well
  inside the fifty seconds of grace stage 0 measured.

### Stage 4 -- what the clients are told (done)

A teleport invalidates most of what a client is holding: local ids are
the region's numbering, the object cache describes somewhere else, and
`sl.Session`'s own `locals` map is stale. The server already flushes on
a region change; the clients do not know it happened.

- A `RegionChanged` event on the subscription stream, carrying the new
  region's name and handle, so a client can drop what it holds.
- `sl.Session` clears its per-region caches when it sees one.
- `PresenceResponse` already carries the region and handle, so a client
  that polls needs nothing new.

Built as a **notice** rather than as anything on the message
subscription stream: `AgentEvent_REGION_CHANGED` already existed for the
reconnect, and a teleport is the same news for a different reason. The
kind gained `region` and `region_handle`, and the reconnect fills them
in too.

Two things were learned building it:

- **The handle decides where the notice is fired from.**
  `RegionHandshake` is where the name, the store and the terrain change
  and is the obvious place; it does not carry the handle, which arrives
  afterwards in `AgentMovementComplete`. A notice from the handshake
  pairs the new name with the old handle -- half right, and therefore
  worse than none.
- **`asking` was a bug, not a cache.** It marks the ids a
  `UUIDNameRequest` is out for and clears on the reply. After a teleport
  the reply never comes, so the mark reads "already asked" for the rest
  of the session and the name is never learned. It is dropped with the
  rest of the region's state.

Run 2026-08-16 against Agni: three round trips, six moves, **six region
changes, each with the right name and handle and none spurious**. The
notice reached a subscriber about **400ms before `Teleport` returned**
to the caller that asked for it. `Info.Region` read "Pelmar Reach"
throughout, which is that field's contract working rather than failing.

### Stage 5 -- slsh (done)

- `tp REGION [X Y Z]` -- the refusal in `man/tp.txt` becomes a
  paragraph about what it costs. Default arrival position is the middle
  of the region at ground level, which is where a viewer puts you.
- `waiting`'s `accept` on a lure follows the teleport instead of firing
  and forgetting.
- `where` gains nothing; it already prints the region.

The shell chose **thirty seconds** where `sl.DefaultTeleportTimeout` is
ninety, which settles that open question for `slsh` and leaves it open
for everyone else. `--wait` overrides it, and it covers the arrival
rather than the map lookup in front of it. `waiting`'s accept takes the
same thirty through a context, which is all `AcceptLure` needed.

`(*Shell).watch` gained a region-change arm, so an arrival nobody typed
at the prompt is said out loud. A `tp` therefore prints twice -- the
session's news and the command's answer -- which is deliberate.

**What the simulator does with the height was measured, and it is not
what everything before this assumed.** It does not stand the avatar on
the ground: the avatar arrives at whichever is higher of the height
asked for and the ground under the point, plus about a metre. Measured
on Agni: 30 came back as 31, 60 as 61, 2001 as 2002, and **0 as the
ground**. So a zero height is how to ask for ground level without
knowing where the ground is, which is what the default wants -- and what
it does not promise is dry land, since a region's middle can be under
its water, as Sandbox Goguen's is.

Four man pages carried sentences that `a2fd50b` made false and now do
not: `tp.txt` rewritten, `answer.txt` no longer says accepting a
teleport ends the session, `regions.txt` no longer says nothing
teleports between regions, `waiting.txt` says an offer is followed. The
same falsehood was in `waiting.go`'s `choices()`.

### Stage 6 -- a viewer attached while it happens

`doc/viewer-frontend.md` left this open deliberately and said to decide
rather than discover. It is a real decision:

- **Refuse.** The viewer's teleport messages are absorbed the way
  `UseCircuitCode` and `LogoutRequest` already are in `fromViewer`, with
  a chat line saying why. Cheap, honest, and the session survives.
- **Follow.** slgod teleports underneath and rewrites `TeleportFinish`
  so the address the viewer is given is slgod's own -- a second circuit
  on a second port, since the viewer expects a new simulator and a new
  handshake, then the same replay `describeRegion` already does for a
  joining viewer. This is closer than it sounds, because the replay
  machinery is the machinery a viewer needs on arrival.

Refuse first. Follow when stage 2 has been running for a while.

### Stage 7 -- walking over the border

A region crossing is the same move with a different trigger:
`CrossedRegion` instead of `TeleportFinish`, and nothing asked for it.
Nothing in the tree handles it today, so an avatar that walks over a
border is in the same position as one that accepts a lure. Once stage 2
exists this is a handler and a test.

## Testing: Pelmar Reach and back

The session on `:7898` stands in **Pelmar Reach at 28, 72, 2001** -- a
Linden Homes region, this avatar's home, and a skybox two kilometres up.

**Vortera is not a region on Agni.** `MapNameRequest` for "Vorter"
returns Vorterhaven Sands, Vorterra Cove and Vortero Flats and nothing
else. **Vortaro** does exist, at (43584, 43712), handle
`47921114796179456`, moderate -- it is next door to Pelmar Reach's (1054,
992) and is probably the name meant. The destination stage 0 actually
teleported to and returned from is **Sandbox Goguen**, (995, 997),
handle `1094014069892352`, general, which is proven to accept this
avatar. Use Vortaro if it accepts us; keep Goguen as the one known to.

Between them they exercise the awkward cases:

- **Out and back**, which is the whole thing. Confirm the region name,
  the position and that objects are described there; then back to
  `Pelmar Reach 28 72 2001` and confirm the height survives. A height of
  2001m is not incidental: coarse location reports it as 1020 (v0.2.0),
  so a teleport that lands by coarse location lands a kilometre wrong.
  Expect the simulator to stand the avatar a metre higher than asked.
- **A sandbox is busy**, so arrival is a real object burst rather than
  the forty-one objects Pelmar Reach has described. The store swap and the
  `Cache` reference counting get exercised by something with weight.
- **Two avatars, one daemon**, in different regions at once -- `qi` in
  the sandbox and another profile left in Pelmar Reach -- which is what the
  per-region `Cache` was built for and has never been asked to do.
- **The failure paths**, all reachable cheaply and none of which ended
  the session in stage 0: a handle that is no region (`no_host`), a
  region that refuses (Sandbox Bricker, adult, `MustHaveVIPStatus`), and
  a second teleport after a successful one, which is answered with
  nothing at all.
- **Getting home if it goes wrong** costs nothing but two minutes: the
  reconnect lands the avatar back at the origin by itself.

A run is cheap and repeatable, so the stage 2 test should be a loop:
twenty round trips, checking the region name and position each time and
that the object count in each region settles rather than growing.

## Open questions

Decisions the stages above depend on and that nobody has made yet, or
findings from building stage 1 and 2 that belong to a later one.

**Which region the test loop uses.** Vortera, which was asked for, is
not on Agni. Vortaro (43584, 43712, moderate) exists and is next door;
Sandbox Goguen (995, 997, general) is the one stage 0 actually
teleported to and returned from. Vortaro has never been tried as a
destination, and a moderate region next to a Linden Homes estate may
refuse an arrival for reasons the map has no field for. Try it once in
stage 3; keep Goguen as the fallback that is known to work.

Stage 3 used Goguen for all forty of its moves and Vortaro is still
untried, so this stays open for whoever wants a second destination --
and a moderate region beside a Linden Homes estate is the more
interesting test of the two.

**A viewer attached across a move gets the wrong seed (stage 6).**
`a.Account.SeedCapability` still holds the region this session LOGGED IN
to, and `cmd/slgod/viewer.go:449` serves exactly that to a viewer being
handed the session. After a move it addresses a simulator the avatar has
left. That is an argument for stage 6 refusing a viewer's teleport
rather than following it, and for refusing to hand a session over at all
while it is mid-move.

**`sl.Direct` goes stale (stage 4).** `Info.Region` and `Info.Caps` are
built once in `Login` (`sl/direct.go:73`) and never revised, so a direct
session that moves reports the region it started in. Whatever stage 4
does about telling a client the region changed has to reach this too.

**Two regions with no id look like one region.** `enterRegion` keys the
object store on the region's uuid, so two regions that both report a
zero `RegionID` would share a store. It has never been reachable --
this is the first stage that enters a second region at all -- and it is
not obviously worth guarding, but it is worth knowing before a strange
report about objects from somewhere else.

**A map lookup can be truncated by another one.** `MapBlockReply`
carries nothing that says which question it answers and the daemon
relays by message number, so two clients looking regions up at once can
end each other's lists early. Documented at the head of `sl/worldmap.go`
along with the half that is fixed. It costs a listing shorter than the
grid's answer, never a wrong one, and it wants a second look if
`regions` ever becomes something a script depends on.

**The next release is a minor.** Stage 2 removed `Agent.Conn` and turned
`Agent.Caps` from a field into a method, and stage 4 added a method to
`sl.Backend`, which anything implementing that interface outside this
tree has to grow. All three are exported, and v0.2.0 set the precedent:
a caller reading only a patch number would not look.

**A client can be told before the capabilities have moved.** The notice
fires from `AgentMovementComplete`, on the dispatch goroutine, while
`moveTo` is still between the arrival and the capability swap -- so a
client that reacts to a region change with a capability-backed request
inside that window addresses the region it has left. Measured at about
400ms of daylight. Nothing has hit it, because the notice is news to act
on rather than a starting gun, and the alternative -- firing after the
whole move -- would put the notice behind the object updates the new
region is already sending. Worth knowing before something reacts to a
region change by fetching.

**A dropped region change is worse than a dropped anything else.** Every
hop from the daemon to a subscriber drops rather than blocks, because
blocking any of them stops a stream or the dispatch goroutine; a session
that missed one goes on believing it is where it is not. The buffers are
sized for one per teleport rather than one per packet, which is the
whole of the defence. It wants a second look only if a client can stop
reading for minutes at a time.

**A teleport's failed dial should probably end the session.** `moveTo`
deliberately leaves the session where it was when the dial fails,
because that is the one failure that changes nothing -- but a dial after
`TeleportFinish` is different: the avatar has already been handed away,
so there is nothing to stay for, and the watchdog takes about a minute
to notice. Stage 3 did not override the policy. It may be that the
caller rather than `moveTo` should decide.

**A move with an unusable seed has no way back.** The circuit moves and
the capability set is dropped, which is right -- URLs into the region
left behind are worse than none -- but nothing ever fetches a set again,
so the session runs on with no capabilities and no event queue. Not
reachable on Agni, where the seed is always a URL, and there is no
recovery if it ever is.

**Two clients teleporting one avatar read each other's answers.** The
same relay-by-name trouble as the map lookup, and nearly harmless for
the same reason it is not harmless there: a finish another client
provoked is still this session going somewhere, and is still followed by
waiting to arrive. What it can do is report one client's refusal as
another's. Documented at the head of `sl/teleport.go`.

**Ninety seconds is a lot.** `DefaultTeleportTimeout` was set before
anything had been timed. Forty moves now say a teleport costs 355ms to
5 seconds, so the constant is two orders of magnitude above what it
covers, and the only argument for keeping it there is that nobody has
seen a slow grid day. Stage 5 answered this for `slsh`, which uses
thirty seconds of its own and no longer inherits the ninety; every other
caller still does, and the constant itself is untouched.

**`README.md` still says there is no teleport.** Its "Not done" section
reads "No region crossing, no teleport beyond the current region". The
second half has been false since `a2fd50b` and the first is stage 7, so
the sentence wants splitting rather than deleting.

### Not about teleport

Two things found while working on this that belong nowhere else yet.

`agents` pads the profile name to ten characters (`cmd/slsh/objects.go:317`),
so a longer name pushes every column after it out of line -- seen with a
24-character profile on this machine. `ls -l` has the same shape and
gets away with it because a path is last on the line; here the name is
first.

`rez` does not name the active group. `rezAt` (`sl/build.go:237`) fills
in `ObjectAdd` without a `GroupID`, where `RezFromInventory`
(`sl/inventory_ops.go:383`) takes one and sends it. A parcel that grants
building to a group rather than to individuals should therefore refuse
`rez` and allow `place`, which would present as the land being wrong.
Unverified: it is a reading of the source, not a measurement.

## What this does not do

- **Neighbouring regions.** A viewer keeps circuits to the simulators
  around it so that the avatar can see across a border and walk over it
  without a pause. slgod connects to one at a time, by choice
  (`c1e9d11`), and nothing here changes that. Crossing a border under
  stage 7 will be a visible pause rather than seamless.
- **Teleport routing.** No landmarks, no home, no map double-click, no
  "teleport to a person" beyond accepting the lure they send.
- **Anything the login response only says once.** The friends list and
  the inventory root come from login, not from a region, and survive a
  teleport untouched. This is worth stating because it is the reason a
  teleport is cheaper than the relog it replaces.
