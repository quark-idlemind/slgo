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

## Examples

Put a HUD on wherever it asks to go:

    wear Objects/badge

Put one on a named point, over whatever is there:

    wear --replace --at "HUD bottom right" Objects/meter

See also: `detach`, `worn` for what is on and where, and `place` for
putting an inventory object into the world instead of onto the avatar.
