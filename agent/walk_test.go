package agent

// Walking, against an avatar that is not there.
//
// A walk is a loop that reads where the avatar is and sends updates
// saying which way to face and whether to keep going, so what it needs
// from a simulator is an avatar that moves when the flag is held and
// reports where it has got to.  legs, below, is that: it reads what the
// session is holding down, walks the avatar's entry in the object store
// the way the simulator's terse updates would, and does it a quarter of
// a second late, because a real avatar does -- measured on Agni, one
// walking at 3.2 metres a second went on for about 0.28 seconds after
// being let go of (see stopLead).  A fake that stopped dead would test a
// walk that never overshoots, which is not the walk that has to work.
//
// What it does not model is everything else about a simulator: there is
// no ground, no turning time, and no terse updates going quiet.

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

const legsLocal = 7777

// legs is a fake avatar body.
type legs struct {
	a     *Agent
	speed float32
	lag   time.Duration

	// wall, when it is not zero, is an x the avatar cannot walk past.
	wall float32

	mu   sync.Mutex
	pos  msg.Vector3
	past []heldAt
	stop chan struct{}
	done chan struct{}
}

type heldAt struct {
	at    time.Time
	flags uint32
	body  msg.Quaternion
}

// standAt puts this session's avatar in the store, described as a
// region would describe it.
func standAt(a *Agent, pos msg.Vector3, parent uint32) {
	a.Objects().update(&msg.ObjectUpdate_ObjectData{
		ID: legsLocal, FullID: a.Account.AgentID, PCode: pcodeAvatar, ParentID: parent,
		ObjectData: placement(pos, msg.Quaternion{}),
	}, msg.Vector3{}, 0)
}

// walkable starts a fake avatar at pos, answering the session's flags.
func walkable(t *testing.T, a *Agent, pos msg.Vector3) *legs {
	t.Helper()
	standAt(a, pos, 0)
	l := &legs{a: a, speed: 3.2, lag: 250 * time.Millisecond, pos: pos,
		stop: make(chan struct{}), done: make(chan struct{})}
	go l.run()
	t.Cleanup(func() { close(l.stop); <-l.done })
	return l
}

func (l *legs) run() {
	defer close(l.done)
	const step = 20 * time.Millisecond
	t := time.NewTicker(step)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case now := <-t.C:
			flags, body := l.a.drive()
			l.mu.Lock()
			l.past = append(l.past, heldAt{now, flags, body})
			// What the simulator is acting on is what it was sent a
			// lag ago.
			var acting heldAt
			for _, h := range l.past {
				if now.Sub(h.at) >= l.lag {
					acting = h
				}
			}
			var vel msg.Vector3
			if acting.flags&ControlAtPos != 0 {
				yaw := float64(RotationYaw(acting.body))
				vel = msg.Vector3{X: l.speed * float32(math.Cos(yaw)), Y: l.speed * float32(math.Sin(yaw))}
				dt := float32(step.Seconds())
				next := msg.Vector3{X: l.pos.X + vel.X*dt, Y: l.pos.Y + vel.Y*dt, Z: l.pos.Z}
				if l.wall != 0 && next.X > l.wall {
					next.X, vel = l.wall, msg.Vector3{}
				}
				l.pos = next
			}
			pos := l.pos
			l.mu.Unlock()
			l.a.Objects().moved(&msg.Terse{LocalID: legsLocal, Avatar: true,
				Position: pos, Velocity: vel, Rotation: acting.body}, nil)
		}
	}
}

func (l *legs) at() msg.Vector3 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pos
}

// updates is every AgentUpdate the session has sent so far, once the
// sender has written out what it was handed: Send queues, and a test
// that read straight after a walk ended would miss its stop.
func updates(t *testing.T, w *sentPackets) []*msg.AgentUpdate {
	t.Helper()
	for n := -1; n != w.count(); {
		n = w.count()
		time.Sleep(30 * time.Millisecond)
	}
	var out []*msg.AgentUpdate
	for _, m := range w.messages(t) {
		if u, ok := m.(*msg.AgentUpdate); ok {
			out = append(out, u)
		}
	}
	return out
}

func near(got, want, tol float32) bool { return got > want-tol && got < want+tol }

// TestYawIsAnticlockwiseFromEast: the three conversions agree with each
// other and with the convention the man pages state, at every quarter
// and in between, including the headings whose quaternion has to be
// turned round to travel.
func TestYawIsAnticlockwiseFromEast(t *testing.T) {
	t.Parallel()
	from := msg.Vector3{X: 100, Y: 100}
	for _, c := range []struct {
		to  msg.Vector3
		yaw float64
	}{
		{msg.Vector3{X: 110, Y: 100}, 0},
		{msg.Vector3{X: 100, Y: 110}, 90},
		{msg.Vector3{X: 90, Y: 100}, 180},
		{msg.Vector3{X: 100, Y: 90}, -90},
		{msg.Vector3{X: 110, Y: 110}, 45},
		{msg.Vector3{X: 90, Y: 90}, -135},
	} {
		yaw := YawTo(from, c.to)
		if deg := float64(yaw) * 180 / math.Pi; math.Abs(deg-c.yaw) > 0.01 {
			t.Errorf("yaw toward %v = %.2f degrees, want %v", c.to, deg, c.yaw)
		}
		back := RotationYaw(YawRotation(yaw))
		// Plus and minus a half turn are the same heading.
		if d := math.Remainder(float64(back-yaw), 2*math.Pi); math.Abs(d) > 0.001 {
			t.Errorf("yaw %.3f went through a rotation and came back as %.3f", yaw, back)
		}
		if q := YawRotation(yaw); q.W() < 0 || !near(q.X, 0, 1e-6) || !near(q.Y, 0, 1e-6) {
			t.Errorf("the rotation for %.3f is %+v; a heading turns about the vertical and nothing else", yaw, q)
		}
	}
}

// TestAPositionIsCarriedForwardByItsVelocity: the simulator sends a
// terse update when the motion changes and not for every step of it, so
// a walk steering by the last one alone steers by where the avatar was.
func TestAPositionIsCarriedForwardByItsVelocity(t *testing.T) {
	t.Parallel()
	then := time.Now()
	pos, vel := msg.Vector3{X: 10, Y: 20, Z: 30}, msg.Vector3{X: 2, Y: -1}

	if got := reckon(pos, vel, then, then.Add(500*time.Millisecond)); !near(got.X, 11, 1e-4) || !near(got.Y, 19.5, 1e-4) || got.Z != 30 {
		t.Errorf("half a second on = %v, want {11 19.5 30}", got)
	}
	// Not for ever: something that has said nothing for a long time is
	// as likely to have stopped.
	if got := reckon(pos, vel, then, then.Add(time.Hour)); !near(got.X, 10+2*float32(deadReckoning.Seconds()), 1e-3) {
		t.Errorf("an hour on = %v, want no further than %v would take it", got, deadReckoning)
	}
	if got := reckon(pos, msg.Vector3{}, then, then.Add(time.Second)); got != pos {
		t.Errorf("standing still moved to %v", got)
	}
	if got := reckon(pos, vel, time.Time{}, then); got != pos {
		t.Errorf("a velocity nobody said when was carried forward to %v", got)
	}
}

// TestAWalkArrivesAndLetsGo is the ordinary walk: the flags held and the
// body turned the right way while it goes, reports while it goes, and at
// the end the flags let go -- STOP, and then nothing -- with the facing
// kept.
func TestAWalkArrivesAndLetsGo(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	l := walkable(t, a, msg.Vector3{X: 100, Y: 100, Z: 25})

	var reports []MoveProgress
	start := time.Now()
	end, err := a.Move(context.Background(), MoveRequest{Target: msg.Vector3{X: 104, Y: 100}},
		func(p MoveProgress) { reports = append(reports, p) })
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if end.State != Arrived || end.Remaining > MinWithin {
		t.Fatalf("the walk ended %v, %.2f m to go", end.State, end.Remaining)
	}
	if got := flat(l.at(), msg.Vector3{X: 104, Y: 100}); got > MinWithin {
		t.Errorf("the avatar is %.2f m from the target", got)
	}

	// About four a second, every one of them a walk still going.
	want := int(took / progressEvery)
	if len(reports) < want-1 || len(reports) > want+1 {
		t.Errorf("%d reports over %v, want about %d", len(reports), took, want)
	}
	for _, p := range reports {
		if p.State != Moving || !p.Exact {
			t.Errorf("a report said %v (exact %v)", p.State, p.Exact)
		}
	}

	us := updates(t, w)
	if len(us) < 5 {
		t.Fatalf("only %d updates were sent for a walk of four metres", len(us))
	}
	east := YawRotation(0)
	for _, u := range us[:len(us)-2] {
		if u.AgentData.ControlFlags != ControlAtPos|ControlFastAt {
			t.Errorf("an update during the walk held %#x", u.AgentData.ControlFlags)
		}
		if u.AgentData.BodyRotation != east || u.AgentData.HeadRotation != east {
			t.Errorf("an update during a walk east faced %+v", u.AgentData.BodyRotation)
		}
	}
	last := us[len(us)-2:]
	if last[0].AgentData.ControlFlags != ControlStop || last[1].AgentData.ControlFlags != 0 {
		t.Errorf("the walk ended with %#x then %#x, want STOP and then nothing",
			last[0].AgentData.ControlFlags, last[1].AgentData.ControlFlags)
	}
	if last[1].AgentData.BodyRotation != east {
		t.Errorf("letting go turned the avatar to %+v", last[1].AgentData.BodyRotation)
	}

	// And the presence loop's update, built now, is not a walk.
	if flags, _ := a.drive(); flags != 0 || a.Walking() {
		t.Errorf("after arriving the session still holds %#x (walking %v)", flags, a.Walking())
	}
	if u := a.agentUpdate(a.Look()); u.AgentData.BodyRotation != east {
		t.Errorf("the next presence update faces %+v, want the walk's heading kept", u.AgentData.BodyRotation)
	}
}

// TestEveryUpdateCarriesTheWalkWhileItGoes: the presence loop's own
// update, a second apart, is built from the same state -- one that
// carried no flag would let go in the middle of a walk.
func TestEveryUpdateCarriesTheWalkWhileItGoes(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	walkable(t, a, msg.Vector3{X: 100, Y: 100, Z: 25})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan MoveProgress, 1)
	go func() {
		p, _ := a.Move(ctx, MoveRequest{Target: msg.Vector3{X: 100, Y: 120}}, nil)
		done <- p
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !a.Walking() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)

	u := a.agentUpdate(a.Look())
	if u.AgentData.ControlFlags&ControlAtPos == 0 {
		t.Errorf("a presence update in the middle of a walk held %#x", u.AgentData.ControlFlags)
	}
	if got := RotationYaw(u.AgentData.BodyRotation); !near(got, math.Pi/2, 0.05) {
		t.Errorf("a presence update in the middle of a walk north faced %.2f", got)
	}
	if at := a.Look().At; !near(at.Y, 1, 0.05) {
		t.Errorf("the camera looks along %v while walking north", at)
	}
	cancel()
	<-done
}

// TestAWalkIntoAWallEndsBlocked: nothing here sees the wall, and nothing
// has to.  The avatar stops getting closer, and that is the answer.
func TestAWalkIntoAWallEndsBlocked(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	l := walkable(t, a, msg.Vector3{X: 100, Y: 100, Z: 25})
	l.wall = 101.5

	end, err := a.Move(context.Background(), MoveRequest{
		Target: msg.Vector3{X: 106, Y: 100}, StallTime: 400 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if end.State != Blocked {
		t.Fatalf("walking into a wall ended %v %q", end.State, end.Reason)
	}
	if !near(end.Remaining, 4.5, 0.1) || end.SinceProgress < 400*time.Millisecond {
		t.Errorf("blocked with %.2f m to go and %v since progress", end.Remaining, end.SinceProgress)
	}
	us := updates(t, w)
	if us[len(us)-2].AgentData.ControlFlags != ControlStop {
		t.Error("a blocked walk did not let go of the flag")
	}
}

// TestAWalkOutsideTheRegionIsRefusedBeforeAnythingMoves: a walk never
// crosses a border, and saying so afterwards would be too late.
func TestAWalkOutsideTheRegionIsRefusedBeforeAnythingMoves(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	walkable(t, a, msg.Vector3{X: 100, Y: 100, Z: 25})

	for _, target := range []msg.Vector3{{X: 300, Y: 10}, {X: -1, Y: 10}, {X: 10, Y: 256}} {
		end, err := a.Move(context.Background(), MoveRequest{Target: target}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if end.State != OutOfRegion || !strings.Contains(end.Reason, "not in this region") {
			t.Errorf("a walk to %v ended %v %q", target, end.State, end.Reason)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := w.count(); n != 0 {
		t.Errorf("%d datagrams went out for walks that were refused", n)
	}
}

// TestASeatedAvatarIsNotStoodUpToWalk: getting up moves an avatar by as
// much as the sit did, so it is the caller's to decide, and a walk is
// refused rather than doing it.  A turn likewise.
func TestASeatedAvatarIsNotStoodUpToWalk(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	standAt(a, msg.Vector3{X: 0.5, Z: 0.7}, 4242)

	end, err := a.Move(context.Background(), MoveRequest{Target: msg.Vector3{X: 10, Y: 10}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if end.State != Refused || end.Reason != ErrSeated.Error() {
		t.Errorf("a walk while seated ended %v %q", end.State, end.Reason)
	}
	if _, err := a.Face(context.Background(), msg.Vector3{X: 10, Y: 10}); err != ErrSeated {
		t.Errorf("a turn while seated gave %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := w.count(); n != 0 {
		t.Errorf("%d datagrams went out for a seated avatar", n)
	}
}

// TestAViewerDrivingTheAvatarIsNotArguedWith: a viewer sends its own
// flags and facing several times a second, and a walk would take turns
// with it.
func TestAViewerDrivingTheAvatarIsNotArguedWith(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	walkable(t, a, msg.Vector3{X: 100, Y: 100, Z: 25})
	a.DeferPresence(time.Now().Add(time.Minute))

	end, err := a.Move(context.Background(), MoveRequest{Target: msg.Vector3{X: 104, Y: 100}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if end.State != Refused || end.Reason != ErrViewerHolds.Error() {
		t.Errorf("a walk under a viewer ended %v %q", end.State, end.Reason)
	}
}

// TestASecondWalkTakesOverWithoutStopping: the first ends superseded,
// and nothing between the two lets go of the flag -- a client steering
// round something changes its mind by asking again, and an avatar that
// stopped every time it did would stutter round every corner.
func TestASecondWalkTakesOverWithoutStopping(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	walkable(t, a, msg.Vector3{X: 100, Y: 100, Z: 25})

	first := make(chan MoveProgress, 1)
	go func() {
		p, _ := a.Move(context.Background(), MoveRequest{Target: msg.Vector3{X: 120, Y: 100}}, nil)
		first <- p
	}()
	time.Sleep(400 * time.Millisecond)
	before := len(updates(t, w))

	second, err := a.Move(context.Background(), MoveRequest{Target: msg.Vector3{X: 101, Y: 103}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := <-first
	if p.State != Cancelled || p.Reason != CancelSuperseded {
		t.Errorf("the first walk ended %v %q", p.State, p.Reason)
	}
	if second.State != Arrived {
		t.Errorf("the second walk ended %v %q, %.2f m to go", second.State, second.Reason, second.Remaining)
	}

	us := updates(t, w)
	for i, u := range us[before : len(us)-2] {
		if u.AgentData.ControlFlags&ControlAtPos == 0 && i < 3 {
			t.Errorf("update %d after the second walk began held %#x; the takeover let go",
				i, u.AgentData.ControlFlags)
		}
	}
	stops := 0
	for _, u := range us[:before+3] {
		if u.AgentData.ControlFlags&ControlStop != 0 {
			stops++
		}
	}
	if stops != 0 {
		t.Errorf("%d stops went out around the takeover", stops)
	}
}

// TestHaltEndsAWalkAndStopsTheAvatar, and says it did.
func TestHaltEndsAWalkAndStopsTheAvatar(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	walkable(t, a, msg.Vector3{X: 100, Y: 100, Z: 25})

	ended := make(chan MoveProgress, 1)
	go func() {
		p, _ := a.Move(context.Background(), MoveRequest{Target: msg.Vector3{X: 120, Y: 100}}, nil)
		ended <- p
	}()
	time.Sleep(300 * time.Millisecond)

	walking, err := a.Halt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !walking {
		t.Error("Halt said nothing was walking")
	}
	p := <-ended
	if p.State != Cancelled || p.Reason != CancelHalted {
		t.Errorf("the walk ended %v %q", p.State, p.Reason)
	}
	us := updates(t, w)
	if us[len(us)-2].AgentData.ControlFlags != ControlStop || us[len(us)-1].AgentData.ControlFlags != 0 {
		t.Error("Halt did not send STOP and then nothing")
	}

	// With nothing walking it still stops, and says there was nothing.
	n := len(us)
	walking, err = a.Halt(context.Background())
	if err != nil || walking {
		t.Errorf("a second Halt said %v, %v", walking, err)
	}
	w.waitFor(t, n+2)
}

// TestAClientGoingAwayStopsTheAvatar: the context ending is how a client
// that exited, crashed or lost its link is heard, and an avatar left
// holding the flag walks until it meets something.
func TestAClientGoingAwayStopsTheAvatar(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	walkable(t, a, msg.Vector3{X: 100, Y: 100, Z: 25})

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	p, err := a.Move(ctx, MoveRequest{Target: msg.Vector3{X: 120, Y: 100}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != Cancelled || p.Reason != CancelClientGone {
		t.Errorf("the walk ended %v %q", p.State, p.Reason)
	}
	if flags, _ := a.drive(); flags != 0 || a.Walking() {
		t.Errorf("the session still holds %#x", flags)
	}
	us := updates(t, w)
	if us[len(us)-2].AgentData.ControlFlags != ControlStop {
		t.Error("a client going away did not stop the avatar")
	}
}

// TestAWalkThatRunsOutOfTimeSaysSo, as a cancellation of its own rather
// than a block: it may have been making perfectly good progress.
func TestAWalkThatRunsOutOfTimeSaysSo(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	walkable(t, a, msg.Vector3{X: 100, Y: 100, Z: 25})

	p, err := a.Move(context.Background(), MoveRequest{
		Target: msg.Vector3{X: 120, Y: 100}, Timeout: 300 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != Cancelled || p.Reason != CancelTimeout {
		t.Errorf("the walk ended %v %q", p.State, p.Reason)
	}
}

// TestRunningIsAMessageAndIsTurnedOffAfterwards: the viewer runs by
// sending SetAlwaysRun, not by holding a flag, and a walk that turned it
// on leaves the avatar walking again for whatever comes next.
func TestRunningIsAMessageAndIsTurnedOffAfterwards(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	walkable(t, a, msg.Vector3{X: 100, Y: 100, Z: 25})

	p, err := a.Move(context.Background(), MoveRequest{Target: msg.Vector3{X: 103, Y: 100}, Run: true}, nil)
	if err != nil || p.State != Arrived {
		t.Fatalf("the run ended %v %q, %v", p.State, p.Reason, err)
	}
	updates(t, w)
	var runs []bool
	for _, m := range w.messages(t) {
		if r, ok := m.(*msg.SetAlwaysRun); ok {
			runs = append(runs, r.AgentData.AlwaysRun)
		}
	}
	if len(runs) != 2 || !runs[0] || runs[1] {
		t.Errorf("SetAlwaysRun went out as %v, want on and then off", runs)
	}
}

// TestFaceTurnsWithoutMovingAndStaysTurned: one update carrying the
// rotation and no flag, and every update after it facing the same way.
func TestFaceTurnsWithoutMovingAndStaysTurned(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	standAt(a, msg.Vector3{X: 100, Y: 100, Z: 25}, 0)

	yaw, err := a.Face(context.Background(), msg.Vector3{X: 100, Y: 90})
	if err != nil {
		t.Fatal(err)
	}
	if !near(yaw, -math.Pi/2, 1e-4) {
		t.Errorf("facing south gave a yaw of %v", yaw)
	}
	w.waitFor(t, 1)
	u := updates(t, w)[0]
	if u.AgentData.ControlFlags != 0 || u.AgentData.BodyRotation != YawRotation(yaw) {
		t.Errorf("the turn went out holding %#x facing %+v", u.AgentData.ControlFlags, u.AgentData.BodyRotation)
	}
	if later := a.agentUpdate(a.Look()); later.AgentData.BodyRotation != YawRotation(yaw) {
		t.Errorf("the next update faces %+v; the turn was forgotten", later.AgentData.BodyRotation)
	}

	if _, err := a.Face(context.Background(), msg.Vector3{X: 100, Y: 100, Z: 40}); err == nil ||
		!strings.Contains(err.Error(), ErrNoDirection.Error()) {
		t.Errorf("facing the spot the avatar stands on gave %v", err)
	}
}

// TestFaceEndsAWalk: a heading set under a walk would be undone by the
// walk's next update, so the walk ends -- superseded -- and the avatar
// stops.
func TestFaceEndsAWalk(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	walkable(t, a, msg.Vector3{X: 100, Y: 100, Z: 25})

	ended := make(chan MoveProgress, 1)
	go func() {
		p, _ := a.Move(context.Background(), MoveRequest{Target: msg.Vector3{X: 120, Y: 100}}, nil)
		ended <- p
	}()
	time.Sleep(300 * time.Millisecond)
	if err := a.FaceYaw(context.Background(), math.Pi); err != nil {
		t.Fatal(err)
	}
	p := <-ended
	if p.State != Cancelled || p.Reason != CancelSuperseded {
		t.Errorf("the walk ended %v %q", p.State, p.Reason)
	}
	us := updates(t, w)
	last := us[len(us)-1]
	if last.AgentData.ControlFlags != 0 || last.AgentData.BodyRotation != YawRotation(math.Pi) {
		t.Errorf("after the turn the avatar holds %#x facing %+v", last.AgentData.ControlFlags, last.AgentData.BodyRotation)
	}
}

// TestAimingShortOfSomebody: a Within asked for is aimed a little inside,
// so as to stop near its edge; none asked for aims at the target.
func TestAimingShortOfSomebody(t *testing.T) {
	t.Parallel()
	if got := aimFor(0, MinWithin, 3.2); got != 0 {
		t.Errorf("as close as it can get aims %.2f short", got)
	}
	if got := aimFor(1.5, 1.5, 3.2); !near(got, 1.24, 0.001) {
		t.Errorf("within a metre and a half aims %.2f short, want 1.24", got)
	}
	if got := aimFor(0.2, MinWithin, 3.2); got < 0 || got > MinWithin {
		t.Errorf("a small within aims %.2f short", got)
	}
}
