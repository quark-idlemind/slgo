"." runs the commands in a file, one to a line, as though each had been
typed at this prompt.  "source" is the same command under a second
name.

It is the other half of redirection, and the two together are the point
of this shell being a shell: a listing written out with ">", edited in
whatever editor is to hand into a list of commands, and run.  Blank
lines are skipped and so is anything beginning with a hash, so an
edited listing keeps its headings and its notes.

Each line is a command line and nothing more.  There are no variables,
no loops and no conditionals, and a line may redirect its own output
exactly as it would at the prompt.

## It stops at the first failure

The rest of the file is not run, and the report says which line failed,
what it said, and how many lines were left.

That is not caution for its own sake.  A file of this kind usually
begins by changing folder, and carrying on after that failed would run
every remaining line somewhere else entirely -- which, when the
remaining lines are removals, is not a thing to discover afterwards.
What failed has already been reported where it happened, so the stop is
announced without repeating it.

A file may run another, and one that is more than a few deep is refused
on the grounds that a file sourcing itself looks exactly like this.

## Running one without a prompt at all

The shell will take a file on its own command line and run it instead
of reading keys, which is how this becomes something another program
can drive.  A failed line ends that run too, and ends it with a status
that says so, rather than leaving whatever called it to read the
output and guess.

## Examples

Write a listing out, edit it into commands, and run it:

    find probe > probes
    . probes

See also: echo for putting a line into such a file, quit for ending one
that is meant to be the whole session, and "help all", which is where
the redirection is written down.
