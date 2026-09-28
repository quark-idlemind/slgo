# A guide to slgod, slrun, slbench and slbotd

Four command-line programs for working with Second Life without a
viewer.

| | |
|---|---|
| `slgod` | a daemon that holds grid sessions, so everything else starts instantly |
| `slrun` | runs LSL scripts and prints what they say |
| `slbench` | measures how much script memory an LSL construct costs |
| `slbotd` | holds several avatars, takes commands from inside the world, and can answer conversation with a local model |

There is a fifth, `slsh`, an interactive shell for inventory, the
region around you and chat. It has a guide of its own:
[handbook/slsh-guide.html](../handbook/slsh-guide.html). Everything under "Connecting"
below applies to it as well.

---

## Connecting

There are two ways to be connected, and `slrun`, `slbench` and
`slsh` all support both. `slgod` has neither flag and needs neither: it
is the thing the others connect to.

**Through `slgod`** (the default). `slgod` holds the connection to the
grid. Programs attach to it, so they start instantly and leave the
avatar logged in when they exit. Several programs can use one session
at the same time.

**Directly**, with `--direct` (`-d`). The program logs in itself and
holds the session for as long as it runs. Quitting logs the avatar out.
This needs no daemon, but every start pays for a fresh login -- several
seconds, against none.

### Options come before the first argument

Every one of these programs stops reading options at the first thing
that is not one, and anything after that is taken as an argument. There
is no warning:

    slrun --rez script.lsl      rezzes a prim
    slrun script.lsl --rez      takes "--rez" for a second filename

The second is not silent, as it happens -- there is no file called
`--rez`, so it fails before connecting to anything. It would be silent
if the option took a value that looked like a filename, which is the
case worth watching for.

`slgod` is Go's `flag` package rather than getopt and stops in the same
place, which is at least louder about it, because its arguments are
profile names: `slgod example -v` reads `-v` as a second profile to log
in, and says

    -v: NOT hosted: agent: profile "-v": open .../slgo/-v: no such file or directory

before carrying on with the one it could read.

### Saying where slgod is

    slrun --addr HOST:PORT script.lsl

If you do not say, the address is worked out for you:

1. `addr = ...` in `~/.config/slsh/config`, for `slsh`;
2. whatever the `sl-host` command prints, if you have one installed --
   the standard port is added to it unless it names a port of its own;
3. this machine, if you have no `sl-host`.

That last case is the normal one when you run your own `slgod`, so
usually you need not pass anything at all. Not being on `$PATH` is what
means "this machine"; an `sl-host` that IS installed and then fails is
reported, and does not fall back here. Falling back would turn "sl-host
is misconfigured" into a connection refused against localhost, which
points at the wrong problem entirely.

`sl-host` is one of the commands here (`go install ./cmd/sl-host`),
for a machine that reaches `slgod` over a network whose address
changes with where it is.  It reads `~/.config/sl-host` (or
`$SL_HOSTFILE`), one rule a line: a network, the address to use when
this machine has an address on it -- with a port, if that slgod is not
on 7807 -- any `@profile` words the rule is kept for, and a label that
is only ever shown.

    # at home, the LAN address; anywhere else, the router's forward
    192.168.1.0/24   192.168.1.20   home
    0/0              192.168.9.1    anywhere else

The first rule that any of the machine's addresses falls in wins, so a
catch-all goes last.  Every address is tried -- wired and wireless at
once, a VPN -- each on its own.  `sl-host -v` says which rule matched
and why, and `sl-host -l` lists every rule with whether it matches.
When none does, it lists the machine's addresses and the rules and
exits 1.  `SL_HOST=<address>` is an answer for a one-off.

A rule with `@` words is tried only when the question is about one of
those profiles, which lets a second `slgod`, on another port, be found
the same way as the first:

    0/0              127.0.0.1:7808   @dev   the development slgod
    192.168.1.0/24   192.168.1.20            home

Each program tells `sl-host` which avatar it is about to ask for, in
`SLGO_AGENT` -- the one from `-a`, `$SLGO_AGENT` or `agent =` -- and
takes that variable away when it has none; `sl-host -a dev` asks the
same thing by hand.  A rule with no `@` is for every profile, and for
a question that names none, so put a profile's own rules first.
`sl-host -l` marks the rules kept for other profiles as `other`.
Build `sl-host` and the programs from the same checkout: an older
program adds the standard port to an address that already has one.

### Profiles

Accounts live one file per account in `~/.config/slgo`, named however
you like -- or under `$SLGO_CONFIG_DIR` if that is set, or `slgo` under
`$XDG_CONFIG_HOME`. Neither the directory nor the files may be readable
or writable by group or other: the test is `perm & 0o077`, so 700 and
600 pass and so do 500 and 400, and these programs refuse to read
credentials anyone else can:

    first           = Example
    last            = Resident
    password        = $1$<the md5 of the password>
    start           = last
    group           = Example Builders
    neighbours      = yes
    viewer_password = $1$<a second one, for handing sessions to a viewer>

The file name is the profile name: the one above as `~/.config/slgo/example`
is the profile `example`, which is what `slgod example` and
`--agent example` mean.

`start` is where to arrive: `last`, `home`, or a region name -- and
`home` is also a standing instruction, since a home region that is down
at login time leaves the avatar somewhere else entirely; see "When home
is down" below. `group` is
the group to act as, which decides more than it looks like -- see "The
group an avatar acts as" below. `neighbours` is whether this avatar
holds a circuit to the regions beside it, which is what walking over a
border needs and what a benchmark has no use for; it wins over `slgod
-neighbours`, which is what profiles that say nothing get, and `slsh
neighbours` turns it over on a session that is already up.
`viewer_password` is what makes a session handable to a real viewer, and
is described with `-viewer`.

Storing the `$1$...` digest rather than the plain password is worth
doing. It is the only form that ever goes over the wire, so it loses
nothing, and it keeps a password that may be used elsewhere off the
disk. Plain text works too, and is hashed on the way out.

For `--direct`, anything missing is asked for at the terminal, and the
password without echo -- so a profile is optional:

    slrun --direct --first Example --last Resident script.lsl

With exactly one profile on disk, `--direct` uses it without being
named and says which -- "using the example profile". That rule is the
direct login's alone. Through `slgod` an avatar nobody named is the
daemon's default instead, which is a different question and is answered
under "Which avatar a program uses".

### The shared secret

`slgod` holds a live Second Life session, so an unauthenticated daemon
reachable off this machine lets anyone drive the avatar. Clients and the
daemon therefore prove themselves to each other, both directions, against
one secret:

    ~/.config/slgod/secret      mode 600, in a directory mode 700

One file for the whole lab, shared by every program here. Create it with

    (umask 077; mkdir -p ~/.config/slgod; openssl rand -hex 32 > ~/.config/slgod/secret)

which is what `slgod` prints if it cannot find one. The secret never
crosses the wire in either direction: each side answers a random,
single-use challenge, so a captured proof is worth nothing afterwards.

That makes 64 hex characters, and nothing short of that is needed. A
secret typed by hand is still accepted, but one under 16 bytes is
guessable over the network, and every program that reads it says so,
once, with a line beginning `WARNING: the shared secret in` and naming
the file and how short it is.

Guessing is slowed as well. After three wrong answers from one address,
every answer from it -- the right one too, or the speed of the answer
would give it away -- waits half a second, then one, two and four; an
address that gets it right starts again from nothing, and one that goes
quiet is forgotten after a quarter of an hour. Failures from everywhere
count too, at a tenth of the weight, so thirty from thirty addresses slow
everybody down the same way. Only so many answers are kept waiting at
once, and past that a login is refused on the spot, with "too many failed
logins lately; try again in a few minutes". None of this touches a client
with the secret on an address nobody is guessing from.

One secret for everything is the usual case and the simplest. A `slgod`
can keep one of its own instead -- one on another machine, say, or one
holding somebody else's accounts, which need not answer to every client
this lab's secret is on. `slgod -secret FILE` names it; with `-config
DIR` and no `-secret`, a file called `secret` in DIR is used when there
is one; otherwise it is the shared secret. The daemon logs `secret:` and
the path it read, and reads it before any avatar logs in.

A client chooses by the address it is about to dial, however it came by
it -- `--addr`, `addr =`, `sl-host` or the `localhost:7807` default --
and takes the first of these in `~/.config/slgod/` that exists:

    secret.HOST.PORT            secret.192.0.2.20.7807
    secret.HOST                 secret.192.0.2.20
    secret                      the shared secret

The host is as dialled, lowercased and without brackets, and an IPv6
address has its colons turned into dots, since a colon is not safe in a
file name everywhere: `[2001:db8::20]:7807` is `secret.2001.db8..20.7807`.
So `localhost:7808` and `127.0.0.1:7808` are two files, even through the
same tunnel; reached as the one with no file, a client falls back on the
shared secret, which a `slgod` with its own refuses. A per-address file
that is there but unusable -- the wrong mode, empty -- is an error, not
a reason to try the next. The refusal names the file that was used,
"(the secret used was PATH)", and a missing shared secret lists the
per-address files looked for first.

The handshake changed shape once, and a client built before the change
is told "this client is older than slgod" rather than refused as if its
secret were wrong. The cure is to rebuild and reinstall every program
here from the same tree as the daemon.

The connection is TLS, and the certificate is self-signed and **not**
checked -- it is there for confidentiality only, and the proof of who is
at each end is bound to that TLS session, so nothing is gained by
verifying it and there is nothing to distribute or rotate.

`-no-auth` serves without any of this. It is for a loopback-only run,
and nothing checks that the connection really is loopback.

---

## slgod

    slgod example                       one avatar
    slgod example builder helper        three
    slgod -listen :7900 example         somewhere other than the default :7807
    slgod -config ~/.config/slgod.dev -listen 127.0.0.1:7808 dev
                                        a second daemon with its own directory

Each argument names a profile. The sessions stay up until the process is
signalled; clients attach and detach freely without the grid noticing.

### The computer it claims to be

The login server asks a client which computer it is running on -- the
network card's address and the first disk's serial number, as digests.
`slgod` has no hardware worth reading, and reading this host's would hand
Linden Lab an identifier that follows you into every other program on the
machine, so it makes a pair up once and keeps it:

    ~/.config/slgod/config

Keeping it matters more than what it is. A pair that changes every login
looks like a different computer every time, which is what an abuser looks
like; a pair that never changes looks like an ordinary resident with one
computer. The file is written on the first run and read on every run
after.

### When a login fails

One expired password should not take down the sessions that did come up,
which may be somebody's benchmark in progress. So a failure is per
profile, and there are two kinds:

    example: not up yet (404 on the seed capability); trying again
    helper: NOT hosted: login failed: key or password incorrect

The first is worth asking again for, and is asked again for. A grid hands
back a dead seed capability often enough that a daemon which gives up on
the first one is a daemon somebody has to go and restart. The second
cannot be cleared by waiting, so that profile is left alone and the rest
carry on. `slgod` stops only when **nothing** came up and nothing is
still being retried -- "no session came up; nothing to serve".

### When home is down

`start = home` is a request, not a promise. If the home **region** is
down at the moment the login happens, the grid puts the avatar
somewhere else entirely and says nothing about it afterwards -- so the
session comes up in the wrong place, on land that is not yours, and
the first sign of it is usually a rez that will not work.

A session whose profile said `home` therefore keeps asking to go home
until it gets there, once a minute, for up to an hour. A region that
was down comes back and the avatar walks in on the next attempt with
nobody watching.

It cannot tell whether it is home already: nothing in the protocol
answers "where is home", and the daemon has never been told. What it
does instead is ask to go there once, a few seconds after the session
comes up, and read the answer. An avatar that is already home is
either moved a metre or two by the grid or told it cannot be teleported
closer to where it already is; both mean home, and the log says

    qi: home, after one attempt

Where the avatar was NOT home, the same line appears once it gets
there, with the number of attempts it took.

**It stops asking when asking cannot help.** Every refusal names a key,
the grid's word for why, and the log names it too: the first time the
avatar is refused, the line begins `not home (` with the key in the
brackets -- `no_host` is a region that is down -- and it is said again
only if the answer changes. Then:

- `RegionTPAccessBlocked` is a region this avatar may not enter, for
  its maturity rating or its access, and it is the same answer however
  often it is asked. The loop stops at once with a line beginning
  `giving up on getting home:` that gives the key and the grid's own
  sentence, and it does not ask again -- not after a reconnect either
  -- until a client sets a new home or `slgod` restarts.
- Anything else, including no answer at all, is asked about once a
  minute for an hour, and then given up on with a line beginning
  `giving up on getting home:` that names the last answer. A region
  that is down is back well inside an hour.

The hour is an hour of asking, and it is carried across reconnects
until the avatar gets home: a session that reconnected every half hour
would otherwise start a fresh hour each time and never stop. Once the
hour is spent, each reconnect asks once more, since the region may be
back, and gives up again at once if it is not. Getting home, or a
client setting a new home, starts the count again.

**A client teleporting the avatar stops it, for the rest of that
session.** Somebody who types `tp` has taken the wheel, and a daemon
that dragged the avatar home a minute later would be a poltergeist: the
shell reports an arrival and the avatar leaves again by itself with
nothing on the screen to say why. It stops on the request rather than
on an arrival, so a teleport that is refused stops it too.

**So does a viewer.** Handing the session to a viewer -- with
`viewer --launch`, or any viewer logging in at the daemon's viewer
endpoint -- stops it at once, before the viewer has asked for anything:
a person at a viewer has the wheel. A teleport asked for at the viewer
stops it the same way, and a new home set there forgets what the grid
said about the old one, as a client's does -- but only when the viewer
sends it as a message. Firestorm's source sends Set Home to Here
through the region's `HomeLocation` capability where the region offers
one, and that goes to the grid without passing `slgod` (read in the
source, not watched); after an access refusal, set the new home with
`landmark --set-home` instead.

A reconnect starts it again, and so does a restart: both are fresh
logins with `start = home` in them, so the same question is being asked
again by the same means. What the grid has said is kept across a
reconnect, as above, and forgotten on a restart.

A viewer still on the session is the exception. A reconnect is the
grid's doing and does not hand the wheel back, so while `slgod` counts
a viewer as on the session the loop does not ask at all, and says so
with a line beginning
`not asking to go home: a viewer is on this session`. A viewer counts
as on from when it joins until it logs out, which quitting one does.
A viewer that crashes sends nothing, and counts until it has been
silent for 100 seconds, the viewer's own circuit timeout, or until
another viewer takes the session or `slgod` restarts.

Profiles that say anything else are left alone. `start = last` means
where the avatar was, and dragging that avatar home would be the daemon
overruling the profile rather than honouring it.

### Where an avatar was sitting

An avatar that was sitting on something when its session ended comes
back standing.  Nothing on the grid remembers a seat: a sit is a
request the simulator acts on and does not record, and no message asks
"what is this avatar on".  So slgod remembers, and sits the avatar down
again at the next login.

It is written down when the avatar sits, and forgotten when it stands,
so there is nothing to configure and no list to keep up to date.  The
file is `~/.config/slgod/seats`, one profile and one object id per
line:

    # What each avatar was last sitting on, written by slgod.
    # One profile and one object id per line.
    example 45557e57-...

Comment a line out to leave that avatar standing at its next login.
Deleting a line does the same and lasts until the next time it sits.

The seat is learned two ways, because neither alone is enough.  The
simulator tells an avatar that has just sat down where it has been put,
and that message carries the object's id already resolved -- which is
the good source, since it arrives however the sit was asked for and
needs nothing looked up.  The other is a watch that notices the avatar
standing up, for which there is no message at all.

Putting it back takes a few tries.  A region does not hand over its
contents at once, and a sit naming an object the simulator has not
described yet is answered with nothing, so one request at the moment of
login would miss by seconds and look like a chair that had gone.  What
counts as having worked is the avatar being parented to something --
not to a thing slgod can name, which is a slower and different
question.

If the seat is really gone, taken home by its owner, the attempts run
out and a line says so rather than leaving somebody to wonder why an
avatar is standing where it used to sit.

### The group an avatar acts as

    slgod -group "Example Builders" example
    slgod -group example="Example Builders" -group builder=Testers example builder

A parcel usually grants "create objects" to a **group** rather than to
individuals, and a login starts with no group active. A viewer hides this
by storing the group in its settings and re-sending it every time, which
makes it feel permanent. Headless it is not -- so an avatar that builds
happily through a viewer cannot rez a thing here, and the refusal blames
the land, which is the wrong place to look.

It is also the group a prim slgo makes belongs to -- `rez`, `slrun
--rez` -- as a viewer's do. On land that runs only its group's scripts,
a script in a prim of no group was seen reported as running and never
run; see `doc/rez.md`.

A profile's own `group` line settles it; `-group` overrides that, by
name or uuid, and the `PROFILE=GROUP` form says which avatar it is for.
PROFILE is one word run up against the `=`; any other value with an `=`
in it is taken whole as a group name, and a group whose name starts with
one word and an `=` is given with its profile in front, as
`example="E=mc2 Society"`.
The setting belongs to the session rather than to a client, so every
program attached shares it, and a reconnect does not lose it.

### Which avatar a program uses

    --agent NAME     this command, and nothing else
    SLGO_AGENT       every command in this shell
    agent = NAME     in slsh's config file, for this machine
    (nothing)        the daemon's default: the session it has held longest

In that order of strength. The daemon's default changes only when that
session itself goes away -- adding an avatar never moves it, and neither
does a reconnect -- so a script that worked yesterday drives the same
avatar today.

When `slrun` does not name one, `-vv` says which it used:

    $ slrun -vv a.lsl b.lsl
    running as example

Worth asking for. With several avatars hosted the choice is the
daemon's, and output attributed to the wrong avatar is not an error --
it is a plausible one. It waits to be asked because the ordinary run of
`slrun` is a script and what it printed, and a line about whose objects
were borrowed arrives in the middle of that saying nothing about what
the script said. `slbench` says the same thing at `-vv`, because its
answer turned out not to depend on which avatar gave it the objects,
and because its objects usually come from more than one.

### What state each avatar is in

`slsh agents` is the readable view of what the daemon holds, and the
states differ in what to do about them:

    * example     Example Resident          Testville   <- this shell
      builder     Builder Resident          Testville
      helper      Helper Resident           stopped: logged out
      spare                                 configured

`configured` exists on disk and can be started, and has no avatar beside
it because nothing has ever logged one in under it. `stopped` was put
down deliberately and is left alone until asked for by name; it keeps
the avatar's name, because there was a session there. `failed` will
waste a login attempt if asked again too soon, and says how long it is
waiting. `connecting` is a login still in flight or a session being
reconnected -- the reason after it tells those two apart.

The star is the first line the daemon still **holds** a session for,
which is what a program that names no avatar gets. A session that is
reconnecting keeps it: the place belongs to the session and not to the
circuit under it, so a bare command still drives that avatar and waits
for it. Only `stopped` gives the place up, and a profile with no
session behind it never had one.

### Letting a viewer have one

    slsh logout example         slgod lets go of it
    ... use it in a viewer ...
    slsh login example          take it back

A logout is remembered. `slgod` will not bring that avatar back on its
own, so nothing is fighting you for it while you use it elsewhere, and
bringing it back has to name it -- undoing a deliberate stop should be
deliberate too. Naming it is enough: `login` always asks with force, and
its `-f` changes nothing. What slgod refuses is a program asking without
force, as slbotd does when it reattaches on its own.

You do not strictly have to log out first. Logging in from a viewer makes
the grid end the daemon's session, and `slgod` treats being thrown off as
a decision rather than a fault: it says so and stays down. Logging out is
the tidier way round, and the only one that lets the session end cleanly.

A logout refuses to be casual about it. It will not take a session that
clients are attached to -- it names them instead -- because a benchmark
mid-run has a script installed and a reading half taken.

Forced, it lets them go. Each program attached to that avatar has its
connection ended with the reason, in the words an attach to it is
refused with from then on:

    example is not connected (logged out); it will not come back on its own

An `slsh` on that avatar stops, saying `the session ended:` in front of
it, and `slbotd` lets go and waits for the avatar to be logged in again.
The same happens, with the grid's reason in the brackets, when the grid
throws the avatar off. A session that drops and is reconnected is
different: nothing attached to it is let go. All of this is from the
code and its tests; it has not been watched on a live grid.

### Handing a session to a viewer

    slgod -viewer 127.0.0.1:9000 example

With this, `slgod` serves a login endpoint of its own, and a viewer
pointed at it is given the session the daemon is already holding rather
than logging in again. The avatar does not leave the grid, nothing is
logged out, and the programs attached carry on.

A profile is only handable if it has a `viewer_password`. A viewer
that asks for one without it is refused, and `slsh viewer -l` says
which line to add:

    example has no viewer_password, so it cannot be handed to a viewer;
    add a "viewer_password = ..." line to that profile and restart slgod

The viewer itself is told less. Whether nobody by that name is here,
the profile has no `viewer_password`, or the password is wrong, the
login box says the same thing:

    That name and password do not match a session here.

That is deliberate: anything more would tell whoever can reach the
endpoint which avatars this daemon is holding and would hand over. The
daemon's log says which it was. Nothing is opened for a login that is
refused -- the avatar's UDP circuit and its capability token are made
only once the password has matched.

That password is the viewer's, not the account's -- what a viewer sends
here never reaches Linden Lab, and a password kept for this cannot be
used to log the account in anywhere.

The endpoint is TLS, always. It carries that password inbound and the
whole login response outbound -- the session id, the circuit code, and
every capability URL the session holds -- so there is no arrangement in
which serving it in the clear is right, and there is no flag for it.
`slgod` writes a self-signed certificate into its own config directory
the first time it needs one, keeps it, and says where:

    viewer: wrote a self-signed certificate to ~/.config/slgod/viewer-cert.pem

A viewer will refuse that until it is told about it. On Firestorm,
either accept the certificate dialog once, or append the file to
`user_settings/CA.pem` -- which is the way in if you are adding the grid
by hand, because the grid manager's own probe of a new address has no
dialog behind it. The certificate covers loopback and whatever address
`-viewer` names; move the daemon to a different address and it is
replaced, which is said in the log, and every viewer has to be told
again. Bound to every address, it covers loopback alone, and the log
says so at each start: a viewer dialling any other address of the
machine refuses it. `-viewer-cert` and `-viewer-key` take your own pair instead, from
a real authority or anywhere else.

The capabilities are behind the login too. A viewer that logs in
successfully is handed a URL with a token in it, and that token is what
`/cap/...` checks -- the profile name on its own used to be enough, and
the answer is every capability URL of the live session.

The endpoint does not wait on a slow sender: a request's headers must
arrive within ten seconds and the whole of it within a minute, and a
body over 256 KB -- eighty times a viewer's login -- is refused. The
event queue still holds a poll for up to twenty seconds when there is
nothing to say, but only four at a time for each avatar.

None of that makes the endpoint safe to put on a network. A viewer on
another machine wants a WireGuard or Tailscale tunnel with `-viewer`
bound to the tunnel address; the UDP circuit is on an OS-assigned port,
which no firewall rule can name and no TLS covers.

`slsh viewer` is the other end of it: it says where the daemon serves
viewer logins and whether one has taken this session, and `viewer -l`
starts a viewer and logs it in. That does not use the profile's
password. The daemon mints a fresh one, good for a single login and a
few minutes, because it reaches the viewer's argv where any process this
user owns can read it. A profile with no `viewer_password` is refused
there too -- the setting is what marks a profile as handable at all.

The design, and what it costs, is written up in
[doc/history/viewer-frontend.md](history/viewer-frontend.md); what
happens with more than one viewer is in
[doc/history/two-viewers.md](history/two-viewers.md).  Both are plans
rather than descriptions -- see [doc/history/](history/).

### Two clients on one avatar, and the half of the conversation you could not see

Several programs can attach to one session, and until now each of them
saw everything the **grid** sent and nothing the others said.

That is not an oversight in slgod, it is how the protocol works: the
grid does not echo your own instant messages back to you. A viewer
shows your own remarks because it composed them itself. So a person
watching through `slsh` while `slbotd` answered for the same avatar saw
every reply the other party made and none of the questions the daemon
had asked — a transcript with one side missing, and no indication that
anything was missing.

slgod now relays what one client sends to the **other** clients of that
session. Never back to the sender, which composed it and has already
shown it. Filtered by exactly the subscriptions that filter the grid's
own traffic and by nothing else: a client that asked for
`ImprovedInstantMessage` gets the instant messages, whoever sent them.
The server still decodes nothing — deciding here which messages were
"worth" echoing would be slgod learning what an instant message is.

In `slsh` these appear the way its own sends do, because that is what
they are:

    21:04:11 > [IM Somebody] three coils, Quark.
    21:04:19 < [IM Somebody] until Thursday, then.

One avatar said one thing to one person; which of its clients composed
it is not what a transcript is about.

### What this means if you are writing a client

`sl.IM` gains `To`, `Mine` and `Via`, and `sl.Line` gains `Mine`,
`Via` and `Channel`. `Mine` means *this avatar sent it, from another
client*.

**`IM.Conversation()` is false for these**, deliberately, and that is
the load-bearing decision. It means "somebody is talking to this
avatar", and a line this avatar sent is not that however much it looks
like one — the whole point of the field is that `From` is us. So every
program written before any of this existed goes on working unchanged,
and, more to the point, nothing that *answers* conversation can answer
itself.

That is not a display nicety. `slbotd` with `chat = *` has the avatar
in its own audience, so a reply would become a remark would become a
reply, and it would not stop. `Spoken()` is the question for a program
showing a conversation; `Conversation()` is the question for a program
answering one. Two different names because the cost of confusing them
falls entirely on the second.

`slbotd` records what was said elsewhere into the conversation and
never acts on it. One avatar should have one memory of what it said
whoever was driving, or the daemon will later contradict a promise a
person made through the same mouth — so a line you type into `slsh` as
that avatar folds into the note like any other. It does not start a
conversation that does not exist: one remark to somebody never spoken
to before is one remark, not a memory.

### Offers that arrive while nobody is attached

A teleport offered, a request to be offered one, an item handed over,
an offer of friendship and an invitation into a group each arrive once,
as an instant message carrying the id that answers it. Before this, a
session with no client attached acknowledged one and dropped it, so the
shell somebody starts *because* they have been away was the one program
that could never be told -- and said "nothing waiting" about an avatar
with two group invitations sitting on it.

slgod now keeps those five kinds, per avatar, whether or not anybody is
attached, and hands what is still waiting to a client in the frame its
attach is answered with. They go through the client's ordinary reading
of an instant message, marked as coming from the record: `sl` lists
them with `Recorded` set, and does not hand them to an `IMs`
subscriber, because they are history rather than news. They are
carried at attach rather than asked for, because only the attach can
promise that an offer is either in the record or relayed afterwards,
and never neither.

An offer leaves the record when it is answered, when a newer one of the
same kind from the same person replaces it -- as the client already
keeps them -- or when a hundred newer ones have pushed it out, which
the record then says. Nothing is dropped on a timer, when the avatar
changes region, or when slgod re-establishes the session: the viewer
puts no expiry on any of the five, carries a group invitation across a
logout, and answers each by quoting the id it came with rather than
anything of the session's. That the grid still honours such an answer
from a re-established session is inferred, from its delivering these
kinds at login out of offline storage, rather than watched. The record
is in memory, so an offer made while slgod itself was down is not in
it, and it says when it starts.

Script dialogs and permission requests are not kept. Both come from an
object in the region and are nearly always the result of something an
attached client just did, and whether an answer to one still reaches
the object from another region has not been watched.

**Dealing with one.** A client about to answer a kept offer tells slgod
first, with `Handled`, and the answer says whether to go on. The first
client to ask takes the offer out of the record and every other client
is told, which drops it from their lists. `slsh` says so at once, since
a number somebody was about to type has just stopped meaning anything:

    12:03:04 * the teleport Example Resident offered was accepted by slbotd -- it is no longer waiting

A client that asks second is told who got there first and sends
nothing, so two clients on one avatar cannot both answer one offer
without knowing -- a group joined twice would be a fee paid twice. A
client whose answer then fails to go out says so, and the offer is put
back for everybody.

`sl` does all of this inside the calls that answer an offer --
`InventoryOffer.Accept`, `Offer.Decline`, `AcceptLure`,
`AcceptInvitation` and the rest -- so a program using them gets it
without asking, and a refusal comes back as an `*sl.AnsweredError`.
Refusing a teleport request sends nothing to the grid, so it is
`RefuseTeleportRequest` that tells the others; `ForgetTeleportRequest`
still only forgets.

An answer that reaches the circuit without being announced -- a client
built before `Handled`, a program using the `client` package directly,
a viewer on the login endpoint -- is noticed on its way out and takes
the offer out of the record just the same. The notice then names no
program, because the circuit does not say which one sent it.

A direct session (`slsh --direct`) has none of this. It holds the grid
session itself, so everything since its login went through its own
reader, and there is nobody else attached to tell.

### Watching the wire

    slgod -v example                                  log every message the grid sends
    slgod -trace packets.txt example                  every packet, both directions, in order
    slgod -trace packets.txt -trace-messages ChatFromSimulator,ObjectUpdate example
    slgod -trace packets.txt -trace-bodies example    each traced message in full

A trace is worth taking before there is anything to debug: a baseline of
what an ordinary session receives is what an unusual one has to be
compared against, and it cannot be collected afterwards.

Each packet the simulator sends is recorded once. Until a viewer has
logged in to a session, that is the packet as it arrived, retransmissions
included. From then on it is what the relay to the viewer was offered,
with what became of it -- forwarded, absorbed, dropped, or held while no
viewer is joined -- and a retransmission, which the relay never sees, is
not recorded.

The trace file is created mode 600, and one already there is narrowed to
600 before it is emptied. That matters most with `-trace-bodies`, which
writes every message whole: the session id is in nearly every message a
client sends, and the chat and instant messages of everybody near the
avatar are in what comes back. Delete such a trace when you are done
with it.

### Where the log goes

Both daemons log to standard error unless told otherwise, and under
launchd that is whatever file the plist names. `-log FILE` has the
daemon open the file itself instead:

    slgod -log ~/.local/log/slgod.log example
    slbotd -log ~/.local/log/slbotd.log

The file is appended to, created mode 600 if it is missing, and narrowed
to 600 if it is wider. Its directory is created mode 700 if it is
missing, and **refused** if it exists and group or others can get into
it -- the daemon will not start, and says `chmod 700` on what. A
warning would have gone into the very file that was exposed. The
directory is also what protects the files the daemon does not open
itself, such as launchd's own capture of standard error, which it
creates with the ordinary umask.

The place recommended is `~/.local/log`, mode 700:

    mkdir -p ~/.local/log && chmod 700 ~/.local/log

and in each LaunchAgent plist, both `-log` in `ProgramArguments` and
`StandardOutPath` and `StandardErrorPath` pointed into that directory --
the second catches what the daemon cannot log itself, such as a panic.
launchd does not expand `~`, so a plist spells the path out in full.

What is in the log is meant to be safe to read, but not to publish: the
avatars' names and ids, and in `slbotd`'s case who spoke to them and
what they said (`-q` leaves the conversation out). What it does not hold
is a credential. The session id and circuit code -- which with the agent
id are all it takes to open a circuit as the avatar -- are never written
whole, and a capability URL is cut to its host, since whoever has the
URL can use the capability. A viewer that claims the wrong circuit is
logged as

    viewer: a viewer claimed session 127d7e57..., and the session id did not match this session's

and a seed capability that would not answer, in the retry line under
"When a login fails", is named by the simulator's host followed by
`/...`.

`slgod -log-secrets` turns that off and writes every value whole, for a
debugging session that needs them. It says so on the first line it
logs. A log written with it on holds the credentials of every session
the daemon had, and anyone who can read it can take them over: keep it
short, and delete it afterwards. `slbotd` has no such flag, because it
holds no session credentials to write -- it attaches to sessions
`slgod` owns, and what `slgod` tells it has already been cut.

`slsh watch` is the lighter way to see the same thing from outside:
message names as the protocol spells them, and naming none means every
one. It opens a connection of its own, so it never steals messages from
the reader keeping the shell's idea of the world up to date. It runs for
half a minute unless `-t` says otherwise, and `-t` needs a unit -- `-t
5m`, not `-t 5`.

### Options

| | |
|---|---|
| `-listen ADDR` | address to serve clients on (default `:7807`) |
| `-config DIR` | keep profiles and slgod's own files (machine identity, seats, viewer certificate) in DIR, instead of `~/.config/slgo` and `~/.config/slgod`; DIR must exist and be mode 700; a file called `secret` in it, if there is one, is this daemon's secret instead of the shared one |
| `-secret FILE` | the secret clients must prove, instead of `DIR/secret` or `~/.config/slgod/secret`; logged as `secret:` at startup |
| `-no-auth` | serve without authentication; loopback only, and it is not checked |
| `-group G`, `-group PROFILE=G` | the group to act as, overriding the profile's own |
| `-start WHERE` | override every profile's start location |
| `-viewer ADDR` | serve viewer logins here, so a real viewer can be handed a session; always TLS |
| `-viewer-cert FILE`, `-viewer-key FILE` | serve that endpoint with your own certificate rather than the kept self-signed one |
| `-neighbours` | hold a circuit to each neighbouring region, so an avatar can walk over a border; a profile's own `neighbours` setting wins over it |
| `-log FILE` | append the log to this file, mode 600, rather than standard error; its directory is made 700, and refused if others can open it |
| `-log-secrets` | for debugging: log session ids, circuit codes and capability URLs whole. The log then holds every session's credentials |
| `-trace FILE` | write a packet trace, mode 600 |
| `-trace-messages NAMES` | trace only these; empty traces every one |
| `-trace-bodies` | write each traced message out in full -- session ids, and everybody's chat and instant messages, included |
| `-v` | log every message the grid sends |
| `-version` | say which build this is, and exit |

---

## slrun

Runs LSL scripts in Second Life and prints what they say.

    slrun script.lsl
    slrun one.lsl two.lsl three.lsl
    slrun --object "bench box" script.lsl

Each file is compiled by Second Life and run, and each line the script
says is printed as it arrives:

    $ slrun hello.lsl
    hello from slrun
    two plus two is 4

With several scripts the lines are prefixed with the file each came
from, because they arrive interleaved and there is otherwise no telling
which said what:

    $ slrun hello.lsl goodbye.lsl
    hello.lsl:   hello from slrun
    hello.lsl:   two plus two is 4
    goodbye.lsl: goodbye from slrun

They finish in whatever order they finish in, so that is one run rather
than the order to expect.

One script needs no such prefix and does not get one -- the same word in
front of every line of the only output there is. `-v` asks for it
anyway, which is worth having when the output is being kept.

### Say DONE when you are finished

A script must say `DONE` when it has finished:

    default {
        state_entry() {
            llSay(0, "hello from slrun");
            llSay(0, "DONE");
        }
    }

Without it there is nothing to wait for but the clock, and every run
costs the whole timeout. Use `--done TEXT` for a different word, or
`--done ""` to deliberately wait out the timeout.

The `DONE` line itself is not printed -- it is the script talking to
`slrun`, not to you.

The match is a **substring** one: the first line *containing* the word
ends the run, and that line is not printed. So a script that says
"checked 3 files, none DONE" has stopped there and said nothing about
it. Pick a word the output cannot contain by accident.

### Where the script runs

A script needs an object to run in, and by default those are the `auto`
objects: four of the prims the avatar wears, kept. Any that are not
being worn are put on; any that do not exist are made, taken into
inventory and put on. That happens once.

Keeping it is what makes runs quick. The first script put into an
object takes several seconds to appear; replacing one already there
takes well under a second. Measured here: about **1 to 2 seconds** a
run, against about 10 seconds when every run rezzed its own prim.

That holds across logins. A worn object is rezzed afresh, with a new
key, every time it is put on and every time the avatar logs in, so its
key is worth nothing between sessions -- but the inventory item it was
worn from does not change, and the simulator says which item each worn
object came from, in an `AttachItemID` line of the object's NameValue.
So the tie is re-read off the wire every session. Nothing is written
down on your machine, and there is no cache to go stale.

    --object NAME    run in some other object already in the region
    --rez            rez a throwaway prim for this run, as before
    --keep           leave a rezzed prim behind

### Several scripts at once

Scripts named on one command line run in different objects at the same
time, four at once by default because four is a group:

    $ slrun a.lsl b.lsl c.lsl d.lsl
    c.lsl: c starting
    b.lsl: b starting
    d.lsl: d starting
    a.lsl: a starting
    c.lsl: c finished
    ...

Measured on the grid, four scripts that each sleep three seconds:
**17.5 seconds one at a time and 5.2 seconds four at once.** Six of them
take 9.1 seconds -- four, then the other two as objects come free, since
an object takes the next script the moment it is free rather than
waiting for its neighbours.

They finish in whatever order they finish in and the lines are
interleaved, which is what the tag in front of every line is for. The
tags are padded to the widest name on the command line so that they read
as a column.

`--jobs N` is how many run at once. Eight scripts of three seconds each
took **6.9 seconds** measured, against about 35 one at a time.

The ceiling is not one avatar. If `slgod` is holding three, a run that
wants more objects than any one of them has free takes them from
whichever have them -- and `-vv` says so, because "running as qi" and
"running as qi (8) and example (4)" are different facts about where the
output came from:

    $ slrun -vv a.lsl b.lsl
    running as qi (8) and example (4)

Two avatars may be standing in different regions, so a script that cares
where it is may not say the same thing on both. `slbench` spreads too,
and by default has to: it asks for one object per division of each of
its two searches, plus three, which is nineteen at the defaults where an
avatar wears twelve. Its readings turned out to agree across avatars --
every row of the `--parts` sweep in [memory.md](memory.md) reported the
same size over one avatar, two and three -- so this is not something a
benchmark has to avoid.

Naming an avatar with `--agent` confines a run to that one. A benchmark
then cannot have nineteen objects, so it takes what fits and says so:

    slbench: holding 9 objects rather than 19; the searches will share fewer rounds

which is slower and not wrong.

The objects are taken all together or not at all. `--jobs 8` where six
are free does not run six: it waits until eight are free. That is not
fussiness -- a run that took the six and
waited for the other two would be holding six objects nobody else can
use while it waits for somebody who may be doing exactly the same
thing, and neither would ever finish.

Asking for more than the daemon has between all its avatars is refused
rather than waited for:

    64 objects were asked for and this daemon's avatars have 36

Waiting, when it happens, is silent: the daemon holds nothing while it
waits and the client has nothing to report but that it has not returned.
Naming an avatar is the exception -- more than twelve, the most one
avatar can hold, is known to be too many before anybody is asked, so
`slrun` says so and suggests leaving out `--agent`, which lets the
objects come from more than one avatar.

An object that has never run a script from `slrun` is slower the
first time: creating the script item costs about eight seconds where
replacing one already there costs about one. Measured, the first
`--jobs 8` run took 14.5 seconds because three of its objects were new
to it, and the next took 6.9.

`--jobs 1` runs them in the order they were named, one after another,
and is what a set of scripts that leave something in the object for one
another needs: with four running there is no shared object to leave
anything in.

Each script is installed under the same name -- `--script`, `slrun`
by default -- because each is in an object of its own. That is why the
fan-out is one script per object and not several: measured, a second
script uploaded into one object under the same name destroys the first,
and then **both** runs report the survivor's output as their own and
both say they finished. Chat carries the object a line came from and
never the script's name, so one script per object is also what makes the
tag on each line true.

`--object` and `--rez` are one object between them, so they are one
script at a time; `--jobs` with either is refused rather than quietly
ignored, because a person watching for a speed-up that was never coming
is worse off than one who was told.

### Several runs at once

A run takes as many objects as it asked for and holds them until it
finishes. An avatar wears twelve and a default benchmark wants nineteen,
so what fits beside what is a question about the whole pool rather than
about one avatar: three avatars are thirty-six objects, which is one
benchmark and seventeen scripts, or nine `slrun`s of four, or any other
way thirty-six divides up.

There is no walk from one avatar to the next. A run makes one request
for the number it wants and the daemon answers it out of the whole pool,
taking objects from as many avatars as it takes. Only when the pool
cannot fill the request at all does anything wait.

Naming an avatar turns that off, and `SLGO_AGENT` counts as naming one:
an avatar asked for by the environment is still an avatar somebody asked
for, so a run waits for it rather than quietly using another. That is
worth knowing before setting the variable in a shell you then start nine
things from.

Why a run gets its objects to itself: the script is installed under a
fixed name, so two runs sharing an object would overwrite each other's
script, and each would then report the survivor's output as its own.

Taking objects one at a time would deadlock: three runs each wanting
four of twelve can end up holding three apiece and waiting for a fourth
that nobody is going to give back. What prevents it is that a request is
answered whole or not at all. The daemon keeps one pool of every avatar's
objects, and a request for N is served out of it in one step -- N of them
or none. A request that cannot be served holds nothing while it waits,
so there is no cycle to deadlock on.

What that leaves is starvation: a run wanting twelve can in principle be
stepped over by a stream of runs wanting one. Nothing prevents that,
deliberately -- these are programs run by hand or from a script rather
than a service under load, so the queue drains.

This used to be done by dividing the pool into fixed groups of four and
locking a whole group, four being what a benchmark's search happened to
use at the time. That could not deadlock either, but it made four the
unit of everything: a run wanting six had to hold eight, and one wanting
twelve could not be served at all. There was a per-avatar pool for a
while after that, and a run walked from one avatar to the next looking
for room; one pool across all of them replaced it, which is what lets a
single request span avatars.

Set an avatar up with `slsh auto -n 12`, once per account. An avatar that
is not allowed to build can still be set up, provided somebody who can
gives it one object: everything after the first is a copy, and copying
something already owned asks the land nothing.

The objects are held by `slgod` for as long as the program holds its
connection, so a run that crashes or is killed gives them back at once;
there is nothing to clean up and no stale lock to break. Use `--rez` to
take a prim of your own and not queue at all, or `--wait` to queue for no
longer than it says.

There is a timeout underneath that, for the case the connection does not
cover: a client that wedged without dying. A grant is asked for thirty
minutes, which is long because what it guards against is a program that
has stopped rather than one that is merely slow. Nothing renews it. A run
that goes on longer loses its places a few seconds after the thirty
minutes are up: the daemon may hand them to the next program that asks,
and nothing stops the two of them running scripts in the same objects
(see [slots.md](slots.md#leases-and-what-they-are-not-for)).

### What can go wrong

Each is reported and each makes the run fail:

    (3, 4) : ERROR : Syntax error        would not compile
    slrun: Math Error                    crashed while running
    it did not say DONE within 1m0s      never finished
    the land doesn't run this ...        will not run where it is

The compiler's line and column **count from zero** -- measured, a bad
token on the fifth line of a script reports line 4 -- but `sl` sends
every script with a newline in front of it, so the numbers you see here
are one higher than that and land on the line your editor shows. `(3,
4)` is the fourth line of your file.

The newline is there for a different reason. An upload sometimes reaches
Second Life's compiler EMPTY, and the compiler says the same thing about
that as about a script wrong at its very first character: `(0, 0) :
ERROR : Syntax error`. With a newline in front, nothing we send has
anything on line 0, so `(0, 0)` can only be an upload that went missing
-- and one that did is simply sent again. Measured on thirty scripts at
once: three runs in eight failed that way before, none in eight after.

The last is said as soon as the script has compiled, without waiting
for the timeout: the region is running no scripts, or the object is
within 50 m of the ground on a parcel that runs only its owner's
scripts, or its group's as well, and the object is neither the owner's
nor in the group. The simulator reports such a script running and it
never says a word. Worn objects, the default, are checked only for the
region; the 50 m is inferred from one measurement. See
[ground.md](ground.md#where-the-land-stops-running-scripts).

`slrun` exits non-zero if **any** script failed, so it can be used
from a Makefile or a test script. It exits 0 only if every script
compiled, ran and finished.

### What is left behind

The script stays in the object, **stopped**, under its name -- `slrun`
unless `--script` says otherwise -- so the next run with that name
updates it in place instead of putting a new one in.  It is stopped
however the run ends: finished, crashed, timed out, or interrupted with
^C.  A script left running would go on doing whatever it does, and its
chat would be heard as the next run's output.

A `slrun` killed outright, or interrupted twice, has no chance to stop
it.  The next run in that object with the same name does, before it
starts listening, so a leftover cannot say `DONE` for the new script.
In the shared pool "that object" is one particular prim, and the next
run may be given a different one: the leftover goes on running until a
run lands on its prim.  `-j` with as many scripts as the avatar has
objects lands on every one.
Nothing else in the object is touched -- scripts under other names may
be there for reasons of their own -- so a run with a different
`--script` name leaves an earlier one running until you stop it.

Putting a script into an object for the first time goes by way of your
inventory, and that copy is deleted once the object has its own.  A
`--rez` prim goes to your Trash when the run ends, unless `--keep`
says to leave it; a `slrun` killed outright leaves it in the world.

Anything that could not be tidied away is said on standard error, and
does not fail the run. So is a `--rez` prim whose going the region did
not confirm within ten seconds: it may still be standing.

### Options

| | |
|---|---|
| `--object NAME` | run in this object instead of the shared `auto` objects |
| `--rez` | rez a throwaway prim for this run, and do not queue |
| `--keep` | leave a rezzed prim behind |
| `--done TEXT` | the text that means "finished" (default `DONE`); matched as a substring |
| `--timeout DUR` | how long to wait for it (default `1m0s`); a bare number is refused -- the unit is required |
| `--script NAME` | what to call the script inside the object (default `slrun`, which is the name a fault is reported under) |
| `--jobs N`, `-j N` | how many scripts to run at once, one per object; four by default, `1` runs them in the order they were named |
| `--clear` | empty the scripts out of the objects the daemon says were not left clean, before running |
| `-v` | put the script name in front of every line, even with one script |
| `-vv` | and say which avatars the objects came from, when `--agent` did not |
| `--agent NAME`, `-a` | which avatar; the daemon's default otherwise |
| `--addr HOST:PORT` | the `slgod` to attach to; `sl-host`, or this machine |
| `--direct`, `-d` | log in to Second Life directly, without `slgod` |
| `--first NAME`, `--last NAME` | the avatar's name, for `--direct` |
| `--start WHERE` | where to arrive, for `--direct`: `last` (the default), `home`, or a region name |
| `--backend HOST:PORT` | run the scripts through a `script.v1` backend there -- a simulator, or a viewer daemon -- instead of in Second Life |
| `--wait DUR` | the longest to wait for somewhere to run when every object is busy -- the shared `auto` objects through `slgod`, or a group of a `--backend`'s -- after which the run fails; as long as it takes by default. The unit is required, and it counts in whole seconds. ^C while waiting gives up the place in the queue |
| `--version` | say which build this is, and exit |

---

## slbench

Measures how many bytes of script memory an LSL construct costs.

    slbench --code "integer gCNT;"
    slbench --statement "llSin(1.0);"
    slbench bench.lsl
    generate-it | slbench

Four ways to say what to measure, and they are the same input by
different routes: `--code` takes it on the command line, `--statement`
takes one statement and wraps a function around it, a filename on its
own is read for it, and with none of those the code is read from
standard input.  A lone `-` says standard input in so many words.

Standard input counts only when it is not a terminal.  A bare `slbench`
at a prompt is somebody who has not said what to measure, and answering
that by waiting silently for typing would look like a program that had
hung.  What it does instead is say so, at once, without logging in.

`--states` is the other slot.  What it takes goes *after* the default
state, which is where LSL puts every state but the one a script starts
in, and it is how the cost of a state or of an event is measured:

    slbench --states "state sCNT { state_entry() { } }"

Two runs are what an event costs, since an event has to live in a
state and cannot be asked about on its own:

    $ slbench --states "state sCNT { state_entry() { } }"
    First Copy: 52
    Additional Copies: 47

    $ slbench --states "state sCNT { state_entry() { } touch_start(integer n) { } }"
    First Copy: 112
    Additional Copies: 100

So a `touch_start(integer n)` costs 53 bytes, which is the difference
between the two.  Measured on Second Life, 2026-08-25.

`CNT` in either slot becomes the copy number.  That is what makes it
possible to ask for a hundred of something that has to be named -- two
states called `s` will not compile, `state s000` and `state s001` will.

Every flag that takes LSL takes the name of a file holding it instead,
written as a path: `/x.lsl`, `./x.lsl` or `../x.lsl`.  That is `--code`,
`--statement`, `--states`, `--preamble` and `--postamble`.  It is the prefix that decides and nothing
else, because there is no reading of what LSL looks like that is safe:
`state sCNT { state_entry() { } }` has no semicolon in it, and it is
exactly the shape `--states` is for.  The one thing beginning with a
slash that is not a path is `//`, which opens a comment.

The flags go in front of the file -- option parsing stops at the first
argument that is not a flag, and here that argument is the file, so
`slbench bench.lsl -v` is a line with a file and no `-v`.

Nothing to measure is refused whichever route it came by.  An empty
file used to be measured as though it were a benchmark and printed the
two numbers an empty one costs, which is a report of nothing that reads
exactly like a report of something.

A script has a fixed memory budget, and Second Life allocates script
code in 512-byte blocks -- so you cannot simply ask what one variable
costs. What `slbench` does about that is written up in
[doc/memory.md](memory.md).

It puts one copy of the code in a script and finds how much filler that
script can still carry before its memory steps into the next block,
which locates the block boundary exactly. Then it does the same for a
script with more copies in it. Everything reported is a difference
between two of those exact positions.

    $ slbench --statement "llSin(1.0);"
    First Copy: 380
    Additional Copies: 47

Two numbers, because two numbers is what a benchmark is for: what a copy
of the code costs, and what another one costs after it. They differ by
whatever the construct pays once and then shares.

`Additional Copies` comes from a second script with `--extra` further
copies in it, eight by default. `--extra 0` measures the first copy
alone and reports only that line.

Everything else is how the answer was arrived at rather than the answer,
and `-v` asks for it:

    $ slbench --statement "llSin(1.0);" -v
    First Copy: 380
    Additional Copies: 47
    Base mem: 3876
    Result mem: 4388
    Result pad: 132
    Shared: 333
    Padding: 480

`Base mem` and `Result mem` are the two readings `First Copy` is the
difference of -- the base script at the padding, sitting exactly on a
block boundary, and the same script with one copy in it. `Result pad` is
the filler that copy can still carry before it spills into the next
block: 4388 - 3876 is 512, of which 132 was filler the copy could still
have carried, so the copy cost 380. `Shared` is what the construct pays
once. `Padding` is described below.

`-vv` adds which avatars' objects it ran in, and `-vvv` prints every
script it sends.

**`--extra` should be a multiple of 4.** Code is 4-aligned, so
individual copies quantise around their real cost -- measured, copies of
`llSin(1.0);` cost 48, 48, 48, 44 repeating, which is four copies of 47.
Only a multiple of four averages that out. `--extra 1` is the cheapest
measurement there is and it reported 48 for that construct and 348 for
another whose real answer was 343. If `Additional Copies` comes out
fractional the copies did not all cost the same, and `Shared` is
withheld.

There used to be a second mode, which put up to 512 copies in one script
and fitted a line through two counts. It existed to beat the
quantisation by dividing it down, and exact boundaries removed the
reason: both approaches report 47 for `llSin(1.0);`, and this one does
it in 14 seconds against 53. Installing 128 copies took 5.07s, 256 took
12.70s, and 512 was refused outright.

### Describing the code to measure

Give the code in one of three ways -- `--code`, `--statement`, or a
filename -- and never more than one.

`--code` is repeated whole:

    slbench --code "integer gCNT;"

`CNT` in it is replaced with the copy number, zero-padded to three
digits -- `g000`, `g001` -- so each copy can have a name of its own.
That matters twice: without it a hundred copies of `integer g;` is not a
hundred variables, and a name whose length changed with the count would
put the count into what is being measured.

`--statement` is for code that must live inside a function, and wraps it
in one for you:

    slbench --statement "x = j + 1;" --params "x,j"

`--preamble` and `--postamble` add text before and after the copies, and
`--globals` declares globals.

This expands to:

```
_(integer x, integer j) {
x = j + 1;
x = j + 1;
x = j + 1;
...
}
```

### Naming variables

`--globals`, `--params` and `--locals` take comma-separated names --
**no spaces** -- whose **first letter gives the type**:

| letter | type | | letter | type |
|---|---|---|---|---|
| `a`, `l` | list | | `k` | key |
| `f`, `g` | float | | `q`, `r` | quaternion |
| `i`, `j`, `x` | integer | | `s` | string |
| | | | `v` | vector |

So `--globals "iCount,sName,vPos"` declares an integer, a string and a
vector, and `ICount` or `SName` would do the same -- the letter is
matched either case. Any other first letter is refused, so a name has to
be chosen to say what it is.


### Reusing the padding

`Padding:` is a property of the base script -- the harness with no
copies of your code in it -- rather than of the code being measured.
Searching for it is most of what a cold benchmark costs.

You do not have to do anything about that: it is remembered, in
`~/.config/slgo/slbench-padding`, and looked up by what the base
script is. The code under test is not in the base script at all, so a
benchmark of new code reuses the answer, and so does any other benchmark
of the same shape whatever it measures.

    cold                       13s   6 rounds
    the padding remembered      9s   3 rounds

An entry is confirmed rather than trusted. Using one runs the base
script at that padding and one byte past it and requires memory to grow
between them: two readings against the three rounds a search costs, and
an answer that has stopped holding -- because Second Life's compiler
changed under it -- is thrown away and searched for again. So there is
nothing to invalidate by hand.

Those two readings travel with the readings that come next if they hold,
in one round: what a benchmark wants after a confirmed padding is fixed
before the confirmation answers, so it is asked at the same time. The
confirmation costs no round of its own.

`--ipad N` names a padding outright -- one printed by an earlier run, or
one out of [doc/memory.md](memory.md) -- and skips the search. It is
confirmed exactly as a remembered one is; there is no flag for that,
because the two readings ride in a round that is being spent anyway.
`--no-cache` neither reads nor writes the file.

Only readings that came from Second Life are remembered at all: the
backend is asked whether it is the grid, so neither `--test` nor a
`--backend` simulator writes to the file or reads from it. The offline
model has no compiler and its paddings are arithmetic rather than
measurements, and one of those in the file would be read by every later
benchmark on the account as though the grid had said it.

### Rounds, and what --parts is for

A benchmark waits on **rounds**, not scripts. Scripts in one round go to
different objects and run at the same time; the round is one wait
however many are in it. Measured, seven scripts in a round cost 1.50s
against 1.31s for one.

`--parts N` cuts the search into N parts a round, spending the N-1
readings that divide them. Nine rounds of a bisection become
`ceil(log_N 512)`. Eight is the default and the measured knee: 8x8x8 is
512 exactly, so three rounds of seven land on the byte with no round
wasted. Sixteen parts takes the same three rounds for ten more scripts;
thirty-two saves a round and measured slower.

`--parts` also sizes the lease, and so does `--extra`: each search wants
its anchor and its N-1 divisions in every round, and the two searches
share rounds, so the default holds 19 objects. Those come from wherever
the daemon has them, several avatars at a time. If the pool cannot grant
that many it takes fewer and says so -- with fewer places the searches
share fewer rounds, and with one there is no parallel search at all and
the block is bisected. Same answer, slower.

`--paranoid` re-reads each crossing before believing it: the step, the
anchor it was measured against, and the pad below. One round, and off by
default. What it guards against was seen once, live, on 2026-08-03, and
never reproduced in 45 later asks at the pads involved. See
[doc/scripttest.md](scripttest.md#a-reading-one-block-high).

### Checking your connection

    $ slbench --probe
    SL Live

It sends a script and checks what comes back, through whichever backend
was chosen -- so `--backend` or `--test` answers `Backend live` instead.
That is the one place a person is asking whether they are talking to
Second Life, and it will not say so about a model.

### Options

| | |
|---|---|
| `--code CODE` | the code to measure, repeated |
| `--statement CODE` | statements to measure, wrapped in a function |
| `--states CODE` | states to measure, placed after the default state |
| `--globals`, `--params`, `--locals` | declare variables (see above) |
| `--preamble`, `--postamble` | text around the copies |
| | any of the five above takes `./FILE` in place of the LSL |
| `--extra N` | further copies to measure, for `Additional Copies` (default 8; `0` for none). A multiple of 4 is advice and not enforced -- only a negative is refused |
| `--parts N` | how many parts to cut the search into each round (default 8; at least 2, and a power of two only by advice) |
| `--paranoid` | read each crossing again before believing it |
| `--ipad N` | measure at this padding instead of searching for one |
| `--no-cache` | do not remember or reuse the padding |
| `--debug` | say what the search is doing, and what it spent |
| `--timeout DUR` | how long one script may take (default `1m0s`); a bare number is refused -- the unit is required |
| `--probe` | check the connection and exit |
| `--test PAD,SIZE[,MARGINAL[,LIMIT]]` | measure against the offline model in this process, without a grid |
| `-v`, `-vv`, `-vvv` | the readings behind the answer; whose objects it ran in; every script it sends |
| `--agent NAME`, `-a` | which avatar; the daemon's default otherwise |
| `--addr HOST:PORT` | the `slgod` to attach to; `sl-host`, or this machine |
| `--direct`, `-d` | log in to Second Life directly, without `slgod` |
| `--first NAME`, `--last NAME` | the avatar's name, for `--direct` |
| `--start WHERE` | where to arrive, for `--direct`: `last` (the default), `home`, or a region name |
| `--backend HOST:PORT` | run the scripts through a `script.v1` backend there, instead of in Second Life |
| `--version` | say which build this is, and exit |

### Reading the numbers honestly

`First Copy` and `Additional Copies` are not two answers to the same
question. One is what a copy costs outright; the other is what each copy
after the first costs. They differ by whatever the construct pays once
and shares, which `-v` reports as `Shared`.

The gap can be large, and when it is, it is telling you something.
Measured live: `string sCNT = "<250 identical characters>";` costs 1054
bytes for one copy and 540 for each after it. Identical literals are
shared, so an extra copy pays only for what it cannot share.

**Precision.** Code is 4-aligned, so two measurements of the same
construct that differ by 4 differ by one quantum. Measured across a
dozen live runs, the same construct came back 364 or 368 depending on
what unrelated globals the script carried, so 4 bytes is the resolution
to expect rather than the exact byte. It is also why `--extra` wants a
multiple of 4.

Constructs of any size are handled, including those larger than a
512-byte block.

Second Life's own numbers move about a little from run to run. If an
answer matters, take it twice.

---

## slbotd

A second daemon, on the other side of `slgod` from everything above. It
holds several avatars at once and takes its orders from inside the
world: a trusted avatar sends an instant message beginning with a
colon, and the message is a command.

    slbotd
    slbotd --check

It logs nobody in. For each avatar in its configuration it asks `slgod`
to bring the session up and attaches to what it is given, so the
credentials stay where they already were and an `slbotd` that is killed
leaves every avatar exactly where it was.

Its log names everybody who spoke to the avatars and what they said, so
give it `-log ~/.local/log/slbotd.log`, or point launchd there; see
"Where the log goes" under `slgod`. `-q` leaves the conversation out.

### What it is for

Standing an avatar somewhere and being able to ask it things from a
viewer, without a terminal anywhere near. Where is it, who is around
it, what is on the parcel; go to that landmark, sit on that, take that
item. And -- the reason it can run `slrun` and `slbench` -- starting a
measurement from inside the world and reading the answer in the chat
window it was asked from.

### The configuration

One file, beside the profiles it names:

    ~/.config/slgo/slbotd.conf

It is not a profile and is not mistaken for one: `slgod` and everything
else decide what is a profile by loading it, and this file fails that
test on its first line. `--config PATH` or `$SLBOTD_CONFIG` names
another. Nothing secret is in it -- `slbotd` never handles a password --
but the directory around it holds the profiles, so it still has to be
mode 700, and `slbotd` says so rather than waiting for the first login
to find out.

    # Which avatars to hold.  Each names a profile in this directory.
    avatar = example
    avatar = builder

    # Who may send commands, and whose inventory offers are taken.
    # A name, or a uuid -- the uuid never changes and cannot be taken
    # by anybody else, so it is the better one where you have it.
    trusted = Quark Idlemind

    # Where slgod is.  Nothing said asks sl-host -- about the avatar,
    # when there is only one -- or, where sl-host is not installed,
    # means this machine.
    addr =

    # What marks a command.  Anything else is somebody talking.
    prefix = :

    # How long one command may take, and how long a program run may.
    timeout     = 2m
    run-timeout = 30m

    # How many commands one avatar runs at once, and how many instant
    # messages one answer may be sent back as.
    jobs        = 4
    reply-limit = 8

    # Whose inventory offers are accepted: trusted, anyone, nobody.
    accept-inventory = trusted

    # How long a conversation has to have been quiet before the avatar
    # is told about it.  Nothing is forgotten at this threshold; it is
    # only how long a pause has to be to be worth remarking on.
    chat-gap = 1h

    # How long a silence makes the next remark from a trusted person a
    # fresh conversation -- the one told, in a line, that things have
    # gone wrong since.  Long rather than short: what it guards against
    # is an avatar that opens every other remark with the same
    # complaint.
    error-gap = 1h

    # Whether somebody who is not trusted is told their command was
    # refused, or simply not answered.
    answer-strangers = no

    # A program a command may run: a name, a path, and any arguments
    # that are always passed.  Naming any REPLACES the built-in pair.
    program = slbench /usr/local/bin/slbench
    program = slrun   /usr/local/bin/slrun

    # Another name for a command.
    alias = autobench slbench
    alias = automate  slrun

Every one of those is optional but `avatar` and `trusted`: a
configuration that holds nobody, or that trusts nobody, would start a
daemon that can never do anything, and is refused rather than run.

A chat setting that is half finished -- `chat` with no `llm-url`, or
the other way about -- turns chat **off** and says so loudly at every
start. It is not fatal: attending avatars and taking commands is what
slbotd is for, chat is bolted on beside it, and a missing line there
must not be the reason nobody can drive an avatar.

`slbotd --check` reads the file, says what it means, and exits without
connecting to anything -- which is how to find a misspelt profile or a
program that is not where it was said to be, before an avatar is logged
in on the strength of it.

### Sending a command

Open an instant message to one of the avatars and begin the line with
the prefix:

    :where
    :who
    :tp Example Bay 128 64 25
    :landmark --go the workshop
    :ls Objects
    :give Quark Idlemind Objects/a lamp
    :slbench --statement "llSin(1.0);"

`:help` lists the commands, grouped; `:help COMMAND` says what one
takes, in the same usage line `COMMAND --help` prints. The groups are
looking (`where`, `who`, `look`, `parcel`, `regions`, `objects`, `worn`,
`lookup`, `profile`, `friends`), moving (`tp`, `landmark`, `sit`,
`stand`, `touch`), talking (`say`, `im`, `offers`, `accept`, `decline`),
inventory (`ls`, `cat`, `mkdir`, `rm`, `mv`, `cp`, `give`, `place`,
`take`, `wear`, `detach`), and the daemon itself (`help`, `agents`,
`status`, `as`, `host`, `trusted`).

`:as OTHER COMMAND` runs a command as another of the avatars this
daemon holds, so one conversation drives all of them:

    :as builder where
    :as builder place Objects/a lamp

Nothing typed reaches a shell. A line is split into words -- quotes
hold a word together, at the start of a word or after an equals sign,
so an apostrophe in an inventory name is an apostrophe -- and the words
are an argv. There is no expansion, no redirection and no second
program.

### Asking what has gone wrong

`:errors` is what an avatar has to say about itself. It lists the
failures it has kept — the last 50 — newest at the bottom, each with
how long ago it was:

    :errors
    3 hours ago: could not send the answer to somebody: connection reset
    12 minutes ago: could not keep the context for somebody: no such slot

`:errors clear` forgets them.

The log file is still the record and still has everything, in order and
across restarts. What this is for is the one person who cannot read
that file: whoever is standing in the virtual world talking to the
avatar, wondering why it has been quiet. An avatar that has been
failing for hours looks exactly like one with nothing to say, and there
was no way from in-world to tell those apart.

### What it does unasked

Four things, and only four.

**It tells an admin, once, that something has gone wrong.** On the
first remark of a fresh conversation from somebody trusted — after a
silence longer than `error-gap`, an hour by default — the avatar sends
one line before anything else:

    (3 things have gone wrong since we last spoke -- say :errors)

One line and not the failures themselves. An avatar is in character and
a page of daemon diagnostics is not; when things are going badly the
list is long, and a conversation that opens with twenty lines is one
nobody reads; and the detail is one command away. It goes as its own
message rather than on the front of the reply, because the reply is
paced to the speed of somebody typing and this is not.

Only somebody trusted is told, and only once: reading them with
`:errors` counts as being told, and so does the notice itself. Silence
of less than `error-gap` is the same conversation, and somebody working
with an avatar all afternoon has heard it already.

**It keeps the avatars attached.** `slgod` supervises the grid session
and reconnects one that drops; `slbotd` asks for the session, attaches,
and attaches again when its own connection ends. What it does *not* do
is overrule a deliberate logout: `slgod` refuses to restart a session
somebody stopped on purpose -- the usual reason being that they are
using that avatar in a viewer -- and `slbotd` stops asking when it is
told that. `:host --force` is how a person says they have checked.

**It accepts inventory from a trusted avatar.** An offer nobody answers
stays pending for ever, and handing a script or a notecard to a daemon
should not need anybody at a keyboard. An offer from anybody else is
left exactly where it is rather than declined, so a person can still
answer it from a viewer; `accept-inventory` changes who that is.

The same rule is applied, when it attaches, to the offers `slgod` kept
while `slbotd` was not attached -- a gift made while the daemon was
restarting is no less wanted for it. And a gift somebody has already
answered from `slsh` is left alone: `slgod` says who took it, nothing
is sent, and the log says so.

**It puts an avatar's outfit back on after a login.** The simulator
puts most of an avatar's attachments back by itself at login, but not
all of them: measured on Agni, wherever a point held several
attachments exactly one came back, as though it restores one per
point. A
viewer covers the gap by putting on whatever the Current Outfit folder
names and is not on. Nothing here did, so a restart of `slgod` could
leave its avatars missing part of what they wore until somebody
noticed.

A few seconds after attaching, `slbotd` compares the folder against
what is actually worn and asks for whatever is missing. Before asking
it waits while the simulator's own list of attachments -- sent with
each bake of the avatar's appearance -- names more than the region has
described, since those are on and merely not described yet. An avatar
is not sent its own list at login, so it asks for a bake first, as a
viewer does. The list never includes HUDs, so for a HUD the region's
description is still the only evidence.

It tries a few times, because a region hands over its contents
gradually and a request that arrives too early is answered with
nothing, and it stops as soon as a pass puts nothing on. A pass that
fails outright -- which is what a session about to be replaced looks
like -- does not count, and is tried again for up to five minutes
rather than given up on.

It adds rather than replaces, as a viewer does. A point holds more than
one attachment and an ordinary outfit uses that -- a body, a dress and
a pair of arms can all be on the chest -- so restoring with replace
puts them on one after another and each knocks the last one off.
Anything worn twice as a result is named in the log. `dress` in `slsh`
is the same thing by hand.

Everything else waits to be asked. A teleport offer, a friendship
offer and a group invitation are listed by `:offers` and answered by
`:accept N` or `:decline N` -- by number, from a listing just read,
because two offers can look identical and answering the wrong one of
them cannot be taken back. The listing includes what `slgod` kept from
before the daemon attached. One answered meanwhile from another client
drops out of it, and answering one that another client has just
answered is refused with who did it and how, and sends nothing.

### Running slbench and slrun

They stay separate programs, run as separate processes, with the words
after the command handed over as arguments:

    :slbench --statement "llSin(1.0);"
    :automate probe.lsl

Which avatar the run is for is passed in the environment, as
`SLGO_AGENT`, which is what every program here reads to decide which
session to attach to -- so `:as builder slbench ...` measures as
`builder`. A `--agent` typed on the command still wins, which is the
right way round: the daemon says which avatar it is acting as, and
somebody who names another one has named it on purpose.

A run is bounded by `run-timeout` rather than by `timeout`: one is a
bound on a benchmark and the other on a listing, and a benchmark killed
after two minutes would be killed by a number nobody would think to
look at.

### Answers, and how much of one arrives

An instant message holds about a kilobyte and what goes over that the
simulator cuts off without a word. So an answer is composed in full,
cut at a line ending where there is one, and sent as up to
`reply-limit` messages a quarter of a second apart -- the gap is not
politeness, it is the per-agent throttle, and a burst sent as fast as
the circuit takes them arrives with the tail missing. What did not fit
is said:

    ...that is the first 8 messages of the answer and there was more;
    ask for less of it.

A command that did what it was asked and printed nothing answers `ok`.
Silence from a daemon is indistinguishable from a daemon that never
heard.

### Two things to know before trusting it

The trusted list is a list of people who can drive an avatar. Every
command is theirs: `rm` deletes, `take` takes an object out of the
world, `give` hands an item to somebody. There is no partial trust and
there is deliberately no confirmation step -- a daemon that asked "are
you sure" over an instant message would be a daemon whose answer
somebody else could send.

And an instant message says who it is from, which is a name and an id.
A name can be matched, which is what makes a file written in names
work, but a display name is not what arrives: the legacy name is. Where
you have the uuid, write the uuid.

A message from an object -- a script's `llInstantMessage` -- is never
obeyed and never answered, whatever it says it is. It carries whatever
name the object was given and its owner's id, so an object named after
somebody trusted, or one a trusted person owns, would pass on either.
The log says `ignored an instant message from [Object] ...`, and any
other name in the log that is not a person's is labelled `[Object]`,
`[Group]` or `[Grid]` the same way. What the grid puts in those two
fields was measured, and is in [im-senders.md](im-senders.md).

### Talking back

An instant message that does **not** begin with the prefix is not a
command, and by default nothing happens to it. Give `slbotd` a model
and a list of who to answer, and it becomes a conversation:

    llm-url = http://127.0.0.1:8080
    chat    = Quark Idlemind
    chat    = *                       # or: anybody who writes

    backstory = example /home/you/characters/hobb.txt

The model is `llama-server` from llama.cpp, reached over HTTP. It is
not interchangeable with Ollama or anything else that speaks the same
chat API, and the reason is the second half of what is used: generation
goes through `/v1/chat/completions`, which everything speaks, but the
**kv cache** goes through `/slots`, which is llama.cpp's own. That is
what lets a conversation be put down and picked up again, and without
it every remark pays for the whole conversation again.

Start the server with somewhere to keep the caches, and with as many
slots as you want conversations live at once:

    llama-server -m model.gguf -c 8192 -np 4 --slot-save-path /var/lib/slbotd/slots

Then tell `slbotd` where that directory is, if it can see it, so it can
tidy up after a conversation it forgets:

    slot-save-path = /var/lib/slbotd/slots

### What the backstory is, and when it is read

One file per avatar, plain text, used as the system prompt. It is what
the avatar *is* — `slbotd` adds one sentence of its own about speaking
through instant messages, because that is about the channel rather than
the character and nobody writing a character should have to explain the
plumbing to it.

It is read **every time it is used**, not held in memory, so working on
a character is an edit and not a restart. That is safe because the
backstory's text is part of the fingerprint described below: change the
words and every kept context built on them stops matching and is built
again from the conversation.

### A character that knows who it is talking to

A backstory line may name a **directory** instead of a file, and then
the avatar can be told something about particular people:

    backstory = example /home/you/characters/hobb/

    hobb/default.txt              used when nothing more specific matches
    hobb/example resident.txt     used when that person is speaking
    hobb/another-person.txt       and that one

**One file is chosen, not two**: the person's if there is one, the
default otherwise.

### Including one file in another

A line in a character that is nothing but a relative path *is* the file
it names:

    ./default.txt

    You have known this one for years. They helped build the dock.
    They ask after your sister; she is well.

That is how a person's file keeps the character rather than replacing
it. Composition is the file's own business, because a fixed rule here
could only say one thing and the useful arrangements are not all that
shape. The shared part often wants to come **last**, nearest the
conversation, which is where a model weighs hardest:

    You have known this one for years.

    ./default.txt

Two people may share a third file that is neither of theirs; a
paragraph several avatars have in common is written once and included
everywhere; and a rule about long absences can live in
`../long-absence.txt` and be included by every character that wants it.

**The rules.** A line that is a path and nothing else — no prose
begins `./`, so a sentence cannot become an include by accident, and a
line with words after the path is left alone as prose. Relative only:
`./` and `../`, resolved against the directory of the file doing the
including, never the daemon's working directory. An absolute path is
not an include and is left as the line it is, so that a directory of
characters can be moved or copied and still be itself. Includes nest,
and a file included from two places is included twice. A file that
includes itself, directly or round a ring, is an error and is said
rather than quietly truncated, as is an include of a file that is not
there.

**What this costs.** The old behaviour added the default to a person's
file automatically. Nothing does that now, so a person's file that
forgets `./default.txt` makes that avatar nobody in particular for that
person, quietly. That is the price of the file deciding.
`slbotd --check` is the answer to it: it assembles every character,
includes and all, and reports what each came to, which is the thing
you cannot see by reading one file.

    example      /home/you/characters/hobb: a default, and 2 for particular people
                   another-person.txt       412 characters
                   default.txt              389 characters
                   example resident.txt     BROKEN: .../hobb/defualt.txt: no such file or directory

**Naming the files.** Lower case the person's name; a `.txt` on the end
is optional, and spaces may be written as hyphens or underscores, so
`example resident.txt`, `example-resident` and `EXAMPLE_RESIDENT.TXT`
are one person. A file may be named by their uuid instead, the way the
`trusted` and `chat` lists take either. The directory is listed and
matched rather than guessed at, so there is no spelling that works only
because somebody thought of it.

Everything is read on the turn it is used, so a character appearing for
somebody who has just started talking — or an edit to a file three
includes deep — does not need a restart.

One consequence in the kv cache. Two conversations with one avatar used
to share the whole backstory as a prefix; they now share only whatever
their characters have in common, and each person's kept context is
fingerprinted with the character *they* were answered under. Editing one
person's file costs that one conversation its cache rather than every
conversation that avatar is holding.

### Noticing that time has passed

An avatar with no sense of time invents one. Asked how long it had
been, with nothing to go on, the model said "just a few days" in four
tries out of four — and on the live grid, after fourteen minutes of
silence, one said "yes, just a few minutes late".

So when a conversation has been quiet for longer than `chat-gap` — an
hour by default — the next remark carries the elapsed time:

    (It has been 3 days since they last wrote to you.)

    Hello again.

**Nothing is forgotten by this.** The turns, the summary and the kept
context are exactly as they were; the avatar is told that time passed
in the conversation it was already having, not given a new one.

It rides on the **remark** and not in the system prompt, for two
reasons. Everything before the last message is the cached prefix, and a
sentence up there that changes every turn would throw away the whole
conversation's kv cache on every single reply. And it is *sent* rather
than *stored* — what goes into the record is what the person actually
wrote, so the hint never reaches the summariser and cannot come back
later as something they said.

The hint alone does nothing at all: measured against a 3B, the elapsed
time offered three different ways was mentioned in **none of thirty**
replies, though asked outright the model could report it, so it was
landing and simply would not volunteer it. What makes it work is one
sentence in `Medium`, which every avatar gets:

> If you are told how long it has been since they last wrote, remark on it.

With that, about half of replies remark on a long absence in their own
words — *"Three days, that's a while. How are you holding up?"* Half,
not all, and about one in twenty-five contradicts the hint outright.
It is an improvement on inventing a duration rather than a guarantee,
and a better model is the fix for the rest of it.

### Keeping a conversation, and what that costs

The conversation is kept as **text**, under `chat-dir` (by default
`~/.local/state/slbotd`), one file per avatar per person. That is the
record. The kv cache is an accelerator and is treated as disposable,
because it is welded to one model, one quantisation, one context size
and one server build — and **the server does not check any of that
before loading one.** A state saved under one model and restored under
another loads cleanly and answers nonsense. So `slbotd` records a
fingerprint of all four, plus the backstory, and refuses to restore a
cache whose fingerprint has moved.

A conversation file that will not read or parse is not thrown away.
`slbotd` renames it to `THEIR-UUID.json.unreadable-TIME`, beside where
it was and never over another, says so in the log and under `:errors`,
and begins that conversation again.

Measured against `llama-server` b11056, Qwen2.5-0.5B-Instruct Q4_K_M,
four slots of 2048 tokens, on an Intel i9 with no GPU offload:

| | |
|---|---|
| a 1442-token prompt, cold | **5.0 s** (288 tokens/second) |
| saving those 1457 tokens | **13.1 ms** (17,927,560 bytes) |
| restoring them into another slot | **5.4 ms** |
| the next turn, warm | **0.53 s** (1442 of 1461 cached) |

So picking a conversation up costs about five milliseconds and saves
about five seconds. The cost of carrying conversations is the disk they
sit on, not the time to resume them.

The size is what to budget for, and it is decided by the shape of the
model rather than its size:

    bytes per token = 2 × layers × kv_heads × head_dim × bytes_per_element

That model came to 12,304 bytes a token, which is exactly
`2 × 24 × 2 × 64 × 2` plus a header. Grouped-query attention dominates
it: a three-billion-parameter model with two kv heads costs less per
token than a smaller one with eight. `--cache-type-k q8_0
--cache-type-v q8_0` roughly halves it.

Below a few hundred tokens, restoring moves more bytes than
re-prefilling would have recomputed. It is long conversations that pay.

### Remembering more than fits

A context window holds a few thousand tokens; a conversation that goes
on for weeks does not. Dropping the oldest exchanges keeps it inside
the window and gives the avatar no memory at all — it forgets your name
between Tuesday and Thursday, and it forgets it silently.

So when `chat-context` is reached the old turns are **compacted**
rather than dropped: handed to the model, which writes a short note,
and replaced by that note. The last `chat-keep` turns stay word for
word, because they carry the thread of what is being said now.
Compacting again folds the note in with whatever has accumulated since,
so one note always stands for the whole conversation before the last
few exchanges.

    chat-context = 1536      # the budget, in tokens
    chat-keep    = 6         # turns kept verbatim
    chat-summary = 200       # how long the note may be

**The backstory is not part of any of this and cannot be lost to it.**
It is read from its file on every turn and is always the first message;
only turns are ever summarised, and the summariser is never given the
character at all — there is no path through the code on which the two
meet. That is structural rather than careful.

Measured live, against Qwen2.5-3B-Instruct Q4_K_M, with the budget set
low to force it. Sixteen turns of a conversation about a wharf became:

    THEM: Quark, chandlery, upriver
    TOPICS: ropes, tide, north berth, salt barge, weather, new hands
    OWED: Three coils, Thursday

The prompt went from 503 tokens to 279. The rope had been asked for
sixteen turns earlier and was no longer in the conversation at all —
and asked "remind me what I asked you to hold?", the avatar answered
"Three coils, Quark." That answer came from the note and from nothing
else, which is the whole of what this is for.

### Why the note is three labelled lines

Because asking in prose does not work, at any size worth running, and
that was measured rather than guessed. The first version of the
instruction said *"write a brief note, in the third person, of what has
passed between them"*. Against a 0.5B it produced fragments of the
transcript separated by rules; against a 3B it copied the exchange back
**verbatim**. Neither summarised anything, and both lost the name the
person had given — the single most useful fact in the conversation.

Three labelled lines with a rule for the empty case worked on the 3B
first time. They also survive being folded again, which is the property
the whole thing rests on: given that note plus four more turns, the
next fold kept the name, the trade and the promise, and added the new
topics to the middle line. A prose note has nothing to hold on to and
drifts; a labelled one has three places to put things and keeps them.

The fields are what somebody means when they say an avatar remembers
them: who they are, what has been talked about, and what was agreed.

### Saying who NOT to talk to

An entry with `!` in front of it is somebody the avatar will not answer,
and a refusal beats a permission however the permission was granted:

    chat = *
    chat = !Quark Idlemind

That is "everybody except", which is the only thing the two lines can
sensibly mean together. Order does not matter, names match without
regard to case, and a uuid may be refused as readily as a name.

### Two of your own avatars talking to each other

If one daemon attends several avatars and each is in the other's
audience, they will talk to each other. That is not a malfunction —
what it produces is a dull but perfectly ordinary conversation — and
`!` is there for an operator who would rather it did not happen.

What is wrong with it is that it does not **stop**. A reply from one is
an ordinary remark to the other, neither is answering *itself*, and
neither will ever be the one to get bored. Measured before there was a
bound: one message typed by hand from one avatar to another ran to 26
exchanges in ninety seconds and was still going when it was killed.

So it is bounded rather than banned:

    chat-own = 8          # things they may say to each other; 0 is never
    chat-own-rest = 30m   # quiet that starts the count again; 0 is never

Past that, one of them stops answering and says why in the log, until
the two have said nothing to each other for `chat-own-rest`; then they
may have another conversation of the same length. The count runs across
compactions -- counting only what is still held word for word would
reset the bound at every compaction, which is to say it would bound
nothing -- so a conversation keeps the times of its last few turns for
it. With `chat-own-rest = 0` the count is everything ever said, and a
pair that reaches it never talks again.

An avatar that another program's model drives is, to slbotd, a
stranger, and a conversation with it has the same problem with nobody
on this side to notice. Name it, and it is bounded exactly like two of
this daemon's own:

    chat-bot = Example Bot        # by name, or by uuid; one a line

### Sharing the server

slbotd pins each conversation to a llama-server slot and uses every slot
the server reports. `llm-slots = N` keeps it to the first N (slots 0 to
N-1), so that another program can send `id_slot` for the ones above and
the two never spoil each other's cached conversations. Start the server
with more `-np` to make room; each slot's share of `-c` is its context.

### Answering at the speed of a person

A model answers in a second or two whatever it was asked, and an avatar
that replies before the question has finished arriving is not a person
however well it writes. The tell is not the words, it is the clock.

So a reply is held back:

    read = len(what arrived) / read-cps
    type = len(the answer)   / type-cps

A 46-character remark read at 23 characters a second is two seconds,
and a 32-character answer typed at 3.2 is ten more. The far end sees
nothing for two seconds, then **"typing…"** for ten, then the reply.

**The notice is repeated, not sent once.** A viewer clears somebody
else's "is typing" nine seconds after the last notification it saw
(`OTHER_TYPING_TIMEOUT`, `llfloaterimsession.cpp:72`) and re-sends its
own every four while the person is still at the keyboard
(`ME_TYPING_TIMEOUT`, line 71). So a single notification at the top of
a thirty-second wait shows for nine seconds and leaves the other side
looking at nothing for the remaining twenty-one. `slbotd` sends one
every four seconds for as long as the wait lasts, which is what a
viewer does.

**Held, not delayed.** The model's own seconds count towards it,
measured from when the remark *arrived* rather than from when the
answer came back — so a reply the model laboured over goes out at once,
and a slow machine does not cost you twice.

    read-cps = 23.0          # the default for every avatar
    type-cps = 3.2
    read-cps = builder 12    # and what this one does instead
    type-cps = builder 1.5
    pace-max = 0             # a ceiling on the whole wait; 0 is none

Avatars are not all the same person. One may type with two fingers and
another answer the instant they have read it, and that difference is
more of what makes them separate people than a backstory is. A line
with one word is the default; a line with two names an avatar. Which it
is comes from how many words there are and not from what they look
like — a profile name is never a number.

Characters a second rather than words a minute, deliberately: it is
what you are actually judging when you set it, where a word is a
fiction of five characters that exists to make typing tests comparable
and has to be divided by five in your head before it means anything.

`pace-max` is **off** by default, because the arithmetic above is the
point and a cap quietly contradicts it. It is there for an operator who
would rather not have an avatar typing for four minutes because the
model felt expansive — which a `chat-max-tokens` of 160 at 3.2
characters a second is. If replies feel too slow the thing to turn down
is `chat-max-tokens`; short answers are more conversational anyway.

The pause is spent **visibly**, and that is not decoration. Twenty
seconds of silence reads as nobody there; twenty seconds of typing
reads as somebody thinking what to say. Without the notice this would
make an avatar seem *less* alive rather than more.

### What it costs, and what it is not

Compacting is a second call to the model, on the turn that crosses the
budget, and the person waiting for a reply waits for both. It happens
once every several dozen exchanges. Doing it afterwards in the
background was the alternative, and it would have a second goroutine
writing to a conversation while the next remark is being answered out
of it.

If the model will not write a note, the turn does not fail: the oldest
exchanges are dropped instead — which is what this replaced — the reply
still goes out, and the log says which of the two happened.

It is lossy and it drifts, and it is meant to. What it buys is not a
transcript, it is the perception of having been talked to before. A
conversation folded a dozen times will have lost detail; it should
still know who you are and what it agreed to.

`:chat` prints the note, so what an avatar believes it remembers is
something you can read.

### How slots are handed out

`llama-server` has a fixed number of slots and hands them out itself
unless told which to use. Left to it, a conversation lands somewhere
different every turn and its cache is always somewhere else — so
`slbotd` pins them: a conversation keeps the slot it spoke in, and when
there are more conversations than slots the one that spoke longest ago
gives its slot up and is picked up again from disk next time it is
spoken to. A slot taken from somebody else is **erased** before it is
used, because two conversations with one avatar share a prefix — the
same backstory — and the server would otherwise match it and carry the
wrong person's words into the reply.

`slbotd` uses every slot the server reports, so anything else sent to
the same server — slsh's `how`, pointed at it — lands in a slot that
holds one of these conversations and replaces its cache without
`slbotd` knowing. The slsh guide, under *Setting up a model for how*,
says what that is likely to cost.

### Who it answers

`chat` is a list of its own and not a flag on `trusted`. Driving an
avatar and being spoken to by one are different powers, and somebody
may reasonably have either without the other. A name, a uuid, or `*`
for anybody.

The decision is one Go function — `Audience` in `audience.go` — and the
list is the first answer to the question rather than the last. Whether
to answer a stranger will eventually want to weigh who they are, what
they said, how often they have said it and what the avatar is doing;
all of that belongs in that one function, which is why the message
handler asks it a question instead of testing a list.

Every verdict carries a reason, and the reason is logged. An avatar
that declines to speak does so silently — that is what declining to
speak is — so the line in the log is the only evidence the decision
happened at all.

### Two practical notes

`chat-jobs` (2 by default) is separate from `jobs` on purpose. A reply
takes seconds where a command takes milliseconds, and a busy region
would otherwise fill an avatar with small talk and leave no room for
anybody to drive it. Beyond the budget a remark is answered by silence,
which for conversation is the right answer: a reply that arrives four
minutes later, behind two others, is worse than none.

And a failure is silence too. There is nothing useful to tell somebody
whose remark could not be answered — they did not ask this daemon a
question, they spoke to an avatar — and "the model is down" said to a
stranger is worse than the silence it replaces. It is in the log.

`:chat` says what model is behind an avatar and who it has been talking
to; `:forget SOMEBODY` drops a conversation, which cannot be undone,
because the text is what the conversation is made of.
