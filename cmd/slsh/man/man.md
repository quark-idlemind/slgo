`man` prints the long description of a command.

    man place

It displays the name, a one-line brief and the usage line, then the page:
what the command does, what the arguments mean, and the traps that
`--help` has no room for.  A command with flags lists them on its page
too, under Options, a paragraph each rather than the column
`place --help` prints.  A command whose only flag is `--help` -- this
one -- has no such section, and then the flags are only the column.

The only argument is a command name.  One at a time.

## The listing

With no name, `man` lists every command that has a page.  Aliases are
listed on their own -- `quit` and `exit`, `source` and `.`, `stand` and
`unsit` -- even though each pair shares a page.  `help` groups commands
by topic and shows one name each; this list is every name you can type.

## Missing pages, missing names

If a command has no page yet, `man` still prints the brief and usage,
and says the page is unwritten.  If the name is not a command at all,
that is an error -- `help all` is the full set.

## Width

The page wraps to this terminal.  Redirected to a file, it still wraps
to the window you ran it in, and the escape sequences a terminal uses
for bold are left out -- they are not text.  Unicode (bullets, rules)
stays.

## Paging

On a terminal, a page longer than the screen is shown a screenful at a
time.  Redirected to a file, or short enough to fit, the whole page is
printed and nothing waits.

| key | action |
| --- | --- |
| space, `f` | forward one screen |
| Enter | forward one line |
| `b` | back one screen |
| `d` | forward half a screen |
| `u` | back half a screen |
| `p` | the top of the page |
| `/text` | the next line containing text |
| `/` | the next match of the last search |
| `?text` | the previous line containing text |
| `?` | the previous match of the last search |
| `n` | the next match in the same direction as `/` or `?` |
| `q` | leave |

At the end of the page, space and Enter leave as well.  Type the
search, then Enter.

Nothing is erased and the screen is never cleared.  Each screenful is
printed under the last, so whatever was on the terminal before the page
began is still above it, and the page itself is still in the scrollback
after the pager has gone.  A key that moves backwards therefore prints
that part of the page again, underneath, rather than carrying the
display back up to it.

## Examples

    man place
    man
    man wear > wear.txt

See also: `help`, and `COMMAND --help` for the same flags in a
column, without the paragraphs.
