`cat` prints a notecard or a script.

    cat /Scripts/greeter > greeter.lsl

The argument is an inventory path, relative to the folder the shell
is in, or the uuid of an item.  Redirection is the other half of it:
that line is how a script gets onto the disk.  `save` writes the
text back.

A notecard and a script, and nothing else.  A texture, a sound, an
animation and the rest are not text; `get` is the command for a
texture.

What is printed is the text.  A notecard's wrapper is not shown.

## Examples

    cat Notecards/README
    cat /Scripts/greeter > greeter.lsl

## A link is followed

An outfit folder holds links rather than items.  A link carries the
same name as the thing it points at, and a listing tells the two apart
only by the word `link` in the type column of `ls -l`, so a path taken
from `My Outfits` names a link almost every time.

`cat` follows one to the item at the other end, which is what the id
it sends has to be: the grid has no object for a link's id and answers
an id it does not recognise with silence rather than with a refusal.

A link this inventory cannot follow is refused, naming the id it
looked for.  A link outlives what it pointed at, so an outfit put
together years ago may name things that have since been deleted.

See also: `save` for writing one back, `new` for making one that
does not exist yet, `get` for a texture, and `ls` for what is there
to read.
