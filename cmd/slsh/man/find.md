`find` looks for names containing a piece of text, from a folder
downwards, and prints the full path of everything that matches.

    find lantern

The first argument is the text and the second is where to start,
which defaults to the folder the shell is in.  The paths printed are
absolute, and they are paths other commands take.

## Options

**-l**

The columns `ls -l` prints: what kind of thing it is, when it was
acquired, its id, and the path.  The same row in the same shape, so
a search and a listing can be read the same way.

    find -l "a hat"
    object  2025-01-08T14:02:55  9b457e57-...  /Objects/a hat
    link    2025-03-04T11:20:08  45557e57-...  /My Outfits/Sunday/a hat

The columns answer what bare paths cannot.  Names are not unique, so
a search that turns up four things of one name gives four identical
lines without them; and a link carries the same name as the item it
points at, so `link` in the first column is the only thing that tells
them apart.

The id is there to be copied into another command.  Every command
that takes a path takes an id instead, which is the way to name one
of several things sharing a name.

Without `-l`, one bare path per line, which is what makes a search
editable into commands that take paths.

**-L**

The same columns, with a link shown as the item it points at: its
kind, its date and its id, under the path where the link was found.
Implies `-l`.

An outfit folder holds nothing but links, every one named after the
item at the far end, so `-l` there gives a column of `link` and a
column of ids that name nothing outside this inventory.  `-L` turns
the same listing into what is actually worn:

    find -L hat "/Current Outfit"
    object     2025-04-14T17:44:09 cfb57e57-...  /Current Outfit/a hat

That is also the way to see clothing and body parts as against
attachments, which `worn` cannot show: `worn` lists objects on
attachment points, and a skin or a shape is not one.

A link that cannot be followed keeps the word `link`, which says what
happened -- this is a link and following it got nowhere.  The id
column is the id it points at either way, since the link carries that
already.

Following costs one walk of inventory for the whole listing, and only
where the listing holds a link at all.

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
    find -l lantern
    find -L hat "/Current Outfit"

See also: `ls`, `cd`, and `cat` for reading what a search turned up.
