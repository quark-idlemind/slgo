# A second touch of an object waits for the first to be over

The region takes a release and a press of one object that come within
about a frame of each other for one touch. A script then sees the first
`touch_start` and the second `touch_end`, and the second start is lost.

## Measured

On 5 October 2026, an invented box whose script said each `touch_start`
and `touch_end`. A click (`sl.Touch`: a grab and a degrab, nothing
between), then after a gap a second click on the same box:

| gap | second touch_start reached the script |
|---|---|
| 0 ms | 0 of 5: the two merged, the script saw the first start and the second end only |
| 10 ms | 3 of 5 |
| 15 ms | 8 of 10 |
| 20 ms | 5 of 10 |
| 25 ms | 14 of 15 |
| 30 ms to 1 s | all |

The same on a two-prim box with its script in the root, a click on the
child and then a click on the root:

| gap | root's touch_start delivered |
|---|---|
| 0 ms | 0 of 10: merged, the child's start and the root's end only |
| 10 ms | 4 of 10 |
| 20 ms | 9 of 10 |
| 100 ms | 10 of 10 |

A click on a separate object followed by the root lost nothing at 0, 10,
20 or 100 ms. Touches merge within one linkset, whichever prims are
touched, and not across objects.

The region runs at 45 frames a second (22 ms). A person does not click
that fast; Slate does: a `touch` and then a `drag` of the same object in
the next step went about 20 ms apart, and the drag's `touch_start` was
lost.

## The rule

A grab of a prim whose linkset this session last released less than
90 ms ago waits out the rest first (`touchGap` in `sl/touch.go`). A
miss was seen at 25 ms (1 of 15) and none from 30 ms up, so the edge is
about 30 ms; 90 ms is three times that, and under the roughly 100 ms a
person needs to click again (by estimate, not measured), so nothing
realistic is slowed.

The gap is kept by the linkset's root local id: the prim's parent when it
has one and that is not the avatar, otherwise the prim itself (a worn
HUD's root hangs from the avatar, so the root is its key). A touch of
another linkset is not delayed. Every grab is sent by `touchStart` and
every degrab by `touchEnd`, so `Touch`, `TouchStart`, `Drag`,
`DragOnScreen` and `TouchHold` all keep it.
