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

The one exception is Quark, who owns this repository and its avatars:
Quark Idlemind and the alts the profiles name.

Linden Lab's own published constants stay as they are, because they are
protocol rather than people: the built-in animation asset ids in
`agent/posture.go`, the grid names, "Governor Linden", and the parcel
name "Protected Land", which `cmd/slsh/parcel.go` matches on.

### What to do instead

Invent a name and invent the uuid. Keep the invention consistent across
the repository -- one invented name per real one, everywhere -- so that
a measurement written up in `doc/` and the test built from the same
capture still agree with each other.

When inventing a uuid, keep the first four characters of the real one
and invent the remaining twenty-eight. Anything that sorts by id then
sorts the way it did, which is what several tests depend on, and
twenty-eight invented characters is no longer anybody's identifier.

A uuid appears in this tree in more than one shape and they have to be
kept in step: hyphenated text, hexdump groups of eight
(`sl/texture_test.go`), and Go byte literals (`msg.UUID{0x89, ...}` in
`cmd/slsh/faces_test.go`). A search that finds only the hyphenated form
will leave the others behind and the tests will fail somewhere else
entirely.

Names hide in the same way. They wrap across comment lines, they appear
downcased in tests that check case-insensitive matching, they turn up
as Go identifiers (`ThrushmoorAsset`), and one region name may be the
prefix another test searches by.

### Checking before a commit that adds captured output

Capturing live output is how measurements get into this repository and
it is worth doing; it is the reason to read what came back before
saving it. A transcript of `parcel --region` is a list of the
neighbours' parcels. A landmark listing names the places somebody has
been. `objects --owner` names people.

    git grep -niE 'a name you know is real|another one'

is the whole of the check, and it takes a moment. Do it while the
output is fresh, rather than leaving it for whoever reviews the branch.

## Measurements

Where a document here says what happens, it was watched happening. Do
not write a measurement that was not taken, do not compose a transcript
out of two real ones, and do not tidy the grid's own words. If
something was inferred rather than measured, the sentence says so.
