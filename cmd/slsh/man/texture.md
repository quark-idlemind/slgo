`texture` is two commands wearing one name, and which one it is depends
on whether it was asked to change anything.  Given a flag that changes
something it changes it; given none, it says what the faces look like
now.

    texture lantern

The object is named by the word the region calls it or by its key.

## A change reads the object first

The message that changes an appearance carries the whole of it -- every
face, together -- so a change has to read what the object looks like
now and send it all back with one face altered.

The region never describes the result of a change on its own.  After a
change this session forgets the appearance it holds for that object, so
the next look asks the region again.  Two runs in a row then compose --
every face red and then face 2 green leaves five red faces and a green
one.

Several changes to one object still belong in one command line, where
they are applied to one reading and sent once.  Everything is parsed
before anything is sent, so a typo in the last flag refuses the lot
rather than leaving the object half changed.

## What the report says

Faces that look alike are printed once, under all of their numbers,
because that is how a prim usually is -- five sides of a box alike and
one different -- and six identical lines say less than one.

How many faces there are comes from the shape the region described: six
for a box, three for a cylinder, one for a sphere, and more where a cut
or a hollow has opened new surfaces.  Where the shape is not known the
answer is the eight a prim can have at most.

A prim nothing has described the appearance of is asked about first,
and reads as plain white if the region does not answer.  White is the
truth about such a prim rather than a guess: an object can be known to
be there before anything has said what it looks like.

The report reads what a change reads, so an object whose appearance was
dropped by a change is described again before it is printed.  What it
prints is what the region says now.

## Options

**-f, --face** *N*

Which face, counting as LSL does.  Without it, every face, which is
what "make this thing red" usually means.  On its own it reports that
one face rather than changing anything.

**--id** *UUID*

The texture to put on a face.

**--repeats** *S,T*

How many times it tiles.

**--offset** *S,T*

How far it slides, each from -1 to 1.

**--rot** *DEGREES*

How far it turns.

**--color** *R,G,B*

The tint, each of R, G, B from 0 to 255.

**--alpha** *N*

Opacity, 0 clear to 255 solid.  Without it, the face is left as it is.

**--fullbright**

Ignore lighting.

**--no-fullbright**

Stop ignoring it.  The two together are refused.

**--shiny** *N*

Shininess, 0 none to 3 high.  Without it, the face is left as it is.

**--glow** *N*

Glow, 0 to 255.  Without it, the face is left as it is.

## Examples

Ask what a thing looks like, then one face of it:

    texture lantern
    texture -f 2 lantern

Everything meant for one object in one line:

    texture --id d8467e57-... --glow 40 sign

See also: `dump`, which describes an object as JSON and leaves textures
out of it deliberately, and `reform` for the rest of what a prim is.
