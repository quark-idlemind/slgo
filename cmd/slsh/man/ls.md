`ls` lists a folder.  With no path it lists the folder the shell is
in; with one it lists that folder instead, relative to here unless
the path starts from the root.

    ls -l Objects

A path that is not a folder lists what it names.  A name can mean
several things, and this is how to see them.

By default it prints one bare path per line.  A listing written to a
file is then a list of paths, which can be edited into commands and
run.

## Options

**-l**

The columns: kind, when it was acquired, id and path.  Without it,
one bare path per line.

**-L**

The same columns, with a link shown as the item it points at: its
kind, its date and its id, under the path where the link was found.
Implies `-l`.

An outfit folder holds nothing but links, every one named after the
item at the far end, so `-l` there gives a column of `link` and a
column of ids that name nothing outside this inventory.  `-L` turns
the same listing into what is actually worn:

    ls -L "/Current Outfit"
    object     2025-04-14T17:44:09 cfb57e57-...  /Current Outfit/a dress
    bodypart   2025-04-14T18:01:16 28d37e57-...  /Current Outfit/a shape

That is also the way to see clothing and body parts as against
attachments, which `worn` cannot show: `worn` lists objects on
attachment points, and a skin or a shape is not one.

A link that cannot be followed keeps the word `link`, which says what
happened -- this is a link and following it got nowhere.  The id
column is the id it points at either way, since the link carries that
already.

Following costs one walk of inventory for the whole listing, and only
where the listing holds a link at all.

**-r**

Descend four levels rather than listing only this folder.  Deeper
than that is not listed and is not reported as missing.

**-t**

Newest first, in a flat order rather than a tree one.  Grouping by
folder would bury a thing made a minute ago under whichever folder
it lives in.  Things made in the same second sort by name.  A folder
has no date and sorts as the oldest thing there is, which puts
folders at the end.

**--in** *OBJECT*

List what a rezzed object holds instead of a folder.  Takes no path:
an object holds no folders.  A line is the kind and the name, and
with `-l` the kind, the id and the name.  There is no date, and no
path.

## The columns

With `-l` a line is kind, when it was acquired, id, path, in that
order and padded to fixed widths.

    notecard   2026-08-03T21:26:43 d8467e57-...  /Notecards/readme

The date is one field, down to the second, with a `T` rather than a
space, so a listing is four fields and the id stays the third.  Two
items of one name, acquired a minute apart, are told apart by this
column and by the id.  `rm --newest` and `--oldest` pick between
them by exactly this number, so a listing is how to see what one of
those is about to take.

A folder has no date and gets a dash, so the columns after it do not
move.

## Listing an item

    ls -l /Scripts/greeter
    script     2026-08-07T12:06:57 cb567e57-...  /Scripts/greeter
    script     2026-08-07T11:26:01 e9d97e57-...  /Scripts/greeter

Several things of one name, one line each.  The dates are what
`--newest` and `--oldest` choose by, and the ids are what names one
of them exactly.  A uuid works as the path too, and prints the one
line it names.

Without `-l` the same listing is the same path several times over,
which is honest and says nothing about which is which.

A folder wins where a folder and an item share a name, as `cd` does;
the id names the item.  A path that is neither is reported as the
name it is rather than as the folder it was tried as first.

Inventory names are not unique.  A path names the first of them.
The id names exactly one, and `cat`, `rm`, `mv`, `drop` and the rest
take an id anywhere they take a path.  `get` looks like it belongs
on that list and does not: the uuid it takes is a texture's asset
id, which is not what this column prints.

## Inside a rezzed object

The ids in a `-l` listing `--in` an object belong to the object's
own copies, not to the inventory items they came from, and they are
there to be read rather than typed back.  `rm --in` and `mv --in`
take the name instead, matched exactly and in the case it has.

## Examples

    ls -l Objects
    ls -l /Scripts/greeter
    ls -lt d8467e57-...
    ls -rt
    ls --in lantern

See also: `cd`, `find`, `cat`, `get`, `rm`, and `drop` for putting
something into the object `--in` lists.
