package slate

import (
	"strings"
	"testing"
	"time"
)

func lexAll(t *testing.T, src string) []token {
	t.Helper()
	l := newLexer("t.slate", []byte(src))
	var out []token
	for {
		tok, err := l.Next()
		if err != nil {
			t.Fatalf("lex %q: %v", src, err)
		}
		out = append(out, tok)
		if tok.kind == kEOF {
			return out
		}
		if len(out) > 1000 {
			t.Fatalf("lexer did not stop")
		}
	}
}

func TestLexesAScript(t *testing.T) {
	got := lexAll(t, "slate 1 # comment\nobject hud is \"Example \\\"East\"\ntimeout 10s\nL$5\n")
	want := []struct {
		kind kind
		text string
	}{
		{kWord, "slate"},
		{kInt, "1"},
		{kWord, "object"},
		{kWord, "hud"},
		{kWord, "is"},
		{kString, "Example \"East"},
		{kWord, "timeout"},
		{kDuration, "10s"},
		{kMoney, "L$"},
		{kInt, "5"},
		{kEOF, ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d tokens, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].kind != w.kind || (w.kind != kEOF && got[i].text != w.text) {
			t.Fatalf("token %d = %s %q, want %q", i, got[i].text, got[i].text, w.text)
		}
	}
	if got[7].dur != 10*time.Second {
		t.Fatalf("duration %s", got[7].dur)
	}
}

func TestLexFloatThenUnitIsNotADuration(t *testing.T) {
	got := lexAll(t, "1.5s")
	if len(got) != 3 || got[0].kind != kFloat || got[0].text != "1.5" || got[1].kind != kWord || got[1].text != "s" {
		t.Fatalf("1.5s lexed as %s %s, %s %s", got[0].text, got[1].text, got[0].String(), got[1].String())
	}
}

func TestLexSpacedDurationIsTwoTokens(t *testing.T) {
	got := lexAll(t, "10 s")
	if got[0].kind != kInt || got[1].kind != kWord || got[1].text != "s" {
		t.Fatalf("10 s lexed as %s then %s", got[0].text, got[1].text)
	}
}

func TestLexDurationUnits(t *testing.T) {
	cases := []struct {
		src  string
		want time.Duration
	}{
		{"500ms", 500 * time.Millisecond},
		{"10s", 10 * time.Second},
		{"1m", time.Minute},
		{"10ms", 10 * time.Millisecond},
	}
	for _, tc := range cases {
		got := lexAll(t, tc.src)
		if got[0].kind != kDuration || got[0].dur != tc.want {
			t.Fatalf("%s -> %s %s", tc.src, got[0].text, got[0].dur)
		}
	}
}

func TestLexDurationBoundary(t *testing.T) {
	cases := []struct{ src, word string }{
		{"10seconds", "seconds"},
		{"5min", "min"},
		{"10meg", "meg"},
		{"3msx", "msx"},
		{"10s5", "s5"},
		{"10s_", "s_"},
	}
	for _, tc := range cases {
		l := newLexer("t.slate", []byte(tc.src))
		_, err := l.Next()
		want := `unknown duration unit "` + tc.word + `"`
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", tc.src, err)
		}
	}
	// A unit followed by a space, a brace or the end is a duration.
	for _, src := range []string{"10s ", "10ms}", "1m\n", "10s#x"} {
		if got := lexAll(t, src); got[0].kind != kDuration {
			t.Fatalf("%q lexed as %s", src, got[0].text)
		}
	}
}

func TestLexUUIDBoundary(t *testing.T) {
	id := "17ca7e57-7e57-c0de-ef46-5feae5a71169"
	for _, tail := range []string{"xyz", "0", "_", "a"} {
		l := newLexer("t.slate", []byte(id+tail))
		_, err := l.Next()
		if err == nil || !strings.Contains(err.Error(), "a UUID is followed by more characters") {
			t.Fatalf("%s: %v", tail, err)
		}
	}
	for _, src := range []string{id + " ", id + "}", id + "\n", id + "#c"} {
		if got := lexAll(t, src); got[0].kind != kUUID {
			t.Fatalf("%q lexed as %s", src, got[0].text)
		}
	}
}

func TestLexBraces(t *testing.T) {
	got := lexAll(t, "{test}{")
	want := []kind{kLBrace, kWord, kRBrace, kLBrace, kEOF}
	if len(got) != len(want) {
		t.Fatalf("%d tokens", len(got))
	}
	for i, k := range want {
		if got[i].kind != k {
			t.Fatalf("token %d kind %d, want %d", i, got[i].kind, k)
		}
	}
	if got[0].String() != "{" || got[2].String() != "}" {
		t.Fatalf("%s %s", got[0].String(), got[2].String())
	}
}

func TestLexUUIDBeforeInteger(t *testing.T) {
	got := lexAll(t, "17CA7E57-7E57-C0DE-EF46-5FEAE5A71169")
	if len(got) != 2 || got[0].kind != kUUID {
		t.Fatalf("kind %d text %s", got[0].kind, got[0].text)
	}
	if got[0].text != "17ca7e57-7e57-c0de-ef46-5feae5a71169" {
		t.Fatalf("canonical %s", got[0].text)
	}
}

func TestLexMoneyWithAndWithoutSpace(t *testing.T) {
	for _, src := range []string{"L$5", "L$ 5"} {
		got := lexAll(t, src)
		if got[0].kind != kMoney || got[1].kind != kInt || got[1].text != "5" {
			t.Fatalf("%s -> %s %s", src, got[0].text, got[1].text)
		}
	}
}

func TestLexDotNeedsDigits(t *testing.T) {
	for _, src := range []string{".5", "5.", "-.5", "5.s"} {
		l := newLexer("t.slate", []byte(src))
		_, err := l.Next()
		if err == nil || !strings.Contains(err.Error(), "a number needs digits on both sides of the dot") {
			t.Fatalf("%s: %v", src, err)
		}
	}
}

func TestLexLoneDotIsIllegal(t *testing.T) {
	l := newLexer("t.slate", []byte("."))
	_, err := l.Next()
	if err == nil || !strings.Contains(err.Error(), "illegal character '.'") {
		t.Fatalf("%v", err)
	}
}

func TestLexStringEscapes(t *testing.T) {
	got := lexAll(t, `"a\"b\\c\n\t"`)
	if got[0].kind != kString || got[0].text != "a\"b\\c\n\t" {
		t.Fatalf("%q", got[0].text)
	}
}

func TestLexStringErrors(t *testing.T) {
	cases := []struct{ src, want string }{
		{"\"abc", "unterminated string"},
		{"\"ab\n\"", "a string cannot contain a newline"},
		{"\"\\q\"", "unknown string escape"},
		{"\"abc\\", "truncated string escape"},
		{"\"\x00\"", "NUL"},
		{"# comment \x00", "NUL"},
		{"\xff", "invalid UTF-8"},
	}
	for _, tc := range cases {
		l := newLexer("t.slate", []byte(tc.src))
		_, err := l.Next()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%q: %v", tc.src, err)
		}
	}
}

func TestLexHashInsideAStringIsNotAComment(t *testing.T) {
	got := lexAll(t, "\"Example # HUD\" hud")
	if got[0].text != "Example # HUD" || got[1].text != "hud" {
		t.Fatalf("%q %q", got[0].text, got[1].text)
	}
}

func TestLexIntegerThatDoesNotFit(t *testing.T) {
	got := lexAll(t, strings.Repeat("9", 40))
	if got[0].kind != kInt || got[0].intOK {
		t.Fatalf("kind %d ok %v", got[0].kind, got[0].intOK)
	}
}

func TestLexDurationOverflow(t *testing.T) {
	l := newLexer("t.slate", []byte(strings.Repeat("9", 20)+"m"))
	_, err := l.Next()
	if err == nil || !strings.Contains(err.Error(), "duration overflows") {
		t.Fatalf("%v", err)
	}
}

func TestLexFloatOverflow(t *testing.T) {
	l := newLexer("t.slate", []byte("1"+strings.Repeat("0", 400)+".0"))
	_, err := l.Next()
	if err == nil || !strings.Contains(err.Error(), "number does not fit in a float") {
		t.Fatalf("%v", err)
	}
}

func TestLexIllegalCharacter(t *testing.T) {
	l := newLexer("t.slate", []byte(";"))
	_, err := l.Next()
	if err == nil || !strings.Contains(err.Error(), "illegal character ';'") {
		t.Fatalf("%v", err)
	}
}
