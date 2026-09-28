`detach` takes a worn object off.

    detach hat

What it names is different from what `wear` names.  `wear` names
something in inventory, because that is where the thing to be put on
is.  `detach` names something worn, and matches the word against what
is actually on the avatar -- so a name that is in inventory and not on
the avatar is a sentence saying so rather than a detach that reports
success and does nothing.

A path is allowed and only its last name is used: nothing about a worn
object says which folder its item came from.  The name is matched
exactly, in the case it has: `hat` and `Hat` are two items, and both
may be on.  A name worn only in another case is refused, and the
refusal names what it is near.  A uuid is looked for as
either the inventory item or the worn object, since the region's answer
holds both.

## Options

**-w, --wait** *SECONDS*

How long to give the region to agree the thing is off.  Without it,
fifteen.

## What `worn` can and cannot see

The region describes an attachment when it goes on, and again at login,
and never otherwise.  An avatar dressed before this session began can
therefore be wearing something nothing here has ever heard of.  If the
Current Outfit folder names it, `detach` takes it off by that, without
waiting for a region that never described it; otherwise `detach` will
say it is not worn.  It is not a denial that the thing is on; it is
this session not having been told.  `worn` lists what can be seen.

A name that is worn twice is refused with the points and the item ids
printed, as `worn -l` prints them; an item id is what tells the two
apart.

## It waits

Nothing replies to a detach.  The request goes out, and what says it
worked is the object no longer being among what is worn -- which
arrives some time later.  `detach` polls until the region agrees before
it says the thing is off, and a wait that runs out is reported as not
knowing rather than as failing.

Either way its link is taken out of the Current Outfit folder, since a
link left there puts the thing back on at the next login, and a rebake
is asked for, as `wear` asks for one after every change to the folder.
When that fails too, the report says both: that the region has not
agreed, and that the link is still there.

## Clothing comes off; a body part does not

`detach` takes off a system wearable too, and works out which it is
from the name: a shirt, an alpha, a tattoo layer are not worn on a
point, so taking one off is dropping its link out of the Current
Outfit folder and asking for a rebake.  The report says the slot.

    detach a blue shirt
    a blue shirt is no longer worn as shirt

Attachments are looked for first, since that is the common case and
the wearables are one more read.  A name that is neither is the
ordinary refusal.

A body part is refused.  An avatar is never without a shape, a skin,
hair or eyes -- there is nothing to fall back to -- so the way out of
one is `wear` on another, which replaces it.  A viewer draws the same
line without saying so: its menu offers Take Off for clothing and
leaves it out for a body part.

## Examples

    detach hat
    detach badge
    detach --wait 30 Objects/hat

See also: `wear`, `worn`.
