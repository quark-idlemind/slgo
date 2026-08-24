`start` sets a script inside a rezzed object running.  `stop` is the
other direction, and the pair is the tick box a viewer's script editor
calls Running.  Neither has anything to say about inventory: a script
in a folder does not run and cannot be made to, because an object is
the only place a script runs at all.

    start lantern greeter

The object comes first and the script takes the rest of the line, as in
`drop`, so an object whose name has a space in it has to be quoted and
a script whose name has one need not be.  The object is named by the
word the region calls it or by its uuid.  With no script named, every
script in the object is started -- scripts only, since starting a
notecard is not a thing, and an object holding none says so.

## Options

**-w, --wait** *SECONDS*

How long to give the object to agree.  Without it, fifteen.

## One line for every script, and no stopping early

    started    greeter
    already    listener
    no answer  watcher

An object of six scripts is six separate requests and they need not
agree with each other, so nothing stops at the first one that will not
change: every script is asked and every script gets its line, in the
columns a listing uses with the name last.

`already` is a line and not an error.  A script that is running when
somebody asks for it to run is in the state they wanted; saying
`started` would be a claim about something that did not happen, and
failing would be a complaint about getting what was asked for.

## What `no answer` means

Nothing replies to the request that flips the switch, so what is waited
for is the object agreeing the script is running.  A script that never
agreed is not a script that failed.  The request went out and may yet
have taken effect; running the same command again says what the object
thinks now.  The count of those that did not agree is what the command
finally fails with, once the listing that explains it is already on the
screen -- or, where the object held one script, that script's name.

One kind of `no answer` never turns into anything else.  A script put
into an object with `drop` has been copied in and not compiled, so
there is no bytecode for the switch to turn on, and the object will not
agree that it is running however long it is asked.  Nothing here can
mend that, because the mending is to have put the script in with
`new --in`, which compiles it.

## Examples

    start lantern
    start lantern greeter

See also: `stop`, `new` for putting a script into an object compiled
and already running, `drop`, and `ls` for what an object holds.
