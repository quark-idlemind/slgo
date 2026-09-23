package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestAFailedRestoreIsTriedAgainRatherThanGivenUpOn.
//
// The session an attach lands on can be one about to be replaced: one
// was measured silent from the start, its inventory capability
// answering 404, and re-established 74 seconds later.  Giving up on the
// first error left that avatar missing part of its outfit until the
// next restart.  A pass that fails outright is the session, not the
// outfit, so it does not use up a pass -- but the retrying is bounded,
// or a session that never comes good would be asked for ever.
func TestAFailedRestoreIsTriedAgainRatherThanGivenUpOn(t *testing.T) {
	settle, retry, passes, giveUp := DressSettle, DressRetry, DressPasses, DressGiveUp
	t.Cleanup(func() { DressSettle, DressRetry, DressPasses, DressGiveUp = settle, retry, passes, giveUp })
	DressSettle, DressRetry, DressPasses, DressGiveUp = 0, 10*time.Millisecond, 2, 300*time.Millisecond

	// The fake grid answers no capability, so every pass fails.
	_, b, _ := newTestDaemon(t)
	s := b.Session()

	done := make(chan struct{})
	go func() { defer close(done); b.keepDressed(context.Background(), s) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("keepDressed was still retrying long after it should have given up")
	}

	failed := 0
	for _, tr := range b.Troubles() {
		if strings.HasPrefix(tr.Text, "cannot put the outfit back on") {
			failed++
		}
	}
	if failed <= DressPasses {
		t.Errorf("%d attempts, want more than the %d passes: a failure should not use one up",
			failed, DressPasses)
	}
}
