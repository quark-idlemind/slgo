place takes one object out of inventory and puts it into the
world.  It is what take undoes: take brings a rezzed object in, place
puts one back out, and between them an object can be picked up and set
down again.

The argument is an inventory path, relative to the folder the shell is
in, or the uuid of an item.  A folder is refused: place puts out one
object, not the contents of a folder.

Without --at it lands a metre east of the avatar: the avatar's own
position with one metre added to X, which is near enough to hand for
something that is about to be looked at.  East and not in front --
nothing here asks which way the avatar is facing, so a thing put down
behind somebody goes down east of them all the same.  Measured: the
position it reported back was the avatar's, X one larger and Y and Z
unchanged.  --at is a position in this region -- two or three numbers
with commas between them and no spaces.  A pair is accepted rather than
refused, and the Z that was not given is zero, which is the bottom of
the region and not the height the avatar is standing at.  rez's --at is
spelt the same and insists on three.

The active group matters more than it looks.  A parcel usually grants
"create objects" to a group rather than to a person, so an avatar with
the wrong group active is refused by the land even where it plainly has
permission, and the refusal talks about the land rather than about the
group.  "group" says which is active, and sets it.

## One object, and only one

place takes exactly one argument, and a name with a space in it has to
be quoted.  That is stricter than it needs to be, deliberately.  Until
recently "place" was the command that moved something already rezzed,
so "place probe 10 20 30" is a line somebody may well type or have in a
file -- and an object taken into inventory keeps its name, so the
lookup usually succeeds.  Reading the first word and ignoring the rest
would answer that line by putting a second copy beside the avatar,
reporting success, and leaving the object that was meant to move
exactly where it was.  Moving one that is already in the world is move.

## Examples

Put a thing down beside the avatar:

    place Objects/probe

Put it somewhere named, quoting the name because it has a space in it:

    place --at 128,128,25 "Objects/big sign"

See also: take, move, group, rez for building an object out of a JSON
file rather than out of inventory, and perms for what others may do
with it once it is standing there.
