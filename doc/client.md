# The client package, and what it used to do

The comments in `client/` say what the package does. This page is why,
where the why is a story: what the code did before, and what was wrong
with it.

## Counting what is dropped

A `Conn` counts what it throws away because nobody was reading fast
enough, and `OnDrop` says what each one was. It used to do neither: a
chat line that never arrived left no way for anybody to know one had
gone missing. A line lost there looked exactly like a line the script
never said, which for a benchmark is a number that is quietly wrong
rather than a run that failed -- and that is the one thing a
measurement must not do.

## The name a client gives slgod

`Name` is the basename of the running binary. Every client used to say
"slgo", which made the name useless for the one thing it is for:
slgod's own "in use by" message read "slgo, slgo" when two different
programs were attached.

## Closing while the relay runs

`Close` does not close the relay channels itself while there is a
`recvLoop`, because `recvLoop` is the only thing that sends on them, and
closing a channel under its sender is a race at best and a panic at
worst. That is what `Close` used to do, on the one path every client
takes to hang up.

`attach` says the relay is running under the lock, and refuses there a
connection already closed, so that `Close` either sees the loop about
to start and waits for it, or gets there first. No test reaches the refusal, and
none can through the package's own doors: `Close` shuts the transport
down, so an attach that got as far as the lock -- stream opened, first
packet read -- ran entirely before it. What is left is the window
between that read and the lock, which is exactly what the guard is for.
