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

See also: `save` for writing one back, `new` for making one that
does not exist yet, `get` for a texture, and `ls` for what is there
to read.
