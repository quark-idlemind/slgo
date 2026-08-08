package main

// Settings, and the key that leaves chat mode.
//
// ParseKey is where the work is.  The prefix key has to be able to be
// anything, including the control characters that are the only keys a
// terminal has left over, so it accepts five spellings of the same
// thing -- and a misreading there is a shell whose escape key does
// something else, which is not a thing anyone debugs quickly.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheDefaultAddrIsNobodysGuess.
//
// Empty rather than localhost: with nothing said in a file and nothing
// on the command line, where slgod runs is a question for sl-host, and
// a default here would answer it first.
func TestTheDefaultAddrIsNobodysGuess(t *testing.T) {
	c := DefaultConfig()
	if c.Addr != "" {
		t.Errorf("the default addr is %q, which would stop sl-host being asked", c.Addr)
	}
	if c.Prefix != 27 {
		t.Errorf("the default prefix key is %d, want ESC", c.Prefix)
	}
}

// TestConfigDirFollowsTheSameRuleTheProfilesDo.
func TestConfigDirFollowsTheSameRuleTheProfilesDo(t *testing.T) {
	t.Setenv("SLSH_CONFIG_DIR", "/named/outright")
	if got, err := ConfigDir(); got != "/named/outright" || err != nil {
		t.Errorf("ConfigDir = %q, %v", got, err)
	}

	t.Setenv("SLSH_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got, err := ConfigDir(); got != filepath.Join("/xdg", "slsh") || err != nil {
		t.Errorf("ConfigDir = %q, %v", got, err)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/somebody")
	if got, err := ConfigDir(); got != filepath.Join("/home/somebody", ".config", "slsh") || err != nil {
		t.Errorf("ConfigDir = %q, %v", got, err)
	}

	// No home at all is worth an error rather than a path under "".
	t.Setenv("HOME", "")
	if _, err := ConfigDir(); err == nil {
		t.Error("no home directory should be an error")
	}
}

// writeConfig puts a settings file where LoadConfig will find it.
func writeConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SLSH_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAMissingConfigFileIsAnEmptyOne, since most machines have none.
func TestAMissingConfigFileIsAnEmptyOne(t *testing.T) {
	t.Setenv("SLSH_CONFIG_DIR", t.TempDir())
	c, err := LoadConfig()
	if err != nil {
		t.Fatalf("a missing file should not be an error: %v", err)
	}
	if c != DefaultConfig() {
		t.Errorf("LoadConfig = %+v, want the defaults", c)
	}

	// A file that is there and will not open is not a missing file,
	// and starting with the defaults would ignore settings somebody
	// wrote down.
	dir := t.TempDir()
	t.Setenv("SLSH_CONFIG_DIR", dir)
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("addr = here:1\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Open(path); err == nil {
		t.Skip("this user can read a file with no permissions on it")
	}
	if _, err := LoadConfig(); err == nil {
		t.Error("a settings file that will not open should be reported")
	}

	// A config directory that cannot even be worked out is another
	// matter, and comes back as itself.
	t.Setenv("SLSH_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if _, err := LoadConfig(); err == nil {
		t.Error("no home directory should reach the caller")
	}
}

// TestConfigReadsEverySettingAndBothSpellings.
func TestConfigReadsEverySettingAndBothSpellings(t *testing.T) {
	writeConfig(t, "# a comment\n\n  server = lab.local:7807\nprofile = example\nprefix_key = ^G\n")

	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "lab.local:7807" || c.Agent != "example" || c.Prefix != 7 {
		t.Errorf("LoadConfig = %+v", c)
	}

	writeConfig(t, "addr = here:1\nagent = other\nescape = TAB\n")
	if c, err = LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if c.Addr != "here:1" || c.Agent != "other" || c.Prefix != 9 {
		t.Errorf("LoadConfig = %+v", c)
	}
}

// TestAMisspelledSettingIsRefused.
//
// Otherwise it is a setting that silently does nothing, which is worse
// than a file that will not load: the shell comes up looking right and
// behaving as though the line were not there.
func TestAMisspelledSettingIsRefused(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"addr localhost:7807\n", "want key = value"},
		{"adr = localhost:7807\n", "unknown setting"},
		{"escape = not a key\n", "is not a key"},
	} {
		writeConfig(t, c.body)
		_, err := LoadConfig()
		if err == nil {
			t.Errorf("%q should be refused", c.body)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: refusal should mention %q, got %v", c.body, c.want, err)
		}
		// And it says which line, since a config file is edited by
		// hand and the answer is usually "the one you just added".
		if !strings.Contains(err.Error(), "line 1") {
			t.Errorf("%q: refusal should name the line, got %v", c.body, err)
		}
	}
}

// TestParseKeyTakesEverySpellingOfAKey.
func TestParseKeyTakesEverySpellingOfAKey(t *testing.T) {
	for _, c := range []struct {
		text string
		want rune
	}{
		{"ESC", 27}, {"esc", 27}, {"escape", 27}, {"^[", 27}, {`\e`, 27},
		{"TAB", 9}, {"^I", 9}, {`\t`, 9},
		{"space", ' '},
		{"^G", 7}, {"C-g", 7}, {"c-G", 7}, {"ctrl-g", 7}, {"CTRL-G", 7},
		{"^@", 0}, {"^_", 31},
		{"0x07", 7}, {"7", 7}, {"27", 27},
		{"!", '!'}, {"~", '~'}, {"é", 'é'},
	} {
		got, err := ParseKey(c.text)
		if err != nil {
			t.Errorf("ParseKey(%q): %v", c.text, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseKey(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}

// TestParseKeyRefusesWhatWouldLeaveNoWayOut.
func TestParseKeyRefusesWhatWouldLeaveNoWayOut(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"", "no key given"},
		{"  ", "no key given"},
		// Enter would leave no way to send a message, which is a shell
		// that can be entered and not left.
		{"enter", "no way to send"},
		{"return", "no way to send"},
		{"^M", "no way to send"},
		// A control spelling of something that is not a control key.
		{"^~", "is not a control key"},
		{"ctrl-~", "is not a control key"},
		{"0", "is not a key"},
		{"-3", "is not a key"},
		{"0x200000", "is not a key"},
		{"two words", "give one character"},
	} {
		_, err := ParseKey(c.text)
		if err == nil {
			t.Errorf("ParseKey(%q) should have been refused", c.text)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("ParseKey(%q): refusal should mention %q, got %v", c.text, c.want, err)
		}
	}
}

// TestKeyNameIsParseKeyBackwards, since the banner has to be able to
// tell somebody which key to press.
func TestKeyNameIsParseKeyBackwards(t *testing.T) {
	for _, c := range []struct {
		r    rune
		want string
	}{
		{27, "ESC"}, {9, "TAB"}, {' ', "SPACE"}, {7, "^G"}, {1, "^A"}, {'!', "!"},
	} {
		if got := KeyName(c.r); got != c.want {
			t.Errorf("KeyName(%d) = %q, want %q", c.r, got, c.want)
		}
		// And what it prints has to parse back to the same key, or the
		// banner names a key the config file would refuse.
		if back, err := ParseKey(c.want); err != nil || back != c.r {
			t.Errorf("ParseKey(KeyName(%d)) = %d, %v", c.r, back, err)
		}
	}
}
