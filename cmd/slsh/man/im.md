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

The session's own cache is asked first, then whoever is standing in the
region, and last the grid's own search -- the one `lookup` makes -- so
somebody on the other side of the grid can be named as plainly as
somebody standing here.  The search is made only when nothing nearer
knew the name, which is a line that was about to be refused anyway.

What the search finds is taken only when it is the name itself:
`Example Resident` in any case, or `example.resident` with the dot a
username has.  The search matches part of a name, and display names
too, so what it turns up is everybody the words resemble; taking one of
them would deliver a private message to a stranger.  So anything short
of the whole name is listed, numbered, and refused, and the number is
what to give this:

    im Example the lantern is rezzed
     1  Example Resident
     2  Example Wanderer
    slsh: im: nobody on the grid is called "Example", and the 2 listed
    have names like it; a number picks one
    im 1 the lantern is rezzed

A first name alone is not a whole name here, even for somebody whose
username it is, and neither is one row that is merely the closest the
search came.

A search that could not be made -- the grid not answering, or a session
never given the search -- finds nobody, and the refusal says so, since
that is the one part worth trying again.

Nothing comes back to say a message was read, or that it was delivered
at all.

## Examples

    im Example Resident the lantern is rezzed

Open the conversation and go on talking in chat mode:

    im Another Resident

See also: `chat`, `talk`, `say`, and `lookup` or `who` for finding
whoever is meant.
