`quit` leaves the shell, and `exit` is the same command under a
second name.  What it does to the avatar depends entirely on how
this shell got its session, which is the one thing worth knowing
before typing it.

    quit

Attached to the daemon -- the ordinary case -- the avatar stays
exactly where it is, logged in, with the daemon holding the session
for whatever attaches next.  Started with `--direct`, this shell
logged in for itself and holds the session, so quitting logs the
avatar out of the grid.  The line printed at startup says which of
the two is in force.

Taking a daemon-held avatar down is a separate act against a named
avatar, which is what `logout` is for; no way of ending a shell does
it.

Neither name is the deprecated one and neither is going away.
`help all` prints each command once, under whichever name sorts
first, so it shows `exit` and not `quit`, while `help shell` shows
`quit`.  `man` answers to either and prints the same page.

## The keys that also end it

At an empty command prompt, either interrupt or end-of-input leaves.
Neither does so while there is anything else for it to mean first:
with a line half typed, interrupt clears the line; in chat mode it
returns to the command prompt, so leaving chat and leaving the shell
are the first and second press rather than one; and while a
multi-line answer is being typed, end-of-input finishes the answer
and interrupt abandons it.

## It can be the last line of a file of commands

A file run with `.` may end by quitting, and the shell stops there
-- which is the difference between a file that leaves a session open
for more work and one that is the whole session.  Lines after it are
not run, since there is nothing left to run them.

## Examples

    quit

See also: `logout` for taking an avatar down rather than a shell,
`agents` for what the daemon still holds afterwards, and `.` for
running a file that ends this way.
