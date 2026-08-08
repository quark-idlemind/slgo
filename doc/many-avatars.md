# Several avatars in one slgod

Written 2026-08-07, at commit `4be5a9d`.

**Status.**  Stages 1, 2, 4 and 8 are built and running; the daemon
holds example, qi and helper with example as the default, twelve auto objects
apiece, and nine concurrent benchmarks have been run across the three.
Stage 4 is complete including `SLGO_AGENT` and the sweep of hardcoded
`-agent` defaults.  Stages 3, 5, 6 and 7 are done and confirmed against
the live grid.

**Every stage in this document is now built.**  What is left is written
at the end, under "What this still does not do".

slgod has been able to hold several sessions since it was written --
`slgod -listen :7807 example qi` works today, every RPC carries the agent
name, and `--agent` is already on slsh, automate and autobench.  What is
missing is not the capacity but the *management*: which avatar a client
gets when it does not say, how one is brought up after the daemon
started, and how one is put down and made to stay down.

The intent is that a person can say "I want quark in Firestorm for an
hour", have slgod let go of quark and not fight for it, and have quark
come back when a client asks for it by name.


## What is already right, and must stay right

Worth stating so that none of the work below quietly breaks it.

- **Sessions are keyed by profile name** in `server/server.go:51`, and
  every request names one (`proto/slgo.proto:203` for the stream,
  `server/grpc.go:597` for the rest).
- **Locks are per session.**  `Hosted.lockSet()` in `server/lock.go`
  means example's auto-object lock does not block qi's, so two avatars can
  run autobench at the same time without queueing behind each other.
- **The auto objects are per avatar.**  `UseAutoN` in
  `internal/session/session.go` finds them by `EnsureAttached` against
  that session's own inventory, so nothing is shared and nothing needs
  to be told apart.
- **No package level state.**  `agent/multi_test.go` runs five complete
  sessions against separate simulators concurrently and checks none sees
  another's traffic.  It exists to catch a cache or registry added
  later.  Keep it passing.

Two smaller things that are fine as they stand.  All profiles present
the same made-up MAC and ID0 (`cmd/slgod/main.go:78`), which is what a
viewer running alts on one machine does anyway, and a profile can
override it.  autobench's padding cache is keyed only by the base
script's hash, which is correct across avatars: a script's memory does
not depend on who owns it.


## Stage 1 -- the group belongs in the profile

`--group` is one flag applied to every profile
(`cmd/slgod/main.go:38`, loop at `:152`).  With two avatars in different
groups only one can be satisfied.

The active group is per-account data that does not vary by invocation,
so it belongs in the profile file next to the password.  Add `Group` to
`agent.Login` (`agent/login.go:24`), parse a `group = ` key in
`agent/profile.go:74`, and have the startup loop use it.  Name or uuid,
resolved by the `chooseGroup` that is already there.

Keep `--group` as an override, but make it name a profile --
`--group qi=Builders` -- since a bare value cannot mean anything sensible
once there are two avatars.

Why this matters more than it looks: a login starts with no active
group, and a parcel that grants building to a group refuses an avatar
with none active.  The refusal blames the land, which is the wrong place
to look, so getting this wrong costs an hour of confusion rather than an
error message.

### And the group must survive a reconnect

Found in the field, 2026-08-07, on a live session that had reconnected:
the active group had dropped to NONE and the avatar could no longer
build.

It is settled exactly once, by a loop in `cmd/slgod/main.go:152` that
runs after every `Host` has returned, and nothing in the `server`
package ever sends `ActivateGroup`.  A reconnect goes through
`supervise` -> `h.reconnect` -> `agent.Connect`
(`server/server.go:224`), which is a fresh login -- and a fresh login
has no active group.  The comment at `cmd/slgod/main.go:151` already
says "a restarted slgod is a fresh login, so it must be settled again";
that is just as true of a reconnect, and the reconnect is the case it
does not cover.

The failure is quiet and badly signposted.  Building fails, the
simulator blames the parcel, and nothing says the daemon dropped
something it was holding on the operator's behalf.

The fix belongs in the server rather than in `main`, because state the
server owns is state it can reapply: keep the resolved group uuid on
`Hosted` when it is first settled, and re-send `ActivateGroup` after
each successful reconnect, next to the `REGION_CHANGED` notify at
`server/server.go:218`.

Re-send the uuid; do not re-run `chooseGroup`.  It calls
`a.WaitGroups(ctx, 15*time.Second)` because group membership is not in
the login response and arrives later on the event queue
(`agent/login.go:318`), so re-deriving would stall every reconnect for
up to fifteen seconds to reach an answer that cannot have changed.

This is worth doing whether or not the rest of the plan is, and it is
the one item here that fixes a fault rather than adding a capability.


## Stage 2 -- one bad profile must not take the others down

Any profile failing to log in is `log.Fatalf` (`cmd/slgod/main.go:100`),
which kills the daemon including the sessions that came up.  Right for
one avatar, wrong for two: qi's expired password should not take example
down.

Log the failure and carry on.  Exit only if *nothing* came up, since a
daemon holding no sessions has nothing to serve.

Independent of everything else here, and worth doing first because it is
five lines and it makes the rest safe to experiment with.


## Stage 3 -- the state of an agent, and what ListAgents says

The stages that follow need a word for an agent that is not connected,
and there are four distinct cases a person must be able to tell apart:

| state | meaning |
|---|---|
| `HOSTED` | logged in and serving |
| `CONNECTING` | a login is in flight |
| `CONFIGURED` | a profile exists, nothing has asked for it |
| `STOPPED` | deliberately logged out; do not bring it back unasked |
| `FAILED` | tried, could not; with the reason and when to retry |

`ListAgents` (`server/grpc.go:382`) returns only what is hosted, so a
client cannot tell "I have never heard of qi" from "qi is deliberately
down".  Extend it to list configured profiles too, and add a state and a
detail string to `AgentInfo` (`proto/slgo.proto:296`).

`Hosted.stopped` (`server/server.go:84`) already exists and is already
checked by the supervisor (`:185`); it is the seed of `STOPPED`.  What is
new is that a stopped agent has to stay in the map to be reported,
rather than being dropped from it as `Close` does now.


## Stage 4 -- picking an agent when the client does not name one

Today `sl.AttachConn` (`sl/hosted.go:46`) takes the only session and
refuses to guess between several.  slgod should pick instead -- but the
pick has to be deterministic, or the same unchanged script drives example
today and qi tomorrow because of what happened to be up.  That failure
is silent: an autobench run against the wrong avatar installs a script
in the wrong attachment and reports a plausible number.

### The rule: the agent that has been logged in the longest

Of the hosted agents, the default is the one hosted earliest.  It is
worth stating as the property it gives rather than as the procedure:

> The default changes only when the default itself is logged out.

Adding an avatar never moves it, a client attaching never moves it, and
a reconnection never moves it.  Nothing a person does casually can
change which avatar a bare command drives, which is the whole point.

It also answers the awkward case directly.  Start with example; add qi
later; log example out, and qi becomes the default; log example back in, and qi
*stays* the default -- example returns at the back of the queue.  That is
the right answer, because the alternative ("the primary avatar resumes")
is a preference rather than a fact, and a preference should be said out
loud rather than inferred.  `SLGO_AGENT` is where it gets said.

Four ways this can be got wrong in the implementation:

- **Rank belongs to the `Hosted`, not to the `agent.Agent`.**  The
  supervisor replaces the agent underneath on every reconnect
  (`server/server.go:224`), so a rank read from the live session would
  send the default to another avatar because of a network blip.  `Hosted`
  survives reconnection by design; the rank goes there.
- **Stamp when the session becomes hosted, not when it was asked for.**
  Stamping at request time looks tidier and quietly breaks the
  invariant: request example, whose login takes eight seconds, then qi,
  whose login takes two, and qi is the default at the four-second mark
  -- until example arrives holding the lower rank and takes it away from an
  agent that never logged out.  Stamping on arrival cannot do that,
  because a new session always gets the highest number and so always
  slots in behind the incumbent.

  A `uint64` counter is enough, incremented under the same lock that
  reserves the name (`server/server.go:114`).  Allocation is therefore
  serialised by construction: no clock is consulted and two sessions
  cannot be given the same rank.

  This does mean the order is the order logins *finished*.  Startup
  logins are serial (`cmd/slgod/main.go:65`), so that is command-line
  order and the rule reads as intended.  Parallelising startup for speed
  would make the default depend on which login the server answered
  first; if that is ever done, assign the startup batch's ranks in
  command-line order and let arrival order govern only what comes after.
- **Logging out clears the rank.**  This is what puts example at the back of
  the queue when it returns, and it is the whole of the mechanism for
  the case above.
- **Do not read the order from `Names()`**, which sorts alphabetically
  (`server/server.go:275`).  With example and qi the two orders coincide,
  which is exactly how that would survive testing and then bite with a
  third avatar.  Rank order and display order are different things.

### The rest of the rule

- **Honour `SLGO_AGENT`**, so the choice is made once per shell rather
  than per command.  Precedence: `--agent`, then `SLGO_AGENT`, then
  slsh's config `agent` key (`cmd/slsh/config.go:26`), then the daemon's
  default.
- **Say which avatar was attached to** whenever the client did not name
  one.  The `Attached` frame already carries `AgentInfo` with the name,
  so this is a line of output and not a protocol change.  It matters
  more under a longest-hosted rule than it would under a fixed one: the
  default now depends on the daemon's history, and there is nothing on
  disk a person can read to work out what a bare command will drive.
- **Report it.**  `agents` should list in rank order and mark which one
  is the default.
- **Resolve it under the map's lock**, so that a logout landing between
  the pick and the attach cannot hand a client an agent that is on its
  way out.

The constraint that makes stage 6 work: **the default pick chooses only
among currently hosted agents.**  A client that names nothing must never
cause a login.  Otherwise quark comes back from the dead while you are
using it in Firestorm.

Note also that the daemon's own `Stream` handler does not default an
empty name at all (`server/grpc.go:260` calls `s.Agent`, not
`s.lookup`); the resolution happens client-side before it gets there.
Once slgod picks, move that logic into `Stream` and let `AttachConn`
send the empty name through.


## Stage 5 -- logging in an agent that was not started

A client naming an agent slgod does not hold should be able to have it
brought up, provided a profile exists for it.

I would make this **explicit rather than implicit**: a flag on `Attach`,
or better a separate `Host` RPC that a person invokes as `slgo host qi`.
The reason is that bringing an avatar into the world is a visible act --
an arrival, a presence, a group chat notice -- and it should be
something a client asked for rather than something that happened to it
because of a typo in a stale config file.

Four things fall out of it, and none is optional:

- **Single-flight.**  Two clients attaching to an unhosted agent at once
  must produce one login.  `Host` currently reserves the name under the
  mutex and hands the loser "already hosted"
  (`server/server.go:112-119`); that has to become "wait for the one in
  flight and share its result".
- **The deadline.**  Attach is near-instant today; this makes it a
  login, a region handshake, capability fetching and group activation.
  Clients need a much longer context for it, and it is worth sending a
  notice on the stream so that a person watching knows why nothing is
  happening.
- **Do not retry-storm.**  An expired password must not mean every
  attach hits the login server again.  Remember the failure with a
  backoff -- `ReconnectDelays` (`server/server.go:164`) is the right
  shape and the right reason: a login server throttles a client that
  hammers it, and the throttle then presents as a different bug.
- **What happens when the client leaves.**  The session stays up.  Not
  paying login cost is the entire point of slgod, and a benchmark that
  logged out between runs would pay it every time.


## Stage 6 -- logout, and making it stick

`slgo logout quark`: log the avatar out and do not bring it back until a
client asks for quark **by name**.

Mechanically small, because `stopped` already exists and the supervisor
already honours it (`server/server.go:185`).  What has to be added:

- **Refuse while clients are attached**, listing who they are, with a
  force flag to do it anyway.  Someone's benchmark is probably mid-run,
  and a run killed in the middle leaves a script in the auto object and
  a half-written reading in its linkset data.  Harmless, but the run is
  lost and the person should get to decide that.
- **Release the locks.**  This is already handled -- `detach` calls
  `releaseAll` (`server/server.go:343`) -- but the streams have to be
  ended with a reason the client can print, rather than dropped.
- **Stay in the map as `STOPPED`**, per stage 3, so that `agents` can
  report why quark is not there.


## Stage 7 -- do not fight the viewer

This is the one that is not in the original list and without which the
rest does not deliver what it promises.

The Firestorm case happens in both directions.  If quark is logged in
through a viewer *without* telling slgod first, the grid ends slgod's
session -- and `supervise` treats every end it did not ask for as
something to recover from, so five seconds later it logs back in and
kicks the viewer out.  The daemon would be in a fight with its user, and
the daemon would win, repeatedly.

So: **a session that ended because the avatar logged in elsewhere is not
retryable.**  Set `stopped`, record it as `STOPPED` with that reason, log
it plainly, and wait to be asked.

`KickUser` was already handled -- it records the grid's reason and ends
the session -- so the work was smaller than this plan first claimed:
make the reason readable (`agent.Kicked`), give it a type so callers can
tell it from a lost circuit (`agent.Kicked`, `agent.Retryable`), and
have `supervise` ask.

The rule this establishes -- **the viewer always wins** -- is the right
one, and it turns stage 6 into a convenience rather than something that
must be remembered before opening Firestorm.

**Settled against the live grid, 2026-08-07.**  A second login produces
`KickUser`, carrying the grid's own words:

	The system has logged you out because you are attempting to log
	in from another location.

Tested by running a second slgod for an avatar the first was holding.
The first reported the kick and stayed down; the other two sessions it
held were untouched.

One thing that fell out of the test and is not obvious: staying down is
not enough on its own.  A session that has ended still answers questions
out of what it last heard, and its sends go nowhere, so `status` on it
looked perfectly healthy.  A stopped session is therefore never the
default, attaching to it by name is refused with the reason, and a
listing says NOT CONNECTED rather than showing a region it is no longer
in.


## Stage 8 -- a pool of auto objects, instead of one set

Independent of the avatar work, and after it rather than in it, because
it changes the shape of the lock table that stages 3 to 6 leave alone.

Today `UseAutoN` takes a single lock covering the whole set
(`internal/session/session.go`), so a second benchmark waits for the
first even when there are objects going spare.  The proposal is a pool:
a client asks for N of M and gets whichever are free.

### How many, and what actually bounds it

There are exactly **eight** HUD points -- `sl/attach.go:15`, numbers 31
to 38 -- and `AutoPoints` uses four of them.  The other four cost
nothing: nothing else wants a HUD point and they are not part of how the
avatar looks.  So **8 is a one-line change** to a slice.

Past 8 means either body points, 1 to 30, which do affect appearance, or
several objects on one point.  The latter is possible and is the better
answer: `AttachAdd = 0x80` is already defined (`sl/attach.go:62`) and
nothing uses it -- `Wear` sets `AttachmentPt` bare (`:141`), so every
attach replaces what is there.  With the bit set the object is added
alongside and the ceiling becomes the 38-attachment total.

Which makes the comment at `internal/session/session.go:102` -- "An
attachment point holds one object, so more objects means more points" --
the thing standing between the present arrangement and 12.  It appears
simply to be out of date.  **Test it before relying on it**: attach two
objects to one point under `AttachAdd` and check both come back
distinctly from `WornFromItem`, which keys on item id and so ought to
tell them apart.

### What a bigger pool is and is not worth

Not a faster search.  `quarterSearch` issues exactly three probes a round
and gives up below three spare objects (`cmd/autobench/main.go`, the
`len(b.spare) < 3` guard).  Seven probes would make it an eighth search:
for a range of about 512 narrowing to 8, three rounds become two.  One
round, about a second.  Every doubling of the pool buys a logarithmic
gain for a linear cost and runs out almost at once.

The gain is **several benchmarks at once**, which today serialise
completely.  Size the pool for the number of concurrent runs wanted, not
for the width of the search.

### Who keeps track of M

slgod, as **integers**.  It leases slot numbers and knows nothing about
attachment points; the client maps a slot to a name and a point.  That
keeps inventory work client-side, where it already is -- `EnsureAttached`
rezzes, names, takes and wears, and none of that is anything slgod does
-- and lets daemon and client versions drift.

The rule that makes it safe, and it must be written where someone
editing the list will see it: **`AutoPoints` and `AutoName` are
append-only and are never reordered.**  If two client versions disagree
about what slot 5 means, the lease stops providing exclusion at all and
nothing detects it -- two runs quietly share an object and both report
plausible numbers.

### The one thing that must not be got wrong

The acquire is a single atomic all-or-nothing operation on the server:
"give me N of M", the server picks which.  **Not** N individual locks.
That is exactly the deadlock the present single lock was built to avoid,
and `session.go:118` says so: two benchmarks each holding some and
waiting for the rest.

The existing `locks` table (`server/lock.go`) has the right machinery but
the wrong shape -- it is keyed per name with a queue per name.  A pool
needs *one* FIFO queue, or a request for 4 starves behind an endless
trickle of requests for 1.

### Grant partially

`UseAutoN` already treats fewer objects than asked for as fine rather
than an error; the extras only make it quicker.  So the interface is
`min, max`: one slot held exclusively for the measured object, up to N
more if they are free, and never wait when anything is available.  That
turns today's queue into a queue only when the pool is genuinely empty.

### Two costs worth naming

Every worn object is in the interest list wherever the avatar goes, and
each keeps the benchmark script under its fixed name after a run.  A
dozen idle scripts is real region memory, held everywhere the avatar
stands.  Fine on quiet land, less fine on a busy region -- and if it ever
matters, the answer is to remove the script when a run finishes rather
than to shrink the pool.

The first use of a new slot pays the expensive path: rez, name, take,
wear.  Only once, but it is tens of seconds, and it will land on whoever
first asks for a larger N rather than on whoever grew the pool.


## What this still does not do

- **Teleport between regions.**  `tp` moves within one.  Another region
  is another simulator: the session has to be established there, a new
  circuit opened, new capabilities fetched and the old circuit closed.
  Arriving elsewhere is still a matter of logging in there.

- **Keep an offer for a client that was not attached.**  Inventory and
  friendship offers reach clients attached when they arrive; slgod
  relays them and does not hold them.  A shell started afterwards never
  sees the offer, and the transaction id it would need to answer with is
  not recoverable.

- **Tell one client from another by name.**  `logout` names what is
  attached, but every client authenticates as "slgo", so it can say how
  many and not which program.

- **Start an agent for a profile added since slgod started** -- it will,
  since the profile list is read each time, but nothing re-reads a
  profile that CHANGED while a session from it is running.


## Order, and the one thing to be careful about

Stages 1, 2 and 3 are independent and safe.  Stage 4 depends on 3 for
the states.  Stages 5 and 6 depend on 4 for the rule that a nameless
client cannot start an agent.  Stage 7 is independent of all of them and
is the one worth doing early, because until it is done, testing stages 5
and 6 by hand means logging an avatar in through a viewer and watching
slgod fight it.  Stage 8 is independent of all of them and should be
last, because it rewrites a lock table the others depend on being
stable.

**Every stage requires restarting slgod**, which ends every session it
holds.  Check that nothing is mid-run first -- `ClientCount` per hosted
agent is the honest answer, and an autobench run can legitimately take
minutes.

The one thing that will surprise a user rather than a developer: the
moment a second avatar is hosted, every command run without `--agent`
changes behaviour.  Before stage 4 it starts *failing* with "name one";
after stage 4 it silently picks.  Anything with a hardcoded flag default
-- and several `cmd/slgo-*` programs hardcoded one --
keeps working but may now be naming an avatar that exists and is not the
one meant.  Worth a sweep of those defaults at the same time as stage 4,
and worth a line in `doc/guide.md` when the whole is done.
