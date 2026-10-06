`cat` prints a notecard or a script.

    cat /Scripts/greeter > greeter.lsl

The argument is an inventory path, relative to the folder the shell
is in, or the uuid of an item.  Redirection is the other half of it:
that line is how a script gets onto the disk.  `save` writes the
text back.

The name is matched exactly, in the case it has, and has to mean one
thing.  A path that names two items is refused, listing their ids,
although reading is harmless: with the output going to a file there
may be nobody to notice that it was the other one.

With `--in` it reads one from inside a rezzed object instead:

    cat --in lantern greeter

The object is named by the word the region calls it or by its uuid, and
the item by its name, exactly and in the case it has, or by the id
`ls -l --in` prints for it.  The name is one argument, so one with a
space in it is quoted.  What is read is the object's own copy, which
need not be what is in inventory under the same name: a script edited
in the object, or a notecard saved there, has parted company with the
item it was dropped in from, and the copy is the one the object runs
or reads.  The region decides whether it may be read, and a script
this avatar may not modify is refused there.

A notecard that may not be copied cannot be read at all, in inventory
or in an object: the region refuses it, and a viewer does not even
ask, saying "You do not have permission to view this notecard".
`cat` says which permission it is.

A notecard and a script, and nothing else.  A texture, a sound, an
animation and the rest are not text; `get` is the command for a
texture.

What is printed is the text.  A notecard's wrapper is not shown, and
neither is the NUL that ends a script's asset: a viewer does not show
it either.

The text is exact where it is data.  To a file or a pipe -- `cat x >
file`, or `slsh -c 'cat x' > file` -- its bytes come out as they are, a
DEL, a tab and a CRLF included, with nothing trimmed and nothing added.
On a terminal control characters are shown in caret form (`^?` for DEL,
`^M` for CR) so that nobody else's escape sequence can act on it, and a
line after the text says so.  `-o` writes the exact text to a file
whatever the terminal is.

That is not a nicety.  A script held `string SEP = "<DEL>";` and was
copied out with `cat > file`; the file had the two characters `^?`
there, and a last line `^@`, and the script, saved back, split on the
wrong thing and silently did nothing.

## Options

**--in** *OBJECT*

Read it from inside a rezzed object, not from inventory.

**--out**, **-o** *FILE*

Write the exact text to FILE and print `FILE: N bytes`, as `get -o` and
`dump -o` do.  A script is the asset's bytes without the terminating
NUL, and `save FILE PATH` puts the same bytes back.

## Examples

    cat Notecards/README
    cat /Scripts/greeter > greeter.lsl
    cat -o greeter.lsl /Scripts/greeter
    cat --in lantern greeter
    cat --in 88fa7e57-... "read me"

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
does not exist yet, `get` for a texture, `ls` for what is there
to read, and `fetch` for copying one out of an object.
