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
	flags.Preamble, flags.Postamble = "", ""
	flags.Params, flags.Locals, flags.Globals = nil, nil, nil
	// Everything a benchmark's shape and cost is decided by, back to
	// what the flag block starts with.  They are package level and a
	// test that left one set would change what the next one leases.
	flags.Extra, flags.Parts, searchesAtOnce = 4, 8, 1
}

// TestBaseKeyIgnoresTheCodeUnderTest is the whole reason this cache is
// worth having: the code being measured is not in the base script, so
// every benchmark of a shape wants the same answer.
func TestBaseKeyIgnoresTheCodeUnderTest(t *testing.T) {
	resetFlags()

	flags.Code = "integer gCNT;"
	first := baseKey()

	flags.Code = "list lCNT; string sCNT;"
	if second := baseKey(); second != first {
		t.Errorf("--code changed the base key: %s then %s", first, second)
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
	if _, err := os.Stat(filepath.Join(dir, "slbench-padding")); err != nil {
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
	if err := os.WriteFile(filepath.Join(dir, "slbench-padding"), []byte(body), 0o600); err != nil {
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

// TestNothingHereIsWorthFailingABenchmarkOver: this is a cache of a
// computation, not a record of anything in the world, so every way it can
// go wrong is a search that costs a dozen runs rather than an error.  A
// machine with nowhere to keep it is the extreme case: there is no file,
// there is no complaint, and the benchmark runs.
func TestNothingHereIsWorthFailingABenchmarkOver(t *testing.T) {
	// No configuration directory to be found at all.  HOME is what
	// os.UserConfigDir reads, and this is the test's own HOME, so the
	// developer's real cache is nowhere near it.
	t.Setenv("SLGO_CONFIG_DIR", "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	if _, err := padCachePath(); err == nil {
		t.Skip("this machine has a configuration directory without a home to put it in")
	}
	if got := loadPadCache(); len(got) != 0 {
		t.Errorf("a cache came back from nowhere: %v", got)
	}
	// And writing is the same: it does not happen, and it does not
	// complain.
	rememberPadding("abc", 377, 5924)
	forgetPadding("abc")
}

// TestACacheThatCannotBeWrittenIsNotAFailure: the same rule where the
// directory is there and cannot be written -- a read-only configuration
// directory is somebody's deliberate arrangement, and a benchmark is not
// the thing to argue with it.
func TestACacheThatCannotBeWrittenIsNotAFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "read-only")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLGO_CONFIG_DIR", dir)

	rememberPadding("abc", 377, 5924)
	if _, ok := loadPadCache()["abc"]; ok {
		t.Skip("this user can write to a directory without the write bit")
	}

	// A directory that cannot even be made is the same again.
	t.Setenv("SLGO_CONFIG_DIR", filepath.Join(dir, "under", "a", "read-only", "one"))
	rememberPadding("def", 349, 5960)
}

// TestForgettingSomethingNeverRememberedWritesNothing: forgetting is what
// happens when a remembered padding turns out not to hold, and the file
// is shared with every other shape this account has measured -- so a
// rewrite for an entry that was not there risks the others for nothing.
func TestForgettingSomethingNeverRememberedWritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SLGO_CONFIG_DIR", dir)
	rememberPadding("abc", 377, 5924)

	path := filepath.Join(dir, "slbench-padding")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	forgetPadding("never seen")

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Error("forgetting an entry that was not there rewrote the file")
	}
	if _, ok := loadPadCache()["abc"]; !ok {
		t.Error("the entry that was there did not survive")
	}
}
