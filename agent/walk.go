package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// Walking.
//
// There is no message that says "walk over there".  A viewer walks an
// avatar the way a person does, by holding a key: every AgentUpdate it
// sends while the key is down carries AGENT_CONTROL_AT_POS, the body
// rotation says which way is forward, and the simulator moves the avatar
// forward for as long as the flag keeps arriving.  Letting go of the key
// is the next update not carrying it.  Walking to a place is the same
// thing with somebody steering: the viewer's autopilot
// (LLAgent::autoPilot, in llagent.cpp) holds the flag, turns the body
// toward the target a little every frame, and lets go when it is close
// enough.  Nothing about it is the simulator's; the grid has no idea the
// avatar is going anywhere in particular.
//
// So it lives here, beside the presence loop, and for the reason the
// camera does.  The flags ride on AgentUpdate, and AgentUpdate is this
// package's to send -- a client that built its own would be inventing a
// camera, see Control -- and a walk has to keep being sent: ten times a
// second, steering from where the avatar actually is, for as long as it
// takes.  A client that stopped in the middle, because it exited or
// crashed or lost its link, would leave the flag held in the last update
// the simulator had, and a held flag is an avatar that walks until it
// meets a wall.  Here the one that holds the flag is the one that is
// certain to let go of it.
//
// # What is taken from the viewer
//
// The flags.  A key held down sends AT_POS together with FAST_AT
// (LLAgent::moveAt); the autopilot sends the same pair while it is far
// from the target and drops FAST_AT for AT_POS alone as it closes in.
// Running is not a flag at all: it is SetAlwaysRun, a message of its
// own, which the viewer sends when the run key is toggled
// (LLAgent::sendWalkRun) -- so Run here sends that, and sends it back
// the other way when the walk is over.
//
// The rotation.  BodyRotation is the avatar's facing, a rotation about
// the vertical, and HeadRotation is the same rotation outside mouselook
// (LLAgent::getHeadRotation returns the avatar's own rotation there).
// Before anything here was written both went out as zero, which is the
// identity: facing east.  They are zero still for a session nothing has
// walked or turned.
//
// Stopping.  The autopilot, and the viewer's stop key, set
// AGENT_CONTROL_STOP on one update when they let go; and
// send_agent_update in llviewermessage.cpp sends an update again after
// any change of flags, with a comment that the simulator "probably
// shouldn't require a reminder to stop a motion, but at the moment it
// does".  So a stop here is the same two updates: STOP, and then
// nothing held.
//
// # What is not taken
//
// The steering is not the autopilot's.  That turns the body a fraction
// of the way each frame and holds FAST_AT only while it is facing within
// a tenth of pi of the target; this sends the whole rotation at once,
// every update, because it sends ten updates a second rather than one
// per frame, and the simulator turns the avatar to face what it was last
// told -- measured, a Face read back a second later was within five
// degrees of what was sent.
//
// Nor is the simulator's own autopilot used, the GenericMessage
// "autopilot" the viewer's "Go here" sends.  It says nothing about how
// it is going, cannot be told to stop by anything here, and stops where
// it likes.
// Why: doc/walk.md#the-simulators-own-autopilot
//
// Nothing here avoids anything.  The walk is a straight line, and what
// is in the way -- a wall, a tree, another avatar -- is the client's to
// steer round, by asking again with a different target; slgod neither
// sees obstacles nor looks for them, by decision rather than omission.
// What it does do is notice that the avatar has stopped getting closer
// (see Blocked), which is how a wall shows up from here.

// The control flags a walk holds, from indra/llcommon/indra_constants.h
// in a Firestorm checkout, where they are written as shifts of named
// indices with the values in comments beside them.
const (
	ControlAtPos  uint32 = 0x00000001 // AGENT_CONTROL_AT_POS
	ControlFastAt uint32 = 0x00000400 // AGENT_CONTROL_FAST_AT
	ControlStop   uint32 = 0x00004000 // AGENT_CONTROL_STOP

	// The rest a script can take with llTakeControls, and so the ones
	// ScriptControls names; see controls.go.  Values as above,
	// indra_constants.h:323-359.
	ControlAtNeg         uint32 = 0x00000002 // AGENT_CONTROL_AT_NEG
	ControlLeftPos       uint32 = 0x00000004 // AGENT_CONTROL_LEFT_POS
	ControlLeftNeg       uint32 = 0x00000008 // AGENT_CONTROL_LEFT_NEG
	ControlUpPos         uint32 = 0x00000010 // AGENT_CONTROL_UP_POS
	ControlUpNeg         uint32 = 0x00000020 // AGENT_CONTROL_UP_NEG
	ControlYawPos        uint32 = 0x00000100 // AGENT_CONTROL_YAW_POS
	ControlYawNeg        uint32 = 0x00000200 // AGENT_CONTROL_YAW_NEG
	ControlNudgeAtPos    uint32 = 0x00080000 // AGENT_CONTROL_NUDGE_AT_POS
	ControlNudgeAtNeg    uint32 = 0x00100000 // AGENT_CONTROL_NUDGE_AT_NEG
	ControlNudgeLeftPos  uint32 = 0x00200000 // AGENT_CONTROL_NUDGE_LEFT_POS
	ControlNudgeLeftNeg  uint32 = 0x00400000 // AGENT_CONTROL_NUDGE_LEFT_NEG
	ControlNudgeUpPos    uint32 = 0x00800000 // AGENT_CONTROL_NUDGE_UP_POS
	ControlNudgeUpNeg    uint32 = 0x01000000 // AGENT_CONTROL_NUDGE_UP_NEG
	ControlLButtonDown   uint32 = 0x10000000 // AGENT_CONTROL_LBUTTON_DOWN
	ControlLButtonUp     uint32 = 0x20000000 // AGENT_CONTROL_LBUTTON_UP
	ControlMLLButtonDown uint32 = 0x40000000 // AGENT_CONTROL_ML_LBUTTON_DOWN
	ControlMLLButtonUp   uint32 = 0x80000000 // AGENT_CONTROL_ML_LBUTTON_UP
)

// RegionWidth is how many metres a region is across, and so the bound a
// walk's target is refused outside of.
//
// It is not read from the grid.  Every region on Second Life's own grid
// is 256 metres square; the variable sized regions some OpenSimulator
// grids run would be refused wrongly past 256, and nothing here has been
// pointed at one.
const RegionWidth = 256

// Walking defaults.
const (
	// MinWithin is the least distance a walk counts as close enough.  A
	// request for less, zero included, is judged against this; zero
	// still means "as close as you can", in where the walk aims (see
	// aimFor).  A window a metre across is about the shortest walk there
	// is from a standstill (see stopLead), so a narrower one is one a
	// walk that stopped outside it could step straight over again.
	MinWithin = 0.5

	// DefaultStallDistance and DefaultStallTime are what Blocked means
	// when the request does not say: less than this much closer over
	// that long.
	DefaultStallDistance = 0.3
	DefaultStallTime     = 2 * time.Second
)

// How often a walk steers and how often it reports.
//
// Ten updates a second is a fraction of what a viewer sends -- the
// viewer is capped at 125 a second and sends one whenever the flags or
// the facing change -- and is enough: an avatar walks about three metres
// a second, so between two updates it goes about a third of a metre.
// The reports are for a client deciding what to do next, and four a
// second is enough for that without the progress stream costing more
// than the walk.
var (
	moveTick      = 100 * time.Millisecond
	progressEvery = 250 * time.Millisecond
)

// Stopping where asked.
//
// An avatar does not stop where the flag is let go of: it goes on at
// full speed for about 0.28 seconds, walking or running.  So the flag is
// let go of stopLead ahead of where the avatar is going to be, at the
// speed the simulator last said it was going (see aimFor for how far
// short of the target that is aimed), and the walk is only called over
// once the avatar has stopped -- below settledSpeed, or settleTime after
// letting go, whichever is first -- and been found close enough.
//
// One that stopped too far off walks again, up to maxStops times in
// all; it then ends Cancelled, "overshot".  A walk from a standstill
// cannot be made much shorter than a metre and still go the way the
// avatar faces, so nothing here tries to step finer than a walk.
// Why: doc/walk.md#stopping-where-asked
var (
	stopLead     = 300 * time.Millisecond
	settleTime   = 1500 * time.Millisecond
	settledSpeed = float32(0.2)
	maxStops     = 3
)

// MoveRequest is where to walk to and how.
type MoveRequest struct {
	// Target is a place in this region, in the region's own metres.
	// Only X and Y are walked to: the ground decides the height, and a
	// walk does not climb or fly.  A target outside the region is
	// refused before anything moves, because a walk never crosses a
	// border.
	Target msg.Vector3

	// Within is how close is close enough, measured flat.  The walk
	// aims to come to rest a little inside it rather than at the
	// target, so that walking up to somebody stops short of them; zero
	// aims at the target itself.  Arriving is judged against it or
	// MinWithin, whichever is larger.
	Within float32

	// Run asks the simulator to run rather than walk.
	Run bool

	// Timeout ends the walk if it has not arrived by then.  Zero is no
	// limit: a walk that stops getting anywhere ends as Blocked anyway.
	Timeout time.Duration

	// StallDistance and StallTime say what Blocked means: less than
	// StallDistance closer to the target over StallTime.  Zero is the
	// default for each.
	StallDistance float32
	StallTime     time.Duration
}

func (r MoveRequest) withDefaults() MoveRequest {
	if r.Within < MinWithin {
		r.Within = MinWithin
	}
	if r.StallDistance <= 0 {
		r.StallDistance = DefaultStallDistance
	}
	if r.StallTime <= 0 {
		r.StallTime = DefaultStallTime
	}
	return r
}

// MoveState is where a walk has got to.  Every state but Moving is an
// end.
type MoveState int

const (
	// Moving is a walk under way.  Only progress reports carry it.
	Moving MoveState = iota

	// Arrived is within the distance asked for.
	Arrived

	// Blocked is a walk that stopped getting closer: less than the stall
	// distance over the stall time.  A wall does this, and so would
	// another avatar standing in the way or a target on the far side of
	// anything that cannot be walked through.  Measured on Agni,
	// 2026-09-24: walking into something unseen on a platform, the
	// avatar was stopped dead half a second in and the walk ended Blocked
	// 2.9 seconds after it began, the default two seconds after the last
	// progress it made.
	Blocked

	// Cancelled is a walk ended by something other than where the
	// avatar got to; the reason says what.
	Cancelled

	// OutOfRegion is a target that is not in this region.  It is
	// refused before anything moves.
	OutOfRegion

	// Refused is a walk that was never started, for the reason given:
	// the avatar is sitting, a viewer is driving it, or a script has
	// taken the forward control.
	Refused
)

func (s MoveState) String() string {
	switch s {
	case Moving:
		return "moving"
	case Arrived:
		return "arrived"
	case Blocked:
		return "blocked"
	case Cancelled:
		return "cancelled"
	case OutOfRegion:
		return "out of region"
	case Refused:
		return "refused"
	}
	return fmt.Sprintf("move state %d", int(s))
}

// Why a walk was cancelled.  They are the Reason of a Cancelled end.
const (
	// CancelSuperseded is another Move, or a Face, taking over.  The
	// avatar is not stopped between the two: the new walk holds the
	// flag from the next update.
	CancelSuperseded = "superseded"

	// CancelHalted is Halt.
	CancelHalted = "halted"

	// CancelClientGone is the caller's context ending -- for a walk
	// asked for over slgod's API, the client going away.
	CancelClientGone = "client gone"

	// CancelTimeout is the request's own Timeout running out.
	CancelTimeout = "timeout"

	// CancelLeftRegion is the avatar no longer being in the region the
	// walk started in, which a walk never does of its own accord: it
	// was teleported, or pushed over the border.
	CancelLeftRegion = "left the region"

	// CancelSeated is the avatar being sat down while it walked.
	CancelSeated = "seated"

	// CancelControlsTaken is a script taking the forward control, and
	// not passing it on, while the walk was under way.  See ErrControlsTaken.
	CancelControlsTaken = "a script took the forward control"

	// CancelViewer is a viewer taking the camera over while the walk
	// was under way.  See DeferPresence.
	CancelViewer = "a viewer took over"

	// CancelSessionEnded is the session going away under the walk.
	CancelSessionEnded = "session ended"

	// CancelOvershot is a walk that stopped outside the distance asked
	// for maxStops times running.  The shortest walk there is from a
	// standstill is about a metre (see stopLead), so a target closer
	// than that with a small Within can be one no walk lands in.
	CancelOvershot = "overshot"
)

// MoveProgress is how a walk is going, or how it ended.
type MoveProgress struct {
	State MoveState

	// Reason says why a walk was Cancelled, Refused or OutOfRegion, and
	// is empty otherwise.
	Reason string

	// Position is where the avatar is, and Exact whether that is the
	// simulator's own figure for this avatar's object or only the
	// whole-metre one CoarseLocationUpdate carries.  It is exact
	// whenever the region has described this avatar, which is always
	// soon after arrival.
	Position msg.Vector3
	Exact    bool

	// Velocity is how fast the avatar was last said to be moving, in
	// metres a second, from the simulator's terse updates for it.  Zero
	// when none has arrived.
	Velocity msg.Vector3

	// Yaw is which way the avatar is facing, as the simulator last said
	// it: radians anticlockwise from east, the positive X axis, between
	// minus pi and pi.
	Yaw float32

	// Remaining is the flat distance still to go to the target.
	Remaining float32

	// SinceProgress is how long since the avatar last got StallDistance
	// closer, which is what Blocked is decided on.
	SinceProgress time.Duration

	// Elapsed is how long the walk has been going.
	Elapsed time.Duration
}

// Ended says whether this is the last word on a walk.
func (p MoveProgress) Ended() bool { return p.State != Moving }

// walk is one Move while it runs.  Stop is how anything else ends it:
// whoever sends on it has already taken the walk out of walker.current,
// and with it the say over what is held.
type walk struct {
	stop chan string
}

func (w *walk) end(reason string) {
	select {
	case w.stop <- reason:
	default:
	}
}

// walker is what an Agent knows about walking.
//
// The mutex decides who is walking and serializes everything that is
// sent on a walk's behalf, so that a walk that has just been superseded
// cannot put an update on the circuit after its successor's.  The rule
// that falls out of it is the one to remember: whoever takes a walk out
// of current becomes responsible for what is held afterwards.  A new
// Move takes it over without letting go; Halt and Face let go
// themselves; a walk that ends of its own accord lets go on its way out.
//
// flags and body are what every AgentUpdate carries, the presence loop's
// included.  They are behind a mutex of their own because agentUpdate
// reads them, and a walk builds its updates while it holds mu.
type walker struct {
	mu      sync.Mutex
	current *walk
	running bool

	driveMu sync.RWMutex
	flags   uint32
	body    msg.Quaternion
}

// drive is what the next AgentUpdate should hold down and which way it
// should face.
func (a *Agent) drive() (uint32, msg.Quaternion) {
	a.walker.driveMu.RLock()
	defer a.walker.driveMu.RUnlock()
	return a.walker.flags, a.walker.body
}

func (a *Agent) setDrive(flags uint32, body msg.Quaternion) {
	a.walker.driveMu.Lock()
	a.walker.flags, a.walker.body = flags, body
	a.walker.driveMu.Unlock()
}

// Placement is where this avatar is, as exactly as it is known.
//
// The object store has the simulator's own figures for this avatar,
// kept current by the ImprovedTerseObjectUpdate it sends several times a
// second while the avatar moves, and that is what a walk has to steer
// by: Position is CoarseLocationUpdate while walking, whole metres and
// seconds behind.  Exact says which of the two this is.  The store has
// this avatar soon after arrival, and a seated avatar's figures there
// are an offset from its seat, so Position is what answers for one of
// those.
//
// # Reckoned forward from the last update
//
// The position is where the simulator last said the avatar was, moved on
// by the velocity it said it had for as long ago as it said it, up to
// deadReckoning.  The simulator does not send a terse update for every
// step of a walk: it sends one when the motion stops being what the last
// one predicted, which is what a viewer's own extrapolation assumes, and
// a second can pass with none while the avatar walks on.  A wall
// stopping the avatar changes its motion, and that is sent.
// Why: doc/walk.md#reckoned-forward
func (a *Agent) Placement() (pos, vel msg.Vector3, rot msg.Quaternion, exact bool) {
	if a.Account != nil {
		if own, ok := a.Objects().Get(a.Account.AgentID); ok && own.Parent == 0 {
			return reckon(own.Position, own.Velocity, own.Moved, time.Now()), own.Velocity, own.Rotation, true
		}
	}
	return a.Position(), msg.Vector3{}, msg.Quaternion{}, false
}

// deadReckoning is the furthest back a position is carried forward by
// its velocity.  Beyond it the last word is taken as it is, since
// something that has said nothing for that long is as likely to have
// stopped as to have carried on.
const deadReckoning = 1500 * time.Millisecond

// reckon is where something at pos, moving at vel, as of when, has got to
// by now.
func reckon(pos, vel msg.Vector3, when, now time.Time) msg.Vector3 {
	if when.IsZero() || vel == (msg.Vector3{}) {
		return pos
	}
	age := now.Sub(when)
	if age <= 0 {
		return pos
	}
	if age > deadReckoning {
		age = deadReckoning
	}
	t := float32(age.Seconds())
	return msg.Vector3{X: pos.X + vel.X*t, Y: pos.Y + vel.Y*t, Z: pos.Z + vel.Z*t}
}

// YawTo is the facing from one place toward another, flat: radians
// anticlockwise from east.
func YawTo(from, to msg.Vector3) float32 {
	return float32(math.Atan2(float64(to.Y-from.Y), float64(to.X-from.X)))
}

// YawRotation is the rotation that faces an avatar that way, about the
// vertical and nothing else.
func YawRotation(yaw float32) msg.Quaternion {
	h := float64(yaw) / 2
	return msg.PackQuaternion(0, 0, float32(math.Sin(h)), float32(math.Cos(h)))
}

// RotationYaw is which way a rotation faces, flat, ignoring any tilt.
func RotationYaw(q msg.Quaternion) float32 {
	x, y, z, w := float64(q.X), float64(q.Y), float64(q.Z), float64(q.W())
	return float32(math.Atan2(2*(w*z+x*y), 1-2*(y*y+z*z)))
}

// flat is the distance between two places ignoring height.
func flat(a, b msg.Vector3) float32 {
	dx, dy := float64(b.X-a.X), float64(b.Y-a.Y)
	return float32(math.Hypot(dx, dy))
}

// inRegion says whether a place is in a region, flat.
func inRegion(v msg.Vector3) bool {
	return v.X >= 0 && v.X < RegionWidth && v.Y >= 0 && v.Y < RegionWidth
}

// ErrViewerHolds is what a walk or a turn is refused with while a
// viewer is attached to the session.  The viewer sends its own
// AgentUpdate several times a second, flags and rotation and all, and
// the two would take turns deciding which way the avatar faces.
var ErrViewerHolds = errors.New("a viewer is driving this avatar")

// ErrSeated is what a walk or a turn is refused with while the avatar
// is sitting.  Nothing stands it up first: getting up moves an avatar
// by as much as a sit did, and that is for the caller to decide.
var ErrSeated = errors.New("the avatar is sitting; stand it up first")

// ErrNoDirection is Face asked to turn toward the place the avatar is
// standing on.
var ErrNoDirection = errors.New("that is where the avatar is standing, which is no direction")

// refusal says whether this avatar can be walked or turned at all now,
// and why not.
func (a *Agent) refusal() error {
	if a.presenceDeferred() {
		return ErrViewerHolds
	}
	if p, _ := a.Posture(); p.Seated() {
		return ErrSeated
	}
	return nil
}

// Move walks the avatar to a place in this region, and returns how that
// ended.
//
// It blocks for the length of the walk.  Progress, when it is not nil,
// is called about every quarter of a second while the avatar is moving,
// on the goroutine Move runs on, and should not dawdle: the walk is
// steered from the same loop.  The end is what Move returns and is not
// passed to progress as well.
//
// A walk that cannot start is not an error.  A target outside the region
// comes back as OutOfRegion, and a seated avatar, one a viewer is
// driving, or one whose forward control a script has taken as Refused,
// before anything is sent -- they are answers about
// the world rather than failures to reach it.  The error is kept for a
// session that could not send at all.
//
// One walk at a time.  A Move asked for while another is under way takes
// it over: the one that was running ends Cancelled, superseded, and the
// avatar is not stopped in between, because the new walk holds the flag
// from its first update.  Halt ends a walk and stops the avatar; so does
// the context ending, which is what makes a client that went away stop
// the avatar rather than leave it walking.
func (a *Agent) Move(ctx context.Context, r MoveRequest, progress func(MoveProgress)) (MoveProgress, error) {
	if a.Account == nil {
		return MoveProgress{}, errors.New("agent: nothing is logged in to walk")
	}
	asked := r.Within
	r = r.withDefaults()
	start := time.Now()

	report := func(state MoveState, reason string, best time.Time) MoveProgress {
		pos, vel, rot, exact := a.Placement()
		return MoveProgress{
			State: state, Reason: reason,
			Position: pos, Exact: exact, Velocity: vel,
			Yaw:           RotationYaw(rot),
			Remaining:     flat(pos, r.Target),
			SinceProgress: time.Since(best),
			Elapsed:       time.Since(start),
		}
	}

	if !inRegion(r.Target) {
		return report(OutOfRegion, fmt.Sprintf("%.1f, %.1f is not in this region, which runs from 0 to %d on each side",
			r.Target.X, r.Target.Y, RegionWidth), start), nil
	}
	if err := a.refusal(); err != nil {
		return report(Refused, err.Error(), start), nil
	}
	if a.walkHeld() {
		return report(Refused, ErrControlsTaken.Error(), start), nil
	}

	w := &walk{stop: make(chan string, 1)}
	a.walker.mu.Lock()
	if old := a.walker.current; old != nil {
		old.end(CancelSuperseded)
	}
	a.walker.current = w
	err := a.setRunning(ctx, r.Run)
	a.walker.mu.Unlock()
	if err != nil {
		a.letGo(w)
		if ctx.Err() != nil {
			return report(Cancelled, CancelClientGone, start), nil
		}
		return report(Cancelled, CancelSessionEnded, start), err
	}

	handle := a.RegionHandle()
	best, bestAt := flat(a.placementOnly(), r.Target), start

	tick := time.NewTicker(moveTick)
	defer tick.Stop()
	every := time.NewTicker(progressEvery)
	defer every.Stop()
	var timeout <-chan time.Time
	if r.Timeout > 0 {
		t := time.NewTimer(r.Timeout)
		defer t.Stop()
		timeout = t.C
	}

	// end is every way out but a superseding one: it lets go of what
	// is held if this walk still holds it, and says how it ended.
	end := func(state MoveState, reason string) (MoveProgress, error) {
		err := a.letGo(w)
		return report(state, reason, bestAt), err
	}

	// braking is when the flag was let go of near the target, and zero
	// while walking; stops is how many times that has happened.  See
	// stopLead for why the flag is let go of before the target and not
	// at it, and why arriving is decided only once the avatar has
	// stopped.
	var braking time.Time
	stops := 0

	// failed is an update that could not be queued: the caller having
	// gone, which is an ordinary end, or the session, which is not.
	failed := func(err error) (MoveProgress, bool, error) {
		if ctx.Err() != nil {
			p, err := end(Cancelled, CancelClientGone)
			return p, true, err
		}
		p, _ := end(Cancelled, CancelSessionEnded)
		return p, true, err
	}

	// step is one look at where the avatar is and one update steering
	// it, or the end of the walk if that look says it is over.
	step := func(first bool) (MoveProgress, bool, error) {
		switch {
		case a.RegionHandle() != handle:
			p, err := end(Cancelled, CancelLeftRegion)
			return p, true, err
		case a.presenceDeferred():
			p, err := end(Cancelled, CancelViewer)
			return p, true, err
		case a.walkHeld():
			p, err := end(Cancelled, CancelControlsTaken)
			return p, true, err
		}
		if p, _ := a.Posture(); p.Seated() {
			p, err := end(Cancelled, CancelSeated)
			return p, true, err
		}

		pos, vel, _, _ := a.Placement()
		left := flat(pos, r.Target)
		speed := float32(math.Hypot(float64(vel.X), float64(vel.Y)))
		if left < best-r.StallDistance {
			best, bestAt = left, time.Now()
		}

		if !braking.IsZero() {
			if speed > settledSpeed && time.Since(braking) < settleTime {
				return MoveProgress{}, false, nil
			}
			if left <= r.Within {
				p, err := end(Arrived, "")
				return p, true, err
			}
			// Stopped, and not close enough: short of it, or past it.
			// Walk again, from a standstill, which coasts less; but not
			// for ever, since a target this keeps missing is one it
			// will go on missing.
			if stops >= maxStops {
				p, err := end(Cancelled, CancelOvershot)
				return p, true, err
			}
			braking = time.Time{}
			best, bestAt = left, time.Now()
		}

		if left <= r.Within && speed <= settledSpeed {
			p, err := end(Arrived, "")
			return p, true, err
		}
		if left-speed*float32(stopLead.Seconds()) <= aimFor(asked, r.Within, speed) {
			if err := a.brake(ctx, w); err != nil {
				return failed(err)
			}
			braking = time.Now()
			stops++
			return MoveProgress{}, false, nil
		}
		if time.Since(bestAt) > r.StallTime {
			p, err := end(Blocked, "")
			return p, true, err
		}
		if err := a.steer(ctx, w, pos, r.Target, first); err != nil {
			return failed(err)
		}
		return MoveProgress{}, false, nil
	}

	if p, done, err := step(true); done {
		return p, err
	}
	for {
		select {
		case reason := <-w.stop:
			// Whoever stopped this has taken the walk out of current
			// and is answerable for what is held; nothing is sent.
			return report(Cancelled, reason, bestAt), nil
		case <-ctx.Done():
			return end(Cancelled, CancelClientGone)
		case <-a.done:
			return end(Cancelled, CancelSessionEnded)
		case <-timeout:
			return end(Cancelled, CancelTimeout)
		case <-tick.C:
			if p, done, err := step(false); done {
				return p, err
			}
		case <-every.C:
			if progress != nil {
				progress(report(Moving, "", bestAt))
			}
		}
	}
}

// aimFor is how far short of the target to aim to come to rest.
//
// The flag is let go of at most one steering update late, and an update
// is a tenth of a second of walking, so where the avatar stops is
// uncertain by half of that either way and a little more for the coast
// itself.  A walk asked to come within some distance aims that much
// inside it, so as to stop near the edge rather than walk up to the
// target: within a metre and a half of somebody is not into them.  One
// asked for as close as it can get -- a Within of zero -- aims at the
// target itself, and comes to rest past it about as often as short.
func aimFor(asked, within, speed float32) float32 {
	if asked <= 0 {
		return 0
	}
	margin := speed*float32(moveTick.Seconds())/2 + 0.1
	if aim := within - margin; aim > 0 {
		return aim
	}
	return 0
}

// placementOnly is Placement's position and nothing else.
func (a *Agent) placementOnly() msg.Vector3 {
	pos, _, _, _ := a.Placement()
	return pos
}

// steer sends one update walking from pos toward target, if this walk
// still has the say.
//
// The first goes reliably, as a stop does: it is the one that starts the
// walk, and a lost one is a tenth of a second of standing still at best
// and, for a walk taking over from another, a tenth of a second of the
// old walk's heading.  The rest go the way the presence loop's do,
// because the next is a tenth of a second behind.
func (a *Agent) steer(ctx context.Context, w *walk, pos, target msg.Vector3, first bool) error {
	a.walker.mu.Lock()
	defer a.walker.mu.Unlock()
	if a.walker.current != w {
		return nil
	}
	yaw := YawTo(pos, target)
	a.setDrive(ControlAtPos|ControlFastAt, YawRotation(yaw))
	a.faceCamera(pos, yaw)
	u := a.agentUpdate(a.Look())
	if first {
		return a.Send.SendReliable(ctx, u)
	}
	return a.Send.Send(ctx, u)
}

// faceCamera puts the camera on the avatar, looking the way it faces.
//
// On the avatar and not behind it, as setCenter always has: the
// simulator scopes what it describes by the camera and not by where the
// avatar is, and a camera that fell behind a walking avatar would be a
// camera that was a few metres wrong about what is near it.  Pointing
// it the way the avatar faces changes nothing about that and keeps the
// update from being a camera looking east at an avatar walking north.
func (a *Agent) faceCamera(at msg.Vector3, yaw float32) {
	s, c := float32(math.Sin(float64(yaw))), float32(math.Cos(float64(yaw)))
	a.mu.Lock()
	a.look.Center = at
	a.look.At = msg.Vector3{X: c, Y: s}
	a.look.Left = msg.Vector3{X: -s, Y: c}
	a.look.Up = msg.Vector3{Z: 1}
	if a.look.Far <= 0 {
		a.look.Far = DefaultDrawDistance
	}
	a.mu.Unlock()
	a.lookFrom()
}

// letGo ends a walk that ended of its own accord: if it is still the
// one walking, nothing is walking any more, the avatar is stopped if it
// was still being walked, and running is turned off if the walk turned
// it on.  A walk something else already took out of current is that
// thing's to stop, and nothing is sent for it here.
//
// The context is one of its own rather than the caller's, because the
// commonest reason to be here is the caller's context having ended.
func (a *Agent) letGo(w *walk) error {
	a.walker.mu.Lock()
	defer a.walker.mu.Unlock()
	if a.walker.current != w {
		return nil
	}
	a.walker.current = nil
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	if flags, _ := a.drive(); flags != 0 {
		err = a.stopLocked(ctx)
	}
	if e := a.setRunning(ctx, false); err == nil {
		err = e
	}
	return err
}

// brake lets go of the flag near the target, if this walk still has
// the say, and leaves the walk in place: it is not over until the
// avatar has stopped and been found close enough.
func (a *Agent) brake(ctx context.Context, w *walk) error {
	a.walker.mu.Lock()
	defer a.walker.mu.Unlock()
	if a.walker.current != w {
		return nil
	}
	return a.stopLocked(ctx)
}

// stopLocked lets go of the walking flags and stops the avatar, with the
// walker's mutex held.
//
// Two updates, as a viewer sends them: one carrying STOP, and one after
// it carrying nothing held.  Both reliably, since there is no next one
// soon -- the presence loop's is up to a second away, and an avatar that
// missed its stop walks for that second.  The facing is kept: an avatar
// that walked north and stopped is left facing north, rather than turned
// back to the east that an update with no rotation in it means.  The
// STOP is the viewer's habit kept rather than something shown to matter.
// Why: doc/walk.md#the-stop-flag
func (a *Agent) stopLocked(ctx context.Context) error {
	_, body := a.drive()
	a.setDrive(0, body)
	l := a.Look()
	l.ControlFlags |= ControlStop
	if err := a.Send.SendReliable(ctx, a.agentUpdate(l)); err != nil {
		return err
	}
	return a.Send.SendReliable(ctx, a.agentUpdate(a.Look()))
}

// setRunning tells the simulator to run or to walk, if that is not what
// it was last told.  With the walker's mutex held.
func (a *Agent) setRunning(ctx context.Context, run bool) error {
	if a.walker.running == run {
		return nil
	}
	m := &msg.SetAlwaysRun{}
	m.AgentData.AgentID = a.Account.AgentID
	m.AgentData.SessionID = a.Account.SessionID
	m.AgentData.AlwaysRun = run
	if err := a.Send.SendReliable(ctx, m); err != nil {
		return err
	}
	a.walker.running = run
	return nil
}

// Halt ends any walk and stops the avatar, and says whether a walk was
// under way.
//
// The stop is sent whether or not one was: it costs two updates, and an
// avatar that is moving for a reason this session did not see -- a walk
// from before a restart, a flag some client sent through Control -- is
// exactly the one somebody reaching for Halt wants stopped.  It goes
// even while a viewer holds the camera, for the reason Control's flags
// do.
func (a *Agent) Halt(ctx context.Context) (bool, error) {
	if a.Account == nil {
		return false, errors.New("agent: nothing is logged in to halt")
	}
	a.walker.mu.Lock()
	defer a.walker.mu.Unlock()
	w := a.walker.current
	a.walker.current = nil
	if w != nil {
		w.end(CancelHalted)
	}
	err := a.stopLocked(ctx)
	if e := a.setRunning(ctx, false); err == nil {
		err = e
	}
	return w != nil, err
}

// Face turns the avatar toward a place in the region without moving it,
// and returns the facing it was given.
//
// One update, reliably, carrying the rotation; and every update after it
// carries the same rotation, the presence loop's included, until a walk
// or another Face changes it.  A walk under way is ended by it --
// Cancelled, superseded -- and the avatar stopped, since a facing set
// under a walk would be overwritten a tenth of a second later.
//
// A place the avatar is standing on has no direction and is refused, as
// are a seated avatar and one a viewer is driving.
func (a *Agent) Face(ctx context.Context, target msg.Vector3) (float32, error) {
	if a.Account == nil {
		return 0, errors.New("agent: nothing is logged in to turn")
	}
	if err := a.refusal(); err != nil {
		return 0, err
	}
	pos := a.placementOnly()
	if flat(pos, target) < 0.01 {
		return 0, fmt.Errorf("agent: %.2f, %.2f: %w", target.X, target.Y, ErrNoDirection)
	}
	yaw := YawTo(pos, target)
	return yaw, a.FaceYaw(ctx, yaw)
}

// FaceYaw is Face given the facing itself: radians anticlockwise from
// east.
func (a *Agent) FaceYaw(ctx context.Context, yaw float32) error {
	if a.Account == nil {
		return errors.New("agent: nothing is logged in to turn")
	}
	if err := a.refusal(); err != nil {
		return err
	}
	a.walker.mu.Lock()
	defer a.walker.mu.Unlock()
	w := a.walker.current
	a.walker.current = nil
	if w != nil {
		w.end(CancelSuperseded)
	}
	a.setDrive(0, YawRotation(yaw))
	a.faceCamera(a.placementOnly(), yaw)
	if w != nil {
		err := a.stopLocked(ctx)
		if e := a.setRunning(ctx, false); err == nil {
			err = e
		}
		return err
	}
	return a.Send.SendReliable(ctx, a.agentUpdate(a.Look()))
}

// Walking says whether a walk is under way.
func (a *Agent) Walking() bool {
	a.walker.mu.Lock()
	defer a.walker.mu.Unlock()
	return a.walker.current != nil
}
