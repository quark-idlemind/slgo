`mv` moves something into another folder, or renames it where it is.

    mv probe Objects/lanterns

The first argument is what to move -- a path relative to the folder
the shell is in, or a uuid, an item or a folder alike.  The second
is where it goes.

## Options

**--in** *OBJECT*

Rename something inside a rezzed object rather than in inventory.
Only a rename: an object holds no folders, so there is nowhere in
one to move anything to.  The object is named by the word the region
calls it or by its uuid, and a name several objects answer to is
refused with their uuids rather than guessed at.  The thing inside
is named by its name, matched exactly and in the case it has.  The
id beside a line of `ls -l --in` belongs to the object's own copy,
and is not something to give this.

Nothing answers a rename, so the object's contents are read back
until the item has its new name, for up to fifteen seconds, and one
that never does is an error rather than reported done.  The viewer
renames nothing the avatar may not modify.

## A move is read back

Nothing answers a move, so the folder it goes into is listed until
the thing moved is there, for up to fifteen seconds, and one that
never arrives is an error rather than reported done.  A move into
Trash is one of those: the grid takes it and does nothing.  `rm` is
what throws something away.

## What decides between moving and renaming

The destination.  If it names a folder, this is a move and the name
is kept.  If it is a plain name that is no folder, this is a rename
and the place is kept.  If it is neither -- a path through folders
that do not exist -- it is refused, since a rename cannot be to a
path.

The trap is in the first of those.  A destination is tried as a
folder first, so renaming something to a word that happens to be a
folder here moves it into that folder instead, quietly and
successfully.  `mv README Notecards` beside a folder called
Notecards does not make a notecard called Notecards; it files the
one there already.

Moving and renaming at once is two commands, in either order.

A folder can hold several things of one name, and a path means the
first of them.  Where that is not the one wanted, `ls -l` prints the
id beside each and an id may be given wherever a path is.

## Examples

    mv probe Objects/lanterns
    mv probe "probe, the second"
    mv --in lantern hello.lsl greeter

## A link is not followed

A link is an inventory entry in its own right, and `mv` acts on the
entry.  Renaming or moving a link renames or moves the link.  An item
may have links to it from several outfits, and following one here
would reach past all of them to the item they share.

See also: `cp`, `mkdir`, `rm`, `ls`, and `drop` for putting an
inventory item inside an object in the first place.
