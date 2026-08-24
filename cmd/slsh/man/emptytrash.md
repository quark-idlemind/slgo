`emptytrash` throws away everything in the trash, permanently, and
leaves the folder itself standing.  `rm` is the other way of getting
rid of something and does not go through the trash at all: what is
in there was put there by a viewer, and `rm` deletes outright.

    emptytrash

It takes no argument.  There is one trash and nothing to say about
it.

## Found by what it is, not by what it is called

The grid marks a folder with the kind of thing it prefers to hold, and
the trash is the folder carrying the mark for it.  Its name is not the
test, and cannot be: the folder can be renamed, and an inventory
made through a viewer in another language never called it `Trash` in
the first place.  An inventory with no such folder is an error
rather than a silent success.

## The count is one level

What it prints is how many entries were directly in the trash,
counted before the purge.  A folder in there counts as one entry and
goes with everything inside it, so a trash of a few folders can
report a small number for a great many things.  An empty trash says
so and does nothing.

Nothing is examined on the way out.  There is no undo, no list of
what went, and no check that the trash held nothing wanted.  `ls` on
the trash before running it is the whole of the safety there is.

## Examples

    ls /Trash
    emptytrash

See also: `rm`, `mv` for taking something out of the trash rather
than losing it, and `ls`.
