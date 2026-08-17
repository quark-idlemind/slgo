package main

// What "set" says and what it leaves behind.
//
// Two things are checked of every change: what the shell is doing now,
// and what the file says -- because the whole point of the command is
// that the second outlives the first.  The settings file is a temporary
// directory throughout, since a test that wrote to somebody's real one
// would be a test that changed the shell they use.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// newSettingShell is a shell with the settings a real one comes up
// with, and a settings file of its own to write to.
//
// DefaultConfig rather than the bare Config the other tests use: this
// is the command that prints every setting, and half a config would
// print half of them as empty.
func newSettingShell(t *testing.T) *testShell {
	t.Helper()
	t.Setenv("SLSH_CONFIG_DIR", t.TempDir())
	cfg := DefaultConfig()
	cfg.Addr = "fake:7807"
	return newTestShellOn(t, newFakeGrid(t), cfg)
}

// settingsFile is what has been written to the file so far, and "" for
// a file nothing has written to yet.
func settingsFile(t *testing.T) string {
	t.Helper()
	path, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}

// TestSetWithNoArgumentsListsEverySetting, which is what a shell's set
// does and the reason the command is called that.
func TestSetWithNoArgumentsListsEverySetting(t *testing.T) {
	x := newSettingShell(t)
	got := x.do(t, "set")

	for _, s := range settings {
		if !strings.Contains(got, s.name) {
			t.Errorf("set should list %q:\n%s", s.name, got)
		}
		// With what it is for, since a name on its own is no answer to
		// "what can I set".
		if !strings.Contains(got, s.about) {
			t.Errorf("set should say what %s is for:\n%s", s.name, got)
		}
	}
	// And the values, so that the listing answers the other question
	// somebody has at the same moment.
	for _, want := range []string{"fake:7807", "16", "64", "7:3", "green", "ESC"} {
		if !strings.Contains(got, want) {
			t.Errorf("set should show the value %q:\n%s", want, got)
		}
	}
	// In alphabetical order, as every list of names in this shell is:
	// it is read by somebody looking for a name they already have in
	// mind.  See sortedNames in groups.go.
	at := -1
	for _, name := range settingNames() {
		i := strings.Index(got, "\n"+name+" ")
		if i < 0 {
			i = strings.Index(got, name+" ")
		}
		if i < at {
			t.Errorf("%q is listed out of order:\n%s", name, got)
		}
		at = i
	}

	// A value nobody has filled in is said rather than left as a gap,
	// which reads as a listing that has gone wrong.
	if !strings.Contains(got, "(empty)") {
		t.Errorf("an empty setting should say so:\n%s", got)
	}
	// And where the changes will go, since that is the file somebody
	// would otherwise have to guess at.
	path, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, path) {
		t.Errorf("set should name the file it writes to:\n%s", got)
	}

	// Nothing has been written: listing is not changing.
	if body := settingsFile(t); body != "" {
		t.Errorf("listing the settings wrote to the file:\n%s", body)
	}
}

// TestSetWithOneNameShowsThatOne, for when the name is remembered and
// the value is not.
func TestSetWithOneNameShowsThatOne(t *testing.T) {
	x := newSettingShell(t)
	got := x.do(t, "set map_ratio")

	if !strings.Contains(got, "7:3") {
		t.Errorf("set map_ratio should show its value:\n%s", got)
	}
	if strings.Contains(got, "map_span") {
		t.Errorf("set map_ratio should show one setting, not all of them:\n%s", got)
	}
}

// TestSetChangesTheShellAndRemembersIt.
//
// Both halves, because either alone is a command that half works: a
// change that is not written is one to type again tomorrow, and one
// that is written and not applied is a command that looks as though it
// did nothing.
func TestSetChangesTheShellAndRemembersIt(t *testing.T) {
	x := newSettingShell(t)
	got := x.do(t, "set map_span 32")

	if !strings.Contains(got, "map_span = 32") {
		t.Errorf("set should say what the setting is now:\n%s", got)
	}
	if x.cfg.MapSpan != 32 {
		t.Errorf("the shell is still drawing %d metres across", x.cfg.MapSpan)
	}
	if body := settingsFile(t); !strings.Contains(body, "map_span = 32") {
		t.Errorf("the file should have it too:\n%s", body)
	}
	// The path is printed, since the file is the part that will still
	// be true tomorrow.
	if !strings.Contains(got, "written to ") {
		t.Errorf("set should say where it wrote:\n%s", got)
	}

	// And the next picture is drawn to it, which is the whole claim.
	if drawn := x.do(t, "map"); !strings.Contains(drawn, "32m x 32m") {
		t.Errorf("the next map should cover 32 metres:\n%s", drawn)
	}

	// A setting that is read back is the setting that was written: a
	// second shell over the same file comes up with it.
	c, err := LoadConfig()
	if err != nil || c.MapSpan != 32 {
		t.Errorf("the file should load as 32 metres: %+v, %v", c, err)
	}
}

// TestSetPrintsTheValueTheFileWillHave, rather than echoing what was
// typed: the two differ whenever a setting has one spelling of its own.
func TestSetPrintsTheValueTheFileWillHave(t *testing.T) {
	x := newSettingShell(t)
	got := x.do(t, "set escape ^g")

	if !strings.Contains(got, "escape = ^G") {
		t.Errorf("set escape should answer in the spelling the file keeps:\n%s", got)
	}
	if body := settingsFile(t); !strings.Contains(body, "escape = ^G") {
		t.Errorf("the file should have the same spelling:\n%s", body)
	}
}

// TestASettingThatCannotTakeYetSaysSo.
//
// The address was used to attach this session before there was a prompt
// to type at, so changing it now changes the next slsh and not this
// one.  A command that printed the new value and left it at that would
// be claiming something that is not true.
func TestASettingThatCannotTakeYetSaysSo(t *testing.T) {
	x := newSettingShell(t)
	got := x.do(t, "set addr lab.local:7807")

	if !strings.Contains(got, startupNote) {
		t.Errorf("a startup-only setting should say it has not taken:\n%s", got)
	}
	if x.cfg.Addr != "fake:7807" {
		t.Errorf("this shell's address changed to %q under it", x.cfg.Addr)
	}
	if body := settingsFile(t); !strings.Contains(body, "addr = lab.local:7807") {
		t.Errorf("it should still have been written:\n%s", body)
	}

	// The listing says it too, since that is where somebody decides
	// what to type -- finding out afterwards is finding out too late.
	if listed := x.do(t, "set"); !strings.Contains(listed, "at startup only") {
		t.Errorf("the listing should mark the settings that wait:\n%s", listed)
	}

	// And the ones that are not marked take at once, or the mark would
	// mean nothing.
	if got := x.do(t, "set map_level 5"); strings.Contains(got, startupNote) {
		t.Errorf("map_level takes at once and should not say otherwise:\n%s", got)
	}
	if x.cfg.MapLevel != 5 {
		t.Errorf("map_level did not take: %d", x.cfg.MapLevel)
	}
}

// TestSetRefusesAValueAndChangesNothing.  A refusal that had already
// written the file would be the worst of both: a shell drawing one
// thing and a file saying another.
func TestSetRefusesAValueAndChangesNothing(t *testing.T) {
	x := newSettingShell(t)
	for _, c := range []struct{ line, want string }{
		{"set map_ratio sideways", "map_ratio: want height:width"},
		{"set map_rows 400", "map_rows: 400 is more than"},
		{"set map_friend_colour puce", "map_friend_colour: no colour called"},
		{"set escape enter", "escape: \"enter\" would leave no way"},
	} {
		got := x.do(t, c.line)
		if !strings.Contains(got, c.want) {
			t.Errorf("%q should be refused with %q, got:\n%s", c.line, c.want, got)
		}
		if body := settingsFile(t); body != "" {
			t.Errorf("%q wrote to the file anyway:\n%s", c.line, body)
		}
	}
	if x.cfg != DefaultConfig() && x.cfg.MapRatio != mapDefaultRatio {
		t.Errorf("a refused value changed the shell: %+v", x.cfg)
	}
}

// TestASettingThatCannotBeWrittenIsNotAppliedEither.
//
// The one outcome nobody could work out afterwards is a change this
// shell took and the file did not: the picture would be drawn one way
// today and another way tomorrow, with nothing having said so.  So a
// file that cannot be written leaves the shell as it was, and says why.
func TestASettingThatCannotBeWrittenIsNotAppliedEither(t *testing.T) {
	x := newSettingShell(t)
	// No settings directory to be worked out at all, which is the one
	// failure that does not depend on the filesystem's own permissions.
	t.Setenv("SLSH_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	got := x.do(t, "set map_span 32")
	if !strings.Contains(got, "map_span") || !strings.Contains(got, "home directory") {
		t.Errorf("set should say which setting could not be written, and why:\n%s", got)
	}
	if x.cfg.MapSpan != mapDefaultSpan {
		t.Errorf("the shell took a setting it could not remember: %d", x.cfg.MapSpan)
	}
}

// TestSetNamesTheSettingsWhenItIsGivenOneThatIsNot.
//
// The commonest way to type a name that is not there is a near miss --
// map_colour for map_friend_colour -- and the answer to that is the
// list, not "try set".
func TestSetNamesTheSettingsWhenItIsGivenOneThatIsNot(t *testing.T) {
	x := newSettingShell(t)
	got := x.do(t, "set map_colour green")

	if !strings.Contains(got, "no setting called") {
		t.Errorf("set should refuse a name that is not a setting:\n%s", got)
	}
	if !strings.Contains(got, "map_friend_colour") {
		t.Errorf("the refusal should name the settings there are:\n%s", got)
	}
}

// TestAValueMayHaveSpacesInIt, since several of these are command lines
// and quoting one at a prompt is a thing to have to remember.
func TestAValueMayHaveSpacesInIt(t *testing.T) {
	x := newSettingShell(t)
	x.do(t, "set viewer_launch run {app} --grid {grid} {first} {last} {password}")

	want := "run {app} --grid {grid} {first} {last} {password}"
	if x.cfg.ViewerLaunch != want {
		t.Errorf("viewer_launch is %q, want %q", x.cfg.ViewerLaunch, want)
	}
	if body := settingsFile(t); !strings.Contains(body, "viewer_launch = "+want) {
		t.Errorf("the file should have the whole line:\n%s", body)
	}
}

// TestTheFriendColourSetByNameReachesThePicture, which is the far end
// of the setting: a name typed at the prompt, and the escape it stands
// for in the characters the terminal is sent.
func TestTheFriendColourSetByNameReachesThePicture(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	x := newSettingShell(t)
	x.term.plain = false
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testSomebody, Local: 2}, PCode: pcodeAvatar,
			Position: msg.Vector3{X: 128, Y: 138, Z: 25}},
	}
	x.grid.friends = []sl.Friend{{ID: testSomebody, Online: true}}

	if got := x.do(t, "map"); !strings.Contains(got, mapColours["green"]) {
		t.Errorf("a friend should be green until somebody says otherwise:\n%q", got)
	}

	x.do(t, "set map_friend_colour bright cyan")
	got := x.do(t, "map")
	if !strings.Contains(got, mapColours["bright cyan"]) {
		t.Errorf("the friend should now be bright cyan:\n%q", got)
	}
	if strings.Contains(got, mapColours["green"]) {
		t.Errorf("nothing should still be green:\n%q", got)
	}
	// The legend says the colour it is showing, in that colour, since
	// the name is what has to be typed to change it again.
	if want := mapColours["bright cyan"] + "bright cyan" + mapColourOff; !strings.Contains(got, want) {
		t.Errorf("the legend should name the colour it is using:\n%q", got)
	}
}

// TestAMapDrawnToTheSettingsIsDrawnToTheSettings: the rows and the
// ratio reach the picture the same way, and the flags still beat them
// for one command.
func TestAMapDrawnToTheSettingsIsDrawnToTheSettings(t *testing.T) {
	x := newSettingShell(t)

	// The default: sixteen rows at 7:3, which is 37 columns.
	if got := x.do(t, "map"); !strings.Contains(got, "+"+strings.Repeat("-", 37)+"+") {
		t.Errorf("the default picture should be 37 columns wide:\n%s", got)
	}

	// Two to one is what it drew before anybody measured a font, and
	// is 32 columns at the same sixteen rows.
	x.do(t, "set map_ratio 2:1")
	if got := x.do(t, "map"); !strings.Contains(got, "+"+strings.Repeat("-", 32)+"+") {
		t.Errorf("2:1 should draw 32 columns:\n%s", got)
	}

	// And a flag is for one command: it does not write anything and the
	// next picture is the settings' again.
	x.do(t, "set map_ratio 7:3")
	if got := x.do(t, "map --rows 8"); !strings.Contains(got, "+"+strings.Repeat("-", 19)+"+") {
		t.Errorf("--rows 8 at 7:3 should draw 19 columns:\n%s", got)
	}
	if x.cfg.MapRows != mapDefaultRows {
		t.Errorf("--rows changed the setting to %d", x.cfg.MapRows)
	}
	if got := x.do(t, "map"); !strings.Contains(got, "+"+strings.Repeat("-", 37)+"+") {
		t.Errorf("the picture after a flag should be the settings' again:\n%s", got)
	}
}

// TestSetWritesToTheFileTheRestOfSlshReads, which is the one thing that
// makes it worth writing at all.
func TestSetWritesToTheFileTheRestOfSlshReads(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SLSH_CONFIG_DIR", dir)
	// A file with something in it already, since that is the case where
	// a careless writer does damage.
	if err := os.WriteFile(filepath.Join(dir, "config"),
		[]byte("# mine\nagent = somebody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.Addr = "fake:7807"
	x := newTestShellOn(t, newFakeGrid(t), cfg)
	x.do(t, "set map_rows 24")

	c, err := LoadConfig()
	if err != nil {
		t.Fatalf("the file should still load: %v", err)
	}
	if c.MapRows != 24 || c.Agent != "somebody" {
		t.Errorf("LoadConfig = %+v, want 24 rows and the agent that was already there", c)
	}
}
