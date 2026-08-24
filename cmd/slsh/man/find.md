`find` looks for names containing a piece of text, from a folder
downwards, and prints the full path of everything that matches.

    find lantern

The first argument is the text and the second is where to start,
which defaults to the folder the shell is in.  The paths printed are
absolute, and they are paths other commands take.

## A substring, not a pattern

The text matches anywhere in a name and case is ignored.  There is
no wildcard.  A star at this prompt means what `rm` means by one,
not anything to do with searching.  `find lantern` finds "brass
lantern" and "Lantern notes" alike, and there is no way to say
"names beginning with".

## What is searched, and how deep

Names, and only names.  Nothing here looks inside a notecard or a
script, and nothing looks at descriptions.

Folders match as readily as items.  A folder is an entry with a
name like any other.

The descent goes four levels below the folder it starts in.  Deeper
than that is not searched and is not reported as unsearched, so a
result of nothing from the root is not proof that nothing is there.
Starting further down is the way to reach it.

## Examples

    find lantern
    find greeter /Scripts

See also: `ls`, `cd`, and `cat` for reading what a search turned up.
