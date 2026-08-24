`rez` builds the object a JSON file describes: the prims are made and
linked, named and described, and the scripts named in the file are
installed in them.  `dump` is the other direction, and the file each of
them reads is the same file.

    rez lantern.json

The argument is the path of a file describing exactly one object.  A
file with several in it is refused rather than built one after another,
since what should stand where would then be the file's business and not
the command's.  What is built is left standing: an object being built
is usually about to be looked at, so nothing takes it into inventory.

## Options

**--at** *X,Y,Z*

Where the root lands; every other prim keeps the vector from the root
that the file gave it.  Without it, the positions the file records.

Three numbers with commas between them and no spaces.  Two are refused
with a message naming all three.  `place`'s `--at` is the other way
round: it takes two or three, and fills in a zero for the Z it was not
given, so a pair of numbers that `place` accepts without a word is a
usage error here.

**-k, --keep**

Leave what was built standing even if a script will not compile.
Without it, a failure removes what it made and says it has: half a
build is prims standing in the region that nobody asked for.  This is
for the case where the wreckage is the interesting part.

## This is not what everyone means by rezzing

`Rez` is the word everybody uses for putting an inventory object into
the world, and that is `place`.  One word cannot mean both "make what
this file describes" and "put back what I took": the first invents an
object and the second restores one, and somebody who mixed them up
would be told their object was not valid JSON.

## Where it lands

The file says where each prim goes, in region coordinates.  A file
written for another region names coordinates that land somewhere else
here, so `--at` moves the whole thing without the file having to be
edited: the root lands on the point given, and every other prim keeps
the vector from the root that the file gave it.

## What is made, and what is not

Only the scripts in a description are made.  The rest of what a file
may name is an asset this avatar may not have, and a texture named by
key is not something a client can conjure into an object it does not
already own a copy of.  A notecard is the one to watch, because it is
written in the same list as a script and reads as though it would be
made too: a file describing a notecard with a body in it builds a prim
whose contents are empty, and says nothing about the notecard it did
not write.

## Examples

Build what a file says, where it says:

    rez lantern.json

Build one here rather than at the position the file records:

    rez --at 128,128,25 probe.json

See also: `dump`, `reform`, `place` for putting an inventory object
into the world, `link` and `unlink`, and `take` for bringing what was
built in.
