# How Second Life allocates script memory

What `llGetUsedMemory` actually reports, measured against Agni on
2026-08-08. This is what `autobench` is working around; the tool itself
is described in [the guide](guide.md).

Everything below is a measurement unless it says otherwise. Where a
reading is inference from the numbers rather than the numbers
themselves, it says so.

---

## The shape of a reading

    llGetUsedMemory()  =  512 · ceil(code_bytes / 512)  +  heap_bytes

Code is allocated in 512-byte blocks. Heap is not. A reading is
therefore never a multiple of 512 -- the base script used here reads
5412, which is ten blocks and 292 bytes over.

That 292 is the part people trip over. It is not a rounding error and it
is not noise: it is the memory the script's own globals occupy, and it
stays put while the block count moves.

### The evidence

Base scripts of vastly different sizes, all declaring no globals of
their own:

| base script | reading | blocks | remainder |
|---|---|---|---|
| bare | 5412 | 10 | **292** |
| + 5 extra functions | 6948 | 13 | **292** |
| + 30 extra functions | 15140 | 29 | **292** |
| + 120 extra functions | 45860 | 89 | **292** |
| bare, one block of filler | 5924 | 11 | **292** |
| bare, two blocks of filler | 6436 | 12 | **292** |

A ninefold range of memory, block counts from 10 to 89, and the
remainder never moves. So the remainder is not compiled size: a theory
of the form "compiled bytes plus some blocks" predicts it wanders, and
it does not.

Adding filler moves the reading in exact 512s. Three paddings measured a
block apart -- 473, 985, 1497 -- each sits on a boundary, so 512 bytes
of filler cost 512 bytes of memory. The rate is 1:1.

---

## Globals are the remainder

Declare one global and the remainder moves by that global's storage. The
block count does not change at all.

| declared | Δ remainder |
|---|---|
| `integer` | +8 |
| `float` | +8 |
| `key` | +8 |
| `vector` | +16 |
| `quaternion` | +16 |
| `string` (uninitialised) | +22 |
| `list` | +44 |
| four `integer`s | +16 |
| `vector` + `quaternion` + `list` | +68 |

Four integers costing 16 rather than 32 says storage is allocated with
some granularity rather than one slot per variable, and the three-global
case at 68 -- where the individual costs sum to 76 -- says the cost is
not simply additive either. Eleven measurements is not enough to
reverse-engineer Mono's allocator and no attempt is made here.

A **local** variable behaves as code, not heap:
`foo(){integer a; a = 1;}` costs 56, a multiple of 4, with no remainder
of its own.

A **string literal** lives in the code, not the heap. Only a fixed-size
reference is on the heap, and it does not grow with the literal:

| construct | cost | remainder mod 4 |
|---|---|---|
| `string sCNT = "<10 chars>";` | 94 | 2 |
| `string sCNT = "<40 chars>";` | 214 | 2 |
| `string sCNT = "<250 chars>";` | 1054 | 2 |

---

## Code is 4-aligned

A construct's measured cost is `4n + k`, where `k` is what the heap it
allocates leaves over mod 4 -- the code is 4-aligned and the heap is
not, so only the remainder shows. For anything declaring no globals,
`k` is zero and the cost is a multiple of 4 exactly:

| construct | cost | mod 4 |
|---|---|---|
| `foo_CNT(){}` | 48 | 0 |
| `foo_CNT(){llDie();}` | 368 | 0 |
| `foo_CNT(){llDie();llDie();}` | 396 | 0 |
| `foo_CNT(){llDie();llDie();llDie();}` | 428 | 0 |
| `foo_CNT(){llSay(0,"hello");}` | 392 | 0 |
| `foo_CNT(){integer a; a = 1;}` | 56 | 0 |

This is worth knowing when reading a benchmark: two answers for the same
construct that differ by 4 differ by **one quantum**, not by an
arbitrary amount. Measured across a dozen live runs the same construct
came back as 364 or 368 depending on what unrelated globals the script
carried, so 4 bytes is the resolution to expect.

---

## What a function costs

Measured over 64 declarations at once, so that anything paid once per
script is amortised away:

    a declaration        32 + one byte per character of its name
    + calling a library function     ~304 more
    + calling a user function        ~320 more

The declaration figure is a fit to three points and it is a good one:

| 64 empty declarations | total | each |
|---|---|---|
| 3-character names | 2248 | 35.1 |
| 16-character names | 3080 | 48.1 |
| 32-character names | 4104 | 64.1 |

`32.1 + name_length` reproduces all three to a tenth of a byte.

Calling anything costs about ten times the declaration. A function that
calls one library function costs 336; one that calls one user function
costs 352. Inference, not measurement: the ~300 is what it costs for a
function to stop being a leaf, and it buys a frame and its metadata. It
is paid by **every** such function, not once per script -- the harness
itself calls `llOwnerSay` four times, so a construct using `llOwnerSay`
is not a first use of it, and it still costs 312.

### Measuring one is not measuring many

The same figures taken from a single copy come out higher:

| | one copy | 64 copies, each |
|---|---|---|
| empty declaration, 7-character name | 48 | ~39 by the fit |
| function calling one library function | ~368 | 336 |

Ten to thirty bytes, consistently high. That is the once-per-script
component -- the name, and whatever a library function costs the first
time it is referenced -- being charged in full to the single copy and
spread across sixty-four in the other. It is not noise, and it is the
same thing `-1` mode and copy mode disagree about: one measures a copy
outright and the other measures an additional one.

---

## What a library call costs

All three of these matter, and all three are separable.

### The name, at a byte a character -- once

Void return, no parameters, one call site each:

| call | name length | cost above an empty function |
|---|---|---|
| `llDie` | 5 | 320 |
| `llResetTime` | 11 | 324 |
| `llResetScript` | 13 | 328 |
| `llBreakAllLinks` | 15 | 328 |
| `llReleaseControls` | 17 | 332 |

Twelve more characters, twelve more bytes, landing in 4-byte quanta.

That is the whole cost, however many times the function is called. With
**64** call sites instead of one:

| | total | difference |
|---|---|---|
| 64 × `llDie()` | 21504 | |
| 64 × `llReleaseControls()` | 21516 | **12** |

Twelve again, not 64 × 12. The name is stored once per script and the
call sites are free.

### The return type

All three names are 8 characters with no parameters, so only the return
type varies:

| call | returns | cost |
|---|---|---|
| `llGetKey` | key | 432 |
| `llGetPos` | vector | 436 |
| `llGetRot` | rotation | 440 |

Four bytes apart, ordered by type size. And note the level: a call with
a return value costs about 110 more than a void call of the same name
length. Having a return value costs more than which one it is.

### The parameters

| call | shape | cost |
|---|---|---|
| `llGetPos` | 8 chars, no params, returns vector | 436 |
| `llSetPos` | 8 chars, one vector param, void | **436** |
| `llSleep` | 7 chars, one float param, void | 328 |

A vector in costs exactly what a vector out costs. A float parameter is
worth about six characters of name.

---

## User-defined calls compared with library calls

Names behave the same way for both, and the difference is elsewhere.

### A name costs a byte a character, once, either way

Sixty-four user declarations differing only in the length of their
names -- the amplification matters, because eleven characters at a
single declaration disappears into the 4-byte quantum and this does
not:

| 64 declarations | total | vs the 3-character case | a byte per character predicts |
|---|---|---|---|
| 3-character names | 2248 | | |
| 16-character names | 3080 | +832 | 64 × 13 = **832** |
| 32-character names | 4104 | +1856 | 64 × 29 = **1856** |

Exact, twice. A user function's declaration carries its name at a byte
a character, exactly as a library reference does.

### Call sites are free for both

Sixty-four call sites of one user function, only the callee's name
length varying:

| | total |
|---|---|
| callee named 3 characters | 22552 |
| callee named 32 characters | 22580 |

Twenty-eight bytes for twenty-nine extra characters -- which is the
callee's single declaration, not the sixty-four calls. The library side
gives the same answer, 12 bytes for 12 characters across 64 call sites.

So a name is stored **once**. For a user function that is its
declaration; a library function has no declaration, so it is charged on
first use.

### A user call site costs about 16 bytes more

| 64 functions, each calling one thing | each |
|---|---|
| a library function | 336.0 |
| a user function | 352.4 |

Matched at a single call site the two are within a quantum -- 328
against 324 for void with no parameters, 360 against 364 for one vector
parameter -- so this only shows up at scale. The one place a single
pair did show a difference was returning a value: 356 for a user
function against 384 for a library one, seven quanta.

---

## What this means for a benchmark

**The two modes answer different questions, and both are right.** `-1`
mode measures one copy outright -- including the ~300 its function pays
for calling anything, and the 24 a library call costs once. Copy mode's
`Size` measures what an *additional* copy costs, which excludes the
shared 24. For `foo_CNT(){llDie();}` those are 368 and 344.

**Constructs are not independent.** Identical string literals are
shared, so a construct measured at 1054 bytes for one copy costs 540 for
each one after it. Give each copy a different literal and the two
converge.

**A construct's cost depends on its surroundings, and the effect is not
small.** `foo_CNT(){llGetPos();}` measured 484 against one base script
and 424 against another -- sixty bytes apart, for the same code. The
second base script already contained a function returning a vector, so
that machinery was paid for before the construct was measured. The same
effect at a smaller scale: a construct costs 24 bytes less measured
against a script that already calls the same library function, and the
empty-function baseline itself moved between 40 and 48.

There is no context-free answer. A figure is only comparable against
another taken against the same base script, which is why every number
in this document says what it was measured against.

---

## What `--parts` buys, and where it stops

The padding search looks for one byte in a 512-byte block: the pad at
which the reading steps up to the next block. A bisection finds it in
nine rounds and spends nine runs doing it.

`--parts=N` cuts the range into N parts a round instead of two, which
takes N-1 readings -- one per division between the parts -- and takes
them all at once. A round is one wait however many scripts are in it, so
the trade is runs for rounds: ceil(log_N 512) rounds instead of nine.
Powers of two divide the block evenly and are the natural choice, but
nothing requires one.

It is the lease size as well, and there is nothing else to decide: a
round runs one script per division and one more object holds the script
being measured, so the objects a benchmark wants ARE its parts, and
slgod keeps track of them. There used to be an `--objects` beside it,
which after this change could only ever have said something the search
would then have had to work around.

Measured on Agni on 2026-08-22, one avatar, `--no-cache` so every run
paid for its own search, the statement `llSin(1.0);`:

| `--parts` | avatars | scripts/round | rounds | runs | time | s/round |
|---:|---:|---:|---:|---:|---:|---:|
| 2  | 1 | 1  | 26 | 26  | 34s | 1.31 |
| 4  | 1 | 3  | 18 | 34  | 24s | 1.33 |
| 8  | 1 | 7  | 14 | 50  | 21s | 1.50 |
| 12 | 1 | 11 | 14 | 57  | 22s | 1.57 |
| 16 | 2 | 15 | 14 | 70  | 22s | 1.57 |
| 24 | 2 | 23 | 12 | 94  | 26s | 2.17 |
| 32 | 3 | 31 | 12 | 100 | 25s | 2.08 |

Copy mode, one avatar, the same statement: 29 rounds and 58s at four
parts, 23 and 54s at eight, 23 and 54s at twelve.

Two separate things stop it, and they stop it at different places.

**The scripts stay cheap, up to a point.** Dividing the time by the
rounds gives what a round cost: 1.31s for one script, 1.33s for three,
1.50s for seven, 1.57s for eleven and for fifteen. Fifteen scripts at
once cost 20% more than one, not fifteen times more, and parallelism
being nearly free is what makes the flag worth having.

It stops being free somewhere past that: 23 scripts a round cost 2.17s
and 31 cost 2.08s, about a third more than 15. Still cheap for the
scripts -- 31 at once for 1.3 times what 7 cost -- but no longer free.

**The rounds stop falling.** ceil(log_N 512) is 9, 5, 3, 3, 2, 2 for N
of 2, 4, 8, 16, 32, 64, so eight parts and sixteen cost the same rounds
and so do thirty-two and sixty-four. The measured 14 rounds at both 8
and 12 parts is that arithmetic and not a limit anywhere in the code.

**And the search is not the whole benchmark.** In `-1` mode the rounds
come to two searches plus a fixed eight -- the base runs, the walk, the
confirmations and the re-reads -- which is why 26 rounds become 18, 14
and then 12 offline at 32 parts rather than falling towards zero. Even
an infinitely wide search would leave 10.

So the knee is at **eight parts**, and the measurements past it say so
plainly: 21s at eight, 22s at twelve and at sixteen, 26s at twenty-four,
25s at thirty-two. The round count does fall -- 14 to 12 -- and the
rounds cost more than the fall is worth. Four times the parts, four
times the scripts, and three seconds SLOWER.

`--parts=2` is not a special case any more, only the worst one: a
two-part round is a bisection sent one script at a time, so the search
declines it and the bisection underneath does the work. The same
happens if a lease comes back smaller than was asked for -- the search
cuts its parts down to the objects it actually got, rather than sending
several rounds and calling them one.

## What a copy after the first costs, without a big script

A construct pays part of its cost once and shares it, so what C copies
cost is that once-paid part plus C marginal ones. Copy mode gets the
marginal cost by amortising: it builds a script with as many copies as
will fit -- up to 512 -- and fits a line through two counts. `-1
--extra=N` gets it from two small scripts instead, one copy and N+1
copies, measured at the same padding. The difference is N marginal
copies and nothing else, because the once-paid part cancels.

Three numbers come out where copy mode gives one:

| | |
|---|---|
| `Size` | what one copy costs outright -- the shared part plus one marginal |
| `Marginal` | what each copy after the first costs |
| `Shared` | the difference: what the construct pays once |

Measured on Agni on 2026-08-22, `llSin(1.0);` at eight parts,
`--no-cache`:

| | rounds | runs | time | answer |
|---|---:|---:|---:|---|
| `-1` | 6 | 44 | 11s | Size 380 |
| `-1 --extra=8` | 6 | 66 | 15s | Size 380, Marginal 47, Shared 333 |
| copy mode | 23 | 77 | 53s | Size 47 |

**The two agree on 47**, which is the result that matters: the marginal
cost measured from a nine-copy script is the one measured from a script
with up to 512 in it.

The rounds are identical with and without `--extra`. The two searches
are anchored at the same padding and narrow in lockstep -- a round
divides a range by parts exactly, so both take three rounds whatever
their answers are -- and the second rides in the first's rounds. It
costs 22 more scripts and four more seconds, which is the rounds being
wider and the nine-copy scripts taking longer to compile, not more
waiting.

Why this is worth preferring: installing 128 copies took 5.07s, 256 took
12.70s, and 512 was refused outright. The copy-count ladder that finds
how many will fit is most of copy mode's cost, and none of it is spent
here.

### N should be a multiple of 4

`--extra=1` is the cheapest possible measurement -- two scripts one copy
apart -- and it gives the wrong answer. Sweeping N for the same
statement and turning each `Marginal` back into a total size
(`380 + N x Marginal`):

| copies | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| size | 380 | 428 | 476 | 520 | 568 | 616 | 664 | 708 | 756 |
| step | | +48 | +48 | +44 | +48 | +48 | +48 | +44 | +48 |

Copies cost **48, 48, 48, 44** repeating. That sums to 188, which is
exactly four copies of 47 -- and 47 is what N of 4, 8, 12, 16 and 20 all
report. It is the 4-alignment above showing through: a copy whose real
cost is not a multiple of 4 is charged a little more or less than its
neighbours, and only a multiple of four copies averages it out.

So `--extra=1` reports 48 and `--extra=2` reports 48, both confidently
and both wrong by one byte, while 3, 5, 6, 7 and 9 report 46.67, 47.2,
47.33, 46.86 and 47.11.

A second construct, `foo_CNT(){llDie();}`, says the same about multiples
of 4 and something worse about everything else:

| N | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 12 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `llSin(1.0);` | 48 | 48 | 46.67 | **47** | 47.2 | 47.33 | 46.86 | **47** | **47** |
| `foo_CNT(){llDie();}` | 348 | 342 | 344 | **343** | 344 | 342.7 | 343.4 | **343** | **343** |

Every multiple of 4 agrees, in both. But N=1 is off by 5 for the
function, not by 1: the error is the per-copy offset from the straight
line, `(d(N+1) - d(1))/N`, and while `d` has period 4 for both -- which
is why multiples of 4 cancel -- its amplitude belongs to the construct.
About 2 for the statement, about 5 for the function. So the rule is a
multiple of 4, and failing that a LARGE N, since whatever the amplitude
is it is divided by N: the function is off by 5 at N=1 and by 0.4 at
N=7.

One thing that does not fit. The function's offsets run 0, +5, -2, +3,
and 4-alignment alone cannot produce a spread of 7 -- the residues cycle
correctly mod 4, but the magnitudes are larger than alignment explains.
Something else varies from copy to copy, plausibly a per-function cost
that depends on position. Not established, and worth knowing before
trusting a marginal cost measured at small N.

The fraction is the tell, and it is worth noticing that it is not a
complete one: 8 copies came to 376, which divides by 8 exactly, so
`--extra=8` reports a whole 47 for the right reason while `--extra=2`
reports a whole 48 for the wrong one. A whole number is not by itself
evidence that the copies agree; a multiple of 4 is what makes it one.

If `Marginal` does not divide evenly it is printed as a fraction and
`Shared` is not reported. Both readings are exact to the byte, so the
only way the division comes out uneven is that the copies do not all
cost the same -- a fact about the code under test rather than a rounding
error.

### Confirming a remembered padding

A padding is remembered in a file and confirmed rather than trusted: two
readings, at the padding and one byte above it, which have to be a block
apart. Confirming costs one round where searching costs seven, so the
saving is large -- and the question is what a confirmation that FAILS
costs, since it is a round spent before the seven.

Measured live on 2026-08-22, `-1` mode at eight parts:

| | rounds | runs | time |
|---|---:|---:|---:|
| no cache at all | 14 | 50 | 21s |
| padding holds | 8 | 27 | 12s |
| padding holds, readings carried | 6 | 27 | 10s |
| padding wrong | 15 | 51 | 22s |
| padding wrong, readings carried | 15 | 59 | 22s |

A failed confirmation costs 15 rounds against 14 for no cache at all --
a fifteenth, not the third it appears to be when a search is counted as
its three rounds of narrowing rather than the seven rounds it really is.
The narrowing is the part that runs in parallel; the anchor reading, the
walk onto the crossing and the two readings that confirm it are four
more rounds that do not.

**The readings carried** are the last two rows. The confirmation's round
has room in it, and what `-1` mode wants next is known before the
confirmation answers: the one-copy reading at that same padding, and the
first round of the search above it. Sending them in the confirmation's
round costs no extra runs at all -- 27 either way -- because they are
exactly the readings that were going to be asked for. Eight rounds
become six.

When the padding turns out to be wrong they are wasted: 59 runs against
51, and the same 15 rounds and 22s, because the waste is scripts and not
rounds.

The first version of this bet the other way, carrying the opening of the
search a failed confirmation would need. That saved 3s on the failure
and cost 0.7s on every success, which pays only if a remembered padding
is wrong about a quarter of the time. Eight independent searches of one
shape returned the same answer, so it is not, and betting on the failure
measured slower overall.

### What is not measured here

Twelve was where the measurements above stopped, because `autobench`
took its objects from a single avatar and an avatar has twelve. It does
not any more: it asks the pool for N places and takes them wherever they
are, several avatars at a time, holding a session per object the way
`automate` does (see [slots.md](slots.md)). Naming an avatar still gets
that avatar's, because that is then something a person asked for rather
than something the program assumed.

So 32 parts is now a matter of having 32 objects logged in rather than a
restriction in the code. The measurements above have not been retaken
across avatars.

Whether readings from different avatars are comparable was the doubt
worth having, because every size here is a difference of two readings
and Second Life runs different simulator versions on different channels
-- two avatars can be in regions with two LSL compilers.

On this grid on this day they agree. Every row of the table above
reported `Size: 380`, over one avatar, two and three, and the rows that
spread did not agree less than the rows that did not. That is one
statement measured once and it is not a proof that the compilers are
always the same; it is the reason the spreading is worth doing.

Asking for more objects than the daemon's avatars have between them is
refused at once, saying so and saying how many there are, rather than
waiting or quietly taking fewer.

## The filler, and what a jump costs

The padding filler has to be able to emit any number of bytes, exactly,
because a padding search asks for one pad after another and every size
reported is a distance between two of them. It has two pieces:

| bytes | code |
|---:|---|
| 0 | `jump Z; @Z;` |
| 1 | `i + i + i;` |
| 2 | `jump Z; @Z; i;` |
| 3 | `i + i + i + i;` |
| 4 | `jump Z; @Z; i + i;` |

An even pad spends the jump/label pair and `pad/2` of the two-byte `+i`
terms; an odd one spends `(pad+5)/2` terms and no pair. Both come to
`5+pad` bytes. `integer i;` backs the chain and is part of the harness,
not the filler, so that it is a constant rather than a step at whichever
pad it first appeared at.

That the pair costs **exactly 5 bytes** was previously inferred. It is
measured now, because the whole scheme rests on it: an even pad and the
odd pad above it are built out of different pieces, so if the pair were
4 or 6 the two parities would drift apart and the staircase would not be
one byte a step.

Live on 2026-08-22, the base script read 3876 at pads 476 through 480
and 4388 at 481 through 483 -- one step, in the right place, across the
even-to-odd seam. And a full block higher it read 4388 at 991 and 992,
4900 at 993 and 994: the next boundary at **992 = 480 + 512**, exactly a
block away, after 512 pads made of about 256 fillers of each parity. A
pair costing anything but 5 could not land it there.

### What this replaced

The pair used to be spent only when the pad was odd. Pads 1 and 3 then
both came out as the bare pair and measured the same 5 bytes -- two
paddings a search could not tell apart -- so the search had to start
above them, at a minimum pad of 5. Every padding this program printed
carried that 5, putting them in [5, 517) for a block that is [0, 512).
Moving the pair to the bottom costs the same 5 bytes in every script,
where a constant belongs, and buys back both: every pad from nought is
expressible, and a padding is a number inside the block it describes.

## Method, and what it does not cover

Every figure was taken with `autobench` against Agni on 2026-08-08,
using three avatars in parallel and `--no-cache` so each measurement
included its own padding search. Paddings were found independently and
agreed: eight separate searches on the unmodified base script all
returned 473.

Not covered, and worth knowing:

- **The allocator itself.** The remainder tracks globals, but the exact
  rule -- why four integers cost 16 and three assorted globals cost 68
  when their individual costs sum to 76 -- is not established here.
- **The ±4.** Its most likely source was the padding filler, which
  spent a `jump Z; @Z;` pair only on an odd pad, and nearly every
  padding these searches found was odd. The pair is in every script now
  (see [the filler](#the-filler-and-what-a-jump-costs)), so if that was
  the source it cancels; whether it did is still not confirmed, and
  these figures were taken before the change either way.
- **The compile ceiling.** A base script with 9000 bytes of preamble was
  refused with "Internal server compile error", as was a padding of
  2009. The practical limit is lower than the 62KB `autobench` assumes
  when estimating a copy count.
- **Why a returned value costs a library call 28 bytes more than a user
  call.** One matched pair, measured once.
- **Whether any of this is stable.** These are measurements of one grid
  on one day. The 512-byte block and the 1:1 filler rate are structural
  enough to rely on; the constants are not.
