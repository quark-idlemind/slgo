package creds

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"
)

// tempProfiles points the profile directory at a fresh one and writes
// the profiles given, which is where Resolve() looks first.
func tempProfiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "slgo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLGO_CONFIG_DIR", dir)
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// testDigest is a digest of a made-up password, computed rather than
// written out.  A stored "$1$" digest is accepted by the login server
// IN PLACE OF the password, so one spelled out here would be a working
// credential rather than an example of one.
var testDigest = func() string {
	sum := md5.Sum([]byte("example-password"))
	return "$1$" + hex.EncodeToString(sum[:])
}()

var qiProfile = `first    = Quark
last     = Idlemind
password = ` + testDigest + `
start    = last
`

// pipeStdin makes a file that reads the given text, since credentials
// reads from an *os.File so that it can tell a terminal from a pipe.
func pipeStdin(t *testing.T, text string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// TestCredentialsFromNamedProfile: naming the profile settles
// everything, and nothing is asked.
func TestCredentialsFromNamedProfile(t *testing.T) {
	tempProfiles(t, map[string]string{"qi": qiProfile})
	var out bytes.Buffer

	l, err := Resolve(pipeStdin(t, ""), &out, "qi", "", "", "last")
	if err != nil {
		t.Fatal(err)
	}
	if l.First != "Quark" || l.Last != "Idlemind" {
		t.Errorf("names = %q %q", l.First, l.Last)
	}
	if l.Password != testDigest {
		t.Errorf("password = %q", l.Password)
	}
	if out.Len() != 0 {
		t.Errorf("something was asked for when nothing needed to be: %q", out.String())
	}
}

// TestCredentialsFindsProfileByName: the profiles are filed under a
// short name of the operator's choosing, so an avatar named on the
// command line has to be looked for by the names inside them.
func TestCredentialsFindsProfileByName(t *testing.T) {
	tempProfiles(t, map[string]string{
		"qi":      qiProfile,
		"example": "first = Example\nlast = Resident\npassword = " + testDigest + "\n",
	})
	var out bytes.Buffer

	l, err := Resolve(pipeStdin(t, ""), &out, "", "quark", "IDLEMIND", "last")
	if err != nil {
		t.Fatal(err)
	}
	// The case as typed does not have to match the case on disk.
	if l.First != "quark" || l.Last != "IDLEMIND" {
		t.Errorf("names = %q %q", l.First, l.Last)
	}
	if l.Password == "" {
		t.Error("the stored password was not found")
	}
	if out.Len() != 0 {
		t.Errorf("something was asked for: %q", out.String())
	}
}

// TestCredentialsAsksForWhatIsMissing: an avatar with no profile.
func TestCredentialsAsksForWhatIsMissing(t *testing.T) {
	tempProfiles(t, nil)
	var out bytes.Buffer

	l, err := Resolve(pipeStdin(t, "Nobody\nResident\nhunter2\n"), &out, "", "", "", "home")
	if err != nil {
		t.Fatal(err)
	}
	if l.First != "Nobody" || l.Last != "Resident" || l.Password != "hunter2" {
		t.Errorf("login = %+v", l)
	}
	if l.Start != "home" {
		t.Errorf("start = %q", l.Start)
	}
	asked := out.String()
	for _, want := range []string{"First name", "Last name", "Password"} {
		if !strings.Contains(asked, want) {
			t.Errorf("never asked for the %s: %q", want, asked)
		}
	}
}

// TestCredentialsAsksOnlyForThePassword: the names came from the
// command line, so only the secret is missing.
func TestCredentialsAsksOnlyForThePassword(t *testing.T) {
	tempProfiles(t, nil)
	var out bytes.Buffer

	l, err := Resolve(pipeStdin(t, "hunter2\n"), &out, "", "Nobody", "Resident", "last")
	if err != nil {
		t.Fatal(err)
	}
	if l.Password != "hunter2" {
		t.Errorf("password = %q", l.Password)
	}
	if strings.Contains(out.String(), "First name") {
		t.Errorf("asked for a name it had been given: %q", out.String())
	}
	// The prompt says who is being logged in, so a typo in the name is
	// noticed before the password is typed rather than after.
	if !strings.Contains(out.String(), "Nobody Resident") {
		t.Errorf("the password prompt does not say who for: %q", out.String())
	}
}

// TestCredentialsDefaultsLastNameToResident: accounts made since 2010
// have no last name, and are Resident to everything that wants one.
func TestCredentialsDefaultsLastNameToResident(t *testing.T) {
	tempProfiles(t, nil)
	var out bytes.Buffer

	l, err := Resolve(pipeStdin(t, "Nobody\n\nhunter2\n"), &out, "", "", "", "last")
	if err != nil {
		t.Fatal(err)
	}
	if l.Last != "Resident" {
		t.Errorf("last = %q, want Resident", l.Last)
	}
}

// TestCredentialsUsesTheOnlyProfile: with one profile and nothing said,
// there is nothing to choose between.
func TestCredentialsUsesTheOnlyProfile(t *testing.T) {
	tempProfiles(t, map[string]string{"qi": qiProfile})
	var out bytes.Buffer

	l, err := Resolve(pipeStdin(t, ""), &out, "", "", "", "last")
	if err != nil {
		t.Fatal(err)
	}
	if l.First != "Quark" || l.Password == "" {
		t.Errorf("login = %+v", l)
	}
	if !strings.Contains(out.String(), "qi") {
		t.Errorf("it did not say which profile it used: %q", out.String())
	}
}

// TestCredentialsRefusesAMissingProfile: a name that is not there is a
// mistake worth stopping for, not a reason to start asking questions.
func TestCredentialsRefusesAMissingProfile(t *testing.T) {
	tempProfiles(t, map[string]string{"qi": qiProfile})
	var out bytes.Buffer

	if _, err := Resolve(pipeStdin(t, ""), &out, "nosuch", "", "", "last"); err == nil {
		t.Fatal("expected a refusal for a profile that does not exist")
	}
}

// TestCredentialsStopsWhenThereIsNoOneToAsk: a program run from cron or
// a pipe that has nothing left on its input cannot be asked for a name,
// and waiting or guessing would both be worse than saying so.
func TestCredentialsStopsWhenThereIsNoOneToAsk(t *testing.T) {
	cases := map[string]string{
		"nothing at all":      "",
		"a name and no more":  "Nobody\n",
		"names but no secret": "Nobody\nResident\n",
	}
	for what, input := range cases {
		tempProfiles(t, nil)
		var out bytes.Buffer
		if l, err := Resolve(pipeStdin(t, input), &out, "", "", "", "last"); err == nil {
			t.Errorf("%s: got %+v, expected a refusal", what, l)
		}
	}
}

// TestCredentialsRefusesAnEmptyAnswer: pressing return at every prompt
// is not a login, and a blank password would be sent to the login server
// as though it were one.
func TestCredentialsRefusesAnEmptyAnswer(t *testing.T) {
	tempProfiles(t, nil)
	var out bytes.Buffer

	if l, err := Resolve(pipeStdin(t, "\n\n\n"), &out, "", "", "", "last"); err == nil {
		t.Errorf("got %+v, expected a refusal", l)
	}
}

// TestCredentialsSurvivesAnUnreadableProfileDirectory: the search by
// avatar name is a convenience, and a configuration directory that
// cannot be listed at all should cost the password prompt, not the run.
func TestCredentialsSurvivesAnUnreadableProfileDirectory(t *testing.T) {
	// A plain file where the directory should be: listing it fails
	// with something other than "it is not there".
	notADir := filepath.Join(t.TempDir(), "slgo")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLGO_CONFIG_DIR", notADir)
	var out bytes.Buffer

	l, err := Resolve(pipeStdin(t, "hunter2\n"), &out, "", "Nobody", "Resident", "last")
	if err != nil {
		t.Fatal(err)
	}
	if l.Password != "hunter2" {
		t.Errorf("password = %q", l.Password)
	}
}

// TestPasswordIsNotEchoedToATerminal is the point of askSecret: down a
// pipe the line is simply read, but at a terminal the password must not
// end up in the scrollback of whoever walks past.  A pseudo-terminal is
// the only way to be a terminal for the purposes of the test.
func TestPasswordIsNotEchoedToATerminal(t *testing.T) {
	tempProfiles(t, nil)

	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	t.Cleanup(func() { master.Close(); slave.Close() })

	// The line is typed before the prompt goes out, which the terminal
	// buffers exactly as it would a fast typist.
	if _, err := master.WriteString("hunter2\n"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	l, err := Resolve(slave, &out, "", "Nobody", "Resident", "last")
	if err != nil {
		t.Fatal(err)
	}
	if l.Password != "hunter2" {
		t.Errorf("password = %q", l.Password)
	}
	// The prompt goes to the given writer rather than to the terminal,
	// so a caller can send it wherever the rest of its output goes.
	if !strings.Contains(out.String(), "Password for Nobody Resident") {
		t.Errorf("prompt = %q", out.String())
	}
	if strings.Contains(out.String(), "hunter2") {
		t.Errorf("the password was written back out: %q", out.String())
	}
}
