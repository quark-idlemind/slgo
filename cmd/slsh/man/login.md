`login` asks the daemon to bring an avatar up.  The argument is a
profile name from the first column of `agents`, not an avatar's name
and not a uuid.  Flags come before the name.

    login builder

It has to be named.  Logging an avatar in puts it in the world -- an
arrival, a presence, a notice to whoever watches for that name -- so
there is no default.

Asking twice is safe.  An avatar already up is reported as already
up and nothing else happens: logging it in again would kick the
session it has.  Two shells asking at the same moment produce one
login between them for the same reason.

## Options

**-f, --force**

Accepted for symmetry with `logout`, and changes nothing here: naming
an avatar IS asking for it back, so `login` always asks.  It does not
overrule a login the daemon has already refused for a failed attempt;
the wait printed with that refusal is still the wait.

## A session that is down and staying down

Something stopped it, and the likeliest something is a person now
using that avatar in a viewer.  `login` starts it anyway, because
typing the name is the deliberate act of asking for it back -- what
the daemon refuses unasked is a DAEMON retrying, which would undo the
logout by itself.  slbotd asks without forcing for exactly that reason
and is exactly what should be refused.

So read the reason before typing it.  The check the refusal asks for
was never one the daemon could make: a stopped session has no clients,
so nobody here can be using it, and what it warns about is somebody
logged in as that avatar in their own viewer -- which slgod cannot see
and no flag here could have checked.

The refusal says why it went down rather than when, and that is the
part worth reading before overruling.  `logged out` is somebody
having asked for it here.  Anything beginning `ended by the grid` is
the avatar having been thrown off, and the grid's own words follow;
the commonest cause of that is the avatar being logged in somewhere
else, which means `login` would take it back off whoever has it.

A login that was refused is a different case.  The daemon remembers
the refusal and will not try again for a while, and says how long.
A login server throttles whatever hammers it, and the throttling then
arrives as a quite different failure, so the wait is the difference
between one clear error and an afternoon of confusing ones.

## What the daemon has to have been told

Only a daemon started with profiles to log in can start one on
demand.  One that was given no way to do it says so rather than
failing obscurely, and a name it has never heard of is an error and
not a silence.  A shell that logged in for itself has no daemon to
ask at all.

## Examples

    login builder
    login -f helper

See also: `logout`, `agents`, `status`, and `viewer` for putting a
real viewer on the session once it is up.
