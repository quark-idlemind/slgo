Plays an animation on this avatar, or stops one.  The animation is one
of the built-ins every viewer carries, or one in inventory.

    animate hello               the built-in called hello
    animate shuffle             the animation in inventory called shuffle
    animate --stop shuffle      stop it
    animate --list              name the built-ins
    animate be387e57-...        the animation with that asset id

Nothing answers.  What this prints is what was asked for, in the form
"asked for NAME (ASSET)", and never that the animation is playing: the
simulator sends no reply to the request, so a refusal, if there is one,
would be silent too.  Nothing here has been measured against the grid
yet: this is what the viewer sends.

## Naming one

A **NAME** is looked for in inventory first, the way `cat` and `wear`
look for a path -- in the current folder, or by a path from the root --
and among the built-ins second.  Names are matched exactly, in the case
they have, and a name that several items share is refused with all
their ids, as it is everywhere.  A link to an animation is followed.  An
item that is not an animation, a notecard called `bow` say, is not
played and does not hide a built-in of the same name.

The built-ins are the viewer's, spelled as the viewer spells them: in
lower case, with underscores.  `--list` prints all of them.

A **UUID** is an asset id, not an item id: the viewer's own play button
sends the item's asset.  To play something in inventory, give its name
or path, which finds the asset; an item's own id is not what to send.

## When a name is both

An animation in inventory and a built-in can have one name.  The
inventory one is played, and the line before the answer says so:

    animate hello
    hello is an animation in inventory, and a built-in has that name too; animate --builtin hello plays the built-in
    asked for hello (bf687e57-...)

Refusing would leave a built-in unreachable for somebody whose inventory
happens to hold an animation with its name, and the alternative to
preferring inventory is a rename.  **--builtin** plays the built-in
without looking in inventory at all.

## Options

**-s, --stop**

Stop the animation instead of starting it.  It takes the same names.

**-b, --builtin**

Take NAME as a built-in, even when inventory has an animation called it.

**-l, --list**

Print the names of the built-in animations, one to a line, and send
nothing.

## What it does not do

It does not say what the avatar is playing, and it does not wait for
anything.  It plays an animation on this avatar only.  A looping
animation goes on until it is stopped, which is what `--stop` is for.

See also: `sit` and `stand`, which play some of these animations as what
they are for, and `ls` for finding the animations in inventory.
