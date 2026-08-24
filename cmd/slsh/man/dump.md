`dump` writes an object out as JSON.  It is the reading half of three
verbs that share one file format: this says what is there, `rez` builds
what a file describes, and `reform` makes what is there match a file.

    dump lantern

The object is named by the word the region calls it or by its key, and
the whole linkset is described, root first -- so naming any prim of an
object describes all of it.

## Options

**-o, --out** *FILE*

Write here, rather than to the terminal.  Without it the JSON goes to
the terminal, which is what makes this usable in a pipe; with it, the
file is written and one line says how many prims went into it.

**-w, --wait** *SECONDS*

How long to let the region describe itself.  Without it, thirty.

## What is in the file, and what is not

Positions are region coordinates.  A child prim's update describes it
as an offset from its root; the file writes both the root and the
children in the region, because that is what somebody reading the file
expects and what building from it takes.

A prim's description, its creator and its permission masks are asked
for one prim at a time, which is most of what the waiting is for.  A
prim that will not answer is still described, with whatever arrived on
its update.

Textures are left out.  `texture` with no flags reads them instead.

Every prim is written with a type, and where the form is not known the
word written is `box`: a prim nothing has described comes out as a
box, and so do meshes and sculpts, whose form this shell cannot name
at all.  Everything else about such a prim is recorded truthfully, but
`rez` and `reform` build the type they are given, so a file holding a
mesh rebuilds it as a box.

## Examples

Look at a thing:

    dump lantern

Write it down, edit the file, and put the edit back:

    dump --out lantern.json lantern
    reform lantern lantern.json

See also: `rez`, `reform`, `texture`, and `objects` for finding the
name to give it.
