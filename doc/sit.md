# Sitting, and standing up again

Written 2026-08-17, against `8389366`. Stage 0 has been run against Agni
and what it measured is folded in below: where this says what happens,
it was watched happening. Nothing else here is built.

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
`AGENT_CONTROL_STAND_UP`, `1<<16`. Measured, both are **edge
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

None of this needs a new RPC. Everything it sends goes through `Send`
and everything it waits for is a subscription, so this is `sl` and
`slsh` and no change to the proto -- which is worth saying out loud,
because the neighbours work went the other way and it is not obvious
from the outside which kind of feature this is.

### The subscription question

`AvatarAnimation` is needed and it is chatty: every avatar in range,
every three seconds, in full. Adding it to `sl.Subscriptions` makes
every client pay for it forever, including `automate` and `autobench`,
which is the same objection that made neighbours an option.

So it is subscribed for the duration of the command and dropped again,
and if that turns out to race the first animation after a ground sit,
the fallback is a session option rather than a permanent subscription.
Stage 1 measures which.

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

### Stage 2 -- the verbs in `sl`

`Sit`, `SitOnGround`, `Stand`, `Seat`, and `ErrSitRefused` carrying the
alert. The alert text is trimmed of its NUL and otherwise passed through
untouched.

The one thing to be careful of: `AlertMessage` is a general channel and
something else may be alerting while a sit is in flight. An alert that
arrives in the window is treated as the answer, which is right nearly
always and wrong occasionally, and the wrongness is a spurious refusal
rather than a false success. Say so in the doc comment.

### Stage 3 -- `sit` and `stand` in `slsh`

The commands, the man pages, and the name resolution shared with
`touch`. `where` after a sit should agree with what `sit` printed.

### Stage 4 -- live, on the grid

Sit on something scripted with a real `llSitTarget`, which stage 0 never
tried: every measurement above is of the simulator's default seat on a
plain box. Sit on something already occupied. Sit while already seated
on something else. Stand from each.

## Open questions

- **What a scripted sit target does to `AvatarSitResponse`.** Everything
  measured is the default seat. `llSitTarget` is the ordinary case in
  the world and it was not tested.

- **Sitting while seated.** Untested. Either the simulator moves the
  avatar or refuses, and the answer decides whether `Sit` has to stand
  first.

- **Crossing a border while seated.** This is how vehicles work, and
  `agent/crossing.go` has never seen it. A seated avatar that crosses
  arrives parented to an object whose local id belonged to the region it
  left, and nothing in the object cache would know that.

- **Whether the range limit is the simulator's or the parcel's.** Seven
  metres worked and eleven did not, in one skybox, on one parcel.

- **Other avatars' postures.** `AvatarAnimation` arrives for everybody
  in range, so "who is sitting" is answerable for the whole crowd at no
  extra cost beyond keeping it. `map` could mark it. Not now, and worth
  writing down before the handler is written in a way that throws
  everybody else's away.
