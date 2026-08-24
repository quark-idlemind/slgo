dump writes an object out as JSON.  It is the reading half of three
verbs that share one file format: this says what is there, "rez" builds
what a file describes, and "reform" makes what is there match a file.

The object is named by the word the region calls it or by its key, and
the whole linkset is described, root first -- so naming any prim of an
object describes all of it.  Without --out the JSON goes to the
terminal, which is what makes this usable in a pipe; with it, the file
is written and one line says how many prims went into it.

## Why JSON, and whose JSON it is

The format is the eLSL simulator's, not one invented here.  That
simulator runs an object with no grid at all, and these commands build
the same object in Second Life, so one file describes a thing to both
programs: a probe written against one is a probe against the other, and
the difference between what the simulator does and what the grid does
becomes a diff of two files rather than an argument.

The field names are therefore a wire format shared with another
program, and a few extra ones carry what the grid knows and the
simulator has no use for: the region-local id most messages take, the
floating text, the permission masks, who made it, and the linkset's
name, which in Second Life belongs to the root prim and is worth having
at the top.

## What is left out, and why

Positions are written in region coordinates, although a child prim's
update describes it as an offset from its root.  The root's frame is
composed back out here, because region coordinates are what somebody
reading the file expects and what building from it takes.

A prim's description, its creator and its permission masks are on no
update at all and have to be asked for one prim at a time, which is
most of what the waiting is for.  A prim that will not answer is still
described, with whatever arrived on its update.

Textures are deliberately absent.  A prim's faces are a packed blob the
simulator has no notion of, and inventing a representation for it here
would be inventing it for both programs; "texture" with no flags reads
them instead.

The shape is not left out that way, and it is worth knowing which.
Every prim is written with a type, and where the form is not known the
word written is "box": a prim nothing has described comes out as a box,
and so do meshes and sculpts, whose form this shell cannot name at all.
Everything else about such a prim is recorded truthfully, but rez and
reform build the type they are given, so a file holding a mesh rebuilds
it as a box.

## Examples

Look at a thing:

    dump lantern

Write it down, edit the file, and put the edit back:

    dump --out lantern.json lantern
    reform lantern lantern.json

See also: rez, reform, texture, and objects for finding the name to
give it.
