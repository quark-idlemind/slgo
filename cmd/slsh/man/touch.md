`touch` clicks an object, the way a viewer's mouse does: a script sees
`touch_start` and `touch_end` from the same messages a viewer would
have sent.  Given points and durations it becomes a drag instead, which
is the general case a plain click is the smallest of.

    touch greeter

The object is named by the word the region calls it, or by its key,
and a name several objects answer to is refused rather than guessed
at.  A bare click lands in the middle of face 0, pointing up, at the
object's own position.

## Options

**-f, --face** *N*

Which face, counting as LSL does.  Without it, face 0.  `-1` is what a
viewer sends when it hit the object but not a face of it.

**--st** *S,T*

Where on that face, each number from 0 to 1 across it, which is what
`llDetectedTouchST` reports.  Without it, the middle of the face, or
with `--uv` the point that texture coordinate is at.

**--uv** *U,V*

The same point in the texture's coordinates -- after the face's
repeats, offset and rotation -- which is what `llDetectedTouchUV`
reports.  Without it, worked out from `--st`; see below.

**--at** *X,Y,Z*

The point in region coordinates, which is what `llDetectedTouchPos`
reports.  Without it, the object's own position.

**--normal** *X,Y,Z*

The direction the surface faces there.  Without it, `0,0,1`.

**--press** *SECONDS*

Hold still at the first point before moving.

**--move** *SECONDS*

How long the path takes.  Two points or more are refused without it.

**-H, --dwell** *SECONDS*

Rest at the last point before letting go.

**--rate** *N*

How finely a moving touch is sampled, not how many events a script
sees: while a touch is held, the `touch` event fires 22.5 times a
second however many updates go out.  Without it, 45.

## Where on the object a touch lands

A viewer decides what was clicked by casting a ray from the camera
through the mouse, and then sends the answer: the object, the face,
the point of intersection, the surface normal, the texture
coordinates.  The simulator does no geometry of its own -- it hands
those numbers to the script as `llDetectedTouchFace`,
`llDetectedTouchPos` and the rest.

That makes this more precise than a viewer rather than less.  A test
that has to touch the third face of a prim nine tenths of the way along
one edge does not have to place a camera and aim -- it says face 3 and
a point on it.  Nothing checks that these numbers describe a point
really on the object; the simulator does not check either.

## On the face, and in the texture

A point on a face is sent twice: as ST, where it is on the face, and
as UV, where it is in the texture drawn there.  The viewer's raycast
finds the first, and it works the second out from it with the face's
texture settings -- turned about the middle by the rotation,
stretched by the repeats and slid by the offset.  A face with nothing
done to its texture has the two the same.

So `--st` is the one to give for "this part of the face" -- a button
on a HUD, the end of a slider -- and `--uv` for "this part of the
picture".  Give one and the other is worked out as the viewer does,
from what the face looks like as this session last heard it; give
both and both go as given.

The viewer uses more than those settings for a face with planar
mapping or a texture animation running on it, and so does not work UV
out this way there.  Nothing here follows it that far: on such a face,
and where the face's appearance cannot be read, the one given goes as
both.  A texture repeated zero times has no one point for a UV to be,
and `--uv` alone on it is refused.

## Points after the first are a drag

Each further argument is another point, read on top of the one before
it, so a drag across a single face names the face once.  Two numbers
are a place on a face, its ST, with the UV worked out afresh, and three
are a place in the region, which is what tells them apart without
another flag; a leading number and a
colon names the face, so a drag can cross from one to another.  Two
points or more are refused without `--move`, since a path with no
duration is a jump.

Nothing acknowledges a touch either: what to wait for is the script's
own output rather than the line printed here.

## Examples

    touch greeter
    touch --face 2 --st 0.9,0.5 sign
    touch --press 2 --move 1.5 --dwell 0.5 sign 2:0.9,0.1

See also: `objects` for finding the thing to click, `texture` for its
faces, and `dump` for the scripts inside it.
