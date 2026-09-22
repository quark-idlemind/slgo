The region, the position in it, what the avatar is sitting on if it is
sitting on anything, and which group it is acting as.

    where
    Testville at 33, 73, 1000
    sitting on "a bench" 45557e57-... (local 8360)
    acting as group Example Builders (d8467e57-...)

The position is in the region's own metres, to the metre -- the same
numbers `tp` and `move` take, and the same numbers `place --at` takes.

## How exact it is

Exact on arriving in a region and after a teleport.  While the avatar
walks, x and y stay good to the metre and the height is in steps of
about four metres.

Above 1020 metres the height stops updating.  The printed Z is then
where the avatar was put -- the arrival or the last teleport -- and not
where it has settled or fallen since.  `landmark --make` is the way to
read a height up there; x and y stay good the whole time.

## The sitting line

It is there when the avatar is sitting on something and missing when it
is not, and it is in the same shape `sit` prints, since it is the same
fact.

It belongs on this listing because a seated avatar's position is not
somewhere it walked to.  A sit picks the avatar up and carries it to
the seat from as much as ten metres off, so the coordinates on the line
above are the seat's doing, and this is what explains them.

The answer is the region's rather than this session's memory: an avatar
sitting on something is PARENTED to it, and the parent is part of how
the region describes the avatar.  So a shell that attached a minute ago
gets the same answer as one that was connected when the sit happened.
It used to get "not sitting", because an avatar sitting still since
before we attached had never been described to us.

A sit on the GROUND is different and is reported only when this session
performed it.  Nothing is parented in a ground sit -- there is nothing
to be parented to -- and what says it is happening is an animation,
which this session hears only while it is waiting on one.

## The group line

A second line names the active group and its key.  It is missing when
the avatar is acting as none, which is how a headless login starts.

That line is the usual answer when building is refused.  A parcel often
grants "create objects" to a group rather than to a person, and the
refusal talks about the land rather than the group.  The name says
whether this is the group the land wants; the key is what `group` takes.

## Examples

    where

See also: `parcel` for which piece of the region this position is on,
`look`, `who`, `map` for this position and everybody else's drawn as a
picture, `objects`, `tp` for moving the avatar, `regions` for a region
the avatar is not in, and `group` for setting the one this reports.
