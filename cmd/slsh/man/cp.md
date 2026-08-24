cp duplicates one inventory item under a new name.  mv is the other
half of the pair: mv leaves one item where cp leaves two.

The first argument is what to copy -- a path relative to the folder
the shell is in, or a uuid -- and the rest of the line is the new
name, so a name with spaces in it needs no quoting.  A folder is
refused; only items are copied.

## Where the copy lands, and where it does not

Here, in the folder cd last set, which is what a bare name means to cp
everywhere else.  There is no destination folder to give: copying
something into another folder is cp and then mv.

## Why this is worth having at all

Not tidiness.  An avatar that may not rez on this parcel cannot make
an object in the world at all, and can still copy one it already owns,
because copying asks the land nothing -- it is entirely between the
avatar and its own inventory.  So an object needed twice can be had
twice without asking anybody for permission to build.

## A no-copy item never appears

The grid makes the copy when it gets round to it, so this waits for it
to show up in the folder and prints its id when it does.  An item
whose permissions forbid copying produces nothing at all rather than a
refusal, and the wait ends after a minute saying so.  That is the
usual reason a cp seems to hang: it is not the shell being slow, it is
an item that was never going to be duplicated.  Nothing here reports
what an item's permissions are beforehand, so the copy not arriving is
how that is found out.

## Examples

    cp probe probe, the second

Into another folder, which is two steps:

    cp probe spare
    mv spare Objects/lanterns

See also: mv, mkdir, rm, place for putting a copy out into the world,
and give for handing one to somebody else.
