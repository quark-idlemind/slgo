`drop` puts an inventory item inside a rezzed object.  It is not
putting something down in the world -- that is `place` -- and it is
not picking something up -- that is `take`.  `rm --in` is what takes
one out again, and `ls --in` is what says which are in there.  Nothing
is offered and nobody is asked: this is the avatar's own object, so it
happens at once, where `give` offers an item to a person and waits for
them to accept it.

    drop lantern Scripts/greeter

The object comes first and the item takes the rest of the line, so an
object whose name has a space in it has to be quoted and an item whose
name has one need not be.  The object is named by the word the region
calls it or by its uuid; where several objects answer to one word it is
refused, with their uuids printed, since editing the wrong one is not
something that can be taken back.  The item is a path relative to the
folder the shell is in, or a uuid, and a folder is refused -- `drop`
takes one item.

## It is a copy, and the object keeps every copy

The item stays in inventory and a copy of it goes in.  An object
renames a newcomer whose name it already has rather than replacing it,
so dropping the same item in twice leaves two entries of nearly one
name, and it is worth listing the object rather than assuming the
second drop overwrote the first.

Ask twice.  A listing run straight after a drop is often the listing
from before it.  `ls --in` forgets what it was told last and asks the
object again every time, so the answer without the item in it is the
region's own answer, freshly given.  Nothing has gone wrong when it
does: the object has the item, and what is being said about it has not
caught up.

What goes in is the whole item, permissions and all, so something whose
permissions were set carefully is inside the object set that way.

## A script dropped in does not run

Copying a script into an object leaves it there uncompiled, and
nothing about it will change that: `start` has nothing to start,
because there is nothing compiled to start.  Putting a script into an
object by hand looks like one action and is really two -- the copy, and
the save that compiles it inside the object.  `new --in OBJECT` does
both, and is the way in for anything that has to run.  Notecards,
textures and everything else that only has to be there are what this
command is for.

## Examples

    drop lantern Scripts/greeter
    drop "brass lantern" Notecards/README

## A link is followed

An outfit folder holds links rather than items.  A link carries the
same name as the thing it points at, and a listing tells the two apart
only by the word `link` in the type column of `ls -l`, so a path taken
from `My Outfits` names a link almost every time.

`drop` follows one to the item at the other end, which is what the id
it sends has to be: the grid has no object for a link's id and answers
an id it does not recognise with silence rather than with a refusal.

A link this inventory cannot follow is refused, naming the id it
looked for.  A link outlives what it pointed at, so an outfit put
together years ago may name things that have since been deleted.

See also: `new`, `rm`, `ls`, `start`, `stop`, `place` for putting an
object into the world, `take` for bringing one in, and `give` for
handing an item to somebody else instead.
