`halt` stops the avatar where it is, ending any walk under way.  It
takes nothing.

    halt

It says whether there was a walk to end:

    halted a walk
    nothing was walking; sent a stop anyway

The stop goes either way.  An avatar can be moving for a reason this
shell did not see -- a walk asked for by another program on the same
avatar, most often -- and that is the avatar somebody reaching for
`halt` wants stopped.  The walk it ended says `cancelled (halted)` to
whoever was waiting for it.

An avatar does not stop dead.  Measured on 2026-09-24, one walking at
full speed came to rest about a metre on from where it was when the
stop was sent.

## Examples

    halt

See also: `walk`, and `face`, which also ends a walk.
