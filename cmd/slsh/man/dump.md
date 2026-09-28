`dump` writes an object out as JSON.  It is the reading half of three
verbs that share one file format: this says what is there, `rez` builds
what a file describes, and `reform` makes what is there match a file.
With `--item` it describes an item in inventory instead, permissions
and all.

    dump lantern

The object is named by the word the region calls it or by its key, and
the whole linkset is described, root first -- so naming any prim of an
object describes all of it.

## Options

**-o, --out** *FILE*

Write here, rather than to the terminal.  Without it the JSON goes to
the terminal, which is what makes this usable in a pipe; with it, the
file is written and one line says how many prims went into it, or
which item.

**-i, --item**

Describe an item in inventory instead, named by its path or its id,
as every inventory command takes one.  See below.

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

## An item in inventory

`--item` answers the question an object in the region answers with its
masks, for something still in inventory: may I give this away, and what
may whoever I give it to do with it.

    dump --item /Objects/lantern
    {
      "name": "lantern",
      "type": "object",
      "uuid": "40f17e57-...",
      "creator": "...",
      "owner": "...",
      "perms": {
        "base": 581632,
        "owner": 581632,
        "group": 0,
        "everyone": 0,
        "next": 532480
      }
    }

The five masks have the names an object's have, and are the grid's
numbers: 8192 is transfer, 16384 modify, 32768 copy and 524288 move,
added together.  The owner's with 8192 in it may give the item away;
the next owner's is what somebody given it may do, 532480 there being
transfer and move and neither copy nor modify.  `ls -l` shows the
owner's three that matter as letters.

A link is followed to the item it points at, whose masks are the ones
that decide anything.  A folder has no permissions and is refused.

## Examples

Look at a thing:

    dump lantern

Ask whether an item in inventory can be given away:

    dump --item /Objects/lantern

Write it down, edit the file, and put the edit back:

    dump --out lantern.json lantern
    reform lantern lantern.json

See also: `rez`, `reform`, `texture`, `objects` for finding the name
to give it, `perms` for changing an object's masks, and `ls -l` for an
item's at a glance.
