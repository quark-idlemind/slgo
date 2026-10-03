`answer` says yes to one of the things `waiting` lists, by its number.

What yes means depends on what is asking: a teleport is taken, an
offered item is taken into Objects, a friendship is accepted, a script
is granted the whole of what it asked for, a group invitation is
joined.  A dialog has buttons, so the word after the number picks one,
either by its number in the listing or by its label.

    answer 1
    answer 3 Yes
    answer 3 2

A dialog with one button needs no word after the number; that button
is pressed.  Several buttons and nothing after the number is refused,
with the labels listed.

A script is granted every bit it asked for, including debit, which
spends this avatar's money.  The listing names the bits; `no` refuses
the whole request.

An offered item taken here lands in Objects, not in the folder the
shell is in.  `accept` is the command that puts one in the current
folder.

## A text box

Everything after the number is the answer:

    answer 4 the north gate

An empty word is an empty answer, which is how a text box is
submitted blank -- some scripts ask for exactly that, to keep a
setting as it is:

    answer 4 ""

With nothing after the number it opens the multi-line form, where
`^D` on an empty first line sends an empty answer too.  Lines are
typed until `^D` at the end of one ends them -- in front of anything,
`^D` deletes forward as it does at the prompt.  ESC starts the answer
again and `^C` sends nothing.

A text box carries 254 bytes and no more.  A longer answer is refused
outright rather than cut short, and the refusal says how many bytes
were offered against the 254 there was room for.

## Options

**--file** *PATH*

Answer a text box from this file, newlines and all.  It is refused on
anything that is not a text box.  An empty file is an empty answer.
Without it the answer is typed after
the number, or as several lines at the prompt.  A file is the way to
send newlines; it is not a way to send more than 254 bytes.

The flag goes in front of the number.  `answer 4 --file notes.txt`
takes `--file notes.txt` as the text of the answer.

## A group invitation with a fee has to name the fee

This is the only kind that can spend money as part of saying yes.  The
fee is in the waiting listing, and where it is not zero it has to be
typed back before the answer will go:

    answer 3 L$50

`L$50` and `50` are the same amount.  An amount that does not match
what the invitation said is refused, with both figures named.  Typing
the fee does not cap what the group charges -- the simulator takes the
group's fee whatever is sent.

An invitation that did not say what joining costs has to be answered
with an amount too: `answer N L$0` if the invitation is free, and the
reply says plainly that the group will charge its fee whatever was
typed.  An invitation that said there is no fee is the one that takes
a bare `answer N`.

The profile has to allow it when there is a fee: accepting an
invitation is a purchase to the rules `pay` describes, refused unless
`pay = on`, with the fee held to `pay_max` and `pay_daily` and the group
in `pay_to`.  A group known to be free is joined without any of that,
and one whose fee nobody has said is refused.  Through slgod the refusal reaches slsh as a
message it does not wait for, so nothing is printed for it.

Nothing answers a join, so `group` is what says whether it worked.

## Accepting a teleport waits for the avatar to arrive

A teleport out of this region is followed rather than fired off: the
session moves with the avatar, and `answer` returns once it is
standing in the region it was invited to.  A line is printed before
the request goes, saying whose offer is being taken, and another
afterwards saying where the avatar ended up.  The offer names who made
it and whatever they typed with it; the region is not in the message
at all.  A lure to somewhere in the same region arrives at once.

Waiting stops after thirty seconds.  Running out is not usually a slow
grid: a teleport asked for while another is under way is answered with
nothing whatever, so it is what accepting a second offer too soon
looks like.  `where` says whether the avatar moved.

Everything this shell knew about the region left behind is gone on
arrival -- see `tp`, which says what goes and what survives.

## Examples

    answer 1
    answer 3 Yes
    answer 3 L$50
    answer --file notes.txt 4

See also: `waiting`, `no`, `ignore`, `accept` for friendship and
inventory offers by name, and `tp` for going somewhere nobody offered.
