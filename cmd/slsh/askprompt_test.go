package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/internal/llm"
)

// askTestCandidate is a real command as phase 2 will hand it over: the
// usage line and brief from the table, and an excerpt or two.
func askTestCandidate(t *testing.T, name string, excerpts ...askExcerpt) askCandidate {
	t.Helper()
	c, ok := commands[name]
	if !ok {
		t.Fatalf("no command %s", name)
	}
	return askCandidate{Name: name, Usage: c.usage(name), Brief: c.brief, Excerpts: excerpts}
}

func TestThePromptShowsTheCommandsAndEndsWithTheQuestion(t *testing.T) {
	cands := []askCandidate{
		askTestCandidate(t, "landmark", askExcerpt{Heading: "Options", Text: "**--set-home**\n\nMake where this avatar is standing the place home is."}),
		askTestCandidate(t, "tp"),
	}
	msgs, shown := askMessages("how do I set my home", cands)
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("messages %+v", msgs)
	}
	if msgs[0].Content != askSystem {
		t.Errorf("the system message is not the constant one, so it cannot be cached")
	}
	if len(shown) != 2 || len(shown[0].Excerpts) != 1 {
		t.Errorf("something was trimmed from a prompt well under budget: %+v", shown)
	}
	u := msgs[1].Content
	for _, want := range []string{
		"COMMAND landmark\n",
		"usage: " + commands["landmark"].usage("landmark") + "\n",
		"description: " + commands["landmark"].brief + "\n",
		"from its manual, Options:\n\"\"\"\n**--set-home**",
		"COMMAND tp\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("user message lacks %q:\n%s", want, u)
		}
	}
	if !strings.HasSuffix(u, "Question: how do I set my home\n") {
		t.Errorf("the question is not last:\n%s", u)
	}
	if strings.Index(u, "COMMAND landmark") > strings.Index(u, "COMMAND tp") {
		t.Errorf("the order the index gave was not kept")
	}
}

// The instructions say the things the checks will hold the model to.
// Not a test of wording -- a test that nobody editing the prompt drops
// one of the rules the checker enforces without noticing.
func TestTheInstructionsSayWhatTheChecksEnforce(t *testing.T) {
	for _, want := range []string{
		"found to false",        // "not found" is allowed
		"That is a good answer", // and said to be good
		"Never make up a command, a flag",
		"word for word",
		"flags come before everything else",
		`"command_line"`, `"quote"`, `"found"`, `"suggestions"`,
	} {
		if !strings.Contains(askSystem, want) {
			t.Errorf("the instructions no longer say %q", want)
		}
	}
}

// Over budget, the lowest-ranked excerpts go first, then the top
// command's first excerpt is shortened, then whole commands go from the
// bottom, and the top command always stays.
func TestTheBudgetTrimsFromTheBottomUp(t *testing.T) {
	long := func(tag string, lines int) askExcerpt {
		var b strings.Builder
		for i := 0; i < lines; i++ {
			fmt.Fprintf(&b, "%s line %d of an excerpt that goes on for a while.\n", tag, i)
		}
		return askExcerpt{Heading: tag, Text: b.String()}
	}
	cands := []askCandidate{
		askTestCandidate(t, "landmark", long("top-a", 80), long("top-b", 80)),
		askTestCandidate(t, "tp", long("second", 80)),
		askTestCandidate(t, "where", long("third", 80)),
	}
	before := fmt.Sprint(cands)
	tokens := func(cs []askCandidate) int {
		return askEstimateTokens(askSystem) + askEstimateTokens(askUser("q", cs))
	}
	if tokens(cands) <= askBudgetTokens {
		t.Fatalf("the test's candidates fit already (%d tokens)", tokens(cands))
	}

	// Enough room for about one long excerpt: the third's and second's go,
	// and the top command's second, and its first survives.
	room := tokens(cands) - askEstimateTokens(long("x", 80).Text)*3 + 20
	got := askFit("q", cands, room)
	if fmt.Sprint(cands) != before {
		t.Errorf("askFit changed what it was given")
	}
	if n := tokens(got); n > room {
		t.Errorf("%d tokens, over the budget of %d", n, room)
	}
	if len(got) != 3 {
		t.Fatalf("a command went while excerpts could still go: %d left", len(got))
	}
	if len(got[2].Excerpts) != 0 || len(got[1].Excerpts) != 0 {
		t.Errorf("lower commands kept excerpts: %+v %+v", got[1].Excerpts, got[2].Excerpts)
	}
	if len(got[0].Excerpts) != 1 || got[0].Excerpts[0].Heading != "top-a" {
		t.Errorf("the top command's first excerpt should be what is left: %+v", got[0].Excerpts)
	}

	// Less room: the top command's excerpt is shortened by whole lines,
	// from the end.
	room = tokens(got) - askEstimateTokens(long("x", 20).Text)
	got2 := askFit("q", cands, room)
	if len(got2) != 3 || len(got2[0].Excerpts) != 1 {
		t.Fatalf("got %+v", got2)
	}
	text := got2[0].Excerpts[0].Text
	if !strings.HasPrefix(text, "top-a line 0 ") || strings.Contains(text, "line 79") {
		t.Errorf("shortened wrongly: %q", text)
	}
	if !strings.HasSuffix(text, "for a while.") {
		t.Errorf("cut in the middle of a line: %q", text)
	}

	// No room at all: the top command, bare, and nothing else.
	got3 := askFit("q", cands, 1)
	if len(got3) != 1 || got3[0].Name != "landmark" || len(got3[0].Excerpts) != 0 {
		t.Errorf("got %+v", got3)
	}
}

// The real budget holds for eight real commands with their whole pages
// as excerpts, which is far more than phase 2 will ever pass.
func TestEightWholePagesFitTheBudget(t *testing.T) {
	var cands []askCandidate
	for _, name := range []string{"landmark", "tp", "where", "ls", "find", "wear", "put", "touch"} {
		page, err := manRead(commands[name].man)
		if err != nil {
			t.Fatal(err)
		}
		cands = append(cands, askTestCandidate(t, name, askExcerpt{Text: page}))
	}
	msgs, shown := askMessages("how do I set my home", cands)
	n := 0
	for _, m := range msgs {
		n += askEstimateTokens(m.Content)
	}
	if n > askBudgetTokens {
		t.Errorf("%d tokens, over %d", n, askBudgetTokens)
	}
	if len(shown) != 8 {
		t.Errorf("%d commands shown; the bare usage lines of eight should fit", len(shown))
	}
	if len(shown[0].Excerpts) != 1 || shown[0].Excerpts[0].Text == "" {
		t.Errorf("the top command lost all of its page")
	}
}

func TestTheSchemaNamesOnlyTheCommandsShown(t *testing.T) {
	s := askSchema([]askCandidate{{Name: "landmark"}, {Name: "tp"}})
	b, err := json.Marshal(s.Schema)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Required   []string `json:"required"`
		Properties struct {
			Suggestions struct {
				MaxItems int `json:"maxItems"`
				Items    struct {
					Required   []string `json:"required"`
					Properties struct {
						Command struct {
							Enum []string `json:"enum"`
						} `json:"command"`
					} `json:"properties"`
					Additional bool `json:"additionalProperties"`
				} `json:"items"`
			} `json:"suggestions"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got.Required) != "[found answer suggestions]" {
		t.Errorf("required %v", got.Required)
	}
	sg := got.Properties.Suggestions
	if sg.MaxItems != askMaxSuggestions {
		t.Errorf("maxItems %d", sg.MaxItems)
	}
	if fmt.Sprint(sg.Items.Properties.Command.Enum) != "[landmark tp]" {
		t.Errorf("enum %v", sg.Items.Properties.Command.Enum)
	}
	if fmt.Sprint(sg.Items.Required) != "[command command_line why quote]" {
		t.Errorf("suggestion required %v", sg.Items.Required)
	}

	// The field names the schema asks for are the ones the reply is read
	// with, or every answer would parse as empty.
	a, err := parseAskReply(`{"found":true,"answer":"a","suggestions":[{"command":"tp","command_line":"tp home","why":"w","quote":"q"}]}`)
	if err != nil || len(a.Suggestions) != 1 || a.Suggestions[0].CommandLine != "tp home" || a.Suggestions[0].Quote != "q" {
		t.Errorf("got %+v, %v", a, err)
	}
}

func TestParseAskReplyForgivesWrapping(t *testing.T) {
	const obj = `{"found": true, "answer": "Use landmark.", "suggestions": [{"command": "landmark", "command_line": "landmark --set-home", "why": "sets home", "quote": "Make where this avatar is standing the place home is."}]}`
	for name, text := range map[string]string{
		"bare":            obj,
		"fenced":          "```json\n" + obj + "\n```",
		"fenced, no tag":  "```\n" + obj + "\n```",
		"with a sentence": "Here is the answer:\n" + obj + "\nHope that helps.",
		"after thinking":  "<think>\nThe person wants home.  {maybe tp}\n</think>\n\n" + obj,
		"fenced, thought": "<think>hmm</think>\n```json\n" + obj + "\n```\n",
	} {
		a, err := parseAskReply(text)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !a.Found || a.Answer != "Use landmark." || len(a.Suggestions) != 1 ||
			a.Suggestions[0].CommandLine != "landmark --set-home" {
			t.Errorf("%s: got %+v", name, a)
		}
	}
}

func TestParseAskReplyTakesNotFound(t *testing.T) {
	a, err := parseAskReply(`{"found": false, "answer": "slsh has no command for that.", "suggestions": []}`)
	if err != nil || a.Found || len(a.Suggestions) != 0 || a.Answer == "" {
		t.Errorf("got %+v, %v", a, err)
	}
	a, err = parseAskReply(`{"found": false, "answer": "no", "suggestions": null}`)
	if err != nil || a.Found || a.Suggestions != nil {
		t.Errorf("null suggestions: got %+v, %v", a, err)
	}
}

// Something that is not the answer is an error, never an empty answer:
// an empty answer would read as "no such command", which is a claim.
func TestParseAskReplyRefusesWhatIsNotAnAnswer(t *testing.T) {
	for name, text := range map[string]string{
		"prose":            "You can use the landmark command.",
		"empty":            "",
		"broken":           `{"found": true, "suggestions": [`,
		"cut off thinking": "<think>the person wants {\"found\": true, \"suggestions\": []}",
		"no found":         `{"answer": "x", "suggestions": []}`,
		"no suggestions":   `{"found": true, "answer": "x"}`,
		"wrong type":       `{"found": "yes", "answer": "x", "suggestions": []}`,
		"array":            `[{"found": true}]`,
	} {
		if a, err := parseAskReply(text); err == nil {
			t.Errorf("%s: taken as %+v", name, a)
		}
	}
}

func TestTheRetrySaysWhatWasWrongInTheShellsWords(t *testing.T) {
	msgs, _ := askMessages("how do I set my home", []askCandidate{askTestCandidate(t, "landmark")})
	reply := `{"found": true, "answer": "x", "suggestions": [{"command": "landmark", "command_line": "landmark --sethome", "why": "w", "quote": "q"}]}`
	a, err := parseAskReply(reply)
	if err != nil {
		t.Fatal(err)
	}
	_, rejected := filterAskAnswer(a)
	if len(rejected) != 1 {
		t.Fatalf("rejected %+v", rejected)
	}
	out := askRetryMessages(msgs, reply, rejected)
	if len(out) != len(msgs)+2 {
		t.Fatalf("%d messages", len(out))
	}
	for i := range msgs {
		if out[i] != msgs[i] {
			t.Errorf("message %d changed", i)
		}
	}
	if out[len(msgs)] != (llm.Message{Role: "assistant", Content: reply}) {
		t.Errorf("the reply is not handed back as the assistant's turn: %+v", out[len(msgs)])
	}
	last := out[len(out)-1]
	if last.Role != "user" {
		t.Errorf("last role %s", last.Role)
	}
	for _, want := range []string{
		"- landmark --sethome",
		"landmark: unknown option: --sethome",
		"too short",
		"the real usage is: " + commands["landmark"].usage("landmark"),
		"set found to false",
	} {
		if !strings.Contains(last.Content, want) {
			t.Errorf("the retry lacks %q:\n%s", want, last.Content)
		}
	}
	// The slice passed in is not appended to in place.
	if len(msgs) != 2 {
		t.Errorf("msgs grew to %d", len(msgs))
	}
}

// A suggestion for a command that does not exist gets no usage line,
// rather than somebody else's.
func TestTheRetryInventsNoUsageLine(t *testing.T) {
	out := askRetryMessages(nil, "{}", []askRejection{{
		Suggestion: askSuggestion{Command: "sethome", CommandLine: "sethome now"},
		Reasons:    []string{"sethome: no such command; try help"},
	}})
	if c := out[len(out)-1].Content; strings.Contains(c, "real usage") {
		t.Errorf("a usage line for a command that is not one:\n%s", c)
	}
}
