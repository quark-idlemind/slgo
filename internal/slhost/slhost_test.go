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

// TestOnlyTheFirstWordIsTheHost: a misconfigured sl-host that prints a
// whole line of explanation, or a second address after the first, still
// has an address at the front of it, and joining a port to the rest
// would produce something that cannot be dialled at all.
func TestOnlyTheFirstWordIsTheHost(t *testing.T) {
	fakeSLHost(t, `printf 'lab.local extra words\nsecond.host\n'`)

	got, err := Addr()
	if err != nil {
		t.Fatal(err)
	}
	if want := "lab.local:" + Port; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
}

// TestMustAddrPassesAnAddressThrough covers what a small tool actually
// does with it.  The other half -- a failure -- ends in os.Exit and
// would take the test binary with it, so it is not driven from here.
func TestMustAddrPassesAnAddressThrough(t *testing.T) {
	emptyPath(t)

	if got := MustAddr("example.com:9999"); got != "example.com:9999" {
		t.Errorf("MustAddr = %q", got)
	}
	if got, want := MustAddr(""), "localhost:"+Port; got != want {
		t.Errorf("MustAddr(\"\") = %q, want %q", got, want)
	}
}

// sl-host may answer with a port, for a slgod that is not on 7807, and
// that port is the one used.  Without one the usual port is added, IPv6
// included, bracketed or not.
func TestAPortFromSLHostIsKept(t *testing.T) {
	for _, c := range []struct{ said, want string }{
		{"192.168.1.42:7808", "192.168.1.42:7808"},
		{"slgod.example:7900", "slgod.example:7900"},
		{"[2001:db8::42]:7808", "[2001:db8::42]:7808"},
		{"192.168.1.42", "192.168.1.42:" + Port},
		{"2001:db8::42", "[2001:db8::42]:" + Port},
		{"[2001:db8::42]", "[2001:db8::42]:" + Port},
	} {
		fakeSLHost(t, "echo '"+c.said+"'")
		got, err := Resolve("")
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("sl-host said %q: resolved to %q, want %q", c.said, got, c.want)
		}
	}
}

// sl-host is told which profile is wanted, in $SLGO_AGENT, and told
// nothing -- the variable taken away, not left as the caller's -- when
// no profile is named, since then the question is about the daemon's
// default and not whatever this shell happens to have exported.
func TestSLHostIsToldWhichProfile(t *testing.T) {
	fakeSLHost(t, `echo "host-for-${SLGO_AGENT:-nobody}"`)
	t.Setenv("SLGO_AGENT", "exported")

	got, err := ResolveFor("", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if got != "host-for-dev:"+Port {
		t.Errorf("for dev: %q", got)
	}
	if got, _ := ResolveFor("", ""); got != "host-for-nobody:"+Port {
		t.Errorf("for no profile: %q", got)
	}

	// An address given is still the answer, whoever it is for.
	if got, _ := ResolveFor("example.com:9999", "dev"); got != "example.com:9999" {
		t.Errorf("a given address was not kept: %q", got)
	}
}
