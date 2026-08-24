`pwd` prints the inventory folder this shell is in, as a path from the
root.

    pwd
    /Objects/lanterns

`cd` is what changes it.  Every command that takes a path without a
leading slash takes it relative to this.  The prompt already carries
the same path, so this is chiefly for a script or a line redirected
into a file.

It takes no argument.  A word given anyway is ignored rather than
refused.

## The root is a slash

Inventory has one root and no home.  A shell that has not been told
otherwise starts at `/`.

## The folder belongs to this shell

Nothing on the grid records where a shell was looking.  A second
shell on the same avatar starts at the root however deep the first
has gone, and quitting forgets it.

## A path is meant to be read back

An inventory name may hold nearly any printable character, including
a slash.  A slash in a name is printed with a backslash in front of
it, and a backslash is doubled: the path that comes out is the path
that goes back in.  The grid trims a leading or trailing space, so a
name cannot begin or end with one.

## Examples

    pwd

See also: `cd`, `ls`, and `find` for looking below here rather than
at where here is.
