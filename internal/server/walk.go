package server

// Walking, as the server does it for a client.
//
// The walk itself is agent.Move; everything here is the stream around
// it.  The one thing that is the server's own is the link between the
// stream and the walk: the walk runs under the stream's context, so a
// client that goes away -- closes the stream, exits, crashes, loses its
// link, all of which end the context -- stops the avatar rather than
// leaving it walking.  That is the reason walking is an RPC and not a
// client holding Control down.

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// moveBacklog is how many progress reports may wait for a slow client.
//
// Reports are not held for a client that is not reading them.  The walk
// is steered from the loop that makes them, and a report that waited on
// the client would be an avatar that went unsteered while it did; a
// report that is dropped costs nothing, since the next is a quarter of a
// second behind and says everything it would have.  The last report is
// never dropped -- it is sent after the walk is over, when there is
// nothing left to hold up.
const moveBacklog = 16

// Move walks the avatar and streams how it is going.
func (s *Server) Move(req *pb.MoveRequest, stream pb.Grid_MoveServer) error {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return err
	}
	a := h.Agent()
	if a == nil {
		return status.Error(codes.Unavailable, "this session is not connected")
	}
	if req.Target == nil {
		return status.Error(codes.InvalidArgument, "a walk needs a target")
	}
	r := agent.MoveRequest{
		Target:        msg.Vector3{X: req.Target.X, Y: req.Target.Y, Z: req.Target.Z},
		Within:        req.Within,
		Run:           req.Run,
		Timeout:       time.Duration(req.TimeoutMs) * time.Millisecond,
		StallDistance: req.StallDistance,
		StallTime:     time.Duration(req.StallMs) * time.Millisecond,
	}

	ctx := stream.Context()
	reports := make(chan *pb.MoveEvent, moveBacklog)
	sent := make(chan error, 1)
	go func() {
		var err error
		for e := range reports {
			if err == nil {
				err = stream.Send(e)
			}
		}
		sent <- err
	}()

	end, err := a.Move(ctx, r, func(p agent.MoveProgress) {
		select {
		case reports <- moveEvent(p):
		default:
		}
	})
	close(reports)
	if serr := <-sent; serr != nil && err == nil && ctx.Err() == nil {
		err = serr
	}
	if err != nil {
		return status.Errorf(codes.Unavailable, "%v", err)
	}
	return stream.Send(moveEvent(end))
}

func moveEvent(p agent.MoveProgress) *pb.MoveEvent {
	return &pb.MoveEvent{
		State:           moveState(p.State),
		Reason:          p.Reason,
		Position:        vec(p.Position),
		Exact:           p.Exact,
		Velocity:        vec(p.Velocity),
		Yaw:             p.Yaw,
		Remaining:       p.Remaining,
		SinceProgressMs: uint32(p.SinceProgress / time.Millisecond),
		ElapsedMs:       uint32(p.Elapsed / time.Millisecond),
	}
}

func moveState(s agent.MoveState) pb.MoveEvent_State {
	switch s {
	case agent.Arrived:
		return pb.MoveEvent_ARRIVED
	case agent.Blocked:
		return pb.MoveEvent_BLOCKED
	case agent.Cancelled:
		return pb.MoveEvent_CANCELLED
	case agent.OutOfRegion:
		return pb.MoveEvent_OUT_OF_REGION
	case agent.Refused:
		return pb.MoveEvent_REFUSED
	}
	return pb.MoveEvent_MOVING
}

// Face turns the avatar without moving it.
func (s *Server) Face(ctx context.Context, req *pb.FaceRequest) (*pb.FaceResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	a := h.Agent()
	if a == nil {
		return nil, status.Error(codes.Unavailable, "this session is not connected")
	}
	var yaw float32
	switch t := req.Toward.(type) {
	case *pb.FaceRequest_Target:
		yaw, err = a.Face(ctx, msg.Vector3{X: t.Target.GetX(), Y: t.Target.GetY(), Z: t.Target.GetZ()})
	case *pb.FaceRequest_Yaw:
		yaw, err = t.Yaw, a.FaceYaw(ctx, t.Yaw)
	default:
		return nil, status.Error(codes.InvalidArgument, "face which way: a target or a yaw")
	}
	if err != nil {
		return nil, faceStatus(err)
	}
	return &pb.FaceResponse{Yaw: yaw}, nil
}

// faceStatus says a refusal is one, and a failure to send is not.
func faceStatus(err error) error {
	switch {
	case errors.Is(err, agent.ErrSeated), errors.Is(err, agent.ErrViewerHolds):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, agent.ErrNoDirection):
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return status.Errorf(codes.Unavailable, "%v", err)
}

// Halt ends any walk and stops the avatar.
func (s *Server) Halt(ctx context.Context, req *pb.HaltRequest) (*pb.HaltResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	a := h.Agent()
	if a == nil {
		return nil, status.Error(codes.Unavailable, "this session is not connected")
	}
	walking, err := a.Halt(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%v", err)
	}
	return &pb.HaltResponse{Walking: walking}, nil
}

// Posture says how the avatar is placed, as the agent knows it.
//
// The agent has watched every animation and every reparenting since the
// session logged in; a client attached since has not, and a ground sit
// in particular leaves nothing but an animation that was sent once.  So
// a client that wants to know -- before a walk, which is refused while
// seated -- asks here rather than guessing from what it happened to see.
func (s *Server) Posture(ctx context.Context, req *pb.PostureRequest) (*pb.PostureResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	a := h.Agent()
	if a == nil {
		return nil, status.Error(codes.Unavailable, "this session is not connected")
	}
	p, seat := a.Posture()
	out := &pb.PostureResponse{SeatLocal: seat.Local}
	switch p {
	case agent.SittingOnGround:
		out.Posture = pb.PostureResponse_SITTING_ON_GROUND
	case agent.SittingOnObject:
		out.Posture = pb.PostureResponse_SITTING_ON_OBJECT
		if !seat.ID.IsZero() {
			out.SeatId = seat.ID.String()
		}
	}
	return out, nil
}
