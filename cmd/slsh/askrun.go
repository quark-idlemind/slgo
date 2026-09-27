package main

// One question, from the index to what may be printed.
//
// askRun is the whole of ask apart from the printing: it finds the
// commands the index thinks could answer, turns them into what the
// model is shown (askprompt.go), asks, checks the answer (askcheck.go),
// gives the model one chance to mend what was refused, and hands back
// everything that happened -- what was shown, what came back, what was
// kept and what was refused and why.  The printing is ask.go's; keeping
// the two apart is what lets the eval measure exactly what a person
// would have been shown without scraping it off a screen.
//
// # Without a model
//
// A nil client is not a failure.  The index alone is most of the
// answer -- it names the commands, and their pages have the example
// lines -- so ask with no model configured, or with the model's server
// down, still answers, from the index alone.  When the model was asked
// and failed, askRun returns the error AND the result with its
// candidates filled in: a result beside the error is what tells the
// caller that the index is there to fall back on, and cmdAsk searches
// it again for the lines it prints (askPrintRetrieval).
//
// # The retry
//
// One, and only when the model's answer was refused outright: some
// suggestion was rejected and none survived.  An answer with one good
// suggestion and one invented one is an answer with one good
// suggestion; asking again would cost a second wait on a small model
// for the chance of an alternative, and the likelier outcome of
// pressing a model that has already guessed once is a second guess.
//
// # No second opinion
//
// A suggestion that passed the checks is not put back to the model to
// ask whether it does what was asked; that was tried, and it cut the
// questions answered right by half or more.  What guards against a real
// command offered for something it does not do is the model's own
// found, asked for first (askSchema), and honoured: suggestions written
// beside found=false are dropped (askToCheck).
// Why: doc/slsh.md#no-second-opinion-on-a-suggestion

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/internal/askindex"
	"github.com/quark-idlemind/slgo/internal/llm"
)

// askConfig is where the model is.  An empty URL is no model at all.
type askConfig struct {
	URL, Model string
	Slot       *int
	Timeout    time.Duration
}

// askResult is everything one question came to.
type askResult struct {
	Question     string
	Candidates   []askCandidate  // what was shown (or would be)
	Answer       *askAnswer      // nil when no model was asked
	Kept         []askSuggestion // survived the checks
	Rejected     []askRejection
	Retried      bool
	PromptTokens int // from the server, 0 if none
	Elapsed      time.Duration
}

const (
	// askCandidateCount is how many commands the model is shown before
	// askFit trims to the budget.  Eight is enough that the right
	// command is usually among them (the retrieval tests ask for top 8),
	// and few enough that a small model can read them all.
	askCandidateCount = 8

	// askExcerptCommands is how many of those carry the index's matching
	// excerpts, and askExcerptsEach how many excerpts each.  Every
	// candidate also gets the page's opening when that is not already
	// among them, and one with no matching page text gets a single
	// passage instead.  A usage line is not a sentence the model can
	// quote, and a decline is final.  askFit drops these passages first
	// when the budget is spent.
	askExcerptCommands = 3
	askExcerptsEach    = 3

	// askPassageMax is how long that one passage may be.  The first
	// paragraph, cut at a word if it is longer, so eight of them fit in
	// the budget that three full excerpts already use most of.
	askPassageMax = 480
)

// newAskClient is the client for c, or nil when no URL is set.
//
// extra is merged into every request, for what one model needs and
// another would refuse: a Qwen3-style model that reasons aloud before
// answering is told not to with
// {"chat_template_kwargs": {"enable_thinking": false}} under
// llama-server, and that is a fact about that model, so it lives in the
// setting rather than here.
//
// c.Timeout is for the whole question, however many requests it takes,
// and not for each request: askRun holds the question to the client's
// Timeout, and a question can be two requests -- the answer and the
// retry -- so without that how_timeout=45s could mean ninety.
func newAskClient(c askConfig, extra map[string]any) *llm.Client {
	if strings.TrimSpace(c.URL) == "" {
		return nil
	}
	return llm.New(llm.Options{
		URL:         c.URL,
		Model:       c.Model,
		Slot:        c.Slot,
		Timeout:     c.Timeout, // zero is llm.DefaultTimeout
		Temperature: 0,
		Extra:       extra,
	})
}

// askRun answers one question.  client nil means retrieval only.
//
// The error is the model's, or the index's.  A model error comes with
// the result as far as it got -- the candidates at least -- so that the
// caller knows it can still answer from the index; an index error comes
// with nil, since without the index there is nothing to answer from.
func askRun(ctx context.Context, client *llm.Client, question string, hints []askindex.Hint) (*askResult, error) {
	start := time.Now()
	res := &askResult{Question: question}
	defer func() { res.Elapsed = time.Since(start) }()

	hits, err := askSearch(question, hints...)
	if err != nil {
		return nil, err
	}
	res.Candidates = askCandidatesFrom(hits)
	if client == nil {
		return res, nil
	}
	// The client's timeout is for the whole question, retry and all;
	// see newAskClient.
	ctx, cancel := context.WithTimeout(ctx, client.Timeout())
	defer cancel()
	if len(res.Candidates) == 0 {
		// Nothing to show the model, and a model shown nothing can
		// only invent.  This is the answer it would have been told to
		// give.
		res.Answer = &askAnswer{Found: false}
		return res, nil
	}

	msgs, shown := askMessages(question, res.Candidates)
	res.Candidates = shown
	schema := askSchema(shown)

	reply, err := client.Chat(ctx, msgs, schema)
	if err != nil {
		return res, err
	}
	res.PromptTokens += reply.PromptTokens
	ans, err := parseAskReply(reply.Text)
	if err != nil {
		return res, err
	}
	res.Answer = &ans
	kept, rejected := filterAskAnswer(askToCheck(ans))
	res.Kept, res.Rejected = kept, rejected
	if len(rejected) > 0 && len(kept) == 0 {
		res.Retried = true
		reply2, err := client.Chat(ctx, askRetryMessages(msgs, reply.Text, rejected), schema)
		if err != nil {
			return res, fmt.Errorf("asking again: %w", err)
		}
		res.PromptTokens += reply2.PromptTokens
		ans2, err := parseAskReply(reply2.Text)
		if err != nil {
			return res, fmt.Errorf("asking again: %w", err)
		}
		res.Answer = &ans2
		kept, rejected = filterAskAnswer(askToCheck(ans2))
		res.Kept = kept
		res.Rejected = append(res.Rejected, rejected...)
	}

	return res, nil
}

// askToCheck is the answer as askRun checks it: with its
// suggestions taken away when the model said none of the commands does
// it, and with each line mended by askMendLine.
func askToCheck(a askAnswer) askAnswer {
	if !a.Found {
		a.Suggestions = nil
		return a
	}
	out := make([]askSuggestion, len(a.Suggestions))
	for i, s := range a.Suggestions {
		s.CommandLine = askMendLine(s)
		out[i] = s
	}
	a.Suggestions = out
	return a
}

// askMendLine is a suggestion's line with the one slip put right that
// can be put right without guessing: a line that leaves out the command
// and starts at its flags -- "--home" where "landmark --home" was meant
// -- when the suggestion names the command in its command field, which
// the schema holds to the commands shown.  A "slsh " in front, the
// shell's own name typed as if it were a command, comes off, and so does
// what askCleanLine takes off round the line.  Nothing else is touched,
// and the mended line goes through every check as if the model had
// written it so.
func askMendLine(s askSuggestion) string {
	line := askCleanLine(s.CommandLine)
	if rest, ok := strings.CutPrefix(line, "slsh "); ok {
		line = strings.TrimSpace(rest)
	}
	name := strings.TrimSpace(s.Command)
	if strings.HasPrefix(line, "-") && name != "" {
		if _, ok := commands[name]; ok {
			line = name + " " + line
		}
	}
	return line
}

// askCandidatesFrom turns the index's hits into what the model is
// shown, best first.
//
// Excerpts are the page's markdown as written, since the model quotes
// them and checkQuote looks the quote up in the page's source.  An
// option's paragraph goes under "Options" with its flag in front, the
// way the page has it; the example lines that matched are gathered
// into one "Examples" excerpt; a section keeps its heading.
func askCandidatesFrom(hits []askindex.CommandHit) []askCandidate {
	var out []askCandidate
	for _, h := range hits {
		if len(out) == askCandidateCount {
			break
		}
		c, ok := commands[h.Command]
		if !ok || askSkip(h.Command) {
			// The index is checked against the command table by a
			// test, so an unknown name is a build whose index is
			// stale; the command it names cannot be suggested either
			// way.
			continue
		}
		cand := askCandidate{Name: h.Command, Usage: c.usage(h.Command), Brief: c.brief}
		if len(out) < askExcerptCommands {
			cand.Excerpts = askExcerptsOf(h.Hits)
		}
		// The matching excerpts above skip the keyword line, so a
		// command found only by its keywords has nothing here yet.
		// One passage gives the model a sentence it is allowed to copy.
		if len(cand.Excerpts) == 0 {
			if ex, ok := askOnePassage(h.Command, h.Hits); ok {
				cand.Excerpts = []askExcerpt{ex}
			}
		}
		// The opening says what the command is.  A match can be a
		// paragraph about something beside that, and the model can
		// only quote what it is shown.
		cand.Excerpts = askWithOpening(h.Command, cand.Excerpts)
		out = append(out, cand)
	}
	return out
}

// askSkip is a command that is never an answer: how itself, by the name
// the command table gives it (howName), so that renaming it again cannot
// leave this behind.  Its page is full of questions and of other
// commands' lines, so it matches nearly every question asked, and "how"
// is never what somebody asking wanted to be told.
func askSkip(name string) bool { return name == howName }

// askWithOpening puts the page's opening first, unless the excerpts
// already contain one.  An intro is the excerpt with no heading.
func askWithOpening(command string, ex []askExcerpt) []askExcerpt {
	for _, e := range ex {
		if e.Heading == "" && strings.TrimSpace(e.Text) != "" {
			return ex
		}
	}
	opening, ok := askOnePassage(command, nil)
	if !ok {
		return ex
	}
	return append([]askExcerpt{opening}, ex...)
}

// askOnePassage is one quotable piece of a command's page: the best
// matching paragraph, or the page's opening when the match was only
// the keyword line (which is not on the page, and so cannot be quoted).
func askOnePassage(command string, hits []askindex.Hit) (askExcerpt, bool) {
	for _, h := range hits {
		if ex, ok := askPassageOf(h.Doc); ok {
			return ex, true
		}
	}
	if ix, err := askIndex(); err == nil {
		docs := ix.Docs()
		for i := range docs {
			if docs[i].Command == command && docs[i].Kind == askindex.KindIntro {
				return askPassageOf(&docs[i])
			}
		}
	}
	return askExcerpt{}, false
}

// askPassageOf is a document as one short excerpt, or false when the
// document is not a piece of the page.  The keyword line and the
// examples are not: one is not written on the page, and the other is a
// command line rather than a sentence about what the command does.
func askPassageOf(d *askindex.Doc) (askExcerpt, bool) {
	if d == nil {
		return askExcerpt{}, false
	}
	body := askShortPassage(d.Text)
	if body == "" {
		return askExcerpt{}, false
	}
	switch d.Kind {
	case askindex.KindOption:
		return askExcerpt{Heading: "Options", Text: "**" + d.Heading + "**\n\n" + body}, true
	case askindex.KindSection:
		return askExcerpt{Heading: d.Heading, Text: body}, true
	case askindex.KindIntro:
		return askExcerpt{Text: body}, true
	default:
		return askExcerpt{}, false
	}
}

// askShortPassage is the first paragraph of s, and no more than
// askPassageMax bytes of that, cut at a word.  A prefix of the page is
// still text the page contains, which is what a quotation has to be.
func askShortPassage(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if len(s) <= askPassageMax {
		return s
	}
	s = s[:askPassageMax]
	if i := strings.LastIndexByte(s, ' '); i > askPassageMax/2 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// askExcerptsOf is the excerpts for one command's matching documents.
func askExcerptsOf(hits []askindex.Hit) []askExcerpt {
	var out []askExcerpt
	var examples []string
	for _, h := range hits {
		d := h.Doc
		switch d.Kind {
		case askindex.KindExample:
			examples = append(examples, "    "+d.Text)
		case askindex.KindOption:
			if len(out) < askExcerptsEach {
				out = append(out, askExcerpt{Heading: "Options", Text: "**" + d.Heading + "**\n\n" + d.Text})
			}
		case askindex.KindSection:
			if len(out) < askExcerptsEach {
				out = append(out, askExcerpt{Heading: d.Heading, Text: d.Text})
			}
		case askindex.KindIntro:
			if len(out) < askExcerptsEach {
				out = append(out, askExcerpt{Text: d.Text})
			}
		}
	}
	if len(examples) > 0 {
		if len(examples) > 4 {
			examples = examples[:4]
		}
		ex := askExcerpt{Heading: "Examples", Text: strings.Join(examples, "\n")}
		if len(out) >= askExcerptsEach {
			out[len(out)-1] = ex
		} else {
			out = append(out, ex)
		}
	}
	return out
}

// askExampleLines is a command's example lines as its page writes
// them: the ones the question matched first, then the page's own, up to
// n.  For retrieval-only answers, where these are the nearest thing to
// a command line there is and are safe to print because a person wrote
// them.
func askExampleLines(name string, matched []askindex.Hit, n int) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] && len(out) < n {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, h := range matched {
		if h.Doc.Kind == askindex.KindExample {
			add(h.Doc.Text)
		}
	}
	if ix, err := askIndex(); err == nil && len(out) < n {
		for _, d := range ix.Docs() {
			if d.Command == name && d.Kind == askindex.KindExample {
				add(d.Text)
			}
		}
	}
	return out
}

// loadAskHints reads a hints file: one hint per line,
//
//	words<TAB>command [command...]
//
// meaning that a question with every one of those words in it raises
// those commands.  Blank lines and lines starting with # are skipped.
// A file that is not there is no hints and no error, since most people
// will never write one.  A line without a tab, or with nothing either
// side of it, is an error that names the line.
//
// A command name the shell does not have is left out of its hint
// rather than refused: a hints file outlives commands being renamed,
// and a shell that would not answer any question because of one stale
// line is worse than one that ignores the line.  readAskHints says
// which names were left out, for ask to warn about.
//
// A second name for a command -- exit for quit, unsit for stand -- is
// taken as the name the index files that command under (askIndexName),
// since a hint raises commands by the index's names and would otherwise
// raise nothing.
func loadAskHints(path string) ([]askindex.Hint, error) {
	hints, _, err := readAskHints(path)
	return hints, err
}

// readAskHints is loadAskHints, and also the unknown command names
// found, each as "line N: NAME".
func readAskHints(path string) (hints []askindex.Hint, unknown []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		words, names, ok := strings.Cut(line, "\t")
		when, cmds := strings.Fields(words), strings.Fields(names)
		if !ok || len(when) == 0 || len(cmds) == 0 {
			return nil, nil, fmt.Errorf("%s line %d: want words, a tab, and command names, got %q", path, n, line)
		}
		var known []string
		for _, c := range cmds {
			name, ok := askIndexName(c)
			switch {
			case !ok:
				unknown = append(unknown, fmt.Sprintf("line %d: %s", n, c))
			case !slices.Contains(known, name):
				known = append(known, name)
			}
		}
		if len(known) > 0 {
			hints = append(hints, askindex.Hint{When: when, Commands: known})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return hints, unknown, nil
}

// askIndexName is the name the index files the command called name
// under, which for a command with two names is the one its man page has
// (askCommands), and whether there is such a command at all.
func askIndexName(name string) (string, bool) {
	c, ok := commands[name]
	if !ok {
		return "", false
	}
	for _, ac := range askCommands() {
		if ac.c == c {
			return ac.name, true
		}
	}
	return name, true
}
