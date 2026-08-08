package agent

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempConfig(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "slgo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLGO_CONFIG_DIR", dir)
	return dir
}

func writeProfile(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// testDigest is a digest of a made-up password, computed rather than
// written out.  A stored "$1$" digest is accepted by the login server
// IN PLACE OF the password, so one spelled out in a test file would be
// a working credential rather than an example of one.
var testDigest = func() string {
	sum := md5.Sum([]byte("example-password"))
	return "$1$" + hex.EncodeToString(sum[:])
}()

func TestLoadProfile(t *testing.T) {
	dir := tempConfig(t)
	writeProfile(t, dir, "example", `
# a comment
first    = Example
last     = Resident
password = `+testDigest+`
start    = the test region/188/203/28
group    = Builders
channel  = slgo
options  = login-flags, global-textures
`, 0o600)

	l, err := LoadProfile("example")
	if err != nil {
		t.Fatal(err)
	}
	if l.First != "Example" || l.Last != "Resident" {
		t.Errorf("names = %q %q", l.First, l.Last)
	}
	if l.Password != testDigest {
		t.Errorf("password = %q", l.Password)
	}
	if l.Start != "the test region/188/203/28" || l.Channel != "slgo" {
		t.Errorf("profile = %+v", l)
	}
	// The group belongs to the account, not to the invocation: a
	// login starts with none active, and a parcel that grants
	// building to a group refuses an avatar that has none.
	if l.Group != "Builders" {
		t.Errorf("group = %q, want %q", l.Group, "Builders")
	}
	if len(l.Options) != 2 || l.Options[1] != "global-textures" {
		t.Errorf("options = %v", l.Options)
	}
}

// TestLoadProfileRejectsLooseFile: a credentials file anyone can read
// is worth stopping for, the way ssh stops.
func TestLoadProfileRejectsLooseFile(t *testing.T) {
	dir := tempConfig(t)
	writeProfile(t, dir, "loose", "first = A\nlast = B\npassword = x\n", 0o644)

	_, err := LoadProfile("loose")
	if err == nil {
		t.Fatal("expected a refusal for a world readable profile")
	}
	if !strings.Contains(err.Error(), "0600") {
		t.Errorf("error should say what to do: %v", err)
	}
}

func TestLoadProfileRejectsLooseDirectory(t *testing.T) {
	dir := tempConfig(t)
	writeProfile(t, dir, "example", "first = A\nlast = B\npassword = x\n", 0o600)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	_, err := LoadProfile("example")
	if err == nil {
		t.Fatal("expected a refusal for a listable directory")
	}
	if !strings.Contains(err.Error(), "0700") {
		t.Errorf("error should say what to do: %v", err)
	}
}

func TestLoadProfileRejectsUnknownSetting(t *testing.T) {
	dir := tempConfig(t)
	writeProfile(t, dir, "typo", "first = A\nlast = B\npassword = x\nstrat = last\n", 0o600)

	_, err := LoadProfile("typo")
	if err == nil {
		t.Fatal("a misspelled setting should be an error, not a silent no-op")
	}
	if !strings.Contains(err.Error(), "strat") {
		t.Errorf("error should name the setting: %v", err)
	}
}

func TestLoadProfileRequiresCredentials(t *testing.T) {
	dir := tempConfig(t)
	writeProfile(t, dir, "nopass", "first = A\nlast = B\n", 0o600)
	if _, err := LoadProfile("nopass"); err == nil || !strings.Contains(err.Error(), "password") {
		t.Errorf("err = %v", err)
	}
	writeProfile(t, dir, "noname", "password = x\n", 0o600)
	if _, err := LoadProfile("noname"); err == nil || !strings.Contains(err.Error(), "first") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadProfileMissing(t *testing.T) {
	tempConfig(t)
	if _, err := LoadProfile("nobody"); err == nil {
		t.Error("expected an error for a profile that is not there")
	}
}

func TestProfileNameIsNotAPath(t *testing.T) {
	tempConfig(t)
	for _, bad := range []string{"", "../secrets", "a/b", ".", ".."} {
		if _, err := ProfilePath(bad); err == nil {
			t.Errorf("ProfilePath(%q) should be refused", bad)
		}
	}
}

func TestSaveProfileRoundTrip(t *testing.T) {
	dir := tempConfig(t)

	in := Login{
		First:    "Example",
		Last:     "Resident",
		Password: "example-password", // plain text going in
		Start:    "last",
		Group:    "Builders",
		Channel:  "slgo",
	}
	if err := SaveProfile("example", in); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "example")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := fi.Mode().Perm(); m != 0o600 {
		t.Errorf("profile written mode %04o, want 0600", m)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m := di.Mode().Perm(); m != 0o700 {
		t.Errorf("directory mode %04o, want 0700", m)
	}

	// The plain password must not have been written.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "example-password") {
		t.Error("the plain password was written to disk")
	}

	out, err := LoadProfile("example")
	if err != nil {
		t.Fatal(err)
	}
	if out.Password != hashPassword("example-password") {
		t.Errorf("password = %q", out.Password)
	}
	if out.First != in.First || out.Last != in.Last || out.Start != in.Start {
		t.Errorf("round trip = %+v", out)
	}
	if out.Group != in.Group {
		t.Errorf("group round trip = %q, want %q", out.Group, in.Group)
	}
	// A saved digest logs in identically.
	if hashPassword(out.Password) != hashPassword(in.Password) {
		t.Error("the stored digest does not hash to the same thing")
	}
}

func TestSaveProfileTightensLooseDirectory(t *testing.T) {
	dir := tempConfig(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfile("example", Login{First: "A", Last: "B", Password: "x"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m := fi.Mode().Perm(); m != 0o700 {
		t.Errorf("directory left at %04o, want 0700", m)
	}
}

func TestListProfiles(t *testing.T) {
	dir := tempConfig(t)
	writeProfile(t, dir, "example", "first = A\nlast = B\npassword = x\n", 0o600)
	writeProfile(t, dir, "builder", "first = C\nlast = D\npassword = y\n", 0o600)
	writeProfile(t, dir, ".hidden", "first = E\n", 0o600)

	got, err := ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("profiles = %v", got)
	}
	seen := map[string]bool{got[0]: true, got[1]: true}
	if !seen["example"] || !seen["builder"] {
		t.Errorf("profiles = %v", got)
	}
}

func TestConfigDirHonoursXDG(t *testing.T) {
	t.Setenv("SLGO_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "/somewhere/xdg")
	got, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("/somewhere/xdg", "slgo") {
		t.Errorf("ConfigDir = %q", got)
	}

	// With XDG unset it is ~/.config/slgo, not the macOS
	// application support directory.
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err = ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if got != filepath.Join(home, ".config", "slgo") {
		t.Errorf("ConfigDir = %q, want ~/.config/slgo", got)
	}
}
