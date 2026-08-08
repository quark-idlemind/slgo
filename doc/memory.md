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

A construct's measured cost is `4n + k`, where `k` is the heap it
allocates. For anything declaring no globals, `k` is zero and the cost
is a multiple of 4 exactly:

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

    empty function                              48
    a function that calls anything             ~300 more
    each further call site in that function     ~30

`foo_CNT(){}` costs 48. `foo_CNT(){llDie();}` costs 368. A second
`llDie()` in the same function adds 28, and a third adds 32.

The ~300 is paid by **every** function that calls anything, not once per
script. The control that settles this: the benchmark harness itself
calls `llOwnerSay` four times, so a construct using `llOwnerSay` is not
a first use of it -- and it still costs 312. Inference, not measurement:
the likeliest reading is that this is what it costs for a function to
stop being a leaf, and it buys a frame and its metadata.

Only a small part is shared. The same construct measured against a base
script that already calls `llDie` costs 344 rather than 368, so **24
bytes** is what a distinct library function costs once and then shares.

---

## What a library call costs

All three of these matter, and all three are separable.

### The name, at about a byte a character

Void return, no parameters:

| call | name length | cost above an empty function |
|---|---|---|
| `llDie` | 5 | 320 |
| `llResetTime` | 11 | 324 |
| `llResetScript` | 13 | 328 |
| `llBreakAllLinks` | 15 | 328 |
| `llReleaseControls` | 17 | 332 |

Twelve more characters, twelve more bytes, landing in 4-byte quanta.
The name is stored.

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

## User-defined calls are not library calls

The compiler treats the two differently, and the difference is the name.

### A user function's name costs nothing

Two user functions in the base script, called from the construct under
test, differing only in the length of their names:

| callee | name length | cost |
|---|---|---|
| `u1234()` | 5 | 328 |
| `u1234567890abcde()` | 16 | **328** |

Eleven more characters, no more memory. Against the library side, where
eleven more characters cost eleven more bytes.

So an `ll*` call **stores the name** and a user call does not.
Inference, not measurement: library functions are bound by name at run
time and user functions are resolved to a direct call when compiled.

### Otherwise they cost about the same, except on the return path

Matched for name length and signature, measured against the same base
script:

| shape | user | library | difference |
|---|---|---|---|
| void, no params, 5-char name | 328 | 324 | −4 |
| one vector parameter, 8-char name | 360 | 364 | +4 |
| returns a vector, 8-char name | 356 | **384** | **+28** |

The first two differ by one quantum, in opposite directions, which is
to say not at all. The third is seven quanta and is real: a library
call carries something extra for returning a value that a user call
does not.

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
