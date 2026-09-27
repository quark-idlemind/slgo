# slrun, and why it does what it does

The comments in `cmd/slrun/` say what the program does. This page is why,
where the why is a story: what was watched, what the program did before,
and what was wrong with it.

How to use it is the [guide](guide.md#slrun). Why the objects it runs in
are shared the way they are is [slots.md](slots.md).

## An interrupt stops the run

`signal.NotifyContext` was in `run` before anything looked at what it
produced, which is worse than not having it: installing a handler takes
away the default, so ^C went from killing slrun to doing nothing
whatsoever. Watched happening -- a script sleeping sixty seconds, an
interrupt, and slrun carried on to the end of it.

What ^C does now is end a wait for somewhere to run, stop handing out
scripts and cancel the ones in flight, and then the deferred cleanup
gives the objects back. The cleanup does not run on the run's context:
`session.RunIn`'s cleanup deletes a rezzed prim on a context of its own,
because the run's may well be why it is going, and an interrupt is
exactly that.

## One line saying interrupted

^C reaches every script at once, and each of them ended by saying
"context canceled" under its own name -- including the ones that had not
started, which said it having done nothing at all. Six scripts
interrupted printed six lines of it and no line saying what had
happened. `main` says "interrupted", once, and `quiet` is what keeps the
six quiet.

`quiet` knows two shapes of it, because a run reaches the grid through
gRPC and a cancelled call comes back as a status rather than as the
context's own error: `rpc error: code = Canceled desc = context
canceled` does not satisfy `errors.Is` against `context.Canceled`. That
was found by the test, `TestInterruptingARunSaysSoOnceAndFails` in
`cmd/slrun/daemon_test.go`, which is the reason it drives a real
interrupt through a daemon rather than cancelling a context and assuming
the rest.

## A name on every line, and none on one script

Every line is printed with the script it came from in front of it, and
the names are lined up: with several running at once the tags are a
column the eye follows down the page rather than a word at the start of
each line.

One script is the exception, because there is nothing to tell it apart
from. The name would then be the same word on the front of every line of
the only output there is, which is the shape of thing a person reads past
rather than reads. `-v` asks for it anyway -- worth having when the
output is being kept, or pasted somewhere that will not say where it came
from.

## Which avatar the objects came from

The line saying which avatar's objects a run used was unconditional, and
the ordinary run of slrun is a script and its output: a line about whose
objects it borrowed arrived in the middle of that, on every run, saying
something that changes nothing about what the script printed.

It is the second thing `-v` buys rather than the first, because the name
in front of the lines is about reading the output and this is about how
the run was arranged. It stays off when `--agent` named one, because
then the answer is on the command line already.

## Why the backend path is not shared with slbench

`cmd/slrun/script.go` and `cmd/slbench/script.go` both talk to the
script.v1 contract, each with its own dial and lease loop.

The two ask different questions of the same contract. A benchmark runs
its measured sequence in the first object it leases and its probes in
the rest, asks whether a script compiles without running it, and sifts
what it hears for its own `RESULT:` and `INFO:` lines; slrun runs each
script wherever is free and prints what it hears. The overlap is the
dial and a lease loop, and a package holding those two would be a
package whose callers each ignore half of it.

When that was first written, a benchmark also needed the object it
measured in to keep what a script left there and turned a fault into a
size limit, and slrun needed one object or none. Read from the code on
2026-09-26, none of those holds now: a benchmark script says one number
wherever it runs
([scripttest.md](scripttest.md#a-benchmark-script-says-one-number)),
nothing in slbench decides anything on a fault's `out_of_memory` any
more, and slrun leases as many objects as `--jobs` asks for.
