//go:build unix

package logfile

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// ordinaryUmask sets the umask most accounts have, 022, for the length
// of one test.  Under it os.Create makes a file every account can read,
// which is the fault these tests are about; a test run under 077 would
// pass whether the fault was there or not.
func ordinaryUmask(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
}

func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// TestATraceIsCreatedForItsOwnerOnly: -trace used os.Create, so a trace
// -- with -trace-bodies, every session id and every instant message --
// was as readable as the umask left it, which is by everyone.
func TestATraceIsCreatedForItsOwnerOnly(t *testing.T) {
	ordinaryUmask(t)
	path := filepath.Join(t.TempDir(), "packets.txt")
	f, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := modeOf(t, path); got != 0o600 {
		t.Errorf("a new trace is mode %03o, want 600", got)
	}
}

// TestATraceOverAWiderFileNarrowsItFirst: a trace written over one left
// from an earlier run, created before any of this, keeps that file's
// mode unless it is changed -- os.OpenFile's mode applies only to a file
// it creates.
func TestATraceOverAWiderFileNarrowsItFirst(t *testing.T) {
	ordinaryUmask(t)
	path := filepath.Join(t.TempDir(), "packets.txt")
	if err := os.WriteFile(path, []byte("an earlier run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := modeOf(t, path); got != 0o600 {
		t.Errorf("an existing trace was left mode %03o, want 600", got)
	}
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Errorf("a new trace kept the old one's contents: %q", b)
	}
}

// TestALogAndItsDirectoryAreMadePrivate: -log with a directory that does
// not exist yet -- the first run after ~/.local/log was suggested --
// makes it 700 and the log 600, whatever the umask would have made them.
func TestALogAndItsDirectoryAreMadePrivate(t *testing.T) {
	ordinaryUmask(t)
	dir := filepath.Join(t.TempDir(), "local", "log")
	path := filepath.Join(dir, "slgod.log")
	f, err := Append(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := modeOf(t, dir); got != 0o700 {
		t.Errorf("the log directory is mode %03o, want 700", got)
	}
	if got := modeOf(t, path); got != 0o600 {
		t.Errorf("the log is mode %03o, want 600", got)
	}
}

// TestALogIsAppendedToAndNarrowed: a daemon launchd restarts opens the
// same log each time, and must add to it rather than lose the last run's
// account of why it stopped; and a log first made by something else,
// with the umask's mode, is narrowed.
func TestALogIsAppendedToAndNarrowed(t *testing.T) {
	ordinaryUmask(t)
	dir := filepath.Join(t.TempDir(), "log")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "slgod.log")
	if err := os.WriteFile(path, []byte("the last run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Append(path)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("this run\n")
	f.Close()
	if b, _ := os.ReadFile(path); string(b) != "the last run\nthis run\n" {
		t.Errorf("log = %q; the last run's lines should still be there", b)
	}
	if got := modeOf(t, path); got != 0o600 {
		t.Errorf("an existing log was left mode %03o, want 600", got)
	}
}

// TestALogInADirectoryOthersCanOpenIsRefused: /tmp is where the log was,
// and a directory anybody can get into protects none of the files in
// it -- launchd's own capture of the output among them.  The refusal
// says what to do about it.
func TestALogInADirectoryOthersCanOpenIsRefused(t *testing.T) {
	ordinaryUmask(t)
	for _, mode := range []fs.FileMode{0o755, 0o750, 0o701, 0o777} {
		dir := filepath.Join(t.TempDir(), "log")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "slgod.log")
		f, err := Append(path)
		if err == nil {
			f.Close()
			t.Errorf("a log in a directory of mode %03o was opened", mode)
			continue
		}
		if !strings.Contains(err.Error(), "chmod 700") {
			t.Errorf("the refusal does not say how to put it right: %v", err)
		}
		if _, err := os.Stat(path); err == nil {
			t.Errorf("a refused log was created anyway in a directory of mode %03o", mode)
		}
	}
}

// TestALinkWhereTheLogShouldBeIsNotFollowed: a symbolic link put where
// the log is about to be opened would have the daemon append its log to
// whatever the link names.
func TestALinkWhereTheLogShouldBeIsNotFollowed(t *testing.T) {
	ordinaryUmask(t)
	dir := t.TempDir()
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(elsewhere, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "packets.txt")
	if err := os.Symlink(elsewhere, path); err != nil {
		t.Fatal(err)
	}
	if f, err := Create(path); err == nil {
		f.Close()
		t.Error("a trace was opened through a symbolic link")
	}
	if got := modeOf(t, elsewhere); got != 0o644 {
		t.Errorf("the file the link named was changed to %03o", got)
	}
}
