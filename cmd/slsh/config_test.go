package main

// Settings: the table, the file they are read from, the file they are
// written back to, and the key that leaves chat mode.
//
// Two things here are worth more than the rest.  The table and the
// reader have to agree, or a setting is one that can be typed and not
// read, or read and not listed -- see the round trip below, which is
// the test that keeps a new row honest.  And the writer must leave
// everything it did not come for alone, because a settings file is
// edited by hand and its comments are somebody's notes.
//
// ParseKey is the rest of the work.  The prefix key has to be able to
// be anything, including the control characters that are the only keys
// a terminal has left over, so it accepts five spellings of the same
// thing -- and a misreading there is a shell whose escape key does
// something else, which is not a thing anyone debugs quickly.

import (
	"fmt"
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

// TestEverySettingCanBeWrittenDownAndReadBack.
//
// The one test that keeps the table honest.  Every setting is written
// out through its own show, read back through the reader, and the whole
// Config compared -- so a row whose show and parse disagree, or a field
// the reader never reaches, fails here rather than at somebody's
// prompt.  It is also what says a listing can be pasted into a file:
// what "set" prints is what LoadConfig takes.
func TestEverySettingCanBeWrittenDownAndReadBack(t *testing.T) {
	// Nothing here is a default, so a setting the reader quietly
	// ignores comes back as the default and is caught.
	want := DefaultConfig()
	want.Addr = "lab.local:7807"
	want.Agent = "somebody"
	want.Prefix = 7
	want.ViewerApp = "Another-Viewer"
	want.ViewerGrid = "another-grid"
	want.ViewerLaunch = "run {app} --grid {grid} {first} {last} {password}"
	want.ViewerRunning = "pgrep {app}"
	want.MapRows = 20
	want.MapSpan = 48
	want.MapRatio = CellRatio{Tall: 2, Wide: 1}
	want.MapLevel = 5
	want.MapFriendColour = "bright cyan"

	var b strings.Builder
	for _, s := range settings {
		fmt.Fprintf(&b, "%s = %s\n", s.name, s.show(&want))
	}
	writeConfig(t, b.String())

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("a file of every setting should load: %v", err)
	}
	if got != want {
		t.Errorf("LoadConfig = %+v\nwant %+v\nfrom\n%s", got, want, b.String())
	}

	// And every one of them was in that file: a table row that showed
	// nothing would otherwise pass the comparison above by writing a
	// line the reader could ignore.
	for _, s := range settings {
		if !strings.Contains(b.String(), s.name+" = ") {
			t.Errorf("%s wrote no line of its own:\n%s", s.name, b.String())
		}
	}
}

// TestTheOlderSpellingsStillLoad, since somebody's file has them in it.
func TestTheOlderSpellingsStillLoad(t *testing.T) {
	for _, s := range settings {
		for _, a := range s.also {
			got, ok := findSetting(a)
			if !ok || got.name != s.name {
				t.Errorf("%q should still mean %s", a, s.name)
			}
		}
	}
	// And in either case, since a file is typed by hand.
	if got, ok := findSetting("  PREFIX_KEY "); !ok || got.name != "escape" {
		t.Errorf("a name should be found whatever case and spacing it was written in")
	}
}

// TestARatioIsHeightThenWidth, which is the one number in the settings
// that cannot be checked by looking at what it draws.
func TestARatioIsHeightThenWidth(t *testing.T) {
	r, err := ParseCellRatio(" 7 : 3 ")
	if err != nil {
		t.Fatalf("ParseCellRatio: %v", err)
	}
	if r.Tall != 7 || r.Wide != 3 {
		t.Errorf("ParseCellRatio(\"7:3\") = %+v, want 7 tall and 3 wide", r)
	}
	// What it prints has to parse back to the same shape, or "set"
	// would print a ratio the file would refuse.
	if r.String() != "7:3" {
		t.Errorf("CellRatio.String = %q, want \"7:3\"", r.String())
	}
	if back, err := ParseCellRatio(r.String()); err != nil || back != r {
		t.Errorf("ParseCellRatio(%q) = %+v, %v", r.String(), back, err)
	}

	for _, c := range []struct{ text, want string }{
		{"7", "want height:width"},
		{"seven:three", "want height:width"},
		{"7:", "want height:width"},
		{"0:3", "between 1 and 20"},
		{"7:0", "between 1 and 20"},
		{"-7:3", "between 1 and 20"},
		{"70:3", "between 1 and 20"},
	} {
		_, err := ParseCellRatio(c.text)
		if err == nil {
			t.Errorf("ParseCellRatio(%q) should have been refused", c.text)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("ParseCellRatio(%q): refusal should mention %q, got %v", c.text, c.want, err)
		}
	}
}

// TestAValueASettingWillNotTakeIsRefusedByName.
//
// A file has a dozen settings in it and the value being complained
// about is often one copied from another line, so "want a whole number"
// on its own leaves somebody looking down the file for which line it
// was -- which is also why the line number is still there.
func TestAValueASettingWillNotTakeIsRefusedByName(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"map_rows = lots\n", "map_rows: want a whole number"},
		{"map_rows = 1\n", "map_rows: want 2 or more"},
		{"map_rows = 400\n", "map_rows: 400 is more than the 64 rows"},
		{"map_span = 1\n", "map_span: want 2 or more"},
		{"map_level = 0\n", "map_level: want 1 or more"},
		{"map_ratio = sideways\n", "map_ratio: want height:width"},
		{"map_friend_colour = puce\n", "map_friend_colour: no colour called"},
		{"escape = not a key\n", "escape: \"not a key\" is not a key"},
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
		if !strings.Contains(err.Error(), "line 1") {
			t.Errorf("%q: refusal should name the line, got %v", c.body, err)
		}
	}

	// A refused value leaves the colour it names in the message, since
	// the answer to "puce" is the list of what there is.
	writeConfig(t, "map_friend_colour = puce\n")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "green") {
		t.Errorf("a refused colour should say what the colours are, got %v", err)
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

// mustSetting is the row for a name, for the tests below.
func mustSetting(t *testing.T, name string) setting {
	t.Helper()
	s, ok := findSetting(name)
	if !ok {
		t.Fatalf("there is no setting called %q", name)
	}
	return s
}

// readConfig is what is in the settings file now.
func readConfig(t *testing.T) string {
	t.Helper()
	path, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the settings file back: %v", err)
	}
	return string(b)
}

// TestWritingASettingChangesOneLineAndLeavesTheRestOfTheFile.
//
// The point of not writing the file out of the struct.  A settings file
// is edited by hand and the comments in it are somebody's notes, so a
// command that rewrote it from memory would throw them away silently
// the first time it was used -- and the person who lost them would have
// no way of knowing what it was that did it.
func TestWritingASettingChangesOneLineAndLeavesTheRestOfTheFile(t *testing.T) {
	const before = `# The settings for this machine.
#
# The grid nickname has to exist in the viewer already: Preferences,
# OpenSim, add the login URI, and give it this name.

addr   = lab.local:7807
agent  = somebody

# The map is drawn to the shape of this font.
map_ratio = 2:1

viewer_grid    = slgod
`
	writeConfig(t, before)

	if _, err := saveSetting(mustSetting(t, "map_ratio"), "7:3"); err != nil {
		t.Fatalf("saveSetting: %v", err)
	}

	want := strings.Replace(before, "map_ratio = 2:1", "map_ratio = 7:3", 1)
	if got := readConfig(t); got != want {
		t.Errorf("the file is now\n%s\nand should be\n%s", got, want)
	}

	// Every comment is still there, said outright: the line-for-line
	// comparison above would pass a file that had lost one if the
	// replacement had eaten it, and this is the sentence that names
	// what was actually being protected.
	for _, note := range []string{
		"# The settings for this machine.",
		"# OpenSim, add the login URI, and give it this name.",
		"# The map is drawn to the shape of this font.",
	} {
		if !strings.Contains(readConfig(t), note) {
			t.Errorf("the comment %q was lost", note)
		}
	}

	// And the file still loads, with the new value and the old ones.
	c, err := LoadConfig()
	if err != nil {
		t.Fatalf("the rewritten file should still load: %v", err)
	}
	if c.MapRatio != (CellRatio{Tall: 7, Wide: 3}) || c.Addr != "lab.local:7807" || c.ViewerGrid != "slgod" {
		t.Errorf("LoadConfig = %+v", c)
	}
}

// TestWritingASettingKeepsTheSpellingAndTheSpacingOfItsLine.
//
// Only the text after the "=" is touched, so a file whose values are
// lined up in a column stays lined up, and a file that says "server"
// goes on saying it.  Somebody who tidied their file should not find
// "set" untidying it.
func TestWritingASettingKeepsTheSpellingAndTheSpacingOfItsLine(t *testing.T) {
	writeConfig(t, "  server         = old.local:7807\nagent = somebody\n")

	if _, err := saveSetting(mustSetting(t, "addr"), "new.local:7807"); err != nil {
		t.Fatalf("saveSetting: %v", err)
	}
	want := "  server         = new.local:7807\nagent = somebody\n"
	if got := readConfig(t); got != want {
		t.Errorf("the file is now %q, want %q", got, want)
	}
}

// TestASettingTheFileDoesNotMentionIsAppended, rather than the rest of
// the file being rewritten around it.
func TestASettingTheFileDoesNotMentionIsAppended(t *testing.T) {
	writeConfig(t, "# a note\naddr = lab.local:7807\n")

	if _, err := saveSetting(mustSetting(t, "map_span"), "48"); err != nil {
		t.Fatalf("saveSetting: %v", err)
	}
	want := "# a note\naddr = lab.local:7807\nmap_span = 48\n"
	if got := readConfig(t); got != want {
		t.Errorf("the file is now %q, want %q", got, want)
	}

	// A file whose last line has no newline on it would otherwise have
	// the new setting run onto the end of it.
	writeConfig(t, "addr = lab.local:7807")
	if _, err := saveSetting(mustSetting(t, "map_span"), "48"); err != nil {
		t.Fatalf("saveSetting: %v", err)
	}
	if got, want := readConfig(t), "addr = lab.local:7807\nmap_span = 48\n"; got != want {
		t.Errorf("the file is now %q, want %q", got, want)
	}
}

// TestTheLineThatChangesIsTheOneInForce.
//
// The reader takes the file from the top and lets each line overwrite
// what came before, so in a file that names a setting twice it is the
// last one that decides.  Changing any other would be a "set" that
// wrote the file and changed nothing anybody could see -- and a
// commented-out setting is a note, which is not a line to write over
// either.
func TestTheLineThatChangesIsTheOneInForce(t *testing.T) {
	writeConfig(t, "# map_span = 16\nmap_span = 32\nmap_span = 64\n")

	if _, err := saveSetting(mustSetting(t, "map_span"), "48"); err != nil {
		t.Fatalf("saveSetting: %v", err)
	}
	if got, want := readConfig(t), "# map_span = 16\nmap_span = 32\nmap_span = 48\n"; got != want {
		t.Errorf("the file is now %q, want %q", got, want)
	}
	c, err := LoadConfig()
	if err != nil || c.MapSpan != 48 {
		t.Errorf("the file should now load as 48 metres: %+v, %v", c, err)
	}
}

// TestASettingsFileThatIsNotThereIsCreated, with its directory, since
// most machines have neither until the first "set".
func TestASettingsFileThatIsNotThereIsCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not", "there", "yet")
	t.Setenv("SLSH_CONFIG_DIR", dir)

	path, err := saveSetting(mustSetting(t, "map_friend_colour"), "bright cyan")
	if err != nil {
		t.Fatalf("saveSetting: %v", err)
	}
	if want := filepath.Join(dir, "config"); path != want {
		t.Errorf("saveSetting wrote %q, want %q", path, want)
	}
	if got, want := readConfig(t), "map_friend_colour = bright cyan\n"; got != want {
		t.Errorf("the new file is %q, want %q", got, want)
	}
	c, err := LoadConfig()
	if err != nil || c.MapFriendColour != "bright cyan" {
		t.Errorf("the new file should load: %+v, %v", c, err)
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
