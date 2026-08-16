# Cross-region teleport

Written 2026-08-16, against `09b3366` (v0.3.0). Nothing here is built.

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

**And there is a fault sitting in it.** `Session.AcceptLure` sends
`TeleportLureRequest` and returns. Nothing stops a person accepting an
offer to another region from `slsh waiting`; the request is granted, the
avatar leaves, and this daemon is left holding a circuit to a simulator
the avatar is no longer in. Stage 0 measures what that actually looks
like. Whatever it turns out to be, it is currently reachable from a
prompt with no warning.

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

The three failure shapes, all of which have to be told apart:
`TeleportFailed` instead of `TeleportFinish`; `TeleportFinish` and then
nothing from the new address; and a request that is simply ignored,
which is what a teleport too soon after the last one looks like.

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

### Stage 0 -- watch one happen

No code. `slgod -trace` on both a lure accepted to another region and a
`TeleportLocationRequest` aimed at another region's handle, with the
questions written down first:

- Does `TeleportFinish` arrive on the event queue, with what fields?
- What does the old simulator do afterwards -- a `CloseCircuit`, a
  silence, or does it keep answering pings?
- How long does the idle watchdog take to end the session, and does it
  report anything a client could tell from an ordinary disconnect?
- What does `TeleportFailed` carry when the destination refuses?
- What is the minimum interval between teleports before requests start
  being ignored?

Everything after this stage is written against the answers rather than
against what the protocol is supposed to do.

### Stage 1 -- a region name becomes a handle

`TeleportLocationRequest` takes a `RegionHandle`, and a person types
"Vortera". `MapNameRequest` is answered by `MapBlockReply`, whose blocks
carry `X` and `Y` as grid coordinates (`msg/messages_gen.go:13575`);
the handle is those multiplied by 256 and packed, `x<<32 | y`. There is
no handle arithmetic in the tree yet, so it arrives with this stage, and
its first test is that it reproduces `Agent.RegionHandle()` for the
region the session is already standing in.

A name that matches nothing, and a name that matches several -- the
reply is a list -- both have to be answerable rather than guessed at.
`lookup`'s numbered listing is the shape to follow.

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

### Stage 3 -- Teleport, in sl

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

### Stage 4 -- what the clients are told

A teleport invalidates most of what a client is holding: local ids are
the region's numbering, the object cache describes somewhere else, and
`sl.Session`'s own `locals` map is stale. The server already flushes on
a region change; the clients do not know it happened.

- A `RegionChanged` event on the subscription stream, carrying the new
  region's name and handle, so a client can drop what it holds.
- `sl.Session` clears its per-region caches when it sees one.
- `PresenceResponse` already carries the region and handle, so a client
  that polls needs nothing new.

### Stage 5 -- slsh

- `tp REGION [X Y Z]` -- the refusal in `man/tp.txt` becomes a
  paragraph about what it costs. Default arrival position is the middle
  of the region at ground level, which is where a viewer puts you.
- `waiting`'s `accept` on a lure follows the teleport instead of firing
  and forgetting.
- `where` gains nothing; it already prints the region.

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

## Testing: Pelmar Reach to Vortera and back

The session on `:7898` is standing in **Pelmar Reach at 28, 72, 2001** --
a Linden Homes region, and a skybox two kilometres up. Vortera is a
public sandbox on the same grid. Between them they exercise the awkward
cases:

- **Out and back**, which is the whole thing. `tp Vortera`, confirm the
  region name, the position and that objects are described there;
  `tp "Pelmar Reach" 28 72 2001` and confirm the height survives. A
  height of 2001m is not incidental: coarse location reports it as 1020
  (v0.2.0), so a teleport that lands by coarse location lands wrong.
- **A sandbox is busy**, so arrival is a real object burst rather than
  the forty-one objects Pelmar Reach has described. The store swap and the
  `Cache` reference counting get exercised by something with weight.
- **Two avatars, one daemon**, in different regions at once -- `qi` in
  Vortera and another profile left in Pelmar Reach -- which is what the
  per-region `Cache` was built for and has never been asked to do.
- **The failure paths**: a region name that does not exist, a teleport
  refused by a parcel, and two teleports in quick succession.
- **Getting home if it goes wrong.** Pelmar Reach is home for this avatar,
  so a session that ends up somewhere unexpected comes back with a
  relog and `start = home`. That is the fallback the current man page
  describes, and it stays true throughout.

A run is cheap and repeatable, so the stage 2 test should be a loop:
twenty round trips, checking the region name and position each time and
that the object count in each region settles rather than growing.

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
