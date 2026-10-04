`auto` is about the worn objects that slrun and slbench run their
scripts in.  With nothing asked it reports what this avatar has: how
many of the objects are worn, and therefore how much can run at once.
With `-n` it sets that many up, and prints which object went to which
slot.  The subcommands lay the worn ones out, or take them off.

    auto
    auto -n COUNT
    auto reset
    auto show
    auto hide
    auto clear
    auto delete

Nothing else in this shell needs it.  It is here because the answer
to "why will only so many scripts run at a time" is a number nothing
else prints.

## The layout

Every object is worn on HUD Bottom Left, and an avatar has twenty-four
of them.  Each is a 0.05 m cube.  An avatar may wear 38 attachments in
all, HUD and body points together, so there was never a reason to
spread them over the eight HUD points; they are small and are moved
instead.

Idle, an object is parked: off the screen, a metre to the left of the
corner and a metre below it.  `show` lays them out along the bottom of
the screen, one cube's width apart, so that twenty-four make a row 1.2 m
long and you can see them.

One object runs one script at a time, so what is worn is what can run.  A
benchmark's width is slbench's own and moves with its flags; there is
nothing here to work a count of benchmarks out from.

The pool they come out of is the daemon's rather than this avatar's.
Twenty-four is what each hosted avatar contributes, and a request for
more is answered out of as many avatars as it takes.  What this
command reports is one avatar's share, and naming an avatar confines
a run to that share.

A run asks for a number of objects and gets that many or none.  One
that cannot be served waits for enough of them to come free rather
than starting narrower than it asked for.  A run that names an avatar
the daemon does not host, or one that is logged out, is refused at
once, since there is nothing to wait for.

## What counts as worn

The count is the objects linked in the Current Outfit folder together
with the attachments the region has described to this session.  Neither
is whole: after a login the region does not describe HUD attachments, so
the second list can miss ones that are plainly worn, and the folder
cannot know about an object put on without a link.

## Options

**-n** *COUNT*

Set the number of auto objects worn to COUNT, from 1 to 24; anything
else is refused with that range.  `auto` to `auto COUNT` are worn on
Bottom Left, each parked and sized.  Any of them found on another point
(the old spread, Center, Center 2) is taken off and worn again on Bottom
Left.  Auto objects worn beyond COUNT are taken off, with their links in
the Current Outfit folder.  The listing after it says which object went
to which slot.

It wears only what fits.  The avatar's attachments are counted against
the region's limit, 38 unless the region says otherwise, less one slot it
keeps free for a viewer's bridge, so `-n` stops at 37 worn in all.  The
region refuses a wear past its limit without a word, so the count is
made first and what does not fit is not asked for.  If fewer than COUNT
fit, it says how many it wore and why, for example `wore 7 of 12: the
avatar wears 37, keeping 1 slot free`.  If a wear times out anyway, it
stops there and says the region refused at the limit, after at most one
wait of forty seconds.

## Subcommands

**reset**  Every worn auto object back to 0.05 m and parked.  The count
is unchanged.

**show**  Tile them along the bottom of the screen, slot *i* at
`<0, -0.025 - 0.05 i, 0.025>` from Bottom Left.  The size is left alone.

**hide**  Park every worn auto object.  The size is left alone.

**clear**  Take off every auto object worn, on whatever point, and its
link in the Current Outfit folder.  The appearance is rebaked once at the
end, as `detach` does.

**delete**  `clear`, then move every auto item in the Objects folder,
named exactly `auto` or `auto` and a number, to the Trash.  The Trash is
not emptied.  Items with other names, such as `autobench` or `automate`,
are never touched.

An object the region has not described, or one worn on another point, is
taken off and worn again on Bottom Left first by `reset`, `show` and
`hide`, so that it can be moved.

## Moving things while something is running

Everything that moves or removes the objects takes this avatar's whole
share before it does anything, and refuses if any of it is busy.  Wearing
things is not something to do underneath a benchmark: an attach replaces
what is on the point, and a run whose object went away reports nothing
useful about why it failed.

A run's own lease puts on only objects the avatar has room for, and keeps
four attachment slots free; when it stops short it says `wore K of N:
keeping 4 slots free` and the run goes on with what it has.  An object
already worn is used where it is, so a layout made with `show` survives a
run.  One the run has to put on is parked.

Getting fewer objects than were asked for is not a failure.  Fewer
objects means less at once, and the report says so in those words.

## Examples

    auto
    auto -n 24
    auto show
    auto hide
    auto delete

See also: `worn` for the same objects as attachments, `wear` and
`detach` for moving one by hand, and `agents` for which avatar this
is being asked of.
