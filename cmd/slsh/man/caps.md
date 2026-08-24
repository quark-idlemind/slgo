`caps` lists the capabilities the simulator granted this session.  A
capability is an HTTP service the session may use alongside the
message circuit -- fetching inventory, uploading a script, searching
for somebody by part of their name, asking what the region supports
-- and what is on this list decides which of those are possible at
all.

    caps

With a word after it, only the capabilities whose names contain that
word are printed, matched without regard to case.  `status` counts
the same list without naming it.

## What is on the list and what is missing from it

The list is the answer to one question asked at login: the session
names the capabilities it wants, and the simulator replies with the
ones it serves.  A name that is absent is absent for one of two
reasons that look identical here -- either nothing asked for it, or
the region does not offer it -- and asking for one that does not
exist is harmless, because the reply simply omits it.

That makes this the first place to look when a command fails in a
way that has nothing to do with permissions or with the object it
names.  Whole paths through the protocol are shut when their
capability is not there, and what the simulator says at that point
is usually about something else.

What is printed is the names alone.  Each one is served by a URL,
and the URL stays with whatever holds the session; nothing about it
would be usable from here.

## It describes this session where it is standing now

Capabilities are granted by the simulator, to one session, for one
region.  Two avatars in the same region can have different lists,
the same avatar logged in again may too, and a listing saved from an
earlier session says what was true then.

A teleport replaces them.  The URLs served the region the avatar
left, so they are dropped on arrival and the whole list is asked for
again, from the new region's own seed.  What is printed after a
teleport is the new simulator's answer rather than a carried-over
one, which is a change nothing on the screen shows: the names may
look the same.  It is the URLs behind them that moved.

## Examples

    caps
    caps inventory

See also: `features` for what the simulator says it supports, `lsl`
for the language it implements, and `status`.
