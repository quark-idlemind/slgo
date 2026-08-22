# A guide to slgod, automate and autobench

Three command-line programs for working with Second Life without a
viewer.

| | |
|---|---|
| `slgod` | a daemon that holds grid sessions, so everything else starts instantly |
| `automate` | runs LSL scripts and prints what they say |
| `autobench` | measures how much script memory an LSL construct costs |

There is a fourth, `slsh`, an interactive shell for inventory, the
region around you and chat. It has a guide of its own:
[doc/slsh-guide.html](slsh-guide.html). Everything under "Connecting"
below applies to it as well.

---

## Connecting

There are two ways to be connected, and `automate`, `autobench` and
`slsh` all support both. `slgod` has neither flag and needs neither: it
is the thing the others connect to.

**Through `slgod`** (the default). `slgod` holds the connection to the
grid. Programs attach to it, so they start instantly and leave the
avatar logged in when they exit. Several programs can use one session
at the same time.

**Directly**, with `--direct` (`-d`). The program logs in itself and
holds the session for as long as it runs. Quitting logs the avatar out.
This needs no daemon, but every start pays for a fresh login -- several
seconds, against none.

### Options come before the first argument

Every one of these programs stops reading options at the first thing
that is not one, and anything after that is taken as an argument. There
is no warning:

    automate --rez script.lsl      rezzes a prim
    automate script.lsl --rez      uses the shared object, and says nothing

`slgod` is Go's `flag` package rather than getopt and stops in the same
place, which is at least louder about it, because its arguments are
profile names: `slgod example -v` reads `-v` as a second profile to log
in, and says

    -v: NOT hosted: agent: profile "-v": open .../slgo/-v: no such file or directory

before carrying on with the one it could read.

### Saying where slgod is

    automate --addr HOST:PORT script.lsl

If you do not say, the address is worked out for you:

1. `addr = ...` in `~/.config/slsh/config`, for `slsh`;
2. whatever the `sl-host` command prints, if you have one installed --
   it names the host, and the standard port is added to it;
3. this machine, if you have no `sl-host`.

That last case is the normal one when you run your own `slgod`, so
usually you need not pass anything at all. Not being on `$PATH` is what
means "this machine"; an `sl-host` that IS installed and then fails is
reported, and does not fall back here. Falling back would turn "sl-host
is misconfigured" into a connection refused against localhost, which
points at the wrong problem entirely.

### Profiles

Accounts live one file per account in `~/.config/slgo`, named however
you like. The directory must be mode 700 and each file mode 600 --
these programs refuse to read credentials anyone else can:

    first           = Example
    last            = Resident
    password        = $1$<the md5 of the password>
    start           = last
    group           = Example Builders
    neighbours      = yes
    viewer_password = $1$<a second one, for handing sessions to a viewer>

The file name is the profile name: the one above as `~/.config/slgo/example`
is the profile `example`, which is what `slgod example` and
`--agent example` mean.

`start` is where to arrive: `last`, `home`, or a region name. `group` is
the group to act as, which decides more than it looks like -- see "The
group an avatar acts as" below. `neighbours` is whether this avatar
holds a circuit to the regions beside it, which is what walking over a
border needs and what a benchmark has no use for; it wins over `slgod
-neighbours`, which is what profiles that say nothing get, and `slsh
neighbours` turns it over on a session that is already up.
`viewer_password` is what makes a session handable to a real viewer, and
is described with `-viewer`.

Storing the `$1$...` digest rather than the plain password is worth
doing. It is the only form that ever goes over the wire, so it loses
nothing, and it keeps a password that may be used elsewhere off the
disk. Plain text works too, and is hashed on the way out.

For `--direct`, anything missing is asked for at the terminal, and the
password without echo -- so a profile is optional:

    automate --direct --first Example --last Resident script.lsl

With exactly one profile on disk, `--direct` uses it without being
named and says which -- "using the example profile". That rule is the
direct login's alone. Through `slgod` an avatar nobody named is the
daemon's default instead, which is a different question and is answered
under "Which avatar a program uses".

### The shared secret

`slgod` holds a live Second Life session, so an unauthenticated daemon
reachable off this machine lets anyone drive the avatar. Clients and the
daemon therefore prove themselves to each other, both directions, against
one secret:

    ~/.config/slrun/secret      mode 600, in a directory mode 700

One file for the whole lab, shared by every program here. Create it with

    (umask 077; mkdir -p ~/.config/slrun; openssl rand -hex 32 > ~/.config/slrun/secret)

which is what `slgod` prints if it cannot find one. The secret never
crosses the wire in either direction: each side answers a random,
single-use challenge, so a captured proof is worth nothing afterwards.

The connection is TLS, and the certificate is self-signed and **not**
checked -- it is there for confidentiality only, and the proof of who is
at each end is bound to that TLS session, so nothing is gained by
verifying it and there is nothing to distribute or rotate.

`-no-auth` serves without any of this. It is for a loopback-only run,
and nothing checks that the connection really is loopback.

---

## slgod

    slgod example                       one avatar
    slgod example builder helper        three
    slgod -listen :7900 example         somewhere other than the default :7807

Each argument names a profile. The sessions stay up until the process is
signalled; clients attach and detach freely without the grid noticing.

### The computer it claims to be

The login server asks a client which computer it is running on -- the
network card's address and the first disk's serial number, as digests.
`slgod` has no hardware worth reading, and reading this host's would hand
Linden Lab an identifier that follows you into every other program on the
machine, so it makes a pair up once and keeps it:

    ~/.config/slgod/config

Keeping it matters more than what it is. A pair that changes every login
looks like a different computer every time, which is what an abuser looks
like; a pair that never changes looks like an ordinary resident with one
computer. The file is written on the first run and read on every run
after.

### When a login fails

One expired password should not take down the sessions that did come up,
which may be somebody's benchmark in progress. So a failure is per
profile, and there are two kinds:

    example: not up yet (404 on the seed capability); trying again
    helper: NOT hosted: login failed: key or password incorrect

The first is worth asking again for, and is asked again for. A grid hands
back a dead seed capability often enough that a daemon which gives up on
the first one is a daemon somebody has to go and restart. The second
cannot be cleared by waiting, so that profile is left alone and the rest
carry on. `slgod` stops only when **nothing** came up and nothing is
still being retried -- "no session came up; nothing to serve".

### The group an avatar acts as

    slgod -group "Example Builders" example
    slgod -group example="Example Builders" -group builder=Testers example builder

A parcel usually grants "create objects" to a **group** rather than to
individuals, and a login starts with no group active. A viewer hides this
by storing the group in its settings and re-sending it every time, which
makes it feel permanent. Headless it is not -- so an avatar that builds
happily through a viewer cannot rez a thing here, and the refusal blames
the land, which is the wrong place to look.

A profile's own `group` line settles it; `-group` overrides that, by
name or uuid, and the `PROFILE=GROUP` form says which avatar it is for.
The setting belongs to the session rather than to a client, so every
program attached shares it, and a reconnect does not lose it.

### Which avatar a program uses

    --agent NAME     this command, and nothing else
    SLGO_AGENT       every command in this shell
    agent = NAME     in slsh's config file, for this machine
    (nothing)        the daemon's default: the session it has held longest

In that order of strength. The daemon's default changes only when that
session itself goes away -- adding an avatar never moves it, and neither
does a reconnect -- so a script that worked yesterday drives the same
avatar today.

Whenever a program does not name one, it says which it used:

    $ autobench --statement "i += 1;" --locals i
    running as example, objects 0-3

Worth reading. With several avatars hosted the choice is the daemon's,
and a benchmark attributed to the wrong avatar is not an error -- it is a
plausible number.

### What state each avatar is in

`slsh agents` is the readable view of what the daemon holds, and the
states differ in what to do about them:

    * example     Example Resident          Testville   <- this shell
      builder     Builder Resident          Testville
      helper      Helper Resident           stopped: logged out
      spare                                 configured

`configured` exists on disk and can be started, and has no avatar beside
it because nothing has ever logged one in under it. `stopped` was put
down deliberately and is left alone until asked for by name; it keeps
the avatar's name, because there was a session there. `failed` will
waste a login attempt if asked again too soon, and says how long it is
waiting. `connecting` is a login still in flight or a session being
reconnected -- the reason after it tells those two apart.

The star is the first line the daemon still **holds** a session for,
which is what a program that names no avatar gets. A session that is
reconnecting keeps it: the place belongs to the session and not to the
circuit under it, so a bare command still drives that avatar and waits
for it. Only `stopped` gives the place up, and a profile with no
session behind it never had one.

### Letting a viewer have one

    slsh logout example         slgod lets go of it
    ... use it in a viewer ...
    slsh login -f example       take it back

A logout is remembered. `slgod` will not bring that avatar back on its
own, so nothing is fighting you for it while you use it elsewhere, and
bringing it back has to name it and say `-f` -- undoing a deliberate stop
should be deliberate too.

You do not strictly have to log out first. Logging in from a viewer makes
the grid end the daemon's session, and `slgod` treats being thrown off as
a decision rather than a fault: it says so and stays down. Logging out is
the tidier way round, and the only one that lets the session end cleanly.

Both refuse to be casual about it. A logout will not take a session that
clients are attached to -- it names them instead -- because a benchmark
mid-run has a script installed and a reading half taken.

### Handing a session to a viewer

    slgod -viewer 127.0.0.1:9000 example

With this, `slgod` serves a login endpoint of its own, and a viewer
pointed at it is given the session the daemon is already holding rather
than logging in again. The avatar does not leave the grid, nothing is
logged out, and the programs attached carry on.

A profile is only handable if it has a `viewer_password`. A viewer
offered one without it is refused, and told which line to add:

    example has no viewer_password, so it cannot be handed to a viewer;
    add a "viewer_password = ..." line to that profile and restart slgod

That password is the viewer's, not the account's -- what a viewer sends
here never reaches Linden Lab, and a password kept for this cannot be
used to log the account in anywhere.

`slsh viewer` is the other end of it: it says where the daemon serves
viewer logins and whether one has taken this session, and `viewer -l`
starts a viewer and logs it in. That does not use the profile's
password. The daemon mints a fresh one, good for a single login and a
few minutes, because it reaches the viewer's argv where any process this
user owns can read it. A profile with no `viewer_password` is refused
there too -- the setting is what marks a profile as handable at all.

The design, and what it costs, is written up in
[doc/viewer-frontend.md](viewer-frontend.md); what happens with more than
one viewer is in [doc/two-viewers.md](two-viewers.md).

### Watching the wire

    slgod -v example                                  log every message the grid sends
    slgod -trace packets.txt example                  every packet, both directions, in order
    slgod -trace packets.txt -trace-messages ChatFromSimulator,ObjectUpdate example
    slgod -trace packets.txt -trace-bodies example    each traced message in full

A trace is worth taking before there is anything to debug: a baseline of
what an ordinary session receives is what an unusual one has to be
compared against, and it cannot be collected afterwards.

`slsh watch` is the lighter way to see the same thing from outside:
message names as the protocol spells them, and naming none means every
one. It opens a connection of its own, so it never steals messages from
the reader keeping the shell's idea of the world up to date. It runs for
half a minute unless `-t` says otherwise, and `-t` needs a unit -- `-t
5m`, not `-t 5`.

### Options

| | |
|---|---|
| `-listen ADDR` | address to serve clients on (default `:7807`) |
| `-no-auth` | serve without authentication; loopback only, and it is not checked |
| `-group G`, `-group PROFILE=G` | the group to act as, overriding the profile's own |
| `-start WHERE` | override every profile's start location |
| `-viewer ADDR` | serve viewer logins here, so a real viewer can be handed a session |
| `-neighbours` | hold a circuit to each neighbouring region, so an avatar can walk over a border; a profile's own `neighbours` setting wins over it |
| `-trace FILE` | write a packet trace |
| `-trace-messages NAMES` | trace only these; empty traces every one |
| `-trace-bodies` | write each traced message out in full |
| `-v` | log every message the grid sends |

---

## automate

Runs LSL scripts in Second Life and prints what they say.

    automate script.lsl
    automate one.lsl two.lsl three.lsl
    automate --object "bench box" script.lsl

Each file is compiled by Second Life and run, and each line the script
says is printed as it arrives, prefixed with the file it came from:

    $ automate hello.lsl
    hello.lsl: hello from automate
    hello.lsl: two plus two is 4

### Say DONE when you are finished

A script must say `DONE` when it has finished:

    default {
        state_entry() {
            llSay(0, "hello from automate");
            llSay(0, "DONE");
        }
    }

Without it there is nothing to wait for but the clock, and every run
costs the whole timeout. Use `--done TEXT` for a different word, or
`--done ""` to deliberately wait out the timeout.

The `DONE` line itself is not printed -- it is the script talking to
`automate`, not to you.

The match is a **substring** one: the first line *containing* the word
ends the run, and that line is not printed. So a script that says
"checked 3 files, none DONE" has stopped there and said nothing about
it. Pick a word the output cannot contain by accident.

### Where the script runs

A script needs an object to run in, and by default those are the `auto`
objects: four of the prims the avatar wears, kept. Any that are not
being worn are put on; any that do not exist are made, taken into
inventory and put on. That happens once.

Keeping it is what makes runs quick. The first script put into an
object takes several seconds to appear; replacing one already there
takes well under a second. Measured here: about **1 to 2 seconds** a
run, against about 10 seconds when every run rezzed its own prim.

That holds across logins. A worn object is rezzed afresh, with a new
key, every time it is put on and every time the avatar logs in, so its
key is worth nothing between sessions -- but the inventory item it was
worn from does not change, and the simulator says which item each worn
object came from, in an `AttachItemID` line of the object's NameValue.
So the tie is re-read off the wire every session. Nothing is written
down on your machine, and there is no cache to go stale.

    --object NAME    run in some other object already in the region
    --rez            rez a throwaway prim for this run, as before
    --keep           leave a rezzed prim behind

### Several scripts at once

Scripts named on one command line run in different objects at the same
time, four at once by default because four is a group:

    $ automate a.lsl b.lsl c.lsl d.lsl
    c.lsl: c starting
    b.lsl: b starting
    d.lsl: d starting
    a.lsl: a starting
    c.lsl: c finished
    ...

Measured on the grid, four scripts that each sleep three seconds:
**17.5 seconds one at a time and 5.2 seconds four at once.** Six of them
take 9.1 seconds -- four, then the other two as objects come free, since
an object takes the next script the moment it is free rather than
waiting for its neighbours.

They finish in whatever order they finish in and the lines are
interleaved, which is what the tag in front of every line is for. The
tags are padded to the widest name on the command line so that they read
as a column.

`--jobs N` is how many run at once, up to the twelve objects an avatar
wears. Eight scripts of three seconds each took **6.9 seconds**
measured, against about 35 one at a time.

The objects are taken all together or not at all. `--jobs 8` on an
avatar with six free does not run six: it waits until eight are free,
and says what it is waiting for. That is not fussiness -- a run that
took the six and waited for the other two would be holding six objects
that nobody else can use while it waits for somebody who may be doing
exactly the same thing, and neither would ever finish.

Asking for more than the avatar has is refused rather than waited for,
and says how to make more (`slsh auto -n 12`).

An object that has never run a script from `automate` is slower the
first time: creating the script item costs about eight seconds where
replacing one already there costs about one. Measured, the first
`--jobs 8` run took 14.5 seconds because three of its objects were new
to it, and the next took 6.9.

`--jobs 1` runs them in the order they were named, one after another,
and is what a set of scripts that leave something in the object for one
another needs: with four running there is no shared object to leave
anything in.

Each script is installed under the same name -- `--script`, `automate`
by default -- because each is in an object of its own. That is why the
fan-out is one script per object and not several: measured, a second
script uploaded into one object under the same name destroys the first,
and then **both** runs report the survivor's output as their own and
both say they finished. Chat carries the object a line came from and
never the script's name, so one script per object is also what makes the
tag on each line true.

`--object` and `--rez` are one object between them, so they are one
script at a time; `--jobs` with either is refused rather than quietly
ignored, because a person watching for a speed-up that was never coming
is worse off than one who was told.

### Several runs at once

A run takes as many objects as it asked for and holds them until it
finishes. An avatar wears twelve, so three benchmarks fit on one avatar,
or a benchmark and eight scripts, or any other way twelve divides up --
and if `slgod` is holding several avatars a run that finds them all busy
moves on to the next avatar rather than queueing.

That order is the daemon's, not alphabetical: the default avatar first,
then the rest. So nine runs started together against three avatars simply
spread out, three apiece. Only when every group on every avatar is busy
does anything wait.

Naming an avatar turns that off, and `SLGO_AGENT` counts as naming one:
an avatar asked for by the environment is still an avatar somebody asked
for, so a run waits for it rather than quietly using another. That is
worth knowing before setting the variable in a shell you then start nine
things from.

Why a run gets its objects to itself: a benchmark carries its base
reading in the object's linkset data, which belongs to the object rather
than to the script, and the script is installed under a fixed name -- so
two runs sharing an object would overwrite each other's reading and each
other's script.

Objects are locked one at a time, and on its own that deadlocks: three
runs each wanting four of twelve can end up holding three apiece and
waiting for a fourth that nobody is going to give back. What prevents it
is one more lock. A run takes an **allocation lock** first, and under it
takes all the objects it wants or none of them -- trying each, never
waiting for any. If it cannot be served it gives back everything it
took, drops the allocation lock, and only then waits. Nothing is ever
held while waiting for anything else, so there is no cycle to deadlock
on.

What that leaves is starvation: a run wanting twelve can in principle be
stepped over by a stream of runs wanting one. Nothing prevents that,
deliberately -- these are programs run by hand or from a script rather
than a service under load, so the queue drains.

This used to be done by dividing the pool into fixed groups of four and
locking a whole group, four being what a benchmark's search happens to
use. That could not deadlock either, but it made four the unit of
everything: a run wanting six had to hold eight, and one wanting twelve
could not be served at all.

Set an avatar up with `slsh auto -n 12`, once per account. An avatar that
is not allowed to build can still be set up, provided somebody who can
gives it one object: everything after the first is a copy, and copying
something already owned asks the land nothing.

The lock is held by `slgod` for as long as the program holds its
connection, so a run that crashes or is killed gives it back at once;
there is nothing to clean up and no stale lock to break. Use `--rez` to
take a prim of your own and not queue at all.

### What can go wrong

Each is reported and each makes the run fail:

    script.lsl: (3, 4) : ERROR : Syntax error          would not compile
    script.lsl: automate: Math Error                   crashed while running
    script.lsl: it did not say DONE within 1m0s        never finished

`automate` exits non-zero if **any** script failed, so it can be used
from a Makefile or a test script. It exits 0 only if every script
compiled, ran and finished.

### Options

| | |
|---|---|
| `--object NAME` | run in this object instead of the shared `auto` one |
| `--rez` | rez a throwaway prim for this run, and do not queue |
| `--keep` | leave a rezzed prim behind |
| `--done TEXT` | the text that means "finished" (default `DONE`); matched as a substring |
| `--timeout DUR` | how long to wait for it (default `1m0s`); a bare number is refused -- the unit is required |
| `--script NAME` | what to call the script inside the object (default `automate`, which is the name a fault is reported under) |
| `--jobs N`, `-j N` | how many scripts to run at once, one per object; four (a group) by default, more takes more groups, `1` runs them in the order they were named |
| `--backend HOST:PORT` | run the scripts through a `script.v1` backend there -- a simulator, or a viewer daemon -- instead of in Second Life |

---

## autobench

Measures how many bytes of script memory an LSL construct costs.

    autobench --title "global integer" --code "integer gCNT;"
    autobench -1 --title "global integer" --code "integer gCNT;"

A script has a fixed memory budget, and Second Life allocates script
code in 512-byte blocks -- so you cannot simply ask what one variable
costs. `autobench` has two ways around that. What it is working around
is written up in [doc/memory.md](memory.md).

**Copy mode** (the default) puts many copies of the code in one script
and measures them at two counts, which separates what the code pays
once from what each copy costs:

    $ autobench --code "integer gCNT;"
    Padding: 473
    Size: 22
    First copy: 34

`Size` is what an **additional** copy costs; `First copy` is what one
costs outright. They differ by whatever the construct pays once and then
shares. `First copy` appears only when the two could be told apart --
see "Reading the numbers honestly" below.

**Padding mode** (`-1`) uses a single copy and finds how much filler it
can still carry before memory steps to the next block, which locates the
boundary exactly. It costs more runs, because that boundary has to be
found twice -- once for the base script and once with the copy in it:

    $ autobench -1 --code "integer gCNT;"
    Base mem: 5412
    Result mem: 5932
    Result pad: 488
    Size: 32
    Padding: 473

`Size` is the answer. `Padding` is described below. The other three are
the readings behind it: the base script at that padding, sitting exactly
on a block boundary; the same script with one copy of the code in it;
and the filler that copy can still carry before it spills into the next
block. The size is the difference of the two readings less that filler:
5932 - 5412 is 520, of which 488 was filler the copy could still have
carried, so the copy cost 32.

The 520 is worth a second look, because it is not a whole block. One
512-byte block of code was allocated and 488 bytes of it went unused, so
24 of the 32 is code; the other 8 is the heap the global integer
occupies, and heap is not block-allocated at all. That is why the same
construct comes back at 32 rather than at 24, and
[doc/memory.md](memory.md) takes the two apart.

### Describing the code to measure

Give the code in one of three ways -- `--code`, `--statement`, or a
filename -- and never more than one.

`--code` is repeated whole:

    autobench --code "integer gCNT;"

`CNT` in it is replaced with the copy number, so each copy can have a
name of its own. That matters: without it, a hundred copies of
`integer g;` is not a hundred variables.

`--statement` is for code that must live inside a function, and wraps it
in one for you:

    autobench --statement "x = j + 1;" --params "x,j"

`--preamble` and `--postamble` add text before and after the copies, and
`--globals` declares globals.

This expands to:

```
_(integer x, integer j) {
x = j + 1;
x = j + 1;
x = j + 1;
...
}
```

### Naming variables

`--globals`, `--params` and `--locals` take comma-separated names --
**no spaces** -- whose **first letter gives the type**:

| letter | type | | letter | type |
|---|---|---|---|---|
| `a`, `l` | list | | `k` | key |
| `f`, `g` | float | | `q`, `r` | quaternion |
| `i`, `j`, `x` | integer | | `s` | string |
| | | | `v` | vector |

So `--globals "iCount,sName,vPos"` declares an integer, a string and a
vector. Any other first letter is refused, so a name has to be chosen to
say what it is.

### Reusing the padding

Both modes print a `Padding:` line. Finding that number costs about a
dozen runs, and it is a property of the base script -- the harness with
no copies of your code in it -- rather than of the code being measured.

You do not have to do anything about that: it is remembered, in
`~/.config/slgo/autobench-padding`, and looked up by what the base
script is. The code under test is not in the base script at all, so a
benchmark of new code reuses the answer, and so does a different
benchmark whose title is the same length -- a title is a string
literal, and what it costs is its length.

    cold, nothing remembered   33s
    the same benchmark again   20s
    a different benchmark      19s

An entry is confirmed rather than trusted. Using one runs the base
script at that padding and one byte past it and requires memory to grow
between them: two runs against the dozen a search costs, and an answer
that has stopped holding -- because Second Life's compiler changed
under it -- is thrown away and searched for again. So there is nothing
to invalidate by hand.

Those two readings are taken at the same time, in two objects. Nothing
a script says identifies the script that said it -- chat carries the
object and no more -- so two scripts in one object cannot be told
apart, but two objects can. `--objects N` sets how many to use, up to
the four in a group; the extra ones are worn and kept like the first.

When a padding does have to be searched for, three pads are tried at
once and the range is quartered rather than halved: four or five rounds
where a bisection takes nine. Three readings taken together cost about
what one costs, being three round trips in flight rather than three in
a row.

Both searches work that way -- the base script's and, in `-1` mode, the
one-copy script's. Median times, with one object against four:

    -1, padding remembered      19.3s -> 15.4s
    -1, searched from scratch   32.0s -> 25.7s
    copy mode, searched         34.2s -> 16.5s
    copy mode, remembered                 5.7s

A script reports its own memory before it reads anything, so the number
a search compares means the same in any object. What a script works out
*afterwards* does not: it divides against a base held in its object's
linkset data, which a spare object has none of. So only the memory
reading travels, and the measured sequence still runs where the base
is.

`--ipad N` still asserts a padding outright and skips all of this,
`--check-ipad` confirms it, and `--no-cache` neither reads nor writes
the file. Only readings that came from Second Life are remembered at
all: the backend is asked whether it is the grid, so neither `--test`
nor a `--backend` simulator writes to the file or reads from it. The
offline model has no compiler and its paddings are arithmetic rather
than measurements, and one of those in the file would be read by every
later benchmark on the account as though the grid had said it.

### Checking your connection

    $ autobench --probe
    SL Live

It sends a script and checks what comes back, through whichever backend
was chosen -- so `--backend` or `--test` answers `Backend live` instead.
That is the one place a person is asking whether they are talking to
Second Life, and it will not say so about a model.

### Options

| | |
|---|---|
| `-1` | padding mode: one copy, exact answer |
| `--code CODE` | the code to measure, repeated |
| `--statement CODE` | statements to measure, wrapped in a function |
| `--globals`, `--params`, `--locals` | declare variables (see above) |
| `--preamble`, `--postamble` | text around the copies |
| `--title NAME` | name the benchmark, echoed in the output |
| `--ipad N` | a padding from an earlier run |
| `--check-ipad` | confirm `--ipad` before trusting it |
| `--no-cache` | do not remember or reuse the padding |
| `--objects N` | how many objects to run in at once (default 4: one to measure in, three to probe with) |
| `--fast` | skip the padding search; `Size` then comes with a `±` margin, because there is no boundary to measure against |
| `--max N` | most copies to use (default 512) |
| `--object NAME`, `--rez`, `--keep` | as for `automate` |
| `--pad PAD` | extra filler in the base script |
| `--debug` | say what the search is doing |
| `--timeout DUR` | how long one script may take (default `1m0s`); a bare number is refused -- the unit is required |
| `-v` | print each script before running it |
| `--probe` | check the connection and exit |
| `--test PAD,SIZE[,MARGINAL[,LIMIT]]` | measure against the offline model in this process, without a grid |
| `--backend HOST:PORT` | run the scripts through a `script.v1` backend there, instead of in Second Life |

### Reading the numbers honestly

`Size: 22` and `Size: 32` are not two answers to the same question. Copy
mode's `Size` is what an **additional** copy costs; `-1` mode measures
one copy outright, and reports 32 for the same construct.

The gap can be large, and when it is, it is telling you something.
Measured live: `string sCNT = "<250 identical characters>";` costs 1054
bytes for one copy and 540 for each after it. Identical literals are
shared, so an extra copy pays only for what it cannot share. That is a
real property of the construct, and copy mode reports both halves of it
from one benchmark.

**Precision.** `-1` mode answers `4n + k`, where `k` is what the
construct's heap leaves over a multiple of 4: 0 for anything that
declares no globals, 0 again for one integer, whose 8 bytes of heap are
themselves 4-aligned, and 2 for a construct declaring one string,
whatever its literal. Code is 4-aligned, so two measurements of the same
construct that differ by 4 differ by one quantum. Measured across a
dozen live runs, the same construct came back 364 or 368 depending on
what unrelated globals the script carried, so 4 bytes is the resolution
to expect rather than the exact byte.

**`First copy` is withheld when it cannot be resolved.** It is the
second-order term -- the difference of two differences -- and live it
moves with the copy count in a way it should not: for one construct 371,
367 and 344 at three counts. Copy mode checks whether the readings
really do resolve into an initial cost plus a constant one per copy, and
if they do not it says so and reports only `Size`. `-1` mode measures
one copy outright and is the thing to use when that is the number you
want.

Both modes handle constructs of any size, including those larger than a
512-byte block.

Second Life's own numbers move about a little from run to run. If an
answer matters, take it twice.
