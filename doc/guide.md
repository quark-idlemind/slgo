# A guide to slsh, automate and autobench

Three command-line programs for working with Second Life.

| | |
|---|---|
| `slsh` | an interactive shell: your inventory, the region around you, and chat |
| `automate` | runs LSL scripts and prints what they say |
| `autobench` | measures how much script memory an LSL construct costs |

They all reach Second Life the same way and share the connection
options described in the next section. If you only want to look around
and move things about, you want `slsh`.

---

## Connecting

There are two ways to be connected, and every one of these programs
supports both.

**Through `slgod`** (the default). `slgod` is a daemon that holds the
connection to the grid. Programs attach to it, so they start instantly
and leave the avatar logged in when they exit. Several programs can use
one session at the same time.

**Directly**, with `--direct` (`-d`). The program logs in itself and
holds the session for as long as it runs. Quitting logs the avatar out.
This needs no daemon, but every start pays for a fresh login.

### Saying where slgod is

    slsh --addr HOST:PORT

If you do not say, the address is worked out for you:

1. `addr = ...` in the configuration file, for `slsh`;
2. whatever the `sl-host` command prints, if you have one installed --
   it names the host, and the standard port is added to it;
3. this machine, if you have no `sl-host`.

That last case is the normal one when you run your own `slgod`, so
usually you need not pass anything at all.

### Credentials, for `--direct`

Accounts live one file per account in `~/.config/slgo`, named however
you like. The directory must be mode 700 and each file mode 600 --
these programs refuse to read credentials anyone else can:

    first    = Example
    last     = Resident
    password = secret
    start    = last

`start` is where to arrive: `last`, `home`, or a region name. Name a
profile with `--agent NAME` (`-a`); with exactly one profile on disk, it
is used without being named.

Anything missing is asked for at the terminal, and the password is asked
for without echo. So a profile is optional:

    slsh --direct --first Example --last Resident

Storing the password as its `$1$...` digest works and is preferred --
that is the only form that ever leaves your machine.

---

## slsh

    slsh                            attach and start at a prompt
    slsh --direct                   log in first
    slsh -c "ls -l Objects"         run one command and exit
    slsh -f commands.slsh           run a file of commands and exit

### Two modes

`slsh` has a **command mode** and a **chat mode**, and the prompt says
which one you are in.

Command mode is the outer one, because that is what a keyboard is mostly
for. `chat` goes inward; the escape key comes back out. Each mode
remembers the line you had half-typed in it, so switching does not lose
what you were saying.

    /Objects$ ls
    /Objects$ chat
    Local> hello everyone
    (press ESC)
    /Objects$

Anything said to you is printed as it arrives, above whatever you are
typing, in **either** mode. You never have to leave what you are doing
to see chat, and chat never eats your half-typed command.

Start in chat mode with `--chat`. Change the key that leaves it with
`--escape KEY` -- `ESC`, `^G`, `TAB`, a plain character, or a number.

### Inventory is a filesystem

`cd`, `ls`, `pwd`, `cat`, `mkdir`, `mv`, `rm`, `find` all work the way
you expect.

    /$ cd Objects
    /Objects$ ls
    /Objects$ ls -l                 kind, date and id as well
    /Objects$ ls -lT                the time of day too
    /Objects$ ls -lt                newest first
    /Objects$ ls -r Textures        descend into subfolders
    /Objects$ find lantern          names containing "lantern", from here down
    /Objects$ cat "my notecard"     print a notecard or a script
    /Objects$ mkdir sorted
    /Objects$ mv "old lamp" sorted  move into a folder
    /Objects$ mv "old lamp" "lamp"  a plain name renames instead
    /Objects$ rm "old lamp"         permanent; there is no undo
    /Objects$ rm --remove-all-copies "old lamp"
    /Objects$ rm --remove-all-copies *      every item here
    /Objects$ emptytrash

`cd` with no path goes to the root, and `..` goes up.

Inventory names may contain almost any printable character, so paths
escape: `/` separates, `\/` is a slash inside a name, and `\\` is a
backslash. Quote with `"` or `'` as well.

Names are **not** unique -- one folder can hold a dozen things with the
same name -- so `mv`, `rm` and `cat` accept an item's id anywhere they
accept a path. That is what makes a listing of duplicates usable.

A listing is in tree order: siblings by name, and things of the same
name newest first, with every folder followed by what is inside it.
`-t` sorts by time instead -- newest first across the whole listing,
and by name for things made in the same second. A folder has no date
and sorts last under `-t`.

`rm` takes the first thing of the name it is given, which is the
careful default when a name may mean a dozen items. `--remove-all-copies`
takes all of them and says how many it took:

    /Scripts$ rm --remove-all-copies /Scripts/slrun-bench
    removed 431 of 645 copies          <- counting up, in place
    /Scripts/slrun-bench: removed 645

Each removal is a round trip, so six hundred of them take minutes. The
count says it is working and roughly how much longer; it is written to
the terminal and never to a file, so a redirect catches the result and
not the flicker.

`*` means every **item** in the folder, never its subfolders. It is not
a glob -- it is the one pattern `rm` needs -- and it is refused without
`--remove-all-copies`, so that the sweeping form has to be asked for:

    /Scripts$ rm *
    *: means every item in this folder: say rm --remove-all-copies * if that is what you want
    /Scripts$ rm --remove-all-copies *
    *: removed 645 items

An item may itself be named `*`, since the grid allows nearly any
character. Such an item can no longer be named at the prompt, but its
id still names it -- which is what `ls -l` prints ids for.

While a command runs there is no prompt. A prompt means the shell is
ready for the next line, and during a long command it is not -- what
you type then waits its turn and arrives when the command is done.

`emptytrash` throws away everything in the trash, permanently, and says
how much it threw:

    /$ emptytrash
    emptied the trash: 325

The trash is found by what it is rather than what it is called, so it
works on an account whose viewer named it something else.

`ls -l` is four columns: kind, date, id, path. `-T` gives the time as
well, joined to the date rather than put in a column of its own:

    notecard   2026-08-02T15:09:30 3ae17e57-...-cbd3405daadf /Notecards/README

so it is still four columns and anything reading the id out of the
third field works either way. A folder has no date and shows a dash.

Two listings of a folder that has not changed are identical, which is
what makes them worth diffing and worth editing into commands.

`quit` leaves, and so does Ctrl-D on an empty line; through slgod that
leaves the avatar logged in, and with `--direct` it does not.

`help` lists the groups the commands are in, since there are enough of
them now that one list is a wall rather than an answer:

    /$ help
    Commands are grouped.  For a group:  help GROUP
    For everything:        help all
    For one command:       COMMAND --help

      inventory   folders and items: what you have, and moving it about  (10)
      people      finding people, talking to them, friendship            (11)
      region      where this avatar is and what is around it             (6)
      giving      handing items to somebody, and taking what is offered  (5)
      avatars     which avatar you are driving, and slgod's sessions     (4)
      simulator   what this grid supports                                (3)
      shell       slsh itself                                            (4)

`help GROUP` lists that group, and `help all` is every command at once.
A command can be in more than one group where it belongs in both: `cp`
is an inventory operation and also how an avatar that may not rez gets
more objects, so it is under `inventory` and `giving` alike.

What one command takes comes from the command, not from `help`:

    /$ ls --help
    Usage: ls [-hlrTt] [PATH]
     -h, --help  show what this command takes
     -l          the columns: kind, date, id and path
     -r          descend into the folders below
     -T          the time of day as well as the date
     -t          newest first, rather than by name

Every command answers `--help`, with one deliberate exception: `echo`
prints its arguments, so `echo --help` prints `--help`. It exists to put
a line into a file and has to be able to put that one there too.

### The world around you

    who         who else is in the region, nearest first
    worn        what this avatar is wearing, and where
    where       the region and position you are at
    look        what the simulator says about the region
    objects     objects the region has described, by name
    caps        the capabilities this session was granted
    features    what the simulator says it supports
    lsl         the LSL this simulator implements

`objects`, `caps`, `features` and `lsl` all take optional text to filter
by, e.g. `lsl llGetUsed`.

`tp X Y Z` moves within the region.  Only within: another region is a
different simulator, needing a new circuit and a new set of
capabilities, which slgod does not yet do -- so arriving somewhere else
is still a matter of logging in there.

#### What LSL this simulator implements

The simulator publishes its own syntax, which makes it the authority on
what a script may say -- better than documentation, which drifts.

    lsl               how many of each there are
    lsl -f            the functions
    lsl -c            the constants
    lsl -e            the events
    lsl -t            the types
    lsl -a            all of it
    lsl -m ...        one record a line, tab separated, for a program

The `-m` form is for a compiler that targets this grid and needs to know
two things it cannot work out for itself: which functions it knows about
will fail if called here, and which exist here that it has never heard
of.  Both are set differences against this list.

    $ slsh -c "lsl -m -f"
    function        llAbs   integer 10      0               integer
    function        llSay   void    10      0.1             integer,string

The first field always names the kind, so `lsl -m -a` reads with the
same loop as `lsl -m -f`, and a reader that meets a kind it does not
know can skip the line rather than misparse it.  The layouts:

    function  NAME  RETURN  ENERGY  SLEEP  FLAGS  ARGTYPE,...
    constant  NAME  TYPE    VALUE
    event     NAME  ARGTYPE,...
    type      NAME

RETURN is `void` when nothing is returned.  FLAGS may be empty and today
holds `deprecated` and `godmode`.  Argument lists are types only, comma
separated, and may be empty.  Fields are never reordered or removed;
anything new goes on the end, so a reader that takes the first few
fields keeps working.

`worn` lists the attachments by where they are worn, and `-l` adds the
ids:

    /$ worn
    HUD bottom left    auto
    HUD centre 1       Worn

The names are the inventory items', not the objects'. A worn object
will not answer a request for its own name, and the item is the better
answer anyway: the object is rezzed afresh with a new key every time it
goes on and every time you log in, while the item does not change.
Something worn whose item cannot be found is listed by that item's id,
which is the only handle it has.

### Giving things away, and taking them

    give SOMEONE PATH       offer them an inventory item
    offers                  what is waiting for an answer, of either kind
    accept [NAME]           take an offer up
    decline [NAME]          refuse it
    cp PATH NAME            copy an item under a new name

An offer is not a transfer: nothing moves until it is accepted, and
nothing here can tell whether it was.  `accept` puts what arrives in the
folder you are in, so `cd Objects` first if that is where it belongs.

An offer only reaches a client that is attached when it arrives -- slgod
relays it and does not keep it -- so a shell started afterwards will not
see it.

`cp` matters more than it looks.  An avatar that may not rez cannot make
an object at all, but it can copy one it already owns, because copying
asks the land nothing.  So an avatar with no build rights needs exactly
one object given to it and can make the rest itself.

### The daemon and its avatars

    agents                  what slgod holds, and what it could hold
    host NAME               bring an avatar up
    logout NAME             put one down, and keep it down
    status                  how this session and its circuit are doing
    watch [-for D] [NAME]   print grid messages as they arrive
    auto                    how many benchmark objects are worn
    auto -n 12              set that many up

`agents` lists the running sessions first, then the profiles that exist
but are not running:

    * example     Example Resident      Testville
      qi          Quark Idlemind        Testville
      helper      Helper Resident       stopped: logged out
      builder                           configured

`status` is the one to reach for when a session looks idle and you want
to know whether it is idle or broken -- packet counters, retransmissions,
and the messages arriving that this build has no handler for, which is
how a protocol change announces itself.

`watch` prints what is actually crossing the wire, by message name or
`*` for everything.  It opens a connection of its own, so watching
everything for an hour does not disturb the shell you are typing in:

    watch --for 1h ChatFromSimulator > chat.log

The state matters because each one means a different thing to do about
it.  `configured` can be started; `stopped` was put down deliberately
and is left alone unless asked for by name; `failed` will waste a login
attempt if asked again too soon, and says how long it is waiting.

#### Using an avatar in a viewer

    logout quark            slgod lets go of it
    ... use it in Firestorm ...
    host quark              take it back

`logout` is remembered.  slgod will not bring that avatar back on its
own, so nothing is fighting you for it while you use it elsewhere, and
`host` afterwards has to name it.

You do not strictly have to say `logout` first.  Logging in from a
viewer makes the grid end slgod's session, and slgod treats being thrown
off as a decision rather than a fault: it says so and stays down.
`logout` is the tidier way round, and the only one that lets slgod log
out cleanly.

Both refuse to be casual about it.  `logout` will not take a session
that clients are attached to -- it names them instead -- because a
benchmark mid-run has a script installed and a reading half taken.
`host` will not restart something that was stopped deliberately.  `-f`
overrules either, once you know what you are overruling.

`agents` lists in the daemon's own order, not alphabetically: the first
is what a command that names no avatar gets, and it is also the order a
benchmark looks in for free objects.

#### Which avatar a command uses

    --agent NAME     this command, and nothing else
    SLGO_AGENT       every command in this shell
    agent = NAME     in the config file, for this machine
    (nothing)        the daemon's default: the session it has held longest

In that order of strength.  The daemon's default changes only when that
session itself goes away -- adding an avatar never moves it, and neither
does a reconnect -- so a script that worked yesterday drives the same
avatar today.

Whenever a command does not name one, it says which it used.  Worth
reading: the default depends on the daemon's history and there is
nothing on disk that records it.

### Talking

    say hello               one line to local chat, without leaving command mode
    chat                    enter chat mode, on local chat
    chat SOMEONE            enter chat mode in a conversation with them
    im SOMEONE hello        send one message without entering chat mode
    talk                    list the conversations chat mode cycles between
    lookup ander            find people whose name contains "ander"
    friends                 friends who are online (-a for all of them)
    offer SOMEONE           offer friendship
    offers                  offers waiting for your answer
    accept / decline        answer one

In chat mode, TAB cycles between conversations, so local chat and your
instant messages are the same interface.

### Keys

| | |
|---|---|
| TAB | complete a command or a path (command mode); next conversation (chat mode) |
| up / down | walk through what you have typed |
| Ctrl-C | clear the line |
| escape | leave chat mode |

### Redirection and command files

Any command's output can be redirected, and a file of commands can be
run with `.` -- which together are the point of having a shell at all:

    /$ ls -l /Objects > listing
    /$ echo done >> listing

Edit the listing into a list of commands with whatever you normally use,
then run it:

    $ awk '{print "mv " $3 " /Objects/sorted"}' listing > moves
    $ slsh -f moves

or from inside `slsh`:

    /$ . moves

A file stops at the first command that fails, naming the line it gave
up on and how much it did not run:

    $ slsh -f moves
    slsh: cd: no folder "sorted"
    slsh: moves:1: cd sorted
    slsh: stopped; 43 lines were not run

That matters because such a file usually begins by changing folder, and
carrying on after that failed would run every line that follows
somewhere else. A one-shot run exits non-zero when anything failed, so
a script wrapping slsh can tell without reading the output.

`echo` prints its arguments, which is how you write a note into a file
you are building.

### The configuration file

`~/.config/slsh/config`, as `key = value` lines, with `#` for comments:

    addr   = HOST:PORT
    agent  = NAME
    escape = ESC

A command-line flag always beats the file. A misspelled key is an error
rather than a setting that silently does nothing.

---

## automate

Runs LSL scripts in Second Life and prints what they say.

    automate script.lsl
    automate one.lsl two.lsl three.lsl
    automate --object "Test HUD" script.lsl

Each file is compiled by Second Life and run, and each line the script
says is printed as it arrives, prefixed with the file it came from:

    $ automate hello.lsl
    hello.lsl: hello from automate
    hello.lsl: two plus two is 4

### Say DONE when you are finished

A script must say `DONE` when it has finished:

    default {
        state_entry() {
            llSay(0, "hello from automate");
            llSay(0, "DONE");
        }
    }

Without it there is nothing to wait for but the clock, and every run
costs the whole timeout. Use `--done TEXT` for a different word, or
`--done ""` to deliberately wait out the timeout.

The `DONE` line itself is not printed -- it is the script talking to
`automate`, not to you.

### Where the script runs

A script needs an object to run in, and by default that is `auto`: one
prim, worn, kept. If it is not being worn it is put on; if it does not
exist it is made, taken into inventory and put on. That happens once.

Keeping it is what makes runs quick. The first script put into an
object takes several seconds to appear; replacing one already there
takes well under a second. Measured here: about **1 to 2 seconds** a
run, against about 10 seconds when every run rezzed its own prim.

That holds across logins. A worn object is rezzed afresh, with a new
key, every time it is put on and every time the avatar logs in, so its
key is worth nothing between sessions -- but the inventory item it was
worn from does not change, and slgod remembers which worn object came
from which item. Nothing is written down on your machine, and there is
no cache to go stale.

    --object NAME    run in some other object already in the region
    --rez            rez a throwaway prim for this run, as before
    --keep           leave a rezzed prim behind

### Several runs at once

A run takes a **group** of four objects and holds it until it
finishes. An avatar wears twelve, so three runs fit on one avatar, and
if slgod is holding several avatars a run that finds them all busy
moves on to the next avatar rather than queueing.

That order is the daemon's, not alphabetical: the default avatar first,
then the rest. So nine runs started together against three avatars
simply spread out, three apiece. Only when every group on every avatar
is busy does anything wait.

    $ autobench --statement "i += 1;" --locals i     # no avatar named
    running as example, objects 0-3

Whenever you do not name an avatar, the run says which one it used.
Worth reading: with several hosted, the choice is the daemon's, and a
benchmark attributed to the wrong avatar is not an error -- it is a
plausible number.

Why a whole group rather than one object at a time: a benchmark carries
its base reading in the object's linkset data, which belongs to the
object rather than to the script, and the script is installed under a
fixed name -- so two runs sharing an object would overwrite each
other's reading and each other's script. Taking objects one at a time
would let two runs each hold some and wait for the rest, which is a
deadlock; taking a whole group cannot deadlock, because nothing ever
holds one group while waiting for another.

Set an avatar up with `auto -n 12`, once per account. An avatar
that is not allowed to build can still be set up, provided somebody who
can gives it one object: everything after the first is a copy, and
copying something already owned asks the land nothing.

The lock is held by slgod for as long as the program holds its
connection, so a run that crashes or is killed gives it back at once;
there is nothing to clean up and no stale lock to break. Use `--rez` to
take a prim of your own and not queue at all.

### What can go wrong

Each is reported and each makes the run fail:

    script.lsl: (3, 4) : ERROR : Syntax error          would not compile
    script.lsl: automate: Math Error                   crashed while running
    script.lsl: it did not say DONE within 1m0s        never finished

`automate` exits non-zero if **any** script failed, so it can be used
from a Makefile or a test script. It exits 0 only if every script
compiled, ran and finished.

### Options

| | |
|---|---|
| `--object NAME` | run in this object instead of the shared `auto` one |
| `--rez` | rez a throwaway prim for this run, and do not queue |
| `--keep` | leave a rezzed prim behind |
| `--done TEXT` | the text that means "finished" (default `DONE`) |
| `--timeout DUR` | how long to wait for it (default `1m`) |
| `--script NAME` | what to call the script inside the object |

---

## autobench

Measures how many bytes of script memory an LSL construct costs.

    autobench --title "global integer" --code "integer gCNT;"
    autobench -1 --title "global integer" --code "integer gCNT;"

A script has a fixed memory budget, and Second Life only reports memory
in 512-byte blocks -- so you cannot simply ask what one variable costs.
`autobench` has two ways around that.

**Copy mode** (the default) puts many copies of the code in one script
and divides the difference by the number of copies. It is quick, and
the answer carries the uncertainty it earned:

    $ autobench --title "global integer" --code "integer gCNT;"
    Title: global integer
    Padding: 377
    Size: 22 ±0

**Padding mode** (`-1`) uses a single copy and adds filler a byte at a
time until memory steps to the next block, which locates the boundary
exactly. It costs more runs and gives an exact answer:

    $ autobench -1 --title "global integer" --code "integer gCNT;"
    Title: global integer
    Base mem: 5932
    Result mem: 6444
    Result pad: 488
    Size: 24
    Padding: 377

`Size` is the answer. `Padding` is described below. The other lines are
the readings behind the answer.

### Describing the code to measure

Give the code in one of three ways -- `--code`, `--statement`, or a
filename -- and never more than one.

`--code` is repeated whole:

    autobench --code "integer gCNT;"

`CNT` in it is replaced with the copy number, so each copy can have a
name of its own. That matters: without it, a hundred copies of
`integer g;` is not a hundred variables.

`--statement` is for code that must live inside a function, and wraps it
in one for you:

    autobench --statement "x = j + 1;" --params "x,j"

`--preamble` and `--postamble` add text before and after the copies, and
`--globals` declares globals.

This expands to:

```
_(integer x, integer j) {
x = j + 1;
x = j + 1;
x = j + 1;
...
}
```

### Naming variables

`--globals`, `--params` and `--locals` take comma-separated names --
**no spaces** -- whose **first letter gives the type**:

| letter | type | | letter | type |
|---|---|---|---|---|
| `a`, `l` | list | | `k` | key |
| `f`, `g` | float | | `q`, `r` | quaternion |
| `i`, `j`, `x` | integer | | `s` | string |
| | | | `v` | vector |

So `--globals "iCount,sName,vPos"` declares an integer, a string and a
vector. Any other first letter is refused, so a name has to be chosen to
say what it is.

### Reusing the padding

Both modes print a `Padding:` line. Finding that number costs about a
dozen runs, and it is a property of the base script -- the harness with
no copies of your code in it -- rather than of the code being measured.

You do not have to do anything about that: it is remembered, in
`~/.config/slgo/autobench-padding`, and looked up by what the base
script is. The code under test is not in the base script at all, so a
benchmark of new code reuses the answer, and so does a different
benchmark whose title is the same length -- a title is a string
literal, and what it costs is its length.

    cold, nothing remembered   33s
    the same benchmark again   20s
    a different benchmark      19s

An entry is confirmed rather than trusted. Using one runs the base
script at that padding and one byte past it and requires memory to grow
between them: two runs against the dozen a search costs, and an answer
that has stopped holding -- because Second Life's compiler changed
under it -- is thrown away and searched for again. So there is nothing
to invalidate by hand.

Those two readings are taken at the same time, in two objects. Nothing
a script says identifies the script that said it -- chat carries the
object and no more -- so two scripts in one object cannot be told
apart, but two objects can. `--objects N` sets how many to use, up to
the four in a group; the extra ones are worn and kept like the first.

When a padding does have to be searched for, three pads are tried at
once and the range is quartered rather than halved: four or five rounds
where a bisection takes nine. Three readings taken together cost about
what one costs, being three round trips in flight rather than three in
a row.

Both searches work that way -- the base script's and, in `-1` mode, the
one-copy script's. Median times, with one object against four:

    -1, padding remembered      19.3s -> 15.4s
    -1, searched from scratch   32.0s -> 25.7s
    copy mode, searched         34.2s -> 16.5s
    copy mode, remembered                 5.7s

A script reports its own memory before it reads anything, so the number
a search compares means the same in any object. What a script works out
*afterwards* does not: it divides against a base held in its object's
linkset data, which a spare object has none of. So only the memory
reading travels, and the measured sequence still runs where the base
is.

`--ipad N` still asserts a padding outright and skips all of this,
`--check-ipad` confirms it, and `--no-cache` neither reads nor writes
the file. Nothing measured with `--test` is ever remembered: the
offline model has no compiler, and its paddings are arithmetic rather
than measurements.

### Checking your connection

    $ autobench --probe
    SL Live

### Options

| | |
|---|---|
| `-1` | padding mode: one copy, exact answer |
| `--code CODE` | the code to measure, repeated |
| `--statement CODE` | statements to measure, wrapped in a function |
| `--globals`, `--params`, `--locals` | declare variables (see above) |
| `--preamble`, `--postamble` | text around the copies |
| `--title NAME` | name the benchmark, echoed in the output |
| `--ipad N` | a padding from an earlier run |
| `--check-ipad` | confirm `--ipad` before trusting it |
| `--no-cache` | do not remember or reuse the padding |
| `--objects N` | how many objects to run in at once (default 4: one to measure in, three to probe with) |
| `--fast` | skip the padding search |
| `--max N` | most copies to use (default 512) |
| `--object NAME`, `--rez`, `--keep` | as for `automate` |
| `--pad PAD` | extra filler in the base script |
| `--debug` | say what the search is doing |
| `--timeout DUR` | how long one script may take |
| `-v` | print each script before running it |
| `--probe` | check the connection and exit |

### Reading the numbers honestly

`Size: 22 ±0` and `Size: 24` are not two answers to the same question.
Copy mode measures what an **additional** copy costs; `-1` mode measures
one copy outright. Compare two constructs in the same mode.

The gap can be large, and when it is, it is telling you something.
Measured: `string sCNT = "<250 identical characters>";` is 1044 bytes in
`-1` mode and 542 in copy mode. Give each copy a *different* literal --
put `CNT` inside the string -- and copy mode says 1030, agreeing with
`-1`. Identical literals are shared, so an extra copy pays only for what
it cannot share. That is a real property of the construct rather than a
disagreement between the modes, and it is only visible because the two
measure different things.

`-1` mode handles constructs of any size, including those larger than a
512-byte block: it counts the whole blocks a copy occupies as well as
the distance to the next boundary.

Second Life's own numbers move about a little from run to run. If an
answer matters, take it twice.
