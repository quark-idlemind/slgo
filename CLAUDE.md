# Working in this repository

## Never write down a real person, place or id

This repository is public and most of what is in it was measured on a
live grid, standing next to other people's homes. Everything captured
that way names somebody: an avatar, the parcel they live on, the region
it is in, the object they left running, and the uuid of each.

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
name "Protected Land", which `cmd/slsh/parcel.go` matches on.  Every
real uuid of that kind is listed in `tools/known-uuids`, with where
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

Names hide in the same way. They wrap across comment lines, they appear
downcased in tests that check case-insensitive matching, they turn up
as Go identifiers (a name welded to a word, like `<place>Asset`),
and one region name may be the
prefix another test searches by.

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

When a measurement brings a new name in, add it to that list at the
same time; a checker is only as good as what it has been told about.

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
