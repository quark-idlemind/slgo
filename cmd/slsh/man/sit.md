Sits the avatar down: on an object when one is named, and on the ground
when nothing is.  It waits for the simulator to say what became of the
request, and what it prints is what happened -- what the avatar is now
sitting on, and where that left it.

    sit                     on the ground, where the avatar is
    sit greeter             on the object called greeter
    sit d8467e57-...          on the object with that key

## A sit moves the avatar

This is the part to know before anything else.  A sit is not a request
to sit on something already within reach: the simulator picks the
avatar up and puts it on the seat, over whatever is in the way, from
several metres off.  A box seven metres away seats as readily as one
half a metre away.  Eleven metres is refused.  A near thing is sat on
and a far one is refused in words.

Standing up afterwards does not undo the journey.  The avatar seated on
that box seven metres away is left about six metres from where it had
been standing.  That is why a second line says where the avatar now is,
in the words `where` and `tp` use:

    sit greeter
    sat on "greeter" d8467e57-... (local 8360)
    Testville at 35, 72, 2001

The local id in that line is the simulator's answer rather than an echo
of the question.  An object is a linkset, and which prim of one it
seats an avatar on is its business.

A ground sit moves nothing, so the position line after one says the
avatar is where it was.

## When the grid says no

A refusal arrives as an alert, and the grid's own words are quoted
whole:

    sit "the long bench"
    slsh: sit: sl: the simulator refused the sit: "No room to sit
    here, try another spot."

Those words are printed as they arrived.  They are not always
accurate -- a key that names no object at all is refused by the grid
with a sentence about the region -- and they are still the only thing
said.  A key this region has never described is refused here first, in
the same words `touch` and `take` use for it, before anything is sent.

Silence is a third thing, neither a yes nor a no: the request may have
taken effect without this hearing about it.  The wait before it gives
up is fifteen seconds.

## One object, and only one

An object is named by the word the region calls it or by its key, the
way `touch` and `take` name one.  A key is taken as itself.  A name is
looked up among the objects the region has described, and a name that
two of them answer to is refused, with both keys, rather than guessed
at.  Guessing costs more here than it does for `touch`: the wrong guess
moves the avatar.

A name with a space in it is one argument, so quote it.  That is
stricter than `tp` and `landmark`, which join the rest of the line.

## Options

**-w, --wait** *SECONDS*

How long to wait for the simulator to answer.  Without it, fifteen
seconds.

## Examples

    sit
    sit greeter
    sit "the long bench"
    sit --wait 30 greeter

See also: `stand` for getting up again, `where` for the position this
prints, `objects` for finding the thing to sit on, `touch` for clicking
it instead, and `tp` for moving the avatar somewhere a sit cannot reach.
