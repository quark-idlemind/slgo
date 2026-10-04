package server

// Sitting down again after a login.
//
// Nothing on the grid remembers a seat, so this daemon does: it is what
// logs the avatar in, including at startup with no client attached.
// What is remembered is the seat's object id, with the id of the region
// the avatar was in; a local id is the region's own numbering, handed
// out afresh every time.
//
// It is learned two ways.  AvatarSitResponse carries the seat's object
// id however the sit was asked for.  The watch reads what the avatar is
// parented to, which is what notices standing up -- there is no message
// for that -- and any sit the first missed.  The watch has three
// answers, seated on this, standing, and no opinion, because the
// region's description of the avatar may not have arrived; only
// standing forgets.
//
// The restore asks again for a while, since a sit naming an object the
// simulator has not described yet is answered with nothing, and then
// gives up.  It does not ask from another region than the seat's, which
// cannot work and loses nothing.  If every ask was refused as "not in
// this region" the seat is forgotten; any other end keeps it.  It leaves
// alone an avatar somebody else sat down meanwhile, and the watch goes on
// writing down whatever the avatar sits on.  A stop reads the seat one
// last time, for a stand the watch had not yet seen.
// Why: doc/daemon.md#sitting-down-again

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// Seats is how the daemon remembers what each avatar was sitting on.
//
// An interface rather than a directory, for the reason state.go gives
// about profiles: where this is kept, what format it is in, and whether
// it is kept at all are cmd/slgod's business and not the server's.  A
// server with none behaves exactly as it did before -- it remembers
// nothing and restores nothing.
type Seats interface {
	// Seat is what this profile was last sitting on, and the id of the
	// region the avatar was in.  The zero id is "nothing remembered",
	// which is also what a profile nobody has ever seen sitting
	// answers.  A zero region is "not known", as in a seat written
	// before regions were kept.
	Seat(profile string) (on, region msg.UUID)

	// SetSeat writes it down.  The zero id forgets.
	SetSeat(profile string, on, region msg.UUID)
}

// SetSeats gives the server somewhere to remember seats.  Nil turns the
// remembering off.
//
// Set before any session comes up.  A Hosted takes its copy when it is
// made, the way it takes everything else it needs, so setting this
// afterwards leaves the sessions already running without it.
func (s *Server) SetSeats(seats Seats) {
	s.mu.Lock()
	s.seats = seats
	s.mu.Unlock()
}

// Seats is where seats are remembered, or nil.
func (s *Server) Seats() Seats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seats
}

// SeatSettle is how long to wait after a login before asking to sit,
// SeatRetry how long between attempts, SeatTries how many, and
// SeatWatch how often the seat is read afterwards.
//
// Vars so that a test can shorten them.  A test of the retry that ran
// at the real pace would take most of a minute to watch three attempts,
// and a test nobody will run is not one.
var (
	SeatSettle = 10 * time.Second
	SeatRetry  = 15 * time.Second
	SeatTries  = 6
	SeatWatch  = 30 * time.Second
)

// seatPace is the four above, read once as a loop starts, so that a test
// putting them back afterwards does not race a loop still running.
type seatPace struct {
	settle, retry, watch time.Duration
	tries                int
}

// keepSeat starts the loop that puts this avatar back on what it was
// sitting on, and then watches for it to sit on something else.
//
// Called for every session as it comes up, including a reconnect, which
// is a fresh login and leaves the avatar standing exactly as the first
// one did.
func (h *Hosted) keepSeat(ctx context.Context) {
	if h.seats == nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	if h.seating != nil {
		h.seating()
	}
	h.seating = cancel
	h.mu.Unlock()

	pace := seatPace{settle: SeatSettle, retry: SeatRetry, watch: SeatWatch, tries: SeatTries}
	go h.seatward(ctx, pace)
}

// seatward restores the remembered seat and then keeps the memory up to
// date.
func (h *Hosted) seatward(ctx context.Context, pace seatPace) {
	want, region := h.seats.Seat(h.Name)
	if !want.IsZero() {
		if !sleepFor(ctx, pace.settle) {
			return
		}
		h.resit(ctx, want, region, pace)
	}
	h.watchSeat(ctx, pace)
}

// resit asks to sit on what was remembered, until it works or until
// there is no more point asking.
//
// What counts as working is the avatar being PARENTED to something,
// rather than being parented to a thing this daemon can put a name to.
// The two are not the same question and only the first is the one being
// asked: an avatar sitting on a chair nobody has described is sitting
// on it just the same.
// Why: doc/daemon.md#what-counts-as-having-sat-down
func (h *Hosted) resit(ctx context.Context, want, region msg.UUID, pace seatPace) {
	here := h.regionID()
	if !region.IsZero() && !here.IsZero() && region != here {
		// Asking would be refused as it is for a deleted seat, and the
		// seat may well be where it was.
		h.logf("not asking to sit on %s again: it was in region %s and this avatar is in %s; keeping it",
			want, region, here)
		return
	}

	refused0 := h.sitRefusals.Load()
	asked := 0
	for attempt := 0; attempt < pace.tries; attempt++ {
		if local, known := h.seatedLocal(); known && local != 0 {
			h.saySeated(want, local, attempt)
			return
		}

		a := h.Agent()
		if a == nil {
			return
		}
		m := &msg.AgentRequestSit{}
		m.AgentData.AgentID = a.Account.AgentID
		m.AgentData.SessionID = a.Account.SessionID
		m.TargetObject.TargetID = want
		if err := a.Send.Send(ctx, m); err != nil {
			return
		}
		asked++
		if !sleepFor(ctx, pace.retry) {
			return
		}
	}
	if local, known := h.seatedLocal(); known && local != 0 {
		h.saySeated(want, local, pace.tries-1)
		return
	}
	// Said once, and said plainly: the seat may be gone for good, and
	// an avatar standing where it used to sit is a thing somebody will
	// otherwise wonder about.
	h.logf("could not sit on %s again after %d attempts; it may no longer be there",
		want, pace.tries)

	// The region answers a sit on what it does not hold the same way
	// for another region's object and for a deleted one, and the avatar
	// is in the seat's region, so every such answer says it is gone.
	// Any other end proves nothing.
	// Why: doc/daemon.md#gone-elsewhere-or-only-slow
	if asked > 0 && h.sitRefusals.Load()-refused0 >= uint64(asked) &&
		(here.IsZero() || h.regionID() == here) {
		h.seats.SetSeat(h.Name, msg.UUID{}, msg.UUID{})
		h.logf("forgot the seat %s: all %d attempts were refused as not in this region, "+
			"and the avatar is in the region the seat was in", want, asked)
	}
}

// regionID is the id of the region the avatar is in, zero if not known.
func (h *Hosted) regionID() msg.UUID {
	a := h.Agent()
	if a == nil {
		return msg.UUID{}
	}
	if r, ok := a.Region(); ok {
		return r.ID
	}
	return msg.UUID{}
}

// noteStandSent forgets the seat when this avatar asks to stand up,
// whoever asked: a client through Control, a shell, a viewer.  The
// region's word that the avatar is standing comes a moment later, and a
// daemon stopped in that moment kept a seat the avatar had left; a stand
// that does not take leaves the avatar on the seat, and the watch writes
// it down again.
// Why: doc/daemon.md#gone-elsewhere-or-only-slow
func (h *Hosted) noteStandSent(p *msg.Packet) {
	if h.seats == nil || p.ID != agentUpdateID {
		return
	}
	body := packetBody(p)
	if body == nil {
		return
	}
	var m msg.AgentUpdate
	if m.Decode(body) != nil || m.AgentData.ControlFlags&agent.ControlStandUp == 0 {
		return
	}
	if was, _ := h.seats.Seat(h.Name); !was.IsZero() {
		h.seats.SetSeat(h.Name, msg.UUID{}, msg.UUID{})
		h.logf("forgot the seat %s: asked to stand up", was)
	}
}

var agentUpdateID = msg.IDOf(&msg.AgentUpdate{})

// standingAtStop forgets the seat if the avatar is standing now, for a
// stand just before a stop that the watch had not yet seen.  It reads
// what the session already holds and asks the region nothing.
func (h *Hosted) standingAtStop() {
	if h.seats == nil {
		return
	}
	if on, known := h.currentSeat(); known && on.IsZero() {
		if was, _ := h.seats.Seat(h.Name); !was.IsZero() {
			h.seats.SetSeat(h.Name, msg.UUID{}, msg.UUID{})
		}
	}
}

// sitRefusedNotSameRegion says whether an alert is the region's answer
// to a sit on an object it does not hold: by its notification name, or
// failing that the shape of its text.
func sitRefusedNotSameRegion(m *msg.AlertMessage) bool {
	texts := []string{string(m.AlertData.Message)}
	for _, i := range m.AlertInfo {
		texts = append(texts, string(i.Message))
	}
	for _, t := range texts {
		t = strings.ToLower(strings.TrimRight(t, "\x00"))
		if t == "sitfailnotsameregion" ||
			(strings.Contains(t, "can't sit") && strings.Contains(t, "same region")) {
			return true
		}
	}
	return false
}

// saySeated reports what the avatar ended up on, in whatever detail is
// actually known.
//
// Three sentences because there are three states and they mean
// different things to whoever reads the log: it worked, somebody else
// got there first, and it is sitting on something that cannot be named
// from here.  The third is not a failure and must not read like one.
func (h *Hosted) saySeated(want msg.UUID, local uint32, attempt int) {
	after := ""
	if attempt > 0 {
		after = fmt.Sprintf(", after %d attempts", attempt+1)
	}
	switch on, known := h.currentSeat(); {
	case known && on == want:
		h.logf("sitting on %s again%s", want, after)
	case known && !on.IsZero():
		// Somebody else sat this avatar down while we were asking.
		// Theirs wins; the memory catches up on the next round of the
		// watch.
		h.logf("something else sat this avatar on %s; leaving it there", on)
	default:
		h.logf("sitting on local %d again%s; nothing here describes it, so this cannot "+
			"say for certain that it is %s", local, after, want)
	}
}

// seatedLocal is the local id this avatar is parented to, and whether
// the region has described the avatar at all.
//
// Zero and known is standing.  Zero and not known is "no opinion", the
// distinction the head of this file is about.
func (h *Hosted) seatedLocal() (uint32, bool) {
	a := h.Agent()
	if a == nil {
		return 0, false
	}
	objs := a.Objects()
	if objs == nil {
		return 0, false
	}
	me, ok := objs.Get(a.Account.AgentID)
	if !ok {
		return 0, false
	}
	return me.Parent, true
}

// watchSeat notices the avatar standing up, and any sit that
// AvatarSitResponse did not carry.
//
// What is remembered is read afresh each round rather than kept here,
// because the other source writes to it too and a copy held in this
// loop would go stale the moment it did.
func (h *Hosted) watchSeat(ctx context.Context, pace seatPace) {
	for {
		if !sleepFor(ctx, pace.watch) {
			return
		}
		on, known := h.currentSeat()
		if !known {
			// No opinion.  Not standing; see the head of this file.
			continue
		}
		was, wasIn := h.seats.Seat(h.Name)
		switch here := h.regionID(); {
		case on.IsZero() && !was.IsZero():
			h.seats.SetSeat(h.Name, msg.UUID{}, msg.UUID{})
		case !on.IsZero() && (on != was || wasIn.IsZero() && !here.IsZero()):
			h.seats.SetSeat(h.Name, on, here)
		}
	}
}

// noteSeatMessage records what the simulator says this avatar has just
// sat on.
//
// Called for every message the session receives; see relay.  It is the
// session's business whether or not a client is attached to hear it,
// which is the same reason the teleport answers are read there.
func (h *Hosted) noteSeatMessage(p *msg.Packet) {
	if h.seats == nil {
		return
	}
	sit, alert := msg.IDOf(&msg.AvatarSitResponse{}), msg.IDOf(&msg.AlertMessage{})
	if p.ID != sit && p.ID != alert {
		return
	}
	body := p.Body
	if body == nil && p.Message != nil {
		b, err := p.Message.Encode()
		if err != nil {
			return
		}
		body = b
	}
	if p.ID == alert {
		var m msg.AlertMessage
		if m.Decode(body) == nil && sitRefusedNotSameRegion(&m) {
			h.sitRefusals.Add(1)
		}
		return
	}
	var m msg.AvatarSitResponse
	if err := m.Decode(body); err != nil {
		return
	}
	// A sit on the ground answers with no object, and there is nothing
	// to remember about one: it is a place rather than a thing, and it
	// will not be there to sit on again.
	if m.SitObject.ID.IsZero() {
		return
	}
	h.seats.SetSeat(h.Name, m.SitObject.ID, h.regionID())
}

// currentSeat is the object this avatar is sitting on, and whether
// anything is known about it at all.
//
// The three answers are the point; see the head of this file.  A zero
// id with known set is standing, which is a fact worth writing down.  A
// zero id with known clear is "the region has not said", which is not.
func (h *Hosted) currentSeat() (on msg.UUID, known bool) {
	local, known := h.seatedLocal()
	if !known {
		return msg.UUID{}, false
	}
	if local == 0 {
		return msg.UUID{}, true
	}
	seat, ok := h.Agent().Objects().ByLocal(local)
	if !ok {
		// Parented to something nobody has described.  Seated, then,
		// but there is no id to remember and the old one is not wrong
		// yet.
		return msg.UUID{}, false
	}
	return seat.ID, true
}
