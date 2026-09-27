# scripttest, the backend that runs no LSL

The comments in `scripttest/` say what the package does. This page is
why, where the why is a measurement or a story: what was measured live
that the model has to be able to express, and what the in-process
client was measured to save.

## Why Direct exists

`scriptv1.RunnerClient` is an interface and so is
`grpc.ServerStreamingClient`, so a caller can be handed something that
answers the contract without a transport in the middle. What that buys
is the per-run cost, and the per-run cost is the whole reason
`direct.go` exists: measured by `BenchmarkARunReachedEachWay`, one run
of the offline model costs about 100us over `Pipe` and about 5us through
`Direct`. Almost all of the difference is goroutine hand-off -- a run
streams seven messages and each one is a wake-up on the other side --
and a profile of slbench's tests before `Direct` was added spent 73% of
the run in `pthread_cond_signal`, `pthread_cond_wait` and
`findRunnable`. That is worth paying where a run is the thing being
tested and not worth paying 440,000 times where the runs are how a sweep
gets to its arithmetic.

A run of that benchmark's script streams five messages now --
`Compiled`, three lines and `Finished` -- since a benchmark script says
one number (see below).

## A reading one block high

`Options.Noise` is there because `llGetUsedMemory` really does
misreport. On 2026-08-03 the one-copy reference script at pad 602 read
6436 bytes where it reads 5924 every other time it has been asked -- one
whole block high, once, and never again in the 45 asks since. A padding
search is a chain of comparisons between readings, and a reading one
block high looks exactly like the memory having grown, which is the
event the search exists to find. slbench's crossing confirmation is
there to survive that, and there is no other way to write a test for it:
the fault cannot be provoked to order.

This package deliberately left the hook out at first as speculative,
and it is in now for one reason: without it the contract cannot express
the one instrument fault this repository has actually seen, so the code
written to survive that fault could not be reached from the caller's
side of the seam at all.

## What the model can express

`Marginal` differs from `CodeSize` whenever a construct pays a cost once
that the rest share. Measured live, a 250-character string literal is
1044 bytes for one copy and 542 for each after it, because identical
literals are shared. A model that could not express that would assert
the two are equal and quietly hold every test written against it to the
same assumption.

`Drift` is for a construct whose per-copy cost is not quite constant.
Live, 32 of one construct cost 11008 bytes and 16 cost 5516, which is
343.25 each. slbench's copy mode had a guard for exactly that, which a
model without drift could never fire; copy mode has since gone, and
nothing in this repository sets `Drift` now.

`Collide` is there because the live behaviour has two size limits rather
than one: above `Collide` the script compiles and then runs out of
memory, above `Limit` the compiler will not take it at all. Measured on
Agni 2026-08-03, 256 copies of the reference shape compiled and then
collided stack with heap, and 512 were refused outright. A caller that
treats the two differently cannot be tested against a model that only
has one.

## The harness line is a comment

slbench writes the copy count and the pad into a comment,
`// slbench cnt=N pad=P`, because a comment is free. Measured: 604 bytes of it
moved `llGetUsedMemory` not at all, the compiler having thrown it away
before there was any bytecode to count -- so the count and the pad can
be carried in the script without the digits of either changing what is
being measured. They used to go into the harness call as integer
literals, where their SIZE varied with their value.

## A benchmark script says one number

A benchmark script says what `llGetUsedMemory` answered, and nothing
else. It used to keep the base reading in its object's linkset data and
do the arithmetic in LSL, so the model had to model that too -- a
per-target base, and a probe in the wrong object dividing against a
zero. All of it went when the arithmetic moved to where it could be
seen.
