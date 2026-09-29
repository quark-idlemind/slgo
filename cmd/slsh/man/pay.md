pay gives an avatar L$.  It is the viewer's pay dialog: the money goes
from this avatar to the other one at once, and the grid says so to
both.

    pay Example Resident 5 thanks for the lamp

The avatar comes first, then the amount, then a reason, which is
optional and is everything after the amount.  The avatar is named the
way `im` and `give` take one: a name, a key, or the number beside a
name in the last listing.  An amount is a whole number of L$ from 1 up,
and may be written `L$5`.  A reason is at most 127 bytes, which is what
the viewer's dialog takes, and the grid echoes it to the other side.

Only avatars are paid.  A key the grid has no name for is refused
rather than paid, since it may be an object's, and a group is not
paid from here at all.

## Options

**--yes**

Pay without asking.  At a prompt, `pay` asks first -- `pay Example
Resident L$5? [y/N]` -- and pays only on `y` or `yes`; anything else,
Ctrl-C included, pays nothing.  Where nobody is at a prompt to answer
-- `slsh -c`, `slsh -f`, a file run with `.`, a session read from a
pipe -- it refuses without `--yes`, and nothing is paid.  The next
line of a script is not an answer to a question it never saw.

## What it says

`paid Example Resident L$5; L$115 left`, with the grid's transaction
id, when the grid said it was done.  A refusal is printed with the
reason: the grid's own sentence, such as `Insufficient funds.`, or the
reason slgod gave -- see below.

A payment is never sent twice.  When the grid's answer does not come
within fifteen seconds, the balance is read again and compared with the
balance read just before paying.  Down by exactly the amount is paid,
and says the answer was lost; the same as before is not paid; anything
else is not known, and both balances are printed so that somebody can
work it out.  A payment made to this avatar in the meantime is its own
answer and is counted as what it is.

A balance short of the amount is refused before anything is sent, as
the viewer refuses one.

## What a profile allows

Paying is off unless the avatar's profile turns it on, and slgod
checks every payment a program sends against what the profile says:

    pay       = on
    pay_max   = 10
    pay_daily = 10
    pay_to    = Example Resident

`pay_max` is the most one payment may be and `pay_daily` the most all
of them may come to in any 24 hours, both 10 when not given.  `pay_to`
names an avatar that may be paid, one line each, by name or key, and
`pay_to = *` is anybody.  With no `pay_to` at all nobody is paid, so
`pay = on` alone pays nobody and the refusal says to add one.

The same rules hold what a program buys.  Buying an object, a parcel
or a pass to one, joining a group -- by request or by accepting its
invitation -- and publishing a classified are checked as payments are:
refused unless `pay = on`, each held to `pay_max`, all of them counted
with payments in `pay_daily`, and whoever is paid -- the object's
owner, the parcel's, the group -- in `pay_to`.  A price nobody told the
session is not guessed: it refuses, and says what was not known.
Creating a group, claiming land, and letting a script take L$
(`PERMISSION_DEBIT`) are refused whatever the profile says, having no
figure to check.  Uploads are not checked.  A refusal for somebody not
in `pay_to` names them, with their name when it is known.

A viewer attached through slgod is not held to any of this: that is a
person at the viewer's own pay dialog, and the rules are about
programs.  A shell started with `--direct` checks the same rules
itself.

## Examples

    pay Example Resident 5
    pay Example Resident L$5 thanks for the lamp
    pay --yes 3 1

See also: `balance`, `lookup` or `who` for finding the person, and
`give` for an item rather than money.
