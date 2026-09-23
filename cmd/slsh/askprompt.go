package main

// What ask says to the model, and what it makes of the answer.
//
// The model is small on purpose -- a few billion parameters, on the
// same machine as everything else -- and a small model asked about a
// program it has never seen will describe a plausible program instead.
// So it is never asked what slsh can do.  It is shown a handful of
// commands the index found, in the words of their own usage lines and
// man pages, and asked which of THOSE does what the person wants; and
// what it says is then checked against the same pages (askcheck.go)
// before a word of it is printed.  The prompt is written to make the
// checks pass by being honest rather than by being clever: it says that
// "no such command" is a good answer, that a flag not shown does not
// exist, and that the evidence has to be a quotation rather than a
// summary, because a summary is where an invented flag hides.
//
// # Two messages, and which one changes
//
// The instructions are the system message and never change; the
// commands and the question are the user message and change every time.
// That split is for llama-server's prompt cache: a slot that has seen
// the same system message before does not process it again, so the part
// that is the same on every ask costs nothing after the first.  (That
// is how the cache works in general; how much it saves here has not
// been measured.)
//
// # The budget
//
// About three thousand tokens of prompt, counted as four characters to
// a token.  That ratio is the usual rule of thumb for English and not a
// measurement of any tokeniser; the server's own count comes back with
// every reply (llm.Reply.PromptTokens), and that is the number to
// correct this one by.  The budget is small because the models this is
// for are small, and small models are generally reported to use a long
// context worse than a short one well before their window is full --
// the usual expectation, not something measured here; the eval in phase
// 2 is where the number gets tested.  The order things are thrown away
// in matters more than the number.  Excerpts go first, from the command the
// index liked least upwards, since the index's ranking is the only
// opinion there is about which of them the answer is in; then, if even
// the bare usage lines will not fit, whole commands from the bottom.
// The top command always stays, with as much of its first excerpt as
// fits.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/quark-idlemind/slgo/internal/llm"
)

// askCandidate is one command the index offered, in the words the model
// is shown.  Phase 2 converts the index's hits into these, best first.
type askCandidate struct {
	Name     string       // command name
	Usage    string       // usage line as usageLine composes it
	Brief    string       // the command's brief
	Excerpts []askExcerpt // may be empty
}

// askExcerpt is one piece of a command's man page.
//
// Text should be the page's markdown as it is written, not as md
// renders it for a terminal: the model is asked to quote it, and
// checkQuote looks the quotation up in the page's source.  Emphasis and
// backticks are forgiven on the way (see quoteNormal), but a table drawn
// in box characters or a paragraph re-wrapped to 78 columns with its
// words hyphenated would be text the page does not contain.
type askExcerpt struct {
	Heading string // "Options", "Examples", a section heading, or ""
	Text    string
}

// askAnswer is the reply asked for, as the schema below describes it.
type askAnswer struct {
	Found       bool            `json:"found"`
	Answer      string          `json:"answer"`
	Suggestions []askSuggestion `json:"suggestions"`
}

// askSuggestion is one command the model proposes.  Nothing in it is
// trusted until filterAskAnswer has been through it.
type askSuggestion struct {
	Command     string `json:"command"`
	CommandLine string `json:"command_line"`
	Why         string `json:"why"`
	Quote       string `json:"quote"`
}

const (
	// askBudgetTokens is the whole prompt: instructions, commands and
	// question.  See the head of this file.
	askBudgetTokens = 3000

	// askCharsPerToken is the estimate the budget is kept by.
	askCharsPerToken = 4

	// askMaxSuggestions is how many the schema allows.  A person asking
	// how to do one thing wants the one way and perhaps an alternative;
	// a model allowed ten will find ten, and the tail of the list is
	// where the guessing is.
	askMaxSuggestions = 3
)

// askEstimateTokens is the budget's measure of a string.
func askEstimateTokens(s string) int {
	return (len(s) + askCharsPerToken - 1) / askCharsPerToken
}

// askSystem is the instructions.  Constant, so that it is a prefix a
// server can cache; see the head of this file.
//
// Every rule in it is one a check enforces afterwards, which is the
// point of writing it down: a model that follows it has its answers
// printed, and one that does not has them thrown away.  The asking is
// only so that less is thrown away.
const askSystem = `You answer questions about slsh, a command shell that drives a Second Life avatar.  The person asking wants to know which slsh command does what they describe, and how to type it.

You are shown some slsh commands.  Each has its usage line, a one-line description, and sometimes excerpts from its manual page.  For this answer, those are the only commands that exist.

Rules:
- Suggest only commands that are shown, and only flags that appear in that command's usage line or excerpts.  Never make up a command, a flag, or what a flag does.
- command_line is one line the person could type.  It starts with the command's name, and flags come before everything else.  Where the person has to fill something in, write the placeholder the usage line uses, in capitals, such as NAME or PATH.  Do not make up names.
- quote is copied word for word from that same command's usage line, description or excerpts, and shows that it does what you say.  At least five words.  Do not change, shorten or join words in it.
- If none of the shown commands does what was asked, set found to false, give no suggestions, and say in answer that slsh has no command for that.  That is a good answer.  A wrong command is a bad one.
- At most three suggestions, best first.  answer is one or two plain sentences.

Reply with JSON only, in this shape:
{"found": true, "answer": "...", "suggestions": [{"command": "...", "command_line": "...", "why": "...", "quote": "..."}]}`

// askMessages is the prompt for one question: the instructions, and the
// commands with the question after them.
//
// The candidates are trimmed to the budget here, so what the model is
// shown is exactly what the returned slice says -- a caller that wants
// to know what the model saw looks at it rather than at what it passed
// in.
func askMessages(question string, cands []askCandidate) ([]llm.Message, []askCandidate) {
	cands = askFit(question, cands, askBudgetTokens)
	return []llm.Message{
		{Role: "system", Content: askSystem},
		{Role: "user", Content: askUser(question, cands)},
	}, cands
}

// askUser is the user message: every command shown, then the question.
//
// The question comes last because it is what the answer follows on
// from, and because models are commonly found to attend best to the end
// of a long prompt -- the usual advice, not a measurement of these
// models.  The
// excerpts are fenced with triple quotes so that a page's own "## "
// headings and indented examples cannot be mistaken for the prompt's
// structure.
func askUser(question string, cands []askCandidate) string {
	var b strings.Builder
	b.WriteString("Commands:\n")
	for _, c := range cands {
		fmt.Fprintf(&b, "\nCOMMAND %s\n", c.Name)
		fmt.Fprintf(&b, "usage: %s\n", c.Usage)
		fmt.Fprintf(&b, "description: %s\n", c.Brief)
		for _, e := range c.Excerpts {
			if e.Heading != "" {
				fmt.Fprintf(&b, "from its manual, %s:\n", e.Heading)
			} else {
				b.WriteString("from its manual:\n")
			}
			fmt.Fprintf(&b, "\"\"\"\n%s\n\"\"\"\n", strings.TrimSpace(e.Text))
		}
	}
	fmt.Fprintf(&b, "\nQuestion: %s\n", strings.TrimSpace(question))
	return b.String()
}

// askFit trims the candidates until the whole prompt fits the budget.
// The candidates passed in are not changed.
func askFit(question string, in []askCandidate, budget int) []askCandidate {
	cands := make([]askCandidate, len(in))
	for i, c := range in {
		c.Excerpts = append([]askExcerpt(nil), c.Excerpts...)
		cands[i] = c
	}
	fits := func() bool {
		return askEstimateTokens(askSystem)+askEstimateTokens(askUser(question, cands)) <= budget
	}

	// Excerpts from the bottom up, the last excerpt of each first.  The
	// very last excerpt left anywhere -- the top command's first -- is
	// shortened a line at a time before it is given up, because it is
	// the index's best guess at where the answer is and part of it is
	// worth more than none.
	for i := len(cands) - 1; i >= 0 && !fits(); i-- {
		for len(cands[i].Excerpts) > 0 && !fits() {
			n := len(cands[i].Excerpts) - 1
			if i > 0 || n > 0 {
				cands[i].Excerpts = cands[i].Excerpts[:n]
				continue
			}
			text := strings.TrimSpace(cands[i].Excerpts[0].Text)
			cut := strings.LastIndexByte(text, '\n')
			if cut <= 0 {
				cands[i].Excerpts = nil
				break
			}
			cands[i].Excerpts[0].Text = text[:cut]
		}
	}

	// Then whole commands, keeping the first whatever it costs: a
	// prompt with no command in it has nothing to ask about.
	for len(cands) > 1 && !fits() {
		cands = cands[:len(cands)-1]
	}
	return cands
}

// askSchema is the shape the reply must have, for the server to enforce
// as a grammar.
//
// command is an enum of the names shown, which is the one check that
// can be made before the model speaks rather than after: a server that
// honours the schema cannot produce a command outside the list at all.
// command_line cannot be constrained the same way -- its flags and
// operands are free text as far as a schema goes -- which is why
// checkCommandLine exists.
func askSchema(cands []askCandidate) *llm.Schema {
	command := map[string]any{"type": "string"}
	if len(cands) > 0 {
		names := make([]string, 0, len(cands))
		for _, c := range cands {
			names = append(names, c.Name)
		}
		command["enum"] = names
	}
	suggestion := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":      command,
			"command_line": map[string]any{"type": "string"},
			"why":          map[string]any{"type": "string"},
			"quote":        map[string]any{"type": "string"},
		},
		"required":             []string{"command", "command_line", "why", "quote"},
		"additionalProperties": false,
	}
	most := askMaxSuggestions
	if len(cands) == 0 {
		most = 0
	}
	return &llm.Schema{
		Name: "slsh_answer",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"found":       map[string]any{"type": "boolean"},
				"answer":      map[string]any{"type": "string"},
				"suggestions": map[string]any{"type": "array", "items": suggestion, "maxItems": most},
			},
			"required":             []string{"found", "answer", "suggestions"},
			"additionalProperties": false,
		},
	}
}

// askRetryMessages is the one second chance: the conversation so far,
// the reply that was given, and a message saying what was wrong with
// it.
//
// Each refusal is quoted in the words the shell itself would have used
// -- "ls: unknown option: --long" -- with the command's real usage line
// under it, so that the model is corrected by the same text a person
// would have been.  It is also told, again, that dropping a suggestion
// or answering "not found" is acceptable: a model pressed to fix an
// invented flag will otherwise invent a second one.
func askRetryMessages(msgs []llm.Message, reply string, rejected []askRejection) []llm.Message {
	var b strings.Builder
	b.WriteString("Some of those suggestions were wrong, and were not shown to the person:\n")
	for _, r := range rejected {
		line := strings.TrimSpace(r.Suggestion.CommandLine)
		if line == "" {
			line = "(no command line)"
		}
		fmt.Fprintf(&b, "\n- %s\n", line)
		for _, why := range r.Reasons {
			fmt.Fprintf(&b, "  %s\n", strings.ReplaceAll(why, "\n", "\n  "))
		}
		if u := askUsageFor(r.Suggestion); u != "" {
			fmt.Fprintf(&b, "  the real usage is: %s\n", u)
		}
	}
	b.WriteString("\nAnswer again with the same JSON.  Use only commands and flags shown above, " +
		"and copy each quote word for word from that command's text.  " +
		"If you cannot fix a suggestion, leave it out.  " +
		"If no shown command does it, set found to false and give no suggestions; that is a good answer.")

	out := make([]llm.Message, 0, len(msgs)+2)
	out = append(out, msgs...)
	out = append(out,
		llm.Message{Role: "assistant", Content: reply},
		llm.Message{Role: "user", Content: b.String()})
	return out
}

// askUsageFor is the usage line of whichever real command a rejected
// suggestion meant, or "" when it named none.
func askUsageFor(s askSuggestion) string {
	for _, name := range []string{askFirstWord(s.CommandLine), strings.TrimSpace(s.Command)} {
		if c, ok := commands[name]; ok && name != "" {
			return c.usage(name)
		}
	}
	return ""
}

// askFirstWord is the command a line runs, as the shell would split it,
// or "" for a line the shell would refuse to split.
func askFirstWord(line string) string {
	words, _, _, err := parse(askCleanLine(line))
	if err != nil || len(words) == 0 {
		return ""
	}
	return words[0]
}

// parseAskReply reads the model's reply into an askAnswer.
//
// A server enforcing the schema hands back bare JSON and none of the
// forgiveness below is needed.  It is here for the server that does
// not, and for models that were trained to be helpful about it: a
// ```json fence round the object, a sentence before it, or a <think>
// block in front from a model that reasons aloud before answering.  All
// of that is wrapping and is taken off; what is inside still has to be
// the object asked for, and an object with the wrong shape is an error
// rather than an empty answer, so that "the model said nothing useful"
// and "the model said there is no such command" stay two things.
func parseAskReply(text string) (askAnswer, error) {
	s := text
	// A reasoning block, closed or (cut off) not.
	if i := strings.Index(s, "<think>"); i >= 0 {
		if j := strings.Index(s[i:], "</think>"); j >= 0 {
			s = s[:i] + s[i+j+len("</think>"):]
		} else {
			s = s[:i]
		}
	}
	// A fence: whatever is between the first ``` line and the next.
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:] // past "json" or whatever the fence was labelled
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			rest = rest[:j]
		}
		s = rest
	}
	// Anything round the object.
	lo, hi := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if lo < 0 || hi < lo {
		return askAnswer{}, fmt.Errorf("the model's reply has no JSON object in it: %s", askExcerptOf(text))
	}
	s = s[lo : hi+1]

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return askAnswer{}, fmt.Errorf("the model's reply is not valid JSON (%v): %s", err, askExcerptOf(text))
	}
	for _, k := range []string{"found", "suggestions"} {
		if _, ok := raw[k]; !ok {
			return askAnswer{}, fmt.Errorf("the model's reply has no %q: %s", k, askExcerptOf(text))
		}
	}
	var a askAnswer
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		return askAnswer{}, fmt.Errorf("the model's reply is not the shape asked for (%v): %s", err, askExcerptOf(text))
	}
	return a, nil
}

// askExcerptOf is enough of a reply to recognise it by in an error.
func askExcerptOf(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "..."
	}
	return fmt.Sprintf("%q", s)
}
