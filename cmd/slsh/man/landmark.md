A place said by name, where `tp` is a place said in numbers.  A
landmark is an ordinary inventory item -- the grid's own way of writing
a place down -- and this lists the ones this avatar holds, says where
one of them goes, makes one of wherever the avatar is standing, and
goes to one.  Home is here too: going to it, and setting it.

With no arguments it lists them, as paths, so that two landmarks of one
name can be told apart.  With a name it reads that one and prints where
it goes, without moving.  Going there is asked for separately, and so
is going home:

    landmark
    landmark Example Workshop
    landmark --make Example Workshop
    landmark --go Example Workshop
    landmark --home
    landmark --set-home

The avatar moves in `--go` and `--home` and in nothing else.  That is
deliberate.  If going somewhere were the bare form, then reading a
landmark and being somewhere else afterwards would be one typo apart.
None of the four verbs has a short letter, for the same reason: `-g`
beside `-m` is exactly the typo a whole word prevents.  `--set-home`
carries the word "set" for a sharper version of it: a `-h` that meant
"make this home" beside a `-h` that meant "go home" is one keystroke
between a journey and a rewritten account setting, and only one of
those is undone by teleporting back.

A name with spaces in it needs no quoting: the words after the flags
are joined back together, as `tp` joins a region name.

The whole of inventory is looked in, not just Landmarks, but only four
levels down.  A landmark buried deeper than that is not listed and is
not found by name, and what comes back is "no landmark called that"
rather than anything about depth.  `find` counts the same four levels
from wherever it is started, so it will not see it either; an `ls` of
the folder it is in will.

## Options

**--make**

Make a landmark of where this avatar is standing, called *NAME*.
Making one, going to one, going home and setting home are four
different things; ask for one.

**--go**

Go to the landmark *NAME* names.

**--home**

Go to wherever this account's home is set.  It takes no name.

**--set-home**

Make where this avatar is standing the place home is.  It takes no
name and no position: home is set where the avatar stands, and
nowhere else.  Nothing moves.

**-w, --wait** *SECONDS*

How long to wait for the avatar to arrive, on `--go` and `--home`.
Without it, thirty seconds -- `tp`'s wait, for the same kind of trip.
It is refused on the reading form and on `--set-home`, neither of
which moves the avatar.

## A landmark is a point and not a place

What the asset holds is a region id and three numbers.  It carries no
region name, no parcel, no owner and no description of anything.  A
landmark remembers where the avatar stood and nothing about what was
there.  The reading form asks the grid what is there now, and prints
the parcel's name, the region's name and the parcel's area:

    Example Workshop
      in       /Landmarks
      region   3c8f7e57-...
      at       28.00, 71.95, 20.00
      item     a08f7e57-...
      asset    bb817e57-...
      parcel   Example Parcel, in Example Region
      area     1024 m²

That costs two questions, each given three seconds.  If either goes
unanswered, one `place` line says so and everything above it is still
printed.  The plain listing asks nothing.  `parcel` asks about the
land under the avatar, after arriving.

Nothing keeps the far end still.  The parcel may have been sold, the
platform stood on taken away, the region emptied or removed.  A
landmark into what is now empty air is an arrival in empty air followed
by a fall.

## Arriving somewhere is not the same as staying there

This command reports the arrival and stops watching.  Measured twice on
Agni: a landmark into a parcel with a security orb on it succeeded --
the avatar arrived, the command printed the position, and about one and
a third seconds later the parcel's own object teleported it home.  So a
position printed here can be true when it is printed and false a moment
afterwards.  If a landmark lands somewhere it should not be, run
`where` again.

The narrower case is unmeasured: what a parcel that refuses this avatar
by its access settings does to a landmark arrival, as against one that
admits everybody and then throws them out.

The name is printed before the waiting starts, and what is printed
after the arrival is where the avatar actually ended up, read back.

Everything the shell knew about the region it left is dropped on the
way, exactly as it is for `tp`.

## Where it goes is an id, and then a name

The line the reading form prints as `region` is the region's grid-wide
uuid, because that is what the asset says.  The asset has no name to
print, so the name comes from the parcel: the uuid and the position are
put to the grid, which names the parcel there, and the grid's description of that
parcel includes the name of its region.  Going there needs no lookup:
the simulator already has the region id.

The grid also gives a position for the parcel.  It is the parcel's own
point and not the landmark's, so it is not printed.

## An item has two ids and the grid takes only one

Every inventory item has an item id and an asset id.  `ls -l` prints
the first, and the second is the one every landmark message takes.
They are both uuids and nothing about either says which it is.

Sent the wrong one, the grid says nothing at all -- no start, no
progress, no refusal -- so the wait is the only way to find out.  It
runs to the end of `--wait`, thirty seconds unless another number was
given, and what is printed then says that the item id where the asset
id was wanted is the usual reason for the silence.

That is why a name here means something in inventory and never a bare
uuid handed on to the grid: the asset id comes out of the listing,
which is the one place the two are told apart correctly.  A uuid may be
typed, and it is looked up in that same listing as either of an item's
two ids; a uuid this avatar does not hold is refused rather than sent.

A landmark with no asset id in it is refused rather than being allowed
to become a quiet trip home: the null id is the one wrong id the grid
is not silent about, and it reads it as home.

A link to a landmark is not a landmark, and is left out of the listing
for the same reason: a link's asset id is the item id of the thing it
points at, so following one would produce exactly that silence.  Name
the landmark itself.

## What is in the trash is not reached from here

A viewer's delete moves things to the trash rather than destroying
them, so a deleted landmark still has a name, still has an asset and
would still teleport.  The trash is left out.  Nothing in it is listed,
and nothing in it can be named -- not by its name, not by its whole
path, and not by either of its ids.  `mv` moves a landmark back out of
the trash, and then it is a landmark again.

A name whose only answer is in the trash is told so, and told where it
is.  A listing with nothing in it says how many of them are deleted.

The trash is found by its folder type and not by its name.  It can be
renamed, and an account made through a viewer in another language never
called it "Trash" in the first place.

## Making one

The position is written by the simulator out of where it believes this
avatar is at the instant the message lands, so for a walking avatar it
is not quite the place it was asked from.  What is printed afterwards
is read back out of the asset rather than assumed.

No folder is named on the way out.  The simulator files a new landmark
under Landmarks without being asked.  If the asset will not read, the
item is made anyway and the command says so.

A name is matched exactly, in the case it has, as every inventory
name is: `Example Workshop` and `example workshop` are two landmarks.
A name no landmark has but one has in another case is refused, and
the refusal names what it is near:

    landmark example workshop
    slsh: landmark: no landmark "example workshop"; did you mean "Example Workshop"?

A name is not made unique by anything.  Two landmarks called the same
thing are two landmarks called the same thing, and naming one of them
afterwards is refused with both listed.  Where they are in different
folders the whole path chooses between them; where they are in the same
folder the path is the same for both and the id beside each is the only
thing that tells them apart.  Making a landmark of a name that is
already taken says so at the time.

## Home is the one landmark nobody has to own

Going home is the same message with the null id in it, which the grid
reads as home rather than as an error.  It costs no inventory lookup
and needs no item.  `tp home` is the same trip said the short way.

Going home while standing at home is refused, and the refusal is the
one a landmark under your feet gets: `CouldntTPCloser`, the grid
describing its own arithmetic.  The line says what it usually means.

## Setting home

`landmark --set-home` makes where this avatar is standing the place
home is.  It is not a landmark and no landmark is involved: it is one
message that carries a position and the direction the avatar is
facing, and the region is not in it at all.  Whichever simulator
receives it is the region, which is why home can be set where the
avatar is and nowhere else.  To put home somewhere else, go there
first and then set it.

Home may be set on land this account controls and at a mainland
infohub.  Anywhere else it is refused, and both answers arrive the
same way -- as an alert, with no reply and no field that says home
moved.  So the grid's own sentence is what is printed, unchanged.
Measured on Agni on 2026-09-01, on two parcels a region apart:

    setting home to Pelmar Reach at 26, 66, 24
    Home position set.

and, a region away and on land this account does not control:

    setting home to Corvane at 44, 66, 24
    slsh: landmark: sl: the grid would not set home here: You can only set your 'Home Location' on your land or at a mainland Infohub.

The position printed is the one the message carried, and not a second
reading taken to print it.  The difference is real: an avatar that has
just teleported is still settling, and two readings a second apart were
eight metres apart when this was written.  What the line says is where
home was set, whether the grid then agreed or refused.

That is also why setting home the moment after arriving somewhere can
put it a metre or two above where the avatar comes to rest.  Home is
set where the avatar is, and just after a teleport that is still
falling.

Where home is cannot be read.  Nothing in the protocol answers the
question and nothing here keeps it, so the only way to find out is to
go there: `landmark --home`, and then `where`.  A home just set is
therefore worth checking by going to it if it matters, and that is
also the only way to see that a home set a month ago is still where
you think.

## Examples

    landmark
    landmark Example Workshop
    landmark /Landmarks/Example Workshop
    landmark --make Example Workshop
    landmark --go Example Workshop
    landmark --wait 60 --go Example Workshop
    landmark --home
    landmark --set-home

See also: `tp` for a place said in numbers, for `tp home`, and for what
a teleport costs and how it is refused, `where` for the position a
landmark is made from and the position home is set to, `parcel` for
what the land at the far end turns out to be, and for whether it is
land this account controls,
`ls` for the items themselves and the two ids each of them has, `mv`
for taking one back out of the trash, and `regions` for finding a place
by name when there is no landmark to it.
