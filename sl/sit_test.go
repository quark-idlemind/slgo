package sl

// Sitting down, and getting up again.
//
// Three verbs and four ways each of them can end, and nothing about the
// protocol makes them look alike.  An object sit is a message answered
// by a reparenting or by a sentence, in about a tenth of a second; a
// ground sit is a control flag answered by nothing at all except an
// animation; and a stand is the same flag with two different proofs
// depending on which kind of sit it is ending.  Each of those is a wait
// that can be made to hang, to give up, or to declare success on the
// wrong evidence.
//
// The calls that wait cannot be the goroutine that relays what they are
// waiting for, so they are run aside; see fake_test.go.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

var theCouch = msg.MustParseUUID("43da7e57-7e57-c0de-1beb-09ee90247da5")

// The local ids in play.  couchLocal is the one from the measurement on
// Agni, kept because a number a person can look up in doc/sit.md is
// worth more here than a round one.
const (
	meLocal    = 4001
	couchLocal = 83600601
)

// seatMe relays this avatar's own ObjectUpdate carrying a parent, which
// is the simulator's word that a sit took: parent set for a sit, zero
// for a stand.
func seatMe(t *testing.T, f *fakeBackend, parent uint32) {
	t.Helper()
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{
		FullID: testAgentID, ID: meLocal, ParentID: parent,
	}))
}

// animate relays this avatar's animation list.  The simulator sends the
// whole list every time, so this replaces rather than adds, and an empty
// one is how an animation stops.
func animate(t *testing.T, f *fakeBackend, ids ...msg.UUID) {
	t.Helper()
	m := &msg.AvatarAnimation{}
	m.Sender.ID = testAgentID
	for _, id := range ids {
		m.AnimationList = append(m.AnimationList,
			msg.AvatarAnimation_AnimationList{AnimID: id})
	}
	f.Relay(t, m)
}

// theBench is the object every sit here is aimed at.
func theBench() *Object {
	return &Object{ID: theCouch, Local: couchLocal, Name: "a bench"}
}

// TestASitEndsWhenTheSimulatorReparentsTheAvatar: the reparenting is the
// fact and everything else is decoration.  AvatarSitResponse arrives
// first -- 106 milliseconds against 123, measured -- and is not what
// says the sit took, so it is relayed first here to make sure nothing
// finishes on it.
func TestASitEndsWhenTheSimulatorReparentsTheAvatar(t *testing.T) {
	w, f := newFakeSession(t)

	wait := aside(t, func() (*Seat, error) {
		return w.Sit(context.Background(), theBench(), 5*time.Second)
	})

	sent := waitSent[*msg.AgentRequestSit](t, f)
	if sent.TargetObject.TargetID != theCouch {
		t.Errorf("the sit asked for %s, want %s", sent.TargetObject.TargetID, theCouch)
	}

	resp := &msg.AvatarSitResponse{}
	resp.SitObject.ID = theCouch
	resp.SitTransform.AutoPilot = true
	resp.SitTransform.SitPosition = msg.Vector3{X: -0.59, Z: 0.55}
	f.Relay(t, resp)
	seatMe(t, f, couchLocal)

	seat, err := wait()
	if err != nil {
		t.Fatalf("Sit: %v", err)
	}
	if seat.Ground {
		t.Error("a sit on an object came back as a sit on the ground")
	}
	if seat.Local != couchLocal || seat.ID != theCouch || seat.Name != "a bench" {
		t.Errorf("the seat came back as %v", seat)
	}
	if want := (msg.Vector3{X: -0.59, Z: 0.55}); seat.Offset != want {
		t.Errorf("the seat offset is %v, want %v from the response", seat.Offset, want)
	}
}

// TestASitIsOneMessageAndNotTwo: every account of the protocol lists
// AgentRequestSit and then AgentSit, and measured the second one does
// nothing -- the avatar was seated six seconds before it was sent.
// Sending it anyway would be cargo cult, and this is what stops it
// creeping back in.
func TestASitIsOneMessageAndNotTwo(t *testing.T) {
	w, f := newFakeSession(t)

	wait := aside(t, func() (*Seat, error) {
		return w.Sit(context.Background(), theBench(), 5*time.Second)
	})
	waitSent[*msg.AgentRequestSit](t, f)
	seatMe(t, f, couchLocal)
	if _, err := wait(); err != nil {
		t.Fatalf("Sit: %v", err)
	}

	if got := sentOf[*msg.AgentSit](f); len(got) != 0 {
		t.Errorf("%d AgentSit went out; the whole of a sit is AgentRequestSit", len(got))
	}
}

// TestASitIsRefusedInTheGridsOwnWords: both of these were measured, both
// arrived in about a tenth of a second, and the second is misleading --
// the id was not an object at all, let alone one in another region.  It
// is repeated verbatim regardless, because it is the only detail
// somebody could act on and a client that improved it would be throwing
// that away.
func TestASitIsRefusedInTheGridsOwnWords(t *testing.T) {
	said := []struct{ when, text string }{
		{"an object eleven metres away",
			"No room to sit here, try another spot."},
		{"a uuid that is not an object at all",
			"Try moving closer.  Can't sit on object because\nit is not in the same region as you."},
	}
	for _, c := range said {
		text := c.text
		t.Run(c.when, func(t *testing.T) {
			w, f := newFakeSession(t)

			wait := aside(t, func() (*Seat, error) {
				return w.Sit(context.Background(), theBench(), 5*time.Second)
			})
			waitSent[*msg.AgentRequestSit](t, f)
			f.Relay(t, alert(text))

			seat, err := wait()
			if seat != nil {
				t.Errorf("a refused sit came back with a seat: %v", seat)
			}
			if !errors.Is(err, ErrSitRefused) {
				t.Fatalf("a refused sit gave %v, want ErrSitRefused", err)
			}
			if errors.Is(err, ErrTimeout) {
				t.Error("a refusal was reported as having heard nothing")
			}
			if !strings.Contains(err.Error(), strconv.Quote(text)) {
				t.Errorf("the refusal reads %q; the grid said %q", err, text)
			}
		})
	}
}

// TestTheNulAtTheEndOfAnAlertIsTrimmed: every alert measured ended in
// one, and it is invisible in most places a refusal is printed -- which
// is exactly why it would survive for years.
func TestTheNulAtTheEndOfAnAlertIsTrimmed(t *testing.T) {
	const text = "No room to sit here, try another spot."

	w, f := newFakeSession(t)
	wait := aside(t, func() (*Seat, error) {
		return w.Sit(context.Background(), theBench(), 5*time.Second)
	})
	waitSent[*msg.AgentRequestSit](t, f)
	f.Relay(t, alert(text))

	_, err := wait()
	if err == nil {
		t.Fatal("the sit was not refused")
	}
	if strings.Contains(err.Error(), `\x00`) || strings.ContainsRune(err.Error(), 0) {
		t.Errorf("the refusal carries the trailing NUL: %q", err)
	}
	if !strings.HasSuffix(err.Error(), strconv.Quote(text)) {
		t.Errorf("the refusal reads %q, want it to end with the grid's own sentence", err)
	}
}

// TestASitThatIsAnsweredWithNothingIsNotARefusal: a sit lands or is
// refused in about a tenth of a second, so silence is a third thing --
// and one where the sit may well have taken effect unseen.  A caller
// that could not tell it from a refusal would go and check the wrong
// thing.
func TestASitThatIsAnsweredWithNothingIsNotARefusal(t *testing.T) {
	w, _ := newFakeSession(t)

	seat, err := w.Sit(context.Background(), theBench(), 200*time.Millisecond)
	if seat != nil {
		t.Errorf("a sit nothing answered came back with a seat: %v", seat)
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("a sit nothing answered gave %v, want ErrTimeout", err)
	}
	if errors.Is(err, ErrSitRefused) {
		t.Error("silence was reported as a refusal")
	}
}

// TestAnAlreadySeatedAvatarIsNotTakenForANewSit: sitting on one thing
// while parented to another must wait for the parent to CHANGE.  The
// hazard is real for a seat this session has never been told the local
// id of, which is the case the second half of this covers: any parent
// would do there, so it had better not be the one already in place.
func TestAnAlreadySeatedAvatarIsNotTakenForANewSit(t *testing.T) {
	w, f := newFakeSession(t)

	// Seated on something else to begin with.
	seatMe(t, f, 555)

	// A bench this session has never heard described, so it has no local
	// id to wait for.
	bench := &Object{ID: theCouch}
	wait := aside(t, func() (*Seat, error) {
		return w.Sit(context.Background(), bench, 5*time.Second)
	})
	waitSent[*msg.AgentRequestSit](t, f)

	// The same parent again is not an answer.
	seatMe(t, f, 555)
	select {
	case <-time.After(150 * time.Millisecond):
	case <-w.Done():
		t.Fatal("the session ended")
	}

	seatMe(t, f, couchLocal)
	seat, err := wait()
	if err != nil {
		t.Fatalf("Sit: %v", err)
	}
	if seat.Local != couchLocal {
		t.Errorf("the sit ended on local %d, want %d", seat.Local, couchLocal)
	}
}

// TestAGroundSitIsProvedByTheAnimationAndNothingElse: there is no
// message for it, no reply to it and no reparenting -- one control flag
// out, and an animation 144 milliseconds later that is the whole of the
// evidence.
func TestAGroundSitIsProvedByTheAnimationAndNothingElse(t *testing.T) {
	w, f := newFakeSession(t)

	wait := asideErr(t, func() error {
		return w.SitOnGround(context.Background(), 5*time.Second)
	})
	waitFor(t, "the sit on ground flag", func() bool { return len(f.Controls()) > 0 })

	if got := f.Controls(); got[0] != agent.ControlSitOnGround {
		t.Errorf("the flag sent was %#x, want %#x", got[0], agent.ControlSitOnGround)
	}
	if got := sentOf[*msg.AgentRequestSit](f); len(got) != 0 {
		t.Error("a ground sit sent AgentRequestSit; there is no message for the ground")
	}

	// The list a measured ground sit produced.
	animate(t, f, agent.AnimSitGroundConstrained)
	if err := wait(); err != nil {
		t.Fatalf("SitOnGround: %v", err)
	}

	seat, err := w.Seat(context.Background())
	if err != nil {
		t.Fatalf("Seat: %v", err)
	}
	if seat == nil || !seat.Ground {
		t.Fatalf("after a ground sit the seat is %v, want the ground", seat)
	}
	if !seat.ID.IsZero() || seat.Local != 0 {
		t.Errorf("a ground sit named an object: %v", seat.Object)
	}
}

// TestAGroundSitIsNotDeclaredOnAnObjectSitAnimation: an avatar already
// seated on something is running a sitting animation, so a ground sit
// that accepted any of them would report success before the flag had
// reached the region.
func TestAGroundSitIsNotDeclaredOnAnObjectSitAnimation(t *testing.T) {
	w, f := newFakeSession(t)
	animate(t, f, agent.AnimSit)

	err := w.SitOnGround(context.Background(), 200*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("the ordinary sit animation ended a ground sit: %v", err)
	}
}

// TestStandingUpFromAnObjectIsTheUnParenting: measured, 126 milliseconds
// and the parent back to zero, with an absolute position again.
func TestStandingUpFromAnObjectIsTheUnParenting(t *testing.T) {
	w, f := newFakeSession(t)
	seatMe(t, f, couchLocal)

	wait := asideErr(t, func() error {
		return w.Stand(context.Background(), 5*time.Second)
	})
	waitFor(t, "the stand up flag", func() bool { return len(f.Controls()) > 0 })
	if got := f.Controls(); got[0] != agent.ControlStandUp {
		t.Errorf("the flag sent was %#x, want %#x", got[0], agent.ControlStandUp)
	}

	seatMe(t, f, 0)
	if err := wait(); err != nil {
		t.Fatalf("Stand: %v", err)
	}

	seat, err := w.Seat(context.Background())
	if err != nil {
		t.Fatalf("Seat: %v", err)
	}
	if seat != nil {
		t.Errorf("after standing the avatar is still sitting on %v", seat)
	}
}

// TestStandingUpFromTheGroundIsTheAnimationStopping: no parent was ever
// set, so nothing is un-parented and the animation going away is all
// there is to see.
func TestStandingUpFromTheGroundIsTheAnimationStopping(t *testing.T) {
	w, f := newFakeSession(t)
	animate(t, f, agent.AnimSitGroundConstrained)

	wait := asideErr(t, func() error {
		return w.Stand(context.Background(), 5*time.Second)
	})
	waitFor(t, "the stand up flag", func() bool { return len(f.Controls()) > 0 })

	animate(t, f, agent.AnimStand)
	if err := wait(); err != nil {
		t.Fatalf("Stand: %v", err)
	}
	if seat, err := w.Seat(context.Background()); err != nil || seat != nil {
		t.Errorf("after standing the seat is %v (err %v), want none", seat, err)
	}
}

// TestStandingWhenAlreadyStandingSendsTheFlagAndWaitsForNothing: there
// is no transition left to watch for, and waiting for one would turn a
// harmless repetition into a timeout.  The flag still goes, because what
// this session believes may be out of date.
func TestStandingWhenAlreadyStandingSendsTheFlagAndWaitsForNothing(t *testing.T) {
	w, f := newFakeSession(t)

	if err := w.Stand(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("Stand: %v", err)
	}
	if got := f.Controls(); len(got) != 1 || got[0] != agent.ControlStandUp {
		t.Errorf("the flags sent were %#x, want one stand up", got)
	}
}

// TestTheAnimationRelayIsBorrowedAndGivenBack: AvatarAnimation is the
// only evidence a ground sit gives and it costs every avatar in range
// every three seconds, so it is subscribed to for the length of the
// command and dropped again.  A subscription that was taken and kept
// would charge every client in the program for it.
func TestTheAnimationRelayIsBorrowedAndGivenBack(t *testing.T) {
	w, f := newFakeSession(t)

	wait := asideErr(t, func() error {
		return w.SitOnGround(context.Background(), 5*time.Second)
	})
	waitFor(t, "the animation subscription", func() bool { return f.Watching("AvatarAnimation") })

	animate(t, f, agent.AnimSitGroundConstrained)
	if err := wait(); err != nil {
		t.Fatalf("SitOnGround: %v", err)
	}

	if f.Watching("AvatarAnimation") {
		t.Error("the animation subscription was kept after the command finished")
	}
	want := []string{"+AvatarAnimation", "-AvatarAnimation"}
	if got := f.Watched(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("the subscription changes were %v, want %v", got, want)
	}
}

// TestASitOnAnObjectBorrowsNothing: the reparenting is relayed to every
// session anyway, so an object sit needs no subscription and must not
// take one -- it would be paying the animation bill for a command that
// never reads them.
func TestASitOnAnObjectBorrowsNothing(t *testing.T) {
	w, f := newFakeSession(t)

	wait := aside(t, func() (*Seat, error) {
		return w.Sit(context.Background(), theBench(), 5*time.Second)
	})
	waitSent[*msg.AgentRequestSit](t, f)
	seatMe(t, f, couchLocal)
	if _, err := wait(); err != nil {
		t.Fatalf("Sit: %v", err)
	}

	if got := f.Watched(); len(got) != 0 {
		t.Errorf("an object sit changed the subscriptions: %v", got)
	}
}

// TestTheSeatIsNamedFromWhatTheRegionHasDescribed: what the simulator
// says is a local id, which means nothing outside the region and nothing
// to a person.  The name and the id come from the object cache, and the
// parent is preferred over the animation wherever both are there --
// furniture that plays its own pose runs no animation this package
// recognises, so the animation is the weaker witness for an object sit.
func TestTheSeatIsNamedFromWhatTheRegionHasDescribed(t *testing.T) {
	w, f := newFakeSession(t)
	f.objects = []*Seen{{Object: Object{ID: theCouch, Local: couchLocal, Name: "a bench"}}}

	if seat, err := w.Seat(context.Background()); err != nil || seat != nil {
		t.Fatalf("a standing avatar has seat %v (err %v), want none", seat, err)
	}

	// Parented, and running a ground animation at the same time, which
	// is the disagreement the parent has to win.
	animate(t, f, agent.AnimSitGroundConstrained)
	seatMe(t, f, couchLocal)

	seat, err := w.Seat(context.Background())
	if err != nil {
		t.Fatalf("Seat: %v", err)
	}
	if seat == nil || seat.Ground {
		t.Fatalf("the seat is %v, want the bench", seat)
	}
	if seat.ID != theCouch || seat.Name != "a bench" || seat.Local != couchLocal {
		t.Errorf("the seat came back as %v", seat)
	}
}
