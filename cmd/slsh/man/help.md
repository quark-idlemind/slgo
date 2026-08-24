`help` is the index.  Commands are gathered into groups: a name,
what the group is for, and how many commands are in it.  Naming one
lists its commands with the line that says how each is typed, and
`help all` is every command at once.

    help

The groups are the questions somebody has -- how do I look at
inventory, how do I talk to somebody, which avatar am I driving.

A command may be in more than one group.  Copying an item is an
inventory operation, and it is also how an avatar that may not rez
gets another object, so it is listed under both.  Looking in either
place finds it.

## It never describes a single command

That is the command's own business, and there are two ways to ask
it: the command itself says what it takes, and `man` says what it is
for and the traps.  Asking `help` about a command is
answered by pointing at those two rather than by refusing, and a
word that is neither a group nor a command is an error, since it is
a typo.

A name can be both a group and a command without ambiguity: here it
always means the group.  `waiting` is both, and `help waiting` is
the group.

## Two names for one command are listed once

Some commands answer to two names -- `quit` and `exit`, `.` and
`source`, `stand` and `unsit` -- and they are not two entries but
one entry reached twice.  `help all` walks the commands rather than
the names, so each of those appears once, under whichever of its
names sorts first: `exit`, and `.`, and `stand`.  The other name is
neither missing nor discouraged.

A group, on the other hand, lists whichever name its own line names,
so `help shell` says `quit` where `help all` says `exit`.  Anyone
searching a listing for a name they know is there wants `man`'s: it
is the one that lists every name rather than every command.

## Examples

    help
    help region
    help all > commands

See also: `man`, and the command itself, which answers what it
takes.
