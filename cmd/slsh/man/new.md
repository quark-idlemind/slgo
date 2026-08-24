`new` makes a notecard or a script that did not exist.  `save` writes
into one that already does.

    new Notecards/README

The argument is where it lands: a path relative to the folder the
shell is in, whose last name is the item and whose folders must
already be there.  What is printed is the name and the new item's
id.

Both kinds cost nothing.  Uploading a texture costs L$; this does
not.

A script made with no source is given a skeleton -- an empty default
state -- because a script with none at all is a compile error the
moment anything runs it.  A script that did not compile says so and
is in inventory all the same.

## Options

**--kind** *KIND*

`notecard` or `script`.  Without it, a notecard.  With `--in` and
no `--kind`, a script, because a notecard cannot be made inside an
object.

**--from** *FILE*

Its contents.  Without this a notecard is empty and a script is
given a skeleton.

**--in** *OBJECT*

Make the script inside a rezzed object, where it is compiled and
started.  Takes a name and not a path, because an object holds no
folders.

**-w, --wait** *SECONDS*

How long to let the region describe the object `--in` names.
Without it, thirty seconds.

## Inside a rezzed object

Dropping a script into an object does not compile it: the copy sits
there uncompiled, and `start` cannot start what was never compiled.

Reusing a name replaces that script rather than adding another.  An
object otherwise keeps every copy it is given and renames the
newcomer, so a command that added would leave two scripts of nearly
one name both running.

A notecard cannot be made inside an object at all.  Asking for one
with `--in` is refused rather than quietly made in inventory
instead.  A notecard is made with a plain `new` and put in with
`drop`.

## Examples

    new Notecards/README
    new --from notes.txt Notecards/README
    new --kind script --from hello.lsl /Scripts/greeter
    new --in lantern --from hello.lsl greeter

See also: `save`, `cat`, `drop`, `start`, `stop`, and `put` for an
image.
