`link` joins objects standing in the region into one.  The first named
is the root, its name becomes the object's, and `unlink` is the way
back.

    link chair "left leg" "right leg"

Each argument is one object, named by the word the region calls it or
by its key, so a name with a space in it has to be quoted.  That is the
opposite of `unlink`'s rule, which joins its arguments into one name,
and the two differ because the commands do: this one has a list to read
and that one has a single thing to find.

## Naming one object twice is refused

A prim cannot be linked to itself, and the simulator would take such a
message without comment: nothing would happen, and the report would say
so in numbers rather than in words.  So a repeated name is a typo and
is treated as one.

A name that several objects answer to is refused as well, with their
keys printed, since linking the wrong one is not something that undoes
itself.

## The report is read back, not counted

What is printed afterwards is what the region says the object is now,
rather than a count of the arguments given.  Linking brings a linkset's
prims along, so joining two things can make an object of seven -- and
that is exactly the case worth telling somebody about.

It waits for the linking to have happened rather than reporting the
request and stopping.  What says it worked is the children naming the
root as their parent, which arrives when it arrives.

## Options

**-w, --wait** *SECONDS*

How long to let the region describe itself.  Without it, thirty.

## Examples

Three things into one, the chair first so the object is a chair:

    link chair "left leg" "right leg"

A thing whose name is shared, named by key instead:

    link sign d8467e57-...

See also: `unlink`, `objects` for the names and keys to give it, `dump`
for what the linkset then looks like written down, and `take`, which
brings a linkset in as one item.
