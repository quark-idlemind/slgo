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
square the grid really answered with uses one.  Every real uuid of that
kind is listed in `tools/known-uuids`, with where
Linden publishes it, and a real uuid that is not on that list does not
belong here.

### What to do instead

Invent a name and invent the uuid. Keep the invention consistent across
the repository -- one invented name per real one, everywhere -- so that
a measurement written up in `doc/` and the test built from the same
capture still agree with each other.

When inventing a uuid, keep the first four characters of the real one
and invent the remaining twenty-eight. Anything that sorts by id then
sorts the way it did, which is what several tests depend on, and
twenty-eight invented characters is no longer anybody's identifier.

And mark it: groups two and three of an invented uuid are always
`fa4e-fa4e`, as in `2ce77e57-7e57-c0de-a128-5caa99806c61`.  A real id and
a convincing invention look exactly alike, and without the mark the only
way to tell them apart is to go and search the live grid's logs for each
one.  With it, an id is either marked, or a placeholder nobody could
mistake for one (a group of one repeated character, like
`c75d7e57-7e57-c0de-b372-000000000001`, or sixteen bytes in order), or
in `tools/known-uuids` -- and `tools/check-identities` refuses any other.
The mark sits in the middle so that the first eight characters, which
the documentation elides ids to, and the tail, which orders a family of
ids, are left alone.

A uuid appears in this tree in more than one shape and they have to be
kept in step: hyphenated text, hexdump groups of eight
(`sl/texture_test.go`), and Go byte literals (`msg.UUID{0x89, ...}` in
`cmd/slsh/faces_test.go`). A search that finds only the hyphenated form
will leave the others behind and the tests will fail somewhere else
entirely.

A grid square is invented too, and marked the way a uuid is: the high
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
real. Its name is not the one in the capture, its uuid carries the mark,
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
    tools/check-identities --all      the whole tree
    tools/check-identities --install-hook

It reads the list of real names from `~/.config/slgo/identities.tsv`,
or from `$SLGO_IDENTITIES`. That list is deliberately not in this
repository -- writing the names down here is the thing being prevented
-- so it lives on the machine that did the measuring, one
`real<TAB>replacement` per line. Somebody who has never touched the
live grid has no such file, and the check says so and passes rather
than failing on them.

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
