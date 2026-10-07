package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestMain lets this test binary stand in for the slgod binary: run with
// SLGOD_TEST_AS_MAIN set it is main, with a real exit status and a real
// stderr, which is what a second instance started by mistake is.  main
// ends a refused start with log.Fatal, so it cannot be run inside a test
// that has to go on afterwards.
func TestMain(m *testing.M) {
	if os.Getenv("SLGOD_TEST_AS_MAIN") != "" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// countingLoginServer is a login server that says how many logins it was
// asked for, which is the thing a refused second instance must leave
// alone.
func countingLoginServer(t *testing.T, sim *fakeSim) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	inner := loginServer(t, sim)
	var n atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	return hs, &n
}

// startMistake runs a second slgod, as a separate process, and returns
// what it said and whether it exited with a failure.  It is given a
// minute, because a refused start is immediate and one that is not is
// the bug.
func startMistake(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	// Not os.Args[0], which runDaemon has overwritten with "slgod": that
	// would be looked up on the PATH and run whatever is installed.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, args...)
	cmd.Env = append(os.Environ(), append([]string{"SLGOD_TEST_AS_MAIN=1"}, env...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.String(), err
	case <-time.After(time.Minute):
		cmd.Process.Kill()
		<-done
		t.Fatalf("the second slgod did not exit by itself:\n%s", out.String())
		return "", nil
	}
}

func wantRefused(t *testing.T, out string, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the second slgod exited without failing:\n%s", out)
	}
	if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("the second slgod did not run: %v", err)
	}
	for _, p := range append(parts, "nobody has been logged in") {
		if !strings.Contains(out, p) {
			t.Errorf("its refusal does not say %q:\n%s", p, out)
		}
	}
}

// TestASecondInstanceFailsBeforeItLogsAnybodyIn is the reason for the
// order of things in main.  A login from anywhere else ends the session
// the grid holds for the avatar, and the daemon that held it does not get
// it back; a second slgod that logged in and only then found its port
// taken cost the running one its avatar for nothing.
func TestASecondInstanceFailsBeforeItLogsAnybodyIn(t *testing.T) {
	sim := newSim(t)
	hs, logins := countingLoginServer(t, sim)
	profileDir(t, hs, "example")
	t.Setenv("HOME", t.TempDir())

	// The first instance: the real thing, up and serving.
	first := runDaemon(t, "-listen", "127.0.0.1:0", "-no-auth", "example")
	addr := first.waitForLog(t, serving, "that it is serving")
	if n := logins.Load(); n != 1 {
		t.Fatalf("the first instance made %d logins", n)
	}
	firstConfig := os.Getenv("SLGOD_CONFIG_DIR")
	otherConfig := filepath.Join(t.TempDir(), "slgod")
	if err := os.MkdirAll(otherConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	free := func() string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		return ln.Addr().String()
	}

	t.Run("the port is busy", func(t *testing.T) {
		out, err := startMistake(t, []string{"SLGOD_CONFIG_DIR=" + otherConfig},
			"-listen", addr, "-no-auth", "example")
		wantRefused(t, out, err, "cannot listen on "+addr, "address already in use")
		if n := logins.Load(); n != 1 {
			t.Errorf("a login was made: %d in all", n)
		}
	})

	t.Run("the profiles are held by another instance on another port", func(t *testing.T) {
		out, err := startMistake(t, []string{"SLGOD_CONFIG_DIR=" + firstConfig},
			"-listen", free(), "-no-auth", "example")
		wantRefused(t, out, err, "another slgod", fmt.Sprintf("(pid %d)", os.Getpid()),
			filepath.Join(firstConfig, lockName))
		if n := logins.Load(); n != 1 {
			t.Errorf("a login was made: %d in all", n)
		}
	})

	t.Run("the viewer address is busy", func(t *testing.T) {
		busy, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer busy.Close()
		out, err := startMistake(t, []string{"SLGOD_CONFIG_DIR=" + otherConfig},
			"-listen", free(), "-viewer", busy.Addr().String(), "-no-auth", "example")
		wantRefused(t, out, err, "-viewer "+busy.Addr().String(), "address already in use")
		if n := logins.Load(); n != 1 {
			t.Errorf("a login was made: %d in all", n)
		}
	})

	// And the first is still the one holding the avatar and the port.
	select {
	case <-first.done:
		t.Fatalf("the first instance stopped:\n%s", first.log.String())
	default:
	}
	if strings.Contains(first.log.String(), "connection ended") {
		t.Errorf("the first instance lost its session:\n%s", first.log.String())
	}
}

// TestTheLockIsGoneWithItsHolder: nothing stale to clear.  The operating
// system lets go of the lock when the process does, however it ends, so
// the lock is the file being held and not the file existing.
func TestTheLockIsGoneWithItsHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", lockName)
	f, err := lockConfigDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Skip("no lock on this platform")
	}
	if _, err := lockConfigDir(path); err == nil {
		t.Fatal("a second lock on the same directory was granted")
	} else if want := fmt.Sprintf("(pid %d)", os.Getpid()); !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal %q does not name its holder %s", err, want)
	}
	f.Close()
	g, err := lockConfigDir(path)
	if err != nil {
		t.Fatalf("the lock was not released with its holder: %v", err)
	}
	g.Close()
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("the directory made for the lock is %v, %v; wanted 0700", fi, err)
	}
}
