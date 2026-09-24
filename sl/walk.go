package sl

// Walking the avatar somewhere, turning it, and stopping it.
//
// The walk is done by whoever holds the circuit -- slgod for a hosted
// session, this process for a direct one -- and not by this package,
// because it is a control flag held down on AgentUpdate ten times a
// second and the thing holding it down has to be the thing that is sure
// to let go.  See agent/walk.go for how a walk is made and what was taken
// from the viewer to make it, and the Move rpc in proto/slgo.proto for
// why it is the daemon's.
//
// What a caller gets is a straight line to a place in the region and a
// running account of how it is going.  Nothing is walked round: an
// obstacle, or another avatar, is the caller's to steer past by asking
// again with another target.  What the walk does notice is that the
// avatar has stopped getting closer, which ends it as Blocked.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// MoveRequest is where to walk to and how; see agent.MoveRequest for
// each field.  An alias, so that a direct session hands it to the agent
// as it is.
type MoveRequest = agent.MoveRequest

// MoveProgress is how a walk is going, or how it ended; see
// agent.MoveProgress.
type MoveProgress = agent.MoveProgress

// MoveState is where a walk has got to.
type MoveState = agent.MoveState

// The states a walk can be in.  Every one but Moving is an end.
const (
	Moving      = agent.Moving
	Arrived     = agent.Arrived
	Blocked     = agent.Blocked
	Cancelled   = agent.Cancelled
	OutOfRegion = agent.OutOfRegion
	Refused     = agent.Refused
)

// Why a walk was Cancelled: MoveProgress.Reason.
const (
	CancelSuperseded   = agent.CancelSuperseded
	CancelHalted       = agent.CancelHalted
	CancelClientGone   = agent.CancelClientGone
	CancelTimeout      = agent.CancelTimeout
	CancelLeftRegion   = agent.CancelLeftRegion
	CancelSeated       = agent.CancelSeated
	CancelViewer       = agent.CancelViewer
	CancelSessionEnded = agent.CancelSessionEnded
	CancelOvershot     = agent.CancelOvershot
)

// A Mover is a backend that can walk the avatar.
//
// Both backends in this package are.  It is an interface of its own
// rather than part of Backend so that the stand-ins other packages build
// for Backend in their tests need not grow four methods they would never
// be asked for; a backend that is not one is answered with
// ErrCannotWalk.
type Mover interface {
	Move(ctx context.Context, r MoveRequest, progress func(MoveProgress)) (MoveProgress, error)
	Face(ctx context.Context, target msg.Vector3) (float32, error)
	FaceYaw(ctx context.Context, yaw float32) error
	Halt(ctx context.Context) (bool, error)
}

// ErrCannotWalk is a backend that has no way to walk the avatar.
var ErrCannotWalk = errors.New("sl: this session cannot walk the avatar")

func (w *Session) mover() (Mover, error) {
	m, ok := w.b.(Mover)
	if !ok {
		return nil, ErrCannotWalk
	}
	return m, nil
}

// Move walks the avatar in a straight line to a place in its region, and
// returns how that ended.
//
// It blocks until the walk is over.  Progress, when it is not nil, is
// called about four times a second while the avatar moves, with where it
// is, how fast it is going, which way it faces and how far it has left;
// the end is returned and is not passed to progress as well.
//
// The end says what happened: Arrived, Blocked, or Cancelled with a
// reason.  A walk that could not start comes back the same way, as
// OutOfRegion or Refused with the reason, and not as an error: nothing
// was sent and nothing is wrong with the session.  The error is kept for
// not being able to ask.
//
// A target already within agent.MinWithin (half a metre) of the avatar,
// or within Within, is Arrived at once and nothing moves: the shortest
// walk an avatar can make from standing is about a metre (measured
// 2026-09-24), so a nearer target has no step to take.  A caller planning
// its own legs should not send ones that short, or it will be told
// Arrived for ever without going anywhere.
//
// Cancelling ctx stops the avatar and ends the walk Cancelled, "client
// gone".  Another Move takes this one over -- it ends Cancelled,
// "superseded", and the avatar does not stop in between -- which is how
// a caller steering round something changes its mind; Halt stops it.
func (w *Session) Move(ctx context.Context, r MoveRequest, progress func(MoveProgress)) (MoveProgress, error) {
	m, err := w.mover()
	if err != nil {
		return MoveProgress{}, err
	}
	return m.Move(ctx, r, progress)
}

// Face turns the avatar toward a place in the region without moving it,
// and returns the heading it was given: radians anticlockwise from east.
// It ends a walk under way.
func (w *Session) Face(ctx context.Context, target msg.Vector3) (float32, error) {
	m, err := w.mover()
	if err != nil {
		return 0, err
	}
	return m.Face(ctx, target)
}

// FaceYaw turns the avatar to a heading, radians anticlockwise from
// east.
func (w *Session) FaceYaw(ctx context.Context, yaw float32) error {
	m, err := w.mover()
	if err != nil {
		return err
	}
	return m.FaceYaw(ctx, yaw)
}

// Halt ends any walk and stops the avatar, and says whether a walk was
// under way.  The stop is sent either way.
func (w *Session) Halt(ctx context.Context) (bool, error) {
	m, err := w.mover()
	if err != nil {
		return false, err
	}
	return m.Halt(ctx)
}

// ------------------------------------------------------------ direct

func (d *Direct) Move(ctx context.Context, r MoveRequest, progress func(MoveProgress)) (MoveProgress, error) {
	return d.a.Move(ctx, r, progress)
}

func (d *Direct) Face(ctx context.Context, target msg.Vector3) (float32, error) {
	return d.a.Face(ctx, target)
}

func (d *Direct) FaceYaw(ctx context.Context, yaw float32) error {
	return d.a.FaceYaw(ctx, yaw)
}

func (d *Direct) Halt(ctx context.Context) (bool, error) { return d.a.Halt(ctx) }

// ------------------------------------------------------------ hosted

func (h *Hosted) Move(ctx context.Context, r MoveRequest, progress func(MoveProgress)) (MoveProgress, error) {
	req := &pb.MoveRequest{
		Target:        &pb.Vector3{X: r.Target.X, Y: r.Target.Y, Z: r.Target.Z},
		Within:        r.Within,
		Run:           r.Run,
		TimeoutMs:     millis(r.Timeout),
		StallDistance: r.StallDistance,
		StallMs:       millis(r.StallTime),
	}
	var each func(*pb.MoveEvent)
	if progress != nil {
		each = func(e *pb.MoveEvent) { progress(moveFromPB(e)) }
	}
	end, err := h.conn.Move(ctx, req, each)
	if err != nil {
		return MoveProgress{}, err
	}
	return moveFromPB(end), nil
}

func (h *Hosted) Face(ctx context.Context, target msg.Vector3) (float32, error) {
	yaw, err := h.conn.Face(ctx, &pb.Vector3{X: target.X, Y: target.Y, Z: target.Z}, 0)
	return yaw, faceError(err)
}

func (h *Hosted) FaceYaw(ctx context.Context, yaw float32) error {
	_, err := h.conn.Face(ctx, nil, yaw)
	return faceError(err)
}

// faceError turns a refusal from the daemon back into the error a direct
// session would have given, so that errors.Is finds agent.ErrSeated and
// the rest whichever way the session is held, and a person is shown the
// sentence rather than the rpc status wrapped round it.
func faceError(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.FailedPrecondition, codes.InvalidArgument:
		for _, known := range []error{agent.ErrSeated, agent.ErrViewerHolds, agent.ErrNoDirection} {
			if prefix, ok := strings.CutSuffix(st.Message(), known.Error()); ok {
				if prefix == "" {
					return known
				}
				return fmt.Errorf("%s%w", prefix, known)
			}
		}
		return errors.New(st.Message())
	}
	return err
}

func (h *Hosted) Halt(ctx context.Context) (bool, error) { return h.conn.Halt(ctx) }

// millis is a duration as the wire carries it, which cannot say more
// than about seven weeks and has no reason to.
func millis(d time.Duration) uint32 {
	if d <= 0 {
		return 0
	}
	ms := d / time.Millisecond
	if ms > 1<<32-1 {
		return 1<<32 - 1
	}
	return uint32(ms)
}

func moveFromPB(e *pb.MoveEvent) MoveProgress {
	state, reason := moveStateFromPB(e.State), e.Reason
	if _, known := pb.MoveEvent_State_name[int32(e.State)]; !known && reason == "" {
		reason = fmt.Sprintf("a state this build does not know (%d)", int32(e.State))
	}
	return MoveProgress{
		State:         state,
		Reason:        reason,
		Position:      fromPB(e.Position),
		Exact:         e.Exact,
		Velocity:      fromPB(e.Velocity),
		Yaw:           e.Yaw,
		Remaining:     e.Remaining,
		SinceProgress: time.Duration(e.SinceProgressMs) * time.Millisecond,
		Elapsed:       time.Duration(e.ElapsedMs) * time.Millisecond,
	}
}

// moveStateFromPB reads a state off the wire.  One this build does not
// know is from a daemon newer than it, and is an end of some kind --
// every state but MOVING is -- so it is read as Cancelled, and
// moveFromPB puts the number in the reason, rather than as a walk still
// going.
func moveStateFromPB(s pb.MoveEvent_State) MoveState {
	switch s {
	case pb.MoveEvent_MOVING:
		return Moving
	case pb.MoveEvent_ARRIVED:
		return Arrived
	case pb.MoveEvent_BLOCKED:
		return Blocked
	case pb.MoveEvent_CANCELLED:
		return Cancelled
	case pb.MoveEvent_OUT_OF_REGION:
		return OutOfRegion
	case pb.MoveEvent_REFUSED:
		return Refused
	}
	return Cancelled
}
