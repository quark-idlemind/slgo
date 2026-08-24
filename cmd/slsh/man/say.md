say puts one line into open chat without leaving command mode.  It is
the short form of chat mode: "chat" is for a conversation, and this is
for the single remark somebody wants to make in the middle of doing
something else.

Everything after the flags is what is said, joined with spaces, so
nothing needs quoting except a word the shell would otherwise take for
something of its own.  It goes out at ordinary speaking volume and is
heard by whoever is near enough, and the line is printed back marked
with where it went -- "Local" for open chat, "channel N" for any other
-- since nothing else on the screen would show for it.

Nothing says whether anybody heard it.  Chat is not acknowledged by
anything, so a line that went out is the whole of what can be
reported, and a line the circuit refused is not printed as though it
had been said.

## The channel is what a script listens on

Open chat is channel zero and is where people talk.  A script listens
on a channel of its own, and saying something there is how a shell
drives one: the object hears the text with this avatar named as the
speaker, exactly as it would hear a remark made out loud.

A negative channel is the usual choice for that, because a viewer has
no way to speak on one, so nothing a passer-by types can set the
script off by accident.  It travels a different way from ordinary chat
-- as a script dialog reply, which is a message with a short field --
and the length limit that comes with it is real: 254 bytes, refused
rather than truncated.

The reach is chat's reach either way.  Measured against a script
listening on a negative channel: heard at two metres, not heard with
the listener a hundred metres up, heard again on coming back.  A
negative channel is a private word rather than a region-wide one.

## Examples

Say something out loud:

    say the gate is open

Start a script that is listening on a channel of its own:

    say -c -4242 start

See also: chat and im for talking to one person rather than to the
room, waiting for what a script puts up when it wants an answer back,
and touch for the other way of setting one off.
