Says whether this avatar holds a circuit to the regions beside the one
it is standing in, and lists what it holds.  With `on` or `off` it
changes that first and then says the state it ended up in.

The circuits are what a border crossing needs.  A simulator will not
hand an avatar over the edge of a region to a client that holds no
circuit to the region on the other side -- it does not refuse, it
simply does not do it -- so with neighbours off the avatar walks to the
border and stops there.  With a circuit open, the same walk is over the
border in a couple of seconds.

## Why it is off unless somebody asks

Each neighbour costs a socket and a share of the traffic, for as long
as it is held, and the number of them is however many regions surround
this one -- up to eight on the open grid.  That is a price worth paying
for an avatar somebody is walking about with, and a waste for one
running a benchmark in a single region, which is why this is a property
of an avatar and not of the daemon holding it.

Off drops what is held rather than merely refusing the next offer.
Nothing is listed after it: one line says they are off.

A word that is neither `on` nor `off` is a usage error, and the session
is left as it was.

## On, with nothing held, is the ordinary state

Turning them on asks for nothing and opens nothing.  The simulator
offers a neighbour of its own accord and repeats the offer for as long
as it goes untaken, so the circuits arrive by themselves; the way to
see them is to run `neighbours` again.

They take their time, and they do not arrive together.  Measured on
Agni on 2026-08-23, timed from turning them on: the first circuit
opened between twenty and sixty seconds later, and more followed over
the minute after that -- at one position three opened together and two
more forty seconds behind them.  An empty listing in the first few
seconds is the offer not having come round yet.

Being away from a border is not what empties it.  Standing in the
exact middle of a 256-metre region, 128 metres from all four edges,
four circuits were held: one across each edge, and none across a
corner.  Twenty-eight metres in from the west edge and seventy-two
from the south, the near three came first -- those two edges and the
corner between them -- and the far two followed.  Height does not
decide it: that second position was 2001 metres up, and the middle was
measured both at 2001 and at ground level.

The set changes as the avatar moves.  The circuits belong to the
region the avatar is in, so a teleport to another region or a crossing
throws them away; a teleport within the region leaves them alone.

## The listing

One region to a line, in the shape `regions` prints: what the region
calls itself, the square it occupies on the grid, the address of the
simulator, and how many packets have arrived on the circuit.

    neighbours are on, 2 circuits held
    Example Landing                   43647, 43648   203.0.113.9:13009  412 heard
    Example Shallows                  43648, 43647   203.0.113.28:13006 87 heard

The handle is not in the line, where `regions` prints it, because
nothing is addressed by a neighbour's handle: a border is walked over
rather than typed at.

A name arrives in the region's handshake, so a line that says
`(no handshake yet)` is a circuit that has not been sent one.  It does
not follow that the offer came to nothing.  On 2026-08-23, three of
the five simulators around one region sent no handshake through four
openings each, while the packet count beside them climbed into double
figures -- and the same three had answered on an earlier day, so it is
the simulator's mood rather than the circuit's health.  The count is
what says a circuit is alive; the name only says the simulator
introduced itself.

The count is what the neighbour has said, counted and dropped.  Nothing
this shell can ask about lives in another region: what is over the
border is not in `objects`, not in `who` and not in the object cache.
The count is there to say the circuit is alive.

## It belongs to the session, not to this shell

Turning neighbours on turns them on for the avatar, in whatever is
holding the session, and it stays on after this shell exits.  Another
client attached to the same daemon sees the same answer and can change
it too.  Quitting does not put it back.

A profile can start an avatar with them on, which is the right place
for one that is always driven by a person:

    neighbours = yes

in the profile under ~/.config/slgo.  The profile spells it `yes`; this
command spells it `on`.  slgod's `-neighbours` flag is what profiles
that say nothing get, and a profile that says either wins over the
flag.

## Examples

    neighbours
    neighbours on
    neighbours off

See also: `tp` for crossing the grid rather than a border, `regions`
for finding a region by name and for what its handle is, `look` for
what the region underfoot says about itself, and `where` for the
position inside it.
