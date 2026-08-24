`worn` lists what this avatar has on and where each thing is worn.  It
is what `wear` adds to and what `detach` takes from, so a thing that is
not here is a thing `detach` will say is not worn.

    worn

With no argument it is everything.  A word matches the name, without
regard to case, and the name only -- `worn hud` does not list the HUD
attachments.  It lists whatever is called something with `hud` in
it.  The lines are sorted by attachment point, so the HUD ones fall
together without being asked for.

The word comes last.  Flags are read only until the first ordinary
argument, so `worn -l auto` is the long listing of the auto objects and
`worn auto -l` searches for the name `auto -l` and matches nothing.

## Options

**-l**

Print two keys: the inventory item first, then the object.  The item is
the one that does not change.  The object is rezzed afresh, with a new
key, every time it goes on and every time the avatar logs in, so a key
written down from a previous session names nothing.

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
all of that and is what answers here, so a shell that attached later
still sees the full list.  What no one heard, no one can list.

## Examples

    worn
    worn -l auto
    detach lantern

See also: `wear`, `detach`, `objects` for the same things as the region
sees them, and `auto` for the worn objects the benchmarks use.
