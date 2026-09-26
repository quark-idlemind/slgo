# Where an ObjectUpdate says an object is

An `ObjectUpdate` carries an object's position and rotation, with its
velocities, in a packed blob, `ObjectData`, and nothing in the message
says which of several layouts the blob uses except its width in bytes.
`msg.DecodePlacement` (`msg/placement.go`) reads it. This is what that
follows, and what has and has not been seen.

## The widths

What follows is read from Firestorm's source, not measured.
Line numbers are in `indra/newview/llviewerobject.cpp` unless another
file is named.

The viewer names eight widths (1221-1228): 60 and 76 are full
precision, for a prim and for an avatar; 32 and 48 are sixteen bit, for
a prim and an avatar; 124, 140, 64 and 80 are the same four with room
for data to be added later. An avatar's blob is sixteen bytes longer
than a prim's because it starts with a collision plane.

An `ObjectUpdate` is handed to `processUpdateMessage` as `OUT_FULL`
with no data packer (`llviewermessage.cpp:4576`,
`llviewerobjectlist.cpp:746`). Its switch on the width (1392-1443)
reads 60 and 124, and 76 and 140 behind their collision plane, all as
the same 60 bytes of floats, and ignores whatever follows those in the
wider two. Any other width
is logged as unexpected and not read, under the comment "length values
48, 32 and 16 were once in viewer code but are never sent by the SL
simulator" (1439).

The sixteen bit layout is in the same function, under `OUT_TERSE_IMPROVED`
(1616-1705): position, velocity and acceleration six bytes each,
rotation eight -- all four components -- and angular velocity six. X
and Y run from half a region below to half a region above it, Z from
`MIN_HEIGHT` to `MAX_HEIGHT` (1305-1306, 1651): a region's width below
zero (`llworld.h:125`), and on Second Life `SL_MAX_OBJECT_Z`, 4096
metres (`llworld.cpp:207`, `llmath/xform.h:38`). Reading the source,
nothing reaches it: that case is only taken with no data packer, the
one call with none is the one `ObjectUpdate` makes as `OUT_FULL`, and
an `ImprovedTerseObjectUpdate` comes with a packer
(`llviewermessage.cpp:4633`) and is read at 1737 instead.

There is no sixteen byte layout anywhere in the viewer. The comment at
1699 says one was there once.

## What slgo reads

- 60, and 76 behind its collision plane: floats, as the viewer reads
  them.
- 124, and 140 behind its collision plane: the same 60 bytes of
  floats, with whatever follows them ignored. The viewer reads these
  widths this way, and says that is how a width made longer for data
  added later is to be read (1215-1219).
- 32, and 48 behind its collision plane, although the viewer's
  `ObjectUpdate` path does not read either width and its comment says
  the simulator never sends them. The owner decided to read them all
  the same, with the viewer's sixteen bit layout and ranges from the
  unreached case above, and to count every width so that traffic
  shows whether they occur (see below). The fourth rotation component
  is kept and normalised the way `DecodeTerse` keeps it. In terse
  updates the simulator was measured not to keep W positive; in this
  form nothing has been measured, and it is read the same way because
  the viewer's reader keeps all four.
- Nothing else.

Before 2026-09-26 the 32 byte form's rotation dropped W and recovered it
as positive, which reads a -q as the mirror image of q; its Z ran over
-128 to 384 metres, the span X and Y use, so nothing above 384 metres
could be said in it; a 16 byte width was read in a layout nothing
here or in the viewer describes; and 124 and 140 were not read at all.

## What has been seen

Every new prim in the rez measurements was first reported in the 60
byte form (`rez.md`). No 32 or 48 byte blob has been recorded, and the
tests for that form are built from the layout, not captured.

So the daemon counts every blob by its width, unread widths included,
and `slsh status` prints the counts on its `placements` line. A day's
traffic there says whether the sixteen bit form, or any width not read
here, is ever sent.
