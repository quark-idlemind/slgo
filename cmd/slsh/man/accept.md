`accept` says yes to an offer of friendship, or to an item somebody
has handed over.  `decline` is the same question answered the other
way, and both of them are what `offers` lists.

With nothing after it, it takes the only offer waiting.  With
something after it, that names one: part of the name of whoever
offered friendship, or part of the name of the item offered, or the
whole name of whoever sent the item.

A person's name, whole or part, is matched without regard to case, and
so is part of an item's name, since that is a search.  The whole of an
item's name is matched in the case it has.  An item offered as
`A Big Box` is not what `accept a big box` names, and is not found as
part of it either: the refusal names what it is near.

    accept
    accept lantern

What is typed is tried against the items first and only then against
the friendship offers, so a word that could be either takes the item.

Naming settles an ambiguity, and a name that does not settle one is
refused rather than guessed at: a word matching two friendship offers,
or two of the items, says how many it matched and answers neither.  The
whole of a name beats part of another, so an item called "a lamp" is
still reachable while "a lamp stand" is on offer beside it.  Two items
with the same name from the same person is the one ambiguity no name
will settle, and `waiting` is the way through -- it numbers them, and
`answer N` takes the one meant.

## An item lands in the folder the shell is in

`cd Objects` then `accept` puts it in Objects.  Answering the same
offer through `waiting` and `answer` instead puts it in the Objects
folder, since a number typed at a listing says nothing about where
anything should go.

A shell standing in a folder that has since been renamed or removed
has nowhere to accept into, and says so rather than letting the grid
choose.

## Which kind a bare command means

Where one of each is waiting, the bare command takes the friendship
offer; the item has to be named.

Where only items are waiting, the bare command means them: the only
one is taken, and two or more are refused with a count.  Naming one of
the two is what to do next.

## When another client got there first

slbotd, or another shell on the same avatar, may have answered the
offer already.  slgod is asked before anything is sent, and if it
says so the answer is refused with who answered and how, and nothing
goes -- the offer has left the list by then either way.

## What the report does not say

Whether the item actually arrived.  The grid does the moving and says
nothing about it, so the report means the answer was sent, and looking
in the folder afterwards is what says it is there.

Accepting a friendship also records it here.  The grid tells the side
that offered and tells the side that accepted nothing, so without that
record the friend list would stay wrong until the next login.

## Examples

The only offer waiting:

    accept

One of several, by who sent it or what it is called:

    accept Example
    cd Objects
    accept lantern

See also: `decline`, `offers`, `waiting` and `answer` for the same
offers alongside everything else that wants an answer, and `ls` for
looking at what arrived.
