package main

// walk, face and halt, over a grid that is not there.
//
// The walk is the daemon's, so the fake here answers one the way the
// daemon's end of it would -- reports while it goes and one end -- and
// what is checked is what the shell makes of those: the line it prints,
// which ends count as the command failing, and where it sent the avatar
// when it was given a name rather than a place.

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

func (f *fakeGrid) Move(ctx context.Context, r sl.MoveRequest, progress func(sl.MoveProgress)) (sl.MoveProgress, error) {
	f.mu.Lock()
	move := f.move
	f.mu.Unlock()
	return move(r, progress), nil
}

func (f *fakeGrid) Face(ctx context.Context, target msg.Vector3) (float32, error) {
	f.mu.Lock()
	at := f.presence.Position
	f.mu.Unlock()
	yaw := float32(math.Atan2(float64(target.Y-at.Y), float64(target.X-at.X)))
	return yaw, f.FaceYaw(ctx, yaw)
}

func (f *fakeGrid) FaceYaw(ctx context.Context, yaw float32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faced = append(f.faced, yaw)
	return nil
}

func (f *fakeGrid) Halt(ctx context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.halts++
	return f.halts == 1, nil
}

// walkingShell is a shell whose walks arrive, having reported once on
// the way, and which remembers what it was asked.
func walkingShell(t *testing.T) (*testShell, *sl.MoveRequest) {
	t.Helper()
	x := newTestShell(t)
	asked := &sl.MoveRequest{}
	x.grid.move = func(r sl.MoveRequest, progress func(sl.MoveProgress)) sl.MoveProgress {
		*asked = r
		if progress != nil {
			progress(sl.MoveProgress{State: sl.Moving, Position: msg.Vector3{X: 130, Y: 128, Z: 25},
				Velocity: msg.Vector3{X: 3.2}, Remaining: 2, Elapsed: 250 * time.Millisecond})
		}
		return sl.MoveProgress{State: sl.Arrived, Position: r.Target, Remaining: 0.02,
			Elapsed: 1200 * time.Millisecond}
	}
	return x, asked
}

func TestAWalkSaysWhereItCameToRest(t *testing.T) {
	x, asked := walkingShell(t)

	got := x.do(t, "walk 132 128")
	want := "arrived at 132.00, 128.00, 0.00, 0.02 m from 132.00, 128.00, in 1.2s\n"
	if got != want {
		t.Errorf("walk printed %q, want %q", got, want)
	}
	if asked.Target != (msg.Vector3{X: 132, Y: 128}) || asked.Within != 0 || asked.Run {
		t.Errorf("the walk asked for %+v", asked)
	}

	got = x.do(t, "walk -p --run -w 2 -t 5s 132 128 30")
	if !strings.HasPrefix(got, "  0.25s  130.00, 128.00, 25.00  3.20 m/s  facing 0° E  2.00 m to go\n") {
		t.Errorf("a walk with --progress printed %q", got)
	}
	if !asked.Run || asked.Within != 2 || asked.Timeout != 5*time.Second || asked.Target.Z != 30 {
		t.Errorf("the walk asked for %+v", asked)
	}
}

// TestAWalkToAnObjectStopsShortOfIt: half a metre from something's
// middle is walking into it.
func TestAWalkToAnObjectStopsShortOfIt(t *testing.T) {
	x, asked := walkingShell(t)
	box := msg.Vector3{X: 135, Y: 72, Z: 2001}
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testLamp, Local: 1, Name: "box"}, PCode: 9, Position: box},
	}
	got := x.do(t, "walk box")
	if !strings.Contains(got, "from box,") {
		t.Errorf("walk box printed %q", got)
	}
	if asked.Target != box || asked.Within != nameWithin {
		t.Errorf("walking to the box asked for %+v", asked)
	}
	if got := x.do(t, "walk nothing-called-this"); !strings.Contains(got, `nobody and nothing here is called "nothing-called-this"`) {
		t.Errorf("walking to nothing printed %q", got)
	}
}

// TestAWalkThatDidNotArriveIsTheCommandFailing: a script running walks
// has to be able to tell.
func TestAWalkThatDidNotArriveIsTheCommandFailing(t *testing.T) {
	x := newTestShell(t)
	for _, c := range []struct {
		end  sl.MoveProgress
		want string
	}{
		{sl.MoveProgress{State: sl.Blocked, Position: msg.Vector3{X: 1, Y: 2, Z: 3}, Remaining: 4,
			SinceProgress: 2 * time.Second},
			"slsh: walk: blocked at 1.00, 2.00, 3.00, 4.00 m short of 5.00, 2.00: no closer for 2s\n"},
		{sl.MoveProgress{State: sl.Cancelled, Reason: sl.CancelTimeout, Position: msg.Vector3{X: 1, Y: 2, Z: 3},
			Remaining: 4, Elapsed: 500 * time.Millisecond},
			"slsh: walk: cancelled (timeout) at 1.00, 2.00, 3.00, 4.00 m from 5.00, 2.00, after 500ms\n"},
		{sl.MoveProgress{State: sl.OutOfRegion, Reason: "5.0, 2.0 is somewhere else"},
			"slsh: walk: 5.0, 2.0 is somewhere else\n"},
	} {
		end := c.end
		x.grid.move = func(sl.MoveRequest, func(sl.MoveProgress)) sl.MoveProgress { return end }
		if got := x.do(t, "walk 5 2"); got != c.want {
			t.Errorf("a walk ending %v printed %q, want %q", end.State, got, c.want)
		}
	}
}

func TestFaceSaysTheHeadingItGave(t *testing.T) {
	x := newTestShell(t)
	if got := x.do(t, "face --yaw 90"); got != "facing 90° N\n" {
		t.Errorf("face --yaw 90 printed %q", got)
	}
	if got := x.do(t, "face -y -90"); got != "facing 270° S\n" {
		t.Errorf("face -y -90 printed %q", got)
	}
	// The fake stands the avatar at 128, 128.
	if got := x.do(t, "face 100 128"); got != "facing 180° W\n" {
		t.Errorf("face 100 128 printed %q", got)
	}
	if got := x.do(t, "face"); !strings.Contains(got, "usage") && !strings.Contains(got, "Usage") {
		t.Errorf("face with nothing printed %q", got)
	}
	if n := len(x.grid.faced); n != 3 {
		t.Errorf("%d turns were asked for, want 3", n)
	}
}

func TestHaltSaysWhetherAnythingWasWalking(t *testing.T) {
	x := newTestShell(t)
	if got := x.do(t, "halt"); got != "halted a walk\n" {
		t.Errorf("the first halt printed %q", got)
	}
	if got := x.do(t, "halt"); got != "nothing was walking; sent a stop anyway\n" {
		t.Errorf("the second halt printed %q", got)
	}
}
