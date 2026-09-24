//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A program that will not finish is stopped, and the answer says that
// is what happened rather than reporting an exit status nobody set.
//
// The script starts a child and waits for it, which is what a wrapper
// script does, and the child is what used to keep the run going: the
// script was killed at the deadline and the sleep it had started held
// the output open for its full thirty seconds.  It writes the child's
// pid down first, so the test knows the child had started before the
// deadline -- this passed by luck on a machine whose /bin/sh was slow
// enough that the deadline came before the sleep did -- and can check
// that the child was stopped too.
func TestAProgramThatWillNotFinishIsStopped(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	d.cfg.RunTimeout = 2 * time.Second
	mark := filepath.Join(t.TempDir(), "child")
	d.cfg.Programs["forever"] = &Program{
		Name: "forever",
		Argv: []string{script(t, "sleep 30 &\necho $! > '"+mark+"'\nwait\n")},
	}
	started := time.Now()
	got := runs(t, d, b, "forever")
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("it took %s to give up", took)
	}
	if !strings.Contains(got, "was still running") {
		t.Errorf("got %q", got)
	}
	pid := childPid(t, mark)
	if pid == 0 {
		t.Fatal("the script's child never started, so this proved nothing; is /bin/sh taking over two seconds to start?")
	}
	if alive(pid, 2*time.Second) {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the script was stopped but its child %d was left running", pid)
	}
}

// A program that finishes but leaves something running with its output
// open is not waited for until that exits: the answer comes back once
// programWaitDelay has passed, says the program finished, and says what
// it left behind.
func TestAProgramThatLeavesAChildRunningStillFinishes(t *testing.T) {
	was := programWaitDelay
	programWaitDelay = 300 * time.Millisecond
	t.Cleanup(func() { programWaitDelay = was })

	d, b, _ := newTestDaemon(t)
	mark := filepath.Join(t.TempDir(), "child")
	d.cfg.Programs["leaves"] = &Program{
		Name: "leaves",
		Argv: []string{script(t, "sleep 30 &\necho $! > '"+mark+"'\necho measured\n")},
	}
	t.Cleanup(func() {
		if pid := childPid(t, mark); pid != 0 {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	started := time.Now()
	got := runs(t, d, b, "leaves")
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("it took %s to come back", took)
	}
	if !strings.Contains(got, "measured") || !strings.Contains(got, "leaves finished in") {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(got, "still had its output open") {
		t.Errorf("the answer does not say something was left running: %q", got)
	}
}

// childPid reads the pid a script wrote down, or 0 if it wrote none.
func childPid(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return pid
}

// alive reports whether pid is still running after up to wait.  A
// killed child is reaped by init rather than by us, so it can take a
// moment to be gone.
func alive(pid int, wait time.Duration) bool {
	for end := time.Now().Add(wait); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return false
		}
	}
	return true
}
