package sl

// Sitting down, and getting up again.
//
// An object sit is one message, AgentRequestSit.  The viewer's AgentSit
// after it does nothing -- the simulator has reparented the avatar by
// then -- so nothing here sends it.
// Why: doc/history/sit.md#agentrequestsit-is-the-whole-request
//
// The answer is a reparenting: this avatar's own ObjectUpdate comes back
// with ParentID set to the seat's local id, and a position that is now
// an offset from the object.  That is what Sit waits for.  It is what
// the simulator itself believes, it arrives whether or not the
// AvatarSitResponse did, and this package already keeps parents for
// everything else it hears about.  A sit also MOVES the avatar, up to
// about ten metres, so a caller that knew where the avatar was should
// treat that as stale.
//
// A refusal is an AlertMessage, in about a tenth of a second, and
// ErrSitRefused carries its text as it came.  So a sit has three
// outcomes: seated, refused in the grid's own words, and heard nothing
// before the timeout -- which is not a refusal, because the request may
// have taken effect unseen.  AlertMessage is a general channel, so an
// unrelated alert inside the window is read as the answer; the
// reparenting is checked first, so the mistake that can make is a
// spurious refusal and never a false success.
// Why: doc/history/sit.md#a-refusal-is-an-alertmessage-and-it-arrives-at-once
//
// The ground is a control flag on AgentUpdate, AGENT_CONTROL_SIT_ON_GROUND,
// and standing is another, AGENT_CONTROL_STAND_UP; the update is built
// by whoever owns the camera, which is never a client (Backend.Control,
// agent.Control).  A ground sit produces no reply and no reparenting,
// only the animation list, which is why these calls borrow a
// subscription to it (borrowAnimations) and hold the flag until it
// takes (controlUntil).  Standing from an object sit also un-parents.
// Why: doc/history/sit.md#sitting-on-the-ground-is-a-different-mechanism-entirely

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// ErrSitRefused is the simulator saying no, in its own words.
//
// The words are what it said and nothing else: trimmed of the NUL the
// alert ends with, and otherwise passed through untouched, newlines and
// double spaces and all.  They are the only part of a refusal a person
// can act on, and they are not always accurate -- an id that is not an
// object at all is refused with "it is not in the same region as you" --
// so repeating them is better than interpreting them.
var ErrSitRefused = errors.New("sl: the simulator refused the sit")

// DefaultSitTimeout is how long a sit or a stand is given when the
// caller names no timeout.
//
// Far longer than the measurements need, and deliberately.  An object
// sit is answered in about a tenth of a second either way, so anything
// beyond a second is already a sick region.  The ground is the reason
// for the rest of it: its only evidence is the animation list, and if
// the one sent 144 milliseconds after the flag is missed -- see
// borrowAnimations -- the next full list is along within about three
// seconds, so a timeout shorter than a few resends would turn a race
// nobody can see into a failure.
//
// The value is not promised and may change in any release: refer to it by name.
const DefaultSitTimeout = 15 * time.Second

// A Seat is what an avatar is sitting on.
//
// The Object is the seat.  Local is the region's own numbering and is
// what the simulator puts in the update that seats the avatar; ID and
// Name are filled in when this session has been told what that local id
// belongs to, and are empty when it has not.  That is ordinary rather
// than alarming: a seat further off than the draw distance is never
// described.
//
// Ground says the avatar is sitting on the ground, which is seated on
// nothing at all -- the Object is zero and stays zero, because there is
// no object.
type Seat struct {
	Object

	Ground bool

	// Offset is where on the seat the avatar was put, relative to the
	// object, as AvatarSitResponse gave it.  Zero means either the
	// middle or that no response was heard, and the two are not worth
	// separating: nothing waits for the response, because the
	// reparenting is what says the sit took.
	Offset msg.Vector3
}

// String names the seat, or says it is the ground.  A value receiver so
// that it is used whichever way a Seat is held: the embedded Object has
// one too, and a pointer method here would leave a Seat value printing
// as the object it does not have.
func (s Seat) String() string {
	if s.Ground {
		return "the ground"
	}
	// A seat nothing has described is known by its local id and by
	// nothing else.  Object.String would print the zero id as though
	// it were an id, which reads as an object whose key is all
	// noughts rather than as one nobody has named.
	if s.ID.IsZero() {
		return fmt.Sprintf("something not described here (local %d)", s.Local)
	}
	return s.Object.String()
}

// Sit sits the avatar on an object and waits for the simulator to say
// what became of it.
//
// The object is named by id, which is what the message carries.  A
// local id, when the caller has one, is used for one thing only: an
// avatar already parented to that very object is already seated on it,
// and asking again is answered at once rather than waited out.
//
// Returning means the simulator has reparented this avatar onto the
// seat, which is its own word that the sit took, and that the avatar has
// been MOVED to it -- possibly several metres, so anything a caller
// remembered about where it was standing is now wrong.  ErrSitRefused
// means the simulator said no and carries what it said.  ErrTimeout
// means neither happened, which is a third thing and not a refusal: the
// request may have taken effect without this hearing about it.
//
// A zero timeout is DefaultSitTimeout.
func (w *Session) Sit(ctx context.Context, o *Object, timeout time.Duration) (*Seat, error) {
	if o == nil || o.ID.IsZero() {
		return nil, fmt.Errorf("sl: nothing to sit on; a sit names an object by id")
	}
	if timeout == 0 {
		timeout = DefaultSitTimeout
	}

	m := &msg.AgentRequestSit{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.TargetObject.TargetID = o.ID
	// No offset.  A viewer sends where on the object the click landed,
	// which it worked out by casting a ray; there is no click here, and
	// the simulator seats the avatar wherever the object says to.

	// Where things stood before asking: which alerts had already been
	// heard, so that only a new one is read as the answer, and what the
	// avatar was parented to, so that being seated somewhere else
	// already cannot pass for having arrived here.
	w.mu.Lock()
	mark := len(w.alerts)
	before := w.seatLocal()
	target := w.localNow(o)
	w.mu.Unlock()

	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	// Half of what Session.await polls at.  The answer arrives in about
	// a tenth of a second either way, and a coarser poll would double
	// how long a sit appears to take for no benefit at all.
	const tick = 50 * time.Millisecond

	deadline := time.Now().Add(timeout)
	for {
		w.mu.Lock()
		if target == 0 {
			// The seat may have been described to us since we asked;
			// the update that seats the avatar often is the first
			// mention of it.
			target = w.locals[o.ID]
		}
		now := w.seatLocal()
		// Seated when the parent has CHANGED to something, or was
		// already the thing asked for.
		//
		// The change rather than a match against the target, because an
		// avatar's parent changes for exactly two reasons -- it sat or
		// it stood -- and only this request can have caused this one.
		// Insisting on the target would be insisting on knowing which
		// prim of a linkset the simulator seats an avatar on, which is
		// not measured anywhere, and would report a sit that plainly
		// worked as a timeout if the answer were the root rather than
		// the prim named.  The local id that came back is handed to the
		// caller either way.
		seated := now != 0 && (now != before || now == target)
		// An alert having ARRIVED is the refusal, rather than its text
		// being non-empty: a simulator that refused in silence has still
		// refused, and reporting that as a timeout would be reporting
		// the one thing a timeout is supposed to mean it was not.
		refused := !seated && len(w.alerts) > mark
		var said string
		if refused {
			said = w.alerts[mark]
		}
		w.mu.Unlock()

		if seated {
			return w.seatOn(now, o), nil
		}
		// Checked second on purpose.  An alert that arrives while a sit
		// is in flight is treated as the answer, and something else may
		// have been alerting, so the reparenting gets the first word:
		// the mistake this can make is a spurious refusal and never a
		// false success.
		if refused {
			return nil, fmt.Errorf("%w: %s", ErrSitRefused, strconv.Quote(said))
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: %s neither seated this avatar nor refused, "+
				"after %s; a sit is answered in about a tenth of a second either way, "+
				"so this is not a refusal and the sit may yet have taken",
				ErrTimeout, o, timeout)
		}
		t := time.NewTimer(tick)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
}

// SitOnGround sits the avatar down where it is standing, and waits for
// the animation that is the only sign it worked.
//
// Nothing is sent back for a ground sit: no reply, no reparenting,
// nothing on the wire at all except the animation list, which arrived
// 144 milliseconds after the flag when it was measured.  So this needs
// AvatarAnimation relayed to it and takes out a subscription for the
// length of the call; see borrowAnimations.
//
// A zero timeout is DefaultSitTimeout.
func (w *Session) SitOnGround(ctx context.Context, timeout time.Duration) error {
	if timeout == 0 {
		timeout = DefaultSitTimeout
	}

	give, err := w.borrowAnimations()
	if err != nil {
		return err
	}
	defer give()

	return w.controlUntil(ctx, agent.ControlSitOnGround, timeout,
		"the ground sit animation to start", func() bool {
			return groundSitting(w.anims)
		})
}

// Stand gets the avatar up, from either kind of sit, and waits for
// whichever proof applies.
//
// From an object sit that is the un-parenting -- 126 milliseconds, the
// parent back to zero and an absolute position again.  From a ground sit
// it is the ground animation stopping, which is all there is.
//
// An avatar this session believes is already standing is not an error
// and does not wait for anything: the flag is sent anyway, because what
// this session believes may be out of date, but there is no transition
// left to watch for.  That is also what a ground sit this session was
// never told about looks like -- the flag goes and the avatar does
// stand, and only the confirmation is missed.
//
// A zero timeout is DefaultSitTimeout.
func (w *Session) Stand(ctx context.Context, timeout time.Duration) error {
	if timeout == 0 {
		timeout = DefaultSitTimeout
	}

	give, err := w.borrowAnimations()
	if err != nil {
		return err
	}
	defer give()

	w.mu.Lock()
	parent := w.seatLocal()
	ground := groundSitting(w.anims)
	w.mu.Unlock()

	switch {
	case parent != 0:
		return w.controlUntil(ctx, agent.ControlStandUp, timeout,
			"this avatar to stop being parented to its seat",
			func() bool { return w.seatLocal() == 0 })
	case ground:
		return w.controlUntil(ctx, agent.ControlStandUp, timeout,
			"the ground sit animation to stop",
			func() bool { return !groundSitting(w.anims) })
	}
	// Nothing to wait for, so nothing to resend either: one flag, in
	// case what this session believes is out of date.
	return w.b.Control(ctx, agent.ControlStandUp)
}

// Seat is what the avatar is sitting on, or nil if it is not sitting.
//
// Nil and no error is standing.  A ground sit answers with a Seat that
// has Ground set and no object, since there is no object.
//
// # Where the answer comes from
//
// The agent's posture, where the backend can say it: the agent has seen
// every reparenting and every animation since login, where this session
// has seen only what arrived after it attached.  A seat's local id is
// then named by looking it up among the region's objects.
//
// A backend that cannot say leaves this session's own account.  An
// avatar sitting on something is PARENTED to it, and the parent is in
// the region's description of the objects in it, which is read when
// this session was told nothing: an avatar sitting still since before
// it attached has not been described to it.  A ground sit is not a
// parent, and what says it is happening is the animation, which this
// session hears only while a sit or a stand holds that subscription --
// so there a ground sit it did not perform reads as standing.
func (w *Session) Seat(ctx context.Context) (*Seat, error) {
	w.mu.Lock()
	local := w.seatLocal()
	ground := groundSitting(w.anims)
	on, offset := w.sitOn, w.sitOffset
	w.mu.Unlock()

	// The agent's own answer, where the backend can give it, beats this
	// session's: the agent has seen every animation and reparenting
	// since login, and this session only what arrived after it attached.
	// A session that attached to an avatar already sitting on the ground
	// has otherwise no evidence of it at all.  An older daemon that
	// cannot say leaves the session's own guess, below.
	if pt, ok := w.b.(postureTeller); ok {
		if p, seat, err := pt.Posture(ctx); err == nil {
			switch p {
			case agent.Standing:
				return nil, nil
			case agent.SittingOnGround:
				return &Seat{Ground: true}, nil
			case agent.SittingOnObject:
				local, ground = seat.Local, false
			}
		}
	}

	// One listing answers both halves: which local id this avatar is
	// parented to, and what object that is.  Asked for even when this
	// session already believes it knows the first, since the second
	// needs it anyway and a second opinion on the first costs nothing.
	found, err := w.fetch(ctx, "", "")
	if err != nil {
		// Not knowing is not standing.  Where this session has been
		// told it is seated, say so unnamed rather than throwing that
		// away because the listing could not be had.
		if local != 0 {
			return &Seat{Object: Object{Local: local}}, nil
		}
		if ground {
			return &Seat{Ground: true}, nil
		}
		return nil, err
	}

	if local == 0 {
		for _, s := range found {
			if s.ID == w.me {
				local = s.Parent
				break
			}
		}
	}
	if local == 0 {
		if ground {
			return &Seat{Ground: true}, nil
		}
		return nil, nil
	}

	seat := &Seat{Object: Object{Local: local}}
	for _, s := range found {
		if s.Local == local {
			seat.Object = s.Object
			break
		}
	}
	if !seat.ID.IsZero() && seat.ID == on {
		seat.Offset = offset
	}
	return seat, nil
}

// postureTeller is a backend that can say how the avatar is placed as
// the agent knows it.  Both of this package's backends can; a test's
// fake need not.
type postureTeller interface {
	Posture(ctx context.Context) (agent.Posture, agent.Seat, error)
}

// Posture for an in-process session is the agent's own.
func (d *Direct) Posture(ctx context.Context) (agent.Posture, agent.Seat, error) {
	p, seat := d.a.Posture()
	return p, seat, nil
}

// Posture through slgod asks the daemon's agent.
func (h *Hosted) Posture(ctx context.Context) (agent.Posture, agent.Seat, error) {
	r, err := h.conn.Posture(ctx)
	if err != nil {
		return agent.Standing, agent.Seat{}, err
	}
	seat := agent.Seat{Local: r.SeatLocal}
	if id, err := msg.ParseUUID(r.SeatId); err == nil {
		seat.ID = id
	}
	switch r.Posture {
	case pb.PostureResponse_SITTING_ON_GROUND:
		return agent.SittingOnGround, seat, nil
	case pb.PostureResponse_SITTING_ON_OBJECT:
		return agent.SittingOnObject, seat, nil
	}
	return agent.Standing, seat, nil
}

// seatOn builds the answer to a sit that worked, out of the object the
// caller named and the local id the simulator actually parented us to.
//
// The simulator's local id wins where they differ, because it is the one
// that says which thing the avatar is on; the caller's name and id ride
// along because they are what a person asked for.
func (w *Session) seatOn(local uint32, o *Object) *Seat {
	seat := &Seat{Object: *o}
	seat.Local = local

	w.mu.Lock()
	seat.from = w.at
	if w.sitOn == o.ID {
		seat.Offset = w.sitOffset
	}
	w.mu.Unlock()
	return seat
}

// seatLocal is the local id this avatar is parented to, or zero.  It
// must be called with the lock held.
//
// Two lookups because the avatar is an object like any other here: what
// local id this session's own avatar has, and what that local id says
// its parent is.  Never having heard either is the same answer as not
// being parented, which is right -- it happens in the seconds before the
// region has described this avatar to itself, and there is nothing
// seated about it.
func (w *Session) seatLocal() uint32 {
	local, ok := w.locals[w.me]
	if !ok {
		return 0
	}
	return w.parents[local]
}

// groundSitting reports whether one of the ground sit animations is
// running.
//
// The ground ones specifically, not any sit.  An avatar already seated
// on an object is running a sitting animation too, and treating that as
// a ground sit would have SitOnGround declare victory before the flag
// had reached the region.  The constants are agent's, which took them
// from the viewer's own llanimationstates.cpp; the constrained one is
// what a measured ground sit played.
func groundSitting(anims []msg.UUID) bool {
	for _, id := range anims {
		if id == agent.AnimSitGround || id == agent.AnimSitGroundConstrained {
			return true
		}
	}
	return false
}

// resendControl is how often a control flag is sent again while waiting
// for it to take.
//
// See controlUntil for why it is sent again at all.  Half a second is
// slower than a viewer, which sends one every frame it has the key held,
// and far faster than the wait it is trying to shorten.
const resendControl = 500 * time.Millisecond

// controlUntil sends a control flag, and keeps sending it until the
// simulator does what it means or the wait runs out.
//
// The flag is edge triggered, and once is still not enough: a stand
// sent a tenth of a second after a ground sit landed, itself straight
// after a stand off an object, was dropped by the simulator and the
// avatar stayed seated.  So this does what a viewer does, which is to
// HOLD the key: an update carrying the flag every resendControl until
// the avatar has done it.  Resending is free, since a flag already
// obeyed asks for something that has already happened, and it settles
// the borrowed subscription's race for nothing: one not yet applied for
// the first flag is there for the second.
// Why: doc/history/sit.md#stage-2-went-to-the-grid-and-the-grid-had-one-more-thing-to-say
func (w *Session) controlUntil(ctx context.Context, flags uint32, timeout time.Duration,
	what string, ok func() bool) error {

	deadline := time.Now().Add(timeout)
	for {
		if err := w.b.Control(ctx, flags); err != nil {
			return err
		}
		// Everything that has ever been measured answers in about a
		// tenth of a second, so this is the one that is expected to end
		// the loop; the resend is for the case that is not.
		err := w.await(ctx, min(resendControl, time.Until(deadline)), what, ok)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrTimeout) || !time.Now().Before(deadline) {
			return err
		}
	}
}

// animationRelay is the message the ground sit and the stand borrow.
const animationRelay = "AvatarAnimation"

// borrowAnimations takes out the AvatarAnimation subscription for the
// length of one command and hands back the way to give it up.
//
// Borrowed rather than kept: it arrives for every avatar in range, in
// full, about every three seconds, and keeping it in Subscriptions would
// charge that to slrun and slbench for ever for something neither
// reads.  Given back for real: Watcher.Unwatch subtracts from the set
// the daemon holds for this stream, the count here stops two
// overlapping commands taking it from each other, and a session that
// named AvatarAnimation when it attached keeps it regardless (see
// Hosted.Unwatch).  A backend that is not a Watcher relays everything
// already, so there is nothing to take out and nothing to give back.
//
// Watch is in force before anything sent afterwards on the stream, and
// Control is not sent on the stream, so the first flag could in
// principle beat the subscription.  The list is resent in full about
// every three seconds, so losing that race costs a wait, not an answer.
// Why: doc/history/sit.md#the-subscription-question
func (w *Session) borrowAnimations() (give func(), err error) {
	b, ok := w.b.(Watcher)
	if !ok {
		return func() {}, nil
	}

	w.mu.Lock()
	w.animWatch++
	first := w.animWatch == 1
	w.mu.Unlock()

	release := func() {
		w.mu.Lock()
		w.animWatch--
		last := w.animWatch == 0
		w.mu.Unlock()
		if last {
			// Nothing to do about a failure here.  It means the stream
			// is gone, which is the session ending, and the
			// subscription goes with it.
			_ = b.Unwatch(animationRelay)
		}
	}

	if first {
		if err := b.Watch(animationRelay); err != nil {
			release()
			return func() {}, fmt.Errorf("sl: cannot listen for animations, "+
				"which is the only evidence a ground sit gives: %w", err)
		}
	}
	return release, nil
}
