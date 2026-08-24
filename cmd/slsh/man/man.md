A man page says what a command is for and where it will surprise you.
It is the place the reasoning behind a command lives: why wear adds
instead of replacing, why place takes exactly one argument, what a
group invitation with a fee wants typed at it.

For the flags, ask the command itself -- "place --help" -- because man
does not repeat them.  What it prints above the prose is derived from
the same table --help reads: the command's name and its one-line brief
as a heading, the usage line under that, and then the page.  A page
that wrote its own flags out would be a second copy of them, and the
copy that went stale first.

With no name, man lists the commands that have a page.

## That listing is of names, not of commands

Every command has a page now, so the listing is the whole command set
-- and it is three names longer than "help all".  Two names can be one
command, and man both answers to each and prints each, where "help all"
walks the commands and prints one name apiece.  So quit is in man's
listing where help's has exit, and the same for source against "." and
unsit against stand.  The page underneath is one page either way, and
"man exit" and "man quit" print the same words.

Anyone hunting a listing for a name they are sure exists wants this one
rather than help's, for exactly that reason.

## An unwritten page is not an error, and a missing command is

No command is without a page today.  That was not always so and need
not stay so, and when it happens man says which case it is: the name,
the brief and the usage line come out as they always do, and under them
a sentence saying the page is unwritten and what to type instead.
Answering "no such thing" would read as man being broken rather than as
somebody having got there first.

A name that is no command at all is the other case and is refused,
pointing at "help all", because it is a typo.

One name at a time.  A page is prose about one command, so there is
nothing sensible to do with two, and the refusal says so rather than
guessing which of them was meant.

## How wide it comes out

Paragraphs are wrapped to the terminal's width less a small margin, and
never wider than 78 columns however wide the window is: past about
eighty the eye loses the line on its way back to the left margin.  Nor
narrower than 40: a window under that gets 40 columns and lines that
run off the edge of it.

The width is the terminal's whether the page is being read or written
out, since redirection is not looked at.  So "man wear > wear.txt" in
an eighty-column window gives a file wrapped at 78, and the same
command in a fifty-column window gives one wrapped at 48.  A shell with
no terminal to measure -- "slsh -c", "slsh -f", input from a pipe -- is
the case that always gets 78.

The heading, the usage line and the indented examples are printed as
they were written and are not wrapped, so a long one of those is the
one thing that can pass 78.  Headings are shouted, capitals being the
only emphasis that survives arriving in a file.

## Examples

    man place
    man wear > wear.txt

See also: help, which is the same command set grouped by the question
somebody has, and the command itself, which answers what it takes.
