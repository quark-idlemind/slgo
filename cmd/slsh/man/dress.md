`dress` puts on everything the Current Outfit folder names that the
avatar is not wearing.

    dress

## What it is for

An avatar logs in wearing its body parts and nothing else.  The
simulator rezzes no attachments of its own accord: they are named in
the Current Outfit folder, that folder is the client's own record, and
putting on what it names is the client's job.  A viewer does it a
second or two after arriving and nobody sees it happen.

Nothing here did it at all, so an avatar dressed from this shell came
back undressed at the next login and stayed that way.  `worn` showed
the difference -- a column of `(in the outfit, not described)` -- and
this is what closes it.

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

## Options

**--wait** *SECONDS*

How long to give the region to describe what it was asked to rez,
twenty seconds by default.  The wait is one wait for the whole outfit
rather than one per item: the requests go out together and the
confirmations arrive together, which is what a viewer does too.

## Examples

    dress
    dress --wait 40

See also: `worn` for what is on and what the outfit says should be,
`wear` and `detach` for one thing at a time, and `ls -L "/Current
Outfit"` to read the folder itself.
