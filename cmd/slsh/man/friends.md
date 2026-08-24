friends lists the people this avatar is friends with who are logged in.
It is the narrow list on purpose: a friend list of four hundred people,
of whom two are online, answers a question nobody asked.

    friends

Each line is a number, a name and a key, and the listing is what the
next command means by a number -- `friends` then `im 2` is the ordinary
way round, and the same number works for `profile`, `offer` or `give`.

A word after it is dropped rather than refused: `friends someone`
prints the whole online list, exactly as `friends` does, because there
is nothing here to search.  The search is `lookup`, and it goes to the
grid rather than to this list.

## What the list is, and what it is not

The list is the session's, kept from login and updated as friends come
and go and as friendships form.  It is not asked of the grid each time
this command is typed.

Online is what the other side granted this avatar the right to see, and
no more than that.  It is not a promise that somebody is awake or
willing to be written to.

## Options

**-a**

List everybody rather than the ones online.  Without it, only whoever
is online.  The whole list is the one to use when the question is who
somebody has ever befriended, or when a name is wanted for a key.

## Examples

    friends
    friends -a

Write to the second person listed:

    friends
    im 2 are you still at the build

See also: `offer` for making a friendship, `offers` for one waiting to
be answered, `who` for everybody in the region whether or not they are
friends, and `lookup` for everybody else.
