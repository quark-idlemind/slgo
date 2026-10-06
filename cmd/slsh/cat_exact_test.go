package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// A script as the grid holds it: a DEL inside a string literal, a tab,
// a line with a CRLF, and the one NUL that ends the asset.
const delScript = "string SEP = \"\x7f\";\n\tdefault { state_entry() { llSay(0, SEP); } }\r\n\x00"

// delText is delScript without the NUL, which is the text.
var delText = strings.TrimSuffix(delScript, "\x00")

// TestCatGivesAScriptsTextExactlyToAPipe: output that is not a terminal
// gets the text's bytes, with no caret form, no CRLF folding and no NUL.
// Why: doc/slsh.md#why-cat-gives-the-text-as-it-is
func TestCatGivesAScriptsTextExactlyToAPipe(t *testing.T) {
	x := newTestShell(t)
	serveAsset(t, x, delScript, false)

	if got := x.do(t, "cat /Scripts/probe"); got != delText {
		t.Errorf("cat to a pipe printed %q, want %q", got, delText)
	}
}

// TestCatShowsCaretFormOnATerminalAndSaysSo: on a screen the text is
// kept safe, and one line says that it is not the text itself.
func TestCatShowsCaretFormOnATerminalAndSaysSo(t *testing.T) {
	x := newTestShell(t)
	x.term.screen = true
	serveAsset(t, x, delScript, false)

	got := x.do(t, "cat /Scripts/probe")
	if want := "string SEP = \"^?\";\n\tdefault { state_entry() { llSay(0, SEP); } }\n"; !strings.HasPrefix(got, want) {
		t.Errorf("cat on a terminal printed %q, want it to begin %q", got, want)
	}
	if strings.Contains(got, "^@") || strings.ContainsAny(got, "\x00\x7f\r") {
		t.Errorf("cat on a terminal printed a NUL, a DEL or a CR: %q", got)
	}
	if n := strings.Count(got, "caret form"); n != 1 || !strings.Contains(got, "cat -o FILE") {
		t.Errorf("cat on a terminal should say once that it is in caret form and name -o: %q", got)
	}

	// Text with nothing to show is not warned about.
	serveAsset(t, x, "default { }\n\x00", false)
	if got, want := x.do(t, "cat /Scripts/probe"), "default { }\n"; got != want {
		t.Errorf("cat of plain text on a terminal printed %q, want %q", got, want)
	}
}

// TestCatOWritesTheExactTextWhateverTheTerminalIs.
func TestCatOWritesTheExactTextWhateverTheTerminalIs(t *testing.T) {
	x := newTestShell(t)
	x.term.screen = true
	serveAsset(t, x, delScript, false)

	file := aFile(t, "out.lsl", "")
	if got, want := x.do(t, "cat -o "+file+" /Scripts/probe"), file+": "+strconv.Itoa(len(delText))+" bytes\n"; got != want {
		t.Errorf("cat -o printed %q", got)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != delText {
		t.Errorf("cat -o wrote %q, want %q", b, delText)
	}
}

// TestCatThenSaveGivesTheSameBytesBack: the way back in sends the file
// as it is, so a DEL, a tab and a CRLF survive, and no NUL is added.
func TestCatThenSaveGivesTheSameBytesBack(t *testing.T) {
	x := newTestShell(t)
	script := serveItemWrite(t, x, "UpdateScriptAgent", compileOK)
	serveAsset(t, x, delScript, false)

	file := aFile(t, "round.lsl", "")
	x.do(t, "cat -o "+file+" /Scripts/probe")
	x.do(t, "save "+file+" /Scripts/probe")
	if _, wrote := script.seen(); wrote != delText {
		t.Errorf("save sent %q, want the text cat -o wrote, %q", wrote, delText)
	}

	// A redirect gets the same bytes.
	x.do(t, "cat /Scripts/probe > "+file)
	if b, _ := os.ReadFile(file); string(b) != delText {
		t.Errorf("cat > file wrote %q, want %q", b, delText)
	}
}

// TestCatOfANotecardKeepsTheControlCharactersInItsBody: a notecard has
// its own container, which is not shown; only the body's control
// characters are at issue.
func TestCatOfANotecardKeepsTheControlCharactersInItsBody(t *testing.T) {
	x := newTestShell(t)
	body := "a\x7fb\tc\r\nd\n"
	serveAsset(t, x, "Linden text version 2\n{\nLLEmbeddedItems version 1\n{\ncount 0\n}\n"+
		"Text length "+strconv.Itoa(len(body))+"\n"+body+"}\n\x00", false)

	if got := x.do(t, "cat readme"); got != body {
		t.Errorf("cat of a notecard to a pipe printed %q, want %q", got, body)
	}
	x.term.screen = true
	got := x.do(t, "cat readme")
	if !strings.HasPrefix(got, "a^?b\tc\nd\n") || !strings.Contains(got, "caret form") {
		t.Errorf("cat of a notecard on a terminal printed %q", got)
	}
}
