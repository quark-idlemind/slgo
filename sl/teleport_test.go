package sl

// Moving the avatar, inside the region and out of it.
//
// Both halves are about the same thing: nothing that comes back from a
// teleport says the avatar is anywhere.  Inside the region a refusal
// produces no error and no reply at all -- somewhere the parcel will not
// have you, or a position outside the region, is simply ignored -- so
// arrival is read off the position afterwards.  Out of it the grid does
// answer, but a TeleportFinish says the simulator being left has let go,
// which is the middle of a teleport; the end of one is this session
// answering with the region the finish named.
//
// The bodies below are the ones stage 0 captured on Agni, kept as they
// arrived.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
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

// The two bodies below are what Agni sent, on 2026-08-16, kept as they
// arrived so that what this package reads is checked against a
// measurement rather than against something written to match it.  Only
// the agent uuid is made up.
//
// agniFinish took this avatar from Pelmar Reach to Sandbox Goguen, grid
// square (995, 997).  Its handle is eight binary bytes big endian, which
// is the one field of it this package reads at all: the address and the
// seed are for whoever moves the circuit, and that is the daemon.
const agniFinish = `<llsd><map><key>Info</key><array><map>` +
	`<key>AgentID</key><uuid>45d57e57-7e57-c0de-d221-6ffd8a188ce4</uuid>` +
	`<key>LocationID</key><binary>AAAAAw==</binary>` +
	`<key>RegionHandle</key><binary>AAPjAAAD5QA=</binary>` +
	`<key>SeedCapability</key>` +
	`<string>https://simhost-0aaaaaaaaaaaaaaa2.agni.secondlife.io:12043/cap/` +
	`fd277e57-7e57-c0de-d6ee-dd800a9b6922</string>` +
	`<key>SimAccess</key><integer>13</integer>` +
	`<key>SimIP</key><binary>ywBxCw==</binary>` +
	`<key>SimPort</key><integer>13032</integer>` +
	`<key>TeleportFlags</key><binary>AAAAEA==</binary>` +
	`</map></array></map></llsd>`

// goguen is the handle in the body above.
const goguen = 1094014069892352

// agniRefused is a region that would not have this avatar: a key in one
// place and a sentence for a person in the other.
const agniRefused = `<llsd><map>` +
	`<key>AlertInfo</key><array><map>` +
	`<key>ExtraParams</key><string></string>` +
	`<key>Message</key><string>MustHaveVIPStatus</string></map></array>` +
	`<key>Info</key><array><map>` +
	`<key>AgentID</key><uuid>45d57e57-7e57-c0de-d221-6ffd8a188ce4</uuid>` +
	`<key>Reason</key><string>You must be a premium or vip subscriber ` +
	`to enter this region.</string></map></array></map></llsd>`

// agniNoSuchRegion is a handle that is no region, where both places say
// the same word.
const agniNoSuchRegion = `<llsd><map>` +
	`<key>AlertInfo</key><array><map>` +
	`<key>ExtraParams</key><string></string>` +
	`<key>Message</key><string>no_host</string></map></array>` +
	`<key>Info</key><array><map>` +
	`<key>AgentID</key><uuid>45d57e57-7e57-c0de-d221-6ffd8a188ce4</uuid>` +
	`<key>Reason</key><string>no_host</string></map></array></map></llsd>`

// TestATeleportWaitsForTheSessionToBeInTheNewRegion: the finish says the
// simulator being left has let go, which is the middle of a teleport.
// The end of one is this session answering with the region the finish
// named.
func TestATeleportWaitsForTheSessionToBeInTheNewRegion(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLocationRequest); !ok {
			return
		}
		f.RelayEvent(t, "TeleportFinish", agniFinish)
		f.mu.Lock()
		f.presence.RegionHandle = goguen
		f.presence.Region = "Sandbox Goguen"
		f.mu.Unlock()
	}
	f.mu.Unlock()

	to := msg.Vector3{X: 128, Y: 128, Z: 30}
	if err := w.Teleport(context.Background(), goguen, to, 5*time.Second); err != nil {
		t.Fatalf("Teleport: %v", err)
	}
	m := onlySent[*msg.TeleportLocationRequest](t, f)
	if m.Info.RegionHandle != goguen || m.Info.Position != to {
		t.Errorf("asked to be taken to %+v", m.Info)
	}
	if m.Info.LookAt == (msg.Vector3{}) {
		t.Error("the request carries no direction to face")
	}
}

// TestATeleportTheGridRefusedSaysBothOfTheThingsItSaid: a refusal has
// two voices -- a key a program could act on and a sentence for a person
// -- and they are not always the same string.  Reporting one of them
// would be dropping either the reason or the machine-readable name of
// it, and there is no way to tell which from the other end.
func TestATeleportTheGridRefusedSaysBothOfTheThingsItSaid(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLocationRequest); ok {
			f.RelayEvent(t, "TeleportFailed", agniRefused)
		}
	}
	f.mu.Unlock()

	err := w.Teleport(context.Background(), goguen, msg.Vector3{X: 128}, 5*time.Second)
	if !errors.Is(err, ErrTeleportRefused) {
		t.Fatalf("Teleport = %v, want a refusal", err)
	}
	for _, want := range []string{"MustHaveVIPStatus", "premium or vip subscriber"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not carry %q", err, want)
		}
	}
	// And the grid square asked about, since a program teleporting on a
	// handle has nothing else to recognise it by.
	if !strings.Contains(err.Error(), "(995, 997)") {
		t.Errorf("the refusal %q does not say where it was about", err)
	}
}

// TestARefusalHandsAProgramTheGridsKey: the key is the half of a
// refusal a program acts on, so it has to reach the caller as a value
// and not only inside the sentence.  Folded into the sentence, the only
// way to ask which refusal it was is to search the words for the key,
// which every caller has to know to do and which matches a sentence
// that merely mentions it.
func TestARefusalHandsAProgramTheGridsKey(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLocationRequest); ok {
			f.RelayEvent(t, "TeleportFailed", agniRefused)
		}
	}
	f.mu.Unlock()

	err := w.Teleport(context.Background(), goguen, msg.Vector3{X: 128}, 5*time.Second)
	var r *TeleportRefusal
	if !errors.As(err, &r) {
		t.Fatalf("Teleport = %v, want a *TeleportRefusal", err)
	}
	if r.Key != agent.KeyMustHaveVIPStatus {
		t.Errorf("key = %q, want %q", r.Key, agent.KeyMustHaveVIPStatus)
	}
	if want := "You must be a premium or vip subscriber to enter this region."; r.Reason != want {
		t.Errorf("reason = %q, want %q", r.Reason, want)
	}
	if !strings.Contains(r.What, "(995, 997)") {
		t.Errorf("what = %q, want the grid square asked about", r.What)
	}
}

// TestARefusalThatSaysOneThingTwiceSaysItOnce: a handle that is no
// region answers "no_host" in both places, and an error that printed it
// twice would read as two separate failures.
func TestARefusalThatSaysOneThingTwiceSaysItOnce(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLocationRequest); ok {
			f.RelayEvent(t, "TeleportFailed", agniNoSuchRegion)
		}
	}
	f.mu.Unlock()

	err := w.Teleport(context.Background(), goguen, msg.Vector3{X: 128}, 5*time.Second)
	if !errors.Is(err, ErrTeleportRefused) {
		t.Fatalf("Teleport = %v, want a refusal", err)
	}
	if n := strings.Count(err.Error(), "no_host"); n != 1 {
		t.Errorf("the refusal says no_host %d times: %v", n, err)
	}
}

// TestATeleportAnsweredWithSilenceSaysWhatSilenceMeans: the third
// failure, and the one with nothing to look at.  Once this avatar has
// been handed off, the region it left answers no further teleport
// request at all -- measured, and not a rate limit that clears -- so a
// timeout here is the ordinary shape of asking twice and not a network
// fault to retry.
func TestATeleportAnsweredWithSilenceSaysWhatSilenceMeans(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.mu.Unlock()

	err := w.Teleport(context.Background(), goguen, msg.Vector3{X: 128}, 300*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Teleport = %v, want a timeout", err)
	}
	if !strings.Contains(err.Error(), "already under way") {
		t.Errorf("the timeout %q does not say what silence means", err)
	}
	if errors.Is(err, ErrTeleportLost) || errors.Is(err, ErrTeleportRefused) {
		t.Errorf("silence was reported as something the grid said: %v", err)
	}
}

// TestATeleportTheDaemonDidNotFollowIsNotArrival: the finish is relayed
// to clients whether or not the daemon managed to follow it, so a
// session that took the event for arrival would report a teleport that
// left the avatar unreachable.
func TestATeleportTheDaemonDidNotFollowIsNotArrival(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLocationRequest); ok {
			// The grid let go and the session stayed where it was,
			// which is what a move that failed looks like from here.
			f.RelayEvent(t, "TeleportFinish", agniFinish)
		}
	}
	f.mu.Unlock()

	err := w.Teleport(context.Background(), goguen, msg.Vector3{X: 128}, 700*time.Millisecond)
	if !errors.Is(err, ErrTeleportLost) {
		t.Fatalf("Teleport = %v, want the one that says the daemon did not follow", err)
	}
	if !strings.Contains(err.Error(), "(995, 997)") {
		t.Errorf("%q does not say where the avatar was handed to", err)
	}
}

// TestATeleportToTheRegionAlreadyInIsALocalOne: there is one region and
// one simulator either way, so dialling a second circuit to the
// simulator on the other end of this one would be a relog to where we
// are standing.
func TestATeleportToTheRegionAlreadyInIsALocalOne(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	const here = 1099511628032
	f.mu.Lock()
	f.presence.RegionHandle = here
	f.mu.Unlock()

	// The fake never answers with a finish, so this can only return by
	// having gone the local way: the position it is asked for is the one
	// the fake already reports.
	if err := w.Teleport(context.Background(), here, msg.Vector3{X: 128, Y: 128, Z: 25},
		5*time.Second); err != nil {
		t.Fatalf("Teleport: %v", err)
	}
	if m := onlySent[*msg.TeleportLocationRequest](t, f); m.Info.RegionHandle != here {
		t.Errorf("asked to be taken to region %d", m.Info.RegionHandle)
	}
}

// TestATeleportNeedsAHandleToGoTo: nothing local turns a name into one,
// and region zero is a real square of the grid.
func TestATeleportNeedsAHandleToGoTo(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	if err := w.Teleport(context.Background(), 0, msg.Vector3{X: 128}, time.Second); err == nil {
		t.Error("Teleport accepted a region handle of zero")
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("a teleport to nowhere was sent anyway: %s", f.describe())
	}
}
