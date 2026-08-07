package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reset puts the flags back to a known state, since they are package
// level and every test here renders a script from them.
func resetFlags() {
	flags.Code, flags.Statement = "integer gCNT;", ""
	flags.Title, flags.Preamble, flags.Postamble, flags.Pad = "", "", "", ""
	flags.Params, flags.Locals, flags.Globals = nil, nil, nil
}

// TestBaseKeyIgnoresTheCodeUnderTest is the whole reason this cache is
// worth having: the code being measured is not in the base script, so
// every benchmark of a shape wants the same answer.
func TestBaseKeyIgnoresTheCodeUnderTest(t *testing.T) {
	resetFlags()
	flags.Title = "global integer"

	flags.Code = "integer gCNT;"
	first := baseKey()

	flags.Code = "list lCNT; string sCNT;"
	if second := baseKey(); second != first {
		t.Errorf("--code changed the base key: %s then %s", first, second)
	}
}

// TestBaseKeyFollowsTheTitleLength: a title is a string literal, so
// what it costs is its length.  Two titles of one length share an
// answer; a longer one does not, and must not -- measured live, a
// twenty-seven character title moved the padding from 377 to 349.
func TestBaseKeyFollowsTheTitleLength(t *testing.T) {
	resetFlags()

	flags.Title = "global integer" // 14
	same := baseKey()

	flags.Title = "global boolean" // also 14
	if got := baseKey(); got != same {
		t.Errorf("titles of one length should share a key: %s then %s", same, got)
	}

	flags.Title = "a considerably longer title"
	if got := baseKey(); got == same {
		t.Error("a longer title must not share the key; it moves the padding")
	}
}

// TestBaseKeyFollowsTheHarness: everything else that goes into the base
// script has to be in the key.
func TestBaseKeyFollowsTheHarness(t *testing.T) {
	resetFlags()
	bare := baseKey()

	for _, c := range []struct {
		what string
		set  func()
	}{
		{"--preamble", func() { flags.Preamble = "integer gExtra;" }},
		{"--postamble", func() { flags.Postamble = "// trailing" }},
		{"--pad", func() { flags.Pad = "integer gPad;" }},
	} {
		resetFlags()
		c.set()
		if got := baseKey(); got == bare {
			t.Errorf("%s did not change the base key", c.what)
		}
	}
}

// TestPadCacheRoundTrip: remembered, found, forgotten.
func TestPadCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SLGO_CONFIG_DIR", dir)

	if _, ok := loadPadCache()["nothing"]; ok {
		t.Error("an empty cache should hold nothing")
	}

	rememberPadding("abc", 377, 5924)
	rememberPadding("def", 349, 5960)

	m := loadPadCache()
	if e, ok := m["abc"]; !ok || e.Padding != 377 || e.BaseMem != 5924 {
		t.Errorf("got %+v ok=%v, want 377/5924", e, ok)
	}
	if e := m["def"]; e.Padding != 349 {
		t.Errorf("the second entry did not survive the first: %+v", e)
	}

	forgetPadding("abc")
	m = loadPadCache()
	if _, ok := m["abc"]; ok {
		t.Error("forgotten entry is still there")
	}
	if _, ok := m["def"]; !ok {
		t.Error("forgetting one entry took the other with it")
	}

	// Whatever else is in that directory is not ours to disturb.
	if _, err := os.Stat(filepath.Join(dir, "autobench-padding")); err != nil {
		t.Errorf("the cache file should be in the config directory: %v", err)
	}
}

// TestPadCacheSurvivesRubbish: a corrupt line is skipped, not fatal.
// Nothing here is worth failing a benchmark over.
func TestPadCacheSurvivesRubbish(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SLGO_CONFIG_DIR", dir)

	body := strings.Join([]string{
		"# a comment",
		"",
		"short",
		"badpad notanumber",
		"good 377 5924",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "autobench-padding"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	m := loadPadCache()
	if len(m) != 1 {
		t.Errorf("got %d entries, want only the good one: %v", len(m), m)
	}
	if m["good"].Padding != 377 {
		t.Errorf("the good line did not survive: %+v", m["good"])
	}
}

// TestBiggerThanABlock: -1 mode measures a copy as the distance between
// two block boundaries, so a copy bigger than a block leaves a
// remainder -- a small, plausible number that is not a size.
//
// Measured live, which is why this is here: a 500-character string is
// 1066 bytes in copy mode and came out of -1 mode as 4, and a
// 250-character one is 542 and came out as 20.
func TestBiggerThanABlock(t *testing.T) {
	const base = 5924

	cache = map[Cache]Results{}
	probeTest = map[Cache]int{}

	// With no base reading to compare against it says nothing, rather
	// than guessing at an answer it has no evidence for.
	if _, over := biggerThanABlock(378, base+9999); over {
		t.Error("fired with nothing to compare against")
	}

	cache[Cache{Count: 0, Padding: 378}] = Results{Base: base}

	// The numbers are the ones measured live against this same base.
	for _, c := range []struct {
		what string
		mem  int
		over bool
	}{
		{"a 24-byte integer, which added 8", base + 8, false},
		{"442 bytes of string, which added 422", base + 422, false},
		{"one byte under a block", base + blockSize - 1, false},
		{"542 bytes of string, which added 1034", base + 1034, true},
		{"1066 bytes of string, which added 2046", base + 2046, true},
		{"exactly a block", base + blockSize, true},
	} {
		added, over := biggerThanABlock(378, c.mem)
		if over != c.over {
			t.Errorf("%s: added %d, over = %v, want %v", c.what, added, over, c.over)
		}
	}
}
