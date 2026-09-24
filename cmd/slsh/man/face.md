`face` turns the avatar toward a place, somebody or something, without
moving it.  The place is two numbers, X and Y, in the region's own
metres; a name is taken the way `walk` takes one, people first and then
objects.

    face 128 64
    face Example
    face lantern
    face --yaw 90

What it prints is the heading the avatar was given, in degrees
anticlockwise from east and as the nearest point of the compass:

    facing 90° N

The avatar keeps that heading.  The daemon goes on saying which way the
avatar faces every time it tells the simulator anything, so a `face`
lasts until a walk or another `face` changes it.

## Options

**-y, --yaw** *DEGREES*

Turn to a heading rather than toward something: degrees anticlockwise
from east, so 0 is east, 90 north, 180 west and -90 south.

## What else it does

A walk under way is ended by it, as `cancelled (superseded)`, and the
avatar is stopped, since a heading set under a walk would be undone a
tenth of a second later.

Turning on the spot can shuffle the avatar a few centimetres; measured
on 2026-09-24, a turn moved it about a tenth of a metre.

A sitting avatar is refused, and so is one somebody is driving from a
viewer.  The place the avatar is standing on has no direction and is
refused too.

## Examples

    face Example
    face -y 180

See also: `walk`, `halt`, and `who` for who there is to face.
