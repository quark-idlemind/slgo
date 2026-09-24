package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/agent"
)

func tempMachineDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SLGOD_CONFIG_DIR", dir)
	return dir
}

var (
	macRE = regexp.MustCompile(`^[0-9A-F]{2}(:[0-9A-F]{2}){5}$`)
	id0RE = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// TestLoadMachineIDIsStable is the whole point: the second login has to
// come from the same computer as the first.
func TestLoadMachineIDIsStable(t *testing.T) {
	dir := tempMachineDir(t)

	first, path, err := loadMachineID()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "config"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if !macRE.MatchString(first.MAC) {
		t.Errorf("mac = %q, want six hex octets", first.MAC)
	}
	if !id0RE.MatchString(first.ID0) {
		t.Errorf("id0 = %q, want 32 hex digits", first.ID0)
	}

	second, _, err := loadMachineID()
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("identity changed between runs: %+v then %+v", first, second)
	}
}

// TestRandomMACIsLocallyAdministered: an invented address must not be
// one a real card could have.
func TestRandomMACIsLocallyAdministered(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		mac, err := randomMAC()
		if err != nil {
			t.Fatal(err)
		}
		if !macRE.MatchString(mac) {
			t.Fatalf("mac = %q", mac)
		}
		first, err := strconv.ParseUint(mac[:2], 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		if first&0x02 == 0 {
			t.Errorf("mac %q is not locally administered", mac)
		}
		if first&0x01 != 0 {
			t.Errorf("mac %q is a multicast address", mac)
		}
		seen[mac] = true
	}
	if len(seen) < 2 {
		t.Error("randomMAC returned the same address every time")
	}
}

// TestLoadMachineIDKeepsWhatIsWritten: a hand written file is honoured,
// not overwritten with something invented.
func TestLoadMachineIDKeepsWhatIsWritten(t *testing.T) {
	dir := tempMachineDir(t)
	body := "# mine\nmac = 02:00:00:00:00:01\nid0 = " + strings.Repeat("a", 32) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	m, _, err := loadMachineID()
	if err != nil {
		t.Fatal(err)
	}
	if m.MAC != "02:00:00:00:00:01" || m.ID0 != strings.Repeat("a", 32) {
		t.Errorf("identity = %+v", m)
	}
}

// TestLoadMachineIDCompletesHalfAFile: an operator who named a mac
// should not have to invent an id0 as well.
func TestLoadMachineIDCompletesHalfAFile(t *testing.T) {
	dir := tempMachineDir(t)
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("mac = 02:00:00:00:00:02\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m, _, err := loadMachineID()
	if err != nil {
		t.Fatal(err)
	}
	if m.MAC != "02:00:00:00:00:02" {
		t.Errorf("mac = %q, should have been left alone", m.MAC)
	}
	if !id0RE.MatchString(m.ID0) {
		t.Errorf("id0 = %q, want 32 hex digits", m.ID0)
	}

	// And the invented half is on disk, so the next run agrees.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), m.ID0) {
		t.Errorf("id0 was not written back:\n%s", b)
	}
}

// TestWhereTheMachineIdentityLives: this is slgod's own setting rather
// than an account's, so it goes under slgod and follows the same rules
// as everything else that is written per machine -- named outright, or
// under XDG_CONFIG_HOME, or under the home directory, in that order.
func TestWhereTheMachineIdentityLives(t *testing.T) {
	t.Setenv("SLGOD_CONFIG_DIR", "")

	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got, err := machineConfigDir(); err != nil || got != filepath.Join(xdg, "slgod") {
		t.Errorf("with XDG_CONFIG_HOME: %q, %v", got, err)
	}

	// Not os.UserConfigDir, deliberately: on macOS that points at
	// ~/Library/Application Support, and the rest of slgo keeps its
	// settings in ~/.config on every platform.
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".config", "slgod")
	if got, err := machineConfigDir(); err != nil || got != want {
		t.Errorf("with only a home directory: %q, %v", got, err)
	}
	if got, err := machineConfigPath(); err != nil || got != filepath.Join(want, "config") {
		t.Errorf("machineConfigPath = %q, %v", got, err)
	}

	// And with nowhere at all to put it, the daemon has to be told
	// rather than guess.
	t.Setenv("HOME", "")
	if _, err := machineConfigDir(); err == nil {
		t.Error("a machine with no home directory was given a config directory anyway")
	}
	if _, err := machineConfigPath(); err == nil {
		t.Error("machineConfigPath answered with no home directory")
	}
	if _, _, err := loadMachineID(); err == nil {
		t.Error("an identity was loaded from nowhere")
	}
}

// TestAnIdentityThatCannotBeWrittenIsAnError: the file is the whole
// point -- an identity that is not kept is a different computer every
// login, which is what an abuser looks like -- so failing to write it
// is a reason not to start rather than something to carry on past.
func TestAnIdentityThatCannotBeWrittenIsAnError(t *testing.T) {
	t.Run("the directory cannot be made", func(t *testing.T) {
		parent := readOnlyDir(t)
		t.Setenv("SLGOD_CONFIG_DIR", filepath.Join(parent, "slgod"))
		if _, _, err := loadMachineID(); err == nil {
			t.Error("an identity was written into a directory that could not be made")
		}
	})

	t.Run("the file cannot be made", func(t *testing.T) {
		// The directory is there and refuses to be written in, which is
		// what a config directory copied from somewhere else looks like.
		t.Setenv("SLGOD_CONFIG_DIR", readOnlyDir(t))
		if _, _, err := loadMachineID(); err == nil {
			t.Error("an identity was written into a directory that refuses writes")
		}
	})

	t.Run("what is there cannot be read", func(t *testing.T) {
		// A path whose parent is a file, which is a config directory
		// that is not one: not "no identity yet", which is the only
		// missing file that may be carried on past.
		notADir := filepath.Join(t.TempDir(), "notadir")
		if err := os.WriteFile(notADir, []byte("a file"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SLGOD_CONFIG_DIR", notADir)
		if _, _, err := loadMachineID(); err == nil {
			t.Error("a config directory that is a file was read as empty")
		}
	})
}

// readOnlyDir makes a directory nothing may be created in, and skips
// the test if this user can create things in one anyway.
func readOnlyDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probe"), nil, 0o600); err == nil {
		t.Skip("this user can write to a directory with no write permission")
	}
	return dir
}

func TestReadMachineIDRejectsALineThatIsNotASetting(t *testing.T) {
	dir := tempMachineDir(t)
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("mac 02:00:00:00:00:04\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A line with no "=" is a typo, and a setting that silently did
	// nothing would be worse than a refusal.
	if _, _, err := loadMachineID(); err == nil {
		t.Fatal("a line that is not key = value was accepted")
	} else if !strings.Contains(err.Error(), "want key = value") {
		t.Errorf("the error does not say what was expected: %v", err)
	}
}

func TestReadMachineIDRejectsUnknownSetting(t *testing.T) {
	dir := tempMachineDir(t)
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("macc = 02:00:00:00:00:03\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := loadMachineID(); err == nil {
		t.Fatal("expected a refusal for a misspelled setting")
	} else if !strings.Contains(err.Error(), "macc") {
		t.Errorf("error should name the setting: %v", err)
	}
}

// TestMachineConfigIsPrivate: the file is created 0600 under a 0700
// directory, like the rest of what slgo writes.
func TestMachineConfigIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "slgod")
	t.Setenv("SLGOD_CONFIG_DIR", dir)

	if _, path, err := loadMachineID(); err != nil {
		t.Fatal(err)
	} else if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if m := fi.Mode().Perm(); m != 0o600 {
		t.Errorf("config is mode %04o, want 0600", m)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m := fi.Mode().Perm(); m&0o077 != 0 {
		t.Errorf("directory is mode %04o, want 0700", m)
	}
}

// -config puts everything slgod keeps in one directory: the profiles
// and its own files both, so a second daemon started with another one
// reads none of the first's.  The directory must already be there and
// private, because it holds credentials.
func TestConfigDirHoldsProfilesAndTheDaemonsOwnFiles(t *testing.T) {
	t.Setenv("SLGO_CONFIG_DIR", "")
	t.Setenv("SLGOD_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", t.TempDir())

	dir := filepath.Join(t.TempDir(), "slgod.dev")
	if err := useConfigDir(dir); err == nil {
		t.Error("a directory that does not exist was taken")
	}

	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := useConfigDir(dir); err == nil {
		t.Error("a directory others can list was taken; it holds credentials")
	}

	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := useConfigDir(notADir); err == nil {
		t.Error("a file was taken as a config directory")
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := useConfigDir(dir); err != nil {
		t.Fatal(err)
	}
	if got, err := machineConfigDir(); err != nil || got != dir {
		t.Errorf("machineConfigDir = %q, %v; want %q", got, err, dir)
	}
	if got, err := agent.ConfigDir(); err != nil || got != dir {
		t.Errorf("agent.ConfigDir = %q, %v; want %q", got, err, dir)
	}
	if got, err := seatsPath(); err != nil || filepath.Dir(got) != dir {
		t.Errorf("seatsPath = %q, %v; want it in %q", got, err, dir)
	}

	// A profile there is one this daemon can log in, and the files the
	// daemon writes beside it are not mistaken for accounts.
	profile := "first = Example\nlast = Resident\npassword = $1$00157e577e57c0de028f000000000000\n"
	if err := os.WriteFile(filepath.Join(dir, "example"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadMachineID(); err != nil {
		t.Fatal(err)
	}
	names, err := agent.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "example" {
		t.Errorf("profiles = %q, want just example", names)
	}
}

// A relative -config is made absolute when it is given, so nothing
// that later changes the working directory can move it.
func TestConfigDirIsMadeAbsolute(t *testing.T) {
	t.Setenv("SLGO_CONFIG_DIR", "")
	t.Setenv("SLGOD_CONFIG_DIR", "")
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "dev"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(parent)
	if err := useConfigDir("dev"); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(parent, "dev"))
	got, _ := filepath.EvalSymlinks(os.Getenv("SLGOD_CONFIG_DIR"))
	if !filepath.IsAbs(os.Getenv("SLGOD_CONFIG_DIR")) || got != want {
		t.Errorf("SLGOD_CONFIG_DIR = %q, want %q", os.Getenv("SLGOD_CONFIG_DIR"), want)
	}
}
