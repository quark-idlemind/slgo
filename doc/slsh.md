# slsh, and why it does what it does

The comments in `cmd/slsh/` say what the shell does. This page is why,
where the why is a story: what was measured, what was tried first, and
what was wrong with it.

What a command does, for somebody typing it, is its man page, in
`cmd/slsh/man/`; the guide to the shell is `handbook/slsh-guide.html`.

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
