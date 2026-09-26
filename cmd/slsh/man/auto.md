`auto` is about the worn objects that slrun and slbench run their
scripts in.  With nothing asked it reports what this avatar has: how
many of the objects are worn, and therefore how much can run at once.
With `-n` it sets that many up, and prints which object went to which
slot.

    auto

Nothing else in this shell needs it.  It is here because the answer
to "why will only eight scripts run at a time" is a number nothing
else prints.

The objects are worn on the HUD points, because nothing else wants
those and they are no part of how the avatar looks.  There are only
eight of them, so the first eight objects have one each and the rest
double up.  That is what the listing after `-n` is for: past the
eighth slot the only way to see the pool is arranged as intended is
to read which point each one landed on.  Twelve is this avatar's
whole share.

One object runs one script at a time, so what is worn is what can
run.  A benchmark's width is slbench's own and moves with its flags;
there is nothing here to work a count of benchmarks out from.

The pool they come out of is the daemon's rather than this avatar's.
Twelve is what each hosted avatar contributes, and a request for
more is answered out of as many avatars as it takes.  What this
command reports is one avatar's share, and naming an avatar confines
a run to that share.

A run asks for a number of objects and gets that many or none.  One
that cannot be served waits for enough of them to come free rather
than starting narrower than it asked for.  A run that names an avatar
the daemon does not host, or one that is logged out, is refused at
once, since there is nothing to wait for.

## Options

**-n** *COUNT*

Set up this many of the objects, rather than only reporting.  Without
it, how many are worn is printed.  The listing after it says which
object went to which slot.

## Setting up while something is running

Setting up takes this avatar's whole share before it does anything,
and refuses if any of it is busy.  Wearing things is not something to do
underneath a benchmark: an attach replaces what is on the point, and
a run whose object went away reports nothing useful about why it
failed.

Getting fewer objects than were asked for is not a failure.  Fewer
objects means less at once, and the report says so in those words.

## Examples

    auto
    auto -n 12

See also: `worn` for the same objects as attachments, `wear` and
`detach` for moving one by hand, and `agents` for which avatar this
is being asked of.
