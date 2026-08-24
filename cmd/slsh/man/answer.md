answer says yes to one of the things waiting lists, by its number.

What "yes" means depends on what is asking, and answer does the right
one: a teleport is accepted, an offered item is taken into Objects, a
friendship is accepted, a script is granted what it asked for, a group
invitation is joined.  A dialog is different only in that it has
buttons, so the word after the number picks one, either by its number
in the listing or by its label.

    answer 1
    answer 3 Yes
    answer 3 2

A text box is the other one that takes words.  Everything after the
number is the answer:

    answer 4 the north gate

and with nothing after the number it opens the multi-line form, where
lines are typed until ^D at the end of one ends them -- in front of
anything, ^D deletes forward as it does at the prompt.  ESC starts the
answer again and ^C sends nothing.  --file PATH takes the answer from
a file instead, newlines and all, which is the way to send something
written elsewhere.

Whichever of the three is used, a text box carries 254 bytes and no
more.  The reply travels in the field a button label travels in, and
the message template gives that field a one-byte length, so 255 with
the terminator is the whole of what there is.  A longer answer is
refused outright rather than cut short -- half an answer arriving is
worse than none -- and the refusal says how many bytes were offered
against the 254 there was room for.  A file is the way to send
newlines; it is not a way to send more.

## A group invitation with a fee has to name the fee

This is the only kind that can spend money, and a bare "answer 3" that
quietly paid a joining fee would be the shell deciding to spend
somebody's money for them.  So the fee is in the waiting listing, and
where it is not zero it has to be typed back before the answer will go:

    answer 3 L$50

That is not a payment instruction and it caps nothing -- the simulator
charges the group's fee whatever is sent.  It is the person saying the
number out loud, which is the only part of this a shell can honestly
ask of them.  An amount that does not match what the invitation said is
refused, with both figures named.

An invitation that did not say what joining costs is the harder case
and is treated as the dangerous one: it too has to be answered with an
amount, "answer N L$0" if you believe it is free, and the reply says
plainly that the group will charge its fee whatever you typed.

Nothing answers a join, so "group" is what says whether it worked.

## Accepting a teleport waits for the avatar to arrive

A teleport out of this region is followed rather than fired off: the
session moves with the avatar, and answer returns once it is standing in
the region it was invited to, which takes about half a second and has
been measured at five.  So a line is printed before the request goes,
saying whose offer is being taken, and another afterwards saying where
the avatar ended up -- read back, because a lure is the one teleport
whose destination nobody knows in advance.  The offer names who made it
and whatever they typed with it; the region is not in the message at
all.  A lure to somewhere in the same region arrives at once.

Waiting stops after thirty seconds.  Running out is not usually a slow
grid: a teleport asked for while another is under way is answered with
nothing whatever, for ever, so it is what accepting a second offer too
soon looks like.  where says whether the avatar moved.

Everything this shell knew about the region left behind is gone on
arrival -- see tp, which says what goes and what survives.

## Options

--file PATH answers a text box from a file.  A shell reads a line at a
time and a viewer's text box is a text editor, so the two shapes do not
match; naming a file is the way through that does not involve inventing
an escape for the newline and then having to escape the escape.

See also: waiting, no, ignore, and tp for going somewhere nobody
offered.
