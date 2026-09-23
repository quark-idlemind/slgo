`decline` refuses an offer of friendship, or an item somebody has
handed over, and tells them so.  It is `accept` answered the other
way, and it names what it means in exactly the same fashion.

With nothing after it, it refuses the only offer waiting.  With
something after it, that names one: part of the name of whoever
offered friendship, or part of the name of the item offered, or the
whole name of whoever sent it.  Where one of each is waiting the bare
command means the friendship offer, and the item has to be named.

    decline
    decline lantern

The traps are `accept`'s traps, since it is the same picking: what is
typed is tried against the items first, so a word that could be either
takes the item; and a word that matches two of anything is refused
with a count, so nothing is answered by accident.

## Saying no is not the same as saying nothing

The refusal is a message, and it has to be sent or the offer stays
pending on the other side with nobody told anything.  That is the
difference between this and simply leaving an offer alone.

If another client on the same avatar -- slbotd, another shell -- has
already answered the offer, nothing is sent: slgod says who answered
and how, and that is what is reported.

The person is told which way it went.  An item declined does not
arrive, and a friendship declined is not formed; neither can be undone
from here, and the other side is free to offer again.

## Examples

The only offer waiting:

    decline

One of several, by who sent it or what it is called:

    decline Another
    decline lantern

See also: `accept`, `offers`, and the pair of `no` and `ignore` --
`no` is this same refusal reached by the number `waiting` prints, and
it answers these two kinds along with teleports, permissions and group
invitations, while `ignore` leaves the offer waiting and says nothing
to anybody.
