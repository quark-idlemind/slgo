say puts one line into open chat without leaving command mode.  `chat`
is for a conversation, and this is for the single remark somebody wants
to make in the middle of doing something else.

    say the gate is open

Everything after the flags is what is said, joined with spaces, so
nothing needs quoting except a word the shell would otherwise take for
something of its own.  It goes out at ordinary speaking volume and is
heard by whoever is near enough, and the line is printed back marked
with where it went -- `Local` for open chat, `channel N` for any other
-- since nothing else on the screen would show for it.

Nothing says whether anybody heard it.  Chat is not acknowledged by
anything, so a line that went out is the whole of what can be reported,
and a line the circuit refused is not printed as though it had been
said.

## The channel is what a script listens on

Open chat is channel zero and is where people talk.  A script listens
on a channel of its own, and saying something there is how a shell
drives one: the object hears the text with this avatar named as the
speaker, exactly as it would hear a remark made out loud.

A negative channel is the usual choice for a script, because a viewer
has no way to speak on one, so nothing a passer-by types can set the
script off by accident.

The reach is chat's reach either way.  A negative channel is a private
word rather than a region-wide one: heard nearby, not heard a hundred
metres up.

## Options

**-c** *CHANNEL*

The channel to say it on.  Without it, open chat -- channel zero.  A
negative one reaches scripts and carries at most 254 bytes, refused
rather than truncated.

## Examples

Say something out loud:

    say the gate is open

Start a script that is listening on a channel of its own:

    say -c -4242 start

See also: `chat` and `im` for talking to one person rather than to the
room, `waiting` for what a script puts up when it wants an answer back,
and `touch` for the other way of setting one off.
