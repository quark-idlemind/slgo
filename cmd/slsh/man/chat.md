chat turns the keyboard over to talking.  Command mode is the outer
one, because a shell is wanted for listing and moving things far more
often than for conversation, and this is the way in; the escape key is
the way back out.

    chat

With nobody named it enters chat mode on whichever conversation is
current.  With somebody named -- a name, a key, or a number from the
last listing -- it opens the conversation with that person first and
switches to it, which is the same thing `im` does when there is nothing
to say.  Everything after `chat` is the name, not a message.  Naming
somebody opens a conversation; `im` with words after the name sends
one.

The prompt is where the next line will go: `Local` for the region's
open chat, or somebody's name for an instant message to them.  A count
in brackets may stand in front of it, as in `(2) Local>`, and that half
belongs to `waiting` rather than to the conversation: it is how many
things are waiting for an answer, and it stands in front of the command
prompt in the same way.

## A half-typed line is put aside, not lost

Leaving a mode puts the line being typed aside and brings back the line
that was put aside on the way in, so an interrupted sentence is still
there afterwards.  Nothing is sent or run by the switch.

There is one place to put a line rather than one for each mode, and the
multi-line answer a text box takes uses it too, so a sentence left half
typed in chat can come back at the `1 text>` prompt.  It comes back on
the line to be edited, and nothing is sent until it is entered.

## What the keys do here

Tab moves to the next conversation, rather than completing a word as it
does at a command prompt, and it does nothing at all while something is
half typed -- moving then would send the line somewhere nobody meant.
Ctrl-C on an empty line returns to command mode rather than leaving the
shell.

Up and down walk what has been said, which is a history of its own: a
command is never offered at a chat prompt, where it would be said out
loud rather than run, and a remark is never offered at a command one.
A line of nothing, a repeat of the line before it, and a line abandoned
rather than entered are left out.  A line the grid refused is kept,
since that is the one most wanted back.

The history belongs to the shell rather than to the conversation, and
that is what makes a line said in the wrong place recoverable: tab to
the conversation it was meant for, press up until it comes back, and
press return to send it there.  What comes back lands on the line to be
edited, and nothing is sent until it is entered.

The escape key is what leaves, and slsh takes a key of its own if
escape is awkward on a particular terminal: `slsh --escape=^G` for one
run, or `set escape ^G` to have it remembered.  Enter and tab are
refused, since chat already takes both for itself.  Escape is read
when the shell starts, so a setting changed at the prompt is the next
shell's escape key and not this one's -- `set escape` says so, and
says which key is in force.

## Everything heard is printed in either mode

Arriving chat has nothing to do with what the keyboard is for, so
remarks, instant messages and notices are printed above whatever is
being typed whichever mode is in force.  Time-stamped, and then one
character that says which kind of line it is: `<` for heard, `>` for
said, and `*` for a notice.  The first two are the pair that has to be
told apart, since a remark and a reply read the same in a column; the
third is the shell speaking rather than anybody in the world.

## A friend logging in or out is a notice

When a friend who lets this avatar see them online logs in or out, a
notice says so, worded as the viewer words it:

    15:07:40 * Example Resident is online
    15:11:02 * Example Resident is offline

At login the grid names every friend already on, and each of those is
announced too, one line each.  A shell that attaches to a daemon that
has been up a while was not listening then and announces only what
arrives after it; `friends` says who is online now.  A name the shell
does not have is asked for and waited for briefly, and the id is
printed in its place when it does not come.  Nothing here has been
measured on the grid yet: see doc/friend-notices.md.

## Control characters are shown, not obeyed

What is heard is somebody else's text, and a terminal acts on the
escape sequences in whatever it is sent: they can clear the screen,
write over the line that says who spoke, set the window title, or put
something on the clipboard.  So slsh shows every control character in
caret notation instead of sending it -- ESC is `^[`, BEL `^G`, DEL
`^?` -- with `M-` in front for the eight-bit ones, `M-^[` for U+009B.
The same goes for everything else the grid names and slsh prints:
avatars, objects, parcels, groups, inventory, the prompt when it names
somebody, and the transcript.

A newline is still a line break and a tab still a tab.  A carriage
return on its own would put the cursor back over the start of the
line, so it is shown as `^M`; one immediately before a newline is
taken with it as the one line break it was meant as.

Output redirected to a file is the one thing left as it arrived, since
a file is what a name is copied out of to be typed back in, and `^[`
typed back is not the name.  Such a file is data rather than a screen:
read it with `less`, which shows control characters rather than
sending them, and not with `cat`, which sends them.

## Examples

Enter chat mode and talk to the room:

    chat

Enter it already talking to somebody:

    chat Example Resident

See also: `talk` for the conversations tab moves between, `im` for one
message without leaving command mode, and `say` for one remark out
loud.
