# Walking

`agent.Agent.Move` walks the avatar the way a viewer does, by holding
`AGENT_CONTROL_AT_POS` on every `AgentUpdate` and steering with the
body rotation, and lets go when it is close enough. The comments in
`agent/walk.go` say what it does and what it takes from the viewer.
This page is what was measured on the way there, on Agni, on
2026-09-24.

## The simulator's own autopilot

The viewer's "Go here" (`handle_go_to`, in `llviewermenu.cpp`) sends
`GenericMessage` "autopilot" with a place in global metres, and the
simulator does still act on it: measured once on Agni, 2026-09-24, the
avatar set off about 0.8 seconds after it was sent and came to rest 2.1
metres past a place 1.7 metres away. It says nothing about how it is
going, cannot be told to stop by anything here, and stops where it
likes, which are the three things a walk asked for over slgod's API has
to do. So it is not used.

## Stopping where asked

An avatar does not stop where the flag is let go of. Measured on Agni,
2026-09-24, on a flat platform, from the position this package had for
the avatar at the moment it let go to where the avatar came to rest:
about 0.9 metres walking, at the 3.2 metres a second the simulator
reports for a walk, and about 1.4 metres running, at 5.1. Both are
about 0.28 seconds of going on at full speed -- the round trip to the
simulator, the simulator's own slowing down and the terse update
steered by being a moment old, all together; which of them is the
larger was not measured. Let go of with the target half a metre ahead,
as the first version of this did, the avatar came to rest 0.7 to 1.0
metres past it. Neither the STOP flag nor holding `AT_POS` alone for
the last two metres, as the viewer's autopilot does, made a difference
that could be seen, in one try of each over the same stretch of floor.

So the flag is let go of `stopLead` ahead of where the avatar is going
to be, and the walk is called over only once the avatar has stopped.
Measured the same day with this in place, over six metres: four walks
came to rest 0.01 to 0.02 metres from the target, and two runs 0.09
and 0.10.

A walk from a standstill cannot be made short: the same day, holding
the flag for a single tenth of a second moved the avatar 0.97 metres,
and an update carrying the flag followed at once by one without it
moved it about a tenth of a metre, but in directions that did not
follow the facing it had just been given. So nothing here tries to step
finer than a walk.

## Reckoned forward

The simulator does not send a terse update for every step of a walk: it
sends one when the motion stops being what the last one predicted.
Measured on Agni, 2026-09-24: updates for an avatar walking in a
straight line at 3.2 metres a second arrived every tenth to fifth of a
second, and then none at all for a whole second, over which the
position read without reckoning stood still while the avatar walked
three metres past the place it was being steered to. That is why
`Placement` carries the last position forward by the last velocity.

## The STOP flag

A stop is sent as a viewer sends one: an update carrying
`AGENT_CONTROL_STOP`, and one after it carrying nothing held. Measured
on Agni, 2026-09-24: with the flag let go of by an update carrying STOP
and by one carrying nothing, an avatar walking at 3.2 metres a second
came to rest about a metre further on either way, so the STOP is the
viewer's habit kept rather than something shown to matter.
