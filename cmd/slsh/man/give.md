`give` offers an inventory item to somebody.  It is the other end of
`accept`: what this sends arrives on their side as a question, and
what they send arrives here as one.

Two things are named, the person and the item, in that order.  The
person is a name, a key, or the number beside a name in the last
listing; the item is an inventory path, relative to the folder the
shell is in unless it begins with a slash, or the key of an item.

    give Example Resident Objects/lantern

The item's name is matched exactly, in the case it has, and has to
mean one thing.  Where a folder holds two items of one name, the path
is refused and the refusal lists their keys, since offering the first
of them would hand a person something nobody chose.  The key names
one; `ls -l` puts it beside each duplicate as well.

A person is resolved the way `im` resolves one: the session's cache is
asked, then whoever is standing in the region, and last the grid's own
search, which reaches somebody who is nowhere near.  What the search
finds is taken only when it is the whole name -- `Example Resident`, or
`example.resident` -- because it matches part of a name too, and a name
guessed at wrong offers an item to a stranger.  Anything less is listed,
numbered and refused rather than guessed at, and the number is what to
give this.

## An offer, not a transfer

Nothing moves until the other side accepts.  The report says "offered"
rather than "gave" because that is all that has happened: the offer
travels as a message to them, and their answer travels back as
another, printed on this side as a notice saying the offer was
accepted or declined.  Whether the item itself arrived is the part
nobody is told.

An offer nobody answers stays pending, and nothing is told to either
side.

## How much of the line is the person

A name has two words in it, so the person is the longest run of one or
two words at the front that answers to somebody, and the rest of the
line is the path.  Read as one word, a two-word name leaves its last
word at the front of the path, and what follows is either an item that
cannot be found or a different item, offered to the right person and
reported as a success.

Everything after the person is the path, spaces and all, so a name
with a space in it needs no quoting -- though quoting does no harm and
says plainly where the person ends and the item begins.

## A folder goes as a folder

A folder may be given, and arrives as a folder rather than as its
contents.

## Examples

    give Example Resident Objects/lantern

Quoting a path that has a space in it:

    give Another Resident "Notecards/build notes"

## A link is followed

An outfit folder holds links rather than items.  A link carries the
same name as the thing it points at, and a listing tells the two apart
only by the word `link` in the type column of `ls -l`, so a path taken
from `My Outfits` names a link almost every time.

`give` follows one to the item at the other end, which is what the id
it sends has to be: the grid has no object for a link's id and answers
an id it does not recognise with silence rather than with a refusal.

A link this inventory cannot follow is refused, naming the id it
looked for.  A link outlives what it pointed at, so an outfit put
together years ago may name things that have since been deleted.

See also: `accept` and `decline` for offers arriving, `offers` for the
ones waiting, `ls` and `cd` for finding the path, `lookup` or `who`
for finding the person, and `cp` for making a copy to give away rather
than the one being used.
