`dress` puts on everything the Current Outfit folder names that the
avatar is not wearing.

    dress

## What it is for

The simulator puts most of an avatar's attachments back by itself when
it logs in, but not all of them.  Measured on Agni over eight logins of
two avatars: wherever a point held several attachments, exactly one of
them came back -- one of three on a chest, which one varying from login
to login, and one HUD on each of eight points where twelve were worn.
Every point with a single attachment was restored every time.  It looks
as though the simulator restores one attachment per point, as it did
when a point could hold only one; that is inferred from the pattern.

A viewer covers the gap.  Once the Current Outfit folder has loaded,
it puts on whatever the folder names and is not on, and nobody sees it
happen.  Nothing here did, so an avatar could come back from a login
missing a head of hair or a dress and stay that way.  `worn` shows the
difference, and this is what closes it.

Only attachments.  Clothing and body parts are not attached and do not
go missing at a login: the folder is what the baking service reads, and
it has been read by the time the avatar is standing up.

## It adds rather than replaces

A point holds more than one attachment and an ordinary outfit uses
that: a mesh body, a dress and a pair of arms all sit on the chest.
So this adds, which is what a viewer does down the same path.

It was written the other way round first, because replacing is
self-limiting -- this cannot be sure what is on already, since what
says an attachment is worn is the region's description of it and that
description can be missing.  That is right about the risk and wrong
about the cost: restoring with replace puts the chest items on one
after another and each knocks the last one off.  Seen, on an avatar
restored that way: she came back in her boots and her hair and nothing
else.

A duplicate is visible and costs a `detach`.  A garment silently lost
is neither.

So the duplicate is reported rather than prevented.  Anything the
region describes twice is named at the end:

    worn more than once, which detach undoes: a hat

Two attachments from one item agree in every field a person could name
one by, so nothing else is going to mention it.

## It waits for what the simulator says is on

After a login the simulator is putting the outfit back itself, and the
region describes each piece as it arrives.  For a while, then,
something can be on and not yet described -- and asking for it again
would be asking for a thing that is already there.

The simulator's own list of what is attached settles it.  It names
every attachment on the body by its object, and a viewer draws an
avatar as a cloud until what it has been shown matches that list.
While the list names more than the region has described, this waits,
for up to `--wait` seconds, before deciding anything is missing.

The list has two limits.  It never includes HUDs, not even in the copy
an avatar is sent about itself.  And an avatar is sent its own list
when it is baked, not when it logs in -- measured, none arrived in the
twenty seconds after a login until a bake was asked for.  So when there
is no list baked from the outfit as it is now, this asks for a bake
first, as a viewer does once the outfit folder has loaded, and gives
the answer a few seconds to arrive.  `wear` and `detach` ask for a bake
afterwards too, which is what keeps it current.

## The report has three parts

    dress
    put on a mesh body, a dress, a hat
    asked for and not described: a HUD

What was **already on** is counted rather than named, so that running
this twice does not read as having done nothing.

What **went on** is the answer.

What was **asked for and not described** is neither a success nor a
failure.  The confirmation is the region describing the new object, and
that description is the thing most likely to be lost, so a name here
means "asked for, and nothing came back about it" -- which is weaker
than "not worn".  `worn` a minute later is the way to settle it.

When the simulator lists attachments that the region has still not
described once the waiting is over, that is said too:

    the simulator lists 1 attachment that nothing here has described

That is the case in which "asked for and not described" may well be
on.

## Options

**--wait** *SECONDS*

How long to give the region to describe what it was asked to rez,
twenty seconds by default.  The wait is one wait for the whole outfit
rather than one per item: the requests go out together and the
confirmations arrive together, which is what a viewer does too.

The same limit bounds the waiting beforehand, for what the simulator
says is on already, so a run can take up to twice this.

## Examples

    dress
    dress --wait 40

See also: `worn` for what is on and what the outfit says should be,
`wear` and `detach` for one thing at a time, and `ls -L "/Current
Outfit"` to read the folder itself.
