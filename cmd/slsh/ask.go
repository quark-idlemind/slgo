package main

// how: which command does this?
//
// A person who knows what they want and not what slsh calls it types
// the question as they would say it -- "how do I make this place my
// home" -- and is told the command, a line to type, and the sentence in
// that command's man page that says it does so.  askRun (askrun.go)
// does the work; this file is the command and what it prints.
//
// # Why the command is a question word
//
// It was called ask, and every question typed to it began "ask how do
// I", which is two words of ceremony before the one that says what the
// question is.  Called how, the command word IS the start of the
// question.  So it is put back in front before the question goes
// anywhere: the index drops it as a stopword either way, but the model
// is shown "how do I set my home", which is the question, rather than
// "do I set my home", which is a different one.  The Go names inside
// are still ask*, from before; they name the machinery, not the word
// typed.
//
// # What is printed, and what never is
//
// A suggested line is printed only after the checks in askcheck.go
// have passed it: the command exists, its flags are flags it takes,
// and the quote is in its page.  It is printed to be read and typed;
// nothing here runs it.  Nothing the model writes outside a checked
// suggestion is printed, because it is the part of the reply nothing
// checks, and a sentence like "use sethome" in it would be an invented
// command with the shell's authority behind it.  What is printed of the
// model's words is each kept suggestion's "why", beside a line and a
// quote that have been checked, flattened to one line with anything a
// terminal would act on taken out.
//
// # Without a model
//
// No how_url set, --index, or a server that does not answer: the
// commands the index found, each with its brief and the example lines
// from its page, which are safe to print because a person wrote them.
// The first line says which mode it is and why, so that an answer from
// the index is never mistaken for an answer from the model or the
// other way about.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/quark-idlemind/slgo/internal/askindex"
	"github.com/quark-idlemind/slgo/internal/llm"
)

// howName is the command's name, which is also the first word of every
// question put to it.
const howName = "how"

// howCommand is shared by both spellings in the table below.
var howCommand = &command{
	params:   "QUESTION...",
	flags:    func() any { return new(askOptions) },
	brief:    "which command does what you describe, with a line to type and its man page's words",
	keywords: "question ask which command does what describe find command search suggest words",
	man:      howName,
	run:      cmdAsk,
}

// "How" is here as well as "how" because what is typed after it is a
// sentence, and a person typing a sentence starts it with a capital.
// Every other command is a word somebody learned to type in lower case;
// this one is the one they type without having learned anything, and
// "How: no such command" is the answer it exists to prevent.  It is
// the one spelling allowed, not case-insensitive lookup, which would
// make "LS" a command too.
const howCapitalised = "How"

var askCommandTable = map[string]*command{
	howName:        howCommand,
	howCapitalised: howCommand,
}

type askOptions struct {
	Index    bool `getopt:"--index -i  answer from the index alone, without asking the model"`
	Rejected bool `getopt:"--rejected -r  also say what the model suggested that the checks refused, and why"`
	Help     bool `getopt:"--help -h  show what this command takes"`
}

// askRetrievalShown is how many commands an answer from the index
// lists, and askNearestShown how many a "not found" names.
const (
	askRetrievalShown = 5
	askNearestShown   = 3
	askExamplesShown  = 3
)

func cmdAsk(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o askOptions
	args, done, err := subOptions(howName, &o, out, args)
	if err != nil || done {
		return err
	}
	question := howQuestion(args)
	if question == "" {
		return usageError(howName, "how to do what?")
	}

	hints, err := askUserHints(out)
	if err != nil {
		return err
	}

	ac, extra, err := askConfigOf(sh.cfg)
	if err != nil {
		return err
	}
	client := newAskClient(ac, extra)
	why := ""
	switch {
	case o.Index:
		client, why = nil, "--index"
	case client == nil:
		why = "how_url is not set"
	}

	res, err := askRun(ctx, client, question, hints)
	if res == nil {
		return err
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		why = askWhyNoModel(ac.URL, err)
		client = nil
	}
	if client == nil {
		askPrintRetrieval(out, question, why, hints)
		return nil
	}
	askPrintAnswer(out, res, o.Rejected)
	return nil
}

// howQuestion is the question as it was asked: the command's own word
// and then the rest of the line.  See the head of this file.  Nothing
// after the word is nothing asked.
func howQuestion(args []string) string {
	rest := strings.TrimSpace(strings.Join(args, " "))
	if rest == "" {
		return ""
	}
	return howName + " " + rest
}

// askConfigOf is the settings as askRun wants them.
func askConfigOf(cfg Config) (askConfig, map[string]any, error) {
	extra, err := askParseExtra(cfg.AskExtra)
	if err != nil {
		return askConfig{}, nil, fmt.Errorf("how_extra: %w", err)
	}
	return askConfig{URL: cfg.AskURL, Model: cfg.AskModel, Slot: cfg.AskSlot, Timeout: cfg.AskTimeout}, extra, nil
}

// askParseExtra reads how_extra, which is empty or a JSON object.
func askParseExtra(s string) (map[string]any, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("want a JSON object, like {\"reasoning_effort\": \"none\"}: %v", err)
	}
	return m, nil
}

// askCheckURL is whether how_url is an address the client can ask.
//
// Empty is no model, which is allowed.  Anything else must say http://
// or https:// in front, because the address a server prints when it
// starts -- Ollama's "Listening on 127.0.0.1:11434" -- has none, and
// taken as it is that is refused on every question with a complaint
// about a scheme or a colon, long after the setting that caused it was
// typed.  A space is refused too: an address has none, and one here is
// most likely a "# comment" left after the value in the settings file.
func askCheckURL(s string) error {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(s, " \t") {
		return fmt.Errorf("want the server's address with http:// in front, like http://127.0.0.1:8080, got %q", s)
	}
	return nil
}

// howHintsName is the hints file's name, in the settings directory.
const howHintsName = "how-hints.tsv"

// howHintsPath is where the hints file is looked for: beside the
// settings file, since it is the same kind of thing -- this person's
// own words for how they use the shell.
func howHintsPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, howHintsName), nil
}

// askUserHints is the hints file, with a line said about each command
// name in it that the shell does not have.
func askUserHints(out io.Writer) ([]askindex.Hint, error) {
	path, err := howHintsPath()
	if err != nil {
		return nil, nil
	}
	hints, unknown, err := readAskHints(path)
	if err != nil {
		return nil, err
	}
	for _, u := range unknown {
		fmt.Fprintf(out, "%s %s: no such command, left out\n", path, u)
	}
	return hints, nil
}

// askWhyNoModel says in a line why the model was not heard from.
func askWhyNoModel(url string, err error) string {
	var se *llm.StatusError
	switch {
	case errors.Is(err, llm.ErrUnreachable):
		return "nothing is answering at " + url
	case errors.Is(err, llm.ErrTimeout):
		return "the model at " + url + " did not answer in time"
	case errors.As(err, &se):
		return "the model at " + url + " refused: " + askOneLine(err.Error())
	}
	return "the model's answer could not be used: " + askOneLine(err.Error())
}

// askPrintAnswer is a checked answer from the model.
func askPrintAnswer(out io.Writer, res *askResult, rejected bool) {
	if len(res.Kept) == 0 {
		fmt.Fprintln(out, "No slsh command found for that.")
		var near []askCandidate
		for _, c := range res.Candidates {
			if !howNeverAnswer(c.Name) {
				near = append(near, c)
			}
		}
		if len(near) > 0 {
			fmt.Fprintln(out, "The nearest are:")
			askPrintBriefs(out, near, askNearestShown)
		}
	} else {
		for i, s := range res.Kept {
			if i > 0 {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "to type:  %s\n", s.CommandLine)
			if why := askOneLine(s.Why); why != "" {
				fmt.Fprintf(out, "  %s\n", why)
			}
			fmt.Fprintf(out, "  %q -- %s\n", askOneLine(s.Quote), askSourceOf(s))
		}
	}
	if rejected && len(res.Rejected) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Refused by the checks (not commands; do not type these):")
		for _, r := range res.Rejected {
			fmt.Fprintf(out, "  %q\n", r.Suggestion.CommandLine)
			for _, why := range r.Reasons {
				fmt.Fprintf(out, "    %s\n", askOneLine(why))
			}
		}
	}
}

// askSourceOf names where a kept suggestion's quote came from: the man
// page when the command has one, which checkQuote looks in first, and
// otherwise the command's own --help.
func askSourceOf(s askSuggestion) string {
	name := askFirstWord(s.CommandLine)
	if c, ok := commands[name]; ok && c.man != "" {
		return "man " + c.man
	}
	return name + " --help"
}

// howNeverAnswer is a command this one never offers: itself, whose page
// is full of questions and matches nearly every one, and whatever else
// askSkip says.
func howNeverAnswer(name string) bool {
	return commands[name] == commands[howName] || askSkip(name)
}

// askPrintRetrieval is an answer from the index alone.
func askPrintRetrieval(out io.Writer, question, why string, hints []askindex.Hint) {
	hits, err := askSearch(question, hints...)
	if err != nil || len(hits) == 0 {
		fmt.Fprintf(out, "From the index alone (%s): nothing in slsh's pages matches those words.\n", why)
		return
	}
	fmt.Fprintf(out, "From the index alone (%s); the commands whose pages match best:\n", why)
	shown := 0
	for _, h := range hits {
		c, ok := commands[h.Command]
		if !ok || howNeverAnswer(h.Command) {
			continue
		}
		if shown == askRetrievalShown {
			break
		}
		shown++
		fmt.Fprintf(out, "\n%s -- %s\n", h.Command, c.brief)
		ex := askExampleLines(h.Command, h.Hits, askExamplesShown)
		if len(ex) == 0 {
			fmt.Fprintf(out, "    %s\n", c.usage(h.Command))
		}
		for _, e := range ex {
			fmt.Fprintf(out, "    %s\n", e)
		}
	}
}

// askPrintBriefs is the first n candidates by name and brief.
func askPrintBriefs(out io.Writer, cands []askCandidate, n int) {
	width := 0
	for i, c := range cands {
		if i < n && len(c.Name) > width {
			width = len(c.Name)
		}
	}
	for i, c := range cands {
		if i == n {
			break
		}
		fmt.Fprintf(out, "  %-*s  %s\n", width, c.Name, c.Brief)
	}
}

// askOneLine flattens model text to one line of plain characters.
//
// A newline in a "why" would make the next line look like something how
// printed, and an escape sequence would do more than that: move the
// cursor, rub out the line above, or rewrite the command a person is
// about to copy.  A model can put either in a JSON string with a
// backslash, so every control character, and every formatting one --
// which includes those that turn text round on the screen -- becomes a
// space before the spaces are collapsed.
func askOneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.In(r, unicode.Cc, unicode.Cf) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
