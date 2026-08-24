`detach` takes a worn object off.

    detach hat

What it names is different from what `wear` names.  `wear` names
something in inventory, because that is where the thing to be put on
is.  `detach` names something worn, and matches the word against what
is actually on the avatar -- so a name that is in inventory and not on
the avatar is a sentence saying so rather than a detach that reports
success and does nothing.

A path is allowed and only its last name is used: nothing about a worn
object says which folder its item came from.  A uuid is looked for as
either the inventory item or the worn object, since the region's answer
holds both.

## Options

**-w, --wait** *SECONDS*

How long to give the region to agree the thing is off.  Without it,
fifteen.

## What `worn` can and cannot see

The region describes an attachment when it goes on, and again at login,
and never otherwise.  An avatar dressed before this session began can
therefore be wearing something nothing here has ever heard of, and
`detach` will say it is not worn.  It is not a denial that the thing is
on; it is this session not having been told.  `worn` lists what can be
seen.

A name that is worn twice is refused with the points
printed.  `worn -l` gives the item ids, which are what tells the two
apart.

## It waits

Nothing replies to a detach.  The request goes out, and what says it
worked is the object no longer being among what is worn -- which
arrives some time later.  `detach` polls until the region agrees before
it says the thing is off, and a wait that runs out is reported as not
knowing rather than as failing.

## Examples

    detach hat
    detach badge
    detach --wait 30 Objects/hat

See also: `wear`, `worn`.
