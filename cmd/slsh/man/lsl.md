`lsl` is the language this simulator implements, from the simulator
itself: every function with its arguments, return type, energy and
sleep, every constant with its type and value, every event with its
arguments, and the type names.  It is what a viewer's own script
editor colours and completes from -- slsh has no editor -- and it
comes from the machine that will run the script rather than from a
page somebody edited.

    lsl llSetLinkPrimitiveParams

A function this does not list does not exist here.  A constant whose
value differs from what a compiler believes is a bug in the
compiler.  When the grid gains something, the difference can be read
off rather than guessed at.

With nothing said it prints one line: the version of the format and
how many of each kind there are.  A word after it searches every
kind for names containing that word, without regard to case.  A
function's line carries its energy and sleep where either is not
zero, and says where it is deprecated or needs god mode.

The document the simulator sends also names the control keywords --
if, else, for, jump and the rest -- and nothing prints them.

## Options

**-f, --functions**

List the functions.

**-c, --constants**

List the constants.

**-e, --events**

List the events.

**-t, --types**

List the type names.  It is described as the types and keywords,
which is what was meant rather than what happens: it prints the type
names alone.

**-a, --all**

List everything.  Without a word, that is the listing rather than
the one-line summary.

**-m, --machine-readable**

One record a line, tab separated, for a program.  Without it the
listing is aligned for reading.  A bare `-m` with no kind flag is
every kind, not the summary.

## The form meant for a program

One record a line, tab separated, no alignment and no totals.  The
first field always says which kind of record it is, so one loop
reads whichever kinds were asked for, and a reader meeting a kind it
does not know can skip the line rather than misread it.

    function  NAME  RETURN  ENERGY  SLEEP  FLAGS  ARGTYPE,...
    constant  NAME  TYPE    VALUE
    event     NAME  ARGTYPE,...
    type      NAME

A function that returns nothing says `void` rather than leaving the
field empty.  The flags are a comma-separated set that may be empty,
and so is a list of argument types, so a function of no arguments
ends its line with an empty field.  Fields are never reordered and
never removed: anything new goes on the end, so a reader that splits
and takes the first few keeps working.  Nothing is quoted or escaped
because nothing in the source contains a tab -- which is also why
the tooltips are not in it, since a tooltip could contain anything.

## What it costs to ask

The whole language is about half a megabyte, so it is fetched once
and kept.  Asking again costs one small request for the id that says
whether it has changed, and, where it has not, nothing else.  Kept
by the session, which means for as long as this shell is running and
no longer: a fresh slsh pays for the document again.  A script that
wants the language more than once is a script to run in one shell,
which is what `.` is for.

## Examples

    lsl llSetLinkPrimitiveParams
    lsl -m -f > functions

See also: `features`, where that id comes from, and `caps` for
whether the capability behind all this was granted at all.
