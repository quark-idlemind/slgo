touch clicks an object, the way a viewer's mouse does: a script sees
touch_start and touch_end from the same messages a viewer would have
sent.  Given points and durations it becomes a drag instead, which is
the general case a plain click is the smallest of.

The object is named by the word the region calls it, or by its key, and
a name several objects answer to is refused rather than guessed at.
With nothing else said the click lands in the middle of face 0,
pointing up, at the object's own position.

## Where on the object a touch lands, and who works it out

A viewer decides what was clicked by casting a ray from the camera
through the mouse, and then sends the answer: the object, the face, the
point of intersection, the surface normal, the texture coordinates.
The simulator does no geometry of its own -- it hands those numbers to
the script as llDetectedTouchFace, llDetectedTouchPos and the rest.

That makes this more precise than a viewer rather than less.  A test
that has to touch the third face of a prim nine tenths of the way along
one edge does not have to place a camera and aim -- it says face 3 and
a point on it.  --face is which face, counting as LSL does, and -1 is
what a viewer sends when it hit the object but not a face of it.  --uv
is where on that face, each number from 0 to 1, and --st is the same
point in the texture's own coordinates.  --at is the point in region
coordinates, which is what llDetectedTouchPos reports, and --normal is
the direction the surface faces there.  Nothing checks that these
numbers describe a point really on the object; the simulator does not
check either, and a script tested edge-on is not a mistake.

## Points after the first are a drag

Each further argument is another point, read on top of the one before
it, so a drag across a single face names the face once.  Two numbers
are a place on a face and three are a place in the region, which is
what tells them apart without another flag; a leading number and a
colon names the face, so a drag can cross from one to another.  Two
points or more are refused without a time to cross them in, since a
path with no duration is a jump.

## How many events a script actually sees

While a touch is held, the touch event fires 22.5 times a second --
half the simulator's frame rate -- and that is the simulator's business
rather than a client's.  It was measured by holding a touch on a
counting script while varying how fast updates were sent: one a second
and ninety a second produced the same count, which depended on the
duration and on nothing else.  So the rate flag decides how finely a
moving touch is sampled, and not how much the script hears.  Nothing
acknowledges a touch either: what to wait for is the script's own
output rather than the line printed here.

## Examples

    touch greeter
    touch --face 2 --uv 0.9,0.5 sign
    touch --press 2 --move 1.5 --dwell 0.5 sign 2:0.9,0.1

See also: objects for finding the thing to click, texture for its
faces, and dump for the scripts inside it.
