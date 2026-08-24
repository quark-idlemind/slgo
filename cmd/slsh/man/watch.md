`watch` prints grid messages as they arrive, decoded as far as this
build can decode them.  Where `status` counts what has crossed the
circuit, this shows it.

    watch ChatFromSimulator

The arguments are message names as the protocol spells them --
`ChatFromSimulator`, `ObjectUpdate` -- and naming none means
everything.  A name this build has never heard of is subscribed to
rather than refused, since the daemon may know a message the shell
does not, and nothing arriving under it is then the answer.

It runs for half a minute, and prints how many messages it saw.

A shell that logged in for itself has no daemon to relay from, so
this is one of the few things such a shell cannot do at all and it
says so rather than showing an empty watch.

## Options

**-t, --for** *D*

How long to watch.  Without it, half a minute.  *D* is a length of
time and not a number of seconds, so it has to carry a unit:
`-t 30s`, `-t 5m`.  A bare `-t 30` is refused with
`time: missing unit in duration`.

## What will not appear

Only what comes over the circuit.  A good deal now arrives on the
grid's event queue instead -- LLSD bodies rather than the binary
message encoding -- and `watch` reads only the message half of it.
A name that has moved to the queue subscribes cleanly and still
shows nothing.

A message the template does not describe is still reported, as its
number and a byte count, and one that will not decode is reported as
undecodable.  Both are worth seeing: an unknown number is what a
protocol change looks like from here, and silence would hide it.

## Examples

    watch ChatFromSimulator
    watch -t 5m ObjectUpdate ImprovedTerseObjectUpdate > updates

See also: `status`, and `agents` for which session is being watched.
