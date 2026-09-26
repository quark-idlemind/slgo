# Handing a session to a viewer

`slgod -viewer` hands the session it is holding to a real viewer: the
viewer logs in to the daemon, is given the session's own ids with the
daemon's address in place of the simulator's, and from then on the
daemon is a simulator to the viewer and a client to the simulator. The
comments in `viewer/` say what the package does. This page is why: what
went wrong before each rule, and what was watched happening.

The plan the package was built from, stage by stage, is
[doc/history/viewer-frontend.md](history/viewer-frontend.md). What a
viewer is told when the avatar moves under it, and what that was seen
to do, is stage 6 of [doc/history/teleport.md](history/teleport.md).

## The session is looked up, not held

A `Circuit` asks for its grid session each time it needs it, through the
function `Listen` was given, rather than keeping the `*agent.Agent`,
because a grid session is replaced when it has to be re-established and
a viewer circuit outlives that. Holding the pointer meant that after a
reconnect the circuit went on talking to a dead session: every forward
failed with "sender is not running" and the viewer sat waiting for a
region handshake that was being composed from a session that had ended.

## Who may talk on the circuit

The circuit follows its viewer's address rather than pinning the first
one, because a viewer that is restarted comes back on a new port.
Pinning meant the circuit went on sending to the socket of a viewer
that had quit, and the new one waited for a handshake that was being
delivered to nobody.

What has changed is the price of being followed. It used to be enough
to send one datagram from anywhere: from then on everything the
simulator said went to the new address, and anything the new address
said was forwarded to the simulator as the avatar. Now a new address is
adopted only by opening with a `UseCircuitCode` carrying this session's
own circuit code and session id -- which is what a viewer sends first
and what a simulator itself demands before it will talk to anybody --
and anything else from an address that has not done that is dropped
unread and counted (`Circuit.admit`).

Note what is not the check. The circuit code and session id are in the
login response, so a viewer that was handed the session knows them;
they are the proof that this is that viewer, not a secret in their own
right. What keeps a stranger out is that they were never given the
response -- which is the login endpoint's decision, now carried this
far instead of stopping at the door.

## A second viewer on the same circuit

The sequence numbers are the whole reason `forgetTheLastViewer` exists.
A viewer numbers its own packets from 1, so a second one opens with
`UseCircuitCode` at 1 and `CompleteAgentMovement` at 2 -- numbers the
first viewer used, and still in the dispatcher's ring of the last 4096.
Both were dropped as retransmissions before ever reaching `fromViewer`,
which is the only thing that replays the region and answers the
movement request, while the peer was noticed anyway because the tap
that noticed it then ran ahead of the duplicate check. The daemon
logged a viewer appearing and then said nothing, and the viewer sat at
`STATE_AGENT_WAIT` with a grey world until slgod was restarted.

Measured on Agni, from two daemon traces, which is why it looked
intermittent rather than certain. Where re-attaching failed the first
viewer had sent 326 traced packets, so 1 and 2 were still in the ring.
Where it succeeded the first viewer had sent 3777 traced packets and
about fifty minutes of acknowledgements and pings the trace does not
record, which is enough for the ring to have wrapped past them.

Both halves of the circuit forget, because both are the departed
viewer's. Anything still awaiting acknowledgement was addressed to a
socket that has closed, and the `LogoutReply` case is the one that
bites: a viewer that quits sends `LogoutRequest`, and the reply it never
acknowledged would be retransmitted to its replacement and log that one
out on arrival.

What is not touched is as deliberate. This is the same circuit and the
same grid session: the session's own circuit to the simulator, its
sequence numbers, and everything it has learned about the region belong
to the daemon rather than to whoever is looking at it, and the simulator
messages already queued for the viewer are the region's current state,
which the new viewer wants as much as the old one did. The appearances
held for a joining viewer are the same: `describeRegion` refills them
from the session, and anything left over describes an avatar standing
in this region either way. The receiver has nothing of its own to forget
-- it carries no state at all from one datagram to the next.

`forgetTheLastViewer` is called from `notePeer`, which runs on the
dispatch goroutine from `admit`, the circuit's gate. That is what makes
`Dispatcher.Forget` safe: it writes fields no lock protects, on the one
goroutine that owns them.

## A teleport asked for at the viewer

A viewer's teleport out of the region is refused rather than followed,
and what forwarding one would now do is not what
[doc/history/viewer-frontend.md](history/viewer-frontend.md) said before
teleport's stage 6. That sentence -- the agent goes to a simulator slgod
is not connected to and the session ends -- was written when nothing in
the daemon read `TeleportFinish`, and the daemon follows a teleport now.
So the session does not end, and what happens instead is harder to see
and worse to be in: the request is granted, slgod moves the circuit to
the new simulator, and the viewer is told none of it, because
`TeleportFinish` is withheld from its event queue. It goes on drawing a
region the avatar has left, pushing a camera around it that the new
simulator is deciding what to stream from, and taking object updates
whose local ids are the new region's numbering laid over the old
region's. Nothing anywhere reports an error. A session that ends at
least says so.

Following properly is a second circuit on a second port and a rewritten
`TeleportFinish` -- the other option in
[doc/history/teleport.md](history/teleport.md#stage-6----a-viewer-attached-while-it-happens-done),
which is deliberately not built. So these are absorbed the way
`UseCircuitCode` and `LogoutRequest` are, and the person is told: a
control that does nothing and says nothing is indistinguishable from a
viewer that has stopped working.

`StartLure` is deliberately not among them and goes on being forwarded.
Offering somebody else a teleport to where this avatar is standing moves
this avatar nowhere, and it is a thing a viewer does far better than a
shell does.

## What a viewer is not given

The session stays the only reader of the simulator's event queue and
hands a copy to the viewer's (`EventQueue`). A few events are left out
of the copy (`withheldEvents`). Each is held with the reason it is
held, because the daemon says that reason out loud once per kind and a
sentence kept somewhere else would end up describing the wrong one.

`EstablishAgentCommunication` is the simulator introducing a neighbour
region: its UDP address and its seed capability, so that a viewer can
open a child connection and draw what is over the border. Handed to a
viewer, that is exactly what it does -- and the connection it opens is
direct, using this session's own agent and session ids, to a simulator
that has never heard of slgod. The handover stops being a handover:
part of the session is then in the daemon and part of it is in the
viewer, and nothing can see both.

So it is withheld, and the cost is plain rather than hidden: the viewer
draws this region and nothing beyond it. Neighbouring regions are void.
slgod can hold the child connections itself (`agent.Options.Neighbours`,
off by default), but making them work in the viewer means offering it
slgod's own port for each, and that is not built
([doc/history/neighbours.md](history/neighbours.md#stage-7----a-viewer-that-can-see-across-the-border)).

`EnableSimulator` is the other half of that introduction and was relayed
for as long as the front end had existed, which made the paragraph above
half true at best. It carries a neighbour's handle, IP and port and no
capability of any kind -- and a capability is not what opening a circuit
takes. `UseCircuitCode`'s whole content is the circuit code, the session
id and the agent id, and a viewer holding this session already has all
three: they are in the login response it was replayed, and
`Circuit.checkCircuit` exists precisely because the viewer sends them
back on attaching. So an address is the only thing it was missing, and
this message is an address. The seed in `EstablishAgentCommunication`
buys the neighbour's HTTP capabilities; the UDP circuit, which is where
the object updates arrive and where anything a viewer might send as this
agent would go, needs none of it. The reference viewer does exactly this
on receiving one -- enables the circuit and sends `UseCircuitCode` to
the address it names (`process_enable_simulator`,
`llworld.cpp:1584-1652`) -- which is a reading of published viewer
source rather than something measured here, and the withholding does not
rest on it: the message plus what the viewer already holds is
sufficient on its own.

A neighbour circuit is worse than the one `EstablishAgentCommunication`
would open, not better. Every absorb in the package -- the logout that
would end the grid session, the teleports, the circuit claim -- is
enforced on the one circuit slgod owns, and a circuit the viewer opened
itself is under none of it.

Withheld, then, and it is the older hole rather than one the teleport
work made: it had been relayed since the front end existed, and the
capture taken in teleport's stage 0 has three of them arriving every few
seconds. What it costs is that a viewer no longer creates the
neighbouring regions at all, so anything it was managing to draw across
a border stops -- which is the sentence above finally becoming true
rather than a new restriction.

`TeleportFinish` is the same failure arriving by a different road, and
it opened when the daemon learned to follow a teleport. It carries the
new simulator's address and its seed capability, and it does not need a
viewer to have asked for anything: `slsh tp` moves the session, the
simulator puts a `TeleportFinish` on the queue, slgod fans it out, and a
viewer handed it opens a circuit straight to the real simulator with
this session's agent id, session id and circuit code. Half the session
would then be in the daemon and half in the viewer, with each one's
sequence numbers meaningless to the other, and nothing anywhere able to
see both. So it is withheld as well, and `Circuit.FromSim` absorbs it on
the circuit in case a grid ever sends it there.

`CrossedRegion` is that message for an avatar that walked, and it is
held on the same terms. It names a simulator and carries its seed,
nobody asked for it at all, and slgod acts on one itself when it arrives
(`agent/crossing.go`) -- so a viewer given it would be a second thing
following the same crossing, by its own circuit, with these ids.
Withheld on the queue and absorbed in `Circuit.FromSim`, both roads.
Agni sends it on the queue, to a session holding a child circuit to the
region over the border; without one the border is a wall and it never
comes.

`TeleportFailed` is not withheld, and it is the one of these that can be
let through. It carries no address and no invitation to connect
anywhere: it is a reason string and, in a viewer, the message that
clears any teleport state and puts up a notice. Nothing it can do is
worse than the truth it carries, and if a `TeleportStart` ever does
reach a viewer past the arm that absorbs it, this is the message that
gets the viewer out of the tunnel again. Withholding it would buy
tidiness and cost the only safety net there is.

`TeleportStart` and `TeleportProgress` arrive on the circuit rather than
the queue, and are absorbed in `Circuit.FromSim`.

The measurements behind these are in stages 6 and 7 of
[doc/history/teleport.md](history/teleport.md) and in
[doc/history/neighbours.md](history/neighbours.md).

## What was said while nobody was attached

The messages a viewer exists to answer -- an offered teleport, and the
rest that `agent.Offers` keeps -- used to go into the daemon and stop
there: an offered teleport arrived four minutes before the viewer did
and was never seen. `describeRegion` now hands what the session kept to
the viewer that joins.

## The login

The circuit used to be opened while the handover was being looked up,
which is before the password has been compared -- so naming an avatar
the daemon holds was enough to open its UDP socket, and the socket
outlives the attempt. It is now opened by `Handover.Admit`, which the
login handler calls only once the password has matched, and a wrong
password costs the asker a refusal and nothing else.

A login refused before its password has matched is told one sentence,
whatever the reason. They used to be two sentences, and the difference between them said, to anybody who
could reach the port and cared to type a wrong password, that the avatar
named was logged in through this daemon right now and could be taken.
The name is not a secret on the grid; that is.

`LoginURI` makes a bare address `https`. It used to make it `http`,
which was right while the endpoint had a plain channel and is now a URL
nothing answers on: Go's TLS server replies to a plaintext request with
400, and a viewer reports it as an unreachable grid.
