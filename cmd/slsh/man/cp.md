`cp` duplicates one inventory item under a new name.  `mv` is the
other half of the pair: `mv` leaves one item where `cp` leaves two.

    cp probe probe, the second

The first argument is what to copy -- a path relative to the folder
the shell is in, or a uuid -- and the rest of the line is the new
name, so a name with spaces in it needs no quoting.  A folder is
refused; only items are copied.

The copy lands here, in the folder the shell is in.  There is no
destination folder to give: copying something into another folder
is `cp` and then `mv`.

Copying asks the land nothing.  An avatar that may not rez on this
parcel cannot make an object in the world, and can still copy one
it already owns.

## A no-copy item never appears

The grid makes the copy when it gets round to it, so this waits for
it to show up in the folder and prints its id when it does.  An
item whose permissions forbid copying produces nothing at all
rather than a refusal, and the wait ends after a minute saying so.
That is the usual reason a `cp` seems to hang: it is not the shell
being slow, it is an item that was never going to be duplicated.

A `cp` interrupted while it waits, whose copy turns up all the same,
says so with the error and gives the copy's id there.

`ls -l` says beforehand whether the item may be copied: a `C` in its
fourth column, as in `MCX`, and a dash there, as in `M-X`, for one that
may not.  `dump --item` prints all its masks.  Without looking first,
the copy not arriving is how a no-copy item is found out.

## Examples

    cp probe probe, the second

Into another folder, which is two steps:

    cp probe spare
    mv spare Objects/lanterns

## A link is not followed

A link is an inventory entry in its own right, and `cp` acts on the
entry.  Copying a link makes another link.  An item may have links to
it from several outfits, and following one here would reach past all
of them to the item they share.

See also: `mv`, `mkdir`, `rm`, `place` for putting a copy out into
the world, and `give` for handing one to somebody else.
