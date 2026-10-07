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

// Operands and flag values that come from a list the shell keeps are
// checked against that list, by the code the command uses; the
// capitalised placeholders its usage line writes pass as they are.
// Every wrong word here is invented.
func TestCheckCommandLineChecksWordsFromTheShellsLists(t *testing.T) {
	for _, c := range []struct {
		line string
		want string // "" is taken
	}{
		// set: a setting's name, under any spelling the file takes,
		// and a value that setting's own parse accepts.
		{"set", ""},
		{"set NAME", ""},
		{"set NAME VALUE", ""},
		{"set map_rows", ""},
		{"set map_rows 20", ""},
		{"set map_rows VALUE", ""},
		{"set prefix ESC", ""},
		{"set log off", ""},
		{"set map_ratio auto", ""},
		{"set viewer_grid my own grid", ""},
		{"set how_url http://127.0.0.1:11434", ""},
		{`set how_extra '{"reasoning_effort": "none"}'`, ""},
		{"set ask_url http://127.0.0.1:8080", `set: no setting called "ask_url"`},
		{"set how_slot", ""},
		{"set display_name YOUR_DISPLAY_NAME", `set: no setting called "display_name"; there is addr, agent,`},
		{"set display_name NAME", `set: no setting called "display_name"`},
		{"set nickname Example", `set: no setting called "nickname"`},
		{"set map_rows lots", `set: map_rows: want a whole number, got "lots"`},
		{"set map_rows N", `set: map_rows: want a whole number, got "N"`},
		{"set log maybe", `set: log: "maybe" is not on or off`},
		{"set map_span auto", "set: map_span: only map_ratio takes"},
		{"set how_url 127.0.0.1:11434", "set: how_url: want the server's address with http:// in front"},
		{`set how_extra {"reasoning_effort": "none"}`, "set: how_extra: want a JSON object"},
		{"set how_timeout soon", "set: how_timeout: want a length of time"},

		// maturity: a rating, by sl.ParseMaturity.
		{"maturity", ""},
		{"maturity RATING", ""},
		{"maturity adult", ""},
		{"maturity PG", ""},
		{"maturity moderate", ""},
		{"maturity teen", `maturity: sl: "teen" is not a maturity rating`},
		{"maturity YOUR_RATING", "is not a maturity rating"},

		// help: a group, all, or a command.
		{"help", ""},
		{"help all", ""},
		{"help GROUP", ""},
		{"help region", ""},
		{"help landmark", ""},
		{"help everything", `help: no group or command "everything"; groups are`},

		// man: one command.
		{"man", ""},
		{"man NAME", ""},
		{"man landmark", ""},
		{"man how", ""},
		{"man sethome", `man: no command called "sethome"`},
		{"man landmark tp", "man: one command at a time"},

		// neighbours: on or off, read off its own parameters.
		{"neighbours", ""},
		{"neighbours on", ""},
		{"neighbours OFF", ""},
		{"neighbours yes", `neighbours: "yes" is neither on nor off`},
		{"neighbours on off", "neighbours: one word at most"},

		// put: the roundings and the filters, by put's own method.
		{"put --round up photo.png", ""},
		{"put --round MODE FILE", ""},
		{"put --filter lanczos --round-x down FILE", ""},
		{"put --filter NAME --round-y nearest FILE", ""},
		{"put --round sideways FILE", `put: no rounding called "sideways"`},
		{"put --round-y MODE --filter blurry FILE", `put: no filter called "blurry"`},

		// wear --at: a point by the viewer's name or its number.
		{`wear --at "left hand" PATH`, ""},
		{"wear --at 5 PATH", ""},
		{"wear --at POINT PATH", ""},
		{"wear --at elbow PATH", `wear: --at: "elbow" is not an attachment point`},
		{"wear --at 900 PATH", "wear: --at: 900 is not an attachment point"},
		{`wear --at "HUD centre" PATH`, `wear: --at: "HUD centre" is HUD centre 1 or HUD centre 2; say which, or give its number`},
		{`wear --at "hud center" PATH`, `is HUD centre 1 or HUD centre 2; say which, or give its number`},
		{`wear --at left PATH`, `"left" is left ear, left eye, left foot`},
		{`wear --at "left ring" PATH`, `did you mean "left ring finger"`},
		{`wear --at "HUD centre 1" PATH`, ""},
		{`wear --at "Skull" PATH`, ""},

		// perms: letters, all or none.
		{"perms --owner mct --next none NAME", ""},
		{"perms --everyone LETTERS NAME", ""},
		{"perms --next all NAME", ""},
		{"perms --next nomodify NAME", `perms: --next: "n" is not a permission`},
		{"perms --group LETTERS --owner x NAME", `perms: --owner: "x" is not a permission`},

		// A command with no such list is not touched.
		{"tp YOUR_REGION", ""},
		{"landmark --go ANYTHING_AT_ALL", ""},
	} {
		err := checkCommandLine(c.line)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%q: refused: %v", c.line, err)
		case c.want != "" && err == nil:
			t.Errorf("%q: taken, want %q", c.line, c.want)
		case c.want != "" && !strings.Contains(err.Error(), c.want):
			t.Errorf("%q: %q, want it to say %q", c.line, err, c.want)
		}
	}
}

// A line with the usage line's syntax copied into it is refused: the
// shell would take "[-lN]" or "NAME|UUID" as an operand like any other.
// Brackets and bars that are not the usage line's, and anything in
// quotes, are values and pass.  The names in the passing lines are
// invented.
func TestCheckCommandLineRefusesUsageSyntax(t *testing.T) {
	const tail = "write one real value, not the usage line's [..] or A|B"
	for _, c := range []struct {
		line string
		want string // "" is taken
	}{
		// Copied whole from the usage line, as models were seen to.
		{"put [-lN] [-d TEXT] FILE", `put: "[-lN]" is the usage line's [...]`},
		{"unlink [-w SECONDS] NAME|UUID", `unlink: "[-w" is the usage line's [...]`},
		{"unlink -w 5 NAME|UUID", `unlink: "NAME|UUID" is the usage line's A|B, which means one of them; ` + tail},
		{"offer WHO [TEXT]", `offer: "[TEXT]" is the usage line's [...], which marks what may be left out; ` + tail},

		// Half a group, a flag in brackets, a group with a bar in it.
		{"put -d TEXT] FILE", `put: "TEXT]" is the usage line's [...]`},
		{"put [-d photo] FILE", `"[-d" is the usage line's [...], which marks what may be left out; ` + tail + ", and a flag without the brackets"},
		{"landmark [--go] Example Workshop", `"[--go]" is the usage line's [...]`},
		{"man [NAME]", `man: "[NAME]" is the usage line's [...]`},
		{"help [GROUP|all]", `help: "[GROUP|all]" is the usage line's [...]`},
		{"tp Example [X Y Z]", `tp: "[X" is the usage line's [...]`},
		{"say [-c CHANNEL] hello", `say: "[-c" is the usage line's [...]`},
		{"say TEXT ...]", `say: "...]" is the usage line's [...]`},

		// Alternatives: the usage line's own, or with its placeholder in.
		{"neighbours on|off", `neighbours: "on|off" is the usage line's A|B`},
		{"wear PATH|UUID", `wear: "PATH|UUID" is the usage line's A|B`},
		{"take --into Objects Example|UUID", `take: "Example|UUID" is the usage line's A|B`},

		// The placeholders alone are still what a person fills in.
		{"unlink NAME", ""},
		{"unlink UUID", ""},
		{"put -d TEXT FILE", ""},
		{"offer WHO TEXT", ""},
		{"man NAME", ""},
		{"cat --in OBJECT PATH", ""},

		// Brackets and bars that are not the usage line's are text.
		{"say [OOC] back soon", ""},
		{"say this|that", ""},
		{"say | on its own", ""},
		{"say [-5] is a number", ""},
		{"wear [Example]Hat", ""},

		// And anything in quotes is a value written on purpose.
		{`say "[TEXT]"`, ""},
		{`unlink "NAME|UUID"`, ""},
		{`wear "[Example] Hat|Blue"`, ""},
		{`set how_extra '{"stop": ["[NAME]", "|"]}'`, ""},
	} {
		err := checkCommandLine(c.line)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%q: refused: %v", c.line, err)
		case c.want != "" && err == nil:
			t.Errorf("%q: taken, want %q", c.line, c.want)
		case c.want != "" && !strings.Contains(err.Error(), c.want):
			t.Errorf("%q: %q, want it to say %q", c.line, err, c.want)
		}
	}
}

// The placeholders are the usage line's own capitalised words, and
// nothing else.
func TestAskPlaceholdersAreTheUsageLines(t *testing.T) {
	ph := askPlaceholders(commands["set"].usage("set"))
	if !ph["NAME"] || !ph["VALUE"] || ph["set"] || ph["YOUR_DISPLAY_NAME"] {
		t.Errorf("set: %v", ph)
	}
	ph = askPlaceholders(commands["put"].usage("put"))
	if !ph["MODE"] || !ph["FILE"] || ph["put"] {
		t.Errorf("put: %v in %q", ph, commands["put"].usage("put"))
	}
	if ph := askPlaceholders("x --dry-run -N [NAME|UUID] L$FEE"); !ph["N"] || !ph["NAME"] || !ph["UUID"] || !ph["FEE"] || ph["D"] {
		t.Errorf("%v", ph)
	}
}

// A line with a character nobody can type in it is refused: it would be
// printed as it is, escape sequences and all.  A tab is typeable, and
// the shell splits on it.
func TestCheckCommandLineRefusesControlCharacters(t *testing.T) {
	for line, want := range map[string]string{
		"ls\x1b[2J":             "U+001B",
		"tp home\x1b[1Aquit":    "U+001B",
		"landmark --home\nquit": "U+000A",
		"say hello\u202e dlrow": "U+202E",
		"say hi\u200b":          "U+200B",
	} {
		err := checkCommandLine(line)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %s", line, err, want)
		}
	}
	if err := checkCommandLine("ls\t-l"); err != nil {
		t.Errorf("a tab: %v", err)
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
		// A heading run into the paragraph under it, with the mark the
		// prompt's "manual, HEADING:" puts between them, or another.
		{"landmark", "A landmark is a point and not a place: What the asset holds is a region id"},
		{"landmark", "A landmark is a point and not a place. What the asset holds"},
		{"landmark", "--set-home: Make where this avatar is standing the place home is"},
		{"landmark", "--set-home -- Make where this avatar is standing"},
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
		// A heading joined to a paragraph that is not the one under it.
		{"landmark", "A landmark is a point and not a place: Make where this avatar is standing", "not in landmark's"},
		{"landmark", "--home: Make where this avatar is standing", "not in landmark's"},
		// Pieces that are there, in the wrong order.
		{"landmark", "the place home is ... Make where this avatar is standing", "not in landmark's"},
		// A usage line that is only the name, whole as it is, and then
		// nothing, and a command that is not one.
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
	kept, rejected := filterAskAnswer(askAnswer{Found: false})
	if len(kept) != 0 || len(rejected) != 0 {
		t.Errorf("kept %v, rejected %v", kept, rejected)
	}
}
