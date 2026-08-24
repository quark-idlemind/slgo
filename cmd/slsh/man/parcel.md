The land underfoot: its name, who owns it, how much of its prim
allowance is used, and what it permits.  A region is one place to the
grid and several to everybody in it -- `where` gives the region and
this gives the other half.  Reach for it when building, flying, running
a script or a stream has stopped working for no reason the region
explains.

With a point it describes any parcel in this region rather than the one
underfoot.  The avatar does not move.  The point is metres from the
region's south-west corner, the same numbers `where` prints, 0 to 256
each way:

    parcel
    parcel 60,60

## Options

**-r, --region**

List every parcel the session has a map of.  It takes no point: the
whole region is what is asked about.

**-m, --map**

Draw the region's parcels in the shape `map` draws a region: one
character to a cell, a different letter for each parcel, and a key
under it naming them.  It takes no point.  `--region` counts them and
this draws them; ask for one.

**--rows** *N*

How many rows that picture is drawn in, from 2 to 64.  Without it,
`map_rows`.  Refused without `--map`.

## What is true now

The parcel is asked for, not remembered, so the answer is what is true
now -- a sale, a rename or a split since arrival is in it.

If the ask goes unanswered, the bare command falls back to what the
session was told when it arrived, and says so.  That fallback is only
good for the parcel underfoot.  `parcel 60,60` has nothing to fall back
on and reports the failure instead.

## The whole region's layout

The region's parcels can be listed or drawn, and both need the parcel
overlay, which arrives when the avatar does and cannot be asked for
again.  A shell attached to a daemon that has been up for hours usually
has it; a session that has just logged in and not moved may not, and
the command says so rather than drawing an empty region.  The only way
to fetch it again is to arrive again.

The overlay carries the shapes and no names at all, so `--region` and
`--map` both ask about a point inside each piece, one after another,
about a tenth of a second each: a dozen parcels is a second or two of
nothing on the screen before either appears.  A piece nothing answers about is
still listed, by its shape, with `(no answer)` where the name would be,
and after three unanswered in a row the rest are not asked about
either; how many went unnamed is said at the foot.  The listing itself
stops at thirty parcels and says how many more there were.

One parcel can be two pieces of ground -- land bought either side of a
road is still one parcel -- and the listing says how many pieces the
parcels were cut into.

Linden's protected land is drawn as ground: a blank for the roads and
waterways, and a `.` for the rez zones inside them.  A `?` is a part of
the overlay that never arrived.  Each other parcel is a letter, in a
colour: the colour is what tells two parcels apart at a glance, and the
letter is what tells them apart at all when the picture is written to a
file or `NO_COLOR` is set.

## A local id is not a name

Every report has a `local` line, which is the region's own numbering
for that parcel.  It belongs to that region and nowhere else: the same
number in the region next door is a different piece of land.

## Asking about a parcel selects it

A viewer attached to this session through slgod will notice: its About
Land floater redraws to show the parcel that was asked about, which is
not the one under its avatar.  That is the grid working as designed.
Somebody watching the screen while somebody else types `parcel 200,200`
will see the panel change under them.

## Examples

    parcel
    parcel 60,60
    parcel --region
    parcel --map --rows 24

See also: `where` for the region and the position, `look` for what the
simulator says about the region itself, `group` for the membership that
parcel permissions usually hang on, and `map` for who is standing here.
