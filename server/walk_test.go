package server

// Walking, through the API, with a session whose avatar goes nowhere.
//
// The simulator here answers the circuit and nothing more: it describes
// no avatar and moves none, so every walk it is asked for stands still.
// That is enough for what this file is about, which is the stream around
// the walk rather than the walk -- agent/walk_test.go has an avatar that
// moves.  What a standing avatar shows is that a walk ends, and says how,
// whatever else happens: blocked, refused, halted, or abandoned by the
// client that asked for it.

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// moveEvents runs one Move through a real client and collects every
// event, the last of them the end.  Safe off the test's goroutine.
func moveEvents(ctx context.Context, c *client.Conn, req *pb.MoveRequest) ([]*pb.MoveEvent, error) {
	var got []*pb.MoveEvent
	end, err := c.Move(ctx, req, func(e *pb.MoveEvent) { got = append(got, e) })
	if end != nil {
		got = append(got, end)
	}
	return got, err
}

// dialed is a client attached to the rig's session, hung up when the
// test ends: the server waits for its clients before it stops.
func dialed(t *testing.T, r *rig) *client.Conn {
	t.Helper()
	c := r.dial(t)
	t.Cleanup(func() { c.Close() })
	return c
}

// stopsSent counts the AgentUpdates carrying STOP that reached the
// simulator.
func stopsSent(t *testing.T, r *rig) int {
	t.Helper()
	r.sim.mu.Lock()
	bodies := append([][]byte(nil), r.sim.bodies["AgentUpdate"]...)
	r.sim.mu.Unlock()
	n := 0
	for _, b := range bodies {
		u := &msg.AgentUpdate{}
		if err := u.Decode(b); err != nil {
			t.Fatalf("an AgentUpdate the simulator got would not decode: %v", err)
		}
		if u.AgentData.ControlFlags&agent.ControlStop != 0 {
			n++
		}
	}
	return n
}

// TestAWalkThatGetsNowhereIsStreamedToItsEnd: reports while it tries,
// one end that says it was blocked, and the stream closed after it.
func TestAWalkThatGetsNowhereIsStreamedToItsEnd(t *testing.T) {
	r := newRig(t, agent.Caps{})
	got, err := moveEvents(context.Background(), dialed(t, r), &pb.MoveRequest{
		Target:  &pb.Vector3{X: 120, Y: 120},
		StallMs: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("%d events for a walk of 0.6 seconds, want reports and an end", len(got))
	}
	for _, e := range got[:len(got)-1] {
		if e.State != pb.MoveEvent_MOVING {
			t.Errorf("an event before the end said %v", e.State)
		}
	}
	end := got[len(got)-1]
	if end.State != pb.MoveEvent_BLOCKED {
		t.Errorf("the end said %v %q", end.State, end.Reason)
	}
	// This simulator never describes the avatar, so where it is comes
	// from where it arrived, and the event says that is not exact.
	if end.Exact || end.Position.GetX() != 1 {
		t.Errorf("the end put the avatar at %v, exact %v", end.Position, end.Exact)
	}
	if end.SinceProgressMs < 600 || end.Remaining < 100 {
		t.Errorf("blocked %d ms since progress with %v m to go", end.SinceProgressMs, end.Remaining)
	}

	// The flag was let go of on the grid, and not merely here.
	deadline := time.Now().Add(2 * time.Second)
	for stopsSent(t, r) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no STOP reached the simulator")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestAWalkThatCannotStartIsOneEvent: a place outside the region is an
// answer about the world and not a failed call.
func TestAWalkThatCannotStartIsOneEvent(t *testing.T) {
	r := newRig(t, agent.Caps{})
	got, err := moveEvents(context.Background(), dialed(t, r), &pb.MoveRequest{Target: &pb.Vector3{X: 300, Y: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].State != pb.MoveEvent_OUT_OF_REGION || got[0].Reason == "" {
		t.Errorf("a walk out of the region streamed %v", got)
	}

	// And a walk to nowhere at all is the caller's mistake.
	c := r.dial(t)
	defer c.Close()
	if _, err := c.Move(context.Background(), &pb.MoveRequest{}, nil); status.Code(err) != codes.InvalidArgument {
		t.Errorf("a walk with no target gave %v", err)
	}
}

// TestHaltEndsAWalkSomebodyElseAskedFor: the walk's own stream is told
// it was halted, and whoever halted it is told there was one.
func TestHaltEndsAWalkSomebodyElseAskedFor(t *testing.T) {
	r := newRig(t, agent.Caps{})
	h, _ := r.srv.Agent("example")

	ended := make(chan []*pb.MoveEvent, 1)
	c := dialed(t, r)
	go func() {
		got, _ := moveEvents(context.Background(), c, &pb.MoveRequest{
			Target: &pb.Vector3{X: 120, Y: 120}, StallMs: 60000,
		})
		ended <- got
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !h.Agent().Walking() {
		if time.Now().After(deadline) {
			t.Fatal("the walk never started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	resp, err := r.srv.Halt(context.Background(), &pb.HaltRequest{Agent: "example"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Walking {
		t.Error("Halt said nothing was walking")
	}
	got := <-ended
	end := got[len(got)-1]
	if end.State != pb.MoveEvent_CANCELLED || end.Reason != agent.CancelHalted {
		t.Errorf("the halted walk ended %v %q", end.State, end.Reason)
	}
}

// TestAClientThatGoesAwayStopsItsWalk: the whole reason walking is the
// server's.  Ending the stream is how every way of going away looks from
// here, and the walk has to end with it.
func TestAClientThatGoesAwayStopsItsWalk(t *testing.T) {
	r := newRig(t, agent.Caps{})
	h, _ := r.srv.Agent("example")

	ctx, cancel := context.WithCancel(context.Background())
	go moveEvents(ctx, dialed(t, r), &pb.MoveRequest{Target: &pb.Vector3{X: 120, Y: 120}, StallMs: 60000})
	deadline := time.Now().Add(5 * time.Second)
	for !h.Agent().Walking() {
		if time.Now().After(deadline) {
			t.Fatal("the walk never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := stopsSent(t, r)
	cancel()

	deadline = time.Now().Add(5 * time.Second)
	for h.Agent().Walking() || stopsSent(t, r) == before {
		if time.Now().After(deadline) {
			t.Fatalf("the client went and the walk did not stop (walking %v)", h.Agent().Walking())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestFaceAnswersWithTheHeading, and refuses to guess which way was
// meant when nothing says.
func TestFaceAnswersWithTheHeading(t *testing.T) {
	r := newRig(t, agent.Caps{})
	ctx := context.Background()

	resp, err := r.srv.Face(ctx, &pb.FaceRequest{Agent: "example", Toward: &pb.FaceRequest_Yaw{Yaw: 1.5}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Yaw != 1.5 {
		t.Errorf("a heading of 1.5 came back as %v", resp.Yaw)
	}
	// The avatar arrived at 1, 2, 3, so a place due north of it is a
	// quarter turn.
	resp, err = r.srv.Face(ctx, &pb.FaceRequest{Agent: "example",
		Toward: &pb.FaceRequest_Target{Target: &pb.Vector3{X: 1, Y: 50}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Yaw < 1.57 || resp.Yaw > 1.572 {
		t.Errorf("facing due north came back as %v", resp.Yaw)
	}

	if _, err := r.srv.Face(ctx, &pb.FaceRequest{Agent: "example"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("a turn toward nothing gave %v", err)
	}
	if _, err := r.srv.Face(ctx, &pb.FaceRequest{Agent: "example",
		Toward: &pb.FaceRequest_Target{Target: &pb.Vector3{X: 1, Y: 2}}}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("a turn toward the spot the avatar stands on gave %v", err)
	}

	// Refusals are refusals, and say so in words a client can match.
	h, _ := r.srv.Agent("example")
	h.Agent().DeferPresence(time.Now().Add(time.Minute))
	_, err = r.srv.Face(ctx, &pb.FaceRequest{Agent: "example", Toward: &pb.FaceRequest_Yaw{Yaw: 0}})
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != agent.ErrViewerHolds.Error() {
		t.Errorf("a turn under a viewer gave %v", err)
	}
}
