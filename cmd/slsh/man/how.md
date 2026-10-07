`how` answers "which command does this?" for somebody who knows what
they want to do and not what slsh calls it.  The question is the
whole line, `how` included, in ordinary words, and needs no quoting:

    how do I make where I am standing my home

`How`, with a capital, is the same command, since a question typed as a
sentence starts with one.  No other spelling is.

The answer is a line to type, why it does what was asked, and the
sentence from that command's man page that says so:

    to type:  landmark --set-home
      it sets home to where the avatar is standing
      "Make where this avatar is standing the place home is." -- man landmark

Nothing is run.  The line is for you to read, change and type.

## Options

**-i, --index**

Answer from the index alone, without asking the model, even when one
is set up.  Quicker, and the same answer `how` gives when there is no
model to ask.

**-r, --rejected**

Also list what the model suggested that the checks refused, each with
the reasons.  Those lines are printed quoted and under a heading that
says they are not commands; they are there to show what was thrown
away, not to be typed.

## How it answers

`how` looks the question up in an index of every command's usage
line, description and man page, built into slsh when it was compiled,
so it is never out of step with the commands.  The commands that
match best are shown to a language model, each with the opening of
its page and some of the parts of it that matched, and it is asked
which of them does what was asked.

What the model says is then checked before any of it is printed:

- the line must run a real command, with flags that command takes,
  parsed the way the shell would parse it when typed;
- where a command takes a word from a list slsh keeps -- a setting's
  name for `set`, a rating for `maturity`, a group for `help` -- the
  word must be on that list, or be the capitalised placeholder the
  usage line writes, like NAME;
- the quotation must be found, word for word, in that command's man
  page, usage line or description.

A suggestion that fails any of these is dropped.  If every suggestion
was dropped, the model is told why, once, and asked again.  Nothing
else the model writes is printed, because nothing checks it; only the
lines that passed, their reasons and their quotations are.

When nothing passes, `how` says plainly that it found no command for
that, and names the nearest commands with their descriptions, so you
can read their pages yourself.

## Without a model

With no `how_url` set, with `--index`, or when the server does not
answer, refuses, or answers with something that cannot be used, `how`
says which of those it is and answers from the index alone: the best-matching commands, each with its description and the
example lines from its own page.  Those are lines a person wrote in
the page, not guesses.

    From the index alone (how_url is not set); the commands whose pages match best:

    landmark -- ...
        landmark --home
        ...

## Setting up a model

Any server that speaks the OpenAI-style `/v1/chat/completions`, such
as llama-server or Ollama, running a small model.  Five settings, set
with `set` like any other:

    how_url        where the server is, with http:// in front; empty is no model
    how_model      the model's name; llama-server ignores it, Ollama needs it
    how_slot       a llama-server slot to use; empty is whichever is free
    how_timeout    how long to wait, like 45s; empty is two minutes
    how_extra      a JSON object added to every request

For Ollama on the same machine, with a model it has pulled:

    set how_url http://127.0.0.1:11434
    set how_model qwen3.5:4b-q4_K_M

Name a GGUF build, as above, and not the bare `qwen3.5:4b`.  `how` asks
the server for an answer in a fixed format (a JSON schema), and the
default tags of Ollama on a Mac are MLX builds, which refuse that with
`501 Not Implemented: structured output is unavailable`.  A tag ending
`-q4_K_M` or `-q8_0` is a GGUF build; `-mlx`, `-nvfp4` and `-mxfp8` are
MLX.  `ollama show MODEL` gives the quantization (nvfp4 is MLX) and
`ollama ps` the runner: "llamacpp" takes the format, "mlx" does not.
This was measured on a Mac with Ollama 0.40.0; it is not known what the
default tag is on other systems.

`how_extra` is for what one model needs and another would refuse.  A
model that reasons at length before it answers should be told not to.
Under Ollama, for Qwen3 and Qwen3.5:

    set how_extra '{"reasoning_effort": "none"}'

Under llama-server, whose documentation gives this form:

    set how_extra '{"chat_template_kwargs": {"enable_thinking": false}}'

The single quotes matter.  The shell takes double quotes off a word
the way it does anywhere else, and JSON without its double quotes is
not JSON, so `set` would refuse it.

Which options a model takes is the model's and the server's business,
so nothing of the kind is built in.  The slsh guide says which models
were measured, and how to share a llama-server that slbotd uses.

## Your own words

A file called `how-hints.tsv`, beside the settings file, teaches `how`
the words you use.  One hint per line: the words, a tab, and the
commands they mean.

    # my words for things
    snapshot picture	look texture
    stash	put

A question containing every word on the left brings the commands on
the right to the top.  Blank lines and lines starting with `#` are
skipped.  A line without a tab is an error naming the line; a command
name slsh does not have is left out, and `how` says so each time it
reads the file.

## Examples

    how do I make where I am standing my home
    how can I see which avatars are near me
    how -i do I take off an attachment
    how -r do I list what is in a folder

See also: `man` for a command's whole page, `help` for the commands
by group, and `set` for the settings.
