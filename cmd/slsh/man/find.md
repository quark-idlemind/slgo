find looks for names containing a piece of text, from a folder
downwards, and prints the full path of everything that matches.  It is
the picking half of "ls -r": the same descent, with only the lines
worth reading left in.

The first argument is the text and the second is where to start, which
defaults to the folder the shell is in.  The paths printed are
absolute and are paths other commands take, so the output of a search
is the input of whatever comes next.

## It is a substring and not a pattern

The text matches anywhere in a name and case is ignored.  There is no
wildcard: the shell has no pattern matching in it, and a star at a
prompt means what rm means by one rather than anything to do with
searching.  So "find lantern" finds "brass lantern" and "Lantern
notes" alike, and there is no way to say "names beginning with".

## What is searched, and how deep

Names, and only names.  Nothing here looks inside a notecard or a
script, and nothing looks at descriptions, so an item whose name says
nothing about what it holds cannot be found by what it holds.

Folders match as readily as items, since a folder is an entry with a
name like any other.

The descent goes four levels below the folder it starts in.  Deeper
than that is not searched and is not reported as unsearched, so a
result of nothing from the root is not proof that nothing is there;
starting further down is the way to reach it.

## Examples

Everything anywhere below here with "lantern" in its name:

    find lantern

The same, from one folder rather than from here:

    find greeter /Scripts

See also: ls, cd, and cat for reading what a search turned up.
