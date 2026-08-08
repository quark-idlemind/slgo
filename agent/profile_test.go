package agent

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
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

// TestEverySettingAProfileMayHold: a key that is read into the wrong
// field, or not read at all, is a setting that silently does nothing --
// which is exactly what the unknown-key error exists to prevent, and it
// cannot catch a key that is spelled right and dropped.
func TestEverySettingAProfileMayHold(t *testing.T) {
	dir := tempConfig(t)
	writeProfile(t, dir, "full", `
# every setting, with the comment and blank line a person would write
first            = Example
last             = Resident
password         = `+testDigest+`
start            = the test region/128/128/25
group            = Builders
url              = https://login.example.com/cgi-bin/login.cgi
channel          = slgo-test
version          = slgo 9.9
mac              = 00:EA:7C:A7:DE:AD
id0              = 0c3e7e577e57c0de1b49b982fc5bae19
platform         = lnx
platform_version = 6.8.0
platform_string  = Linux 6.8.0
options          = inventory-root, buddy-list , , login-flags
`, 0o600)

	l, err := LoadProfile("full")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, got, want string }{
		{"first", l.First, "Example"},
		{"last", l.Last, "Resident"},
		{"password", l.Password, testDigest},
		{"start", l.Start, "the test region/128/128/25"},
		{"group", l.Group, "Builders"},
		{"url", l.URL, "https://login.example.com/cgi-bin/login.cgi"},
		{"channel", l.Channel, "slgo-test"},
		{"version", l.Version, "slgo 9.9"},
		{"mac", l.MAC, "00:EA:7C:A7:DE:AD"},
		{"id0", l.ID0, "0c3e7e577e57c0de1b49b982fc5bae19"},
		{"platform", l.Platform, "lnx"},
		{"platform_version", l.PlatformVersion, "6.8.0"},
		{"platform_string", l.PlatformString, "Linux 6.8.0"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	// The empty entry between the commas is nothing, not an option
	// named "".
	if len(l.Options) != 3 || l.Options[1] != "buddy-list" || l.Options[2] != "login-flags" {
		t.Errorf("options = %q", l.Options)
	}

	// login_url is the same setting under the name a viewer's own
	// configuration uses.
	writeProfile(t, dir, "alias", "first=A\nlast=B\npassword=x\nlogin_url = https://elsewhere/\n", 0o600)
	l, err = LoadProfile("alias")
	if err != nil {
		t.Fatal(err)
	}
	if l.URL != "https://elsewhere/" {
		t.Errorf("login_url gave %q", l.URL)
	}
}

// TestALineThatIsNotASetting: the file is edited by hand, so a line that
// is neither blank, a comment, nor key = value has to say where it is.
func TestALineThatIsNotASetting(t *testing.T) {
	dir := tempConfig(t)
	writeProfile(t, dir, "broken", "first = A\nlast\npassword = x\n", 0o600)

	_, err := LoadProfile("broken")
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("err = %v, should name the line", err)
	}
}

// TestAProfileIsSavedAndReadBack: SaveProfile and LoadProfile are the
// two halves of one format, and a setting that survives one but not the
// other is a setting that quietly resets.
func TestAProfileSurvivesBeingSavedAndReadBack(t *testing.T) {
	tempConfig(t)

	want := Login{
		First: "Example", Last: "Resident", Password: "example-password",
		Start: "home", Group: "Builders",
		URL: "https://login.example.com/", Channel: "slgo-test", Version: "slgo 9.9",
		MAC: "00:EA:7C:A7:DE:AD", ID0: "0c3e7e577e57c0de1b49b982fc5bae19",
		Platform: "lnx", PlatformVersion: "6.8.0", PlatformString: "Linux 6.8.0",
		Options: []string{"inventory-root", "buddy-list"},
	}
	if err := SaveProfile("round-trip", want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProfile("round-trip")
	if err != nil {
		t.Fatal(err)
	}

	// The password is stored as its digest, which is the only form that
	// ever goes over the wire.
	want.Password = hashPassword(want.Password)
	if got.First != want.First || got.Password != want.Password ||
		got.URL != want.URL || got.ID0 != want.ID0 ||
		got.PlatformString != want.PlatformString {
		t.Errorf("saved %+v, read back %+v", want, got)
	}
	if len(got.Options) != 2 || got.Options[0] != "inventory-root" {
		t.Errorf("options = %q", got.Options)
	}
}

// TestAProfileNeedsSomewhereToLive: everything here goes through
// ConfigDir, so a machine with no home directory and nothing configured
// has to say so rather than write to the working directory.
func TestAProfileNeedsSomewhereToLive(t *testing.T) {
	t.Setenv("SLGO_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	if _, err := ConfigDir(); err == nil {
		t.Error("expected an error with no home directory")
	}
	if _, err := ProfilePath("example"); err == nil {
		t.Error("ProfilePath should carry it up")
	}
	if _, err := LoadProfile("example"); err == nil {
		t.Error("LoadProfile should carry it up")
	}
	if err := SaveProfile("example", Login{}); err == nil {
		t.Error("SaveProfile should carry it up")
	}
	if _, err := ListProfiles(); err == nil {
		t.Error("ListProfiles should carry it up")
	}
	// A profile with no name is not a path either.
	if _, err := LoadProfile(""); err == nil {
		t.Error("expected an error for a profile with no name")
	}
}

// TestSaveProfileCannotMakeItsDirectory: the configured directory is
// whatever the operator said, and it may be something a directory cannot
// be made under.
func TestSaveProfileCannotMakeItsDirectory(t *testing.T) {
	base := t.TempDir()
	blocking := filepath.Join(base, "in-the-way")
	if err := os.WriteFile(blocking, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLGO_CONFIG_DIR", filepath.Join(blocking, "slgo"))

	if err := SaveProfile("example", Login{First: "A"}); err == nil {
		t.Error("expected an error making the directory")
	}
}

// TestListingProfilesWhereThereAreNone: a first run has no directory at
// all, which is not a failure -- it is an empty list.
func TestListingProfilesWhereThereAreNone(t *testing.T) {
	base := t.TempDir()
	t.Setenv("SLGO_CONFIG_DIR", filepath.Join(base, "never-created"))

	got, err := ListProfiles()
	if err != nil || got != nil {
		t.Errorf("ListProfiles = %v, %v; want nothing and no error", got, err)
	}

	// A configured directory that is a file is a different matter and is
	// worth reporting.
	notADir := filepath.Join(base, "a-file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLGO_CONFIG_DIR", notADir)
	if _, err := ListProfiles(); err == nil {
		t.Error("expected an error listing something that is not a directory")
	}
}

// TestNotEverythingInTheDirectoryIsAProfile: programs keep their own
// things here -- autobench remembers paddings in one -- and listing
// those as accounts is worse than untidy.  With one real profile and one
// cache file, "the only profile" stops being the only one, and a program
// that would have chosen for you starts asking, or offers a login as a
// file with no credentials in it.
func TestNotEverythingInTheDirectoryIsAProfile(t *testing.T) {
	dir := tempConfig(t)
	writeProfile(t, dir, "example", "first = A\nlast = B\npassword = x\n", 0o600)
	writeProfile(t, dir, "autobench-cache", "some padding = 4\n", 0o600)
	if err := os.MkdirAll(filepath.Join(dir, "a-directory"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "example" {
		t.Errorf("profiles = %v, want only the one that loads", got)
	}
}

// TestLoginAsIsTheWholeOfWhatAProgramNeeds: name a profile and be logged
// in.  Nothing else in this package reads the credentials.
func TestLoginAsIsTheWholeOfWhatAProgramNeeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t))
	}))
	defer srv.Close()

	dir := tempConfig(t)
	writeProfile(t, dir, "example",
		"first = Example\nlast = Resident\npassword = "+testDigest+"\nurl = "+srv.URL+"\n", 0o600)

	acct, err := LoginAs(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	if acct.CircuitCode == 0 {
		t.Errorf("account = %+v", acct)
	}

	if _, err := LoginAs(context.Background(), "no-such-profile"); err == nil {
		t.Error("expected an error for a profile that is not there")
	}
}
