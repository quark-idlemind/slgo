`wear` puts an inventory object on the avatar.  The argument is an
inventory path, relative to the folder the shell is in, or the uuid of
an item -- the same sort of argument `place` takes.

    wear Objects/badge

The report names the point the region gave back rather than one guessed
here, so nobody has to know where a HUD asked to land.

## Options

**--at** *POINT*

Where to wear it.  Without it, wherever the object itself says, which
is the point its maker set.

The point may be the way `worn` prints it (`left hand`, `HUD top
right`), the way the viewer names it on its own menu (`Skull`, `R Upper
Arm`, `Top Right`), or its number.  Case and surrounding space do not
matter, and the American `center` is taken for `centre`.  The numbers
run from 1 to 127; `0` asks for wherever the object itself says, the
same as leaving `--at` off.  A word that is neither a printed name nor
a viewer name -- `elbow` -- is refused rather than guessed at.

Only the printing differs from the viewer.  `R Upper Arm` is written
out as `right upper arm`, HUD points are said to be HUD ones rather
than left as the viewer's bare `Top` and `Center`, and three are
different words: the point the viewer calls `skull` is `head` here,
`spine` is `back` and `stomach` is `belly`.  `--at` understands both
spellings.

**--replace**

Take off whatever is on that point, instead of wearing alongside it.
A bare `wear` adds.  A wrong add is visible and costs a `detach`.  A
wrong replace is invisible and costs whatever was there.

The report names what it displaced -- `X is worn on chest; Y came
off` -- by looking afterwards rather than by predicting, because a
replace displaces one attachment and not the whole point's worth of
them.  Where the clause is missing, either nothing came off or the
region has not caught up; the line is never a guess.

## The same item twice is refused

Adding makes it possible to wear one inventory item twice, and `wear`
will not.  An attachment is known here by the item it came from, so two
attachments from one item agree in every field a person could name one
by: `detach` would find both and refuse as ambiguous.  The refusal
names the point the item is already on, and `--replace` and `detach`
are the two ways on from there.

A folder is refused: `wear` takes one object.

## Clothing and body parts

A shirt, a skin, a shape, a pair of eyes or a hair base is a system
wearable rather than an object.  `wear` takes one all the same and
works out which it is from the item.

It does not attach.  A wearable is worn by putting a link to it in the
Current Outfit folder -- which is the grid's own record of what an
avatar has on, and what the baking service reads -- and then asking the
region to rebuild the appearance.  The report says the slot rather than
a point, because there is no point:

    wear Clothing/a blue shirt
    a blue shirt is worn as shirt

The slot is what a person is choosing between when they have four
shirts and can wear one of each layer.  It is `shape`, `skin`, `hair`,
`eyes`, `shirt`, `pants`, `shoes`, `socks`, `jacket`, `gloves`,
`undershirt`, `underpants`, `skirt`, `alpha`, `tattoo`, `physics` or
`universal`.

**A body part always replaces.**  There is no such thing as an avatar
wearing two skins, so a bare `wear` of one is a replace however it is
worded, `--replace` or not, and the line says what came off.  Clothing
layers, so it adds unless `--replace` is given -- the same default, and
the same reason, as wearing an object.

`detach` takes clothing off again.  It will not take off a body part:
an avatar is never without a shape, a skin, hair or eyes, so the way
out of one is to wear another.  The viewer draws the same line without
saying so, by offering Take Off for clothing and not for a body part.

## The Current Outfit folder

Everything this command does to a wearable happens in that folder, and
`ls -L "/Current Outfit"` is how to read it: one line per worn thing,
clothing and attachments alike, with the links followed.

Attachments go in it too.  An object put on here is linked into the
folder, and taken out of it by `detach`, so that what is worn now is
what `dress` and `slbotd` put back after the next login.  A viewer
writes the folder for both kinds in the same way.

Every change to the folder is followed by a request to rebake the
avatar, as a viewer makes one.  For clothing that is what makes the
change visible.  For an attachment it is what brings the simulator's
own list of what is attached up to date -- the list `worn` and `dress`
check against.  A rebake that fails does not undo the wearing, and is
reported on the same line.

## Examples

Put a HUD on wherever it asks to go:

    wear Objects/badge

Put one on a named point, over whatever is there:

    wear --replace --at "HUD bottom right" Objects/meter

See also: `detach`, `worn` for what is on and where, and `place` for
putting an inventory object into the world instead of onto the avatar.
