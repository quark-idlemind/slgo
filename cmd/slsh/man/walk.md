`walk` walks the avatar in a straight line to a place in this region,
and waits for it to get there.  The place is two numbers, X and Y, in
the region's own metres, or a name: somebody nearby, or an object.

    walk 128 64
    walk 128 64 25
    walk Example
    walk lantern

A third number is accepted and ignored.  The ground decides the height;
a walk does not climb or fly.

A name is tried against the people in the region first, the way `im`
takes one: somebody's whole name, in any case, and otherwise the start
of any part of it; then against objects, by the whole name.  A name
that fits two people, or two objects, is refused with what it matched
rather than guessed at.  Walking to a name stops a
metre and a half short unless `--within` says otherwise, since half a
metre from somebody's middle is walking into them.

What comes back is where the avatar came to rest, to the centimetre, and
how far that is from where it was sent:

    arrived at 84.78, 173.40, 4001.20, 0.08 m from 84.70, 173.40, in 900ms

## Options

**-w, --within** *METRES*

How close is close enough, measured flat.  Without it the walk aims at
the place itself and counts anything within half a metre as there.
Given, it aims a little inside that distance rather than at the place.

**-r, --run**

Run rather than walk.  The daemon asks the simulator to run for the
length of the walk and asks it to walk again afterwards.

**-t, --timeout** *D*

Give up after this long, as `500ms` or `10s`.  Without it there is no
limit: a walk that stops getting anywhere ends anyway, as blocked.

**-p, --progress**

Print how it is going four times a second: the time, where the avatar
is, how fast it is going, which way it faces and how far it has left.

    0.25s  85.79, 173.38, 4001.20  3.20 m/s  facing 164° W  1.09 m to go

## It walks round nothing

The walk is a straight line.  Whatever is in the way -- a wall, a tree,
somebody standing there -- is walked into, and the walk ends when the
avatar has got no closer for two seconds:

    blocked at 87.00, 174.16, 4001.20, 2.84 m short of 87.00, 177.00: no closer for 2.1s

That is deliberate.  Going round things is left to whoever is steering,
which here is whoever is typing: walk to somewhere beside the obstacle,
and then on.

## How it ends

`arrived` is the only end the command counts as success.  Every other
end is printed as the command's failure, with where the avatar is:

    blocked       it stopped getting closer
    cancelled     something else ended it; the reason is in brackets
    out of region the place is not in this region, and nothing moved

A walk is never taken across a region border, so a place outside 0 to
256 on either side is refused before anything moves.  An avatar that is
sitting is refused too -- `stand` first -- and so is one somebody is
driving from a viewer through the same daemon.

So is one whose forward control a script has taken and does not pass
on, which a vehicle or a game can do to whoever is in it:

    a script has taken the forward control and does not pass it on

A walk holds that control, and the script would be given it and the
avatar would stand still until the walk called itself blocked.  `where`
says which controls scripts hold.  One taken while the walk is going
ends it, cancelled, with the reason below.

The reasons a walk is cancelled are its own words: `timeout`,
`superseded` when another walk or a `face` took over, `halted` when
`halt` stopped it, `left the region`, `seated`, `a viewer took over`,
`a script took the forward control`, `session ended`, and `overshot` for a walk that came to rest outside
the distance asked for three times running.

## Interrupting it stops the avatar

The walk is steered by the daemon, not by the shell, and it stops the
avatar the moment the shell stops waiting for it.  `^C` during a walk,
or the shell exiting, or the connection to the daemon breaking, all
leave the avatar standing about a metre on from where it was, which is
how far it goes after being told to stop.

A second `walk` from another shell on the same avatar takes the first
one over without stopping in between, and the first one ends
`cancelled (superseded)`.

## How close it gets

Measured on 2026-09-24, on flat ground: walks of six metres came to rest
one or two centimetres from the place, and runs about ten.  A walk of a
metre or less is rougher.  The shortest walk there is from a standstill
is about a metre, so a place half a metre away may be walked past and
walked back to.

## Examples

    walk 128 64
    walk --run 128 64
    walk -w 3 Example
    walk -p -t 20s 30 200

See also: `face` to turn without moving, `halt` to stop a walk from
elsewhere, `where` for where the avatar is, `who` for who is near, and
`tp` for anywhere a walk cannot reach.
