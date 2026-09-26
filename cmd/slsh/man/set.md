`set` is the settings, from inside the shell.  There are three
forms, told apart by how many words follow:

    set                     every setting, its value, and what it is for
    set NAME                one of them
    set NAME VALUE          change it, and write it to the settings file

The value is the rest of the line, joined with single spaces, so
nothing has to be quoted to be typed.  That matters for the settings
whose value is a command line of its own:

    set viewer_launch open -a {app} --args --grid {grid}

The one exception is `>`, which is a redirection wherever it is
typed unquoted, with or without a space in front of it:
`set viewer_grid a>b` sets the setting to `a` and puts set's own
report into a new file called `b`.  Quote a value with one in it --
`set viewer_grid "a>b"`.

## It writes the file, and does nothing else to it

Every change is written to the settings file -- `config` in the
directory `$SLSH_CONFIG_DIR` names, otherwise `slsh/config` under
`$XDG_CONFIG_HOME`, otherwise `~/.config/slsh/config` -- and the
file is created, with its directory, if it is not there yet.  The
path is printed every time, since the file is the thing that will
still be true tomorrow.

What changes in that file is the one line for the one setting, and
nothing else.  Everything up to and including the `=` is left
exactly as it stands, so the indentation, the column the equals
signs are lined up in, and whichever spelling of the name is already
there all survive.  A setting the file does not mention is appended,
one line, at the end.  Comments on lines of their own survive
untouched.

A `#` after a value is part of the value rather than a note about
it: a line reading `map_ratio = 7:3  # measured here` is a
`map_ratio` of `7:3  # measured here`, which is not a ratio, and
since a value the setting will not take is refused when the file is
read, it is a shell that will not come up until the line is edited.
A comment has to be on a line of its own.

If the file names a setting twice, it is the last of the two that is
in force, since the file is read from the top and each line
overwrites what came before; that is the line `set` changes, for the
same reason.

## Some of it cannot take until slsh is started again

Three settings are used before there is a prompt to type `set` at:
the address, because the session was attached through it; the
profile, because it says which avatar this is; and the escape key,
because the terminal was already in raw mode reading for it.  A flag
may have overridden the file for this run as well.

They are written to the file all the same, and both the listing and
the answer to a change say so outright.  The listing ends their line
with `at startup only`, and changing one prints the sentence about
the next slsh under it.  The three are `addr`, `agent` and `escape`,
and they are the only three.

Everything else takes at once.  `set map_ratio 2:1` and then `map`
draws the new shape.

## Where the settings come from and what beats what

A flag on the slsh command line beats the file, and the file beats
the defaults.  So a shell started with `--escape ^G` is running with
`^G` whatever the file says, which is the other half of why the
three above are left for the next run.

The names are the names in the file, and the file has always taken
another spelling of some of them -- `server` for `addr`, `profile`
for `agent`, `prefix` or `prefix_key` for `escape`.  Those still
load, and `set` answers to them as well: `set server` prints the
setting under the name `addr`.

A value a setting will not take is refused, with the setting named,
and neither the shell nor the file is touched.  The same refusal
happens when the file is read at startup, where it stops slsh coming
up rather than leaving somebody with a setting that silently does
nothing.

## What there is

`set` is the listing, and it is the one place they are all written
down, so this page does not copy it.  They fall into these:

    addr, agent, escape             slsh itself
    log, log_dir                    the transcript
    viewer_*                        what "viewer --launch" starts
    map_*                           how "map" draws its picture
    notice_keep                     how long "notice" keeps one

The map's are the ones most worth knowing about, because the picture
is drawn to the shape of the font it is being read in: `map_ratio`
is how tall a character cell is against how wide, height first, and
`7:3` is a measurement of one font rather than a fact about all of
them.  See `man map`, which says what each of the map's five does.

## The transcript

`log` is on to begin with, and what it keeps is what you saw: every
command you ran, everything each one printed, everything heard and
everything said.  One file per avatar, named after the avatar --
`example-resident.log` -- in `$XDG_DATA_HOME/slgo`, or
`~/.local/share/slgo` where that is not set.  `log_dir` puts it
somewhere else; empty is the default place.

    2026-08-26 15:56:29 $ where
    2026-08-26 15:56:29   Testville at 33, 70, 2001
    2026-08-26 15:56:33 < [IM Example Resident] are you still at the build
    2026-08-26 15:56:41 > [IM Example Resident] on my way

A command is written down before it runs, so a command that hung is
in the file that says what happened.  `set log off` stops it, and
leaves the file where it is.

Control characters are written the way the screen showed them, `^[`
for ESC and so on -- see `man chat` -- so that reading the file with
`cat` or `tail` later does not hand a stranger's escape sequences to
that terminal instead.

Two things are deliberately not in it.  Output that was redirected --
`ls > listing` -- is a listing you did not see, so the command is
recorded and the listing is not; it is in the file you sent it to. A
man page read at a terminal goes through the pager rather than to the
screen a line at a time, and is not recorded either.

What no shell can record is what happened while none was running.
slgod stays logged in and goes on hearing, but a transcript is what
somebody saw, and there is nobody there to see it.

## Measuring the font

Some terminals will say, and slsh will ask them:

    set map_ratio auto

That asks for the size of one character cell in pixels, and writes
back whatever comes.  The numbers written are the ones reported, not
reduced -- `18:10` rather than the `9:5` that means the same shape
-- because the file is where the tweaking happens next and `18:9` is
one digit away from `18:10` in a way it is not away from `9:5`.

What the terminal reports is the cell it hands the font, which is
not always the shape the letters look.  Take what `auto` writes as
where to start, draw a region with `map --region`, and change the
ratio until a square region looks square -- that is the only test
that matters.

`auto` needs a terminal to ask.  It is refused where there is none
-- `slsh -c`, `slsh -f`, and a session with its input coming from a
pipe -- and refused again, naming what it tried, by a terminal that
will not answer.  Both of those refusals mean measuring it by hand,
which is the same question sent the same way:

    printf '\033[16t'

The answer comes back as ESC [ 6 ; HEIGHT ; WIDTH t -- so `6;18;10`
is a cell eighteen pixels tall and ten wide, and `18:10` is the
ratio to try.  A terminal that answers nothing at all to that is one
that does not know the question, and then it is by eye: start from
`2:1`, draw a region, and go from there.

`map_ratio` is the only setting that takes `auto`, and every other
one takes its value as it stands.  A word that might mean "work it
out" in any row would be a word to check the meaning of in every
row.

## Examples

    set
    set map_span
    set map_ratio 2:1
    set map_ratio auto
    set map_friend_colour bright cyan

The last of these cannot take until the next slsh, and says so:

    set addr lab.local:7807
    addr = lab.local:7807
    written to /home/somebody/.config/slsh/config
    this shell keeps the old value; the new one is for the next slsh

See also: `map` for what the map settings do to the picture,
`viewer` for what the viewer settings start, and `chat` for the key
the escape setting names.
