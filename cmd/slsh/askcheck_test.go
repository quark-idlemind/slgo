package main

import (
	"strings"
	"testing"
)

// Lines the shell would take.  Every one is typed against a real
// command's real option struct; the names in them are invented.
func TestCheckCommandLineTakesWhatTheShellTakes(t *testing.T) {
	for _, line := range []string{
		"ls",
		"ls -l",
		"ls -lrt /Objects",
		"ls --in \"Example Box\" Contents",
		"landmark",
		"landmark --set-home",
		"landmark --home",
		"landmark --go Example Workshop",
		"landmark --wait 60 --go NAME",
		"landmark -w 60 --go NAME",
		"landmark --wait=60 --go NAME",
		"tp home",
		"tp Example Region",
		"tp 128 128 25",
		"tp 10 -5 20",       // tp puts a "--" in front of a negative position
		"tp -w 5 10 -5 -20", // and after its own flags
		"say -c -12345 ok",
		"say hello -1 degrees", // a negative number after the text is text
		"find -l TEXT",
		"find -Ll TEXT PATH",
		"put --filters",
		"put -N photo.png",
		"wear --replace PATH",
		"take --copy --into Objects NAME",
		"cat --in OBJECT PATH",
		"exit",  // quit, by its other name
		"unsit", // stand, likewise
		". FILE",
		"source FILE",
		"echo --help --anything -x", // echo has no flags, so nothing is one
		"ls > listing",
		"ls -l >> listing",
		"ls --help",
		"help all",
		"man landmark",
		"  ls -l  ",
		"mv -- -odd-name Other", // after "--" a dash is a name
	} {
		if err := checkCommandLine(line); err != nil {
			t.Errorf("%q: refused: %v", line, err)
		}
	}
}

// Lines the shell would refuse, or would take and then do something
// other than what they seem to say.  The error is what a person would
// have seen.
func TestCheckCommandLineRefusesWhatTheShellRefuses(t *testing.T) {
	for _, c := range []struct {
		line string
		want string
	}{
		// Commands that do not exist.
		{"sethome", "sethome: no such command; try help"},
		{"teleport Example Region", "teleport: no such command; try help"},
		{"home", "home: no such command; try help"},
		{"landmarks --set-home", "landmarks: no such command"},
		{"LS -l", "LS: no such command"},
		{"slsh ls", "slsh: no such command"},
		{"/ls", "/ls: no such command"},

		// Flags that do not exist.
		{"landmark --sethome", "landmark: unknown option: --sethome"},
		{"landmark --set-home-here", "landmark: unknown option: --set-home-here"},
		{"landmark -s", "landmark: unknown option: -s"},
		{"ls --long", "ls: unknown option: --long"},
		{"ls -x", "ls: unknown option: -x"},
		{"ls -la", "ls: unknown option: -a"},
		{"tp --region Example", "tp: unknown option: --region"},
		{"exit --now", "exit: unknown option: --now"},
		{"wear --replace --all PATH", "wear: unknown option: --all"},
		{"say --channel 5 hi", "say: unknown option: --channel"},

		// A flag that wants a value and has none, or a wrong one.
		{"landmark --wait", "landmark: missing parameter for --wait"},
		{"landmark -w", "landmark: missing parameter for -w"},
		{"landmark -w soon --go NAME", "landmark: not a valid number: soon"},
		{"cat --in", "cat: missing parameter for --in"},
		{"say -c", "say: missing parameter for -c"},

		// A flag after the operands, which the command takes as text.
		{"landmark Example Workshop --go", `landmark: --go comes after "Example", where it is taken as text`},
		{"ls /Objects -l", `ls: -l comes after "/Objects"`},
		{"ls /Objects --long", "ls: unknown option: --long"},
		{"tp Example Region --wait 5", `tp: --wait comes after "Example"`},

		// Lines that are not commands at all.
		{"", "the line is empty"},
		{"   ", "the line is empty"},
		{"# ls", "the line is a comment"},
		{`say "unclosed`, "unclosed \" quote"},
		{"ls > a > b", "only one redirection per line"},
		{"ls >", "no file after >"},
	} {
		err := checkCommandLine(c.line)
		if err == nil {
			t.Errorf("%q: taken, want %q", c.line, c.want)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %q, want it to say %q", c.line, err, c.want)
		}
	}
}

// Every example a man page gives is a line the checker takes.  The
// pages are the other thing that knows what a command accepts, and they
// were written by somebody who ran the lines; a checker that refused
// one of them would be refusing the model for copying the manual.
func TestEveryManPageExampleIsTakenByTheChecker(t *testing.T) {
	names, err := manFileNames()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range names {
		text, err := manRead(strings.TrimSuffix(file, ".md"))
		if err != nil {
			t.Fatal(err)
		}
		inExamples := false
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "## ") {
				inExamples = strings.TrimSpace(line[3:]) == "Examples"
				continue
			}
			if !inExamples || !strings.HasPrefix(line, "    ") {
				continue
			}
			ex := strings.TrimSpace(line)
			first := askFirstWord(ex)
			if _, ok := commands[first]; !ok {
				continue // output, or a line of a file, shown under a command
			}
			checked++
			if err := checkCommandLine(ex); err != nil {
				t.Errorf("%s: example %q: %v", file, ex, err)
			}
		}
	}
	if checked < 50 {
		t.Errorf("only %d examples found; has the Examples heading changed?", checked)
	}
}

func TestCheckQuoteFindsTheCommandsOwnWords(t *testing.T) {
	for _, c := range []struct{ command, quote string }{
		// The page, as written.
		{"landmark", "Make where this avatar is standing the place home is."},
		// Across a line break in the page, and in another case.
		{"landmark", "MAKE WHERE THIS AVATAR is standing the place home is"},
		// Past the page's markdown: **--set-home** and a blank line.
		{"landmark", "--set-home Make where this avatar is standing"},
		// With backticks the page has and the quote does not.
		{"landmark", "tp home is the same trip said the short way"},
		// With backticks the quote has and the page does not.
		{"landmark", "`landmark --set-home` makes where this avatar is standing the place home is"},
		// Typographic quotes and a dash a model put in.
		{"landmark", "the grid\u2019s own way of writing a place down"},
		{"landmark", "thirty seconds \u2014 tp\u2019s wait, for the same kind of trip"},
		// Round a quotation, and with an ellipsis.
		{"landmark", "\"Make where this avatar is standing ... the place home is.\""},
		{"landmark", "Make where this avatar is standing \u2026 the place home is"},
		// The brief and the usage line.
		{"landmark", "the landmarks in inventory, where one goes"},
		{"landmark", "landmark [--go] [--home] [--make]"},
		// A name that shares a command quotes that command.
		{"exit", "leave slsh"},
		{"unsit", "unsit [-w SECONDS]"},
	} {
		if err := checkQuote(c.command, c.quote); err != nil {
			t.Errorf("%s %q: %v", c.command, c.quote, err)
		}
	}
}

func TestCheckQuoteRefusesWhatIsNotThere(t *testing.T) {
	for _, c := range []struct{ command, quote, want string }{
		// Invented, though it reads like the page.
		{"landmark", "sets your home to the current landmark", "not in landmark's"},
		{"landmark", "Make where this avatar is standing the place home will be", "not in landmark's"},
		// Words from the page, but one changed.
		{"landmark", "Make where this avatar is sitting the place home is", "not in landmark's"},
		// True, and on another command's page.
		{"tp", "Make where this avatar is standing the place home is", "not in tp's"},
		// Too short to be evidence of anything.
		{"landmark", "--set-home", "too short"},
		{"landmark", "the landmark", "too short"},
		{"landmark", "Make where ... home", "too short"},
		// Pieces that are there, in the wrong order.
		{"landmark", "the place home is ... Make where this avatar is standing", "not in landmark's"},
		// Nothing, and a command that is not one.
		// A usage line that is only the name, whole as it is.
		{"pwd", "pwd", "too short"},
		{"landmark", "", "no quote"},
		{"landmark", "  ...  ", "no quote"},
		{"sethome", "Make where this avatar is standing", "sethome: no such command"},
	} {
		err := checkQuote(c.command, c.quote)
		if err == nil {
			t.Errorf("%s %q: taken", c.command, c.quote)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %q: %q, want it to say %q", c.command, c.quote, err, c.want)
		}
	}
}

func TestQuoteNormal(t *testing.T) {
	for in, want := range map[string]string{
		"**--go**\n\nGo to the  landmark *NAME* names.": "--go go to the landmark name names",
		"## Setting home": "setting home",
		"`tp home`":       "tp home",
		"\u201cit\u2019s here\u201d \u2014 there": "it's here\" -- there",
		"NAME|UUID":       "name uuid",
		"  \"quoted.\"  ": "quoted",
	} {
		if got := quoteNormal(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestFilterAskAnswerKeepsOnlyWhatChecks(t *testing.T) {
	good := askSuggestion{
		Command:     "landmark",
		CommandLine: "landmark --set-home",
		Why:         "it sets home where the avatar stands",
		Quote:       "Make where this avatar is standing the place home is.",
	}
	a := askAnswer{Found: true, Suggestions: []askSuggestion{
		good,
		// An invented flag, with a real quote.
		{Command: "landmark", CommandLine: "landmark --sethome", Quote: good.Quote},
		// An invented command.
		{Command: "sethome", CommandLine: "sethome", Quote: "sets home to where you are standing"},
		// A real line with an invented quote.
		{Command: "landmark", CommandLine: "landmark --home", Quote: "teleports you to your home location instantly"},
		// A line that runs one command, labelled as another.
		{Command: "tp", CommandLine: "landmark --home", Quote: "tp home is the same trip said the short way"},
		// The good one again, wrapped the way models wrap it.
		{Command: "landmark", CommandLine: "`$ landmark  --set-home`", Quote: good.Quote},
		// A good one by another name.
		{Command: "unsit", CommandLine: "stand", Quote: "get up, from either kind of sit"},
	}}
	kept, rejected := filterAskAnswer(a)

	if len(kept) != 2 || kept[0].CommandLine != "landmark --set-home" || kept[1].CommandLine != "stand" {
		t.Errorf("kept %+v", kept)
	}
	want := []struct {
		line    string
		reasons []string
	}{
		{"landmark --sethome", []string{"landmark: unknown option: --sethome"}},
		{"sethome", []string{"sethome: no such command; try help", "sethome: no such command"}},
		{"landmark --home", []string{"not in landmark's"}},
		{"landmark --home", []string{"labelled tp but the line runs landmark"}},
	}
	if len(rejected) != len(want) {
		t.Fatalf("rejected %d, want %d: %+v", len(rejected), len(want), rejected)
	}
	for i, w := range want {
		r := rejected[i]
		if r.Suggestion.CommandLine != w.line {
			t.Errorf("rejection %d is %q, want %q", i, r.Suggestion.CommandLine, w.line)
		}
		if len(r.Reasons) != len(w.reasons) {
			t.Errorf("%q: reasons %q, want %d of them", w.line, r.Reasons, len(w.reasons))
			continue
		}
		for j, reason := range w.reasons {
			if !strings.Contains(r.Reasons[j], reason) {
				t.Errorf("%q: reason %q, want it to say %q", w.line, r.Reasons[j], reason)
			}
		}
	}
}

// A not-found answer has nothing to filter, and filtering it finds
// nothing wrong: "no such command" is an answer, not a failure.
func TestFilteringNotFoundIsNotARejection(t *testing.T) {
	kept, rejected := filterAskAnswer(askAnswer{Found: false, Answer: "slsh has no command for that."})
	if len(kept) != 0 || len(rejected) != 0 {
		t.Errorf("kept %v, rejected %v", kept, rejected)
	}
}
