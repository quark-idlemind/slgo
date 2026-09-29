`logout` puts one of the daemon's sessions down and keeps it down.
The argument is a profile name out of the `agents` listing, the same
name `login` takes.  Flags come before the name.

    logout builder

The keeping down is the part worth knowing.  The daemon brings a
session back when it drops, which is what it is for, and it must not
do that to an avatar somebody has taken away on purpose -- so a
logout is remembered.  Nothing will bring that avatar back on its
own: it stays listed as stopped until `login` asks for it by name
and with `-f`, since a deliberate stop is exactly what `login` will
not undo casually.

The session also gives up its place in the queue, so the avatar a
bare command drives moves on to the next one up.  Logging it back in
later puts it at the end of that queue rather than back at the head.

Only a session the daemon is holding can be logged out.  A profile
listed as configured is one it has never been asked to bring up, so
naming it is answered with `no agent named ...` rather than with a
shrug.  There is nothing there to put down; `login` is what that
line is for.

Asking for one that is already down is not an error: it reports the
logout as though it had just done it, because from the caller's side
the wanted state has been reached either way.

## Options

**-f, --force**

Log out a session that has clients attached.  Without it a session
in use is left alone.  It overrules only that refusal.

## It refuses while somebody is using it

A session with clients attached is left alone and the refusal names
them: the programs that attached, under the names they authenticated
with, rather than a count of them.

A shell attached to the avatar it logs out is one of those clients, so
`slsh -a example -c 'logout example'` is refused by itself.  From
outside the shell, `slsh --logout example` attaches to nothing and is
refused only for somebody else; it is also how to put down the last
avatar the daemon has up.  `slsh --login example` brings it back.

## What happens to whoever is attached

With `-f`, every program attached to that avatar is let go, and told
why in the words an attach to it is refused with from then on:

    example is not connected (logged out); it will not come back on its own

A shell attached to the avatar being logged out -- this one, if it
is the avatar named -- stops there, saying `the session ended:` in
front of that line.  A shell attached to another avatar carries on.

A session the daemon loses and brings back by itself is different:
nothing attached to it is let go, and a shell on it carries on once
it is back.

## Nothing here is the way to end this shell

A shell attached to the daemon leaves the avatar logged in when it
quits; that is the arrangement, and logging out is a separate act
against a named avatar.  The other way round, a shell that logged in
for itself holds the session in this process, so there is no daemon
to ask and quitting is what logs the avatar out.  The line printed
at startup says which of the two is in force.

## Examples

    logout builder
    logout -f helper

See also: `login`, `agents`, `quit`, and `viewer` for handing a
session to a real viewer instead of taking it down.
