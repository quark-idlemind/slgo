`fetch` brings an item out of a rezzed object and into inventory.  It
is `drop` the other way round, and what a viewer does when something
is dragged out of an object's Contents tab or its Copy to Inventory
button is pressed.  It is not picking the object itself up -- that is
`take`.

    fetch lantern greeter

The object comes first and the item takes the rest of the line, as in
`drop`, so an object whose name has a space in it has to be quoted and
an item whose name has one need not be.  The object is named by the
word the region calls it or by its uuid.  The item is named exactly,
in the case it has, or by the id `ls -l --in` prints for it.  An
object renames a second item of one name as it goes in, so a name
inside one is never two items; a name that is nothing there is
refused, naming what differs from it only in case.

It lands in the folder the shell is in, where `new` and `mkdir` would
make something.  A viewer would put it in the folder for its kind
instead -- a notecard in Notecards -- and `--into` says so when that is
what is wanted.

## Copied, or moved out

The simulator decides which, by the item's permissions, and nothing in
the request can overrule it.  An item this avatar may copy is copied
and the object keeps it.  One it may not copy is moved: it comes into
inventory and the object does not have it any more.

So an item that would be moved is refused unless `--move` says that is
meant, and the refusal says why.  An item that is somebody else's and
may not be copied is refused whatever is said, since only the object's
owner can take it out.  One that everyone may copy -- the contents of a
box of freebies -- is copied from anybody's object.

## Options

**--into** *FOLDER*

The folder it lands in, as a path or as the folder's uuid.  Without
it, the folder the shell is in.

**--move**

Bring out an item that may not be copied, which leaves the object
without it.

**-w, --wait** *SECONDS*

How long to let the region describe itself before giving up on finding
the object.  Without it, thirty.

## Nothing answers

The region does not reply to the request, so the new item is looked for
in the folder it was sent to, and after forty seconds a fetch that
never arrived says so.  A refusal the region makes looks the same as
one that is slow: the item never appears.

## Examples

    fetch lantern greeter
    fetch --into /Notecards "brass lantern" README
    fetch --move lantern only copy

See also: `drop` for putting an item in, `ls --in` for what an object
holds, `cat --in` for reading a notecard or a script where it is, and
`take` for bringing in the object itself.
