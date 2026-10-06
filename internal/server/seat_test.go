package server

// Putting an avatar back on what it was sitting on.
//
// Like the homing tests, these are about what goes out on the wire and
// when it stops: the fake sim does not seat anybody, so from this side
// a successful sit and a request into the void look the same, and the
// request is the only thing there is to assert on.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// quickSeating makes the loop run in test time rather than in the
// minute and a half a region takes to hand over its contents.
func quickSeating(t *testing.T) {
	t.Helper()
	settle, retry, tries, watch := SeatSettle, SeatRetry, SeatTries, SeatWatch
	SeatSettle, SeatRetry, SeatTries, SeatWatch =
		10*time.Millisecond, 20*time.Millisecond, 3, 20*time.Millisecond
	t.Cleanup(func() {
		SeatSettle, SeatRetry, SeatTries, SeatWatch = settle, retry, tries, watch
	})
}

// rememberedSeats is server.Seats in memory, and counts what was
// written so that a test can tell "wrote the same thing again" from
// "wrote nothing".
type rememberedSeats struct {
	mu     sync.Mutex
	on     map[string]msg.UUID
	in     map[string]msg.UUID
	writes int
}

func newSeats(profile string, on msg.UUID) *rememberedSeats {
	return newSeatsIn(profile, on, msg.UUID{})
}

// newSeatsIn is a seat with the region it was in; a zero region is a
// line from before regions were kept.
func newSeatsIn(profile string, on, region msg.UUID) *rememberedSeats {
	s := &rememberedSeats{on: map[string]msg.UUID{}, in: map[string]msg.UUID{}}
	if !on.IsZero() {
		s.on[profile] = on
		s.in[profile] = region
	}
	return s
}

func (s *rememberedSeats) Seat(profile string) (on, region msg.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on[profile], s.in[profile]
}

func (s *rememberedSeats) SetSeat(profile string, on, region msg.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	if on.IsZero() {
		delete(s.on, profile)
		delete(s.in, profile)
		return
	}
	s.on[profile] = on
	s.in[profile] = region
}

func (s *rememberedSeats) wrote() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

// seatRig is homeRig with a place to remember seats.
func seatRig(t *testing.T, seats Seats) (*Hosted, *fakeSim) {
	t.Helper()
	h, sim, _ := seatRigWith(t, seats, nil, nil)
	return h, sim
}

// seatRigWith sets the sim up before the login, and logs to log.
func seatRigWith(t *testing.T, seats Seats, setup func(*fakeSim), log func(string, ...any)) (*Hosted, *fakeSim, *Server) {
	t.Helper()
	sim := newSim(t)
	t.Cleanup(sim.close)
	if setup != nil {
		sim.mu.Lock() // the sim is already listening
		setup(sim)
		sim.mu.Unlock()
	}

	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-8597-66b760a8cb5f</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-3bf6-2277c65663be</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-2a9a-c37f17e64c61</string></value></member>
		  <member><name>circuit_code</name><value><int>4321</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>first_name</name><value><string>"Example"</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`,
			sim.addr().IP, sim.addr().Port)
	}))
	t.Cleanup(hs.Close)

	srv := New()
	srv.SetSeats(seats)
	if log != nil {
		srv.SetLog(log)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	h, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL, Start: "last"},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}
	return h, sim, srv
}

// satOn is how many times this session has asked to sit.
func satOn(sim *fakeSim) int { return counted(sim, "AgentRequestSit") }

// TestAnAvatarIsPutBackOnWhatItWasSittingOn.
//
// Nothing on the grid remembers a seat.  A sit is a request the
// simulator acts on and does not record, so an avatar that was in a
// chair when its session ended comes back standing -- which is the
// whole reason this exists.
//
// The retry is the other half.  A region does not hand over its
// contents at once, and a sit naming an object the simulator has not
// described yet is answered with nothing at all, so one request at the
// moment of login would miss by a few seconds and look like a seat
// that had gone.
func TestAnAvatarIsPutBackOnWhatItWasSittingOn(t *testing.T) {
	quickSeating(t)
	chair := msg.MustParseUUID("2ddb7e57-7e57-c0de-1fc2-4a8634a86f7a")
	seats := newSeats("example", chair)

	_, sim := seatRig(t, seats)

	deadline := time.Now().Add(2 * time.Second)
	for satOn(sim) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if satOn(sim) == 0 {
		t.Fatal("a remembered seat was never asked for")
	}

	// And it names the object that was remembered rather than any
	// other, since a local id would not have survived the login and an
	// id this daemon invented would name nothing.
	bodies := sim.sawBody("AgentRequestSit")
	if len(bodies) == 0 {
		t.Fatal("the sit request was counted but not kept")
	}
	var m msg.AgentRequestSit
	if err := m.Decode(bodies[0]); err != nil {
		t.Fatalf("the sit request will not decode: %v", err)
	}
	if m.TargetObject.TargetID != chair {
		t.Errorf("the sit named %v, want the remembered seat %v", m.TargetObject.TargetID, chair)
	}

	// The fake seats nobody, so the loop goes on asking and then gives
	// up rather than asking for ever.
	time.Sleep(300 * time.Millisecond)
	if n := satOn(sim); n > SeatTries {
		t.Errorf("it asked %d times, want no more than %d", n, SeatTries)
	}
}

// TestNothingRememberedAsksNothing, since an avatar that has never been
// seen sitting has no chair to be put back in and a daemon that sat it
// somewhere anyway would be inventing a fact.
func TestNothingRememberedAsksNothing(t *testing.T) {
	quickSeating(t)
	_, sim := seatRig(t, newSeats("example", msg.UUID{}))

	time.Sleep(200 * time.Millisecond)
	if n := satOn(sim); n != 0 {
		t.Errorf("it asked to sit %d times with nothing remembered", n)
	}
}

// TestNoSeatsAtAllIsTheOldBehaviour.
//
// A server given nowhere to remember seats does not remember them, and
// in particular does not start a loop that would read a nil interface.
func TestNoSeatsAtAllIsTheOldBehaviour(t *testing.T) {
	quickSeating(t)
	_, sim := seatRig(t, nil)

	time.Sleep(200 * time.Millisecond)
	if n := satOn(sim); n != 0 {
		t.Errorf("a server with no seat store asked to sit %d times", n)
	}
}

// TestNotKnowingIsNotStanding.
//
// The distinction the whole watch turns on.  A session that cannot say
// where the avatar is has no opinion, and an opinionless answer must
// not be written down as "standing" -- that would throw away a good
// seat because the region had not described the avatar yet, which it
// has not for the first seconds of every session.
func TestNotKnowingIsNotStanding(t *testing.T) {
	h := &Hosted{Name: "example"}
	if on, known := h.currentSeat(); known || !on.IsZero() {
		t.Errorf("a session with no agent answered %v, %v; want the zero id and not known",
			on, known)
	}
}

// TestAnUnnameableSeatIsStillASeat.
//
// The loop confirms by the avatar being parented to something, not by
// being parented to a thing it can put a name to.  Those are different
// questions and only the first is the one being asked -- an avatar
// sitting on a chair nobody has described is sitting on it just the
// same.
//
// Measured, and the reason: a restart where the chair was missing from
// the region listing throughout confirmed only on the sixth and last
// attempt, eighty-five seconds in, having actually sat within seconds.
// Waiting to RECOGNISE the seat rather than to see the avatar sit is
// what cost that, and a slower region would have turned it into a
// report of failure for a sit that worked.
func TestAnUnnameableSeatIsStillASeat(t *testing.T) {
	h := &Hosted{Name: "example"}
	// No agent at all: not seated, and not known to be standing
	// either.
	if local, known := h.seatedLocal(); known || local != 0 {
		t.Errorf("a session with no agent answered %d, %v; want zero and not known",
			local, known)
	}
}

// The region's answer to a sit on an object it does not hold, as the
// log of a measurement shows it: the notification's name in AlertInfo,
// and its text.  Why: doc/daemon.md#gone-elsewhere-or-only-slow
func notSameRegion() *msg.AlertMessage {
	m := &msg.AlertMessage{}
	m.AlertData.Message = []byte("Try moving closer.  Can't sit on object because\nit is not in the same region as you.\x00")
	m.AlertInfo = []msg.AlertMessage_AlertInfo{{Message: []byte("SitFailNotSameRegion\x00")}}
	return m
}

var (
	seatChair   = msg.MustParseUUID("5cb57e57-7e57-c0de-8a22-9bc748f03c36")
	seatRegion  = msg.MustParseUUID("72497e57-7e57-c0de-09f6-f01db3576907")
	otherRegion = msg.MustParseUUID("a6f37e57-7e57-c0de-375c-a1552202781c")
	seatSelf    = msg.MustParseUUID("876e7e57-7e57-c0de-8597-66b760a8cb5f")
)

// inRegion makes the sim a region with this id.
func inRegion(id msg.UUID) func(*fakeSim) { return func(f *fakeSim) { f.regionID = id } }

// parentTo tells the session the avatar is parented to local, zero
// being standing.
func parentTo(sim *fakeSim, local uint32) {
	sim.send(&msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{
		ID: 77, FullID: seatSelf, PCode: 47, ParentID: local,
		ObjectData: make([]byte, 60)}}}, 0)
}

// logLines collects what the session logs.
type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) add(f string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(f, v...))
}

func (l *logLines) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestASeatTheRegionDoesNotHoldIsForgotten: the avatar is in the
// seat's region, every attempt was refused as not in this region, so
// the seat is gone.  An old line without a region is asked the same
// way and ends the same way.
func TestASeatTheRegionDoesNotHoldIsForgotten(t *testing.T) {
	for _, c := range []struct {
		what   string
		region msg.UUID
	}{
		{"a seat with its region", seatRegion},
		{"an old line without a region", msg.UUID{}},
	} {
		t.Run(c.what, func(t *testing.T) {
			quickSeating(t)
			seats := newSeatsIn("example", seatChair, c.region)
			var logs logLines
			_, sim, _ := seatRigWith(t, seats, func(f *fakeSim) {
				f.regionID = seatRegion
				f.onSit = func(int) { f.send(notSameRegion(), 0) }
			}, logs.add)
			_ = sim

			waitFor(t, 3*time.Second, "the seat to be forgotten", func() bool {
				on, _ := seats.Seat("example")
				return on.IsZero()
			})
			waitFor(t, 3*time.Second, "forgetting the seat to be logged", func() bool {
				return logs.has("forgot the seat")
			})
			if n := satOn(sim); n != SeatTries {
				t.Errorf("it asked %d times, want %d", n, SeatTries)
			}
		})
	}
}

// TestASeatInAnotherRegionIsNotAskedFor: the sit cannot work from
// here and the seat may well be there, so nothing is sent and the seat
// is kept.
func TestASeatInAnotherRegionIsNotAskedFor(t *testing.T) {
	quickSeating(t)
	seats := newSeatsIn("example", seatChair, otherRegion)
	var logs logLines
	_, sim, _ := seatRigWith(t, seats, inRegion(seatRegion), logs.add)

	waitFor(t, 3*time.Second, "the line saying why it did not ask", func() bool {
		return logs.has("not asking to sit")
	})
	time.Sleep(150 * time.Millisecond)
	if n := satOn(sim); n != 0 {
		t.Errorf("it asked %d times for a seat in another region", n)
	}
	if on, in := seats.Seat("example"); on != seatChair || in != otherRegion {
		t.Errorf("the seat is %v in %v, want it kept", on, in)
	}
}

// TestAnUnansweredSeatIsKept: no answer proves nothing, so the seat
// stays as it did before.
func TestAnUnansweredSeatIsKept(t *testing.T) {
	quickSeating(t)
	seats := newSeatsIn("example", seatChair, seatRegion)
	var logs logLines
	_, sim, _ := seatRigWith(t, seats, inRegion(seatRegion), logs.add)

	waitFor(t, 3*time.Second, "the giving up", func() bool {
		return logs.has("could not sit on")
	})
	if n := satOn(sim); n != SeatTries {
		t.Errorf("it asked %d times, want %d", n, SeatTries)
	}
	if on, _ := seats.Seat("example"); on != seatChair {
		t.Errorf("the seat is %v, want it kept", on)
	}
}

// TestASeatThatSeatsOnTheThirdTryIsKept: refused twice and then
// seated is a seat that exists.
func TestASeatThatSeatsOnTheThirdTryIsKept(t *testing.T) {
	quickSeating(t)
	seats := newSeatsIn("example", seatChair, seatRegion)
	var logs logLines
	_, sim, _ := seatRigWith(t, seats, func(f *fakeSim) {
		f.regionID = seatRegion
		f.onSit = func(n int) {
			if n < 3 {
				f.send(notSameRegion(), 0)
				return
			}
			parentTo(f, 99)
		}
	}, logs.add)
	_ = sim

	waitFor(t, 3*time.Second, "the avatar to be seated again", func() bool {
		return logs.has("again, after 3 attempts")
	})
	if on, _ := seats.Seat("example"); on != seatChair {
		t.Errorf("the seat is %v, want it kept", on)
	}
	if logs.has("forgot") {
		t.Error("a seat that seated the avatar was forgotten")
	}
}

// TestAStandJustBeforeLogoutIsWrittenDown: the watch had not seen the
// avatar stand, and the logout reads it one last time.
func TestAStandJustBeforeLogoutIsWrittenDown(t *testing.T) {
	quickSeating(t)
	// The watch is slower than the test, which is the case.
	SeatWatch = time.Hour
	// And no sit is asked, so the seat is still there at the stop.
	seats := newSeatsIn("example", seatChair, otherRegion)
	h, sim, srv := seatRigWith(t, seats, inRegion(seatRegion), nil)

	parentTo(sim, 0)
	waitFor(t, 3*time.Second, "the avatar to be known standing", func() bool {
		on, known := h.currentSeat()
		return known && on.IsZero()
	})
	if on, _ := seats.Seat("example"); on != seatChair {
		t.Fatalf("the seat was forgotten before the stop: %v", on)
	}
	if _, err := srv.Logout(context.Background(), &pb.LogoutRequest{Agent: "example"}); err != nil {
		t.Fatal(err)
	}
	if on, _ := seats.Seat("example"); !on.IsZero() {
		t.Errorf("the seat is %v after a stand and a logout, want it forgotten", on)
	}
}

// TestAStandAskedForForgetsTheSeat: a stand this avatar asks for forgets
// the seat at once, without waiting for the region to say the avatar is
// standing; a daemon stopped in that moment kept the seat.  Any other
// update leaves it.
func TestAStandAskedForForgetsTheSeat(t *testing.T) {
	quickSeating(t)
	SeatWatch = time.Hour
	seats := newSeatsIn("example", seatChair, otherRegion)
	h, _, _ := seatRigWith(t, seats, inRegion(seatRegion), nil)

	send := func(flags uint32) {
		m := &msg.AgentUpdate{}
		m.AgentData.ControlFlags = flags
		body, err := m.Encode()
		if err != nil {
			t.Fatal(err)
		}
		h.noteStandSent(&msg.Packet{ID: msg.IDOf(m), Body: body})
	}
	send(agent.ControlAtPos)
	if on, _ := seats.Seat("example"); on != seatChair {
		t.Fatalf("a walk forgot the seat: %v", on)
	}
	send(agent.ControlStandUp)
	if on, _ := seats.Seat("example"); !on.IsZero() {
		t.Errorf("the seat is %v after asking to stand, want it forgotten", on)
	}
}
