mkdir makes one folder.  rm is what takes one away, and takes
everything inside it at the same time.

The argument is a path.  The last name in it is the folder that gets
made and everything before it has to be there already: nothing here
makes a chain of folders, so a path through a folder that does not
exist is refused rather than filled in.  A path with no slashes in it
makes the folder here, in the folder cd last set.

What it prints is the new folder's id, and that is worth keeping when
a script is about to put things in it.

## Nothing stops two folders sharing a name

There is no check for a folder of that name first, here or on the
grid, so asking twice makes two folders with one name.  A path then
means the first of them and the second can only be named by its id,
which is what "ls -l" prints ids for.  Where that has happened by
accident, mv renames one of them and the ambiguity is gone.

## What a name may contain

Very nearly any printable character, which was measured rather than
assumed: an item was made for each character from space to tilde and
every one came back byte for byte.  Two of them need care in a path,
because a path is made of them -- a slash inside a name is written
with a backslash in front of it and a backslash is doubled.  The grid
trims a leading or trailing space, so a name cannot begin or end with
one however carefully it is quoted.

## Examples

    mkdir lanterns

    mkdir /Objects/lanterns/brass
    42d17e57-...

See also: cd, ls, mv, and rm.
