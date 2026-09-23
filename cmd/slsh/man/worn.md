`worn` lists everything the avatar has on: clothing, body parts and
attachments together, with where each one is worn.

    worn

The first column is the slot for a system wearable -- `shirt`, `skin`,
`shape`, `eyes` -- and the attachment point for an object.  A wearable
has no attachment point, because it is not attached to anything.

With no argument it is everything.  A word matches the name, without
regard to case, and the name only -- `worn hud` does not list the HUD
attachments.  It lists whatever is called something with `hud` in it.
Wearables come first, then attachments sorted by point, so the HUD
ones fall together without being asked for.

The word comes last.  Flags are read only until the first ordinary
argument, so `worn -l auto` is the long listing of the auto objects and
`worn auto -l` searches for the name `auto -l` and matches nothing.

## Two records, and where they disagree

This reads two things that are not the same thing.

The **Current Outfit folder** is what should be on: a link per worn
thing, written by whatever put it on.  It is the grid's own record and
the one the baking service reads.

The **region's description of the objects around us** is what is on,
and covers attachments only, since a shirt is not an object.

Both are listed, and a line that came from only one of them says so.

    (in the outfit, not described)

The folder names it and the region has not mentioned it.  It may have
failed to rez at login, which happens; or this session may never have
been told, since an attachment is announced when it goes on and at
login and at no other time.  It is still worn, and `detach` still
takes it off -- that needs the inventory item and nothing about the
object.

    (not in the outfit, so it will not come back)

The region describes it and the folder does not hold it.  It is on the
avatar now and will be gone at the next login, because nothing wrote
the record.  Wearing it again from here puts that right.

    (in the outfit, and the item is gone)

The link outlived what it pointed at.

## A third record, which settles the first line

"In the outfit, not described" can mean off, or on and not yet
described, and the two records above cannot tell those apart.  The
simulator's own list of what is attached can.  It arrives with each
bake of the avatar's appearance, and names every attachment on the body
by its object -- the object, not the inventory item, so it cannot say
which line is which, only how many are on.

It is shown only when it settles something:

    (the simulator lists 1 attachment that nothing here has described:)
    head               ?

Something is on that nothing has described; with `-l` its object is
printed too.

    (the simulator lists nothing that is not described here, so what is
    marked not described is off -- unless it is a HUD, which it never lists)

HUDs are never in the list, not even in the copy an avatar is sent
about itself, so it can say nothing about one.

    (the simulator's list of what is worn is from before the last change
    to the outfit, so it cannot settle what is not described)

The list is sent only when the avatar is baked.  `wear` and `detach`
ask for a bake afterwards, as a viewer does, so this should be seen
only after something else changed the outfit, or after a bake that
failed.

## Options

**-l**

Print two keys: the inventory item first, then the object.  The item is
the one that does not change.  The object is rezzed afresh, with a new
key, every time it goes on and every time the avatar logs in, so a key
written down from a previous session names nothing.

A wearable has no object at all and prints `-` in that column, and so
does an attachment nothing has described.

The item id is also what tells two attachments apart when their names
agree, which is the refusal `detach` gives for a word that is worn
twice.

## The names come from inventory

A worn object will not answer a request for its properties, so the name
printed is the inventory item's.  That is the name `detach` matches
against, and unlike the object's name it does not change.

Inventory is walked a few levels down rather than in full.  Anything
not found in that much of it is printed as its item key -- the item may
sit deeper than the listing went, or have been deleted while still
worn, which Second Life allows.  A key here means the name was not to
hand, not that the thing is nameless.

## What this can and cannot see

The region describes an attachment when it goes on, and again at every
login, and never otherwise.  Whatever held the session at login heard
all of that, and is what answers the second half of this listing, so a
shell that attached later is not at a disadvantage.

What no one heard, no one can place.  That is what the folder is for:
a thing nobody described is still listed, still named, and still comes
off, with the first column left as `-` rather than a point invented for
it.

Measured on Agni, an avatar sitting on a chair had one attachment of
ten described and the other nine only in the folder.  That was not the
region being quiet.  It had described all ten; the daemon had then
thrown them away, because the chair itself had never been described
and so nothing hanging off the avatar on it could be placed.  That is
mended, and an avatar that has been logged in for a while should show
nothing here.  A line that does is worth a look rather than a shrug.

Clothing and body parts are not described by the region at all, in any
circumstances.  They come from the folder alone.

## Examples

    worn
    worn -l auto
    detach lantern

See also: `wear`, `detach`, `objects` for the same things as the region
sees them, and `auto` for the worn objects the benchmarks use.
