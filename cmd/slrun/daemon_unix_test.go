//go:build unix

package main

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

// TestInterruptingARunSaysSoOnceAndFails: ^C during a run.
//
// The signal is raised from inside the fake as the script is uploaded,
// which is the one moment the program is certainly listening for one:
// run has installed its handler and has not taken it down again.  The
// script never says DONE, so without the interrupt this would sit here
// until the timeout.
//
// What is being pinned is the reporting, not the exit.  slrun already
// stopped and already exited non-zero -- measured, against the version
// before this -- but it said "context canceled" under the name of every
// script including the ones that never started, and nothing at all
// about having been interrupted.
func TestInterruptingARunSaysSoOnceAndFails(t *testing.T) {
	reset(t)
	f, addr := newFakeDaemon(t)
	f.silent = true // never says DONE, so the run would wait
	f.onRun = func() { syscall.Kill(os.Getpid(), syscall.SIGINT) }
	commandLine(t, "--addr", addr, script(t, "default {}"))

	var err error
	out, errOut := bothOf(t, func() { err = run() })

	if !errors.Is(err, errInterrupted) {
		t.Fatalf("run = %v, want errInterrupted", err)
	}
	if errors.Is(err, errScript) {
		t.Error("an interrupt was reported as a script failing")
	}
	for _, said := range []string{out, errOut} {
		if strings.Contains(said, "context canceled") {
			t.Errorf("the cancellation was reported as a script error:\n%s", said)
		}
	}
}

// TestInterruptingAWaitForObjectsSaysInterrupted: ^C while every object
// is busy and slrun is waiting for slgod.  The wait ends with the run's
// own cancellation, which is an interrupt, as it is on the backend path
// -- not a daemon too old to answer, which is what it was reported as.
func TestInterruptingAWaitForObjectsSaysInterrupted(t *testing.T) {
	reset(t)
	f, addr := newBusyDaemon(t, boundsWaits)
	f.onAsk = func() { syscall.Kill(os.Getpid(), syscall.SIGINT) }
	commandLine(t, "--addr", addr, script(t, "default {}"))

	var err error
	out, errOut := bothOf(t, func() { err = run() })

	if !errors.Is(err, errInterrupted) {
		t.Fatalf("run = %v, want errInterrupted", err)
	}
	if want := "interrupted while waiting for objects"; err.Error() != want {
		t.Errorf("run = %q, want %q", err, want)
	}
	for _, said := range []string{err.Error(), out, errOut} {
		if strings.Contains(said, "context canceled") || strings.Contains(said, "older than the pool") {
			t.Errorf("the interrupt was reported as something else:\n%s", said)
		}
	}
}
