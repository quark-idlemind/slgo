`mkdir` makes one folder.  `rm` is what takes one away, and takes
everything inside it at the same time.

    mkdir lanterns

The argument is a path.  The last name in it is the folder that gets
made, and everything before it has to be there already: nothing here
makes a chain of folders, so a path through a folder that does not
exist is refused rather than filled in.  A path with no slashes in
it makes the folder here, in the folder the shell is in.

What it prints is the new folder's id.

## Nothing stops two folders sharing a name

Asking twice makes two folders with one name.  A path then means
the first of them, and the second can only be named by its id, which
is what `ls -l` prints ids for.  Where that has happened by
accident, `mv` renames one of them and the ambiguity is gone.

## What a name may contain

Nearly any printable character.  A slash inside a name is written
with a backslash in front of it, and a backslash is doubled.  The
grid trims a leading or trailing space, so a name cannot begin or
end with one however carefully it is quoted.

## Examples

    mkdir lanterns
    mkdir /Objects/lanterns/brass

See also: `cd`, `ls`, `mv`, and `rm`.
