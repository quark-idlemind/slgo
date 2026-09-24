package sl

// Walking, as a hosted session asks for it.
//
// The walk itself is the daemon's and is tested there and in the agent;
// what is here is the translation, which is where a field read off its
// neighbour would hide: every figure in a report crosses the wire and
// comes back as this package's own type, and a refusal comes back as the
// same error a direct session would have given.

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

func (d *fakeDaemon) Move(r *pb.MoveRequest, s grpc.ServerStreamingServer[pb.MoveEvent]) error {
	if d.walked != nil {
		d.walked <- r
	}
	for _, e := range d.walk {
		if err := s.Send(e); err != nil {
			return err
		}
	}
	return nil
}

func (d *fakeDaemon) Face(_ context.Context, r *pb.FaceRequest) (*pb.FaceResponse, error) {
	if d.faced != nil {
		d.faced <- r
	}
	return d.face(r)
}

func (d *fakeDaemon) Halt(context.Context, *pb.HaltRequest) (*pb.HaltResponse, error) {
	return &pb.HaltResponse{Walking: true}, nil
}

// TestAWalkCrossesTheWireFieldByField: the request out, and each report
// and the end back.
func TestAWalkCrossesTheWireFieldByField(t *testing.T) {
	h, d := newFakeDaemon(t)
	d.walked = make(chan *pb.MoveRequest, 1)
	d.walk = []*pb.MoveEvent{
		{State: pb.MoveEvent_MOVING, Position: &pb.Vector3{X: 1, Y: 2, Z: 3}, Exact: true,
			Velocity: &pb.Vector3{X: 3.2}, Yaw: 0.5, Remaining: 4, SinceProgressMs: 50, ElapsedMs: 250},
		{State: pb.MoveEvent_ARRIVED, Position: &pb.Vector3{X: 5, Y: 2, Z: 3}, Exact: true,
			Remaining: 0.02, ElapsedMs: 1500},
	}
	w := newSession(t, h)

	var reports []MoveProgress
	end, err := w.Move(context.Background(), MoveRequest{
		Target: msg.Vector3{X: 5, Y: 2}, Within: 1.5, Run: true,
		Timeout: 10 * time.Second, StallDistance: 0.4, StallTime: 3 * time.Second,
	}, func(p MoveProgress) { reports = append(reports, p) })
	if err != nil {
		t.Fatal(err)
	}

	r := <-d.walked
	if r.Target.GetX() != 5 || r.Within != 1.5 || !r.Run || r.TimeoutMs != 10000 ||
		r.StallDistance != 0.4 || r.StallMs != 3000 {
		t.Errorf("the request crossed as %v", r)
	}
	if len(reports) != 1 {
		t.Fatalf("%d reports, want the one MOVING", len(reports))
	}
	p := reports[0]
	if p.State != Moving || p.Position != (msg.Vector3{X: 1, Y: 2, Z: 3}) || !p.Exact ||
		p.Velocity.X != 3.2 || p.Yaw != 0.5 || p.Remaining != 4 ||
		p.SinceProgress != 50*time.Millisecond || p.Elapsed != 250*time.Millisecond {
		t.Errorf("the report arrived as %+v", p)
	}
	if end.State != Arrived || end.Remaining != 0.02 || end.Elapsed != 1500*time.Millisecond {
		t.Errorf("the end arrived as %+v", end)
	}
}

// TestAnEndThisBuildDoesNotKnowIsStillAnEnd: a newer daemon may say how
// a walk ended in a way this build has no name for, and reading that as
// a walk still going would wait for ever.
func TestAnEndThisBuildDoesNotKnowIsStillAnEnd(t *testing.T) {
	h, d := newFakeDaemon(t)
	d.walk = []*pb.MoveEvent{{State: pb.MoveEvent_State(99)}}
	w := newSession(t, h)

	end, err := w.Move(context.Background(), MoveRequest{Target: msg.Vector3{X: 5, Y: 2}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if end.State != Cancelled || end.Reason == "" {
		t.Errorf("an unknown end arrived as %v %q", end.State, end.Reason)
	}
}

// TestAStreamThatEndsWithoutAnEndIsAnError: the daemon going away in the
// middle is not a walk that arrived.
func TestAStreamThatEndsWithoutAnEndIsAnError(t *testing.T) {
	h, d := newFakeDaemon(t)
	d.walk = []*pb.MoveEvent{{State: pb.MoveEvent_MOVING}}
	w := newSession(t, h)

	if _, err := w.Move(context.Background(), MoveRequest{Target: msg.Vector3{X: 5, Y: 2}}, nil); err == nil {
		t.Error("a stream with no end in it was taken as a walk")
	}
}

// TestARefusedTurnIsTheSameErrorEitherWay: errors.Is finds the agent's
// own refusal through the daemon, and nobody is shown an rpc status.
func TestARefusedTurnIsTheSameErrorEitherWay(t *testing.T) {
	h, d := newFakeDaemon(t)
	d.faced = make(chan *pb.FaceRequest, 2)
	d.face = func(*pb.FaceRequest) (*pb.FaceResponse, error) {
		return nil, status.Error(codes.FailedPrecondition, agent.ErrSeated.Error())
	}
	w := newSession(t, h)

	_, err := w.Face(context.Background(), msg.Vector3{X: 1, Y: 2})
	if !errors.Is(err, agent.ErrSeated) || err.Error() != agent.ErrSeated.Error() {
		t.Errorf("a turn refused for sitting gave %v", err)
	}
	if r := <-d.faced; r.GetTarget().GetY() != 2 {
		t.Errorf("the turn asked for %v", r)
	}

	d.face = func(r *pb.FaceRequest) (*pb.FaceResponse, error) { return &pb.FaceResponse{Yaw: r.GetYaw()}, nil }
	if err := w.FaceYaw(context.Background(), 1.25); err != nil {
		t.Fatal(err)
	}
	if r := <-d.faced; r.GetYaw() != 1.25 {
		t.Errorf("the heading crossed as %v", r)
	}
	if walking, err := w.Halt(context.Background()); err != nil || !walking {
		t.Errorf("Halt said %v, %v", walking, err)
	}
}

// TestABackendThatCannotWalkSaysSo rather than pretending.
func TestABackendThatCannotWalkSaysSo(t *testing.T) {
	w := &Session{b: &fakeBackend{}}
	if _, err := w.Move(context.Background(), MoveRequest{}, nil); !errors.Is(err, ErrCannotWalk) {
		t.Errorf("Move on a backend that cannot walk gave %v", err)
	}
	if _, err := w.Halt(context.Background()); !errors.Is(err, ErrCannotWalk) {
		t.Errorf("Halt on a backend that cannot walk gave %v", err)
	}
}

// newSession is a Session over a hosted backend a test has attached.
func newSession(t *testing.T, h *Hosted) *Session {
	t.Helper()
	w, err := New(h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return w
}

func (d *fakeDaemon) Posture(context.Context, *pb.PostureRequest) (*pb.PostureResponse, error) {
	if d.posture == nil {
		return nil, status.Error(codes.Unimplemented, "method Posture not implemented")
	}
	return d.posture, nil
}

// TestSeatAsksTheDaemonsAgent: a session attached after the avatar sat on
// the ground has seen no animation to say so, and the daemon's agent has.
// Its answer is taken; standing is taken too, over anything the session
// thought; and a daemon too old to be asked leaves the session's guess.
func TestSeatAsksTheDaemonsAgent(t *testing.T) {
	h, d := newFakeDaemon(t)
	w := newSession(t, h)
	ctx := context.Background()

	d.posture = &pb.PostureResponse{Posture: pb.PostureResponse_SITTING_ON_GROUND}
	seat, err := w.Seat(ctx)
	if err != nil || seat == nil || !seat.Ground {
		t.Errorf("sitting on the ground: %+v, %v", seat, err)
	}

	d.posture = &pb.PostureResponse{Posture: pb.PostureResponse_STANDING}
	if seat, err := w.Seat(ctx); err != nil || seat != nil {
		t.Errorf("standing: %+v, %v", seat, err)
	}

	d.posture = nil
	if seat, err := w.Seat(ctx); err != nil || seat != nil {
		t.Errorf("an old daemon, and nothing seen: %+v, %v", seat, err)
	}
}
