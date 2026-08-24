`ignore` sets one of the things `waiting` lists aside: it stays
waiting, and the prompt stops counting it.

Nothing is said to whoever asked.  That is the whole of the difference
between `ignore` and `no` -- `no` sends a refusal and ends it,
`ignore` is this shell's own note that this one is not being dealt
with now.  A script asking for a permission that is not wanted, a
dialog from an object nobody is using: the count at the prompt is only
useful while it means things not yet looked at, and one of these left
in it makes the count worth nothing.

    ignore 4
    waiting -a

It is this shell's, not the session's.  Another client attached to the
same avatar has its own idea of what it has dealt with, and ignoring
something here does not reach it.

`waiting -a` lists what has been set aside.  Answering or declining
one takes it out of the set again, and out of the listing in the same
breath.

See also: `waiting`, `answer`, `no`.
