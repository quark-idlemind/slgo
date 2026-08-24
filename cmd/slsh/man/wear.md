wear puts an inventory object on the avatar.  The argument is an
inventory path, relative to the folder the shell is in, or the uuid of
an item -- the same sort of argument place takes, because both of them
are naming something in inventory.

Where it lands is usually not something you say.  Without --at the
object goes wherever the object itself says, which is the point its
maker set, and the report names the point the region gave back rather
than one guessed here:

    wear Objects/place-probe
    place-probe is worn on HUD top

Nobody typed "HUD top" there, and nothing here could have worked it
out: it is the point the object itself carries, sent back in the
region's answer and read off that.  The points are the viewer's, the
names are for reading, and the two are not always the same word: the
viewer's abbreviations are written out ("R Upper Arm" is "right upper
arm"), the HUD points are said to be HUD ones rather than left as the
viewer's bare "Top" and "Center", and three are different words -- the
point the viewer calls "skull" is "head" here, "spine" is "back" and
"stomach" is "belly".

Only the printing differs.  --at understands the viewer's name for a
point as well as the one printed here, so "skull" and "head" are the
same point and either can be typed.  A word that is neither -- "elbow"
-- is refused rather than guessed at.

## Wearing adds rather than replaces

A bare wear puts the object on alongside whatever is already on that
point.  It does not take anything off; --replace is the other one.
That is the viewer's Add rather than its Wear, and the difference
between the two is one bit in the request, so which one a bare command
means was ours to choose.

It was chosen the hard way.  With a badge already on a HUD point,
wearing a second thing with no --at at all put that thing on the point
and took the badge off -- and nothing said so: not this command,
which printed its one line about what went on, and not the region.  A
wrong add is visible and costs a detach.  A wrong replace is invisible and costs
whatever was there.  Between two defaults, the one to have is the one
whose mistake can be seen.

--replace is the old behaviour, for when putting a thing where another
thing is really is what is meant.  It names what it displaced --
"X is worn on chest; Y came off" -- and it names it by looking
afterwards rather than by predicting, because a replace displaces one
attachment and not the whole point's worth of them.  Where the clause
is missing, either nothing came off or the region has not caught up;
the line is never a guess.

## The same item twice is refused

Adding makes it possible to wear one inventory item twice, and wear
will not.  An attachment is known here by the item it came from, so two
attachments from one item agree in every field a person could name one
by: detach would find both and refuse as ambiguous, advising you to
tell them apart by an item id that is precisely the thing they share.
The refusal names the point the item is already on, and --replace and
detach are the two ways on from there.

## Options

--at names the point, either the way worn prints it -- "left hand",
"HUD top right" -- or the way the viewer names it on its own menu --
"Skull", "R Upper Arm", "Top Right" -- or by its number, which is what
the point is on the wire.  Case and surrounding space do not matter,
and the American "center" is taken for "centre".  The numbers run from
1 to 127; a bigger one is refused, because the byte a point travels
in also carries the bit that says add rather than replace.  Zero is
taken as well and asks for what leaving --at off asks for: wherever
the object itself says.  The refusal for a bigger number names both:
1 to 127, and 0 for wherever the object itself says.

--replace takes off whatever is on that point instead of wearing
alongside it.

## Examples

Put a HUD on wherever it asks to go:

    wear Objects/badge

Put one on a named point, over whatever is there:

    wear --replace --at "HUD bottom right" Objects/meter

See also: detach, worn for what is on and where, and place for putting
an inventory object into the world instead of onto the avatar.
