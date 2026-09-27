# slsh, and why it does what it does

The comments in `cmd/slsh/` say what the shell does. This page is why,
where the why is a story: what was measured, what was tried first, and
what was wrong with it.

What a command does, for somebody typing it, is its man page, in
`cmd/slsh/man/`; the guide to the shell is `handbook/slsh-guide.html`.

## One usage line for every command

`usageLine`, in `cmd/slsh/options.go`, is the one thing in slsh that
writes the line saying how a command is typed. That line used to be
written down three times: a string in the command table, the same words
again in the command's own "usage:" refusal, and a third wording from
getopt in `--help`. Three copies kept by hand, and they had already
drifted -- perms named three of its four permission flags in the table,
touch left its trailing points out of `--help`, and put's three forms
appeared in one place only. A person who read one of them and typed
what it said was sometimes wrong.

So there is one composer, `usageLine`, and everything goes through it:
the name, whatever getopt's `Set.UsageLine` makes of the option struct,
and the parameters. A flag added to a struct appears in the help
listing, in the refusal and in `--help` with no other edit, because none
of those three has any words of its own to change.

### Why the help flag is left out of the usage line

One thing is dropped on the way through, and only one: the help flag.
Every command has it, so it tells nobody anything about the command
they are looking at, and it is not free -- getopt bundles the short
flags, so a listing of the waiting group read "waiting [-ah]",
"no [-h] N", "ignore [-h] N" down the page and pushed answer's line onto
a second row to make room for a flag all four of them share. The foot
of every listing already says "COMMAND --help", and `--help` itself
still lists -h underneath the line, which is where a person looks for
it; a usage line that leaves it out while the options under it name it
is the ordinary shape of a Unix tool rather than a disagreement.

It comes out inside the composer, in `withoutHelp`, and nowhere else.
Doing it at the call sites would give the three places three chances to
disagree again, which is the whole of what this arrangement is for.

## Why a typed answer ends with Ctrl-D

A multi-line answer to a text box, typed after `answer N` with nothing
after the number, is collected the way ed collects one and ended the
way mail does: Ctrl-D, which cannot appear in text and so needs no
escape.

It was a full stop on a line of its own first, with ed's rule for
escaping one -- a line of nothing but full stops losing one, so ".."
said "." -- and that went as soon as Ctrl-D was bound, because the
reason to keep it did not survive being looked at.  The argument was
that a file of commands cannot send Ctrl-D; but a file of commands
cannot type an answer either, since slsh -f hands every line to the
command parser and the line after "answer 1" would be run as a
command.  Scripted answers have --file.  So the escape rule was
paying for a case that does not exist, and every line now means
itself.

## Reading an escape sequence to its end

`Term.decode`, which turns the terminal's bytes into keys, reads the
whole of an escape sequence, however little of it is understood.
Reading to the end matters as much for the sequences nothing in slsh
answers to as for the ones it does.  The decoder used to stop at the
third byte and give up on anything it did not recognise, which left
the REST of the sequence in the stream to be decoded as ordinary keys:
a report of the cell size, ESC [ 6 ; 18 ; 10 t, typed ";18;10t" at the
prompt, and a bracketed paste typed "00~".

## Why slsh writes the transcript

The transcript -- what was heard, what was said, and every command
that was run -- is written by slsh rather than by slgod because slsh
is where the three of them exist at once.  The daemon relays messages
as undecoded bytes and says so in as many words -- "it does not decode
message bodies, hold an inventory, understand chat, or know what a
script is" (server/server.go) -- and it never sees a command line at
all, since parsing one and running it is the whole of what slsh does.
What the daemon could log is packets; what a person wants is what they
saw.

The price is stated so nobody has to discover it: an avatar nobody is
attached to writes nothing.  slgod stays logged in and keeps hearing,
and none of that reaches a file until a shell is there to hear it.

## Settings from inside the shell

`set`, in `cmd/slsh/set.go`, lists slsh's settings, shows one, and
changes one. Everything slsh can be told used to live in one
hand-edited file and nowhere else: to find out what could be set you
read the head of config.go, and to change one you left the shell,
opened an editor, and started again. A person who has just been drawn a
map that is the wrong shape for their font should be able to say so
where they are.

### Why the command is called set

Because that is the word a shell user reaches for, and because bare
"set" listing everything is exactly what a shell's set does. The
obvious objection is that a shell's set is about variables and this one
is not, and it comes to nothing here: slsh has no variables for it to be
confused with, and if it ever does, they will be the thing somebody
types "set" expecting to see.

### Why a change is written to the file

`set` writes the file, every time, because a setting that lasted until
the shell was closed would be a setting somebody had to type again
after every crash and every reboot -- and the whole complaint was about
having to say the same thing twice. `saveSetting`, in
`cmd/slsh/config.go`, says what it does to the rest of the file, which
is nothing.

### One table of settings

`settings`, in `cmd/slsh/config.go`, has one row per setting, and the
reader, `set` and the file writer all work from it. Every setting used
to be in three places at once -- a field of Config, a case in the
reader's switch, and a line of DefaultConfig -- and adding one meant
remembering all three. A name misspelled in the switch was caught
nowhere: the file would refuse it as unknown, which reads as the person
having typed it wrongly.

### Auto means something for one setting

`cmdSet` takes "auto" for `map_ratio` and refuses it, in the command,
for every other setting, rather than leaving it to a row of the table.
A table where any row might turn out to have a magic word in it is a
table nobody can predict: the only way to find out whether
"set addr auto" measured something or set the address to the word
"auto" would be to type it and look afterwards. So there is exactly one
row the word means anything for, it is named in the command, and
everywhere else it is an error with that name in it. The price is a
`viewer_grid` that cannot be called "auto", which is a nickname nobody
has, against a rule that can be stated in one line and holds for every
setting there will ever be.

### What auto measures

`set map_ratio auto` asks the terminal how big a character cell is
(`measureCellRatio`), and what it prints afterwards (`autoNote`) says
the number is a starting point, because it is one: what the terminal
reports is the cell it hands the font, which is not always the shape the
letters look. On the machine this was written on the terminal said
18:10 and 18:9 drew the squarer picture. So the last word is a square
region looked at, which takes one line and one look -- and somebody who
has just been given a number is exactly who is in a position to try it.

The numbers are the ones reported, and are not reduced. 18:10 and 9:5
are the same ratio and are not the same starting point. The reported
number is where somebody begins, not where they end, and the person who
measured this went from a reported 18:10 to 18:9 by changing one digit.
Reduced to 9:5 that same tweak is a sum to do first and a bigger step
to take, for nothing gained: the picture is drawn from the shape of the
ratio and not from the size of its numbers, so carrying the reported
ones costs nothing at all.

## The man pages

### What a man page repeats, and what it does not

`man` prints a command's name and brief as its heading and the usage
line under it, both composed the same way help composes them, so a
page never writes either out again: a page that did would be another
copy of the thing the series of changes `man` came in with had just
finished reducing to one.

The flags are not one of those two.  They were left out of the pages
at first, on the grounds that --help already had them, and what that
bought was a reference somebody had to leave in order to find out what
a flag did.  So a page with flags lists them under Options, a
paragraph each.

### Why a man page is a markdown file

A page lives in cmd/slsh/man, one file per command, embedded into the
binary.  A page is prose and is written and read as prose; as a Go
string constant it was prose being edited inside a quoting construct,
where a stray backtick is a compile error and no editor wraps or
spell-checks a paragraph.

It is NAME.md, and nothing else.  There was a second form, NAME.txt,
laid out by a reader in man.go: it had no mark available but capitals,
so every heading was shouted and a flag name in one came out as
"--REPLACE".  Markdown can send bold, so a heading keeps the case it
was written in, and once every page had been rewritten the text form
was 75 files saying the same thing in a worse hand.  A heading longer
than the width wraps like anything else in markdown, which the text
form never did -- it had no way to and left the line long.

The whole directory is embedded rather than a pattern like
"man/*.md", because a pattern is a thing a file can silently fall
outside of and a directory is not.  The page still ends up inside the
binary either way, which is the property worth keeping: a shell that
is installed cannot have lost its documentation on the way.

A generator writing the pages back out as Go source was the other
answer -- msg/messages_gen.go is done that way -- and buys nothing
here.  That generator exists because the message template is somebody
else's file in somebody else's format; these are ours, in no format at
all, and a generated file is one more thing to keep in step.

## The how command

`how` shows a small model a handful of commands the index found, and
checks what it says against their pages before any of it is printed.
These are the parts of that which were measured, or tried and dropped.

### No second opinion on a suggestion

`askRun` does not put a suggestion that passed the checks back to the
model.  A second, short question for each such suggestion -- "does
this command do what they asked? yes or no" -- was tried as a guard
against a real command offered for something it does not do.  It said
no to nearly everything: asked whether pwd, "print the current
inventory folder", tells somebody which folder they are in, qwen3.5:4b
said no, and when asked for a reason first, said that pwd is a Unix
command.  On the eval (2026-09-23, qwen3.5:4b and qwen3.5:2b) it cut
the questions answered right by half or more, so it is not there.

What guards against such a suggestion is the model's own `found`,
asked for first (`askSchema`), and honoured: suggestions written
beside `found=false` are dropped (`askToCheck`).

### Where the prompt's rules came from

Every rule in `askSystem`, the instructions, is one a check enforces
afterwards.  Three of them came from reading what the checks threw
away (the eval's per-question log, 2026-09-23, qwen3.5:4b and
qwen3.5:2b): placeholders for numbers ("--glow N"), which getopt
refuses, where the usage line had taught the model to write N; usage
lines copied whole, brackets and all; and quotes run on from one text
into the next.  Saying so, with the texts fenced (`askUser`), took the
4B from 73% right to 79% and the 2B from 52% to 72%.

`askUser` fences the brief as it does the excerpts, on lines of its
own, and labels an excerpt "manual" and its heading and no more, for
the quote.  With the brief on its "description:" line and each excerpt
under "from its manual, Options:", the smaller models quoted across
the joins -- "put on
everything the Current Outfit folder names that is not on from its
manual" -- which `checkQuote` rightly refuses, since no page says it.
A quote has to come from one fenced text, and the instructions now say
which texts those are.

The reply for "not found" is written out whole at the end of the
instructions.  A full pair of worked examples, one found and one not,
was measured too: it kept the 2B from suggesting anything for a
question nothing answers, but cost the 4B three points of right and
gave it wrong suggestions it had not made without them, and the 4B is
the better model.

### The order the reply is written in

`askSchema` gives the reply's properties in a fixed order
(`askObject`), because a model writes them in the order the grammar
makes it, and what it writes first it has decided by the time it
writes the rest.  `found` is first and is a bare boolean: the
decision, with nothing to say before it.  Two other orders were
measured and are not this one (2026-09-23, Ollama, qwen3.5:4b and
qwen3.5:2b with reasoning_effort none, the 82 questions of
cmd/slsh/testdata/ask-questions.tsv):

- The map's own order, which is alphabetical and put a free-text
  "answer" first.  The model wrote its prose before choosing, and on
  questions a shown command did answer it often wrote "slsh has no
  command for that" -- the sentence the instructions offered -- and
  then chose to agree with itself.
- `found` last, after the suggestions.  More answers came through, but
  so did suggestions for questions nothing answers (not-found fell
  from 100% to 93% on the 4B and to 80% on the 2B), even when `found`
  was false and the suggestions were thrown away for it.

Found first with the answer dropped kept not-found at 100% on both and
took the 4B's wrong kept suggestions from 7% of questions to 2%.

That is why `askAnswer` has no free-text answer in it.  There was one,
and nothing printed it; what it did was give the model a sentence to
write before it had chosen anything -- and the instructions supplied
the sentence, "slsh has no command for that", ready to copy.

### Find, asking to learn something

The questions `how` is evaluated by are written as what follows the
word how.  Of the 132 in cmd/slsh/testdata/ask-questions.tsv when this
was measured, 21 started "how do I find", and in 19 of those a
question word or "out" came next: "how do I find where I am", "how do
I find what I am wearing".  There find is a verb of learning.  Counted
in full it was the heaviest word in each -- it is the find command's
name, and regions and lookup have it in their keywords -- and those
commands came above the one that answered.  The tokenizer now counts a
find that a question word or "out" follows at a quarter
(`learnVerbWeight`).  Measured with `TestAskRetrieval` and
`TestAskEvalRetrieval` on 2026-09-26, before and after:

    question                                  wants  before  after
    how do I find what I am wearing           worn   4th     3rd
    how do I find where I am                  where  10th    3rd
    how do I find what is inside that box     ls     9th     7th
      sitting on the floor

On the 103 answerable questions the eval had then, the right command
was in the first 1, 3 and 8 for 80, 94 and 100 of them before, and
81, 96 and 102 after.  Counting such a find at nothing scored 82, 96
and 103, and at a half 80, 96 and 102.  A quarter and not nothing
because of "how do I find where my hair is", which is asking for the
find command after all: counted in full find is first for it, at a
quarter third, and at nothing fifth.

Keywords alone were tried first, and could not do it.  Taking the
second find out of regions' line (its brief already says find) put
worn third, and where stayed tenth.  In that question where's line
has only the word where to be found by, and 242 of the index's 901
documents have that word; saying it twice in where's keywords took
where to seventh.  The one keyword that put it in the first three was
find, and where does not answer to find: it would have come up for
every "how do I find" question in the set.

## Why save takes two plain arguments

`save` writes a local file into a notecard or a script.  Reading an item
out has had commands for a long time -- `cat` prints a notecard or a
script, `get` writes a texture to disk -- and so has making a new one,
which is `new --from FILE PATH`.  Writing a file into an item that is
ALREADY there had none, although the session layer has done it all
along: `sl.SaveNotecard` and `sl.SaveScript` were reachable from `new`
and from nothing at all respectively.

    save notes.txt readme            a notecard
    save hello.lsl /Scripts/greeter  a script, which is compiled

The word is the viewer's.  These are the two capabilities behind its own
Save button -- `LLPreviewLSL::saveIfNeeded` asks the region for
UpdateScriptAgent (llpreviewscript.cpp:2569) and
`LLPreviewNotecard::saveIfNeeded` for UpdateNotecardAgentInventory
(llpreviewnotecard.cpp:674) -- so "save" is what somebody who has used
the viewer already calls this.  "put" was not free to take: it means
uploading an image, which costs L$ where a notecard and a script cost
nothing, and one word for both would hide that.

Two positional arguments, and not `save --from FILE PATH` -- which would
have matched `new --from FILE PATH` word for word, and was the other
real candidate.  What differs is that `new` can make an empty notecard
and `save` cannot write one: the file is the whole of what this command
does, and an option that must always be given is a positional argument
spelled at length.  That is the objection that kept the object out of a
flag in `start` and `stop`.  It is also a trap: a `--from` that may be
left off makes `save readme` a legal line that empties a notecard, and
nothing would have been asked for.

So the source is first and the destination second, in `cp`'s order, and
which side is which is the shell's own vocabulary rather than a
convention invented here.  FILE is on this machine wherever it appears
-- `put FILE`, `. FILE`, `--from FILE` -- and PATH is in inventory
wherever it appears -- `cat PATH`, `rm PATH`, `drop OBJECT PATH`.
`get -o FILE PATH` reads the other way round because its subject is the
thing on the grid and the file is only where the copy lands; here the
file is the subject and the item is where it lands.

What `save` does not do is start anything, or touch the world.  A script
in inventory is not running and cannot be made to run: an object is the
only place a script runs at all, and putting one there is `new --in
OBJECT`, which compiles it inside the object and starts it (see
`sl.InstallScript`).  Saving compiles too -- the capability answers with
the verdict -- but what it has changed is the item, and the copies
already inside objects are untouched.  So the output says whether it
compiled and says nothing whatever about running, and there is no
`--in` here: a second way into an object would be a second thing to
keep right.

## A link's id is not the item's

`linkTarget` follows an inventory link to the item it points at, for
the commands that send an id to the grid as the thing itself.  It
matters because an outfit folder holds nothing else.  Everything under
My Outfits is a link.  It carries the same name as the thing it points
at, and a listing tells the two apart only by the word "link" in the
type column -- so the path a person reads the name off, when they are
looking at an outfit they want back, names a link almost every time.
Links are what a person has in front of them when they are reading off
the name of something to put on.

The id on a link is its own, and its "asset" is not an asset: it is the
ITEM id of what it points at, delivered as linked_id where an item
carries asset_id.  A command that took the id it found there and sent it
would be sending an id the simulator has no object for, and the
simulator answers an id it does not know with silence rather than an
error -- so the whole of what a person sees is their command sitting out
its timeout and then saying the region never agreed.  Nothing in that
sentence is true except the last clause, and the thing that went wrong
is not mentioned anywhere in it.

For `wear`, sending a link's own id in RezSingleAttachmentFromInv sends
the simulator an id it has no object for.  So the whole of what the
person sees is the forty second wait running out and then a time-out
waiting for the simulator to report it as worn -- true in every clause
and about nothing that was wrong.

So `wear` follows one, which is what the viewer does with the same
click.  `linkTarget` does the following, and refuses the two cases where
there is nothing to follow to: a link whose item is no longer in
inventory, since a link outlives what it pointed at, and a link to
another link, which the viewer also declines rather than choosing how
far to go.

## Why the pair is take and place

`take` and `place` move objects between the world and inventory.
"rez" is what everyone calls the second of these, and it is already the
command that builds an object from a JSON file.  One word cannot mean
both "make what this file describes" and "put back what I took": the
first invents an object and the second restores one, and a person who
mixed them up would be told their file was not valid JSON.  That has not
changed and is not going to.

What did change is that "place" became free.  It used to be the command
that repositioned something already rezzed, and that is now `move`,
which is the plainer word for shifting a thing that is already there and
leaves "place" to mean what it sounds like: putting a thing into the
world.  So the pair is take and place, and each of them says which
direction it goes in.

`place` was called "bring", and nothing answers to that now.  A script
that says it stops with an unknown command, which is loud, immediate and
costs a re-run, so there is no alias for it: an alias would keep the
word in circulation, and the word being a poor description of the act
is the whole reason for the rename.

"place" is the half worth being careful about, because it did not
disappear -- it changed meaning, and both meanings are spelt the same
way.  See the argument count in `cmdPlace` for what that costs and what
is done about it.

## move and login, and the words they replaced

`move` was "place" until the word was wanted for putting an inventory
object into the world, which is what "place" sounds like it means.
"move" says what this one does without any of that argument, since the
thing is already in the world and all that changes is where.

`login` was "host" until the word was measured against what a person
asking for it has in mind. Hosting is what the daemon does with a
session once it exists, which is slgod's half of the arrangement and
not a thing anybody types at a prompt; what the person wants is the
avatar logged in, and logout was already the word for the other
direction. Nothing answers to "host" now: the pair reads login and
logout, and a half-renamed pair would be worse than either.

## start, stop and new inside an object

`ls`, `cat`, `rm` and `mv` take `--in OBJECT` because each of them is
an operation the shell already has somewhere else, and `--in` only says
which container to perform it in. Starting a script has no counterpart:
a script in inventory does not run and cannot be made to, because an
object is the only place a script runs at all. So there is nothing for
a flag to choose between. `run --in Box1 hello.lsl` would be a flag
with one legal value, which is a verb spelled at length -- and it would
put the object, the one argument that is never optional, behind a
flag.

So the object is an argument, in the place `drop` puts it:

    start Box1 hello.lsl   start one script
    start Box1             start every script in the object
    stop Box1 hello.lsl

and, as in `drop`, the first word is the object and everything after it
is one name. An object whose name has a space in it is quoted; a script
whose name has one need not be.

The names are the plainest words for it. The viewer has no verb to
borrow -- its script editor shows a "Running" tick box, and "running"
and "unrunning" are not a pair of commands -- and "run" is worse than
it looks, because `sl.Run` means putting a script in, compiling it and
waiting for what it says, which is a different and much longer act
than flipping a switch on one that is already there.

`new --in` goes the other way round because making a script IS an
operation that exists in both places, and `--in` says which. What
differs is what happens afterwards: a script made in inventory sits
there, and a script put into an object is compiled and started by the
same call that puts it there (`sl.InstallScript`), so the two report
different things and the command says which of them it did.

A notecard cannot be made inside an object at all -- nothing here can
write one into a prim -- so `--in` without `--kind` means a script,
since a script is the only thing it could mean, and `--kind notecard`
with `--in` is refused rather than quietly made in inventory instead.

## link and unlink

Linking has been possible in the sl package since `Build` had to make
anything bigger than one prim -- `sl.Link` is what puts a described
object together -- and there had never been a way to ask for it from
the prompt. Taking one apart was possible nowhere at all, so
`sl.Unlink` is new and `unlink` is the command that wanted it.

### Why link takes several names and unlink takes one

`link` names a root and everything that goes under it, so each argument
is one object and a name with a space in it is quoted:

    link chair "left leg" "right leg"

`unlink` names one thing, so its arguments are joined back into one
name the way `detach`'s are, and `unlink a lamp` means what it looks
like. The two rules differ because the commands differ, and the
alternative -- making `link` quote-free by taking the root and then a
list -- needs a separator between the two halves that is not a space,
which is a new thing to remember for a command whose whole job is one
sentence long.

### What unlink does with a root, and with a child

The whole of it comes apart when given a root, and just that prim
leaves when given a child. That is the viewer's pair of behaviours --
clicking an object selects the linkset and Unlink frees all of it,
while Edit Linked Parts selects one prim and Unlink frees only that --
and it falls out of what a delink message is: the local ids in it are
the prims being FREED, so the caller says which they are. See
`sl.Unlink` for the citation.

Naming a child is a real thing to want and not a mistake to guard
against: a prim of a linkset has its own name, `objects -c` prints
them, and a linkset is otherwise all or nothing.

### What happens to a linkset's name

A linkset answers to its root's name, so "chair" is the four-prim chair
while the four prims are linked and is one prim afterwards. The pieces
keep the names they had inside it, which for anything built by hand is
often "Object" for all of them, so after an unlink several things in
the region can answer to one word -- and `objectNamed` refuses an
ambiguous name rather than picking. That is why `unlink` lists what it
freed with keys instead of printing a success line: the keys are the
only handle on the pieces that is certain to work, and the moment they
are wanted is the moment the object comes apart.

### Why the report is read back rather than counted

Both commands say what the object is now, and both work it out from
what the region says afterwards rather than from how many arguments
they were given. Linking something that was already a linkset brings
its prims along, so `link a b` can make an object of seven; and a
person watching wants to know that. Counting the arguments would print
"2 prims" and be wrong in exactly the case worth reporting.

## What wear and detach are called, and what they take

`wear`, `detach` and `dress` finish what `worn` started.  Listing the
attachments has been possible since there was a shell, and changing them
has been possible in the sl package for just as long -- slbench has been
hanging HUDs on an avatar with Wear and TakeOff all along -- so the only
thing missing was a way to ask for it from the prompt.

`wear` names something in inventory and `detach` names something worn,
and those are different places even when they hold the same word.  So
`wear` takes a path, the way `place` does, and looks the item up;
`detach` takes a name and matches it against what is actually on,
because the thing it has to send is the id of the item an attachment was
worn from and that is what the region's answer carries.

It could have gone the other way -- `detach` resolving a path to an item
and sending that id without ever asking what is worn.  It would work,
and it would be silent about the case that matters: a name that is in
inventory and not on the avatar would be detached with every appearance
of success and nothing would happen.  Asking first costs one call and
turns that into a sentence.

The object's key is not what `detach` takes.  A worn object is rezzed
afresh, with a key nobody has seen before, every time it goes on and
again at every login, so its key is worth nothing the moment it comes
off.  The inventory item does not change.  A key typed at `detach` is
therefore looked for as either -- both are in the answer already, so
neither costs anything -- but the item is what goes on the wire.

The names are the viewer's words, from the menu somebody will have used
before they came here.  "attach" and "remove" were the alternative and
were worse in both halves: attach is what the protocol calls it rather
than what a person does, and remove sits one letter from `rm`, which
deletes things.

## Why wear adds rather than replaces

A point can hold more than one attachment, and which of the two happens
is the request's to say: the point travels in one byte with 0x80,
ATTACHMENT_ADD (indra_constants.h:193), laid over it, and the viewer
lays it there exactly that way -- "if (attachment.mAdd) attachment_pt
|= ATTACHMENT_ADD" (llattachmentsmgr.cpp:247-249).  So the difference
between the viewer's Add and its Wear is one bit, and the choice of
which is the default is entirely ours.

`wear` replaced, and now it adds.  What settled it was measured on Agni,
in Pelmar Reach: holt was wearing "auto 11" on HUD bottom right, and

    wear Objects/auto 3

with no `--at` at all put auto 3 on HUD bottom right and took auto 11
off.  Nothing was said about auto 11 by anybody -- not by the command,
which printed its one line about auto 3, and not by the region.  It
simply stopped being worn.

That silence is the argument.  The person most likely to type `wear` is
already dressed; the point an object asks for is one they did not choose
and usually do not know until the answer names it; and so the replacing
default put the loss of something they were wearing behind a command
that reads as purely additive.  A wrong add is visible and costs a
detach.  A wrong replace is invisible and costs whatever was there.
Between two defaults, the one to have is the one whose mistake can be
seen.

`--replace` is the old behaviour, kept because putting a thing where
another thing is really is sometimes what is meant -- and it now names
what it displaced instead of leaving that to be found out.

The point laid under the bit may be 0, which is a value here rather than
a missing one (see `attachWhereItSays`).  "Add, wherever the object
itself says" is therefore the single byte 0x80, and that is what a bare
`wear` sends.

The add bit is laid on on the way past, in this command, rather than by
anything in sl.  `sl.Wear` takes the AttachmentPt byte and sends it, and
that byte is the point with the bit over it: the protocol has one field
for both, so a caller able to pass 0x85 can already say everything the
message can express.  A second way of saying it in sl -- a flag, an
options struct -- would be an argument nobody but this command ever
varies, and it would sit two files from the `--replace` that decides it.

The other half of that is what it leaves alone.  `sl.Wear`'s other
callers pass the byte they mean and are untouched by any of this: the
pool of auto objects lays the add bit on itself before handing the point
to `EnsureAttached`, and slbotd's `wear` sends a bare point.

## Why wear will not put one item on twice

Adding makes it possible to wear one inventory item twice, and `wear`
refuses to.  An attachment is known here by the item it came from -- the
session keys its map that way (`w.attach` in sl/attach.go) and the
object's own id is freshly minted at every attach and every login -- so
two attachments from one item are two rows agreeing in every field a
person could name one by.  `detach` would find both, refuse as
ambiguous, and advise telling them apart by the item id that `worn -l`
prints, which is precisely the thing they share.  That is a state this
shell can create and cannot then unpick, so it is not created: the
refusal names the point it is already on, and names `--replace` and
`detach` as the two ways on from there.

The viewer declines the same thing more quietly, by dropping the request
where a person cannot see it -- "ATT duplicate attachment request,
ignoring" (llinventorybridge.cpp:8144-8149).

## Naming what a replace took off

`wear` asks what is worn before it sends, which is the same question the
refusal of a second copy needs answered, so that read is paid for either
way.  The last line then says what a `--replace` displaced: "X is worn
on chest; Y came off".

That clause was first written as a prediction -- whatever was on the
point beforehand -- on the reasoning that the region had been asked to
replace and had answered by putting the new attachment on that point.
It is wrong, and it is wrong in exactly the case the change to adding
created, because before it a point never held two.  Measured on Agni,
as holt, with auto 11 and auto 3 both on HUD bottom right:

    wear --replace --at "HUD bottom right" Objects/auto 4
    auto 4 is worn on HUD bottom right; auto 11 and auto 3 came off

and afterwards the point held auto 3 and auto 4.  A replace displaces
ONE attachment, not the point's worth of them, so the prediction named
something that was still on.

So it is confirmed instead: what was on that point before, is not worn
now, and is not the thing just put on.  That is one more read and not
the polling loop `detach` needs -- `detach` polls because nothing else
will ever tell it, whereas here `sl.Wear` has already waited for the
region to describe the new attachment, so the answer is sitting there
for the asking.

Both ways of coming up empty say nothing rather than inventing a
reassurance.  If the difference is empty -- nothing was displaced, or
the region has not caught up with the fact yet -- the line is the bare
one; the failure that leaves is a person not being told about something
that did come off, which is where they were before this clause existed,
rather than being told a thing that is untrue.  And a read that fails
prints the line without the clause: the wear worked, and it is the
report that could not be finished.

## Why detach waits and TakeOff does not

Nothing replies to a detach.  `sl.TakeOff` puts the message on the wire
and returns, and what says the thing came off is the object no longer
being among what is worn -- which happens some time later.  Live, that
gap is visible: a `worn` run straight after a `detach` still listed the
attachment on the point it had just been taken off, and only the run
after that showed it gone.

So `cmdDetach` polls until the region agrees, and `sl.TakeOff` is left
as it was.  Its caller there, `Worn`, which `EnsureAttached` goes
through, takes a thing off in order to put it straight back on and does
its own settling; making `TakeOff` wait would slow it for a confirmation
it throws away.  What is at stake is the command's last line -- "is no
longer worn" is a claim the SHELL makes, and the shell is what should
have established it before printing it.

## What worn used to leave out

`worn` reads two records, the Current Outfit folder and the region's
description of the objects around the avatar, and lists both.  It used
to list only the region's description and call the result what was
worn.  It was not: an avatar wearing a skin, a shape and four clothing
layers showed none of them, and the answer read as a complete one.

## How long tp waits for another region

`tp` waits thirty seconds for a teleport to another region before
saying nothing answered, where `sl.DefaultTeleportTimeout` is ninety.
That constant was set before a teleport had ever been timed; forty moves
between Pelmar Reach and Sandbox Goguen have since been measured at 355
milliseconds to 4.95 seconds, so ninety is two orders of magnitude above
what it covers. Thirty is six times the slowest move measured, which
leaves room for a grid having a bad day, and it is what a teleport
inside the region has been given all along.

The other end of the argument is what waiting costs. A shell that
inherited the ninety would sit silent for a minute and a half over an
offer the grid was never going to answer, and the third of
`sl.Teleport`'s failures -- a request answered with nothing whatever --
is exactly the one a person meets when they accept a second lure while
the first is still under way.

## Where tp lands when no position is given

A teleport with no position lands in the middle of the region
(`tpMiddle`), which is 256 metres square, because that is where a
viewer puts an avatar that typed a name into the world map and said
nothing about where in it.

The height is left at zero rather than guessed at, and zero is not
arbitrary: measured on Agni, the avatar arrives at whichever is higher
of the height asked for and the ground under the point, plus about a
metre -- 30 came back as 31, 60 as 61, and 0 as the ground. So zero is
how a client asks for ground level without knowing where the ground
is, and there is no cheap way to ask that about a region this session
has never been to.

What it does not promise is dry land. A region's middle can be under
water, and Sandbox Goguen's is, so this arrives there submerged. That
is the region rather than the default, and the man page says so.

## tp home, and a region called home

`tp` takes the word home (`wantsHome`) only bare, and only when it is
the whole of what was said, so that the region name a word could also
be is reachable in the forms that carry one: `tp home 128 128 25` is a
region called home, as it was before this existed, and so is anything
with more words in it. What is out of reach is a region whose whole
name is "home" gone to without a position -- and, measured on Agni on
2026-09-01, there is no such region: the map answers the prefix "home"
with nine longer names and nothing that is exactly it.

It is matched without regard to case, since it is this shell's word
and not a name the grid keeps: "Home" at a prompt is the same word, and
a rule that sent one of them to the map and the other to the account's
home position would be a difference nobody could see.

## A negative coordinate after tp

`tp` puts a "--" in front of the position when the position has a
negative number in it and nothing before it has ended the options
already (`endOptionsAtANegativeNumber`). Option parsing would otherwise
eat one: "-10" is the option -1 with the value 0 as far as getopt is
concerned, so `tp -10 128 25` answered "unknown option: -1" and a
position west of this region's corner could not be typed at all. A
"--" says the rest are operands, which is what it means everywhere;
putting it there rather than making somebody remember to is what keeps
the obvious line working.

It goes in front of the LAST THREE arguments rather than the first
negative one, because that is where a position is in every form tp
takes, and because put ahead of the whole position it survives
`tp --wait 60 -10 128 25`, where the flag and its value are parsed
before it.

It goes there only when nothing in front of the position is an
operand, which is to say only when there is no region name -- the
`tp X Y Z` form. getopt stops reading options at the first operand, so
a negative coordinate after a name was never at risk and needs no
help; and a "--" put there is not an end-of-options mark at all, it is
a word, so it was joined onto the name. `tp Example Landing -10 128 25`
was refused as a region called "Example Landing --" for as long as this
inserted one whatever came before.

And only when one of the three really begins with a minus, so that a
mistyped option is still reported as one rather than handed on as a
region called "-wiat".

## Why tp says an arrival twice

When `tp` takes the avatar to another region, two lines say so. The
notice from the shell's watcher says the avatar is in another region,
and the line `tp` prints says where it ended up. Both are wanted and
they say different things: one is the session's news, which arrives
whoever asked for the move, and the other is this command's answer to
the person who typed it. Suppressing the notice for a change this
command asked for would need state shared between the two, and would
silently swallow a second change that arrived at the same moment.

## Naming a landmark, and going to one

`landmark`, in `cmd/slsh/landmark.go`, lists, reads, makes and goes to
landmarks, and goes home and sets home. The measurements behind it are
in [doc/history/landmark.md](history/landmark.md).

### Going somewhere is a word and not a letter

If going somewhere were the bare form, then reading a landmark and
being somewhere else afterwards would be one typo apart -- so it is not
the bare form: "landmark NAME" says where it goes and
"landmark --go NAME" goes there. For the same reason none of the four
verbs has a short letter. -g beside -m is exactly the typo that the
whole word is there to prevent, and the only cost of spelling it out is
four characters on a line that moves an avatar across the grid.

`--set-home` is the sharp end of the same argument. It is the one verb
whose damage a teleport does not undo -- an avatar sent to the wrong
place walks back, and an account whose home was quietly rewritten finds
out weeks later, somewhere it did not mean to log in -- so it carries
the word "set", it has no letter, and it takes no name. It is the one
that changes something and does not move anybody: `--home` beside a
`--home` that meant "make this home" is one keystroke between going
somewhere and rewriting where this account starts, and the second of
those is not undone by teleporting back.

### A landmark is something the avatar holds

An inventory item has an ITEM id and an ASSET id and the grid takes
only the second: measured, the item id and a uuid that is nothing at
all are both answered with perfect silence, waited out to twelve
seconds ([A wrong id is silence](history/landmark.md#a-wrong-id-is-silence)).
There is no error to catch and nothing to report.

So `landmark` never hands the grid a uuid a person typed. A name is
looked up in what this avatar KEEPS -- inventory less the trash, see
`landmarksHeld` -- and the asset id comes out of the listing, which is
the one place the two ids are told apart correctly. A uuid typed there
is looked up in that same listing -- as an item id or as an asset id,
since a person pasting one from "ls -l" has the first -- and a uuid
that names nothing there is refused rather than sent. Reading an
unknown uuid would in fact work, because the fetch either parses as a
landmark or does not; but the same uuid handed to `--go` is either an
asset id or an item id and nothing distinguishes them but a wait that
never ends, so the two forms would differ in a way nobody could predict
from the outside. One rule: a landmark is something you have.

## Where a sit leaves the avatar

`sit` was the first thing in the shell that moved the avatar without a
teleport, and it moves it a long way.  A sit is not a walk: the
simulator picks the avatar up and puts it on the seat, over whatever is
in the way, from as much as ten metres off -- measured on Agni, a box
seven metres away seated the avatar as readily as one half a metre
away, and standing up afterwards left it six metres from where it had
been standing.  So what `sit` and `stand` print is not only what they
did but where that left the avatar, in the same words `where` and `tp`
say it, and the position line is the answer rather than decoration.

For `stand` it is more surprising: standing does not undo the journey
the sit made.  Measured, an avatar that walked -- was carried -- seven
metres to a box was left six metres from where it started when it stood
up again, so the line `stand` prints is where the avatar now is and not
where it was before any of this began.

The measurement is written up in
[doc/history/sit.md](history/sit.md#a-sit-moves-the-avatar-and-about-ten-metres-is-the-limit).

## Sitting on an object nothing has described

`sit` given a uuid sits on it even when nothing in the listing of what
the region has described has that id.  A uuid needs no listing:
`AgentRequestSit` carries the id and the simulator resolves it.  What
the listing is for is the local id, which `sl.Sit` uses for one thing
only -- seeing that the avatar is already on that very object -- and
can do without.

It matters because the listing is not complete.  Measured: a chair
plainly in world, sat on ten minutes earlier, absent from a listing of
976 objects.  Nothing had described it since login and nothing would
unasked -- a region describes each object once.  Why its description
never arrived was not established; a packet thrown away as undecodable,
which the decoder did at the time, is the likeliest reason.  Refusing to
sit on an object whose id is right there, for want of a description of
it, is refusing to do a thing that works.

## The maturity command and a refused teleport

`maturity` tells apart the two numbers an account carries about land
ratings -- the PREFERENCE, the highest it has asked to be shown, and
the CEILING, the highest it is permitted to ask for -- because the
distinction is invisible from a refused teleport, which is where
anybody meets it.  Measured on Agni on 2026-09-02, two avatars a moment
apart to the same public region on the adult continent -- one arrived,
and the other got

    RegionTPAccessBlocked: "You aren't allowed in that Region due to
    your maturity Rating. You may need to validate your age and/or
    install the latest Viewer. ..."

which names both causes and picks neither.

Asking for adult makes the grid say which it was: granted adult, and
the preference was the problem and is now fixed; granted something
lower, and that is the ceiling, and the rest of the job is on a web
page.  The refused avatar above was granted adult and made the same
journey a minute later, so the first half is measured.  The second half
-- a grant lower than the request -- is not: neither account this has
run against was capped.

## Neighbours on, and no circuit yet

`neighbours` with the circuits on and none held is a state worth
spelling out, and it is not what it was once documented as.  It was
said to mean "not near a border", on the belief that a simulator offers
nothing to an avatar in the middle of a region; measured on 2026-08-23,
the middle of a 256-metre region is offered all four edges.  What an
empty listing means is almost always "not yet": the first circuit took
between twenty and sixty seconds from turning them on, and the set went
on growing for a minute after that.  See
[cmd/slsh/man/neighbours.md](../cmd/slsh/man/neighbours.md).

## A neighbour with no handshake

`neighbours` prints "(no handshake yet)" for a circuit without a
handshake, since the name arrives in the handshake and there is none to
print.  Seeing it twice running does NOT mean the offer came to
nothing: on 2026-08-23 three of the five simulators around one region
sent no handshake through four openings each while their packet counts
climbed, and the same three had answered on an earlier day.  Heard is
what says the circuit is alive.

## The colours a map is drawn in

`map` picks a friend out of the picture in a colour, and `parcel --map`
draws its parcels in them; the colours are named, in `mapColours`.

A name and not an escape sequence.  Nobody should have to write
"\x1b[32m" into a configuration file to choose a colour, and a name is
the thing that can be checked: a misspelled name is refused where it
was typed, where an escape sequence somebody got wrong would be written
into the middle of the picture and arrive as rubbish among the marks.
It is also the only spelling that survives being read back: `set`
prints what the file would take, and an escape printed to a terminal is
invisible.

The colours are the eight a terminal has had since it was a terminal,
and the bright half of each for the terminals that have them, written
"bright green".  Nothing is a 256-colour index or an RGB triple: those
are not colours every terminal has, and one that has them draws the
eight by their own scheme anyway, which is what makes green mean green
on somebody's own screen rather than a particular green.

The foreground and not a block of colour behind the mark.  A background
commits to one terminal's idea of paper -- a green slab is the only
thing the eye sees on a dark terminal, and dark text on it is hard to
read on a light one -- while coloured ink over whatever paper is
already there is legible on both, and leaves the mark itself readable
as the mark it is: an "o", a "^" and a count all still say what they
said.

## Drawing parcels rather than ownership

`parcel --map` draws the pieces the overlay's boundaries cut the region
into, each with its own mark, and the key under it names them.  Drawing
the ownership instead was the first attempt and it drew nothing: every
square of a region of Linden Homes reads "owned", so the picture was one
character from corner to corner.  What a person wants from a map of
parcels is which parcel is which, and that is what the overlay's
boundaries are for.

## Protected land drawn as ground

`parcel --map` draws Linden's protected land as ground rather than as a
parcel: blank for the roads and waterways a region is laid out around,
and "." for the rez zones inside them.  It is most of a mainland region
by area and none of it is anybody's, so giving it a letter of its own
puts the loudest mark in the picture on the one parcel nobody is asking
about -- and takes the eye off the homes, which are what a person is
looking for.  It is matched by name rather than by owner because the
name is what says which it is: "Protected Land" and "Protected Land -
Rez zone" on Pelmar Reach, measured 2026-08-18.

## Finding a person by name

A command that takes a person turns what was typed into somebody: a
uuid, the number from the last listing, a name the session has heard,
somebody standing in the region, or somebody the grid's search knows.
The code is in `cmd/slsh/social.go`: `who`, and the functions below.

### Asking the region for a name

`whoNear` looks at the region when nothing the session has heard names
the person.  The session's name cache holds whoever has been mentioned
to it: a listing printed, somebody who has spoken, a conversation
opened.  Nothing evicts from it -- the daemon keeps avatars whatever the
distance (`agent.Objects.Trim`) and the cache is kept for the life of
the session (`sl/names.go`) -- but a shell that has just started has had
nothing mentioned to it, so `slsh -c` would refuse a name that `who`
would have listed a moment later.  The daemon has known that avatar all
along; only this process had not asked.

So asking is the second step.  It is safe for every command that
resolves a person because it is not a guess: the answer is somebody
standing in the region, matched by the same rules the cache is matched
by, and a shell that lists a person under a name has to accept that
name back from the next command typed.

The cost is the reason the region is asked second and not first: it is
a round trip to the daemon and a name resolution, so the case that
already works must not pay for it.

Searching the grid is the third step, and it is kept out of `whoNear`
because the two kinds of caller take different answers from it.
`whoOrSearch`, for `profile`, which only looks, takes a name that
merely resembles what was typed when it is the only one; `onTheGrid`,
for the commands that reach somebody, takes only the name itself.

### Searching the grid for somebody to reach

`onTheGrid` is the last place a name is looked for by the commands
that reach somebody -- `im`, `chat`, `offer`, `lure`, `give`, `invite`.

These commands search at all because the person they are for is so
often not here.  Somebody invited into a group is quite often being
invited because they are somewhere else, and a name nothing here had
heard of used to be refused with advice to run `lookup` and type the
number it printed -- which is a thing that has to be learnt, for a line
that plainly said who was meant.  The search costs one round trip a
run, made only on a line that was about to fail.

It answers only to a whole name.  The search matches part of a name,
and display names as well, so what comes back is everybody the words
resemble.  For `profile` that is fine -- guessing wrong there costs a
listing -- but these commands deliver something to whoever the name
resolves to, and a name guessed at wrong hands a message, a friendship
or an item to a stranger.  So a row is taken only when its name IS
what was typed: "First Last" in any case, or the same with a dot for
the space, which is how a username is written.  Anything short of that
is listed, numbered, and refused, which is the rule the rest of the
shell keeps for an ambiguous answer.  One row that is not the name is
refused as well: it is the grid's best guess, and a guess is what is
being kept out.

A bare word is never a whole name here, even where it is somebody's
username exactly.  The last run searched is the first word of the line,
whatever was meant by it, so for "im Lorn Harbour hello" with nobody
called Lorn Harbour, taking a username of "lorn" would send "Harbour
hello" to whoever holds it -- and a first name on its own is the kind
of word that somebody has probably registered.  The listing shows that
person's whole name, which is what to type.

A search that could not be made has found nobody, never somebody: the
refusal the nearer places gave comes back as it was, saying why the
grid could not add to it.  That includes a session that was not given
the capability.  `lookup` falls back from it to the older whole-name
message, and `onTheGrid` does not, because the older message has
nothing but a fifteen-second deadline to say that nobody answered it,
and a line that was going to fail should not be made to wait that long
to do so.

### A name at the front of a line

`whoAndRest` reads the person named at the front of a command line for
the commands that take somebody AND something else -- `im`, `offer`,
`lure`, `give`, `invite`.  They cannot simply read the first word as
the name, because a name has two words in it and `sh.who` matches
either half of one.  Measured live, with the names changed:

    $ slsh -c "im Example Resident hello from the guide"
    > [IM Example Resident] Resident hello from the guide

"Example" resolved to Example Resident all by itself, so the last name
became the first word of the message.  It went to the right person and
said the wrong thing, and nothing on this side looked amiss -- the same
shape as "place probe 10 20 30", where a partial match succeeding is
what makes the mistake silent.  So the longest leading run that names
somebody wins.

### The search profile makes

`whoOrSearch`, which `profile` uses, falls back to the grid's own
search when nothing here has heard the name.  `sh.whoNear` reaches
whoever has been mentioned and whoever is standing in the region, which
is everybody a shell usually talks about and not everybody there is.
Somebody on the other side of the grid has been mentioned to nobody and
is standing nowhere near, so

    slsh -a qi -c "profile Perrick Hobb"

would refuse a name that `lookup` finds at once.  A profile is exactly
the question one asks about somebody who is not here, so the command
that answers it should not be the one command that cannot find them.

It is not the search `sh.who` makes.  `sh.who` searches too, and takes
less from it, because of what its callers do with the answer.  Reading
a profile is a public question about somebody, answered by the grid to
anybody who asks: nothing reaches the person, nothing is spent, and
guessing wrong costs a wasted listing on the screen.  `im`, `offer` and
`give` reach OUT -- a message arrives, a friendship is offered, an item
changes hands -- and a name guessed at there delivers it to a stranger,
which is a different kind of mistake and not one to make on somebody's
behalf because a search was convenient.  So they take a whole name from
the grid and nothing less (see `onTheGrid`, above), where `whoOrSearch`
takes the one row a search returned and a username typed alone as well.

One hit is the answer.  A name that matches one row exactly is that row
even when the search returned others, which is what `chooseGroup` does
with a group name and for the same reason: a name typed in full is not
an ambiguous name.

Several are printed, as `lookup`'s own numbered listing and through
`lookup`'s own code, and the refusal after them says only that a
number picks one.  Naming them in the sentence instead is what this did
first, and one letter typed on Agni made it ninety-five names joined by
commas into a single line -- ending with a promise about numbers that
were nowhere on the screen.  A list that somebody is meant to choose
from has to look like a list.

## The star in agents during a reconnection

`agents` stars the session that a command naming no avatar would be
given, and a CONNECTING session can have the star as well as a HOSTED
one (`canBeTheDefault`). CONNECTING is the one that matters and it was
the bug: a circuit that dropped and is being rebuilt never costs a
session its place, so the daemon still answers with it, while a star
drawn on the first HOSTED row moved to the second session for as long
as the reconnection took -- the listing and the daemon disagreeing
exactly when somebody is reading the listing to find out what is going
on.

## Why auto prints no count of benchmarks

`auto` reports what is worn, which is what can run at once, one object
per script, and that is the whole of what this avatar's share buys. It
used to offer a count of benchmarks alongside, worked out from
`session.AutoGroupSize`, and that number was wrong: a benchmark leases
one object per division of each of its searches and three besides,
which is nineteen at slbench's defaults and moves with `--parts` and
`--extra`, and it halves that again when the pool cannot grant it.
Only slbench can say it, and it says it when it settles for less. A
figure printed here could only go stale again, which is worse than not
printing one.

## Starting a viewer from slsh

`viewer`, in `cmd/slsh/viewer.go`, says where slgod serves viewer
logins and whether a viewer has the session, and `viewer --launch`
starts a real viewer and hands it the session. `viewerDefaults`, in
`cmd/slsh/config.go`, is what it launches with. What slgod does with
the session once a viewer has it is [doc/handover.md](handover.md).

### Why the viewer address is in a status

slgod can serve an XMLRPC login endpoint that hands a running session
to a real viewer. Until the address crossed the wire, it appeared in
exactly one place: a line in the daemon's log at startup
(`cmd/slgod/viewer.go`, "viewer logins at %s"). Somebody who attached
with a shell an hour later, or from another terminal, or after the log
had scrolled, could not ask. A feature reachable only by whoever
started the daemon and still has the window is close to no feature at
all.

It rides in `StatusResponse` rather than a call of its own because it
is the same kind of thing as the counters there: state the daemon
holds, about one agent, that a client cannot work out for itself.
Minting a credential is not that -- it has an effect -- so that is a
call of its own; see `server/viewer.go`.

### When slgod serves no viewer logins

`-viewer` is not the default and the daemon here runs without it, so a
daemon serving no viewer logins is the ORDINARY case. Printing an empty
address for it would leave somebody comparing a blank field against a
working one with no idea which they were looking at, so `viewer`
answers it in words, with the flag that fixes it.

### The password a launch is given

A profile stores `viewer_password` as a "$1$" md5 digest
(`agent.Login.ViewerPassword`), so nothing on slsh's side can produce a
plaintext that a viewer would hash into a match: the shell cannot know
a password it could type in. So the daemon mints one -- random, good
for a single login, and short lived -- and it is passed straight to the
viewer being started.

What the viewer does with it is what makes that work at all: the
plaintext from `--login` is md5'd whole (llloginhandler.cpp:168-170)
and sent as "$1$" and those hex digits (llsecapi.cpp:136), which is
exactly the form slgod stores and compares -- so a password minted by
slgod matches without either end knowing anything about the other.

It reaches that viewer's argv, which any process this user owns can
read (ps). That is the cost, it is not hidden, and being SINGLE USE is
what answers it: an onlooker who copies the password out of ps is
racing the viewer that is already logging in with it, and loses the
moment it does. The expiry is the belt to that pair of braces and is
deliberately generous, because a viewer takes the best part of a
minute to reach a login screen -- see `viewerCredentialLife` in
`cmd/slgod/viewer.go`, where the timings are. The alternative -- the
account's own grid password -- would put the real credential into a
viewer's saved settings for a login that never leaves this machine,
which is the road `agent.Login.ViewerPassword` explains is closed.

Nothing in slsh prints, logs or keeps the password. The command it
would run is printed with the password struck out, because a launch
that appears to do nothing is otherwise unreadable.

### Why the OpenSim build of Firestorm

`viewerDefaults` names the OpenSim build of Firestorm, and not the one
most people already have installed. This is the trap. A Firestorm
built for Second Life CANNOT be pointed at a private grid at all. That
flavour compiles LLGridManager from llviewernetwork.cpp, whose
grid-file block -- the one that would read the viewer's own
grids.user.xml -- is compiled out (llviewernetwork.cpp:149-201,
"#if 0 <FS:AW disabled for meeting havok sublicense requirements/>"),
so the only grids it has are the two it hardcodes. Driven live, the SL
build at /Applications/Firestorm-Releasex64.app logged

    WARNING #GridManager# llviewernetwork.cpp(214) initialize :
    Unknown grid 'slgod'

and then llviewernetwork.cpp:244, "Default grid to
util.agni.lindenlab.com" -- an Agni login screen, with nothing on it to
suggest why. Somebody who reaches for the viewer they already have gets
exactly that, which is why the default names the other one.

The OpenSim flavour compiles fsgridhandler.cpp instead, which does read
grids.user.xml, and on the machine this was written on it is at
~/Applications/Firestorm-OpenSim.app. "open -a Firestorm-OpenSim"
resolves it by name; both the bare name and the full path were tried
live and both attached.

### The grid is a nickname out of the viewer's own list

`--grid` takes a nickname out of the viewer's own grid list, not a URL.

`--loginuri` is the obvious flag and it does nothing. Firestorm reads it
into the CmdLineLoginURI setting (app_settings/cmd_line.xml:201-208)
and then never looks at it again: both grid managers take the grid from
CmdLineGridChoice, which is `--grid` (fsgridhandler.cpp:269,
llviewernetwork.cpp:206), and CmdLineLoginURI appears nowhere else in
the source but its own unit tests. Driven live, a viewer launched with
`--loginuri` came up on whichever grid it had used last.

Passing the login URI where the nickname goes was tried too, on the
chance that the auto-add path would take it, and did not work:
"Unknown grid 'http://127.0.0.1:9000/'", then Agni again.

What works, and was run twice against a real daemon, is a grid
NICKNAME out of the viewer's own list:

    open -a Firestorm-OpenSim --args --grid slgod --login First Last PASSWORD

So the grid has to exist in the viewer BEFORE any of this works:
Preferences -> OpenSim, add the login URI that `viewer` prints, and
give it the nickname `viewer_grid` names. That is a one-off piece of
local setup, which is exactly the sort of thing that belongs in a
setting rather than buried in a command template where nobody would
find it -- which is why the nickname is a setting of its own.

`--login` itself is read and works: llloginhandler.cpp:165-183 md5s the
third token whole and logs in with it, setting AutoLogin as it goes, so
no `--autologin` is wanted beside it.

### Why macOS goes through open(1)

A macOS application is a bundle, not an executable: the binary is
Firestorm-OpenSim.app/Contents/MacOS/Firestorm, and running it directly
is not the same as launching the app. "open -a" is the supported way
and is what the recovery script on the machine this was written on
already uses (~/bin/sl-restart, which launches with open -a "$APP"
--args --autologin), so it is copied from there.

It also brings the viewer to the FRONT, which is deliberate and is what
was asked for: open activates the application it launches unless -g is
given (man open), and somebody who has just typed "viewer --launch"
wants to be looking at it.

open returns as soon as the launch has been handed off rather than
waiting for the application to exit -- that is what -W is for, and it
is not passed -- so the prompt comes back at once.

### A viewer that is already running

`viewer --launch` refuses when a viewer is already up. "open -a" raises
an application that is already running and does not pass it the
arguments again, so a second launch would bring Firestorm to the front,
log nobody in, and report success. That is the one outcome worth
refusing outright: it looks exactly like it worked.

The check, `viewer_running`, is a process match. It has to answer
before the viewer has a window, a port or a session, and a process is
the only thing it has by then. The pattern is the bundle path rather
than the process name because the process is called plain "Firestorm"
whichever build it came from -- which is also why it must follow the
app setting, since the two flavours are told apart only by their
bundles. sl-restart reaps orphans with the same shape of pattern
(pkill -f "Firestorm-Releasex64.app/Contents").
