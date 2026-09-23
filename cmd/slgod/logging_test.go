package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/internal/redact"
)

// TestTheDaemonsLogAndTraceAreItsOwnersAlone: the daemon's output was
// appended to a file in /tmp at mode 644 and -trace was os.Create, so
// both were as readable as the umask left them -- by every account on
// the machine.  -log now puts the log in a directory of its own at 700
// and the file at 600, -trace makes its file 600, and -log-secrets, the
// one way to have credentials written to that log, says so at the top
// of it.
//
// Run under umask 022, the ordinary one, because under 077 every file
// would come out private whether the daemon saw to it or not.
func TestTheDaemonsLogAndTraceAreItsOwnersAlone(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
	// Registered before the daemon is started, so that it runs after
	// the daemon is stopped: cleanups run last first.
	t.Cleanup(func() { redact.SetFull(false) })

	sim := newSim(t)
	hs := loginServer(t, sim)
	profileDir(t, hs, "example")
	writeSecret(t, "a shared secret for a test")

	logDir := filepath.Join(t.TempDir(), "local", "log")
	logPath := filepath.Join(logDir, "slgod.log")
	tracePath := filepath.Join(t.TempDir(), "packets.txt")

	d := runDaemon(t, "-listen", "127.0.0.1:0",
		"-log", logPath, "-log-secrets", "-trace", tracePath, "example")
	waitFor(t, 30*time.Second, "the daemon to say in its log file that it is serving", func() bool {
		b, _ := os.ReadFile(logPath)
		return serving.Match(b)
	})

	for _, c := range []struct {
		what, path string
		want       os.FileMode
	}{
		{"the log's directory", logDir, 0o700},
		{"the log", logPath, 0o600},
		{"the trace", tracePath, 0o600},
	} {
		fi, err := os.Stat(c.path)
		if err != nil {
			t.Errorf("%s: %v", c.what, err)
			continue
		}
		if got := fi.Mode().Perm(); got != c.want {
			t.Errorf("%s is mode %03o, want %03o", c.what, got, c.want)
		}
	}

	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "WARNING: -log-secrets is on") {
		t.Errorf("-log-secrets was not announced at the top of the log:\n%s", b)
	}
	if !redact.Full() {
		t.Error("-log-secrets did not turn redaction off")
	}
	// With -log the log goes there and nowhere else.
	if strings.Contains(d.log.String(), "serving gRPC") {
		t.Errorf("the log went to standard error as well:\n%s", d.log.String())
	}
}
