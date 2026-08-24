`agents` is the listing `login` and `logout` name something out of.
The first column is the profile -- the name those two take -- not the
avatar's own name beside it.

    agents

Sessions the daemon holds come first, oldest first.  A session that
was stopped sits at the end of that group rather than keeping its
place.  Profiles the daemon knows of but is not holding follow, in
the order it lists them.  The order is not alphabetical.

A star marks the session a command that names no avatar drives: the
first one the daemon still holds.  A session this shell is attached
to says so at the end of its line.  The two are often the same and
do not have to be.

A shell that logged in for itself has no daemon to ask, and prints
the one avatar it has.

## What stands where the region would

A session that is up shows the region its avatar is in.  Anything
else shows its state instead, with the reason after it where there
is one:

    connecting  a login in flight, or a session being brought back
    configured  a profile exists and nothing has asked for it
    stopped     down and staying down; the reason says which sort
    failed      a login was tried and refused; the detail says how

`connecting` is two situations.  A login that has not finished
reads `logging in`.  A session the daemon holds and has lost the
circuit to shows the error as its reason; the supervisor is already
reconnecting, so waiting is the right thing to do about either.

`stopped` is three.  `logged out` is somebody having asked here.
Anything beginning `ended by the grid` is the avatar having been
thrown off -- commonly because it is logged in somewhere else --
and the grid's own words follow.  The third is an error the daemon
will not retry, with no phrase in front of it.

Configured, stopped and failed are what the listing is for.  Told
only whether a session is connected, a profile nobody has ever
started and an avatar somebody logged out an hour ago look identical
-- and the second is not a fault, it is somebody using that avatar
in a viewer.

## The star

The default moves only when the session it is on goes away.
Bringing another avatar up does not move it, attaching does not
move it, and a session that drops and is reconnected keeps its
place.  An avatar that has been logged out gives up its place, so
logging it back in puts it at the end of the queue rather than at
the head.

A session shown as connecting keeps the star -- a reconnection never
cost it its place, and a bare command goes to it and waits.  A
profile the daemon holds no session for is never starred.  When
every session has been stopped there is no star at all.

## Examples

    agents

See also: `login`, `logout`, `status` for how one session's circuit
is doing, and `viewer` for handing one over to a real viewer.
