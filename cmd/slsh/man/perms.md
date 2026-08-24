perms sets what somebody other than this avatar may do with an object
standing in the region.  It is the question "take" and "place" leave
open: an object can be moved into inventory and back out again without
anybody ever having said who else may copy it.

The object is named by the word the region calls it or by its key.  At
least one of the four groups has to be named, because a command that
changes things and was told to change nothing has been mistyped rather
than asked for a report.

The flags go in front of the name.  Option parsing stops at the first
argument that is not a flag, so "perms lantern --next cm" is a line
with an object named and no group named at all, and what comes back is
the refusal above: a complaint that none of four flags was given, to
somebody who has just typed one of them.  Read that message as saying
the flag is in the wrong place.

## The letters

c is copy, m is modify, t is transfer and v is move.  "all" is the four
of them and "none" is a bare refusal of everything.  What is set is
said back in words, so a line that was typed as letters can be read
without knowing them.

Those four are all there are here.  Export and the damage bit are real
permission bits and are deliberately not among the letters.

## A set of letters replaces, and does not add

Each flag says what that group may do afterwards, in full.  A letter
left out is a letter turned off, so asking for modify alone on
something that could be copied has taken copying away.

Underneath, that is two messages rather than one.  The protocol turns
bits on and turns bits off; it has no way to say "be exactly this", so
sending only the letters asked for would leave a bit the caller meant
to clear still set.  A half with nothing in it is not sent, so "all"
and "none" are one message each: there is nothing to turn off in the
one and nothing to turn on in the other.

## Which group matters when

Owner, group and everyone are what may be done now, by this avatar's
group and by passers-by.  The next owner's set is the one that travels:
it is what the object carries when it changes hands, and it is what
somebody means by full permissions on a thing they were given.

## Nothing here confirms it

The request goes out and nothing answers it, so the line printed is
what was asked for rather than what the region did.  Where that matters
-- a parcel or an object that will not have its permissions changed
refuses in silence -- "dump" is the way to look: the masks are among
what it writes out for each prim.

## Examples

Let anybody who gets it copy and modify it, but not pass it on:

    perms --next cm lantern

Take everything back from everyone but the owner:

    perms --group none --everyone none lantern

See also: take and place, give for handing an item to somebody, and
dump for reading back what an object's masks actually are.
