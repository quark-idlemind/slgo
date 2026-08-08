package sl

// Moving the avatar inside the region it is already in.
//
// A refused teleport produces no error and no reply.  Somewhere the
// parcel will not have you, or a position outside the region, is simply
// ignored -- so a caller that did not check would carry on believing it
// had moved, and every position it computed afterwards would be wrong.
// That is why this waits to be told where the avatar ended up rather
// than returning once the request has gone.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// TestATeleportWaitsToBeToldTheAvatarMoved: the confirmation is the
// whole point.  Nothing answers a TeleportLocationRequest, so arriving
// is read off the position afterwards.
func TestATeleportWaitsToBeToldTheAvatarMoved(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.presence.LookAt = msg.Vector3{}
	f.mu.Unlock()

	to := msg.Vector3{X: 130, Y: 128, Z: 25}
	if err := w.TeleportLocal(context.Background(), to, 5*time.Second); err != nil {
		t.Fatalf("TeleportLocal: %v", err)
	}

	m := onlySent[*msg.TeleportLocationRequest](t, f)
	if m.Info.RegionHandle != 1099511628032 || m.Info.Position != to {
		t.Errorf("teleported to %+v", m.Info)
	}
	// The field is not optional and a zero vector is not a direction,
	// so a session that was not looking anywhere is given a direction.
	if m.Info.LookAt == (msg.Vector3{}) {
		t.Error("the request carries no direction to face")
	}
}

// TestATeleportKeepsFacingTheWayItWas: the avatar's heading is not the
// caller's business, and turning it round on every move would be a
// surprise nobody asked for.
func TestATeleportKeepsFacingTheWayItWas(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.presence.LookAt = msg.Vector3{Y: 1}
	f.mu.Unlock()

	if err := w.TeleportLocal(context.Background(), msg.Vector3{X: 128, Y: 128, Z: 25}, 0); err != nil {
		t.Fatalf("TeleportLocal: %v", err)
	}
	if got := onlySent[*msg.TeleportLocationRequest](t, f).Info.LookAt; got != (msg.Vector3{Y: 1}) {
		t.Errorf("the avatar was turned to face %v", got)
	}
}

// TestATeleportThatWasRefusedSaysSo: a parcel that will not have this
// avatar refuses silently, so the only evidence is that the avatar is
// still where it was.
func TestATeleportThatWasRefusedSaysSo(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.mu.Unlock()

	// The fake never moves, which is exactly what a refusal looks like.
	err := w.TeleportLocal(context.Background(), msg.Vector3{X: 20, Y: 20, Z: 25},
		600*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("TeleportLocal = %v, want a timeout", err)
	}
	if !strings.Contains(err.Error(), "refuses silently") {
		t.Errorf("TeleportLocal = %v, want it to say why there was no reply", err)
	}
}

// TestATeleportNeedsToKnowWhichRegionItIsIn: the request names the
// region by handle, and a session that has not been told one would ask
// to be moved inside region zero.
func TestATeleportNeedsToKnowWhichRegionItIsIn(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	// The fake's presence carries no handle, which is a session that
	// has not heard AgentMovementComplete.
	err := w.TeleportLocal(context.Background(), msg.Vector3{X: 128}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "which region") {
		t.Errorf("TeleportLocal = %v, want it to say it does not know where it is", err)
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("a teleport to nowhere was sent anyway: %s", f.describe())
	}

	f.mu.Lock()
	f.presenceErr = errors.New("nothing knows where we are")
	f.mu.Unlock()
	if err := w.TeleportLocal(context.Background(), msg.Vector3{}, time.Second); err == nil {
		t.Error("TeleportLocal moved an avatar it could not find")
	}
}

// TestATeleportThatCouldNotBeSentIsNotATeleport: the request goes over
// the wire, and a circuit that has gone is not a parcel refusing.
func TestATeleportThatCouldNotBeSentIsNotATeleport(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.mu.Unlock()
	f.FailSends(errors.New("the circuit is gone"))

	err := w.TeleportLocal(context.Background(), msg.Vector3{X: 128}, time.Second)
	if err == nil || errors.Is(err, ErrTimeout) {
		t.Errorf("TeleportLocal = %v, want the send's own failure", err)
	}
}

// TestDistancesAreComparedSquared: comparing a squared distance against
// a plain one is a bug that looks right, so the name says which this is.
func TestDistancesAreComparedSquared(t *testing.T) {
	a := msg.Vector3{X: 1, Y: 2, Z: 3}
	b := msg.Vector3{X: 4, Y: 6, Z: 3}
	if got := dist2(a, b); got != 25 {
		t.Errorf("dist2 = %v, want 25, which is five squared", got)
	}
	if got := dist2(a, a); got != 0 {
		t.Errorf("dist2 of a point with itself = %v", got)
	}
}
