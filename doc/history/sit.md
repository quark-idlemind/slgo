# Sitting, and standing up again

> **A plan, not a description of what slgo does.**  Written before the
> work and kept for the measurements folded into it; the stage headings
> below record what was found, including where the plan turned out to be
> wrong.  For current behaviour see `man sit`, `man stand` and
> `sl/sit.go`.  [doc/history/README.md](README.md) says why this is
> here.

Written 2026-08-17, against `8389366`. **All of it is built**, and every
stage was run against Agni: where this says what happens, it was watched
happening. Two things it first said turned out to be wrong and are
corrected in place rather than quietly edited away -- that no new RPC
was needed, and that an edge triggered flag only has to be sent once.

## Why this before walking

An avatar cannot move. `tp` goes to a position and the neighbour
circuits let a crossing happen, and between those two there is nothing
that puts one foot in front of the other. The obvious next thing is a
walk, and the obvious next thing is the hard one: the simulator never
says "you bumped into something". A blocked walk and a walk still in
progress are the same silence, so walking honestly means guessing at
collision from the object cache -- a viewer's physics, invented here.

Sitting is the same question with the simulator answering it. The
client names a thing; the simulator decides whether it can be reached,
moves the avatar there, and says which. It refuses in words. Everything
a walk would have to guess is already on the wire, and one of the
measurements below is that a sit **moves the avatar up to about ten
metres**, obstacles and all. That is not a walk, but it is the first
locomotion in this program that cannot be wrong about where it ended up.

## What stage 0 measured

Run 2026-08-17 on Agni, against the session slgod holds for this
author's avatar, with a throwaway probe attached over gRPC that sends
the messages by hand and prints everything that comes back. Three
one-prim boxes were rezzed in a private skybox at Pelmar Reach, sat on, and
taken back in.

### `AgentRequestSit` is the whole request

The viewer's pair is `AgentRequestSit` and then `AgentSit`, and every
account of the protocol lists both. Measured, the first one is
sufficient and the second does nothing:

	0.000  AgentRequestSit -->
	0.106  <-- AvatarSitResponse   autopilot=true pos=<-0.59, 0, 0.55>
	0.123  <-- ObjectUpdate  me  parent=83600601
	6.007  AgentSit -->
	       (nothing at all)

The avatar was seated 123 milliseconds after asking and six seconds
before the message that supposedly seats it. `AgentSit` is what a
viewer sends when its own autopilot has finished walking the avatar
over; the simulator has already reparented by then. So nothing here
sends it.

### The answer is a reparenting, and the position is relative

`AvatarSitResponse` carries where the seat is -- `<-0.59, 0, 0.55>` for
a plain box, which is the simulator's default seat rather than anything
the object asked for -- and the `ObjectUpdate` that follows is the fact:
this avatar's own update comes back with `ParentID` set to the object's
local id, and a position that is now **an offset from the object**
rather than a place in the region.

That reparenting is the one signal worth trusting. It is what the
simulator believes, it arrives whether or not the response did, and
`sl` already keeps parents for everything else it hears about.

### A sit moves the avatar, and about ten metres is the limit

The box seven metres away seated the avatar as readily as the one
half a metre away, and standing up left it **six metres from where it
started** -- 29, 72, 2001 before, 35, 72, 2001 after. There was no
walk on the wire: one `ObjectUpdate`, 102 milliseconds after the
request, already parented. The `autopilot=true` in the response is an
instruction to the *viewer* to animate a walk it has already missed.

Eleven metres was refused. So there is a range, it is somewhere between
7 and 11 metres, and the number itself is not worth pinning down
further: what matters is that far things are refused in words and near
things are not.

### A refusal is an `AlertMessage`, and it arrives at once

	sit on an object eleven metres away
	  0.109  Alert "No room to sit here, try another spot."

	sit on a uuid that is not an object at all
	  0.090  Alert "Try moving closer.  Can't sit on object because
	                it is not in the same region as you."

Both in about a tenth of a second, both ending in a NUL byte that has
to be trimmed. The second is the simulator's answer for an id it cannot
find, and it is misleading enough to be worth repeating verbatim rather
than translating: a client that says "that object is not here" when the
grid said "not in the same region as you" has thrown away the only
detail somebody could act on.

This is better than the protocol usually manages. A sit either lands or
says why, in a tenth of a second, and a timeout means neither -- which
is a genuinely different thing and should read differently.

### Sitting on the ground is a different mechanism entirely

There is no message for it. It is a control flag on `AgentUpdate` --
`AGENT_CONTROL_SIT_ON_GROUND`, `1<<17` -- and standing is another,
`AGENT_CONTROL_STAND_UP`, `1<<16`. (Both were checked against a
Firestorm checkout in stage 2 and both are right. They are declared in
`indra/llcommon/indra_constants.h`, not the `llagentconstants.h` this
first said: that file is not in the tree.) Measured, both are **edge
triggered**: one update carrying the flag was enough, and the daemon's
own presence update a second later, carrying no flags at all, did not
undo it. So neither of these belongs in `Look.ControlFlags`, where it
would be resent forever; each is a single update built from the current
look with one bit set.

And a ground sit produces **no reply and no reparenting**. Nothing is
sent back at all except the animation list:

	0.009  AgentUpdate SIT_ON_GROUND -->
	0.144  <-- AvatarAnimation  1a2bd58e-...  (sit on ground)

That is the whole of the evidence. `AvatarAnimation` is therefore not
optional decoration here: it is the only proof a ground sit took, and
the only proof a stand took after one. It is also a second opinion on
the object case -- `1a5fe8ac-...`, the standard sit, appeared while
seated and was gone after standing.

The list is resent about every three seconds, in full, for every avatar
in range. That is a real cost and it decides a question in stage 2.

### Standing up

	12.007  AgentUpdate STAND_UP -->
	12.133  <-- ObjectUpdate  me  parent=0  pos=<29.07, 72, 2000.88>

126 milliseconds, the parent back to zero, and an absolute position
again. From a ground sit, the same flag with only the animation to show
for it.

## What this becomes

### `agent`: posture, which is two facts

The session already stores its own avatar's `ObjectUpdate` in the object
cache, parent and all, so "what am I sitting on" is nearly answerable
today. What is missing is the animation half, which needs a handler for
`AvatarAnimation` keeping the set for this avatar's own id and nothing
else -- everybody else's is the crowd's business, and keeping it would
be keeping a list that grows with the region.

Two facts, then: the local id this avatar is parented to, and whether
the animation set contains one of the sitting ones. Object sit sets
both, ground sit sets only the second, and standing clears both.

### `sl`: three verbs that wait for an answer

	Sit(ctx, object, timeout)     (*Seat, error)
	SitOnGround(ctx, timeout)     error
	Stand(ctx, timeout)           error
	Seat(ctx)                     (*Seat, error)

Each returns when the simulator has said something, not when the message
went out. `Sit` ends on the reparenting, on an `AlertMessage`, or on the
timeout, and those are three different results: seated, refused with the
grid's own words, and heard nothing. `ErrSitRefused` carries the alert
text.

### One of these needs a new RPC, and this said otherwise

**Corrected 2026-08-17, before stage 2 was built.** What this said was
that nothing here needs a new RPC: everything it sends goes through
`Send` and everything it waits for is a subscription. That is true of
the object sit and false of the other two.

A ground sit and a stand are control flags on `AgentUpdate`, and an
`AgentUpdate` is not just its flags -- it carries the camera, its three
axes, and the draw distance, and the simulator scopes its interest list
by them. A client building one has none of that: it knows a position
from `Presence` and no axes at all, so it would be guessing at the
camera and inventing a draw distance, and the simulator would believe it
until the daemon's own update a second later put it back. The probe that
measured stage 0 did exactly this, with `Far` set to 128 on a session
that may well run 256, and got away with it because it was a probe.

The daemon owns `Look` and is the only thing that can send an
`AgentUpdate` that is right about everything except the one bit being
asked for. So the one-shot belongs in `agent`, and a client asks for it:

	rpc Control(ControlRequest) returns (ControlResponse);

carrying the flags and nothing else. `agent` builds the update from the
current look with those bits set, sends it once, and forgets them --
measured, they are edge triggered, and the presence update a second
later carrying no flags did not undo either one.

It is worth having as a primitive rather than as two named calls,
because it is the same shape everything else that moves an avatar will
need: fly, stop, jump, and the nudges a walk is made of.

The object sit still needs nothing new: `AgentRequestSit` goes through
`Send` as any message does.

### The subscription question

`AvatarAnimation` is needed and it is chatty: every avatar in range,
every three seconds, in full. Adding it to `sl.Subscriptions` makes
every client pay for it forever, including `slrun` and `slbench`,
which is the same objection that made neighbours an option.

So it is subscribed for the duration of the command and dropped again,
and if that turns out to race the first animation after a ground sit,
the fallback is a session option rather than a permanent subscription.
Stage 1 measures which.

**Settled in stage 2.** Borrowed and given back, and the cost really
does stop: `client.Conn` has `Watch` and `Unwatch`, the daemon keeps one
subscription set per client stream, and `Unwatch` subtracts from it. Two
things had to be added around that. The borrow is counted, so that the
first of two overlapping sits to finish does not take the subscription
from the second; and `Hosted` remembers what the session named when it
attached and will not hand back a name it was never lent.

The race is real but harmless. `Watch` travels on the client's stream
and the daemon reads that stream in order, so anything sent afterwards
*on the stream* is safely after it -- but a ground sit is sent by
`Control`, a unary call on another goroutine, so in principle the flag
could go out before the subscription was applied. What saves it is the
resending: the list arrives in full about every three seconds whether
anything changed or not, so a subscription that landed late still hears
the sit one resend later. Losing the race costs a wait and not an
answer, which is why the default timeout is fifteen seconds rather than
one.

`AvatarSitResponse` went into `sl.Subscriptions` permanently, which the
same argument allows: it arrives once, when this avatar sits on
something, and it carries the seat offset. Nothing waits for it.

### `slsh`: `sit` and `stand`

	sit             sit on the ground
	sit NAME|UUID   sit on that object
	stand           get up

The bare form meaning the ground falls out of the protocol rather than
being a convenience: the two are different mechanisms, and the argument
is exactly what tells them apart. Names are resolved the way `take` and
`touch` resolve them, with the same refusal when a word names two
things.

`stand` rather than `unsit`, on the grounds that it is what the world
calls the button and what somebody types without thinking; `unsit` is
what a script calls the function. Both spellings can be accepted if the
other one is what actually gets typed.

What `sit` prints is what it did: what it sat on, and -- because the
avatar has probably moved -- where that left it. A sit is the first
command in this shell whose whole point may be the moving.

## Stages

### Stage 1 -- posture in `agent`

`AvatarAnimation` for this avatar's own id, the sitting animations named
as constants rather than spelled at the point of use, and `Agent.Seat()`
reading the parent out of the object cache. Tests against a fake sim for
all three transitions: object sit, ground sit, stand.

Measure: whether a subscription taken out at the moment of the command
reliably catches the animation 144 milliseconds later, or whether it has
to be standing when the command starts.

### Stage 2 -- the one-shot control flag, and the verbs in `sl`

`Agent.Control`, the `Control` RPC above, and then `Sit`,
`SitOnGround`, `Stand`, `Seat`, and `ErrSitRefused` carrying the alert. The alert text is trimmed of its NUL and otherwise passed through
untouched.

The one thing to be careful of: `AlertMessage` is a general channel and
something else may be alerting while a sit is in flight. An alert that
arrives in the window is treated as the answer, which is right nearly
always and wrong occasionally, and the wrongness is a spurious refusal
rather than a false success. Say so in the doc comment.

**Built 2026-08-17.** Two decisions this did not make, made:

`Agent.Control` sends the flag even while presence is deferred to a
viewer. The deferral stops this session *arguing* about the camera,
which is a thing only the viewer can be right about; it is not a stop on
being asked to do things, and the viewer will never send a flag nobody
told it about. What goes out is the current `Look`, whose centre follows
the avatar whether or not a viewer is attached, so it is exactly the
update the session would have sent had the viewer not been there. The
viewer re-asserts its own camera within a fraction of a second.

`Control` is part of `sl.Backend`, which took six fake backends in five
packages growing the method on the same day. It was built as an
interface asserted at the point of use, to keep the change inside the
packages stage 2 was allowed to touch, and that was the wrong shape for
the reason `backend.go` gives at the top of itself: both real backends
do it, nothing above the interface may care which it got, and an
optional method is a way of caring. The fakes were changed instead.

`sl.Watcher` stayed an assertion, and that one is not a compromise. A
direct session relays everything the circuit carries, so there is no
subscription to take out or give back; a caller that finds no `Watcher`
should conclude the message is already arriving rather than that it
cannot be asked for. That is a real difference between the two backends
and not an accident of what was convenient to change.

### Stage 2 went to the grid, and the grid had one more thing to say

**Measured 2026-08-17, with stage 2 built.** Everything above works
live, first time, at about a tenth of a second: an object sit, the seat
named back, a stand, a ground sit, and a refusal quoting the grid word
for word. One thing did not.

	sit on an object          seated,   101ms
	stand                     stood,    101ms
	sit on the ground         seated,   101ms
	stand                     IGNORED

Not a slow answer -- ignored. An independent session watching the same
avatar saw the ground sit animation still arriving at 3, 5, 12, 17 and
23 seconds. The daemon's trace shows the flag went out and was
acknowledged: `ControlFlags 65536`, on the wire, a tenth of a second
after the ground sit landed. The simulator dropped it.

It took a while to find because every simpler version works. A ground
sit and a stand on their own work at any spacing, down to none at all.
Two ground sits and two stands work. The same four steps sent by hand
work. What fails is those four with each step following its own
confirmation, and leaving three seconds before the last one -- changing
nothing else -- makes it work every time.

The reading that fits: **the animation saying a ground sit has begun is
not the avatar having finished sitting down**, and an avatar that was
still standing up off an object when it was told to sit takes longer to
get there. A stand arriving inside that window is swallowed.

So "edge triggered" was right and "once is enough" was the wrong thing
to conclude from it. A viewer does not send a stand, it **holds the
key**: an update carrying the flag goes out every frame until the avatar
is up. `sl.controlUntil` does the same at half a second, which turns a
swallowed flag into a wait nobody notices -- the failing sequence now
stands in 201ms, or 1.2 seconds when it takes two goes. Resending is
free: an edge triggered flag that has already been obeyed asks for
something that has already happened.

It settles the subscription race for nothing as well. A borrowed
subscription that had not been applied when the first flag went out is
certainly there for the second.

### Stage 3 -- `sit` and `stand` in `slsh`

The commands, the man pages, and the name resolution shared with
`touch`. `where` after a sit should agree with what `sit` printed.

### Stage 4 -- live, on the grid (done)

Run 2026-08-17 through `slsh` against Agni, on a scripted chair rezzed
for it in the skybox at Pelmar Reach.

**A scripted sit target behaves, and says what it was asked for.** A
prim running `llSitTarget(<0, 0, 0.8>, ZERO_ROTATION)` answered with
`SitPosition <0, 0, 1.15>` -- the target plus the 0.35 the simulator
adds for where an avatar's root is against where it sits -- and the
rotation as given. The same sit animation, the same reparenting, the
same tenth of a second. Nothing in stage 2 needed changing for it, which
was the thing worth checking: every earlier measurement was of the
simulator's default seat on a bare box.

**Sitting while already seated just works.** `sit perch` and then `sit`
on another object without standing in between moved the avatar from one
to the other. So `Sit` does not have to stand first, and its test for
success -- the parent CHANGING rather than matching the object asked for
-- is what makes it come out right.

**And `where` was lying.** Not by much and not for long, but a sit that
carried the avatar three metres read as the old position for about ten
seconds afterwards, because a position comes from `CoarseLocationUpdate`
and that arrives every few seconds. The exact answer was in hand the
whole time: a seated avatar's own update carries its position as an
offset from the seat. `agent.Position` now composes it back, and a sit
reports where it left the avatar the instant it lands.

Not done: **an occupied seat.** It wants a second avatar in the same
region and the other sessions here are on another grid.

## Open questions

- ~~**What a scripted sit target does to `AvatarSitResponse`.**~~
  Answered in stage 4: the sit target plus 0.35, and the rotation as
  given.

- ~~**Sitting while seated.**~~ Answered in stage 4: the simulator moves
  the avatar, and `Sit` needs no stand in front of it.

- **An occupied seat.** Still untested, and the one thing stage 4 could
  not do alone.

- **Crossing a border while seated.** This is how vehicles work, and
  `agent/crossing.go` has never seen it. A seated avatar that crosses
  arrives parented to an object whose local id belonged to the region it
  left, and nothing in the object cache would know that.

- **Whether the range limit is the simulator's or the parcel's.** Seven
  metres worked and eleven did not, in one skybox, on one parcel.

- ~~**A ground sit `sl` did not perform.**~~ `sl.Seat` reads the object sit
  off the reparenting, which is relayed always, and the ground sit off
  the animation list, which is relayed only while one of the sit calls
  is holding the borrowed subscription. So a ground sit this session
  performed is reported and one performed by a viewer, or by another
  client while this session was not listening, reads as standing. The
  daemon knows the answer -- `agent.Posture` has it, permanently and for
  nothing -- so the fix is a `Posture` RPC rather than a subscription.
  Stage 3 did not need one after all: `Sit` returns the seat and the
  position comes from `Where`. Answered since: `sl.Seat` asks the
  backend for the agent's posture first -- the `Posture` RPC, through
  slgod -- and falls back to what the session heard only where the
  backend cannot say.

- **Other avatars' postures.** `AvatarAnimation` arrives for everybody
  in range, so "who is sitting" is answerable for the whole crowd at no
  extra cost beyond keeping it. `map` could mark it. Not now, and worth
  writing down before the handler is written in a way that throws
  everybody else's away.
