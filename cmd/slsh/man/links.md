`links` asks an object for the region's own numbering of its prims, and
sets it beside the numbering this shell has read off the packets.

    links lantern

The numbering the region counts by is the one `llGetLinkNumber`,
`llGetLinkKey` and `llSetLinkColor` use, and a viewer cannot be told it:
it works the order out from the order the region described the prims in.
`objects --how` shows what it worked out and how sure it is.  `links`
is the way to be certain, by having the object say it.

The object is named by the word the region calls it or by its key, and
it is the root: a prim inside another object is refused, with the local
id it is linked under, since its number means nothing without the whole.

## How it asks

A small script is dropped into the root, which says each link's number,
key and name to the owner and removes itself.  The script is kept in the
avatar's Scripts folder as "slgo linkmap", made the first time, so a
call costs a drop and about a second, not an upload.  Afterwards the
object's contents are read, and a copy that did not remove itself is
removed.

The script going in and coming out are two changes of the object's
inventory, and the object's own scripts are told of each with
`changed()` and CHANGED_INVENTORY.  A product that reloads its
configuration, resets or re-reads a notecard on that is then not in the
state it was in before `links` was asked.  A person asks for `links`, so
it goes ahead; do not run it against a product whose state matters
without meaning that, and note that a Slate file never does it unasked
(its `linkmap` header is the file's way of asking).

That needs a script to go into the object, so the avatar must own it and
be allowed to modify it.  An object it may not modify is refused with a
sentence before anything is dropped, and its order stays what `objects
--how` says of it: the store's best reading from the packets.  Land that
runs no scripts is refused the same way.

## What it prints

Each prim, by the object's own link number, with its key and name and the
number the store had for it, and `differs` where the two are not the
same.  Then a verdict: the store agreed, and whether it had called the
order known or not; or it differed, at which links.

An avatar sitting on the object is numbered after the prims, in the order
it sat; it is counted and left out of the list.  A prim the store has not
been told of yet is `not seen by the store`, and then there is nothing to
compare or to tell it.

## The store is told

Whatever it found, the order the object gave is handed to the store, which
takes it where it differs and marks the set confirmed by the object's own
script.  `objects --how` says so from then on, and a Slate file's `link N`
reads it.  A link or an unlink of the object, or a prim killed in it,
takes the confirmation away.  A daemon older than the call is told
nothing and says so.

## Options

**-w, --wait** *SECONDS*

How long to let the region describe itself.  Without it, thirty.

## Examples

    links lantern

The object by key, where a name is shared:

    links 4fa27e57-...

See also: `objects` and its `--how` for what the store thinks, `drop` for
putting a script inside an object by hand, `link` and `unlink` for
changing the numbering, and `ls --in` for what an object holds.
