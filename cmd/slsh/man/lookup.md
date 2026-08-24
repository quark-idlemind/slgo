lookup searches the whole grid for people by part of a name.  It is the
wide question, where "who" asks who is standing in this region and
"friends" asks who is on the friend list; this one reaches somebody who
is nowhere near and has never been mentioned here.

The argument is whatever part of a name is remembered, joined with
spaces.  What comes back is a numbered listing in name order, with the
display name in a second column where it differs from the name -- a
display name that is only the name again is a column of noise.

No key is printed, which is where this differs from "who".  The search
knows the key of everybody it found and hands it to whatever the number
is typed at; a hundred keys down the page would bury the names they
belong to, and the one that is wanted comes back from "profile" a line
later anyway.

The listing is what the next command means by a number.  "lookup" then
"profile 3" is the ordinary way round, and the same number works for
anything else that takes a person.

## A full page is not a complete answer

The search answers with at most a hundred names and says nothing at all
about how many it left behind, so a page of a hundred would otherwise
read exactly like the whole of the grid.  A listing that is a full page
therefore says so underneath.  Nothing here asks for the next page:
somebody who typed too little of a name wants to type more of it rather
than read a hundred more names.

## What too little of a name gets is not a page

It is an error from the grid.  Measured on Agni, on one afternoon: every
one-letter search tried came back as a status from the search service
rather than as names -- "a" answered 500, three times running, with the
grid's own "Your request failed to rez" in the body, "b" the same and
"e" a 504.  Two letters were no better, answering 504 for "an", "ar",
"el", "jo", "li", "ka", "lo", "ne", "ro", "sa", "si", "ta" and "za",
while "ab" came back in full with sixty-three names.  So the search
gives up before it has a page to count, and what is printed is the
status as it arrived.

Nothing tried that afternoon reached the hundred either: "kat" found
98, "quark" 95, "sam" 94, "angel" 89.  The full-page line is written
for an answer that was never seen on that run, which is worth knowing
before reading its absence as "this is all of them".

## What the search will and will not match

Where the grid grants this session its name search, part of a name
matches, and so does a display name.  Where it does not, the older
message is used instead and only a whole name matches -- so a search
that finds nothing on a name typed in part is worth trying again in
full before concluding nobody is there.

A listing short of a page is not a complete answer either.  Searching
for "idlemind" answered with eighty names, every one of them ending in
that surname -- and Quark Idlemind, who is on this grid, who answers to
"quark idle" and to "Quark Id", and who would have sorted between two
of the eighty, was not among them.  Eighty is not a full page, so
nothing was printed underneath, and nothing in the reply said anybody
had been left out.  The way to find a particular person is more of
their name rather than more of the listing.

A name found here is not a person who is online, in this region, or
reachable; it is a name the grid knows, and a key to go with it.

## Examples

    lookup example

Find somebody by as much of the name as is remembered, then read what
their profile says:

    lookup quark idle
    profile 1

See also: profile, which makes this same search for itself when nothing
nearer knows the name, who for the people in this region, friends for
the friend list, and im for saying something to whoever was found.
