# Plans, as they were written

Nothing in this directory describes what slgo does today.

Each of these was written **before** the work it plans, in stages, and
then run.  A plan says what somebody intended on the day, and the
intent and the result differ in every one of them -- stage 0 was
usually a measurement taken to find out whether the rest of the plan
was possible, and several times it was not.  Where that happened the
document says so under its own heading rather than being quietly
corrected, which is the reason to keep them: the measurement and the
wrong guess it corrected are both in there, and neither is written
down anywhere else.

The authorities on current behaviour are, in order:

| | |
|---|---|
| the code | what it actually does |
| `slsh`'s man pages | `man tp`, `man sit`, `man parcel`, ... |
| `doc/guide.md`, `doc/slsh-guide.html` | the two user guides |
| `doc/memory.md`, `doc/slots.md`, `doc/login-parameters.md` | measured reference that is still true |

## What is here

| | |
|---|---|
| `teleport.md` | cross-region teleport.  Built; `tp` and `man tp` are the answer now |
| `neighbours.md` | child circuits to the neighbouring regions, and the border that turned out to be a wall |
| `sit.md` | sitting and standing up.  Built; `man sit`, `man stand` |
| `parcel.md` | parcels, the land under the avatar.  Built; `man parcel` |
| `landmark.md` | landmarks.  Built; `man landmark` |
| `many-avatars.md` | several avatars in one slgod.  Built |
| `viewer-frontend.md` | slgod as a viewer frontend.  Partly built; `slsh viewer` is stage 8 of it |
| `two-viewers.md` | two viewers on one session.  **Never built**, and costed rather than planned |

Two of these open by saying "nothing here is built", which was true
when the line was typed and is the thing this directory exists to stop
somebody believing.  The stage headings underneath say `(done)`.
