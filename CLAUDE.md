# Working in this repository

## Never write down a real person, place or id

This repository is public and most of what is in it was measured on a
live grid, standing next to other people's homes. Everything captured
that way names somebody: an avatar, the parcel they live on, the region
it is in, the object they left running, and the uuid of each.

A place is named by more than its name. A region's grid square is one
map lookup from the region's name -- the `(x, y)` pair says it, and so
do the handle made from it and that handle's bytes -- so an invented
region name written next to the real square hides nothing. That has
happened here: the region names in the measurements were replaced, and
the squares beside them were left.

**Nothing that identifies a real resident, their land, their objects or
their groups belongs in a file here.** Not in documentation, not in a
test fixture, not in a comment, not in an example, and not in a commit
message.

The one exception is the person who owns this repository, and the one
avatar that is his: **Quark Idlemind**, whose profile is `qi`.

Nobody else is an exception, and in particular the other avatars a
daemon here may hold are NOT. They belong to other people, who lent
them for measuring; their names, their profile handles and anything
they own are elided exactly like a stranger's. Do not reason from a
credential being in somebody's config file to the account being theirs
-- that inference has been made here before and it was wrong.

Linden Lab's own published constants stay as they are, because they are
protocol rather than people: the built-in animation asset ids in
`agent/posture.go`, the grid names, "Governor Linden", and the parcel
name "Protected Land", which `cmd/slsh/parcel.go` matches on.  So do
Linden's public sandbox regions, their names and their squares: nobody
lives in a sandbox, and a test that has to check arithmetic against a
square the grid really answered with uses one.  So does the null key,
`00000000-0000-0000-0000-000000000000`, the id that names nothing.  Every
real uuid of that kind is listed in `tools/known-uuids`, with where
Linden publishes it, and a real uuid that is not on that list does not
belong here.

### What to do instead

Invent a name and invent the uuid. Keep the invention consistent across
the repository -- one invented name per real one, everywhere -- so that
a measurement written up in `doc/` and the test built from the same
capture still agree with each other.

An invented uuid carries one signature, and there is no other way for
an id to be here:

    xxxx7e57-7e57-c0de-xxxx-xxxxxxxxxxxx

The first group is four random hex digits and `7e57`, groups two and
three are `7e57-c0de`, and everything else is random, as in
`ab3f7e57-7e57-c0de-9d41-6c2e08f1b7a5`. Make one with `tools/new-id`
(`-n 5` for five, sorted); it refuses a first group that any id in the
tree already uses. Nothing of a real id is kept -- not its first four
characters, not its tail -- because a kept fragment is a fragment of
somebody's identifier. A real id and a convincing invention look exactly
alike, and the only way to tell them apart without the signature is to
go and search the live grid's logs for each one; with it, an id is
signed, or it is in `tools/known-uuids`, and `tools/check-identities`
refuses any other. There are no exceptions: not a placeholder (a
repeated character, or bytes in order), not a test's throwaway. An id that has to sort a family of ids is made with
`tools/new-id -n`, which sorts what it makes.

An id elided in documentation is written as its first group, `ab3f7e57-...`,
and because that group ends in `7e57` the elision shows it was invented.
Elide to the first group and no further.

Every other 128-bit value is the same shape without the hyphens, and is
signed the same way: `xxxx7e577e57c0dexxxxxxxxxxxxxxxx`
(`tools/new-id --hash`). That is a `$1$` password digest -- a password to
the grid, because the viewer sends `$1$` and the MD5 of the password --
and a login's `mac` and `id0`, which name a machine; a digest in a
captured login is always replaced, and zeros are no exception, because
a blank is a value too. The one digest a test has to work out for real
from a string it names is in `tools/known-hashes`, with the string.

A uuid appears in this tree in many shapes and they all have to carry the
signature. `tools/scan-ids` finds each one -- hyphenated in either case,
with or without braces or quotes or a URL round it, without hyphens, a
hexdump in groups of eight or pairs or fours (wrapped or not), a Go byte
literal over several lines (`msg.UUID{0x89, ...}`, `[]byte{...}`,
`[16]byte{...}`), `"\x89\x03..."` escapes, base64, a format string that
makes ids (`"...-%012d"`), and an id elided to its first group -- and
`doc/identities.md` lists them with what each one is checked against. A
search that finds only the hyphenated form leaves the others behind.
What the tool cannot see is an id derived from another by arithmetic:
a literal derived from a real id lacks the signature and is caught, and
one a test computes from a signed id is fine, but two uint64s or a big
integer are for review.

A grid square is invented too, and marked in the way a uuid is signed: the high
byte of both coordinates of an invented square is always `0xAA`. On
each axis the invented square is `base + (real - centre)`, where
`centre` is the real square of the region the group is about. A region
and its neighbours move together, so the one to the west is still x-1
and a test of a crossing still crosses, and nothing of the real number
is left. The first group, the home region's, has the base `0xAA80` on
both axes; each further group gets a base of its own, anywhere in
`0xAA00`-`0xAAFF` on each axis, so that no two regions share a square
and the distance between groups is not kept either. In decimal a marked
square is 43520 to 43775. Every form is worked out again from the
invented square rather than edited by hand: the pair,
`msg.RegionHandle(x, y)`, the decimal handle and its bytes. Linden's
sandboxes keep their real squares, and each one used is in
`tools/known-squares` with where it was observed. `tools/check-identities`
reads the square out of every handle it finds and refuses one that is
not marked, not a placeholder with both coordinates below 16, and not
on that list; a bare `(x, y)` it leaves to the list of real names below.

Names hide in the same way. They wrap across comment lines, they appear
downcased in tests that check case-insensitive matching, they turn up
as Go identifiers (a name welded to a word, like `<place>Asset`),
and one region name may be the
prefix another test searches by.

### An example is made up, not copied

Most of what has leaked here did not leak as a capture. It leaked as an
example: a comment that needed a region and took the one in the log it
was written from, a test that needed a person and took the one who
answered, a man page that needed output and pasted the last run with its
names swapped and its numbers left alone.

So an example is invented from nothing, not adapted from something
real. Its name is not the one in the capture, its uuid carries the signature,
and its numbers were never on the grid. The same goes for an example
somebody gives in conversation to say what they mean: that is the real
thing, told so that it is understood, and what goes in the file is an
equivalent made up for the purpose.

A measurement is the one case where the real thing is the point. It
goes through the replacement list whole, and every number in it is
looked at, not only the words.

### Saying what was replaced

A change that takes a real name out of the tree must not write it down
again on the way out. Its commit message says what was replaced and
where -- "invent the region squares in the neighbour tests" -- and never
what it was: not the name, not part of it, not a description that picks
it out ("the parcel next to ours", "the avatar that was lent"), and not
"A becomes B because A is a real name". The same holds for a pull
request, a release note, a comment and an issue title.

Taking something out of the tree does not take it out of the history:
the diff of the commit that removes it shows it. So anything that was
ever committed is also written into the record of what the history
holds, `public-audit-*.md`, which is untracked, by its replacement and
never by itself, so that the rewrite before publishing catches it.

### Checking before a commit that adds captured output

Capturing live output is how measurements get into this repository and
it is worth doing; it is the reason to read what came back before
saving it. A transcript of `parcel --region` is a list of the
neighbours' parcels. A landmark listing names the places somebody has
been. `objects --owner` names people.

    tools/check-identities            what is staged
    tools/check-identities --all      the whole tree, and the tags
    tools/check-identities --message FILE   a commit message
    tools/check-identities --install-hook   before every commit and message

Every check runs and the exit status says if any found something: a
compiled binary, a network address, an id without the signature, a grid
square that is not marked, a name on the list. Only the last needs the
list; the others need nothing, and run for everybody. An id in a file
name, a tag or a commit message is as written down as one in a file, and
`doc/identities.md` catalogues every form an identifier or a name takes
here and which check, if any, sees it.

It reads the list of real names from `~/.config/slgo/identities.tsv`,
or from `$SLGO_IDENTITIES`. That list is deliberately not in this
repository -- writing the names down here is the thing being prevented
-- so it lives on the machine that did the measuring, one
`real<TAB>replacement` per line. Somebody who has never touched the
live grid has no such file, and that one check says so and is skipped
rather than failing on them.

When a measurement brings a new name, uuid or grid square in, add it to
that list at the same time; a checker is only as good as what it has
been told about. A number goes in the forms it is written in -- a
square as its pair, a handle in decimal and as bytes -- and never bare,
where it would match a byte count somewhere else.

## Never commit a binary

Nothing compiled belongs in this repository. Go leaves a binary beside
its source when built inside a `cmd` directory and in the root when
built by path, and both are ignored by name in `.gitignore` -- a list
that only works if a new command is added to it, so add it when you add
the command.

Two binaries were committed once and rode in the history for months, 36
MB between them, because neither list had heard of their names. They
were found by accident, while grepping the whole object database for
something else. A compiled binary also carries whatever string
constants the source had at the time, which is the other reason not to
keep one: it is a copy of the tree that no sweep of the tree will ever
reach.

`tools/check-identities` refuses a staged file that looks compiled, and
says what to add to `.gitignore`. If you find one already committed, it
goes in `.gitignore` and out of the tree in the same change.

## The issue log

If an `issues/` directory is present, it is the list of what is known
to be wrong with this tree and has not been fixed: `issues/INDEX.md` is
every issue on one line each with a severity, and there is a file per
issue behind it.  Read the index when asked to review issues, or before
starting work in an area, and add a file and a row when something new
turns up -- `issues/README.md` says how.

It is untracked, deliberately: an issue is worth having only if it says
what was actually seen, and what was seen quotes the live grid.  So a
checkout has no issues directory, and that means nobody has written one
here rather than that nothing is wrong.

## Measurements

Where a document here says what happens, it was watched happening. Do
not write a measurement that was not taken, do not compose a transcript
out of two real ones, and do not tidy the grid's own words. If
something was inferred rather than measured, the sentence says so.

## Comments are short, and the reasons live in doc/

A comment says what the code does, where that is not plain from the
code, and the constraint that makes it so -- in a few lines. The story
of how it came to be that way is worth keeping, and it goes in `doc/`:
the measurement, what was tried first and failed, the transcript. The
code keeps one line pointing at it:

    // Poll the destination until the item shows: a UDP move has no
    // reply, and one into Trash is accepted and ignored.
    // Why: doc/<topic>.md#<section>

A reader should be able to understand the code without opening the
document, and open it only to learn why this path was chosen over
another. The test for what stays is whether it helps someone reading
the code, not its length. A long comment is worth a second look, but
there is no limit, and a doc comment that says how to use a package or
a function is often rightly long.

A comment that says something the code no longer does is worse than
none, because a reader believes it, and a model believes it more readily
than a person does. So a change that makes a comment untrue fixes the
comment in the same change.

## Shared helpers

Some waits, and a few other things, are easy to get wrong and are
written once. Use these rather than a loop of your own, and add a line
here when another is shared the same way.

- `poll`, in `sl/session.go`: asks a read -- AIS, the backend -- until
  it holds, returning `ctx.Err()` at once when the caller gives up and
  `ErrTimeout` at the deadline. A loop of read, `time.Sleep`, go round
  cannot hear a cancel, and `TestNothingInThisPackageSleeps` refuses
  one anywhere in `sl`; a plain pause is `Session.Settle`. A call that
  asked for something to be made follows a cancelled poll with
  `lastLook`, beside it, and hands back what that finds.
- `Session.await`, in `sl/session.go`: waits for what the session has
  been told to satisfy a predicate, checked under the session's lock,
  and quotes in its timeout any alert the simulator sent meanwhile.
- `Session.local`, in `sl/region.go`: the local id to send for an
  `Object`. One found in another region, or another run of this one, is
  looked up again by its uuid, and one that is not here is refused with
  `ErrNotHere`. A local id read straight off `o.Local` into a message
  names whatever has that number where the avatar is now, and
  `TestNoLocalIDIsSentStraightOffAnObject` refuses one.
- `PickNamed`, in `sl/pick.go`: the one inventory entry or item inside
  an object that a name means, matched exactly, in the case it has. It
  refuses a name several things have, listing their ids, and a name
  nothing has, offering what differs from it only in case; `AllNamed`,
  beside it, is the same match for a command that acts on every one of
  a name, and `PickNamedFunc` and `AllNamedFunc` are the two for things
  whose name is read by a function -- a worn attachment, an outfit
  link. A person's name is not one of these, and is matched ignoring
  case. `TestNoInventoryNameIsMatchedIgnoringCase` refuses a
  `strings.EqualFold` on a `.Name` or `.Path` in `sl`, `slsh` or
  `slbotd` until it is listed there with what it matches.
- `Conn.sendPacket`, in `client/client.go`: every send on an attach
  stream, one at a time under `sendMu`, since gRPC allows one sender per
  stream. `TestEverySendOnTheStreamIsOneAtATime` refuses a send anywhere
  else in `client`.
- `Conn.agentName`, in `client/client.go`: the session a connection is
  attached to, read under `mu`, which `attach` writes it under.
  `TestTheAgentIsReadOnlyUnderTheLock` refuses a read of `c.agent`
  anywhere else in `client`.
- `Sender.Label`, in `sl/sender.go`: a name as a line prints it, with
  what it names in front -- `[Object]`, `[Group]`, `[Conference]`, `[Grid]` -- and a
  person's bare; the kind comes from `IM.Sender` or `Line.Sender`. An
  object's name can be anybody's, so slsh and slbotd print no sender
  any other way. `TestNoObjectNameIsPrintedBare` refuses an
  `ObjectName` used outside `Label` in `sl`, slsh or slbotd.
- `sl.Options`, in `sl/options.go`, set with `Session.SetOptions`: how
  long a move, a permission change and a delete are read back before
  they are reported not confirmed, 15, 15 and 10 s by default, and how
  long each wait for the grid about L$ -- `Balance`, a payment's answer,
  the balance read after that answer did not come -- lasts, 15 s, and how
  long a group chat's or conference's start is waited for, 30 s. A
  test that proves one of them runs out sets it short rather than
  waiting the default out, as `sl`'s do and `shortReadBacks` does for
  `cmd/slsh`'s.
- `pay.Gate`, in `internal/pay`: the one check of a
  `MoneyTransferRequest`, and of every other message that spends L$
  (`pay.Spends` lists them: buying an object, land or a pass, joining a
  group, a classified), against a profile's rules for paying, and the
  record of what it has paid. slgod runs it where it forwards a client's
  message (`server/pay.go`) and `sl.Direct.Send` for a session held
  without a daemon, so every payment and purchase a program sends passes
  one of the two; a viewer's does not, on purpose.
  `TestEveryMessageThatCarriesAPriceIsCheckedOrListed` fails for a
  message added to the template that carries a price and is neither in
  `Spends` nor listed there with why not. slbotd pays nothing, and
  `TestNothingHerePays` refuses a payment anywhere in it.
- `Server.SetLog` (and `SetBase`), in `server/server.go`: a session's
  `Log` is given when `StartAgent` makes it, before its circuit is up,
  and is never assigned afterwards; a handler reads it from the first
  message. `TestASessionLogsFromTheFirstMessageItHears` fails for one
  assigned after.
- `tools/new-id` and `tools/scan-ids`: the one way to make an invented
  id (signed, with a first group nothing else uses, sorted when you ask
  for several) and the one place that knows every shape an id is written
  in. A new shape goes in `scan-ids`, with a case in
  `tools/identities_test.go`, and not in a pattern of your own;
  `TestNothingInTheTreeIsAnUnsignedIdentifier` fails for any id in a
  tracked file that is neither signed nor in `tools/known-uuids`.
- `WatchSilence`, in `agent/agent.go` (`watchSilence` inside the
  package): calls a function once when a circuit has heard nothing for
  longer than a timeout, by a last-heard time it is given, looking a
  quarter of the timeout at a time and at most once a second. The
  root circuit's watchdog, each child circuit's and slgod's watch on an
  attached viewer run on it, the last two on the viewer's own circuit
  timeout of 100 seconds.
