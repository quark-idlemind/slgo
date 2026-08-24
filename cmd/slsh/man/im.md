im says something to one person.  A remark out loud reaches whoever is
standing near, and this reaches the person named and nobody else,
wherever they are.

    im Example Resident the lantern is rezzed

Who is named by a name, by a key, or by the number beside a name in the
last listing of people, which is what `who`, `friends` and `lookup`
print -- and `profile` too, where the name it was given answered to
several people and it listed them to be picked from.  With words after
the name they are the message, sent at once, and the shell stays in
command mode.  With nothing after it the conversation is opened and the
shell enters chat mode, which is what `chat` on somebody does.

Either way the conversation joins the list that tab cycles between, and
stays there for the rest of the session.

## How much of the line is the name

A Second Life name has two words in it, so the name is the longest run
of one or two words at the front of the line that answers to somebody,
and everything after that run is the message.  Two words are tried
first: `im Example Resident hello` says "hello" to Example Resident,
because the pair names them; `im Example hello there` says "hello
there", because "Example hello" names nobody and the run shortens.

A name is matched on either half and on the start of it, so reading the
first word as the name and the rest as the message would send the last
name as the first word of what was said -- to the right person, reported
as a success, with the mistake visible only to whoever received it.

A name that could be several people is refused with all of them named
rather than shortened, since every shorter run matches at least as
many.  Typing more of the name is the answer.

## Who a name may be

The session's own cache is asked first, and then whoever is standing in
the region.  The grid's search is not: it reaches people who are
nowhere near and whose names merely resemble what was typed, and a name
guessed at wrong here delivers a private message to a stranger.
`lookup` is that search, and the number it prints is what to give this.

Nothing comes back to say a message was read, or that it was delivered
at all.

## Examples

    im Example Resident the lantern is rezzed

Open the conversation and go on talking in chat mode:

    im Another Resident

See also: `chat`, `talk`, `say`, and `lookup` or `who` for finding
whoever is meant.
