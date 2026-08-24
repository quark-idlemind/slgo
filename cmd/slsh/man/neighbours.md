neighbours says whether this avatar holds a circuit to the regions
beside the one it is standing in, and lists what it holds.  With "on"
or "off" it changes that first and then says the state it ended up in.

The circuits are what a border crossing needs.  A simulator will not
hand an avatar over the edge of a region to a client that holds no
circuit to the region on the other side -- it does not refuse, it
simply does not do it -- so with neighbours off the avatar walks to the
border and stops there.  Measured: at Pelmar Reach's west edge, pinned at
x=0 for twelve seconds, and at x=255 for twenty-four seconds coming the
other way.  With a circuit open to Pelmar Mill the same walk was over the
border in two seconds.

## Why it is off unless somebody asks

Each neighbour costs a socket and a share of the traffic, for as long
as it is held, and the number of them is however many regions surround
this one: four around Pelmar Reach, up to eight anywhere on the open grid.
A neighbour describes itself fully and unprompted -- 59 object updates
and 51 pieces of terrain in the first thirty seconds, measured on Agni
-- and none of it is kept.  That is a price worth paying for an avatar
somebody is walking about with, and a waste for one running a benchmark
in a single region, which is why this is a property of an avatar and
not of the daemon holding it.

So it can be turned over while the session is up.  A benchmark that has
finished can be walked out of the region; an avatar that has finished
walking can stop paying for the circuits.

Off drops what is held rather than merely refusing the next offer.
Nothing is listed after it: one line says they are off, which is the
whole of what there is to say once no circuit is held.

## On, with nothing held, is the ordinary state

Turning them on asks for nothing and opens nothing.  The simulator
offers a neighbour of its own accord, and repeats the offer for as long
as it goes untaken -- 57 offers of four regions in one 200 second run,
and exactly one apiece once they were taken up.  So a session that has
just turned them on picks the next repeat up within a second or two,
and the way to see the circuits is to run "neighbours" again.

An empty listing usually means the avatar is not near a border.  A
simulator introduces what is near: standing twelve metres from the west
edge, one neighbour was offered; standing in the middle of the same
region at 2001 metres, none at all.

It is the distance to a border and not the height that decides it.
Measured on the same region and at that same 2001 metres, but 28 metres
in from the west edge and 72 from the south, three circuits were offered
and taken up inside four seconds: the two regions across those edges and
the one across the corner between them.  A skybox is not out of reach of
its neighbours; the middle of a region is.

The set changes as the avatar moves.  Walking toward a corner brought
two more, and arriving in the next region dropped all three and opened
that region's own within a second: the circuits belong to the region
the avatar is in, and a teleport or a crossing throws them away.

## The listing

One region to a line, in the shape "regions" prints: what the region
calls itself, the square it occupies on the grid, the address of the
simulator, and how many packets have arrived on the circuit.

    neighbours are on, 2 circuits held
    Pelmar Mill                       43647, 43648   203.0.113.9:13009  412 heard
    Orrick Gate                       43648, 43647   203.0.113.28:13006 87 heard

The addresses above are made up, and deliberately: a simulator's address
says which machine a region is on as plainly as its name says which
region it is, and this file is public.  What comes back is an ordinary
address and port.

The handle is not in the line, where "regions" prints it, because
nothing is addressed by a neighbour's handle: a border is walked over
rather than typed at.  Use "regions" if the number is wanted.

A name arrives in the region's handshake, which came back in about a
second, so a line that says "(no handshake yet)" is a circuit that has
just opened.  One that still says it a moment later is an offer that
came to nothing -- an address that was dialled and never answered --
and the packet count beside it will be zero.

The count is what the neighbour has said, counted and dropped.  Nothing
this shell can ask about lives in another region: what is over the
border is not in "objects", not in "who" and not in the object cache,
because local ids are each region's own numbering and a neighbour's
would name this region's objects.  The count is there to say the
circuit is alive.

## It belongs to the session, not to this shell

Turning neighbours on turns them on for the avatar, in whatever is
holding the session, and it stays on after this shell exits.  Another
client attached to the same daemon sees the same answer and can change
it too.  Quitting does not put it back.

A profile can start an avatar with them on, which is the right place
for one that is always driven by a person:

    neighbours = yes

in the profile under ~/.config/slgo.  slgod's -neighbours flag is what
profiles that say nothing get, and a profile that says either wins over
the flag.

## Examples

    neighbours
    neighbours on
    neighbours off

See also: tp for crossing the grid rather than a border, regions for
finding a region by name and for what its handle is, look for what the
region underfoot says about itself, and where for the position inside
it.
