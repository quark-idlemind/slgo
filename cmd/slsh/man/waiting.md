`waiting` lists everything that wants an answer, numbered.

    waiting

Seven kinds arrive that way and they share one list: a teleport, a
script asking permission, a blue menu, a text box, an item being
handed over, an offer of friendship, an invitation into a group.  A
viewer draws these on the screen until somebody deals with them.  A
shell has no screen to leave them on, so they are gathered here, the
prompt says how many there are, and nothing is answered by being
looked at.

Each line is the number, the kind -- dialog, text box, teleport,
inventory, friendship, permission, group -- then who is asking and
what for.  The line under it says what may be typed: the buttons a
dialog offers, the two shapes a text box takes, the fee an invitation
wants, the note that taking a teleport waits for the arrival, or the
plain `answer`, `no`, `ignore`.

A group invitation may cost money, so its fee has to be typed back.  A
teleport offer is followed rather than answered and forgotten:
accepting one moves the avatar and waits for it to arrive, and
everything this shell knew about the region left behind is dropped on
the way.

## Options

**-a, --all**

Include the things `ignore` has set aside.  Without it they are not
listed and not counted at the prompt, which is what `ignore` is for.
If everything waiting has been ignored, the listing says so and points
at `-a`.

## The number belongs to the thing

A number is handed out when a thing is first seen and kept until it
goes, so a number on the screen still means the same thing after
something else has been answered.  Numbering by position looks
identical until the moment it matters -- answer the first of two and
the second becomes 1, and the `answer 2` already decided on is now
either an error or, worse, something else.  The list can therefore
look sparse: the thing that was 2 is still 2.  When nothing is left
waiting the numbers start again at one.

Every kind leaves the listing as it is answered, whichever command
answered it, so the count at the prompt falls by one.

## What the listing is a record of

Everything this shell has seen arrive, and -- through slgod -- the
offers that arrived before it attached.

slgod keeps six of the seven kinds whether or not any client is
attached: teleports, requests for a teleport, items (an object's give
too), friendship, group invitations, and a script's dialogs and text
boxes.  A shell is handed whatever of those is still
waiting when it attaches, and they are listed with the rest, marked
`(from before this shell)` because nobody saw them arrive.  Starting a
shell says how many there are.

Permission requests are not kept.  One raised before this shell
attached is not known here; it is waiting in the world, and a viewer
would show it.

Nor are they listed for ever.  A dialog or a permission request nobody
has answered leaves the listing after an hour, and no more than 32 of
each are listed: when another arrives, the oldest goes.  slgod's record
of dialogs follows the same two numbers.  A line says so when one goes:

    12:03:04 * the dialog from [Object] Example Box was forgotten after 1h unanswered by this session -- it is no longer waiting

That answers nothing.  The script that asked for permission is still
waiting for an answer, and a dialog expires where it was raised.

So an empty listing says what it is an account of, rather than
`nothing waiting` alone, which reads as an answer:

    nothing waiting -- slgod has kept every offer, teleport and invitation
    made to this avatar since 09:14 and holds none unanswered; a script's
    permission request from before this shell attached is not kept

slgod's record starts when it logged the avatar in, and keeps the
hundred most recent; when it has had to drop older ones, the line says
how many.  Against a slgod too old to keep a record it says anything
offered before the shell attached is not known here, and a shell that
logged in with `--direct` has seen everything since its own login.

## Another shell, or slbotd, on the same avatar

Every client attached to the avatar is handed the same offers, and the
first to answer one is the only one that does.  When another client
answers something this shell is listing, it leaves the listing at once
and a line says so:

    12:03:04 * the teleport Example Resident offered was accepted by slbotd -- it is no longer waiting

Answering one that another client answered a moment before is refused
rather than sent twice, and says who answered it and how.

## Examples

    waiting
    waiting -a

See also: `answer`, `no`, `ignore`, `offers` for the older way to the
friendship and inventory offers, which appear here too, and `tp` for
going somewhere without waiting to be invited.
