`unlink` takes a linked object apart.  It is the way back from `link`,
and the reason `take` is not the whole answer to an object that should
never have been one thing: a linkset comes into inventory as a single
item, so the taking apart happens in the world.

    unlink garden chair

It names one thing, and its arguments are joined back into one name, so
`unlink a lamp` means what it looks like.  A key may be given instead,
which is the way past a name several objects answer to.

## A root means all of it; a prim means that prim

Naming the root takes the whole object apart.  Naming one prim of it
frees that prim and leaves the rest linked.

That is the viewer's pair of behaviours -- clicking an object selects
the linkset and Unlink frees all of it, while editing linked parts
selects one prim and Unlink frees only that.  The root is never among
the prims being freed: it has no parent to lose, so naming it frees
nothing.

Naming a child is a real thing to want rather than a mistake to guard
against.  A prim of a linkset has its own name, `objects -c` prints
them, and a linkset is otherwise all or nothing.

## What the pieces are called afterwards

A linkset answers to its root's name, so a name means a four-prim thing
while the four prims are linked and one prim afterwards.  The pieces
keep the names they had inside it, which for anything built by hand is
often `Object` for every one of them -- so an unlink can leave several
things in the region answering to a single word, and every command that
takes a name refuses an ambiguous one rather than picking.

That is why this lists the pieces with their keys instead of printing
a line saying it worked.  The root is in that listing with the rest,
although it was never freed: what is wanted afterwards is a handle on
each of the things now standing there, and the root is one of
them.  The keys are the only handle certain to work, and the moment
they are wanted is the moment the object comes apart.  Where two
pieces share a name it says so as well.

Freeing one prim out of a set that stays a set is the exception, and
is one line rather than a listing: only one piece came out, and what
is left is still one object under its root, so the line names the prim
that left, the object it left, and how many prims that object has
now.  The keys are in it all the same.

## Options

**-w, --wait** *SECONDS*

How long to let the region describe itself.  Without it, thirty.

## Examples

    unlink garden chair
    unlink d8467e57-...

See also: `link`, `objects` and its `-c` for the prims inside an
object, `take`, and `dump` for writing a linkset down before disturbing
it.
