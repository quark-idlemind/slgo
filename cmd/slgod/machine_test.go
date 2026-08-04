package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
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
