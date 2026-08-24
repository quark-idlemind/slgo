set is the settings, from inside the shell.  Everything slsh can be
told -- which daemon to attach to, which key leaves chat mode, how a map
is drawn -- lived in a file that had to be found and edited with slsh
closed.  This is the same settings said out loud at the prompt, and
remembered.

There are three forms, told apart by how many words follow:

    set                     every setting, its value, and what it is for
    set NAME                one of them
    set NAME VALUE          change it, and write it to the settings file

The value is the rest of the line, joined with single spaces, so
nothing has to be quoted to be typed.  That matters for the settings
whose value is a command line of its own:

    set viewer_launch open -a {app} --args --grid {grid}

The one exception is ">", which is a redirection wherever it is typed
unquoted, with or without a space in front of it: "set viewer_grid a>b"
sets the setting to "a" and puts set's own report into a new file called
b.  Quote a value with one in it -- set viewer_grid "a>b".

## It writes the file, and does nothing else to it

A setting that lasted until the shell was closed would be a setting to
type again after every reboot, which is the whole of what this exists
not to be.  So every change is written to the settings file -- config in
the directory $SLSH_CONFIG_DIR names, otherwise slsh/config under
$XDG_CONFIG_HOME, otherwise ~/.config/slsh/config -- and the file is
created, with its directory, if it is not there yet.  The path is
printed every time, since the file is the thing that will still be true
tomorrow.

What changes in that file is the one line for the one setting, and
nothing else.  Everything up to and including the "=" is left exactly
as it stands, so the indentation, the column the equals signs are lined
up in, and whichever spelling of the name is already there all survive;
what follows the "=" is replaced by one space and the new value.  A
file whose values were padded out to a column of their own loses that
padding on the one line that changed and keeps it everywhere else.
Every other line survives untouched, comments included.
A settings file is written by hand and the comments in it are somebody's
notes -- which grid nickname the viewer wants, which address was tried
and did not answer -- and a command that rewrote the file from what it
had in memory would throw all of that away the first time it was used.
A setting the file does not mention is appended, one line, at the end.

A comment has to be on a line of its own, and that is worth knowing
before writing one, because nothing warns about it until the next
start.  A "#" after a value is part of the value rather than a note
about it: a line reading "map_ratio = 7:3  # measured here" is a
map_ratio of "7:3  # measured here", which is not a ratio, and since a
value the setting will not take is refused when the file is read, it is
a shell that will not come up until the line is edited.  Nothing here
writes a line like that; it is one to have typed in by hand.

If the file names a setting twice, it is the last of the two that is in
force, since the file is read from the top and each line overwrites what
came before; that is the line "set" changes, for the same reason.

## Some of it cannot take until slsh is started again

Three settings are used before there is a prompt to type "set" at: the
address, because the session was attached through it; the profile,
because it says which avatar this is; and the escape key, because the
terminal was already in raw mode reading for it.  A flag may have
overridden the file for this run as well, so applying one of these now
would mean two different things depending on how slsh was started.

They are written to the file all the same, and both the listing and the
answer to a change say so outright.  Nothing here pretends a change took
effect that did not: the listing ends their line with "at startup only",
and changing one prints the sentence about the next slsh under it.  The
three are addr, agent and escape, and they are the only three.

Everything else takes at once.  "set map_ratio 2:1" and then "map"
draws the new shape.

## Where the settings come from and what beats what

A flag on the slsh command line beats the file, and the file beats the
defaults.  So a shell started with "--escape ^G" is running with ^G
whatever the file says, which is the other half of why the three above
are left for the next run.

The names are the names in the file, and the file has always taken
another spelling of some of them -- "server" for addr, "profile" for
agent, "prefix" or "prefix_key" for escape.  Those still load, and set
answers to them as well: "set server" prints the setting under the name
addr, which is the answer to what to type as well as to what it is.

A value a setting will not take is refused, with the setting named, and
neither the shell nor the file is touched: "set map_ratio sideways"
leaves the picture exactly as it was.  The same refusal happens when the
file is read at startup, where it stops slsh coming up rather than
leaving somebody with a setting that silently does nothing.

## What there is

"set" is the listing, and it is the one place they are all written down,
so this page does not copy it.  They fall into three:

    addr, agent, escape             slsh itself
    viewer_*                        what "viewer --launch" starts
    map_*                           how "map" draws its picture

The map's are the ones most worth knowing about, because the picture is
drawn to the shape of the font it is being read in: map_ratio is how
tall a character cell is against how wide, height first, and 7:3 is a
measurement of one font rather than a fact about all of them.  See "man
map", which says what each of the map's five does.

## Measuring your own font

Some terminals will say, and slsh will ask them:

    set map_ratio auto

That sends \033[16t, which asks for the size of one character cell in
pixels, and writes back whatever comes: a terminal that answers ESC [ 6
; 18 ; 10 t leaves "map_ratio = 18:10" in the settings file, exactly as
though it had been typed there.

The numbers written are the ones reported, not reduced -- 18:10 rather
than the 9:5 that means the same shape -- because the file is where the
tweaking happens next and 18:9 is one digit away from 18:10 in a way it
is not away from 9:5.

Try rather than trust.  What the terminal reports is the cell it hands
the font, which is not always the shape the letters look: on the machine
this was written on the terminal said 18:10 and 18:9 drew the squarer
picture.  So take what "auto" writes as where to start, draw a region
with "map --region", and change the ratio until a square region looks
square -- that is the only test that matters, and it takes one line and
one look.

Whether a terminal answers at all is not something to expect either
way, and it does not follow from how good the terminal is.  Measured:
macOS Terminal.app does not answer, and a small hand-written ssh client
for a tablet does.  So the refusal below is an ordinary outcome rather
than a sign of anything being wrong, and a terminal that will not say is
one where the ratio is found by eye in about a minute.

"auto" needs a terminal to ask.  It is refused where there is none --
"slsh -c", "slsh -f", and a session with its input coming from a pipe --
and refused again, naming what it tried, by a terminal that will not
answer.  Both of those refusals mean measuring it by hand, which is the
same question sent the same way:

    printf '\033[16t'

The answer comes back as ESC [ 6 ; HEIGHT ; WIDTH t -- so "6;18;10" is
a cell eighteen pixels tall and ten wide, and "18:10" is the ratio to
try.  A terminal that answers nothing at all to that is one that does
not know the question, and then it is by eye: start from 2:1, draw a
region, and go from there.

map_ratio is the only setting that takes "auto", and every other one
takes its value as it stands.  A word that might mean "work it out" in
any row would be a word to check the meaning of in every row.

## Examples

Everything, with what it is for:

    set

One of them, when the name is remembered and the value is not:

    set map_span

A picture that is the wrong shape for this terminal's font, measured
and put right, and it takes at once:

    set map_ratio 2:1
    map

The same thing with the terminal asked rather than the eye, which
writes the number it reports and says to try it:

    set map_ratio auto
    map --region

A friend picked out in something other than green:

    set map_friend_colour bright cyan

And one that has to wait, which says so:

    set addr lab.local:7807
    addr = lab.local:7807
    written to /home/somebody/.config/slsh/config
    this shell keeps the old value; the new one is for the next slsh

See also: map for what the map settings do to the picture, viewer for
what the viewer settings start, and chat for the key the escape setting
names.
