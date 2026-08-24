`stop` takes a script inside a rezzed object out of the running
state.  `start` is the other direction, and the two of them are one
tick box in a viewer's script editor rather than two operations.

    stop lantern greeter

The object comes first and the script takes the rest of the line, as in
`drop`, so an object whose name has a space in it has to be quoted and
a script whose name has one need not be.  With no script named, every
script in the object is stopped; an object holding none says so, and
`ls --in` lists what it does hold.

## Options

**-w, --wait** *SECONDS*

How long to give the object to agree.  Without it, fifteen.

## Stopping is not removing

A stopped script stays where it is, in the object's contents, and
appears in the listing exactly as before.  Getting it out is
`rm --in OBJECT`, and that copy is then gone: anything wanted back has
to come from the original in inventory.

## The listing, and the answers in it

One line per script, in the columns a listing uses:

    stopped    greeter
    already    listener
    no answer  watcher

Every script is asked, and nothing stops at the first one that will not
change, since the requests are separate and need not agree.  `already`
is not an error -- a script that is not running when somebody asks for
it to be stopped is in the state they wanted.

`no answer` is not a failure either.  Nothing replies to the request
itself, so the object is asked again until the wait runs out.  The
request went out and may yet have taken effect, and running the command
again says what the object thinks now.  The count of those that did not
agree is what the command fails with at the end, under the listing that
explains it -- or the one script's name, where the object held only one
of them.

## Examples

    stop lantern
    stop lantern greeter

See also: `start`, `ls` for what an object holds, `rm` for taking a
script out of it, and `new` for putting one in and starting it.
