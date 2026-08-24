echo prints its arguments.  In a shell with no scripting in it that
sounds like nothing, and it is there for one thing: with a redirection
after it, it is how a line gets into a file.  A listing written out
with ">" is the raw material of a file of commands, and echo is what
adds to it the lines no listing produced -- a note at the top, a
command the listing could not have known about, a blank line between
two batches.

The arguments are joined with a single space, so a run of spaces
between two words closes up unless the words are quoted together.
Quoting and backslashes follow the same rules as anywhere else at this
prompt: quotes group, and a backslash is left alone, because an
inventory path uses it to escape a separator and a shell that ate it
would make those paths untypeable.

## It has no flags at all, on purpose

Every other command here answers --help with a usage message.  This one
prints the words, because the whole point of it is to put a line into a
file and a line may perfectly well be about a flag.  A command that
refused to print the one thing somebody asked for would be a command
refusing to do its only job.  Unix echo makes the same choice for the
same reason.

It follows that there is nothing to ask it for: whatever comes after it
is text.

## Examples

Build a file of commands and run it:

    echo "# clear out the probes left over from yesterday" > tidy
    echo cd Objects >> tidy
    echo rm probe-1 >> tidy
    echo rm probe-2 >> tidy
    . tidy

Note that ">" replaces a file and ">>" adds to it, which is what makes
the first line above start afresh and the rest accumulate.

See also: ".", which runs the file this builds, and "help all", where
the redirection itself is written down.
