`lure` offers somebody a teleport to where this avatar is standing.  It
is what a viewer's menu calls Offer Teleport; the grid's own word for
it is a lure, which is what `waiting` already says when one arrives
here.

    lure Example Resident
    lure Example Resident come and look at the dock

Who is named by a name, by a key, or by the number beside a name in the
last listing of people, exactly as `im` and `offer` name somebody.
Anything after the name is the note that goes with it.

## What the other side sees

The destination is not a field of the message.  What arrives is the
note, and a viewer shows that as the whole of the offer -- so an offer
with no note arrives with nothing to say where it goes.  For that
reason the shell appends this avatar's position to the note as a map
link, which is what a viewer does with its own offers:

    come and look at the dock
    https://maps.secondlife.com/secondlife/Example%20Region/128/128/25

## Options

**-a, --ask**

Ask to be brought to *them* instead of offering to bring them here.
That sends a teleport request, which is an ordinary instant message
carrying dialog 26 rather than a message of its own, and is answered by
an offer coming back -- if they answer at all.

    lure --ask Example Resident could you bring me over

## Nothing comes back

An offer is sent and that is the end of what can be known here.  Being
accepted arrives as its own instant message and being ignored arrives
as nothing at all, so `lure` reports what was sent rather than what
happened.  Whether they came is a question for `who`.

## When somebody asks this avatar

A request arriving here is listed by `waiting` as a teleport request,
and `answer N` sends them an offer -- which is the only thing that
answers it, since a request carries no id of its own.  `no N` merely
drops it from the listing: the grid has no message for refusing one,
a viewer's own No button sends nothing, and the person who asked is
never told either way.

Before this existed such a request arrived as `* dialog 26 from
Somebody`, which named neither what it was nor what to do about it.

See also: `tp`, `waiting`, `answer`, `offer`, `im`.
