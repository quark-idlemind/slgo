# scripttest, the backend that runs no LSL

The comments in `scripttest/` say what the package does. This page is
why, where the why is a measurement or a story: what was measured live
that the model has to be able to express, and what the in-process
client was measured to save. The last sections are slbench's side of
the same seam: why its `--test` is a backend at all.

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
event the search exists to find. Nothing about it is visible from
inside the search, so the search believed it -- and the run cache then
served the same wrong reading back for the rest of the walk, so it
never got a second chance. The published Size moved 16 bytes, twice,
with nothing in the output to say why. slbench's crossing confirmation
is there to survive that, and there is no other way to write a test for
it: the fault cannot be provoked to order.

The complaint was as much that the event was silent as that it
happened, which is why slbench's `noticef` is unconditional where its
debug output is not: a benchmark that had to argue with its instrument
says so whether or not anybody asked for `--debug`.

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
literals, where their SIZE varied with their value, and the harness
branched on the count, so the base script and the test scripts were not
quite the same program.

## A benchmark script says one number

A benchmark script says what `llGetUsedMemory` answered, and nothing
else. It used to keep the base reading in its object's linkset data and
do the arithmetic in LSL, so the model had to model that too -- a
per-target base, and a probe in the wrong object dividing against a
zero. All of it went when the arithmetic moved to where it could be
seen.

Keeping the base in linkset data made it a piece of WORLD state. The
object that held it was special, only a cnt=0 script that actually RAN
could write it, and a cache hit in slbench -- which sent no script and
so wrote no base -- left the two disagreeing. What that cost is on the
record: a base one block low is 512/count on every size reported,
silently, for half of all shapes.

It shaped slbench beyond the script. A probe taken in a spare object had
no base in that object's linkset data to divide against, so its size and
base were arithmetic on a zero and had to be kept in a cache of their
own that copy mode would never read. And the live transport was one
session and a list of objects, because carrying the base between scripts
through the measured object made that one object special, and one avatar
enough. With one number said and the arithmetic done in slbench, a
reading is a reading wherever it was taken, and a benchmark asks for N
places to run scripts, exactly as slrun does.

## slbench's --test is a backend

slbench's measurement machinery is arithmetic on readings of
`llGetUsedMemory`, so anything that can answer readings can drive the
whole of it, and a model can answer readings. That is what `--test` has
always been. What changed is where it answers from.

It used to answer ABOVE the transport: `runScript` had a branch that
computed the model's number and returned it, having sent nothing.
Everything the answer had to pass through on its way back from Second
Life -- the compile refusal, the fault, the sift of what the object
said, `absorbResults` -- was therefore skipped under `--test` and
reachable only with a grid at the far end, which is to say reachable
only by hand. That is why slbench's `backend` is an interface rather
than left implicit: with a seam there, the model is a script.v1 backend
like any other, started in the same process, and an offline benchmark
runs the same code a live one does.

It was a real connection at first -- `scripttest.Pipe`, marshalling and
all -- on the argument that the model should exercise slbench's
`script.go` rather than stand in for it. It still does: what
`openScript`, `runIn`, `sift` and `absorbResults` do is unchanged,
because what they are written against is `scriptv1.RunnerClient` and
that is an interface. What the connection was buying on top of that was
the transport's own behaviour, which is worth testing exactly once and
is tested here against `Pipe`, and what it cost was 100 microseconds a
run against 5 through `Direct`. A live run costs 1 to 30 seconds and
would not care; a `--test` sweep of a hundred thousand of them cares a
great deal, and paying it there bought nothing slbench is responsible
for.

Answering above the transport had hidden a cost in `buildScript` too.
It appended the `+i` chain of the filler to a string two bytes at a
time, and appending to a string copies the whole of it each time, so
rendering a pad of n cost n^2/4 bytes of copying -- 53 kilobytes of
garbage for a middling script. It went unnoticed while `--test` built no
script at all; the offline sweeps build 440,000 and it was the largest
single thing they spent. It is a `strings.Builder` now, emitting the
same bytes.

## Out of memory is a fact in the contract

A fault that ran out of memory is told from every other fault by
`Fault.out_of_memory`. While slbench searched for a size limit, running
out of memory was the ANSWER and every other fault a failure, and it
told them apart with
`strings.Contains(Detail, "Stack-Heap")` -- Second Life's wording, known
to a program that is not supposed to know Second Life, and a match that
would have gone on matching, wrongly and silently, against a simulator
that worded it differently. The contract says it outright, and each
backend answers for itself: the grid transport asks `sl.Fault`, which is
where those words are already understood.
