//go:build unix

package main

import (
	"errors"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestAnInterruptWhileQueuedGivesUpThePlace: ^C while every group is
// busy.  The wait for a lease ran on a context of its own, so the
// interrupt reached nothing and slrun stayed queued until the group came
// free.  SIGTERM ends the same context, so it is the same case.
//
// The harness is TestInterruptingARunSaysSoOnceAndFails's, a real SIGINT
// to this process, raised once the backend says slrun is queued -- the
// moment it is certainly listening for one.  A run that did not hear it
// is given the group after a few seconds, so that it fails rather than
// hangs.
func TestAnInterruptWhileQueuedGivesUpThePlace(t *testing.T) {
	reset(t)
	addr, c := backendAt(t)
	release := holdTheGroup(t, c)
	commandLine(t, "--backend", addr, script(t, speaks), script(t, speaks))

	// A second handler, so that a signal arriving after run has put the
	// default back is not the end of the test binary.
	stray := make(chan os.Signal, 1)
	signal.Notify(stray, syscall.SIGINT)
	defer signal.Stop(stray)

	over := letGoAfter(5*time.Second, release)
	sent := make(chan time.Time, 1)
	go func() {
		for waiting(c) < 1 {
			select {
			case <-over:
				return
			case <-time.After(time.Millisecond):
			}
		}
		sent <- time.Now()
		syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()

	var err error
	out, errOut := bothOf(t, func() { err = run() })
	ended := time.Now()
	close(over)

	var at time.Time
	select {
	case at = <-sent:
	default:
		t.Fatalf("run ended before it was queued: %v\n%s", err, errOut)
	}
	if took := ended.Sub(at); took > 2*time.Second {
		t.Errorf("slrun went on for %v after the interrupt", took)
	}
	if !errors.Is(err, errInterrupted) {
		t.Errorf("run = %v, want errInterrupted", err)
	}
	if out != "" {
		t.Errorf("a run interrupted before it had anywhere to run printed:\n%s", out)
	}
	if strings.Contains(errOut, "context canceled") {
		t.Errorf("the cancellation was reported as an error:\n%s", errOut)
	}

	// The place is gone: the queue empties, and the group given back is
	// then held by nobody.  A waiter left in the queue would be handed it.
	until(t, "the queue emptying", func() bool { return waiting(c) == 0 })
	release()
	until(t, "the group coming free", func() bool { return heldGroups(t, c) == 0 })
}
