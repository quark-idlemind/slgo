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
`cat` and `ls` say "nothing called that here" instead, so which
sentence appears says which command was typed rather than which kind
of thing the name turned out to be.

A uuid does not work here.  `cd` walks names; a uuid in a path is
read as a folder called that, which nothing is.  `ls -l` prints the
id beside the path for the commands that do take one.

## Case, and names that are not unique

The comparison ignores case, as every path through inventory does.
The grid keeps whatever case a name was given, but two names that
differ only in case are the same name at this prompt.  Inside an
object it is the other way about: `rm --in` and `mv --in` want the
name exactly as it is spelled there.

Where two folders in one place share a name, a path means the first
of them and there is no way to say the other.  Moving or renaming
one is the way out.

A name may contain a slash.  A backslash in front of it says so:
`cd Notecards/2026\/07` enters a folder called `2026/07`.

## Examples

    cd Objects/lanterns
    cd ..
    cd

See also: `pwd`, `ls`, `mkdir`, and `find` for looking without going.
