# Finding the prim a rez made

`Build` and `Rez` in `sl` both make a prim through `rezAt`
(`sl/build.go`), which sends an `ObjectAdd` and then has to find the
prim in the region: nothing in reply names the prim that was made.
`findOurs` finds it by where it stands. This is what that rests on.

## Where a new prim lands

Measured on Agni on 2026-09-25 and 2026-09-26, with `rezAt` as it
stands: an `ObjectAdd` with `BypassRaycast = 1`, and `RayStart` and
`RayEnd` both the point asked for. The prims were unrotated 0.5 m
cubes, asked for at 4,002 m, just above the ground, and 43 m and 3 m
under the ground.

- X and Y came back as exactly the float32 of the values asked for,
  bit for bit.
- Z came back as exactly float32(the Z asked for) + 0.25, half the
  height, added in float32, bit for bit, at every height from 45 m to
  4,004 m. The simulator puts the prim's **bottom** on the point asked
  for, not its centre.
- A point under the ground was raised until the box's bottom rested on
  the ground: on gentle slopes, the ground under the box's centre.
- The first report of the new prim was always a full `ObjectUpdate`
  carrying its position in full floats (the 60-byte form), and it was
  already the position the prim settled at.

## How a rez is recognised

`findOurs` takes a prim as the one just made when it is a root prim,
was not in the region before the rez, is owned by this avatar, and
stands where the measurements above say it will:

1. X and Y equal to what was asked for, within 1 mm.
2. Z at or above the Z asked for, less 1 mm.
3. Among several that pass, the one whose Z is closest to the Z asked
   for plus half the Z scale sent, which is where the centre of an
   unrotated prim was measured to be. A nearer one whose owner is not
   yet known is asked about and waited for, not passed over.

What is inferred rather than measured:

- The 1 mm. Positions came back exact, so any allowance would do; 1 mm
  is more than the spacing of float32 values anywhere below 8,192 m,
  which is about half a millimetre just under it. That is arithmetic.
- That Z never comes back lower than it was asked for. Every offset
  measured was upward; nothing was measured going down.
- Where a rotated prim's centre ends up. Nothing rotated was measured,
  so the expected height is used only to choose between prims that
  both pass the first two rules, and never to turn one away.

What was in the region before the rez is read from the backend's list
of objects, not from what this session has been relayed: a client that
attached a minute ago has been relayed almost nothing, and everything
it had not heard of would look new.

Recognising a rez by novelty alone went wrong twice before this. It
reported an object that had just been taken, whose entry lingered until
its `KillObject` was processed (4efc3c1, in `RezFromInventory`, which
has matched by position since). And it returned the avatar, which is
owned by us and was new to the session when a build followed a login
closely (fc26d52).
