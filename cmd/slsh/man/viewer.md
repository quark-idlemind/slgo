`viewer` hands a running session to a real viewer: it says where the
daemon serves viewer logins and whether one has taken this session.
It is how to get eyes on an avatar the shell drives without logging
that avatar out and in again elsewhere.

    viewer

A shell that logged in for itself has no daemon serving viewer
logins, so there is nothing for it to hand over and it says so.

Serving viewer logins is not the daemon's default.  One that serves
none says so in words, and names the flag it would have to be
restarted with, rather than printing an empty address nobody could
tell from a working one.

Whether a viewer is there is mostly known: one that takes the session
announces itself, and one that logs out, as quitting a viewer does,
is no longer counted.  One that goes without logging out -- a crash,
a lost connection -- says nothing, and is shown as there until it has
been silent for 100 seconds, the viewer's own circuit timeout, or
another viewer takes the session or slgod restarts.

## Options

**-l, --launch**

Start a viewer and log it in as this avatar.  Without it, the address
is printed and nothing is started.

## The password is minted, used once, and expires

A profile keeps only a digest of a password, so nothing on this side
can know one a viewer could be told to type.  The daemon mints a
fresh one, good for a single login and for a few minutes, and it
goes straight to the viewer being started.  Nothing here prints,
logs or keeps it; the command line is echoed with it struck out,
since a launch that appears to do nothing is unreadable.

A profile with no `viewer_password` line in it is refused rather
than minted for.  That line is not one a profile has unless somebody
put it there: it is what marks a profile as one that may be handed
to a viewer at all.  The refusal names the line to add and says
slgod has to be restarted after it.

It does reach that viewer's arguments, where any process this user
owns can read them.  Single use is the answer to it: an onlooker who
copies the password out is racing a viewer already logging in with
it, and loses the moment it does.  The expiry is the second line of
defence, and generous, because a viewer takes the better part of a
minute to reach a login screen.

## The viewer has to be one that can be told about another grid

A viewer built for the main grid may have the part that reads its
own list of grids compiled out, and then cannot be pointed at a
private grid at all: it falls back to a main grid login screen, with
nothing on it to say why.  The build to have is the one made for
OpenSim grids, which does read that list.

The grid has to be added to the viewer once, by hand, before any of
this works: its preferences, the login URI this command prints, and
a nickname.  What a viewer takes on its command line is that
nickname and not a URI; handed the URI it answers `unknown grid` and
goes to the main grid again.

Four settings carry the local half: `viewer_app` names the
application, `viewer_grid` that nickname, `viewer_launch` the
command that starts it, and `viewer_running` the command that says
whether one is up already.  `set` is where they are changed.

A viewer already running is refused rather than launched: raising an
application that is up does not pass it the arguments again, so a
second launch would log nobody in and report success all the same.

## Examples

    viewer
    viewer --launch

See also: `agents`, `status`, `set` for the viewer settings, and
`logout` for the other way to free an avatar up.
