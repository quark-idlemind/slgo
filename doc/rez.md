# Finding the prim a rez made

`Build` and `Rez` in `sl` both make a prim through `rezAt`
(`sl/build.go`), which sends an `ObjectAdd` and then has to find the
prim in the region: nothing in reply names the prim that was made.
`findOurs` finds it by where it stands. This is what that rests on,
and why the prim is made in the avatar's active group.

## Where a new prim lands

Measured on Agni on 2026-09-25 and 2026-09-26, with `rezAt` as it
stands: an `ObjectAdd` with `BypassRaycast = 1`, and `RayStart` and
`RayEnd` both the point asked for. Every prim was an unrotated 0.5 m
cube; 56 were rezzed and deleted.

- 12 were asked for at about 4,002 m, and 12 just above the ground, at
  about 43.6 m to 45.9 m. Each came back with X and Y as asked and Z
  exactly 0.25 higher, to the millimetre the probe printed.
- 8 more, 4 near 4,004 m and 4 near 45 m, were asked for with X, Y and
  Z given to seven decimals, and compared bit for bit. X and Y came
  back as the float32 of the values asked for, and Z as
  float32(the Z asked for) + 0.25, added in float32. So the simulator
  keeps a requested point as the float32 it was sent as, and puts the
  prim's **bottom** on it, not its centre. The only loss is float32's
  own spacing: about 0.24 mm near 4,004 m.
- 24 were asked for under the ground, 12 about 43 m under it and 12 at
  z = -3.17. X and Y came back as asked, and each was raised until its
  bottom rested on the ground. Both depths gave the same height at each
  point, and on that gentle slope it was the ground under the box's
  centre.
- The first report of every new prim was a full `ObjectUpdate`
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

The list is looked at every 250 ms for up to 15 s, through `poll`. A
look that fails is made again, and the timeout names the last failure.
A caller that gives up is told so at once, after one more look on a
context the cancel does not reach (`lastLook`). A prim that look finds
passes the same rules, so it is confirmed, and `Build` returns it with
the error so that the caller can clear it away, as `slsh`'s `rez` does.

Recognising a rez by novelty alone went wrong twice before this. It
reported an object that had just been taken, whose entry lingered until
its `KillObject` was processed (4efc3c1, in `RezFromInventory`, which
has matched by position since). And it returned the avatar, which is
owned by us and was new to the session when a build followed a login
closely (fc26d52).

## The group a prim is made in

Measured on Agni on 2026-09-26: on a parcel that runs only group
scripts, a script in a prim that had no group, within 50 m of the
ground, never executed, though the simulator reported it running.
[ground.md](ground.md#where-the-land-stops-running-scripts) has the
measurement, and what `Run` does about it.

`rezAt` sets `GroupID` in the `ObjectAdd` to the avatar's active group,
as a viewer does. Firestorm's `LLToolPlacer` sends
`FSCommon::getGroupForRezzing()` (`newview/lltoolplacer.cpp:319`),
which is `gAgent.getGroupID()`, or the land's group under the
`RezUnderLandGroup` setting (`newview/fscommon.cpp:505-522`). slgo does
not follow that setting: the active group is the one `group` shows and
sets.
