Gets the avatar up, from either kind of sit, and waits for the
simulator to show that it happened before saying so.  An avatar sits on
one thing at a time and the simulator knows which, so there is nothing
here for anybody to name.

`unsit` is the same command under a second name.  `stand` is what the
world calls the button; `unsit` is what a script calls the function,
and both spellings work so that neither has to be remembered.

## It says where that left the avatar

The second line is the position, in the words `where` and `tp` use.
Standing up does not undo the journey the sit made.  An avatar seated
on a box seven metres away is left about six metres from where it had
been standing.  This line is where the avatar is now, and not where it
was before any of it began.

## What counts as having stood up

From an object sit it is the un-parenting: the avatar's own update
comes back with no seat in it.  From a ground sit there is nothing to
unsit from, so what says the avatar is up is the ground animation
stopping -- and this shell hears animations only while one of these
commands is running, having asked for them and given them back.

There is a gap in that, and it is worth knowing rather than being
surprised by.  A ground sit this shell's session performed is known
about.  One performed by a viewer driving the same avatar, or by
another client while this session was not listening, is not, and reads
here as standing.  A `stand` then still sends the request -- the avatar
does get up -- and returns at once, having had nothing to wait for.

## It may take a moment

A stand asked for while the avatar is still settling into the last
thing it was told to do can be swallowed.  The command keeps asking
until the avatar is up, or until fifteen seconds have passed.

## Options

**-w, --wait** *SECONDS*

How long to keep asking until the avatar is up.  Without it, fifteen
seconds.

## Examples

    stand
    unsit
    stand --wait 30

See also: `sit` for going the other way, `where` for the position this
prints, and `tp` for moving the avatar somewhere sitting cannot reach.
