Draws the other avatars as characters on a grid.  It is the half of the
question `who` cannot answer: a listing says somebody is twenty metres
away and nothing at all about which way, and three people at twenty
metres are a crowd in one direction and a scattering in another.

There are two pictures.  By default it is the ground around this
avatar, sixty-four metres across, with the avatar in the middle of it.
The other is the whole region -- 256 metres -- with the avatar
wherever in it the avatar really is.

    map

## The picture is wider than it is tall, which is what makes it look square

A character cell is taller than it is wide, so a grid drawn with as
many columns as rows comes out on the screen taller than it is wide,
and a square region looks like a doorway.

The rows are what is chosen and the columns follow: as many as it takes
for a square of ground to look square.  How much taller than wide a
cell is belongs to the font, so it is a setting -- `map_ratio` --
written height first.  The default is 7:3.  `set map_ratio 2:1` is
twice as tall as wide.  `set map_ratio auto` asks the terminal for the
cell size and writes down what came back, which is where to start
rather than the answer: what a terminal reports is the box it hands the
font and not the shape the letters look.  `man set` has the rest of it.

The line under the grid says how much ground the picture covers --
`64m x 64m` -- because that is the number somebody reads a map for.
More rows buy detail rather than reach, so drawing it taller does not
make it cover more ground.

## The marks

    *   this avatar
    o   somebody within three metres of this avatar's height
    ^   somebody higher than that
    v   somebody lower than that

Three metres is about one storey: an `o` is somebody who could be
walked over to and a `^` is somebody on the roof.  It is the
`map_level` setting, and whatever it is set to is the number in the
line under the grid.  The height is compared with this avatar's in both
views, so a picture of the whole region still says who is up in a
skybox.

A friend's mark is drawn in colour.  Two or more people in one cell are
drawn as the count -- 2 to 9 -- and ten or more as a `+`.  Somebody
standing in the same cell as this avatar is covered by the star, which
wins its own cell: a picture whose reader cannot find themselves in it
is a picture of nowhere.  The line under the grid says how many are
under the star.

A picture with no star in it says so.  Where there is nobody else at
all there is no count either, only "nobody else is in range", which is
the wording `who` uses for the same state.

## A friend is picked out in colour

Green unless `map_friend_colour` says otherwise.  The colours are named
rather than spelled as escape sequences -- black, red, green, yellow,
blue, magenta, cyan, white, and "bright" before any of them.  The line
under the grid says the colour's own name, in that colour, so the
picture always names the word to type to change it.

A cell with several people in it is drawn as a count, and the count is
coloured when any one of them is a friend.  Somebody outside the
picture is coloured in the line that names them underneath.  The star
is never coloured.

The colour only ever reaches a terminal.  A picture written to a file
-- `map > listing` -- and a picture from `slsh -c` or `slsh -f` or a
piped session have no escape sequences in them at all.  `NO_COLOR` is
honoured: set it to anything at all, `0` included, and nothing is
coloured.  When there is no colour there is no line explaining one
either.

## North is up and east is right

Anybody the picture does not reach is counted and named underneath it,
with how far away they are, rather than being drawn at the edge.  An
avatar drawn at the edge is a claim about where they are standing, and
that claim would be false.  The names stop at eight and the rest are a
count, because naming forty people under a grid is `who` printed twice.

## Scale

Nothing chooses the region view's scale -- a region is 256 metres --
but the close view has to cover something, and 64 metres is a choice:
comfortably more than the twenty metres ordinary chat carries, and
rather less than the draw distance usually is.

## What can be set

`map_rows` is how many rows a picture is drawn in.  `map_span` is how
much ground the close picture covers, in metres.  `map_ratio` is the
shape of a character cell, height first.  `map_level` is how far above
or below still counts as level.  `map_friend_colour` is the colour a
friend is drawn in.

`set` lists them with the values they have now, and `set NAME VALUE`
changes one and writes it to the settings file, so that the next
picture and every picture after it is drawn that way.

## It numbers nobody

Every listing that prints a number down the left -- `who`, `friends`,
`lookup` -- replaces what a number means for the commands that take
one.  A picture cannot be numbered usefully, since two people in one
cell are one character, so this deliberately leaves the last listing
alone: after a map, `im 2` still means the second line of whatever
printed one.

## What it cannot show

The avatars are the ones the simulator has described to this session,
which is what is within the draw distance and not everyone in the
region.  A picture of the whole region can only mark the people this
session has been told about: an empty corner is a corner nobody has
described, not a corner with nobody in it.  The count under the grid,
and `where`, are what to believe; the blank cells are not.

## Options

**-r, --region**

Draw the whole region -- 256 metres -- with the avatar wherever in it
the avatar really is.  Without it, the ground around this avatar,
sixty-four metres across.

**--rows** *N*

How many rows to draw, for this picture only, from 2 to 64.  Without
it, `map_rows`.

**--span** *METRES*

How much ground the close picture covers, in metres across.  Without
it, `map_span`.  Less than 2 is narrower than the avatar in the middle
of it.  `--region` has no span to change and says so rather than
ignoring one.

## Examples

    map
    map --region
    map --rows 24
    map --span 24

See also: `set` for the settings above and everything else slsh keeps,
`who` for the same people as a listing with their distances and the
numbers other commands take, `where` for this avatar's own position,
`look` for what the region says about itself, `parcel --map` for the
land rather than the people, and `tp` for going to a position rather
than reading one.
