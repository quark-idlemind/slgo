package sl

// Sitting down, and getting up again.
//
// # A sit is one message, not two
//
// Every account of the protocol lists AgentRequestSit and then AgentSit,
// and a viewer does send both.  Measured on Agni, the first is the whole
// request and the second does nothing:
//
//	0.000  AgentRequestSit -->
//	0.106  <-- AvatarSitResponse   autopilot=true pos=<-0.59, 0, 0.55>
//	0.123  <-- ObjectUpdate  me  parent=83600601
//	6.007  AgentSit -->
//	       (nothing at all)
//
// The avatar was seated 123 milliseconds after asking and six seconds
// before the message that supposedly seats it.  AgentSit is what a
// viewer sends when its OWN autopilot has finished walking the avatar
// over; the simulator has already reparented by then.  So nothing here
// sends it.
//
// # The answer is a reparenting
//
// AvatarSitResponse carries where the seat is, and the ObjectUpdate that
// follows is the fact: this avatar's own update comes back with ParentID
// set to the seat's local id, and a position that is now an offset from
// the object rather than a place in the region.  That is what Sit waits
// for.  It is what the simulator itself believes, it arrives whether or
// not the response did, and this package already keeps parents for
// everything else it hears about.
//
// A sit also MOVES the avatar, up to about ten metres, over whatever is
// in the way -- a box seven metres off seated the avatar as readily as
// one half a metre away, and standing up left it six metres from where
// it started.  There is no walk on the wire; the autopilot flag in the
// response is an instruction to the viewer to animate a journey it has
// already missed.  So a caller that knew where the avatar was should
// treat that as stale.
//
// # A refusal is an AlertMessage, and it arrives at once
//
//	sit on an object eleven metres away
//	  0.109  Alert "No room to sit here, try another spot."
//
//	sit on a uuid that is not an object at all
//	  0.090  Alert "Try moving closer.  Can't sit on object because
//	                it is not in the same region as you."
//
// Both in about a tenth of a second, both ending in a NUL byte that has
// to be trimmed.  ErrSitRefused carries the text as it came: the second
// one is misleading -- the id was not in another region, it was not an
// object at all -- but it is the only detail somebody could act on, and
// a client that translated it into "no such object here" would have
// thrown that away.
//
// So a sit has three outcomes and they are three different things:
// seated, refused in the grid's own words, and heard nothing at all
// before the timeout.  The last is not a refusal and must not read like
// one, because the request may well have taken effect unseen.
//
// A caution about the middle one.  AlertMessage is a general channel and
// something else may be alerting while a sit is in flight -- a script
// somewhere, an estate message, anything.  An alert that arrives inside
// the window is treated as the answer, which is right nearly always and
// wrong occasionally.  It is arranged so that the wrongness is a
// spurious refusal rather than a false success: the reparenting is
// checked first, so an alert can only be believed when nothing else has
// happened.
//
// # The ground is a different mechanism entirely
//
// There is no message for it.  It is a control flag on AgentUpdate --
// AGENT_CONTROL_SIT_ON_GROUND -- and standing is another,
// AGENT_CONTROL_STAND_UP.  Both are edge triggered: one update carrying
// the flag was enough, and the session's own presence update a second
// later, carrying no flags, undid neither.  The update itself is built
// by whoever owns the camera, which is never a client; see
// Backend.Control and agent.Control.
//
// Edge triggered does not mean once is enough, which cost a live run to
// find out: a flag that arrives while the avatar is still settling into
// the last thing it was told to do is dropped without a word.  So these
// calls hold the flag the way a viewer holds a key rather than sending
// it and hoping; see controlUntil, which is where the measurement is.
//
// A ground sit then produces NO reply and NO reparenting:
//
//	0.009  AgentUpdate SIT_ON_GROUND -->
//	0.144  <-- AvatarAnimation  1a2bd58e-...  (sit on ground)
//
// The animation list is the whole of the evidence, which is why these
// calls borrow a subscription to it; see borrowAnimations for what that
// costs and why it is given back.  Standing up is the same flag
// business, and from an object sit it also un-parents: 126 milliseconds,
// parent back to zero, absolute position again.
//
// See doc/history/sit.md, where all of this was written down as it was
// measured.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
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
	target := o.Local
	if target == 0 {
		target = w.locals[o.ID]
	}
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
// An avatar sitting on something is PARENTED to it, and the parent is
// in the region's description of the objects in it.  So the question is
// answered by looking this avatar up among them and reading what it
// says its parent is -- and then looking that local id up in the same
// listing to name it.
//
// It used to be answered from what this session had been told instead,
// which was right for a client that was connected when the sit
// happened and wrong for every other one.  A freshly attached client
// has been told nothing: an avatar is described when it arrives and
// when it moves, and one sitting still since before we attached has
// done neither.  So a shell that attached a minute ago reported an
// avatar that had been sitting for an hour as standing, with no way to
// tell that from the truth.
//
// # The ground half is still only as good as what we heard
//
// A ground sit is not a parent -- there is nothing to be parented to --
// and what says it is happening is the animation.  This session is told
// about animations only while it holds that subscription, which it does
// only while one of the calls above is waiting.  So a ground sit this
// session performed is reported, and one performed by a viewer or by
// another client while this session was not listening is not, and reads
// as standing.  Nothing here can close that gap without paying for the
// animations of every avatar in range for ever; the daemon already
// knows the answer, and asking it is a question for another day.
func (w *Session) Seat(ctx context.Context) (*Seat, error) {
	w.mu.Lock()
	local := w.seatLocal()
	ground := groundSitting(w.anims)
	on, offset := w.sitOn, w.sitOffset
	w.mu.Unlock()

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
// # Once is not enough, measured
//
// The flag is edge triggered -- one update carrying it did it, every
// time it was measured in isolation -- and that made a single send look
// sufficient.  It is not, and the case that shows it took a while to
// find:
//
//	sit on an object          seated,   101ms
//	stand                     stood,    101ms
//	sit on the ground         seated,   101ms
//	stand                     IGNORED, and the avatar was still
//	                          sitting twenty-three seconds later
//
// Each step went out as the one before was confirmed, so the last flag
// left about a tenth of a second after the ground sit landed.  The trace
// says slgod sent it: ControlFlags 65536, on the wire, acknowledged.
// The simulator dropped it.  Leaving three seconds before the last step
// -- and changing nothing else -- makes the same sequence work every
// time.
//
// What the sequence has that a ground sit and a stand on their own do
// not is the stand before it.  The reading that fits: the animation
// saying the ground sit has begun is not the same as the avatar having
// finished sitting down, and an avatar that was mid-way through standing
// up off an object when it was told to sit takes longer to get there.  A
// stand that arrives inside that window is swallowed.
//
// So this does what a viewer does, which is the part the one-shot got
// wrong: a viewer does not send a stand, it HOLDS the key, and an update
// carrying the flag goes out every frame until the avatar is up.  Half a
// second apart rather than every frame is enough to turn a swallowed
// flag into a wait nobody notices, and resending is free -- an edge
// triggered flag that has already been obeyed asks for something that
// has already happened.
//
// It also settles the other race for nothing: a borrowed subscription
// that had not been applied when the first flag went out will be there
// for the second.
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
// # Why it is borrowed rather than kept
//
// AvatarAnimation is the only evidence a ground sit or a stand from one
// ever produced, and it is expensive to keep: it arrives for every
// avatar in range, in full, about every three seconds.  Putting it in
// Subscriptions would charge that to slrun and slbench for ever,
// for something neither of them will ever read -- the same objection
// that made neighbouring circuits an option rather than a default.
//
// It really is given back.  A subscription is a set held per client
// stream in the daemon, and Watcher.Unwatch subtracts from it, so the
// cost stops when the command does rather than merely appearing to; and
// two commands overlapping do not take it from each other, because the
// count is what decides, not the last one to finish.  A session that
// named AvatarAnimation itself when it attached keeps it regardless --
// see Hosted.Unwatch.  A backend that is not a Watcher relays everything
// already, which is what a direct session does, so there is nothing to
// take out and nothing to give back.
//
// # The one race, and why it heals
//
// Watch travels on the client's stream and the daemon reads that stream
// in order, so a name asked for here is in force before anything sent
// afterwards ON THE STREAM.  A ground sit is not sent on the stream: it
// is Control, a unary call the daemon may take up on another goroutine,
// so in principle the flag could go out and the animation come back
// before the subscription had been applied.
//
// What makes that survivable is the resending.  The list is sent in
// full about every three seconds whether anything changed or not, so a
// subscription that arrived a moment late still hears the sit -- one
// resend later, well inside DefaultSitTimeout.  Losing the race costs a
// wait, not an answer.
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
