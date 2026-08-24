`move` shifts an object that is already standing in the region to a
position.  It changes nothing else: the object keeps the rotation and
the scale it had.

    move probe 128 128 25

The object is named by the word the region calls it, and a name with a
space in it has to be quoted.  A name that several objects answer to is
refused rather than guessed at, since moving the wrong one is a thing
somebody then has to find.  The position is three numbers -- X Y Z,
separated by spaces, in this region's metres.

A key will not do in place of the name.  `take`, `touch`, `dump` and
the rest accept a uuid where a name would go; `move` looks its argument
up as a name and as nothing else.  A uuid typed there is refused with
`no object named` and the uuid back again, which reads like the thing
has gone rather than like the form is wrong.  The way past a name
several objects answer to is the one the refusal itself names -- rename
one of them -- and not the key every neighbouring command would have
taken.

Nothing answers a move: the region reports the new position when it
gets round to it, so this asks again until the object is where it was
sent rather than reporting the request and stopping.  A position read
back by hand without that wait is the one the object had before,
printed with total confidence.  A move that has gone nowhere after twenty seconds
is reported as that, and the likeliest reason is the one the message
names: a parcel that will not have objects moved refuses in silence.

## This is not `place`

`place` puts a thing into the world out of inventory.  `move` shifts
one that is already there.  They were the other way round until
recently -- `place` meant this command -- so a script written before
the change may say `place NAME X Y Z`, and that line is now refused
rather than obeyed.

## Putting something back where it was

Second Life does not remember where an object stood before it was
taken, so a take-and-place round trip lands the object beside the
avatar and not where it started.  Writing the position down first and
moving it back afterwards is the whole of the fix:

    objects probe
    take probe
    place Objects/probe
    move probe 33 73 1000

## Examples

    move probe 128 128 25
    move "big sign" 33.5 73 1000.2

See also: `place`, `take`, `objects` for what is in the region and
where, and `tp` for moving the avatar rather than an object.
