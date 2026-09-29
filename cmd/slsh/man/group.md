group says which group this avatar is acting as, lists the ones it has
joined, and with an argument makes one of them active.  It is not about
joining or leaving a group: an invitation into a group arrives in
`waiting` and is answered there.  It is also how a group's chat is joined,
spoken in and left; see the last section.

    group

With nothing after it, the listing marks the active one in a column of
its own and gives each group's key beside its name.  With a name, a key
or the word `none`, that becomes the active group and the command waits
for the simulator to agree before saying so.  A name with a space in it
needs no quoting: the rest of the line is the name.

## Why this matters more than it looks

Land usually grants building to a group rather than to individuals, so
whether an avatar may rez something somewhere depends on which group it
is acting as and not on who it is.  An avatar acting as nobody is
refused by land that would otherwise allow it, and the refusal talks
about the land rather than about the group, then sends the reader to
the land tool, which has nothing to show.

It is also the group every prim `rez` makes belongs to, as a viewer's
do.  On land that runs only its group's scripts, a script in a prim of
no group was seen reported as running and never run.

A viewer remembers the active group from one session to the next and a
headless login does not: the login itself always comes up acting as
nobody, and a refusal that blames the land is the first sign of it.

When slgod brings a session up it settles the group as well.  A profile
under `~/.config/slgo` may carry

    group = Example Builders

by name or by key, and an avatar that has joined exactly one group and
said nothing about it gets that one.  So an avatar the daemon brought
up may well be acting as something already, and this is the command
that says which.

## Acting as nobody is spelled none

The way back out is the word, because the thing it stands for is a key
of thirty-two zeros and nobody would guess that from a listing that
says "acting as no group".  The word wins over a group that happens to
be called it; such a group is still reachable by its key, which the
listing prints beside every name.

## An empty list is not the same as no groups

Nothing asks for the list: it arrives on its own shortly after login,
and again whenever the membership changes.  So an empty one means "not
told yet" exactly as much as it means "belongs to none", and the
listing says both rather than picking one.  Trying again in a moment is
what settles it.

A name is matched against that list, whole and without regard to case,
and nothing else.  A key is sent as typed, even if the list has not
arrived yet.  An avatar that is not a member is what a key that never
becomes active usually means, and that is what the timeout says.  Two
groups of one name are refused with their keys rather than guessed
between.

## A group's chat

    group chat NAME
    group say NAME TEXT
    group leave-chat NAME

These are about the chat of a group this avatar has joined, which is a
different thing from being in the group.  Nothing here joins a chat on
its own: a busy group would flood the shell.  What somebody says in a
chat this shell has joined is printed as it arrives,

    12:01:40 < Example Resident in [Group] Example Builders: shall we start?

with the speaker first and the group after it, and the group's name behind
its label like any name that is not a person's.  Somebody speaking in a
group whose chat has not been joined is printed the same way once, with a
notice saying how to join it, and for as long as nothing is joined that
line is all that is heard of it.

`group chat NAME` starts the chat and waits for the grid to say it has,
up to half a minute, and says how many it lists in it.  A refusal is
printed with the grid's reason.  `group say NAME TEXT` speaks in it, and
refuses a group whose chat this shell has not joined; a name with spaces
in it needs no quoting, since the longest run of words that is a whole
group name is taken as the group and the rest is what to say.  Text over
1023 bytes is sent as several messages.  `group leave-chat NAME` leaves it;
nothing answers a leave, so it says what was sent.  A key does for a name
anywhere a name is taken.

A line that is the whole name of a group means that group, so a group
whose name begins with `chat`, `say` or `leave-chat` is still activated
by naming it; its key reaches it for the chat commands.

What is sent and what is expected is read from the viewer's source and has
not been measured on a grid: doc/group-chat.md says which is which.
Whether the grid goes on inviting an avatar that never answers, and
whether it sends a speaker's own words back to them, are two things it
does not settle.

## Examples

    group
    group Example Builders
    group none
    group chat Example Builders
    group say Example Builders good evening, all
    group leave-chat Example Builders

See also: `notice` for what a group posts, `im` for one person, `conference`
for several, `place` and `rez` for the commands land refuses when this is
wrong, `waiting` and `answer` for an invitation into a group, and
`profile` for the groups somebody else has chosen to publish, which is
a different list from this one.
