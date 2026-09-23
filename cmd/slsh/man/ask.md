`ask` answers "which command does this?" for somebody who knows what
they want to do and not what slsh calls it.  The question is the rest
of the line, in ordinary words, and needs no quoting:

    ask how do I make where I am standing my home

The answer is a line to type, why it does what was asked, and the
sentence from that command's man page that says so:

    to type:  landmark --set-home
      it sets home to where the avatar is standing
      "Make where this avatar is standing the place home is." -- man landmark

Nothing is run.  The line is for you to read, change and type.

## Options

**-i, --index**

Answer from the index alone, without asking the model, even when one
is set up.  Quicker, and the same answer `ask` gives when there is no
model to ask.

**-r, --rejected**

Also list what the model suggested that the checks refused, each with
the reasons.  Those lines are printed quoted and under a heading that
says they are not commands; they are there to show what was thrown
away, not to be typed.

## How it answers

`ask` looks the question up in an index of every command's usage
line, description and man page, built into slsh when it was compiled,
so it is never out of step with the commands.  The commands that
match best are shown to a language model, with the parts of their
pages that matched, and it is asked which of them does what was asked.

What the model says is then checked before any of it is printed:

- the line must run a real command, with flags that command takes,
  parsed the way the shell would parse it when typed;
- the quotation must be found, word for word, in that command's man
  page, usage line or description.

A suggestion that fails either is dropped.  If every suggestion was
dropped, the model is told why, once, and asked again.  The model's
own summary of its answer is never printed, because nothing checks
it; only the lines that passed, their reasons and their quotations
are.

When nothing passes, `ask` says plainly that it found no command for
that, and names the nearest commands with their descriptions, so you
can read their pages yourself.

## Without a model

With no `ask_url` set, with `--index`, or when the server does not
answer, `ask` says which of those it is and answers from the index
alone: the best-matching commands, each with its description and the
example lines from its own page.  Those are lines a person wrote in
the page, not guesses.

    From the index alone (ask_url is not set); the commands whose pages match best:

    landmark -- ...
        landmark --home
        ...

## Setting up a model

Any server that speaks the OpenAI-style `/v1/chat/completions`, such
as llama-server or Ollama, running a small model.  Five settings, set
with `set` like any other:

    ask_url        where the server is; empty is no model
    ask_model      the model's name; llama-server ignores it, Ollama needs it
    ask_slot       a llama-server slot to use; empty is whichever is free
    ask_timeout    how long to wait, like 45s; empty is two minutes
    ask_extra      a JSON object added to every request

`ask_extra` is for what one model needs and another would refuse.  A
model that writes out its reasoning before answering can be told not
to, under llama-server, with:

    set ask_extra {"chat_template_kwargs": {"enable_thinking": false}}

Which options a model takes is the model's and the server's business,
so nothing of the kind is built in.

## Your own words

A file called `ask-hints.tsv`, beside the settings file, teaches `ask`
the words you use.  One hint per line: the words, a tab, and the
commands they mean.

    # my words for things
    snapshot picture	look texture
    stash	put

A question containing every word on the left brings the commands on
the right to the top.  Blank lines and lines starting with `#` are
skipped.  A line without a tab is an error naming the line; a command
name slsh does not have is left out, and `ask` says so each time it
reads the file.

## Examples

    ask how do I make where I am standing my home
    ask which avatars are near me
    ask -i take off an attachment
    ask -r how do I list what is in a folder

See also: `man` for a command's whole page, `help` for the commands
by group, and `set` for the settings.
