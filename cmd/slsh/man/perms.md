`perms` sets what somebody other than this avatar may do with an
object standing in the region.  It is the question `take` and `place`
leave open: an object can be moved into inventory and back out again
without anybody ever having said who else may copy it.

The object is named by the word the region calls it or by its key.  A
name with a space in it has to be quoted, and a name several objects
answer to is refused with their keys rather than guessed at.  At least
one of the four groups has to be named, because a command that changes
things and was told to change nothing has been mistyped rather than
asked for a report.

    perms --next cm lantern

The flags go in front of the name.  Option parsing stops at the first
argument that is not a flag, so `perms lantern --next cm` is a line
with an object named and no group named at all, and what comes back is
a complaint that none of four flags was given, to somebody who has
just typed one of them.  Read that message as saying the flag is in
the wrong place.

## The letters

Every one of the four flags takes the same *LETTERS*: `c` copy, `m`
modify, `t` transfer and `v` move, or `all` for the four of them, or
`none` for a bare refusal of everything.  What is set is said back in
words, so a line that was typed as letters can be read without knowing
them.  Those four are all there are here: export and the damage bit are
real permission bits and are deliberately not among them.

## Options

**--owner** *LETTERS*

What the owner may do now.

**--group** *LETTERS*

What this avatar's group may do now.

**--everyone** *LETTERS*

What passers-by may do now.

**--next** *LETTERS*

What the next owner may do.  It is the set that travels: it is what
the object carries when it changes hands, and it is what somebody
means by full permissions on a thing they were given.

**-w, --wait** *SECONDS*

How long to let the region describe itself before giving up on finding
the object.  Without it, 30 seconds.

## A set of letters replaces, and does not add

Each flag says what that group may do afterwards, in full.  A letter
left out is a letter turned off, so asking for modify alone on
something that could be copied has taken copying away.

## What is printed was read back

Nothing answers a permission change, so after each one the object's
masks are read back, for up to fifteen seconds, and the line printed
is what the mask then allows.  That need not be the letters typed.
The permission rules adjust a request rather than refuse it, and in
the viewer's copy of them nobody is given more than the base allows,
everyone is never given modify, and a next owner who may not copy may
always transfer.  So `perms --next m lantern` prints

    the next owner may now modify, transfer

A mask that never reads as those rules make the request is an error
saying what it allows instead, and no line is printed for it; the
lines printed before it are the changes that were made.

## Examples

Let anybody who gets it copy and modify it, but not pass it on:

    perms --next cm lantern

Take everything back from everyone but the owner:

    perms --group none --everyone none lantern

See also: `take` and `place`, `give` for handing an item to somebody,
and `dump` for reading back what an object's masks actually are.
