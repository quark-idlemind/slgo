regions asks the grid's map where a region is.  It is "look" pointed
somewhere else: look describes the region this avatar is standing in,
out of what that simulator said when the avatar arrived, and this one
reaches a region on the other side of the grid that nobody here has
mentioned and this avatar has never been to.

The argument is the name, or the start of one, joined with spaces --
a name with a space in it needs no quoting.  Each line that comes back
is one region: its name, the square it occupies on the grid, its
maturity rating, and its handle.  The name is plural because the answer
usually is; see below.

## Several matches is the ordinary case and not a mistake

The search matches from the start of a name and ignores case.  So a
word finds every region beginning with that word -- "Example" finds
Example Landing, Example Landing North and Example Shallows alike --
and typing a name out in full still finds it along with everything else
that begins with it.

There is no way to ask for an exact name, because the map has no such
question in it.  So this prints what matched and picks none of it: the
line to read is the one whose name is the name, and choosing that line
here would mean choosing between somebody's several possibilities
without showing them.  Measured: "Sandbox" comes back with thirty-three
regions, of which one is the place meant and the rest are its namesakes,
and not one of them is called Sandbox.  Nothing caps the other end
either: a single letter answered with 1586 regions, inside a second, and
every one of them was printed.  A short name is a long listing rather
than a refusal.

A name that matches nothing is an error saying which name, since that
is the map's own answer rather than a listing that came back empty.

## How long the map is given

The answer comes back over the message circuit in however many packets
it takes, ended by a block that says the list is finished.  Fifteen
seconds are allowed for the whole of it and --wait, in seconds, changes
that.  Running out is a real timeout and not "no such region": what
arrived is a fragment of the list, and a fragment printed as the answer
would be a shorter grid than the real one.  The error says how many had
come in by then, since nothing at all and most of an answer are
different things to do something about.

## The handle is the number a teleport is addressed to

A region's place on the grid can be said two ways.  The square -- 1200,
980 -- is the readable one, and the handle is that square's south-west
corner in metres packed into a single number, which is how every message
that names a region names it.  The two are the same fact, and the second
is the one to quote when the question is which region something meant.

tp takes a name rather than a handle, and looks it up through this same
search -- so the listing here is what tp will be choosing between, and
running regions first is how to find out whether the name is enough.  A
name that matches several is refused there, where here it is the answer.

## What the map will not tell you

The reply has fields in it for how many avatars are in a region, what
the region allows and how high its water is, and Second Life sends all
three as zero for every region every time.  So this cannot say whether a
region is up, or busy, or empty; the position and the rating are the
whole of the answer.

A region listed here is a region that exists.  It is not a region that
will have this avatar: an estate may refuse an arrival for reasons the
map has no field for.

## Examples

    regions Example Landing

Everything whose name begins with a word, which is the same command with
less of the name typed:

    regions Example

See also: look for what the region underfoot says about itself, where
for the position inside it, tp for going to one of these, and neighbours
for the regions this one touches, which are the ones the avatar can walk
into.
