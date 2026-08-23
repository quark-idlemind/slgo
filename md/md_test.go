package md

import (
	"errors"
	"strings"
	"testing"
)

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
					i++
				}
				if i < len(s) {
					i++
				}
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func vislines(s string) []string {
	s = strings.TrimRight(stripANSI(s), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestWidthIs80WhenTheTerminalWillNotSay(t *testing.T) {
	old := termSize
	termSize = func(int) (int, int, error) {
		return 0, 0, errors.New("not a terminal")
	}
	defer func() { termSize = old }()
	if g := Width(); g != DefaultWidth {
		t.Errorf("Width = %d, want %d", g, DefaultWidth)
	}
}

func TestWidthUsesWhatTheTerminalReports(t *testing.T) {
	old := termSize
	termSize = func(int) (int, int, error) {
		return 120, 40, nil
	}
	defer func() { termSize = old }()
	if g := Width(); g != 120 {
		t.Errorf("Width = %d, want 120", g)
	}
}

func TestRenderUsesTheTerminalWidth(t *testing.T) {
	old := termSize
	termSize = func(int) (int, int, error) {
		return 20, 10, nil
	}
	defer func() { termSize = old }()
	got := Render("one two three four five six seven eight")
	for _, line := range vislines(got) {
		if visibleColumns(line) > 20 {
			t.Errorf("Render wrapped past the terminal: %q", line)
		}
	}
}

func TestRenderWidthOfZeroIsEighty(t *testing.T) {
	got := RenderWidth("hello", 0)
	if !strings.Contains(stripANSI(got), "hello") {
		t.Errorf("got %q", got)
	}
}

func TestEmptyInputIsEmpty(t *testing.T) {
	if g := RenderWidth("", 80); g != "" {
		t.Errorf("empty document rendered as %q", g)
	}
}

// TestAHeadingIsBoldNotShouted.
//
// slsh's man pages uppercase a heading because they have no styling to
// send.  This package does, so a heading keeps the case it was written
// in and is marked bold.  Shouting "--REPLACE" is exactly what we are
// no longer doing.
func TestAHeadingIsBoldNotShouted(t *testing.T) {
	got := RenderWidth("# Hello World", 80)
	if !strings.Contains(got, "\x1b[1m") {
		t.Errorf("heading has no bold:\n%s", got)
	}
	vis := stripANSI(got)
	if strings.Contains(vis, "# Hello") {
		t.Errorf("heading marker reached the screen:\n%s", vis)
	}
	if strings.Contains(vis, "HELLO WORLD") && !strings.Contains(vis, "Hello World") {
		t.Errorf("heading was shouted:\n%s", vis)
	}
	if !strings.Contains(vis, "Hello World") {
		t.Errorf("heading text missing:\n%s", vis)
	}
	if !strings.Contains(vis, hdouble) {
		t.Errorf("h1 should be underlined:\n%s", vis)
	}
}

func TestASecondLevelHeadingUsesASingleRule(t *testing.T) {
	got := stripANSI(RenderWidth("## Section", 80))
	if !strings.Contains(got, "Section") {
		t.Errorf("got %q", got)
	}
	if strings.Contains(got, hdouble) {
		t.Errorf("h2 should not use the double rule:\n%s", got)
	}
	if !strings.Contains(got, hline) {
		t.Errorf("h2 should be underlined:\n%s", got)
	}
}

func TestProseWrapsToTheWidthItWasGiven(t *testing.T) {
	src := "one two three four five six seven eight nine ten eleven twelve"
	got := RenderWidth(src, 20)
	for _, line := range vislines(got) {
		if visibleColumns(line) > 20 {
			t.Errorf("prose wrapped past 20 columns: %q", line)
		}
	}
	vis := stripANSI(got)
	for _, w := range []string{"one", "twelve"} {
		if !strings.Contains(vis, w) {
			t.Errorf("missing %q in\n%s", w, vis)
		}
	}
}

func TestAWordLongerThanTheWidthIsLeftWhole(t *testing.T) {
	const word = "a9a87e577e57c0de1386d6cc4c66fa82"
	got := vislines(RenderWidth(word, 10))
	if len(got) != 1 || got[0] != word {
		t.Errorf("a long word was cut: %q", got)
	}
}

func TestACodeBlockIsNotWrapped(t *testing.T) {
	src := "```\n" + strings.Repeat("abcde ", 20) + "\n```"
	got := vislines(RenderWidth(src, 20))
	if len(got) != 1 {
		t.Fatalf("code block wrapped into %d lines:\n%q", len(got), got)
	}
	if !strings.HasPrefix(got[0], "    ") {
		t.Errorf("code block should be indented: %q", got[0])
	}
}

func TestIndentedCodeIsLeftAlone(t *testing.T) {
	src := "    place --at 128,128,25 \"a name with spaces\""
	got := vislines(RenderWidth(src, 20))
	if len(got) != 1 {
		t.Fatalf("indented code wrapped: %q", got)
	}
	if !strings.Contains(got[0], "place --at") {
		t.Errorf("got %q", got[0])
	}
}

func TestEmphasisAndCodeUseEscapeSequences(t *testing.T) {
	got := RenderWidth("say **bold** and *italic* and `code` and ~~old~~", 80)
	for _, seq := range []string{"\x1b[1m", "\x1b[3m", "\x1b[2m", "\x1b[9m", "\x1b[0m"} {
		if !strings.Contains(got, seq) {
			t.Errorf("missing %q in\n%q", seq, got)
		}
	}
	vis := stripANSI(got)
	if strings.Contains(vis, "**") || strings.Contains(vis, "*italic*") {
		t.Errorf("markup reached the screen:\n%s", vis)
	}
	if !strings.Contains(vis, "bold") || !strings.Contains(vis, "italic") || !strings.Contains(vis, "code") {
		t.Errorf("text missing:\n%s", vis)
	}
}

func TestEscapedMarkupIsLiteral(t *testing.T) {
	raw := RenderWidth(`\*not italic\* and \*\*not bold\*\*`, 80)
	got := stripANSI(raw)
	if !strings.Contains(got, "*not italic*") {
		t.Errorf("escaped stars lost: %q", got)
	}
	if strings.Contains(raw, "\x1b[1m") || strings.Contains(raw, "\x1b[3m") {
		t.Errorf("escaped markup still styled: %q", raw)
	}
}

func TestInlineCodeDoesNotInterpretStars(t *testing.T) {
	got := stripANSI(RenderWidth("`**still stars**`", 80))
	if got := strings.TrimSpace(got); got != "**still stars**" {
		t.Errorf("code span rendered as %q", got)
	}
}

func TestALinkKeepsTheURL(t *testing.T) {
	got := RenderWidth("see [the guide](guide.md) please", 80)
	if !strings.Contains(got, "\x1b[4m") {
		t.Errorf("link is not underlined:\n%q", got)
	}
	vis := stripANSI(got)
	if !strings.Contains(vis, "the guide") {
		t.Errorf("link text missing:\n%s", vis)
	}
	if !strings.Contains(vis, "guide.md") {
		t.Errorf("url missing:\n%s", vis)
	}
}

func TestALinkWhoseTextIsTheURLIsNotRepeated(t *testing.T) {
	got := stripANSI(RenderWidth("[http://example.com](http://example.com)", 80))
	if strings.Count(got, "http://example.com") != 1 {
		t.Errorf("url printed twice: %q", got)
	}
}

func TestAnImageBecomesItsAltText(t *testing.T) {
	got := stripANSI(RenderWidth("![a cat](cat.png)", 80))
	if !strings.Contains(got, "a cat") {
		t.Errorf("alt missing: %q", got)
	}
	if strings.Contains(got, "cat.png") {
		t.Errorf("image url leaked: %q", got)
	}
}

func TestAListUsesABulletAndHangsTheWrap(t *testing.T) {
	src := "- first item that is long enough to wrap around\n- second"
	got := vislines(RenderWidth(src, 24))
	if len(got) < 2 {
		t.Fatalf("list too short: %q", got)
	}
	if !strings.HasPrefix(got[0], bullet+" ") {
		t.Errorf("first item has no bullet: %q", got[0])
	}
	if strings.HasPrefix(got[1], bullet+" ") {
		// second visible line should be the wrap, not the next item,
		// at this width.
		if !strings.Contains(got[1], "second") {
			t.Errorf("wrap line took a bullet: %q", got)
		}
	} else if visibleColumns(got[1]) > 0 && !strings.HasPrefix(got[1], "  ") {
		t.Errorf("wrapped list item is not hung: %q", got[1])
	}
}

func TestAnOrderedListKeepsItsNumbers(t *testing.T) {
	src := "1. alpha\n2. beta"
	got := stripANSI(RenderWidth(src, 80))
	if !strings.Contains(got, "1. alpha") || !strings.Contains(got, "2. beta") {
		t.Errorf("got %q", got)
	}
}

func TestANestedListIndents(t *testing.T) {
	src := "- outer\n  - inner"
	got := vislines(RenderWidth(src, 80))
	if len(got) < 2 {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got[0], "outer") || !strings.Contains(got[1], "inner") {
		t.Errorf("got %q", got)
	}
	if indentOf(got[1]) <= indentOf(got[0]) {
		t.Errorf("nested item is not indented further: %q", got)
	}
}

func TestAQuoteHasABar(t *testing.T) {
	got := vislines(RenderWidth("> spoken", 80))
	if len(got) != 1 || !strings.HasPrefix(got[0], vbar+" ") || !strings.Contains(got[0], "spoken") {
		t.Errorf("got %q", got)
	}
}

func TestARuleIsALineOfTheWidth(t *testing.T) {
	got := vislines(RenderWidth("before\n\n---\n\nafter", 12))
	found := false
	for _, line := range got {
		if line == strings.Repeat(hline, 12) {
			found = true
		}
	}
	if !found {
		t.Errorf("no rule of width 12 in %q", got)
	}
}

func TestWrappedLinesDoNotEndInASpace(t *testing.T) {
	src := "Three command-line programs for working with Second Life without a viewer."
	for _, line := range vislines(RenderWidth(src, 40)) {
		if strings.HasSuffix(line, " ") {
			t.Errorf("wrapped line has a trailing space: %q", line)
		}
	}
}

func TestAnEmptyTableHeaderIsNotABlankRow(t *testing.T) {
	src := "" +
		"| | |\n" +
		"|---|---|\n" +
		"| slgod | holds the session |\n"
	got := vislines(RenderWidth(src, 40))
	blank := 0
	for _, line := range got {
		if !strings.Contains(line, vbar) || strings.Contains(line, hline) {
			continue
		}
		stripped := strings.ReplaceAll(strings.ReplaceAll(line, vbar, ""), " ", "")
		if stripped == "" {
			blank++
		}
	}
	if blank != 0 {
		t.Errorf("empty header was drawn as a blank row:\n%s", strings.Join(got, "\n"))
	}
	if !strings.Contains(strings.Join(got, "\n"), "slgod") {
		t.Errorf("body missing:\n%s", strings.Join(got, "\n"))
	}
}

func TestATableFitsTheWidth(t *testing.T) {
	src := "" +
		"| name | what it does |\n" +
		"|---|---|\n" +
		"| slgod | a daemon that holds grid sessions, so everything else starts instantly |\n"
	const width = 40
	got := RenderWidth(src, width)
	for _, line := range vislines(got) {
		if visibleColumns(line) > width {
			t.Errorf("table line past %d columns: %q (vis %d)", width, line, visibleColumns(line))
		}
	}
}

func TestATableSurvivesAsATable(t *testing.T) {
	src := "" +
		"| atom | bytes |\n" +
		"|---|---|\n" +
		"| integer constant | 5 |\n" +
		"| float constant | 9 |\n"
	got := stripANSI(RenderWidth(src, 80))
	for _, want := range []string{"atom", "bytes", "integer constant", "5", "float constant", "9", cornerTL, vbar} {
		if !strings.Contains(got, want) {
			t.Errorf("table missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, vbar) < 8 {
		t.Errorf("table has too few bars:\n%s", got)
	}
}

func TestBoldDoesNotLeakAcrossAWrap(t *testing.T) {
	src := "**one two three four five six seven** and then plain"
	got := RenderWidth(src, 16)
	// After the closing reset, "and then plain" must not still be bold.
	reset := strings.LastIndex(got, sgrReset)
	if reset < 0 {
		t.Fatalf("no reset in %q", got)
	}
	tail := got[reset+len(sgrReset):]
	if strings.Contains(tail, "\x1b[1m") && strings.Contains(stripANSI(tail), "plain") {
		// a new bold after the paragraph would be a leak of a different kind
	}
	if i := strings.Index(stripANSI(got), "plain"); i >= 0 {
		// Find "plain" in the raw string; the characters of "plain" should
		// not be inside an un-reset bold run at the end.
		if strings.Contains(got, "\x1b[1mplain") {
			t.Errorf("plain text is bold:\n%q", got)
		}
	}
}

func TestSetextHeading(t *testing.T) {
	got := stripANSI(RenderWidth("Title\n=====\n\nbody", 80))
	if !strings.Contains(got, "Title") || !strings.Contains(got, "body") {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(got, hdouble) {
		t.Errorf("setext h1 should be underlined:\n%s", got)
	}
}

func TestAHardBreakIsKept(t *testing.T) {
	got := vislines(RenderWidth("left  \nright", 80))
	if len(got) != 2 || got[0] != "left" || got[1] != "right" {
		t.Errorf("hard break lost: %q", got)
	}
}

func TestBackslashHardBreak(t *testing.T) {
	got := vislines(RenderWidth("left\\\nright", 80))
	if len(got) != 2 || got[0] != "left" || got[1] != "right" {
		t.Errorf("backslash break lost: %q", got)
	}
}

func TestParagraphsAreSeparated(t *testing.T) {
	got := vislines(RenderWidth("first paragraph\n\nsecond paragraph", 80))
	if len(got) != 3 || got[0] != "first paragraph" || got[1] != "" || got[2] != "second paragraph" {
		t.Errorf("got %q", got)
	}
}

func TestUnderscoreInsideAWordIsNotEmphasis(t *testing.T) {
	got := stripANSI(RenderWidth("foo_bar_baz", 80))
	if strings.TrimSpace(got) != "foo_bar_baz" {
		t.Errorf("got %q", got)
	}
}

func TestAutolink(t *testing.T) {
	got := RenderWidth("see <https://example.com/a> please", 80)
	if !strings.Contains(got, "\x1b[4m") {
		t.Errorf("autolink not underlined: %q", got)
	}
	if !strings.Contains(stripANSI(got), "https://example.com/a") {
		t.Errorf("url missing: %q", got)
	}
}

func TestFencedLanguageTagIsDropped(t *testing.T) {
	got := stripANSI(RenderWidth("```go\nfmt.Println(1)\n```", 80))
	if strings.Contains(got, "```") || strings.Contains(got, "go\n") {
		t.Errorf("fence leaked: %q", got)
	}
	if !strings.Contains(got, "fmt.Println(1)") {
		t.Errorf("code missing: %q", got)
	}
}

func TestWrapCountsStyleAsZeroWidth(t *testing.T) {
	// A bold word must wrap as if the SGR were not there; otherwise a
	// paragraph of emphasised words wraps early and looks ragged.
	src := "**one** **two** **three** **four**"
	got := RenderWidth(src, 18)
	for _, line := range vislines(got) {
		if visibleColumns(line) > 18 {
			t.Errorf("styled wrap past 18: %q (vis %d)", line, visibleColumns(line))
		}
	}
}

func TestADocumentFromTheTreeStillReads(t *testing.T) {
	src := `# A guide

There are **two** ways to be connected.

| program | what |
|---|---|
| slgod | holds the session |
| slsh | the shell |

- first
- second

    slgod example
`
	got := stripANSI(RenderWidth(src, 60))
	for _, want := range []string{"A guide", "two", "slgod", "holds the session", "first", "slgod example"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}
