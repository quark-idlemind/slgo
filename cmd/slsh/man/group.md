group says which group this avatar is acting as, lists the ones it has
joined, and with an argument makes one of them active.  It is not about
joining or leaving: an invitation into a group arrives in `waiting` and
is answered there.

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

## Examples

    group
    group Example Builders
    group none

See also: `place` and `rez` for the commands land refuses when this is
wrong, `waiting` and `answer` for an invitation into a group, and
`profile` for the groups somebody else has chosen to publish, which is
a different list from this one.
