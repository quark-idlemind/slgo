package server

// Sitting down again after a login.
//
// An avatar that was sitting on something when its session ended comes
// back standing.  Nothing on the grid remembers the seat: a sit is a
// request the simulator acts on and does not record, and the Current
// Outfit folder -- which does record what an avatar is wearing -- has
// nothing to say about where it is.  So the remembering is this
// daemon's to do, and it is worth doing here rather than in a client
// because it is this daemon that logs the avatar in, including at its
// own startup when no client is attached to notice.
//
// # What is remembered, and when
//
// The object's id, which is the thing that survives.  A local id does
// not: locals are the region's own numbering and are handed out afresh
// every time, so the number an avatar was parented to last week names
// something else entirely today.
//
// It is learned two ways, because neither alone is enough.
//
// AvatarSitResponse is the simulator telling the avatar that just sat
// down where it has been put, and it carries the seat's OBJECT id
// already resolved.  That is the good source: it needs nothing looked
// up, it arrives however the sit was asked for, and it is the
// simulator's own word.
//
// The watch is the other, and it is what notices STANDING UP -- there
// is no message for that -- and any sit the first source missed.  A
// seated avatar is PARENTED to its seat, and the parent is in the
// region's own description of the avatar, so both facts are there to be
// read.
//
// The watch alone was tried first and is not enough on its own.  The
// parent is a LOCAL id, and turning one into an object means finding
// that object in the region listing -- which is exactly what may be
// missing.  Measured: an avatar seated on a chair, with the chair
// absent from a listing of 976 objects.  The parent was known, the seat
// was not, and there was nothing to write down.  Why the chair was
// never described was not established -- a packet thrown away as
// undecodable is the likeliest reason -- and the object store now asks
// for a parent it has not heard of, but a watch that depends on a
// description arriving is still at the mercy of one that does not.
//
// # Not knowing is not standing
//
// The distinction the watch turns on.  The region's description of this
// avatar may be missing -- it has not arrived yet, or it was lost --
// and a watch that read that as "standing" would throw away a perfectly
// good seat because it looked away at the wrong moment.  So the watch
// has three answers and not two: seated on this, standing, and no
// opinion.  Only the second forgets.
//
// # Why the restore retries
//
// A region does not hand over its contents at once.  The avatar is in
// world and able to move some seconds before the object it was sitting
// on has been described, and a sit request naming an object the
// simulator has not got to yet is answered with nothing at all.  So the
// request is repeated for a while and then given up on, which also
// covers the case that matters more: the seat is genuinely gone, taken
// home by its owner, and no amount of asking will bring it back.
//
// # Somebody else deciding is not a reason to stop watching
//
// The homing loop stops for good when a client teleports, because its
// job is to put an avatar back where it belongs and somebody who
// teleported has taken that decision.  This is not that.  Its job is to
// remember, so a client sitting the avatar somewhere else is not
// something to get out of the way of -- it is the next thing to write
// down, and the watch does.
//
// The RESTORE gets out of the way, and only the restore: an avatar
// somebody has already sat down while this was asking is left where
// they put it.

import (
	"context"
	"fmt"
	"time"

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
	// Seat is what this profile was last sitting on.  The zero id is
	// "nothing remembered", which is also what a profile nobody has
	// ever seen sitting answers.
	Seat(profile string) msg.UUID

	// SetSeat writes it down.  The zero id forgets.
	SetSeat(profile string, on msg.UUID)
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
// at the real pace would take two minutes to watch three attempts, and
// a test nobody will run is not one.
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
	want := h.seats.Seat(h.Name)
	if !want.IsZero() {
		if !sleepFor(ctx, pace.settle) {
			return
		}
		h.resit(ctx, want, pace)
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
//
// Measured, and the reason it is written this way: a restart where the
// chair was absent from the region listing throughout confirmed the sit
// only on the sixth and last attempt, eighty-five seconds in, because
// each round was waiting to recognise an object rather than to see the
// avatar sit down.  It had actually sat within seconds.
func (h *Hosted) resit(ctx context.Context, want msg.UUID, pace seatPace) {
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
		if !sleepFor(ctx, pace.retry) {
			return
		}
	}
	// Said once, and said plainly: the seat may be gone for good, and
	// an avatar standing where it used to sit is a thing somebody will
	// otherwise wonder about.
	h.logf("could not sit on %s again after %d attempts; it may no longer be there",
		want, pace.tries)
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
		switch was := h.seats.Seat(h.Name); {
		case on.IsZero() && !was.IsZero():
			h.seats.SetSeat(h.Name, msg.UUID{})
		case !on.IsZero() && on != was:
			h.seats.SetSeat(h.Name, on)
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
	if h.seats == nil || p.ID != msg.IDOf(&msg.AvatarSitResponse{}) {
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
	h.seats.SetSeat(h.Name, m.SitObject.ID)
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
