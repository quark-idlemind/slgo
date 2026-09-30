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

The answer is the agent's rather than this session's memory: an avatar
sitting on something is PARENTED to it, the parent is part of how the
region describes the avatar, and the agent behind this shell has kept
that description since it logged in.  So a shell that attached a minute
ago gets the same answer as one that was connected when the sit
happened.  It used to get "not sitting", because an avatar sitting
still since before we attached had never been described to us.

A sit on the GROUND is found the other way.  Nothing is parented in a
ground sit -- there is nothing to be parented to -- and what says it is
happening is an animation, which the agent has heard all along too; so
a ground sit is reported whoever made it, this shell, a viewer or
another client.  Only a daemon too old to be asked leaves this shell
with what its own session heard, and then a ground sit is reported
only when this session performed it.

## The group line

A second line names the active group and its key.  It is missing when
the avatar is acting as none, which is how a headless login starts.

That line is the usual answer when building is refused.  A parcel often
grants "create objects" to a group rather than to a person, and the
refusal talks about the land rather than the group.  The name says
whether this is the group the land wants; the key is what `group` takes.

## The controls line

    controls taken by a script: forward, back
    controls taken by a script and passed on: up

A script that has asked for the avatar's controls -- a vehicle, a
heads-up display, a game -- holds some of them, and these lines say
which.  They are missing when no script holds any.

The first are the ones the script keeps to itself.  The avatar does not
move when one is pressed, or walked with: `walk` is refused while
`forward` is in that line, and ends if a script takes it in the middle.
The second are the ones the script is told of and the avatar obeys as
well.  The words are forward, back, left, right, up, down, turn left,
turn right, click and mouselook click, and a control with none is its
number in hex.

It is counted as a viewer counts it: each take adds one and each
release takes one off, so a control two scripts hold is still held when
one lets go.  It is what the simulator has told this session since it
began, and it is never cleared, so the line can be stale if a script
died without letting go.

## The health line

    health 73%

It is there only when the simulator has said the avatar's health and it
is below full: an avatar nothing has hurt is at 100, which says nothing
worth a line.  It is the last figure sent, as a whole number the way a
viewer's status bar shows it, and it is kept across a move, so after
one it is the last region's figure until the new one says its own.

## Examples

    where

See also: `parcel` for which piece of the region this position is on,
`look`, `who`, `map` for this position and everybody else's drawn as a
picture, `objects`, `tp` for moving the avatar, `regions` for a region
the avatar is not in, and `group` for setting the one this reports.
