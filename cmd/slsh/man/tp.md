tp moves the avatar: to a point in the region it is already in, or to
another region by name.  It is "move" done to a person instead of to an
object, and like move it waits for the arrival rather than reporting the
request.  What is printed afterwards is where the avatar actually ended
up, read back, and not the point that was asked for.

Three numbers and nothing else -- X Y Z, in this region's metres -- is a
move to that position, which is usually inside this region and need not
be: see below.  Anything else is a region's name, joined with spaces so
that a name with one in it needs no quoting, and the last three words
are the position when all three are numbers:

    tp 128 128 25
    tp Example Landing
    tp Example Landing 33 73 1000.5

A trailing comma on a number is ignored, so a position read off "where"
or off the line above a map is pasted back in as it stands: "tp 128,
128, 25" is the first of those three again.

## A position outside this region is the region it really is in

A region is 256 metres square and the grid is regions laid edge to
edge, so x of 300 is not an error and not a point in this region: it is
44 metres into the region to the east.  x of -10 is 246 metres into the
one to the west.  The shell works out which region owns the point and
teleports there, in the same way that the 25th hour of Monday is one in
the morning on Tuesday.

    tp 300 128 25          44 metres into the region east of here
    tp -10 128 25          246 metres into the region west of here
    tp 128 -1 25           255 metres into the region to the south

It generalises as far as the arithmetic does, because there is nothing
special about one region over: the position is turned into a place on
the grid, the region containing that place is worked out, and what is
left over is where in it.  So x of 1000 is three regions east and 232
metres in, and costs exactly what 300 does.

The line printed before the waiting names the region by its square on
the grid rather than by name, because this never asked the map about a
name -- that is the saving.  What is printed after the arrival is the
name, read back with the position.

If nothing is standing on that square the grid answers "no_host", which
is its own way of saying there is no region there.  A position that
would be off the edge of the grid altogether is refused here instead,
since there is no square west of the first one and a handle built from
a negative coordinate would name somewhere real.

## ~ moves by, rather than to

A coordinate written with a leading ~ is measured from where the avatar
is standing rather than from the region's corner.  "~" on its own is
that axis left alone, "~10" is ten metres further along it, and "~-10"
is ten metres back:

    tp ~10 ~ ~             ten metres east, same y, same height
    tp ~ ~ ~50             fifty metres up, without moving
    tp 128 128 ~           the middle of the region, same height
    tp ~-5 ~-5 ~           five metres back on both axes

It is per axis, which is why it is a mark on the number and not an
option: an option would make all three relative or none.

The character is borrowed from the game consoles that have wanted the
same thing, and it is here because the obvious spelling is taken.  A
bare -10 already means ten metres west of this region's corner, which
is a real place in the region next door, so a leading minus cannot also
mean "ten metres back".

Relative and outside-the-region compose, since one is worked out before
the other: from x of 100, "tp ~300 ~ ~" is x of 400, which is 144
metres into the region to the east.

Two of the same relative teleport go twice as far, where two of the
same absolute one go nowhere the second time.  That is the point of it.

It is measured from where the avatar is at the moment the command runs,
which is worth remembering when that is changing.  An avatar standing
on a skybox platform 2001 metres up was moved ten metres north with
"tp ~ ~10 ~", which put it over the edge; it fell, and the next
"tp ~ ~-10 ~" read the height it had fallen to and kept it.  Nothing
went wrong -- relative means relative -- but an absolute height is the
way back.

A position relative to the avatar cannot be given with the name of
another region: the avatar is not standing in that region, so there is
nothing for "~" to be measured from, and it is refused rather than
resolved against a position somewhere else.  A position given with a
region name is that region's own, and is not read as the next region
along -- name the region you mean, or leave the name off and count in
metres.

A name with no position arrives in the middle of the region, at 128,
128, which is where a viewer puts somebody who typed a name into the
world map and said nothing about where in it.  The height asked for is
nothing, which the simulator reads as ground level -- and ground level
in the middle of a region is sometimes under water: Sandbox Goguen's
water is at 5 metres and its middle is below that, so tp with no
position arrives there submerged.  Name a height to land somewhere
else.

## Several regions can answer to one name

The map's search matches from the start of a name and ignores case, so
"Example Landing" comes back beside Example Landing North and everything
else beginning with those words.  A name that matches one of them
exactly is that one, even when longer names shared its prefix -- a name
typed in full is not an ambiguous name.  Anything else that matched more
than once is printed, in the listing regions prints, and refused.

Refused rather than guessed at, because the guess is not a listing to
read again: it is an avatar somewhere it was not sent, with everything
this shell knew about the region it left thrown away on the way.  Type
the name in full, or run regions first to see what the name catches.

Two regions may share a name outright.  There is no more of the name to
type then, and the refusal says so.

## What a teleport costs

About four hundred milliseconds, and up to about five seconds.  Twenty
round trips between Pelmar Reach and Sandbox Goguen -- forty moves -- were
measured end to end at between 355 milliseconds and 4.95 seconds, with a
median of 425 and none of them failing.  So it is quick, and the slow
tail is seconds rather than fractions of one, which is worth knowing
before anything treats a teleport as instant.

The line saying where it is going is printed before the waiting starts,
because a shell that has silently stopped answering is indistinguishable
from one that has hung.  A move inside this region prints no such line:
there is nothing to name that the position typed did not already say, so
the first thing it prints is the position read back after the arrival.

The wait is thirty seconds, which is six times the slowest move ever
measured here and leaves room for a grid having a bad day.  It can be
set with --wait, in seconds, and it covers the arrival -- in this region
or another one -- rather than the map lookup in front of it, which has a
deadline of its own.

## Everything about the old region is gone the moment it arrives

Local ids are the region's own numbering and the next region hands the
same numbers out again, so what the shell knew about objects is not
merely stale after a teleport -- it would name something here as
confidently as it named something there.  All of it is dropped: the
objects, who owns them, what they are called, what is inside them.
Anything a script was holding across a tp has to be asked for again.

What survives is what was never a region's to say: who people are,
inventory, friends, the offers and dialogs still waiting for an answer.
That is a good part of why a teleport is cheaper than the relog it
replaces.

A line of notice says the avatar is now somewhere else.  It comes from
the same place arriving chat does, so it appears whoever asked for the
move -- another client attached to the same daemon, a session
re-established after the circuit was lost -- and it appears for this
command too, alongside the command's own answer.  The two say different
things: one is the session's news, the other is the answer to what was
typed.

## When the grid says no

A refusal is the grid's and not this shell's, and it arrives in two
voices: a key a program could act on and a sentence meant for a person.
They are not always the same string, so both are printed where they
differ -- "MustHaveVIPStatus" comes with a sentence about being a
premium subscriber, and the line carries the key and the sentence.
Where the grid puts the same word in both places the line carries it
once, which is what a handle that is no region does: the refusal for one
of those ends in "no_host" and says it once, not twice.  An estate can
refuse an arrival for reasons the map has no field for, so a region the
map listed is not a region that will have this avatar.

The session is left standing where it was: a refusal costs the request
and nothing else.

Silence is the third answer.  Once this avatar has been handed to
another simulator, the one it left answers no further teleport request
at all -- not a rate limit, and not a wait that clears -- so a teleport
asked for while one is under way runs out its wait and says so.

## Landing near, rather than exactly

The height is the simulator's to settle, and what it does with it was
measured rather than guessed: the avatar arrives at whichever is higher
of the height asked for and the ground under the point, plus about a
metre.  A request for 30 came back as 31, one for 60 as 61, one for 2001
as 2002, and one for 0 as the ground.  So asking for nothing is asking
for ground level rather than for a fall, and asking for a height above
the ground is honoured rather than corrected.  Anything within a few
metres counts as having arrived, and the line printed afterwards is the
position to believe -- with the one exception "where" explains at
length.  Above 1020 metres nothing that follows a moving avatar can say
its height, so a read-back up there is the height the arrival stated
and not the height the avatar may since have fallen or settled to.

A teleport within a region that is refused -- a parcel that will not
have the avatar, a point outside the region -- produces no error and no
reply of any kind.  That is why this waits and reads back: without it a
refusal would be indistinguishable from success, and every position
computed afterwards would be wrong.

## A name that ends in three numbers cannot be typed here

The last three words are the position when all three of them are
numbers, so a region actually called something ending in three numbers
is out of reach, and so is one whose whole name is numbers.  It is
written down rather than defended against: the defence would be a
quoting rule everybody has to remember, for a region nobody has met.

A line that is nothing but numbers and is not three of them is a
position typed short, and is refused as one rather than sent to the map
as a name.

## Examples

    tp 128 128 25
    tp ~10 ~ ~
    tp 300 128 25
    tp Example Landing
    tp Example Landing 128 128 2001
    tp --wait 60 Example Shallows

## A negative coordinate and where the end of options goes

The negative forms are worth one note, and nothing has to be typed
differently for them.  "tp -10 128 25" begins with something that looks
like an option -- to an option parser "-10" is the option -1 with the
value 0 -- so the shell puts an end-of-options mark in front of the
position for you.

It goes there only where the options have not ended already, which is
the position typed on its own and the position after a flag:
"tp --wait 60 -10 128 25" has its flag read as one and its position
read as a position.  A region's name ends the options where it stands,
so "tp Example Landing -10 128 25" needs no mark and gets none; one put
there would not be a mark at all but a word, and would be joined onto
the name.

See also: regions for finding out what a name matches and where it is,
where for the position this is given in, look for what the region
underfoot says about itself, neighbours for walking over a border into
the next region rather than teleporting across the grid, move for
shifting an object rather than the avatar, and answer for accepting a
teleport somebody else offered.
