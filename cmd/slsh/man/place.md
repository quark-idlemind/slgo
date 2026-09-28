`place` takes one object out of inventory and puts it into the
world.  It is what take undoes: take brings a rezzed object in, place
puts one back out, and between them an object can be picked up and set
down again.

    place Objects/probe

The argument is an inventory path, relative to the folder the shell is
in, or the uuid of an item.  A folder is refused: `place` puts out one
object, not the contents of a folder.

The active group matters more than it looks.  A parcel usually grants
"create objects" to a group rather than to a person, so an avatar with
the wrong group active is refused by the land even where it plainly has
permission, and the refusal talks about the land rather than about the
group.  `group` says which is active, and sets it.

A `place` interrupted while it waits for the object to appear, which
then turns up all the same, puts it in the trash rather than leaving it
standing with nothing holding on to it, and says so with the error.

## Options

**--at** *X,Y,Z*

Where to put it, in this region.  Without it, a metre east of the
avatar: the avatar's own position with one metre added to X, near
enough to hand for something that is about to be looked at.  East and
not in front -- nothing here asks which way the avatar is facing, so a
thing put down behind somebody goes down east of them all the same.

Two or three numbers with commas between them and no spaces.  A pair
is accepted rather than refused, and the Z that was not given is zero,
which is the bottom of the region and not the height the avatar is
standing at.  `rez`'s `--at` is spelt the same and insists on three.

## One object, and only one

`place` takes exactly one argument, and a name with a space in it has
to be quoted.  Extra words are not a position: `place probe 10 20 30`
is refused rather than treated as a move.  Moving something already in
the world is `move`.

## Examples

Put a thing down beside the avatar:

    place Objects/probe

Put it somewhere named, quoting the name because it has a space in it:

    place --at 128,128,25 "Objects/big sign"

## A link is followed

An outfit folder holds links rather than items.  A link carries the
same name as the thing it points at, and a listing tells the two apart
only by the word `link` in the type column of `ls -l`, so a path taken
from `My Outfits` names a link almost every time.

`place` follows one to the item at the other end, which is what the id
it sends has to be: the grid has no object for a link's id and answers
an id it does not recognise with silence rather than with a refusal.

A link this inventory cannot follow is refused, naming the id it
looked for.  A link outlives what it pointed at, so an outfit put
together years ago may name things that have since been deleted.

See also: `take`, `move`, `group`, `rez` for building an object out of
a JSON file rather than out of inventory, and `perms` for what others
may do with it once it is standing there.
