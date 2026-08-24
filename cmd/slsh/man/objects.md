`objects` lists what the region has described so far: one line per
object, its key, its name and where it is, gathered under whoever owns
it.  It is the listing to look at when `take`, `move`, `touch` and the
rest say they cannot find a thing.

    objects lantern

With no argument it is everything in range.  A word matches any part of
a name, without regard to case.  Quote a name that has a space in it:
only the first word is searched otherwise.

The name printed is the one `take` and `move` want typed back, in
full.  A search for `lantern` will find `brass lantern`; `take
lantern` will not.

## Options

**-c, --children**

The prims of each object as well, indented under it.  Without it, the
listing is the roots.

**--owner** *WHO*

Only one owner's things: a uuid, or a pattern for the name.  Somebody
nobody has named cannot be matched by a pattern about names, so those
are counted at the foot instead of being quietly dropped.

## Roots, and the prims inside them

What a person means by an object is the root of a linkset, so the roots
are the listing.  Printing every prim would turn a hundred things into
a thousand lines, most of them called `Object` and placed at an offset
from something the listing did not name.

A search is treated differently from browsing.  A root matches on its
children's names as well as its own, because the name somebody remembers
is often on a prim inside; and a prim whose own name was asked for is
printed even when the listing is only roots.  The foot of the listing
says how many prims were held back.

A prim whose root nothing has described is listed on its own, saying
so, rather than dropped.

## What has been described, and what has not

The simulator describes what is near the camera and nothing else.  An
object beyond the draw distance is not unnamed here, it is unknown, and
a region with nothing of that name in it reads exactly the same as one
where the thing is too far away.

The first run is slow: names are asked for one by one.  The answers are
kept, so the next run is quick.

## Whose things these are

The owner heads each group.  Attachments are in the listing too.  Their
last column says which point they are worn on, and names the wearer
when it is not the owner.  Avatars themselves are not listed: `who` is
the listing for people.

## Examples

    objects lantern
    objects -c "garden chair"
    objects --owner "^Example"

See also: `who`, `look`, `worn` for this avatar's attachments alone,
and `take`, `move`, `dump` and `unlink` for the things done to what it
finds.
