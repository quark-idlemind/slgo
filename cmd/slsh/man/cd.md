cd changes which inventory folder the shell is in.  Every command that
takes a path takes it relative to that folder unless the path begins
with a slash, and `pwd` says which folder it is.

The argument is a folder path: a leading slash starts from the root,
`..` goes up a level, `.` stays, and the names between the slashes are
walked one at a time.  With no argument at all it goes to the root,
the way a bare cd goes home in a shell -- and that has to be said
outright, because the empty path resolves to wherever the shell
already is, so a bare cd would otherwise do nothing whatever.

## Only a folder, and only by name

An item is not somewhere to be, so a path naming one is refused, and
the refusal is "no folder" with the name in it -- word for word what a
name that is nothing at all gets.  cd asks for a folder of that name
and there is not one either way, so nothing here can tell the two cases
apart.  The other answer, "nothing called that here", belongs to the
commands that do take an item: `cat` and `ls` say it, and meeting one
sentence rather than the other says which command was typed rather than
which kind of thing the name turned out to be.

A uuid names an item to `cat`, `rm` and the rest, and does not work here.
This walks names rather than looking a path up whole, because a path
is names and the grid answers about ids, so a uuid in a path is read
as a folder called that, which nothing is.  `ls -l` prints the id
beside the path for the commands that do take one.

## Case, and names that are not unique

The comparison ignores case, as the rest of the shell does: the grid
keeps whatever case a name was given but does not make two names
differing only in case into two things worth telling apart at a
prompt.  Where two folders in one place really do share a name, a path
means the first of them and there is no way to say the other -- moving
or renaming one is the way out.

A name may contain a slash, since the grid allows nearly any printable
character.  A backslash in front of it says so: `cd Notecards/2026\/07`
enters a folder called `2026/07`.

## Examples

    cd Objects/lanterns
    cd ..
    cd

See also: `pwd`, `ls`, `mkdir`, and `find` for looking without going.
