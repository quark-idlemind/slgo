`save` writes a file from this machine into a notecard or a script
that already exists in inventory.  It is what `cat` reads back, and
it is not what makes an item: `new` does that.

    save notes.txt Notecards/README

Two arguments, the file first and the item second.  The item takes
the rest of the line, since a name with spaces in it is ordinary in
inventory and rare on disk.

The item's own type decides which write happens.  A link is refused
rather than followed: it carries the type of whatever it points at,
so it looks exactly like the notecard it names, and the write would
go to the link's own item, which is not where the text lives.

## A script that does not compile is still saved

The item is written and then compiled, in that order, so the verdict
is a clause on the same line rather than a failure.  The compiler
reports the first error and stops.  The file goes up as it stands:
unlike `new`, nothing here puts a skeleton in a script given no
source.

## What it does not do

It does not start anything and does not touch the world.  A script
in inventory is not running and cannot be made to run; putting one
there and starting it is `new --in OBJECT`.  Nor does it reach the
copies already inside objects: they were separate the moment they
were put there, and a save leaves every one of them as it was.

## Examples

    save notes.txt Notecards/README
    save hello.lsl /Scripts/greeter

See also: `cat`, `new`, `drop`, `start`, and `put` for an image,
which costs L$.
