`cd` changes which inventory folder the shell is in.  Every command
that takes a path takes it relative to that folder unless the path
begins with a slash, and `pwd` says which folder it is.

    cd Objects/lanterns

The argument is a folder path: a leading slash starts from the root,
`..` goes up a level, `.` stays, and the names between the slashes
are walked one at a time.  With no argument it goes to the root.
The empty path would otherwise mean wherever the shell already is,
so a bare `cd` would do nothing.

## Only a folder, and only by name

An item is not somewhere to be.  A path naming one is refused as "no
folder", word for word what a name that is nothing at all gets: `cd`
asks for a folder of that name and there is not one either way.
`cat` and `ls` say "nothing called" instead, so which sentence
appears says which command was typed rather than which kind of thing
the name turned out to be.

A uuid does not work here.  `cd` walks names; a uuid in a path is
read as a folder called that, which nothing is.  `ls -l` prints the
id beside the path for the commands that do take one.

## Case, and names that are not unique

A name is matched exactly, in the case it has, as every path through
inventory is.  The grid keeps `Notecards` and `notecards` as two
names, and so does this prompt.  A name spelt a way nothing is spelt
is refused, and the refusal names what differs from it only in case:

    cd notecards
    slsh: cd: no folder "notecards" here; did you mean "Notecards"?

Where two folders in one place share a name exactly, a path through it
means neither.  It is refused, and the refusal lists the two ids; `ls`
of the name lists the two folders themselves.  `cd` takes no id, so
renaming one of them with `mv`, which does, is the way out.

A name may contain a slash.  A backslash in front of it says so:
`cd Notecards/2026\/07` enters a folder called `2026/07`.

## Examples

    cd Objects/lanterns
    cd ..
    cd

See also: `pwd`, `ls`, `mkdir`, and `find` for looking without going.
