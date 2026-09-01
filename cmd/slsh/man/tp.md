Moves the avatar: to a point in this region, to another region by
name, or home.  It waits for the arrival rather than reporting the
request.  What is printed afterwards is where the avatar actually
ended up, read back, and not the point that was asked for.

Three numbers and nothing else -- X Y Z, in this region's metres -- is
a move to that position.  The word `home` on its own is this account's
home position.  Anything else is a region's name, joined with spaces so
that a name with one in it needs no quoting, and the last three words
are the position when all three are numbers:

    tp 128 128 25
    tp home
    tp Example Landing
    tp Example Landing 33 73 1000.5

A trailing comma on a number is ignored, so a position read off `where`
or off the line above a map pastes back in as it stands: `tp 128, 128, 25`
is the first of those three again.

## Options

**-w, --wait** *SECONDS*

How long to wait for the avatar to arrive.  Without it, thirty
seconds.

## Home

`tp home` goes to wherever this account's home is set.  It is the one
destination that is not typed out: the map is asked nothing, no name is
looked up and no position is computed, because the grid keeps home
itself and the whole of what goes out is "home".  It is
`landmark --home` under a shorter name and behaves identically,
including the wait.

The word is matched without regard to case, as every other name this
shell matches is, so `tp HOME` is the same trip.

Going home while standing at home is refused: the grid will not shorten
a teleport that arrives where it started, and says `CouldntTPCloser`.
The line says what it usually means.

The bare word is home, and only the bare word.  A region really called
"home" is still reached in the forms that carry more than one word --
`tp home 128 128 25` is that region, as it always was.  On 2026-09-01
the map had nine regions whose names begin with "home" and none called
exactly that, so nothing is out of reach today; a region named tomorrow
would be reached with a position after it.

Where home is cannot be read from here, and going there is the only way
to find out.  `landmark --set-home` is what puts it somewhere else: it
sets home to where the avatar is standing, so the way to move home is
to go there and then set it.

## A position outside this region is the region it really is in

A region is 256 metres square and the grid is regions laid edge to
edge, so x of 300 is not an error and not a point in this region: it is
44 metres into the region to the east.  x of -10 is 246 metres into the
one to the west.  The shell works out which region owns the point and
teleports there, the way the 25th hour of Monday is one in the morning
on Tuesday.

    tp 300 128 25          44 metres into the region east of here
    tp -10 128 25          246 metres into the region west of here
    tp 128 -1 25           255 metres into the region to the south

It generalises as far as the arithmetic does.  x of 1000 is three
regions east and 232 metres in.  The line printed before the waiting
names the region by its square on the grid rather than by name; what is
printed after the arrival is the name, read back with the position.

If nothing is standing on that square the grid answers `no_host`.  A
position that would be off the edge of the grid altogether is refused
here instead.

## ~ moves by, rather than to

A coordinate written with a leading `~` is measured from where the
avatar is standing rather than from the region's corner.  `~` on its
own is that axis left alone, `~10` is ten metres further along it, and
`~-10` is ten metres back:

    tp ~10 ~ ~             ten metres east, same y, same height
    tp ~ ~ ~50             fifty metres up, without moving
    tp 128 128 ~           the middle of the region, same height
    tp ~-5 ~-5 ~           five metres back on both axes

It is per axis, which is why it is a mark on the number and not an
option: an option would make all three relative or none.  A bare `-10`
already means ten metres west of this region's corner -- a real place
in the region next door -- so a leading minus cannot also mean "ten
metres back".

Relative and outside-the-region compose: from x of 100, `tp ~300 ~ ~`
is x of 400, which is 144 metres into the region to the east.

Two of the same relative teleport go twice as far, where two of the
same absolute one go nowhere the second time.  That is the point of it.

It is measured from where the avatar is at the moment the command runs.
An avatar that has just walked off a platform, or fallen, is moved from
there.  An absolute height is the way back.

`~` cannot be given with the name of another region: the avatar is not
standing in that region, so there is nothing for it to be measured
from.  A position given with a region name is that region's own, and is
not read as the next region along.

A name with no position arrives in the middle of the region, at 128,
128.  The height asked for is nothing, which the simulator reads as
ground level -- and ground level in the middle of a region is sometimes
under water.  Name a height to land somewhere else.

## Several regions can answer to one name

The map's search matches from the start of a name and ignores case, so
`Example Landing` comes back beside Example Landing North and everything
else beginning with those words.  A name that matches one of them
exactly is that one, even when longer names shared its prefix.  Anything
else that matched more than once is printed, in the listing `regions`
prints, and refused.

Refused rather than guessed at, because the guess is not a listing to
read again: it is an avatar somewhere it was not sent.  Type the name
in full, or run `regions` first to see what the name catches.

Two regions may share a name outright.  There is no more of the name to
type then, and the refusal says so.

## How long it waits

A teleport to another region prints a line before the waiting starts,
because a shell that has silently stopped answering is
indistinguishable from one that has hung.  A move inside this region
prints no such line: the first thing it prints is the position read
back after the arrival.

The wait is thirty seconds and covers the arrival, in this region or
another one, rather than the map lookup in front of it.

Everything the shell knew about the old region is dropped on arrival:
the objects, who owns them, what they are called, what is inside them.
What survives is what was never a region's to say -- who people are,
inventory, friends, the offers and dialogs still waiting.

A line of notice says the avatar is now somewhere else.  It appears
whoever asked for the move, alongside this command's own answer.  The
two say different things: one is the session's news, the other is the
answer to what was typed.

## When the grid says no

A refusal is the grid's, and it arrives in two voices: a key a program
could act on and a sentence meant for a person.  Both are printed where
they differ.  Where the grid puts the same word in both places the line
carries it once, which is what a handle that is no region does: the
refusal ends in `no_host` and says it once, not twice.  An estate can
refuse an arrival for reasons the map has no field for, so a region the
map listed is not a region that will have this avatar.

The session is left standing where it was.

Silence is the third answer.  Once this avatar has been handed to
another simulator, the one it left answers no further teleport request
at all, so a teleport asked for while one is under way runs out its
wait and says so.

## Landing near, rather than exactly

The height is the simulator's to settle.  The avatar arrives at
whichever is higher of the height asked for and the ground under the
point, plus about a metre.  Asking for nothing is asking for ground
level rather than for a fall.  The line printed afterwards is the
position to believe -- with the one exception `where` explains: above
1020 metres a read-back is the height the arrival stated, not the
height the avatar may since have fallen or settled to.

A teleport within a region that is refused -- a parcel that will not
have the avatar -- produces no error and no reply of any kind.  That is
why this waits and reads back: without it a refusal would be
indistinguishable from success.

## A name that ends in three numbers cannot be typed here

The last three words are the position when all three of them are
numbers, so a region actually called something ending in three numbers
is out of reach, and so is one whose whole name is numbers.

A line that is nothing but numbers and is not three of them is a
position typed short, and is refused as one rather than sent to the map
as a name.

## A negative coordinate

`tp -10 128 25` begins with something that looks like an option, so the
shell puts an end-of-options mark in front of the position.  It goes
there only where the options have not ended already, which is the
position typed on its own and the position after a flag:
`tp --wait 60 -10 128 25` has its flag read as one and its position
read as a position.  A region's name ends the options where it stands,
so `tp Example Landing -10 128 25` needs no mark and gets none; one put
there would be joined onto the name.

## Examples

    tp 128 128 25
    tp ~10 ~ ~
    tp 300 128 25
    tp Example Landing
    tp Example Landing 128 128 2001
    tp --wait 60 Example Shallows
    tp home

See also: `regions` for finding out what a name matches and where it is,
`where` for the position this is given in, `look` for what the region
underfoot says about itself, `neighbours` for walking over a border
into the next region rather than teleporting across the grid, `move`
for shifting an object rather than the avatar, `landmark` for a place
said by name in inventory and for setting where home is, and `answer`
for accepting a teleport somebody else offered.
