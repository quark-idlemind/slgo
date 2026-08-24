profile prints what somebody's profile says about them: when the
account was made, what it says about payment, who its partner is, the
web page and the text they wrote, what they say they are looking for,
can do and speak, and the groups they chose to show.  It is a public
question, answered by the grid to anybody who asks, and nothing about
it reaches the person asked about.

    profile Example Resident

Who is named by a name, by a key, or by the number beside a name in the
last listing.  Empty fields say what they do not know rather than
printing a blank: a blank beside "born" reads as an answer that went
missing, where "not said" reads as a profile that has none.  Four are
printed only where there is something in them -- the web page, and the
"wants", "skills" and "speaks" that viewers stopped showing years ago.

The keys stay in the lines.  A key is what everything else here takes,
including this command's own argument, so a partner or a group worth
reading about is one more line to type rather than a search.

## Options

**-w, --wait** *SECONDS*

How many seconds to give the grid.  Fifteen when it is not said.

## This is the one command that searches the grid for a name

Everywhere else, a name is matched against what the session has heard
and then against whoever is standing in the region, and no further.
This one goes on to the grid's own search, because a profile is exactly
the question one asks about somebody who is not here -- and because
guessing wrong costs nothing but a wasted listing, where guessing wrong
for `im`, `offer` or `give` delivers something to a stranger.  Several
people answering to the name are listed, numbered, and a number picks
one.  A name typed in full that matches one row exactly is that row
even when the search returned others.

## Two fields that are easy to misread

"none on file" and "not revealed" are different facts about payment.
An account carrying a caption -- a Linden, or one of the special
accounts -- has its payment information withheld rather than absent.
Where there is caption text, it is printed under the payment line as
"account", so that it does not read as a title somebody chose.  Where
there is not, the payment line stands alone, and that is the answer
rather than a field that went missing.

The groups are only the ones their owner published, which is why the
count says so: somebody may be in a dozen and show none.

## A key nobody knows is a sentence, not a failure

The grid has no way of saying it has never heard of somebody.  The
answer for a made-up key is the wait running out, printed as the plain
sentence it is -- nothing went wrong, and the question was answered.
It is also the only case where the wait is spent, since a profile that
exists arrives in milliseconds.

## Examples

    profile Example Resident
    lookup another
    profile 2

See also: `lookup` for the search this makes on its own, `who` for the
people in the region, and `group` for the groups this avatar is in,
which is a different question from the ones a profile lists.
