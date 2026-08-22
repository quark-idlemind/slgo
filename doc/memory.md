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

| `--parts` | scripts/round | `-1` rounds | `-1` runs | `-1` time | copy rounds | copy runs | copy time |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 2  | 1  | 26 | 26 | 34s | 39 | 39 | -- |
| 4  | 3  | 18 | 34 | 24s | 29 | 53 | 58s |
| 8  | 7  | 14 | 50 | 21s | 23 | 77 | 54s |
| 12 | 11 | 14 | 57 | 22s | 23 | 87 | 54s |

Two separate things stop it, and they stop it at different places.

**The scripts stay cheap.** Dividing the time by the rounds gives what a
round cost: 1.31s for one script, 1.33s for three, 1.50s for seven,
1.57s for eleven. Eleven scripts at once cost 20% more than one, not
eleven times more. Parallelism is very nearly free here, which is the
result that makes the flag worth having at all.

**The rounds stop falling.** ceil(log_N 512) is 9, 5, 3, 3, 2, 2 for N
of 2, 4, 8, 16, 32, 64, so eight parts and sixteen cost the same rounds
and so do thirty-two and sixty-four. The measured 14 rounds at both 8
and 12 parts is that arithmetic and not a limit anywhere in the code.

**And the search is not the whole benchmark.** In `-1` mode the rounds
come to two searches plus a fixed eight -- the base runs, the walk, the
confirmations and the re-reads -- which is why 26 rounds become 18, 14
and then 12 offline at 32 parts rather than falling towards zero. Even
an infinitely wide search would leave 10.

So the knee is at **eight parts**: below it every doubling is worth
about 20% of the wall clock, above it the round count plateaus while the
per-round cost keeps creeping up. Twelve parts measured slightly SLOWER
than eight in `-1` mode, for seven more runs.

`--parts=2` is not a special case any more, only the worst one: a
two-part round is a bisection sent one script at a time, so the search
declines it and the bisection underneath does the work. The same
happens if a lease comes back smaller than was asked for -- the search
cuts its parts down to the objects it actually got, rather than sending
several rounds and calling them one.

### What is not measured here

Twelve is where this stops because `autobench` takes its objects from a
single avatar and an avatar has twelve. The next step down -- 32 parts,
two rounds a search -- needs 32 objects, which needs readings taken
across several avatars at once. The slot pool grants exactly that (see
[slots.md](slots.md)); `autobench` does not yet ask for it, so
`--parts=32` is refused at once, naming the avatar and the twelve it
has, rather than waiting or quietly taking fewer.

Whether a round of 31 scripts stays as cheap as a round of 11 is
therefore unmeasured. The trend through 11 is mild and the mechanism is
the sim compiling them concurrently, but 31 has not been watched.

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
- **The ±4.** Its most likely source is the padding filler: an odd pad
  spends a `jump Z; @Z;` pair charged at exactly 5 bytes, and nearly
  every padding these searches found was odd. Not confirmed.
- **The compile ceiling.** A base script with 9000 bytes of preamble was
  refused with "Internal server compile error", as was a padding of
  2009. The practical limit is lower than the 62KB `autobench` assumes
  when estimating a copy count.
- **Why a returned value costs a library call 28 bytes more than a user
  call.** One matched pair, measured once.
- **Whether any of this is stable.** These are measurements of one grid
  on one day. The 512-byte block and the 1:1 filler rate are structural
  enough to rely on; the constants are not.
