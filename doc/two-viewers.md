# Two viewers on one slgod session

Written 2026-08-15, against commit `abaa2af`. Nothing here is built. It is
what was found while making `slsh viewer --launch` work, written down
before it is lost.

`doc/viewer-frontend.md` is the design this builds on: slgod holds the
grid session and a viewer is handed *that* session rather than logging in
again.

## The two things somebody might want

They are not the same problem and only one of them is mostly plumbing.

**Two views of one avatar.** A first person view, and a second one
watching the point where people arrive in the region. Hard, and the hard
part is not the relay -- see "Who drives" below.

**The same view from more than one machine.** A viewer at a desk and
another on a laptop, either of which might be the one somebody is sitting
at. Doable. Only one drives at a time, and which one it is can follow
whichever last saw a person touch it, or be said outright from `slsh`.

## What works today, measured

**Two avatars on one daemon: yes.** Measured on Agni with one
`slgod -viewer 127.0.0.1:9000 qi holt`:

	20:45:05 viewer: listening on 127.0.0.1:54578 for Quark Idlemind
	20:46:28 viewer: listening on 127.0.0.1:55559 for Taren Holt
	20:45:11 viewer: a viewer appeared at 127.0.0.1:58674
	20:46:35 viewer: a viewer appeared at 127.0.0.1:59715

Both handed over in full, both told their own avatar's position, both
still attached afterwards. This needs no new code: circuits are keyed by
profile and the login endpoint routes by avatar name (`hostedNamed`), so
each avatar already gets its own circuit on its own socket.

What it does need is a `viewer_password` in each profile -- that setting
is what marks a profile as handable at all (`cmd/slgod/viewer.go:324`) --
one daemon holding both avatars, and, for the second Firestorm, `open -n`
with `--multiple`, which maps to `AllowMultipleViewers`
(`cmd_line.xml:229`, checked at `llappviewer.cpp:3572`).

**Two viewers on one avatar: no, and it fails badly.** One circuit per
profile means one `peer` address. `notePeer` (`viewer/circuit.go:191`)
moves the peer to whoever a packet last came from, and since `abaa2af`
each move also forgets the departed viewer's sequence numbers and pending
retransmissions. Two live viewers would take the circuit from each other
in turn, each move throwing away the other's state. Nothing refuses it:
`find()` hands over a session that already has a viewer without so much
as a log line.

## What a Circuit already owns

This is the good news, and the reason the second use case is close. A
`viewer.Circuit` is already the per-viewer unit, and nothing in it knows
it is the only one:

- its own UDP socket, hence its own port -- `Addr()` is what goes into
  the handover as `sim_ip`/`sim_port`, so two circuits hand two viewers
  two different ports with no arrangement needed;
- `peer`, and `joined`;
- its own sender: sequence numbers and unacknowledged packets;
- its own dispatcher, including the duplicate ring that caused the
  re-attach bug;
- `pending`, the appearances held back until the viewer knows the avatar
  they belong to;
- both ack domains stay separate by construction: `forward()` re-sends a
  message on the session's circuit rather than relaying bytes, so neither
  side ever sees the other's numbering.

## What is keyed by profile and would have to be keyed by viewer

Three maps and a URL.

1. **`circuits`** (`cmd/slgod/viewer.go:49`, `circuitFor` at `:387`). Profile to one
   circuit; `circuitFor` returns the existing one, which is exactly why a
   second login lands on the first viewer's socket. It becomes a set, and
   each login makes a circuit.

2. **`relayFor`** (`:157`) hands each simulator packet to *the* circuit.
   It becomes a loop over the set. Each circuit keeps its own `joined`
   gate and its own replay, so a viewer that joins late is described the
   region while the other carries on -- that already works, it is what
   `describeRegion` does, it has simply never been asked to do it twice.

3. **`queues`** (`:49`, `queueFor` at `:122`). One `EventQueue` per profile, and
   this is the one that cannot be shared. The file that implements it
   makes the argument itself (`viewer/caps.go:14-22`): a queue has one
   reader, two pollers split the events between them at random, and
   neither can tell it is missing any. That is the same argument one
   level down. slgod already solved it once -- it is the only thing
   polling the simulator's queue and hands copies on -- so the answer is
   the same trick again, one fan-out copy per viewer.

4. **The cap URL.** `/cap/<profile>/event` (`serveCap`, `:426`) carries
   the profile because "one daemon hosts several avatars and their queues
   must not be confused". It would carry a per-viewer token minted at
   handover instead. The comment there already notes it is not a secret
   and does not need to be, since the login endpoint decided who may
   attach; the same holds per viewer.

## Who drives

This is the part that is not plumbing, and it is where the two use cases
part company.

The simulator streams what it streams because of the camera. `takeCamera`
(`viewer/circuit.go:571`) says it plainly -- "the camera, which is not
decoration: the simulator works out what to stream from it" -- and while
a viewer is attached its camera replaces the session's own. There is one
agent, one `AgentUpdate`, one camera.

**Why two independent views is hard.** A second view aimed somewhere else
needs the simulator to describe objects the camera never looked at, and
it will not: nothing beyond what is asked for is described at all, which
is the point `SetDrawDistance`'s doc comment makes about a floor on what
can be found. Two viewers alternating cameras would make the simulator
jitter between two positions and stream for neither. A second view of the
arrival point would show whatever the driver's camera had already caused
to be sent -- stale, or empty. Making it real means asking the simulator
for a second interest list, which is not something one agent can do; the
honest ways are a second avatar (which works today) or accepting that the
second view lags the first.

**Why the same view from two machines is straightforward.** Only one
viewer drives at a time and the others are shown what the driver's camera
earned. The rule: a non-driver's `AgentUpdate` is absorbed rather than
forwarded -- the same treatment `UseCircuitCode` and `LogoutRequest`
already get in `fromViewer`, and for the same reason, that it would tell
the simulator something untrue about a session it shares.

Choosing the driver, two ways, not exclusive:

- **Whichever last saw a person.** A viewer sends `AgentUpdate`
  continuously whether or not anybody is touching it, so "human input"
  cannot mean "sent one". It means a *change*: control flags that are not
  zero, or a camera that has moved more than some small amount. Both are
  in the message already, so this costs a comparison per update and no
  new protocol. It wants hysteresis, or two viewers with a drifting
  camera would hand the role back and forth.
- **Said outright from `slsh`**, which is the answer when the automatic
  rule guesses wrong, and the one to build first because it is what makes
  the automatic rule testable.

## What slsh should show

`viewer` today answers for the session: where the endpoint is, and
whether *a* viewer has taken it. With several it should list them --
each circuit's address, when it joined, whether it is driving, and how
much has gone to it -- and let the driver be set. The census already
counts per circuit; what is missing is a name for each one and a way to
ask.

## Smaller things found on the way

- Every join asks the simulator to describe the objects in range again,
  so a second viewer arriving costs the first a burst of updates it
  already has. Harmless, but it is the sort of thing that looks like a
  bug in a trace.
- `census` and `trace` are shared across circuits. Two viewers would
  interleave in one trace with nothing saying which is which; a
  per-circuit label would be wanted before debugging anything.
- The login endpoint does not refuse, or even mention, a second login to
  a session that already has a viewer. Whatever is decided above, saying
  something is better than the silence there is now.

## Roughly what it would cost

The second use case is the three maps keyed one level finer, a fan-out
loop, a per-viewer queue and token, the absorb-if-not-driving rule, and
the `slsh` listing. A few days in the shape this codebase likes, with the
queue the only part needing real care.

The first use case is not a plumbing change and should not be estimated
as one.
