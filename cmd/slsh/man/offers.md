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

## It can only list what arrived while this shell was here

Offers reach the client that is attached at the moment they
arrive.  Anything offered before this shell started is not in this
list -- and it is still open on the other side: they are waiting, and
nothing here shows it.

The same follows for two shells on one avatar.  Both hold the offer,
and each holds its own copy: answering in one takes it out of that
one's list and leaves the other's alone for as long as that shell
lives.  Looking again does not help -- the list is what this process
caught, not something the grid can be asked for a second time.

A friendship offer can only ever be answered by the client that
received it.  An offer printed and forgotten is an offer that can
never be accepted.

## Examples

    offers

Answer one of them by name:

    offers
    accept lantern

See also: `accept` and `decline` for answering, `waiting` for the same
offers alongside teleports, dialogs, permissions and group
invitations, and `offer` or `give` for the ones going the other way.
