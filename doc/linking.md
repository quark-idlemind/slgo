# Unlinking

`sl.Unlink` frees prims from their linkset by naming them in an
`ObjectDelink`, and waits for each to say it has no parent. The comment
on it in `sl/object.go` says what it sends and why the root is left out
of the wait. This page is where it departs from the viewer, and why.

## The root left out

The viewer sends the root of a linkset in a delink, since clicking an
object selects the whole linkset and every prim of a selection goes
into the message. So the selection `Unlink` takes -- exactly the prims
being freed -- is one the viewer would not make, and the viewer's
source cannot say whether the simulator minds: it is only ever the
sending half.

It does not mind. Measured on Agni, in Pelmar Reach: three prims linked
into one object, then a delink naming only the last child, with the
root left out of both the selection and the message. The child came out
and stood where it had been, and what was left answered to the root's
name as two prims.

## The physics shape the viewer changes on the way past

Before sending, the viewer walks the selection and, for every
modifiable prim whose physics shape type is `PHYSICS_SHAPE_NONE`, sets
it to `PHYSICS_SHAPE_CONVEX_HULL` and calls `updateFlags()`
(`llselectmgr.cpp:5458-5472`). `Unlink` does not, for two reasons.

The first is that the step never puts the shape on the wire.
`updateFlags()` takes a `physics_changed` parameter which defaults to
false (`llviewerobject.h:650`) and the delink passes nothing, and it is
that parameter which decides whether the `ExtraPhysics` block carrying
`PhysicsShapeType` is added at all (`llviewerobject.cpp:7297-7305`).
What reaches the simulator is an `ObjectFlagUpdate` of `UsePhysics`,
`IsTemporary` and `IsPhantom` -- none of which the delink touched. The
new shape type stays in the viewer's own copy of the object, which is
where its build floater reads one from.

The second is that this client does not know what a prim's physics
shape is. It arrives in `ObjectPhysicsProperties`, which nothing here
asks for or decodes, so copying the step would mean fetching the
properties of every prim and sending a message `sl` does not have, in
order to reproduce bookkeeping the viewer does for its own display.
What a freed prim's physics shape ends up as is the simulator's
business and was not verifiable from the viewer's source; if one ever
comes out of a delink shaped wrongly, that is worth measuring on a live
region before writing code against it.
