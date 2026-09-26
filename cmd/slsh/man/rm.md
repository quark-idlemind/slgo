`rm` deletes inventory, permanently and at once.  Nothing here moves
anything to the trash: what `emptytrash` disposes of is what a
viewer put there, and what `rm` names is simply gone.

    rm Notecards/README

The arguments are paths, relative to the folder the shell is in, or
uuids.  Several may be given on one line, and a folder goes with
everything inside it.

## Options

**--newest**

Of several of a name, delete the one acquired last, by the date
`ls -l` prints.  What went is named on the way out, with its date
and its id.

**--oldest**

Of several of a name, delete the one acquired first.

**--remove-all-copies**

Delete every one of the name.  It is the whole words; no shorter
spelling of it is accepted.  The count printed at the end is the
only evidence it did what was wanted, and is printed only when the
name meant more than one thing.

**--in** *OBJECT*

Delete from inside a rezzed object rather than from inventory.  It
is a delete and not a take: the copy is gone, and anything wanted
back has to come from the original in inventory.  `--newest` and
`--oldest` do not apply with it: what an object holds has no dates
on it.  Nor does `--remove-all-copies`: an object renames a second
item of one name as it goes in, so there is only ever one.  What
follows is a name, matched exactly and in the case it has.  The
paths and the uuids above are inventory's.  The id beside a line of
`ls -l --in` is not something to hand back to this.

## One name can mean a dozen items

Inventory names are not unique, so a path may name several things.
A plain `rm` refuses that and deletes nothing.  A name is matched
exactly, in the case it has: `Greeter` beside `greeter` is another
name, not another copy, and `--remove-all-copies` leaves it alone.

    /Scripts$ rm greeter
    slsh: rm: "greeter" is 3 things here: say --newest or --oldest
    to delete one of them, --remove-all-copies for all 3, or name
    one by its id, which ls -l prints beside the date

`ls -l greeter` is what to look at then: a path that names items
lists those items, one line each, with the dates and the ids that
tell them apart.

`--newest`, `--oldest` and `--remove-all-copies` are one choice
rather than three options.  Any two of them together is a line that
means two things, and is refused.

## When a date does not settle it

`--newest` and `--oldest` refuse rather than guess in two cases.
Inventory dates are whole seconds, so several items can be stamped
alike, and there is no sense in which one of those is the newer.
The other case is something with no date at all -- a folder has
none -- and undated is not the same as old.

Both refusals point at the id.  The other end of the pile may still
be unambiguous: two tied at the newest date does not stop `--oldest`
from having an answer.

## The star, which is not a glob

A path of one star means every item in the folder the shell is in,
and it is refused unless `--remove-all-copies` is given as well.
There is no other pattern.  Folders are never included in it:
emptying a folder of its items is a thing to want, and taking its
subfolders with them is not.

An item may itself be named with a star.  Such an item can no longer
be named at a prompt; its id still names it.

## Hundreds of removals take minutes

Each one is a round trip to the grid.  A count climbs in place while
it works, and it goes to the terminal rather than to the output: a
line redirected to a file catches the result and not a flickering
counter.

## Examples

    rm Notecards/README
    rm --newest greeter
    rm --oldest greeter
    rm --remove-all-copies greeter
    rm --remove-all-copies *
    rm d8467e57-...
    rm --in lantern hello.lsl

## A link is not followed

A link is an inventory entry in its own right, and `rm` acts on the
entry.  Deleting a link deletes the link, and leaves the item it
pointed at alone.  An item may have links to it from several outfits,
and following one here would reach past all of them to the item they
share.

See also: `emptytrash`, `mv`, `ls`, and `drop` for putting something
into an object that `rm` can take out again.
