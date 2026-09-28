`mkdir` makes one folder.  `rm` is what takes one away, and takes
everything inside it at the same time.

    mkdir lanterns

The argument is a path.  The last name in it is the folder that gets
made, and everything before it has to be there already: nothing here
makes a chain of folders, so a path through a folder that does not
exist is refused rather than filled in.  A path with no slashes in
it makes the folder here, in the folder the shell is in.

What it prints is the new folder's id.  A `mkdir` interrupted while
it waits for the folder to show, which was made all the same, says so
with the error and gives the id there.

## Nothing stops two folders sharing a name

Asking twice makes two folders with one name.  A path through that
name then means neither of them and is refused, listing both ids;
`ls -l` prints them too.  Where that has happened by accident, `mv`
given one of the ids renames it, and the ambiguity is gone.

## What a name may contain

Nearly any printable character.  A slash inside a name is written
with a backslash in front of it, and a backslash is doubled.  The
grid trims a leading or trailing space, so a name cannot begin or
end with one however carefully it is quoted.

## Examples

    mkdir lanterns
    mkdir /Objects/lanterns/brass

See also: `cd`, `ls`, `mv`, and `rm`.
