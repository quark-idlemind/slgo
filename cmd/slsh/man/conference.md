`conference` takes part in an instant message with several people in it:
starts one, invites more people into it, speaks in it, and leaves it.
With nothing after it, it lists the conferences this shell knows of.

    conference
    conference start Example Resident, Another Resident
    conference from-im Example Resident, Third Resident
    conference add 1 Third Resident
    conference say 1 shall we start?
    conference join 2
    conference leave 1

## People

A conference has several people, so they are separated by commas, and
each is read as `im` reads a person: by name, by key, or by the number
beside a name in the last listing of people.  A name is matched ignoring
case, and one that could be several people is refused with all of them
named.  This avatar is in every conference it starts and is not named.

## Starting

`conference start WHO, WHO ...` starts a conference with two people or
more, and waits for the grid to say it has, up to half a minute.  It is
called "Multi-person chat", as the viewer calls it, and numbered in the
listing.  Every conference this avatar starts has that name, which is why
they are picked by number.  Starting one with the people of a conference
already held gives that one back and sends nothing, as the viewer does.

`conference from-im WHO, WHO ...` is what adding somebody to an instant
message does in the viewer: the first person is the one the message is
with, and the message is left before the conference is started with them
and the others, so one that fails to start leaves the message all the
same.  The message stays in `talk`; it is only left as far as the grid is
concerned.

## Adding, saying, leaving

`conference add N WHO, WHO ...` invites people into conference N.  People
already in it or already invited are left out.  The grid does not answer an
invitation; it says when somebody comes in, and that is printed.

`conference say N TEXT` speaks in it.  Text over 1023 bytes is sent as
several messages.  A conference this avatar has not joined is refused.

`conference leave N` leaves it, and is also how an invitation is refused.
Nothing answers a leave, so it says what was sent.

There is no way to remove somebody from a conference, because the viewer
has none: the moderator actions it has are for a group's chat, and what
it calls ejecting is ejecting from the group.  None is built.

## Being invited

Nothing joins on its own.  When somebody invites this avatar into a
conference, or speaks in one it was invited into, the line is printed
once, and a notice says how to join or refuse it:

    12:01:40 < Example Resident in [Conference] Multi-person chat #2: are you there?
    12:01:40 * Example Resident invites this avatar into [Conference] Multi-person chat #2 -- "conference join 2" joins it, "conference leave 2" refuses

`conference join N` accepts, and prints who is in it.  What the viewer does
is answer at once, and this departs from it, on the same instruction as
group chat: an avatar somebody can invite into a conference with a line
would otherwise be joined by anybody.

## What is printed

What is said in a conference this shell is in, and who comes and goes, is
printed as it arrives.  The speaker is first and the conference after it,
behind its label like any name that is not a person's, and with its
number:

    12:03:12 < Another Resident in [Conference] Multi-person chat #1: yes, now
    12:03:40 * Third Resident came into [Conference] Multi-person chat #1

The listing shows each conference with its state, `joined`, `invited` or
`left`, and the people the grid or this shell knows of.  A conference left
stays listed, so the numbers do not move.

What is sent is read from the viewer's source and has not been measured on
a grid: doc/conference.md says which is which, and what is not known.

## Examples

    conference start Example Resident, Another Resident
    conference say 1 good evening, both
    conference add 1 Third Resident
    conference leave 1

See also: `im` for one person, `group chat` for a group's, `talk` for the
conversations chat mode cycles between, and `lookup` for finding whoever
is meant.
