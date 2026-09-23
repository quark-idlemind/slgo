lookup searches the whole grid for people by part of a name.  `who`
asks who is standing in this region and `friends` asks who is on the
friend list; this one reaches somebody who is nowhere near and has
never been mentioned here.

    lookup example

The argument is whatever part of a name is remembered, joined with
spaces.  A username typed with a dot -- `example.resident` -- is
searched as a name.  What comes back is a numbered listing in name
order, with the display name in a second column where it differs from
the name.

No key is printed unless `-l` asks for one.  The search knows the key
of everybody it found and hands it to whatever the number is typed at,
so most of the time the key is machinery rather than an answer -- and
a hundred of them down the page would bury the names they belong to.

## Options

**-l**

The key as well as the name, between the name and the display name.

    lookup -l example
     1  Example Resident                  a4c67e57-...  Exemplary

It is the one handle that does not change.  A display name is today's,
a name is shared in part by everybody the search turned up, and every
command here that takes a person takes a key instead -- so a key is
what to write down, paste into something else, or put in a script that
cannot type a number at a listing.

The columns before it are fixed width, so the key lands in the same
place on every row and can be cut out of a listing.  The display name
comes last because it is the one field with no width at all.

The listing is what the next command means by a number.  `lookup` then
`profile 3` is the ordinary way round, and the same number works for
anything else that takes a person.

## A full page is not a complete answer

The search answers with at most a hundred names and says nothing about
how many it left behind, so a page of a hundred would otherwise read
exactly like the whole of the grid.  A listing that is a full page
therefore says so underneath.  Nothing here asks for the next page:
somebody who typed too little of a name wants to type more of it rather
than read a hundred more names.

A listing short of a hundred is not a complete answer either.  The way
to find a particular person is more of their name rather than more of
the listing.

## Too little of a name is an error, not a short list

One or two letters often come back as a failure from the search rather
than as names.  More of the name is the answer.

Where the grid grants this session its name search, part of a name
matches, and so does a display name.  Where it does not, only a whole
name matches -- so a search that finds nothing on a name typed in part
is worth trying again in full before concluding nobody is there.

A name found here is not a person who is online, in this region, or
reachable; it is a name the grid knows, and a key to go with it.

## Examples

Find somebody by as much of the name as is remembered, then read what
their profile says:

    lookup example res
    profile 1

Or take the key straight out of the listing:

    lookup -l example res

See also: `profile`, `im`, `offer`, `give` and the other commands that
take a person, which make this same search for themselves when nothing
nearer knows the name -- `profile` taking what it resembles, the rest
only the whole name -- `who` for the people in this region, and
`friends` for the friend list.
