reform changes an object that is already standing so that it matches a
JSON file.  It is the third of the three verbs sharing one format:
"dump" says what is there, "rez" builds what a file describes, and this
one edits.

It takes the object -- named by the word the region calls it, or by its
key -- and then the file.  What the file omits is left exactly as it
was, which is what makes a two-line file a useful edit rather than a
demolition.

## It edits, and never builds or removes

Prims are matched by position in the list: the first prim described is
the root, and the rest follow in the order dump writes them, which is
the order of the region's own ids for them rather than the link numbers
a script would count in.  The two orders need not agree, so a file that
came out of dump lines up prim for prim, and one written by hand
against link numbers may not.  A file with fewer prims than the object
leaves the rest of them alone.  A file with more is refused, because
the extra prims would have to be made and linked, and that is what
"rez" is for.  Nothing here removes a prim either.

Prims are the only thing it will not build.  Every prim the file
describes has its inventory put in as rez puts it in, so a file
carrying script source installs that script, compiles it and sets it
running unless the file says the script is disabled -- and a script
that will not compile fails the command.  A two-line file is a small
edit; a file with a script in it does inside an existing prim
everything rez would have done inside a new one.

The name is not "edit", although that is the obvious word, because a
shell with an "edit" that does not open an editor is a shell that will
be asked why it did not.

## What "left alone" cannot quite mean

Two things go over the wire whole, so for those the file's silence is
filled in from what the prim is now rather than from nothing.

Position, rotation and scale travel in one message.  A file that gives
only a position is honoured by reading the prim's current rotation and
scale and sending all three.

The shape is all or nothing: there is no message for "change the hollow
and leave the rest of the shape", so a file that says anything about
the shape has the rest of it taken from what the prim is at that
moment.  A file that says nothing about the shape does not touch it.

Positions in the file are region coordinates, as dump writes them, and
a child is moved in its root's frame -- so the root's position and
rotation are undone on the way in.  The linkset is read once, before
anything is sent, and that reading is what the undoing uses: the root
as it stood before the edit, not as the file is about to leave it.

So a file that moves the root and names a child absolutely does not
mean what it says.  Moving the root carries its children with it, and
the child named absolutely is then placed against where the root used
to be, which lands it displaced by exactly the root's movement.  An
absolute position for a child is only what it looks like where the root
stays where it is.  Where both have to change, move the root in one run
and dump again before writing anything about the children.

## Examples

Write an object down, change the file, and put the change back:

    dump --out sign.json sign
    reform sign sign.json

A file naming one prim and one field is a one-field edit:

    reform lantern brighter.json

See also: dump, rez, texture for the faces the format leaves out, and
move for shifting a whole object without a file at all.
