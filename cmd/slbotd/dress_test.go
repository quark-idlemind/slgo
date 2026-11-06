package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/client"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
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

// TestALoginAgainDressesAgain: slgod logging the avatar in again keeps
// the stream open, so the attach that dressed it once is not repeated,
// and after a region restart the avatar came back missing a dress.  The
// notice that it was logged in again dresses it again; a teleport or a
// crossing, which keep the attachments, does not.
func TestALoginAgainDressesAgain(t *testing.T) {
	_, b, _ := newTestDaemon(t)
	notices := make(chan *pb.AgentEvent, 4)
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.drainNotices(ctx, notices, func() { calls.Add(1) })

	notices <- &pb.AgentEvent{Kind: pb.AgentEvent_REGION_CHANGED, Detail: "the avatar is now in Example"}
	notices <- &pb.AgentEvent{Kind: pb.AgentEvent_DISCONNECTED, Detail: "kicked"}
	notices <- &pb.AgentEvent{Kind: pb.AgentEvent_REGION_CHANGED, Detail: client.SessionReEstablished}
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Errorf("dressed again %d times for a teleport, a kick and a login again; want once", n)
	}
}

// TestARestartStopsTheRunBeforeIt: two passes at an outfit at once
// would each put on what the other is putting on.
func TestARestartStopsTheRunBeforeIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var running, stopped atomic.Int32
	again := restarting(ctx, func(ctx context.Context) {
		running.Add(1)
		<-ctx.Done()
		running.Add(-1)
		stopped.Add(1)
	})
	again()
	again()
	again()
	waitUntil := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %d running, %d stopped", what, running.Load(), stopped.Load())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitUntil("two stopped and one running", func() bool { return stopped.Load() == 2 && running.Load() == 1 })
	cancel()
	waitUntil("the last stopped with its context", func() bool { return running.Load() == 0 })
}
