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
    /Objects$ ls -r Textures        descend into subfolders
    /Objects$ find lantern          names containing "lantern", from here down
    /Objects$ cat "my notecard"     print a notecard or a script
    /Objects$ mkdir sorted
    /Objects$ mv "old lamp" sorted  move into a folder
    /Objects$ mv "old lamp" "lamp"  a plain name renames instead
    /Objects$ rm "old lamp"         permanent; there is no undo

`cd` with no path goes to the root, and `..` goes up.

Inventory names may contain almost any printable character, so paths
escape: `/` separates, `\/` is a slash inside a name, and `\\` is a
backslash. Quote with `"` or `'` as well.

Names are **not** unique -- one folder can hold a dozen things with the
same name -- so `mv`, `rm` and `cat` accept an item's id anywhere they
accept a path. That is what makes a listing of duplicates usable.

### The world around you

    who         who else is in the region, nearest first
    where       the region and position you are at
    look        what the simulator says about the region
    objects     objects the region has described, by name
    caps        the capabilities this session was granted
    features    what the simulator says it supports
    lsl         the LSL this simulator implements

`objects`, `caps`, `features` and `lsl` all take optional text to filter
by, e.g. `lsl llGetUsed`.

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

A script needs an object to run in. By default a prim is rezzed beside
your avatar, used, and put in the trash afterwards. Use `--keep` to
leave it, or `--object NAME` to run in an object already in the region.

Naming an object you use again and again is worth doing for anything
repetitive. The first script `automate` puts in an object takes several
seconds to appear; replacing one that is already there takes well under
a second, and a named object keeps that saving across runs.

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
| `--object NAME` | run in this object instead of rezzing one |
| `--keep` | leave the rezzed object behind |
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
dozen runs, and it is a property of the shape of the script rather than
of the code being measured -- so when you benchmark the same shape
again, hand it back and skip the search:

    autobench -1 --ipad 377 --code "integer gCNT;"

Add `--check-ipad` to spend two runs confirming it first. `--fast` skips
the padding search altogether, which is quicker and less exact.

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
| `--fast` | skip the padding search |
| `--max N` | most copies to use (default 512) |
| `--object NAME`, `--keep` | as for `automate` |
| `--timeout DUR` | how long one script may take |
| `-v` | print each script before running it |
| `--probe` | check the connection and exit |

### Reading the numbers honestly

`Size: 22 ±0` and `Size: 24` are not two answers to the same question.
Copy mode measures what an **additional** copy costs; padding mode
measures a single one, including whatever fixed cost comes with it. When
comparing two constructs, compare them in the same mode.

Second Life's own numbers move about a little from run to run. If an
answer matters, take it twice.
