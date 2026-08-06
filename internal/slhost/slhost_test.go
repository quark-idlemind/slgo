package slhost

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeSLHost puts a script called sl-host on $PATH and returns nothing;
// the test's PATH is restored when it ends.
func fakeSLHost(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake is a shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, Command)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// emptyPath is a $PATH with nothing on it, which is how a machine
// without sl-host is arranged for a test.
func emptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// TestNotInstalledIsThisMachine: no sl-host means slgod is here, which
// has to be a default rather than a failure -- it is the ordinary case
// on a machine that runs its own.
func TestNotInstalledIsThisMachine(t *testing.T) {
	emptyPath(t)

	got, err := Addr()
	if err != nil {
		t.Fatalf("no sl-host should not be an error: %v", err)
	}
	if want := "localhost:" + Port; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
}

// TestAsksSLHostWhenItIsThere is the point of the package.
func TestAsksSLHostWhenItIsThere(t *testing.T) {
	fakeSLHost(t, "echo 192.168.1.42")

	got, err := Addr()
	if err != nil {
		t.Fatal(err)
	}
	if want := "192.168.1.42:" + Port; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
}

// TestPortIsJoinedHere: sl-host prints a bare host, so the port is this
// package's to add -- including for a caller that wants another one.
func TestPortIsJoinedHere(t *testing.T) {
	fakeSLHost(t, "echo lab.local")

	got, err := AddrOn("50051")
	if err != nil {
		t.Fatal(err)
	}
	if want := "lab.local:50051"; got != want {
		t.Errorf("AddrOn = %q, want %q", got, want)
	}
}

// TestIPv6IsBracketed, because an address with colons in it cannot be
// joined to a port by concatenation.
func TestIPv6IsBracketed(t *testing.T) {
	fakeSLHost(t, "echo fd00::1")

	got, err := Addr()
	if err != nil {
		t.Fatal(err)
	}
	if want := "[fd00::1]:" + Port; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
}

// TestSurroundingSpaceIsTrimmed: the answer comes off a command's
// stdout, so it arrives with a newline on it at least.
func TestSurroundingSpaceIsTrimmed(t *testing.T) {
	fakeSLHost(t, `printf '  10.0.0.7 \n'`)

	got, err := Addr()
	if err != nil {
		t.Fatal(err)
	}
	if want := "10.0.0.7:" + Port; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
}

// TestResolveKeepsWhatItWasGiven: an address said out loud wins, and
// nothing is asked -- the fake here would answer differently if it were.
func TestResolveKeepsWhatItWasGiven(t *testing.T) {
	fakeSLHost(t, "echo 192.168.1.42")

	got, err := Resolve("example.com:9999")
	if err != nil {
		t.Fatal(err)
	}
	if want := "example.com:9999"; got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}
}

// TestResolveAsksWhenNothingWasGiven is the other half.
func TestResolveAsksWhenNothingWasGiven(t *testing.T) {
	fakeSLHost(t, "echo 192.168.1.42")

	got, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if want := "192.168.1.42:" + Port; got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}
}

// TestFailingIsReported: installed but broken is NOT localhost.  Saying
// localhost here would report a connection refused against this machine
// and send the reader looking for a slgod that was never meant to be
// running on it.
func TestFailingIsReported(t *testing.T) {
	fakeSLHost(t, "echo 'no host configured' >&2; exit 1")

	got, err := Addr()
	if err == nil {
		t.Fatalf("a failing %s should be an error; got %q", Command, got)
	}
	if !strings.Contains(err.Error(), "no host configured") {
		t.Errorf("the error should quote what %s said, got: %v", Command, err)
	}
	if !strings.Contains(err.Error(), "--addr") {
		t.Errorf("the error should say how to override it, got: %v", err)
	}
}

// TestSayingNothingIsReported, for the same reason.
func TestSayingNothingIsReported(t *testing.T) {
	fakeSLHost(t, "exit 0")

	if got, err := Addr(); err == nil {
		t.Fatalf("an empty answer should be an error; got %q", got)
	}
}
