agents lists what the daemon has: a line for every session it is
holding, and a line for every profile it could bring up and has not.
It is the listing login and logout act on, and the name in the first
column is the name those two take -- the profile the daemon hosts an
avatar under, which is not the avatar's own name beside it.

The order is the daemon's and is not alphabetical.  Everything the
daemon is holding a session for comes first, longest held first, and a
session that was stopped goes to the end of that group rather than
keeping its place in it; then every other profile the daemon knows of
and holds no session for, in the order its own list of them gives.  A
star marks the first session the daemon still holds, which is the one a
command that names no avatar drives, and a session this shell is
attached to says so at the end of its line.  The two are often the same
and do not have to be.

## What stands where the region would

A session that is up shows the region its avatar is in, and there is no
word for that state in the listing: the region is the word.  Anything
else shows its state instead, with the reason after it where there is
one:

    connecting  a login in flight, or a session being brought back
    configured  a profile exists and nothing has asked for it
    stopped     down and staying down; the reason says which sort
    failed      a login was tried and refused; the detail says how

"connecting" is two situations wearing one word and they are not the
same news.  One is a login that has not finished, and the reason reads
"logging in".  The other is a session the daemon holds and has lost the
circuit to, which its supervisor is already reconnecting; that one has
the error as its reason, and it is not lost -- waiting is the right
thing to do about either.

"stopped" is three.  Either something asked for it -- logout -- or the
grid threw the avatar off, which is what happens when somebody logs that
avatar into a viewer, and the daemon then stays down rather than fight
them for it; or the session fell over with something the daemon will not
retry, and stays down for that instead of reconnecting.  The reason
tells them apart: "logged out", the grid's own words after "ended by
the grid", and, for the third, the error itself with no phrase in front
of it.

Configured, stopped and failed are what the listing is for.  Told only
whether a session is connected, a profile nobody has ever started and
an avatar somebody logged out an hour ago look identical -- and the
second is not a fault at all, it is somebody using that avatar in a
viewer.

## Why the default sits on the oldest session

The daemon's default moves only when the session it is on goes away.
Bringing another avatar up does not move it, attaching does not move
it, and a session that drops and is reconnected keeps its place -- the
place belongs to the session the daemon holds and not to the connection
under it, which is the one that goes.  Nothing done casually can change
which avatar a bare command drives, which is the point: a benchmark run
against the wrong avatar is not an error, it is a plausible number.  An
avatar that has been logged out gives up its place, so logging it back
in puts it at the end of the queue rather than at the head of it.

The star is drawn from that same rule and not from what a line happens
to look like, so it stays where the default is.  A session shown as
connecting keeps the star -- a reconnection never cost it its place,
and a bare command goes to it and waits -- and a profile the daemon
holds no session for is never starred, however far up the listing it
is, since there is nothing there to send anything to.  When every
session the daemon holds has been stopped there is no star at all,
which is the daemon's own answer to the question: it has nothing to
give a command that names no avatar.

A shell that logged in for itself has no daemon to ask, and says so
with the one avatar it has.

## Examples

    agents

See also: login, logout, status for how one session's circuit is doing,
and viewer for handing one over to a real viewer.
