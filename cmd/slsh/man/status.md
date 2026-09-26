`status` is about the plumbing rather than about the world: how the
circuit under this session is doing, and what has been crossing it.

    status

It answers for the session this shell is attached to, and it is the
daemon's answer -- a shell that logged in for itself has nothing to
ask and says so.  The heading names the avatar, the profile it is
hosted under in brackets after it, and the region -- in that order,
which is the other way round from the `agents` listing, where the
profile is the first column.  Under it the simulator's own channel
version, the packet counters, how many clients the daemon has
attached to this session, how many capabilities the simulator
granted, and how many objects have been described in each form of
placement.

## What the counters are for

They say whether a session that looks idle is idle or broken.  A
quiet region and a dead circuit look identical from the prompt:
nothing is being printed either way.  Packets still arriving is a
quiet region; packets going out and being resent and then abandoned
is a circuit that has stopped being listened to at the far end.
Duplicates are the region saying something twice, which is ordinary
in small numbers.

The counters belong to the connection the session is on now.  A
session that dropped and was brought back is counting from the new
circuit, so small numbers under a session that has been up for hours
are a reconnection rather than a quiet afternoon.

## Placements

An `ObjectUpdate`, the simulator's full description of an object,
says where the object is in a packed blob, and the width of the blob
in bytes is the only thing that says which form it is in.  The
`placements` line counts them by width, narrowest first.  60 is
plain floats, the form new objects have been seen to arrive in, and
76 is the same for an avatar; 124 and 140 are those two with room
for more after them, which the daemon reads the same way.  32, and 48
for an avatar, is a narrower form the daemon can read and that has
not been seen.  Any other width is counted and not read: the object
is known, and where it is is not.  The line is there to say whether
anything but 60 and 76 turns up, and it prints nothing until an
object has been described.

## The messages with no handler

Anything listed under that heading arrived and this build had
nothing to do with it, commonest first.  The list is long and that
is ordinary: a region telling a client about sound and weather and
time produces a lot of messages nothing here has ever wanted.  A big
number is not by itself a hole worth filling; what is worth reading
is a name appearing that was not there yesterday.  An empty list
prints nothing at all rather than an empty heading.

## Examples

    status
    status > status-before-the-run

See also: `watch` for the messages themselves as they arrive,
`agents` for the other sessions, and `caps` for the capabilities
counted here.
