features prints what the simulator says about itself, in one request:
whether mesh may be rezzed and uploaded here, how many attachments an
avatar may wear, how many groups it may join, which voice server the
region runs, and the id of the LSL syntax it implements.  Where caps
says which services this session may use, this says what the region on
the other end of them will do.

With a word after it, only the features whose names contain that word
are printed, matched without regard to case.

## Why it is worth asking rather than trying

Every one of these is otherwise discovered by doing something and being
refused, and a refusal names what failed rather than why the region was
never going to allow it.  The answer is also per region and regions
differ -- a test grid answers differently from a main grid one, which
is the reason the question exists at all.

The id of the LSL syntax is the one to know about: it changes when the
language changes, which is how a cached copy of the language can be
told to be stale without fetching half a megabyte to find out.  lsl
uses it for exactly that.

## What is printed, and what a list in the value means

The names are the simulator's and so are the values, printed as they
arrived.  Where a value is itself a set of settings rather than a
number or a flag, its keys are printed instead of its contents, which
keeps a line a line; that a name has keys under it is the useful half,
and what is under them is a question for whatever needs it.

The set of names is not ours and has grown several times, so a name
nothing here has ever heard of is still printed rather than dropped.

## It describes the region, not the parcel

A feature the region allows can still be refused by the land under the
avatar, which grants building and object entry parcel by parcel.  A
region that says it will rez mesh is saying nothing about whether this
particular parcel will.

## Examples

    features
    features mesh

See also: caps, lsl, and look for the rest of what the region said.
