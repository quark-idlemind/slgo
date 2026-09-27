# What slgod does for a session on its own

slgod holds a session whether or not any client is attached, and some
of what a session needs it does by itself, because nobody else is
there to. The comments in `server/` say what the package does. This
page is why: what went wrong before each rule, and what was watched
happening.

What each of these looks like to somebody running slgod is in
[doc/guide.md](guide.md), under "When home is down", "Where an avatar
was sitting", "The group an avatar acts as" and "Offers that arrive
while nobody is attached".

## Getting home

A profile may say `start = home`, which asks the login server to put
the avatar at its home position. That is a request and not a promise:
if the home REGION is down when the login happens, the grid puts the
avatar somewhere else entirely and says nothing about it afterwards.
What comes up is a session in the wrong place, with the wrong land
under it -- and for a lab whose avatars are meant to be standing on
their own parcel that is a working day spent wondering why nothing can
rez.

So a session that asked to start at home keeps asking to GO home until
it gets there, once a minute, for up to an hour. A region that was down
comes back and the avatar walks in on the next attempt with nobody
watching.

### How it knows whether it is home already

It does not, and cannot: nothing in the protocol answers "where is
home", and the daemon has never been told. What it can do is ask to go
there and read what the grid says, which is exactly what the first
attempt is for. Four answers, all of them measured in `sl` against
Agni -- the access refusal on a teleport to somewhere other than home,
and read here as meaning the same on the way home, which has not been
seen:

- a `TeleportFinish`, or a `TeleportLocal`: the avatar moved, so it is
  home now and there is nothing more to do.
- a `TeleportFailed` carrying `CouldntTPCloser`: the grid will not
  shorten a teleport that arrives where it started, which is the
  grid's way of saying the avatar is already there. Done.
- a `TeleportFailed` carrying `RegionTPAccessBlocked`: not home, and
  never going to be by asking. See below.
- anything else, or silence: not home and not going, so wait a minute
  and ask again.

So the cost is one teleport request per login, and what the grid does
with it depends on how exactly the login placed the avatar. Measured on
Agni on 2026-09-02, the first time this ran in service: three sessions
that had logged in at home were each answered with a `TeleportLocal`
-- a move inside the region -- and logged "home, after one attempt",
finishing within seconds and leaving all three at the coordinates they
had started at. The `CouldntTPCloser` refusal is real and was measured
the same day from the shell, going home while standing exactly on the
home point; which of the two answers comes back is the grid's
arithmetic and not something to depend on.

Either way the avatar ends up at home and the loop stops, which is all
this needs, and it buys not having to know where home is.

The loop decides on a refusal's key, and the grid's words are only for
the log. Deciding on the words is what it used to do, by searching them
for a copy of the key kept in `server/home.go`; the key is the grid's,
and it is written down once, in `agent`.

### When it stops without getting there

Asking again is worth doing only for an obstacle that goes away, and
the one this loop was built for does: a region that is down comes
back, in minutes. Not every refusal is that.

`RegionTPAccessBlocked` is the grid's key for a region this avatar may
not enter. It was measured for a maturity rating above what the avatar
may be shown, and the name says access in general; either way it is
the same answer every time it is asked, because nothing about the
avatar or the region changes by asking. So that refusal stops the loop
at once, with one line naming the key and the grid's sentence, and it
is remembered: a reconnect does not ask again. What clears it is a new
home being set through this daemon (see `forgetHome`), since the
refusal was about the old one, or the daemon restarting.

Anything else -- `no_host`, which is a region that is down, or
silence, or a key nobody here has seen -- is asked about once a minute
for an hour, and then given up on with one line saying so and naming
the last answer. An hour is well past how long a region takes to come
back; a refusal that has not changed in that time is not a region on
its way back up.

The hour is an hour of ASKING, counted across reconnects until the
avatar gets home. A session that reconnects every half hour would
otherwise start a fresh hour each time and never give up -- the same
loop for ever, by the back door. Time between loops, while there is no
session to ask with, is not counted, so a session that was down for a
day does not come back having spent its hour. Once the hour is spent,
each reconnect asks once more -- a fresh login is a fresh question, and
one request per login is what this has always cost -- and gives up
again at once if that is refused. Getting home resets it all.

What it says while it tries is the first answer, then any answer that
differs from the one before, then the line that gives up. A refusal
that never changes is two lines in all: one saying it is not home and
why, and one an hour later saying it has stopped asking. Saying it
every tenth attempt, as this once did, was too often to be quiet and
too rare to notice.

### Why a client teleporting stops it

Because the point is to put an avatar back where it belongs, not to
keep it there. Somebody who types `tp` has taken the wheel, and a
daemon that dragged the avatar home a minute later would be a
poltergeist: the shell would report an arrival and the avatar would
leave again by itself, with nothing on the screen to say why.

So any teleport a CLIENT asks for stops the loop for the rest of the
session, and so does one asked for at a viewer. They arrive by two
roads: a client's messages through `sendMessage`, and a viewer's down
its own circuit straight to the session, with the circuit telling
`ViewerSent` of each one it passes on. It stops on the request rather
than on an arrival, so a teleport that is refused stops it too: the
person meant to be somewhere else, and finding out they cannot is their
business.

A viewer being handed the session stops it as well, before the viewer
has asked for anything: a person at a viewer has the wheel. See
`ViewerAttached`.

A reconnect starts it again, and that is deliberate. A reconnect is a
fresh login with `start = home` in it, so the same question is being
asked again by the same means, and whatever a client did with the
previous session was about a session that no longer exists. What the
GRID said is another matter, and is kept: the hour of asking and an
access refusal both outlive a reconnect, for the reasons in the
section above.

A viewer still on the session is the exception: a reconnect is the
grid's doing and does not hand the wheel back. So before each attempt
the loop asks the viewer endpoint, and one that says a viewer is on
stops it for the rest of that session, as the other stops do.

## Sitting down again

An avatar that was sitting on something when its session ended comes
back standing. Nothing on the grid remembers the seat: a sit is a
request the simulator acts on and does not record, and the Current
Outfit folder -- which does record what an avatar is wearing -- has
nothing to say about where it is. So the remembering is the daemon's
to do, and it is worth doing there rather than in a client because it
is the daemon that logs the avatar in, including at its own startup
when no client is attached to notice.

### What is remembered, and when

The object's id, which is the thing that survives. A local id does
not: locals are the region's own numbering and are handed out afresh
every time, so the number an avatar was parented to last week names
something else entirely today.

It is learned two ways, because neither alone is enough.

`AvatarSitResponse` is the simulator telling the avatar that just sat
down where it has been put, and it carries the seat's OBJECT id already
resolved. That is the good source: it needs nothing looked up, it
arrives however the sit was asked for, and it is the simulator's own
word.

The watch is the other, and it is what notices STANDING UP -- there is
no message for that -- and any sit the first source missed. A seated
avatar is PARENTED to its seat, and the parent is in the region's own
description of the avatar, so both facts are there to be read.

The watch alone was tried first and is not enough on its own. The
parent is a LOCAL id, and turning one into an object means finding that
object in the region listing -- which is exactly what may be missing.
Measured: an avatar seated on a chair, with the chair absent from a
listing of 976 objects. The parent was known, the seat was not, and
there was nothing to write down. Why the chair was never described was
not established -- a packet thrown away as undecodable is the likeliest
reason -- and the object store now asks for a parent it has not heard
of, but a watch that depends on a description arriving is still at the
mercy of one that does not.

### Not knowing is not standing

The distinction the watch turns on. The region's description of this
avatar may be missing -- it has not arrived yet, or it was lost -- and a
watch that read that as "standing" would throw away a perfectly good
seat because it looked away at the wrong moment. So the watch has three
answers and not two: seated on this, standing, and no opinion. Only the
second forgets.

### Why the restore retries

A region does not hand over its contents at once. The avatar is in
world and able to move some seconds before the object it was sitting on
has been described, and a sit request naming an object the simulator
has not got to yet is answered with nothing at all. So the request is
repeated for a while and then given up on, which also covers the case
that matters more: the seat is genuinely gone, taken home by its owner,
and no amount of asking will bring it back.

### What counts as having sat down

The avatar being parented to something, rather than being parented to
a thing the daemon can put a name to. Measured, and the reason it is
written this way: a restart where the chair was absent from the region
listing throughout confirmed the sit only on the sixth and last
attempt, eighty-five seconds in, because each round was waiting to
recognise an object rather than to see the avatar sit down. It had
actually sat within seconds.

### Somebody else deciding is not a reason to stop watching

The homing loop stops for the rest of the session when a client
teleports, because its job is to put an avatar back where it belongs
and somebody who teleported has taken that decision. This is not that.
Its job is to remember, so a client sitting the avatar somewhere else
is not something to get out of the way of -- it is the next thing to
write down, and the watch does.

The RESTORE gets out of the way, and only the restore: an avatar
somebody has already sat down while it was asking is left where they
put it.

## Offers kept while nobody is attached

Somebody offers this avatar a teleport, an item, their friendship or a
place in a group, and the offer is an instant message carrying an id
that answers it and nothing else does. It arrives once. Before the
daemon kept them, it acknowledged one and relayed it to whoever was
attached, and with nobody attached it was acknowledged and dropped --
so the program somebody starts BECAUSE they were away was the one
program that could never be told, and "nothing waiting" read as an
answer when it was a blind spot.

### Why script dialogs are not kept

Script dialogs and permission requests are not kept. Both come from an
object in the region, both are almost always the result of something
an attached client just did, and whether an answer to one still reaches
it from another region has never been watched -- so a record of them
would be a list of questions that may no longer have anybody asking.

### Why nothing else drops one

An offer leaves the record when it is answered, when a newer one
replaces it, or when there is no more room, and that is all.

Nothing is dropped on a timer, because nothing in the viewer drops these
on one either: none of the five notifications has a duration in its
template (`skins/default/xui/en/notifications.xml`), and the group
invitation is marked persist, which carries it across a logout
(`llpersistentnotificationstorage.cpp`). Nothing is dropped when the
avatar changes region, because none of the five belongs to a region:
each is answered by quoting its id to the grid or to the person who made
it, never to an object that stayed behind. And nothing is dropped when
the daemon re-establishes the session, because the answers quote ids
the offer carried and not anything of the session's own. That last is
inferred rather than watched, from the grid delivering these same five
kinds to an avatar at login, out of offline storage, to be answered
with those same ids.

## The active group

A parcel usually grants "create objects" to a GROUP rather than to
individuals, and a login starts with NONE active. A viewer hides this by
storing the group in its settings and re-sending it every time, which
makes it feel like a property of the account; headless it is not. So an
avatar that builds happily through a viewer cannot rez a thing through
slgod, and the refusal blames the land -- the wrong place to look.

Settling it once, after logging in, is therefore not enough: a
reconnect is a FRESH LOGIN, with a new session id and no active group,
and an avatar that was building five minutes ago silently stops being
able to. That is a fault found in the field on 2026-08-07, on a session
that had reconnected.

So the resolved uuid belongs to the `Hosted`, which survives
reconnection, rather than to the agent, which does not -- and the
supervisor reapplies it every time it puts a new session in place.

### What the refusal looks like

Measured on Agni, in Pelmar Reach, with two avatars a couple of metres
apart:

    hobb       Pelmar Reach at 33, 75, 2001 / acting as group 488f7e57-...
    holt  Pelmar Reach at 31, 73, 2001

Rezzing a prim as hobb worked.  As holt it failed, with the simulator
saying "You cannot create objects here. The owner of this land does not
allow it. Use the land tool to see land ownership." -- which is untrue
as it stands and unhelpful as a hint.  The land does allow it; the
request simply arrived from nobody in particular.  Until slsh had its
`group` command there was no way to fix that from its prompt.

## A capability asked for during a move

A move -- a teleport, or a walk over a border -- tells everything above
the session that the avatar has arrived before it has asked the new
region for its capabilities, and until that answers, the set the
session holds is the region left's. `Server.Cap` looked a capability up
in whatever set the session held at the moment of the request, so a
request made in between went to a simulator the avatar had just left.
slbotd puts an avatar's outfit back 15 seconds after it attaches, and
slgod asks to take an avatar home 5 seconds after a login, so a restore
and a move can meet in that window. Read from the code, that is the
likeliest cause of the 404s "cap not found" in slbotd's log of
2026-09-26 ([doc/client.md](client.md#capabilities-after-the-session-changes));
it has not been watched happening.

So `Cap`, and `Status` for its list of capabilities, wait for a move
under way to finish first (`agent.WaitCaps`), as `sl.Direct.Refresh`
does, and as Firestorm does after a teleport before it uses the new
region's capabilities (`newview/llagent.cpp:4976-4995`). With no move
under way nothing waits.

The bound is generous on purpose: as long as a move is allowed to take,
ninety seconds, the figure of `sl.DefaultTeleportTimeout` and what the
move's own bounds add up to (thirty seconds for the new simulator's
handshake and sixty for the capability fetch, by default). The wait
ends with the move, so the bound is a backstop. A request whose wait runs out, or whose caller gives up, fails
rather than being sent to the region left; a status answers anyway,
with the list it has, since a status is wanted most when something is
wrong. A move that fails ends the session, and a request to a session
that has ended goes where it went before there was a wait.

What this does not reach: a request made after a teleport was asked for
and before the grid's `TeleportFinish` arrives, when no move has begun
on this side and the region being left still holds the avatar; and the
capability names an attach and the list of agents give, which are read
without waiting.
