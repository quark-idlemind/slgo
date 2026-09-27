# Slots: sharing the objects that scripts run in

Written 2026-08-22, against `4b5c42a`. Where this says what happens, it
was watched happening on Agni with three avatars in one region. What was
built and then thrown away is here too, under its own heading, because
the reason it went is the useful part.

## What is being shared, and why it needs sharing

`slrun` and `slbench` run LSL by putting a script into an object and
listening to what it says. The objects are worn and kept rather than
rezzed per run, because installing a script into an object that has never
held one costs about 8.1 seconds where replacing one already there costs
about 0.9 — measured 2026-08-06 and the reason the pool exists at all.

Two runs in one object is the failure this is all built against, and it
does not look like a failure. Measured: two scripts installed into one
object under one name means the second upload destroys the first, and
then **both runs report the survivor's output as their own and both say
they finished**. A benchmark carries its base reading in the object's
linkset data as well, so two runs also divide by each other's numbers.
Nothing raises an error. The answer is simply wrong.

## What a slot is

A PLACE: an avatar and a number. Not an object.

The daemon deliberately does not know which object stands in which place.
It can see what is worn and which inventory item each attachment came
from — that arrives in the `AttachItemID` of an object's NameValue — but
not the item's NAME, and the name is the only thing that says an item
belongs to the pool. Names live in AIS, which is a capability the daemon
proxies rather than reads.

The client already knows all of that, and is the one that wears a missing
object and makes a missing item. So the daemon hands out exclusion and
the client hands out objects. That division was there before this work
and it survived it.

## Who decides

The daemon. It is the only thing that can see every client at once, so it
is the only thing that can answer *give me twelve* without two callers
each ending up holding some of what the other needs.

A request is for a COUNT. It is answered all of them or none, out of
whichever avatars have places free — twelve wanted on a daemon holding
three avatars may come back as eight of one and four of another, and the
answer says which avatar each place belongs to. A caller that minds names
one, and then it is that avatar or a wait -- or a refusal, when the
daemon does not host it or it is logged out.

`Slots`, `RenewSlots` and `ReleaseSlots` ride on the client's STREAM
rather than being calls of their own, for the reason a lock does and more
so: what a client holds has to be given back when it dies, however it
died, and the stream is the thing slgod already watches for that. A
client that crashed, was killed, or was unplugged gives its places back
without having said anything. A request the daemon settles just as the
stream ends is not kept either: its places go straight back rather than
to a grant nobody holds, and a lock asked for then is not taken.

## Asking and being answered

Each `Slots` and `RenewSlots` carries a number the client chose, and the
`SlotsGranted` that answers it carries the number back. Answers do not
come back in the order they were asked for — a try or a renewal is
answered at once, a wait when places come free — so a client with two
requests out on one stream tells the answers apart by number. A daemon
older than the number answers with 0, and the client then gives each
answer to the request that has waited longest, which is right while only
one is out at a time.

A `Lock` is numbered the same way, and the `Locked` that answers it
carries the number back. A try is answered at once and a wait when the
lock is given, so a `TryLock` asked while a `Lock` of the same name waits
on the same stream is answered first; matched by name, oldest first, as
they were before the number, the wait was handed the try's "not held"
and the try the lock. An answer with 0 is from a daemon older than the
number, and still goes to the oldest request for that name.

A client that stops waiting does not tell the daemon, which grants the
request anyway once places come free. The client gives that grant
straight back, and not clean: nothing ran in the objects, and clean would
wipe out the mark the last holder left on them.

A wait can be bounded. `Slots.wait_seconds` says for how long; zero
waits as long as it takes. When it runs out with the places still not
free, the daemon answers with a why and nothing granted, and stops
waiting. The client keeps the same deadline, counted from before it
asked, for a daemon older than the field that would wait on. Its clock
starts first, so it will usually give up a moment before the daemon's
answer arrives: what the daemon grants in that moment goes back as
above, and a refusal that arrives once the deadline has passed is taken
as the daemon's answer. Either way the caller is given
`client.ErrStillBusy`, which is what `slrun --wait` reports.

Answers are never dropped. The daemon's queue to each client drops
relayed traffic when the client falls behind, which costs nothing on the
grid; a grant dropped there would leave the client waiting for places
the daemon believes it gave. So answers have a queue of their own, 64
deep, sent ahead of relayed traffic, which the answers to locks share for
the same reason. It fills only for a client with that many requests out
at once, or one that has stopped reading, and that client's stream is
ended — which gives back everything it holds — rather than an answer
lost.

Every send a client makes on its stream is one at a time, behind one
lock: gRPC's documentation says two at once on a stream are not safe,
and a client sends from all its callers and from the loop that gives
grants back. That is taken from the documentation, not seen: with the
lock taken out, the race detector found nothing in two hundred runs of
every kind of send at once against a fake daemon.

None of this section was watched on the grid. It is how the code was
built on 2026-09-26, and the tests in `client/slots_test.go`,
`client/lock_test.go`, `client/send_test.go`, `server/slots_test.go`
and, for `--wait`, `cmd/slrun/daemon_test.go` check it against a fake
daemon and a fake stream.

## When an avatar is logged out, or leaves

Each avatar the daemon hosts has twelve places. One that is logged out
-- stopped, on request or by the grid throwing it off -- is still hosted
and listed, but it comes back only when somebody hosts it again on
purpose, with `Host` and force. So a request that names it is refused at
once, a try or not, with `"NAME" is logged out`, and the reason after it
when the grid ended the session. A request already waiting for it when it
logs out is refused then, with the same words. Its places stay in the
pool, passed over by requests that name nobody, which are served from
the other avatars; those still count it among the avatars whose places
there could ever be, so one that wants more than the others have waits
for it.

An avatar leaves the daemon when it is removed, which is what a forced
`Host` does to the stopped session it replaces before it logs in again.
Its places go with it, and the name, hosted again, is given twelve new
ones -- but not while a client still holds any of the old. An old place
and a new one with the same avatar and number are the same object, and
two holders of it is the failure this whole pool is built against. The
holder keeps its grant: it can renew it and give it back as before, and
giving it back puts nothing into the pool. Once the last old place has
been given back or has run out, the new ones go in, and a request that
was waiting for them is woken.

A request naming an avatar the daemon does not host at all is refused at
once, with `no avatar called "NAME" is hosted here`, and a try is given
the same answer. That includes a profile the daemon could start and has
not. One whose `Host` is still logging it in is waited for, and is woken
when the avatar arrives; if the login fails nothing wakes it, and it is
refused when something else in the pool changes, or gives up when its
bound runs out. A request already waiting when its avatar is removed is
woken and refused.

A session that drops and is logged back in by the daemon is none of
these: it is never marked stopped. It keeps its places, and they are
handed out as usual while it is reconnecting, since the daemon still
holds the old connection until the new one is up. That is read from the
code, not watched.

Until 2026-09-26 a request that met a logged-out avatar's places threw
them away for the life of the daemon, and hosting the avatar again did
not bring them back; and a request naming an avatar the daemon did not
host, with no bound on its wait, waited for ever. Both were found by
reading the code. The first fix had a request naming a logged-out avatar
wait for it, on the belief that such an avatar was often reconnecting;
a reconnecting session is never stopped, and a stopped one does not come
back on its own, so it is refused instead. The behaviour above was built
then and has not been watched on the grid; the tests in `server/slots_test.go` check it
against a fake login server and simulator, and
`internal/slots/slots_test.go` checks the pool's half.

## All of them or none

A caller given four of the eight it asked for has two choices and both
are bad: hold them while waiting for the rest, which is how two callers
deadlock against each other, or give them back. The daemon gives them
back for it and answers with nothing, plus a channel to wait on.

What that leaves is starvation — a caller wanting twelve can in principle
be stepped over for ever by a stream of callers wanting one. Nothing
prevents that, deliberately. These are programs run by hand or from a
script, not a service under load; the requests drain, and a scheme that
could not be reasoned about would be the worse trade.

## Leases, and what they are not for

A grant runs out. That is a backstop for a client that wedges without
dying: connected, so its stream says it is alive, and never going to give
anything back. `RenewSlots` is for work that cannot say in advance how
long it will take, which is any benchmark whose search finishes when it
converges.

The pool holds a grant a little longer than the holder was told — a
private grace — so that a deadline is not a cliff. A caller that reads
the clock a moment before its grant ends and whose write lands a moment
after would otherwise be refused for a race it could not have avoided, on
a place nobody else wanted.

The grace is **not** what makes overrunning safe, and nothing here is
yet. The pool hands out names for things it knows nothing about and
cannot stop anybody using one it has taken back. That belongs to whoever
owns the objects: the daemon writes the scripts, so it can see which
place an object is and refuse a write from a caller that no longer holds
it. **Intended and not built.** Until it is, the pool is the only thing
standing between two callers.

## Every object is a copy of the first

The objects after the first are made by COPYING it rather than by
building each (`session.EnsureAutoItems`). There are two reasons, and the
first is not an optimisation.

An avatar may not be allowed to rez. A parcel grants "create objects" to
a group, and an avatar in no group is refused — with a message blaming
the land (see [daemon.md](daemon.md#the-active-group)). Such an avatar
can still be given one object by somebody who can build, and from that
one it can make all the others, because copying an item it already owns
asks the land nothing at all.

It is quicker even when rezzing is allowed. Rez, name, take is eight
seconds and change; a copy is under two.

## What was built and thrown away

### Locks, a group at a time

Before this, the clients did the deciding themselves: a lock per group of
four objects, taken whole. Four was never a fact about the pool — it was
what `slbench`'s search took, three readings alongside the object being
measured — and it became the unit of everything. A caller wanting six had
to hold eight; one wanting twelve could not be served at all.

It is no longer a fact about a benchmark either. That search now cuts its
range into `--parts` and leases whatever that comes to, so nothing
computes from four any more. What is left of it is
`session.AutoGroupSize`, a default chosen for being a useful width
without being anybody's whole pool, which is what `slrun` takes when
`--jobs` does not say.

The deadlock that groups avoided is not caused by granularity. It is
caused by callers taking objects incrementally while others do the same:
three callers each wanting four of twelve can hold three apiece and wait
for a fourth nobody will give back. What prevents it is deciding a
request whole and never holding anything while waiting, which needs no
granularity at all.

The client-side version got there by a different route: a lock per object
and one allocation lock over them, with an invariant every caller had to
keep — nothing may block while holding the allocation lock. That is
deadlock free for the same reason, and it lasted a few hours before the
daemon took the job over: it worked and it was delicate, and it could
not answer the question a caller actually asks. There is
no allocation lock now. One goroutine in the daemon owns the whole pool,
holds no locks, and settles a request in a single pass over what is free:
all of them or none, and a request that cannot be served takes nothing
and comes away with a channel to wait on. The invariant is gone with the
lock, and with it the thing a caller could get wrong — see
`internal/slots/slots.go`.

### A ceiling on uploads at once

Written to explain the failures below, and thrown away because it did not
explain them. Peak concurrency was the same in the runs that failed and
the runs that were clean.

### Clearing an object before using it

An object handed on may still be running the last holder's script, and
chat carries the OBJECT a line came from and never the script's name,
which was measured — so a script still talking is a line the next run
reads as its own, since its collector can only filter on the object. A
run that reports somebody else's numbers is not a failure, it is a
plausible answer, which is the worst kind. Clearing installs a
do-nothing script over every script the object holds, which stops them:
installing over a name destroys what was there, measured.

The script written over each is empty, rather than the scripts being
removed. Installing a script over one of the same name destroys the one
that was there, measured: the old script never says another word. So an
empty script silences whatever was running and leaves the ITEM in place
— and an item already there is what makes the next install cost about
0.9s instead of the 8.1s creating one costs. Removing the scripts would
be tidier and would make every later run slower.

And the clearing waits to hear from it. The upload's answer says the new
script is in; it says nothing about what the old one had already said.
Chat arrives on the UDP path with no ordering relationship to an HTTP
reply, so the only honest way to know the noise is over is to hear
something come the same way after it. The empty script says a word
nobody else could say, and the clearing is finished when that word
arrives.

It is `--clear` and off, because the cost is certain and the hazard is
not. Installing under a name already destroys that name's script, so the
previous run — nearly always another `slrun` — stops the moment this
one starts. What is left is a script under a DIFFERENT name that goes on
talking after it has finished, and neither program that shares these
objects writes one: both say their piece from `state_entry` and fall
silent.

The cost is the one measured under [Where it breaks](#where-it-breaks):
thirty scripts behind a clearing were about ninety uploads, and failed
every run, where thirty on their own ran clean. So `--clear` is a
certain two thirds of the upload budget spent on a hazard nobody here
has yet seen, and what it is for is the day somebody DOES see foreign
lines in their output.

An object that will not come clean is dropped from the run rather than
ending it. It used to end the whole run, and thirty scripts went nowhere
because one object out of thirty would not answer -- which is a worse
answer than running twenty-nine. What is left is narrower and still
true: the caller does not know what is in that object, so it does not
listen to it.

## What was measured

Thirty three-second scripts, three avatars wearing twelve objects each,
all three standing in one region.

	thirty at once, warm                   11.4s, 10.1s, 10.9s
	thirty one at a time (extrapolated)    about 2 minutes
	thirty at once, objects newly made     26.8s

The last is the create path: `auto -n 12` makes the extra objects by
COPYING the first, and a copy carries whatever scripts that one held, so
a freshly built pool holds no script under the name the run wants and
every install pays the 8.1 seconds.

It does not scale the way one would expect:

	N=1    9.4s      N=12   22.0s
	N=3   30.1s      N=18   46.6s
	N=6   20.2s

One script costs nine seconds and eighteen cost between twenty and
forty-seven. Wall time is dominated by how fast the capability will take
uploads, and varies by more than a factor of two minute to minute; the
three-second sleep in each script is noise beside it.

**Spreading across avatars buys nothing.** Twelve on one avatar against
four on each of three: 12.6, 11.9, 11.6 seconds against 11.4, 12.3, 11.2.
No difference — and thirty is already spread over three and used to fail.
All three avatars are in one region, so the shared thing being saturated
is the simulator rather than the agent. The pool hands places out oldest
first and fills one avatar before touching the next, and that is fine.

### Where it breaks

	--clear off, thirty at once       clean, three runs
	--clear on, thirty at once        a failure every run, five runs

Clearing the pool objects is two uploads apiece, because each holds both
an `slrun` and an `slbench` script from having been copied. Thirty
scripts meant about ninety uploads rather than thirty. Peak concurrency
was the same either way — clearing does an object's scripts one at a time
— so what broke was the volume and how long it was sustained, not the
number in flight.

Two shapes of failure, both Second Life's:

	sl: UpdateScriptTask: status 500: <html> ... 'method': 'handle_request'
	(0, 0) : ERROR : Syntax error

The first is a Python stack trace out of Linden Lab's own web service,
one or two per run. Every one was the FIRST half of the two-step upload —
the half that hands over a description and is given a URL, having written
nothing — which is why asking again there is safe and why `sl` now does.
The name in the error is what says which half: the first carries the
capability's name and the second the uploader URL.

The second is the compiler's answer to an EMPTY body.

## The compiler's line numbers count from zero

Measured, because a retry was nearly built on the opposite belief:

	}  as the first character of a script    (0, 0) : ERROR : Syntax error
	an empty script                          (0, 0) : ERROR : Syntax error
	a bad token on the FIFTH line            (4, 10) : ERROR : Syntax error

So `(0, 0)` is a real position, and an empty body is **indistinguishable**
from a syntax error on the first character. Nothing may read `(0, 0)` as
"the upload was empty" and ask again: for somebody whose script really is
wrong at its start, that is a second upload and the same answer.

Also measured while looking for a way to force an error at the start:
`$` and `#` both COMPILE there, and `$` compiles inside `state_entry`
too. Neither is a way to make the compiler complain.

## Open questions

- **Nothing enforces a grant at the moment of writing.** The daemon
  could see which place an object is and refuse a script write from a
  caller that no longer holds it, which would turn an overrun from a
  silent collision into a loud refusal. Described above; not built.

- **`slbench` takes its objects from one avatar**, and that is
  plumbing rather than measurement: `runner` holds a single session and
  sends every script through it. What the objects share is the base
  reading in the measured object's own linkset data, which the spares
  never touch. Lifting it is a session per object and `UseAutoSpread`.
  One thing to check when somebody does: Second Life runs different
  simulator versions on different channels, so two avatars can be in
  regions with two LSL compilers, and whether that moves a reading is
  unmeasured.

- **Where the far end's ceiling actually is.** Thirty uploads in a burst
  is fine and ninety over twice as long is not, but nothing has measured
  the shape between them, or whether it is a rate, a total, or something
  about the region.

- **Whether a stale line has ever actually been read as somebody
  else's.** The window is real -- `sl.Run` registers its collector before
  it uploads -- but chat is not buffered when nobody is listening, so
  what exists is a race rather than a queue, and nobody has yet seen it
  lost.
