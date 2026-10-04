package sl

// Moving the avatar.
//
// # The two kinds of teleport, and who does the work
//
// A teleport WITHIN the region is a small thing: the simulator already
// has the avatar, so it moves it and says TeleportLocal.  Nothing about
// the session changes -- same circuit, same capabilities, same session
// id -- so a client can ask for it and the daemon underneath need not
// know it happened.
//
// A teleport to ANOTHER region is a different act.  The destination is a
// different simulator on a different address, so the circuit has to be
// opened there, that region's capabilities fetched and the old ones
// dropped, and the event queue closed against the region left and
// started against the one arrived in.  All of that is the daemon's, and
// it is not asked for: the grid announces it with a TeleportFinish and
// the daemon follows.  What this file does is ask for the teleport and
// then find out which of the four things below happened.
//
// # Waiting is the whole of the difficulty
//
// Measured on Agni: the request is answered on the circuit with
// TeleportStart and a couple of TeleportProgress lines within 200ms, and
// then, at about 300ms, with a TeleportFinish on the EVENT QUEUE rather
// than the circuit -- as is TeleportFailed, which the message template
// does not say.  None of those means the avatar is anywhere yet.  The
// finish says the simulator being left has let go, so it is the middle
// of a teleport and not the end of one, and the end is this session
// answering with the region the finish named.  That is what is waited
// for here, and it is deliberately not "the daemon relayed the finish,
// so it must have moved by now": the daemon does act on the event before
// relaying it, but that ordering belongs to how it reads its queue, and
// a contract resting on it would break without a word.
//
// Nothing in either answer says which request provoked it, and the
// daemon relays by name to every client subscribed, so two clients
// teleporting one avatar at once will read each other's answers.  Unlike
// the same trouble in worldmap.go this is nearly harmless, because there
// is one avatar: a finish another client's request provoked is still
// this session going somewhere, and it is still followed by waiting to
// be there.  What it can do is report another client's refusal as this
// one's.
//
// The three ways it does not work are all real and all measured, and
// they are three errors rather than one because there is nothing in
// common to do about them.  The grid can refuse, which it does in two
// voices at once -- a machine key and a sentence, sometimes the same
// string and sometimes not.  The daemon can fail to follow, which ends
// the session it is holding.  And the grid can answer with nothing
// whatever, for ever, which is what a second teleport gets once this
// avatar has already been handed off; a timeout is the only way to learn
// it.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// Why a teleport was made, in the bits RegionChange.TeleportFlags
// carries: the viewer's TELEPORT_FLAGS_*.  See agent.TeleportViaHome and
// the others for where each is from.
const (
	TeleportSetHomeToTarget = agent.TeleportSetHomeToTarget
	TeleportSetLastToTarget = agent.TeleportSetLastToTarget
	TeleportViaLure         = agent.TeleportViaLure
	TeleportViaLandmark     = agent.TeleportViaLandmark
	TeleportViaLocation     = agent.TeleportViaLocation
	TeleportViaHome         = agent.TeleportViaHome
	TeleportViaTelehub      = agent.TeleportViaTelehub
	TeleportViaLogin        = agent.TeleportViaLogin
	TeleportViaGodlikeLure  = agent.TeleportViaGodlikeLure
	TeleportGodlike         = agent.TeleportGodlike
	Teleport911             = agent.Teleport911
	TeleportDisableCancel   = agent.TeleportDisableCancel
	TeleportViaRegionID     = agent.TeleportViaRegionID
	TeleportIsFlying        = agent.TeleportIsFlying
	TeleportShowResetHome   = agent.TeleportShowResetHome
	TeleportForceRedirect   = agent.TeleportForceRedirect
	TeleportViaGlobalCoords = agent.TeleportViaGlobalCoords
	TeleportWithinRegion    = agent.TeleportWithinRegion
)

// ErrTeleportRefused is the grid saying no: the region does not exist,
// or will not have this avatar.  The error is a *TeleportRefusal, which
// carries both halves of what it said; errors.Is finds this inside it
// and errors.As finds the refusal.
var ErrTeleportRefused = errors.New("sl: the grid refused the teleport")

// TeleportRefusal is a teleport the grid refused, with what it said.
//
// It is a type so that the grid's key can be read as a key.  Before it
// was one, the key was folded into the error's sentence and the only
// way to ask "was that CouldntTPCloser" was to search the sentence for
// the word -- which every caller had to know to do, and which would
// have matched a sentence that merely mentioned it.  Now a caller asks
//
//	var r *sl.TeleportRefusal
//	if errors.As(err, &r) && r.Key == agent.KeyCouldntTPCloser { ... }
//
// and the words are left for a person to read.
type TeleportRefusal struct {
	// What names the teleport, as the caller would call it: "the
	// teleport to grid square (995, 997)", "going home".
	What string

	// Key is the grid's name for the refusal, and the one of the two a
	// program should act on.  Empty when the grid sent none.  The keys
	// this tree has met are named in the agent package, which is where
	// the daemon reads them too.
	Key agent.RefusalKey

	// Reason is what the grid said for a person to read.  Sometimes it
	// is a sentence and sometimes it is the key again; see
	// agent.Refusal.
	Reason string
}

// Error renders the two voices without pretending either is the other,
// and says the same string once when they agree.
func (e *TeleportRefusal) Error() string {
	var said string
	key := string(e.Key)
	switch {
	case key == "" && e.Reason == "":
		said = "and would not say why"
	case key == "":
		said = strconv.Quote(e.Reason)
	case e.Reason == "" || e.Reason == key:
		said = key
	default:
		said = key + ": " + strconv.Quote(e.Reason)
	}
	return ErrTeleportRefused.Error() + ": " + e.What + ": " + said
}

// Unwrap is what makes errors.Is(err, ErrTeleportRefused) true of a
// refusal, which is how every caller written before this type existed
// still recognises one.
func (e *TeleportRefusal) Unwrap() error { return ErrTeleportRefused }

// refusedWith says whether err is the grid refusing a teleport with
// this key, wherever in a chain of wrapping the refusal is.
func refusedWith(err error, key agent.RefusalKey) bool {
	var r *TeleportRefusal
	return errors.As(err, &r) && r.Key == key
}

// ErrTeleportLost is the grid handing this avatar to another simulator
// and the session never turning up there.
//
// It means the daemon failed to follow, which it does not survive: a
// session pointed at a region the avatar has left looks healthy and is
// not, so the daemon ends it rather than keep it.  A session that
// reports this is very probably over.
var ErrTeleportLost = errors.New("sl: the teleport finished somewhere this session did not arrive")

// TeleportLocal moves the avatar to a position in the region it is
// already in, and waits for the simulator to agree that it moved.
//
// The confirmation matters.  A refused teleport -- somewhere the parcel
// will not have you, or a position outside the region -- produces no
// error and no reply, so a caller that did not check would carry on
// believing it had moved and every position it computed afterwards
// would be wrong.
func (w *Session) TeleportLocal(ctx context.Context, to msg.Vector3, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	where, err := w.Where(ctx)
	if err != nil {
		return err
	}
	if where.RegionHandle == 0 {
		return fmt.Errorf("sl: this session does not know which region it is in")
	}

	m := &msg.TeleportLocationRequest{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.Info.RegionHandle = where.RegionHandle
	m.Info.Position = to
	// Looking the way we are already looking.  The field is not
	// optional and a zero vector is not a direction.
	m.Info.LookAt = where.LookAt
	if m.Info.LookAt == (msg.Vector3{}) {
		m.Info.LookAt = msg.Vector3{X: 1}
	}
	if err := w.Send(ctx, m); err != nil {
		return err
	}

	// Near enough, rather than exact: the simulator stands the avatar
	// on whatever is under the point, so the height it settles at is
	// its choice and not ours.
	const near = 4.0 // metres
	return poll(ctx, timeout, 500*time.Millisecond, fmt.Sprintf("the avatar to arrive at %v; "+
		"a parcel that will not have this avatar refuses silently", to),
		func(ctx context.Context) (bool, error) {
			p, err := w.Where(ctx)
			if err != nil {
				return false, err
			}
			return dist2(p.Position, to) < near*near, nil
		})
}

// DefaultTeleportTimeout is how long a teleport to another region is
// given when a caller names no timeout.
//
// It is generous because it covers more than the grid's part.  The
// exchange up to TeleportFinish took 300ms when it was measured, and
// everything after it is the daemon's: a circuit at the new simulator, a
// capability fetch over HTTP, and a fresh event queue, any of which can
// be waiting on something slow.
//
// The value is not promised and may change in any release: refer to it by name.
const DefaultTeleportTimeout = 90 * time.Second

// Teleport takes the avatar to another region and waits until it is
// there, or says which of the three failures happened.
//
// The region is named by handle, which is what the protocol takes and
// what FindRegions turns a name into.  The position is where in that
// region to arrive; the middle of it at ground level is what a viewer
// uses, and the simulator will stand the avatar wherever it likes near
// the point asked for.
//
// The handle of the region the avatar is ALREADY in is not an error and
// not a circuit move: it is TeleportLocal, and that is what this does
// with it.  Nothing else would be honest -- there is one region and one
// simulator either way, and dialling a second circuit to the simulator
// on the other end of this one is a relog to where we are standing.
//
// Returning means the session is usable in the new region: the daemon
// has the circuit, the capabilities and the queue there.  What it does
// NOT mean is that anything a caller was holding is still good.  Local
// ids are the region's own numbering and the objects are somewhere
// else's, so a caller that teleports should treat what it knew as gone.
//
// This package throws away its own share of that when it is told the
// avatar has moved -- see region.go for what goes and what stays -- but
// being told arrives on the relay and this call does not wait for it,
// so a caller with state of its own should subscribe to RegionChanges
// rather than take the return as the moment.
//
// A zero timeout is DefaultTeleportTimeout.
func (w *Session) Teleport(ctx context.Context, handle uint64, to msg.Vector3, timeout time.Duration) error {
	if handle == 0 {
		return fmt.Errorf("sl: Teleport needs the handle of a region; " +
			"FindRegions turns a name into one")
	}
	if timeout == 0 {
		timeout = DefaultTeleportTimeout
	}

	where, err := w.Where(ctx)
	if err != nil {
		return err
	}
	if where.RegionHandle == handle {
		return w.TeleportLocal(ctx, to, timeout)
	}

	m := &msg.TeleportLocationRequest{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.Info.RegionHandle = handle
	m.Info.Position = to
	// Facing the way we already are, for TeleportLocal's reason: the
	// field is not optional and a zero vector is not a direction.
	m.Info.LookAt = where.LookAt
	if m.Info.LookAt == (msg.Vector3{}) {
		m.Info.LookAt = msg.Vector3{X: 1}
	}

	// Watching before asking.  The finish arrived 298 milliseconds after
	// the request when this was measured, which is a race that a watcher
	// registered afterwards loses often enough to matter and silently
	// when it does.
	// A TeleportLocal does not answer this: it was asked to leave the
	// region, so a move inside it is somebody else's teleport arriving
	// on a relay that carries every client's.
	watch := w.watchTeleport(where.RegionHandle, false)
	defer watch.stop()

	if err := w.Send(ctx, m); err != nil {
		return err
	}
	x, y := msg.GridCoords(handle)
	return watch.arrive(ctx, fmt.Sprintf("the teleport to grid square (%d, %d)", x, y), timeout)
}

// teleportAnswer is what the grid said about a teleport that was asked
// for: one of a finish, a failure, or a move inside this region.
type teleportAnswer struct {
	// handle is the region a TeleportFinish named, and is the only
	// place a lure's destination is ever named.  Zero means the finish
	// carried none this package could read.
	handle uint64

	// local says a TeleportLocal arrived, which is the whole answer: the
	// avatar went nowhere the session has to follow.
	local bool

	// failed says a TeleportFailed arrived, and refusal is its two
	// voices.  Measured on Agni, a handle that is no region gives
	// "no_host" for both, while a region that refuses gives
	// "MustHaveVIPStatus" as the key and "You must be a premium or vip
	// subscriber to enter this region." as the reason.  So neither is
	// the other, and both are carried.
	failed  bool
	refusal agent.Refusal
}

// teleportWatch is a teleport being waited for.
//
// It exists because the two halves cannot be one call: the watch has to
// be registered before the request goes out, and the request is
// different for a teleport and for an accepted lure.
type teleportWatch struct {
	w *Session

	// from is the region the avatar was in when the watch began, which
	// is what tells arrival from having gone nowhere.
	from uint64

	stop func()
	done chan struct{}

	// answer is the first thing the grid said, under w.mu.  The first
	// rather than the last: a teleport is answered once, and anything
	// after it belongs to whatever happens next.
	answer *teleportAnswer
}

// watchTeleport starts listening for the answer to a teleport that has
// not been asked for yet.
//
// local says whether a TeleportLocal counts as an answer.  It does for a
// lure, which may well be to a spot a few metres away and is answered
// with one of those and nothing else; it does not for a teleport that
// asked to leave the region, since a move inside it cannot be what that
// was answered with.
//
// The closure runs with the session's lock held -- see onTeleport -- so
// what it writes is written under the lock arrive reads it under.
func (w *Session) watchTeleport(from uint64, local bool) *teleportWatch {
	t := &teleportWatch{w: w, from: from, done: make(chan struct{}, 1)}
	t.stop = w.onTeleport(func(a *teleportAnswer) {
		if t.answer != nil || (a.local && !local) {
			return
		}
		t.answer = a
		select {
		case t.done <- struct{}{}:
		default:
		}
	})
	return t
}

// arrive waits for the grid to answer and then for the session to be in
// the region the answer named.  what names the teleport, for the errors.
func (t *teleportWatch) arrive(ctx context.Context, what string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-t.done:
	case <-timer.C:
		// The third failure, and the one with nothing to see.  Once this
		// avatar has been handed off, the simulator it left answers no
		// further teleport request at all -- no start, no progress, no
		// failure, ever -- so a request that vanishes is the ordinary
		// shape of asking twice and not a network fault to retry.
		return fmt.Errorf("%w: %s was answered with nothing at all, neither a "+
			"finish nor a refusal; a teleport asked for while one is already "+
			"under way is answered with silence and never anything else",
			ErrTimeout, what)
	case <-ctx.Done():
		return ctx.Err()
	}

	t.w.mu.Lock()
	a := t.answer
	t.w.mu.Unlock()

	switch {
	case a.failed:
		return &TeleportRefusal{What: what, Key: a.refusal.Key, Reason: a.refusal.Reason}
	case a.local:
		// The destination turned out to be in the region already
		// occupied, so there is nothing to follow and nothing to wait
		// for.  This is the ordinary answer to a lure from somebody
		// standing nearby.
		return nil
	}

	// The finish is the middle of a teleport.  What says the end of it
	// is this session answering with the region the finish named, so
	// that is polled for, the way TeleportLocal polls for a position.
	arrived := func(p *Presence) bool {
		if a.handle != 0 {
			return p.RegionHandle == a.handle
		}
		// A finish whose handle did not survive the relay.  Somewhere
		// else is all that can be waited for then, and it is better than
		// reporting a teleport that worked as lost.
		return p.RegionHandle != 0 && p.RegionHandle != t.from
	}

	for {
		p, err := t.w.Where(ctx)
		if err == nil && arrived(p) {
			return nil
		}
		if time.Now().After(deadline) {
			x, y := msg.GridCoords(a.handle)
			return fmt.Errorf("%w: %s: the grid handed this avatar to grid square "+
				"(%d, %d) and the daemon did not follow it there, so the session "+
				"it is holding is over or ending: %w",
				ErrTeleportLost, what, x, y, ErrTimeout)
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// onTeleport registers a watcher for what the grid says about a
// teleport, in the style of onMapBlocks and onAvatarReply, and for their
// reason: it is called with mu held, which is what makes the answer safe
// to read from the goroutine that is waiting.
func (w *Session) onTeleport(fn func(*teleportAnswer)) (stop func()) {
	w.mu.Lock()
	w.teleportFns = append(w.teleportFns, fn)
	i := len(w.teleportFns) - 1
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		if i < len(w.teleportFns) {
			w.teleportFns[i] = nil
		}
		w.mu.Unlock()
	}
}

// teleportAnswered hands one answer to whoever asked for the teleport.
//
// Delivered rather than stored.  A teleport is an act with a moment
// attached, and an answer kept from the last one would be handed to the
// next as though it were about that.
func (w *Session) teleportAnswered(a *teleportAnswer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, fn := range w.teleportFns {
		if fn != nil {
			fn(a)
		}
	}
}

// teleportFinishEvent reads the TeleportFinish that comes off the event
// queue, which on Second Life is where it comes from.
//
// Only the handle is read.  Everything else in the block -- the address,
// the port, the seed capability -- is for whoever has to move the
// circuit, and that is the daemon; a client cannot use any of it and
// should not be holding it.  The handle is LLSD binary, eight bytes big
// endian, which llsd.Int decodes.
func (w *Session) teleportFinishEvent(m map[string]any) {
	info := llsdBlocks(m, "Info")
	if len(info) == 0 {
		return
	}
	w.teleportAnswered(&teleportAnswer{handle: uint64(llsd.Int(info[0], "RegionHandle"))})
}

// teleportFailedEvent reads the refusal, which arrives on the event
// queue as well.
//
// Both blocks are read and neither is preferred.  Measured:
//
//	{"AlertInfo":[{"ExtraParams":"","Message":"no_host"}],
//	 "Info":[{"AgentID":"...","Reason":"no_host"}]}
//	{"AlertInfo":[{"ExtraParams":"","Message":"MustHaveVIPStatus"}],
//	 "Info":[{"AgentID":"...","Reason":"You must be a premium or vip
//	  subscriber to enter this region."}]}
//
// so Message is a key a program could switch on and Reason is sometimes
// that same key and sometimes a sentence for a person.  The reading is
// agent.ReadTeleportFailedEvent, which the daemon uses as well, so that
// the two agree on which field is the key.
func (w *Session) teleportFailedEvent(m map[string]any) {
	w.teleportAnswered(&teleportAnswer{failed: true, refusal: agent.ReadTeleportFailedEvent(m)})
}

// dist2 is the SQUARED distance: comparing squares avoids a square
// root, and the name says so, because comparing a squared distance
// against a plain one is a bug that looks right.
func dist2(a, b msg.Vector3) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return dx*dx + dy*dy + dz*dz
}
