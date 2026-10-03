# The client package, and what it used to do

The comments in `client/` say what the package does. This page is why,
where the why is a story: what the code did before, and what was wrong
with it.

## Counting what is dropped

A `Conn` counts what it throws away because nobody was reading fast
enough, and `OnDrop` says what each one was. It used to do neither: a
chat line that never arrived left no way for anybody to know one had
gone missing. A line lost there looked exactly like a line the script
never said, which for a benchmark is a number that is quietly wrong
rather than a run that failed -- and that is the one thing a
measurement must not do.

## The name a client gives slgod

`Name` is the basename of the running binary. Every client used to say
"slgo", which made the name useless for the one thing it is for:
slgod's own "in use by" message read "slgo, slgo" when two different
programs were attached.

## Closing while the relay runs

`Close` does not close the relay channels itself while there is a
`recvLoop`, because `recvLoop` is the only thing that sends on them, and
closing a channel under its sender is a race at best and a panic at
worst. That is what `Close` used to do, on the one path every client
takes to hang up.

`attach` says the relay is running under the lock, and refuses there a
connection already closed, so that `Close` either sees the loop about
to start and waits for it, or gets there first. No test reaches the refusal, and
none can through the package's own doors: `Close` shuts the transport
down, so an attach that got as far as the lock -- stream opened, first
packet read -- ran entirely before it. What is left is the window
between that read and the lock, which is exactly what the guard is for.

## Capabilities after the session changes

A `Conn` used to keep the capability list the attach answer gave it
for as long as it was attached. But slgod re-establishes a session
under an attached stream, and a teleport or a crossing gives the same
session another region's capabilities; each arrives as a region change,
and after one the list described somewhere the avatar no longer was.
slbotd's log on 2026-09-26 had, nine times, a line of the form

    cannot put the outfit back on: sl: listing <id>: agent: inventory <id>: status 404: cap not found

after slgod had logged an avatar in again.

So a region change now marks the list, and the next `HasCap` or `Caps`
asks the daemon again, through `Refresh`. And a named capability
answered 404 "cap not found" is made once more after a `Refresh`.

What the list does not do is choose the URL. slgod looks a capability
up in the session it holds at the moment of the request, so a 404
there means that session's own capability was unknown to the grid at
that moment: a session the grid had already ended and slgod had not yet
replaced, or one whose new region's capabilities had not arrived yet.
That is read from the code, not measured. slgod now waits out the
second during a move
([doc/daemon.md](daemon.md#a-capability-asked-for-during-a-move)). A request made again after
the replacement reaches the new session; one made while the old is
still in place is answered 404 again, and it is the caller's own
retrying -- slbotd's outfit passes, every 20 seconds for five minutes
-- that outlasts it.

## A connection that comes back

slgod proves a transport connection, not a gRPC channel: it refuses
every method but Login with `Unauthenticated`, "not authenticated: call
Login on this connection first", on a connection that has not run the
handshake, and the handshake is bound to that connection's TLS session.
`Dial` made one channel and logged in once. A gRPC channel with no calls
for its idle timeout, 30 minutes by default and not changed here, goes
idle and closes its transport; the next call opens a new one, with a new
TLS session that has never logged in. A transport re-established after
a network blip would be the same; that is inferred, not measured. That
the idle one does this was measured on
2026-10-02 against a running slgod, with a probe that dialled, called
`ListAgents`, waited and called again:

    22:29:19 at once: agents=8 code=OK err=<nil>
    23:00:19 after 31m0s: agents=0 code=Unauthenticated err=rpc error: code = Unauthenticated desc = not authenticated: call Login on this connection first
    23:00:19 again: agents=0 code=Unauthenticated err=rpc error: code = Unauthenticated desc = not authenticated: call Login on this connection first

After 5 seconds idle, the same probe's second call was answered. The
effect seen in production on 2026-10-01 was slbotd's control
connection, dialled once, refusing every `Host` from then on, so that an
avatar needing to be hosted again never was until slbotd restarted.

So a `Conn` from `Dial` now logs in again when a call is refused
`Unauthenticated`: it runs the handshake on the same channel, which
binds to the new TLS session, and makes the call once more. The retry is
safe for every method, those that change something included, because
slgod's interceptor refuses before the handler runs; that is read from
the code, not measured. Calls refused together cause one login between
them: each notes a count of logins before it calls, and one that finds
the count moved on has been logged in for already and only retries. If
logging in again fails, the call returns the refusal, with the reason
added, and `status.Code` still says `Unauthenticated`.

Streams are not retried. A stream on a lost transport ends and the
`Conn` finishes as it always did; whoever dialled attaches again. A
login that fails is also why slbotd drops its control connection on
`Unauthenticated` as it does on `Unavailable`: the next call dials
afresh.
