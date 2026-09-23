`offers` lists the friendship and inventory offers waiting for an
answer.  `accept` and `decline` are what answer them, and `waiting` is
the listing that has these in it along with everything else somebody
can be asked.

It takes nothing.  Both kinds are in one list.  Each line begins with
what it is, and the two kinds fill the rest of the line differently:

    friendship  Example Resident             12s ago
    object      a lantern                    4s ago   from Another Resident

A friendship offer has no name of its own, so the second column is
whoever made it; an item has one, so the second column is what it was
called and the sender comes at the end.  The first column of an item
is the kind -- object, notecard, script -- rather than the word
inventory.

Nothing is answered by being looked at.  An offer nobody answers
simply stays pending, and the other side is told nothing either way.

## What arrived before this shell

slgod keeps every friendship and item offer made to the avatar until
it is answered, whether or not any client is attached, and a shell is
handed what is still waiting when it starts.  Those are listed with the
rest, marked `(from before this shell)`.

An empty list says what it is an account of: since when slgod has been
keeping them, or, against a slgod too old to keep a record, that
anything offered before this shell started is not known here -- and it
is still open on the other side, waiting, with nothing here to show it.

The grid cannot be asked for an offer a second time.  The id that
answers it arrives once, so what is listed is what slgod or this shell
caught.

## Two shells on one avatar

Both are handed the offer, and the first to answer it is the only one
that does.  The other drops it from its list the moment slgod says so,
with a line saying who answered and how, and an `accept` or `decline`
typed there a moment too late is refused rather than sent twice.

## Examples

    offers

Answer one of them by name:

    offers
    accept lantern

See also: `accept` and `decline` for answering, `waiting` for the same
offers alongside teleports, dialogs, permissions and group
invitations, and `offer` or `give` for the ones going the other way.
