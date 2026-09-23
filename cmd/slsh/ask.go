package main

// ask: which command does this?
//
// A person who knows what they want and not what slsh calls it types
// the question as they would say it -- "ask how do I make this place my
// home" -- and is told the command, a line to type, and the sentence in
// that command's man page that says it does so.  askRun (askrun.go)
// does the work; this file is the command and what it prints.
//
// # What is printed, and what never is
//
// A suggested line is printed only after the checks in askcheck.go
// have passed it: the command exists, its flags are flags it takes,
// and the quote is in its page.  It is printed to be read and typed;
// nothing here runs it.  The model's own free-text answer is not
// printed at all, because it is the one part of the reply nothing
// checks, and a sentence like "use sethome" in it would be an invented
// command with the shell's authority behind it.  What is printed of
// the model's words is each kept suggestion's "why", beside a line and
// a quote that have been checked.
//
// # Without a model
//
// No ask_url set, --index, or a server that does not answer: the
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
	"strings"

	"github.com/quark-idlemind/slgo/internal/askindex"
	"github.com/quark-idlemind/slgo/internal/llm"
)

var askCommandTable = map[string]*command{
	"ask": {
		params:   "QUESTION...",
		flags:    func() any { return new(askOptions) },
		brief:    "which command does what you describe, with a line to type and its man page's words",
		keywords: "question how do i which command what does help find command search suggest",
		man:      "ask",
		run:      cmdAsk,
	},
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
	args, done, err := subOptions("ask", &o, out, args)
	if err != nil || done {
		return err
	}
	question := strings.TrimSpace(strings.Join(args, " "))
	if question == "" {
		return usageError("ask", "ask what?")
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
		why = "ask_url is not set"
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

// askConfigOf is the settings as askRun wants them.
func askConfigOf(cfg Config) (askConfig, map[string]any, error) {
	extra, err := askParseExtra(cfg.AskExtra)
	if err != nil {
		return askConfig{}, nil, fmt.Errorf("ask_extra: %w", err)
	}
	return askConfig{URL: cfg.AskURL, Model: cfg.AskModel, Slot: cfg.AskSlot, Timeout: cfg.AskTimeout}, extra, nil
}

// askParseExtra reads ask_extra, which is empty or a JSON object.
func askParseExtra(s string) (map[string]any, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("want a JSON object, like {\"chat_template_kwargs\": {\"enable_thinking\": false}}: %v", err)
	}
	return m, nil
}

// askUserHints is the hints file, with a line said about each command
// name in it that the shell does not have.
func askUserHints(out io.Writer) ([]askindex.Hint, error) {
	path, err := askHintsPath()
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
		return "the model at " + url + " refused: " + err.Error()
	}
	return "the model's answer could not be used: " + err.Error()
}

// askPrintAnswer is a checked answer from the model.
func askPrintAnswer(out io.Writer, res *askResult, rejected bool) {
	if len(res.Kept) == 0 {
		fmt.Fprintln(out, "No slsh command found for that.")
		if len(res.Candidates) > 0 {
			fmt.Fprintln(out, "The nearest are:")
			askPrintBriefs(out, res.Candidates, askNearestShown)
		}
	} else {
		for i, s := range res.Kept {
			if i > 0 {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "to type:  %s\n", s.CommandLine)
			if why := strings.TrimSpace(s.Why); why != "" {
				fmt.Fprintf(out, "  %s\n", askOneLine(why))
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
		if !ok || askSkip(h.Command) {
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

// askOneLine flattens model text to one line, so that a newline in a
// "why" cannot make the next line look like something ask printed.
func askOneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
