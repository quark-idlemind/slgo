package sl

// Scripts asking to act on this avatar's behalf.
//
// llRequestPermissions makes the simulator send a ScriptQuestion, and
// nothing happens until it is answered: the script's
// run_time_permissions never fires and the call it was waiting to make
// keeps failing.  Answering is one message back with the bits being
// granted.
//
// Two things the message names hide.  There is no ScriptAnswerNo:
// refusing is the same message with no bits set, which is what Deny
// sends.  And the field is a mask rather than a yes, so a request for
// three things can be answered with one of them -- a viewer never does
// that, because its dialog has one accept button, but the protocol has
// always allowed it and the simulator honours it.
//
// Nothing here grants anything on its own.  Some of these bits hand
// over real authority -- PermissionDebit spends money, PermissionTeleport
// moves the avatar, PermissionTakeControls reads the keyboard -- so the
// caller says which ones, every time, and everything unnamed is
// withheld.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// Perms is the permission mask, with the values LSL uses.
type Perms int32

// The bits, from the simulator's own syntax document.  The four the
// grid still describes as "not yet implemented" are here because they
// can arrive in a request, not because granting them does anything.
const (
	PermissionDebit                Perms = 0x00002 // spend the avatar's money
	PermissionTakeControls         Perms = 0x00004 // read and consume the movement keys
	PermissionRemapControls        Perms = 0x00008 // not implemented
	PermissionTriggerAnimation     Perms = 0x00010 // play animations on the avatar
	PermissionAttach               Perms = 0x00020 // attach itself to the avatar
	PermissionReleaseOwnership     Perms = 0x00040 // not implemented
	PermissionChangeLinks          Perms = 0x00080 // link and unlink objects
	PermissionChangeJoints         Perms = 0x00100 // not implemented
	PermissionChangePermissions    Perms = 0x00200 // not implemented
	PermissionTrackCamera          Perms = 0x00400 // read where the camera is
	PermissionControlCamera        Perms = 0x00800 // move the camera
	PermissionTeleport             Perms = 0x01000 // teleport the avatar
	PermissionExperience           Perms = 0x02000 // act under an experience
	PermissionSilentEstateManage   Perms = 0x04000 // change the estate without saying so
	PermissionOverrideAnimations   Perms = 0x08000 // replace the default animations
	PermissionReturnObjects        Perms = 0x10000 // return objects
	PermissionPrivilegedLandAccess Perms = 0x80000 // parcel controls
)

var permNames = []struct {
	bit  Perms
	name string
}{
	{PermissionDebit, "debit"},
	{PermissionTakeControls, "take controls"},
	{PermissionRemapControls, "remap controls"},
	{PermissionTriggerAnimation, "trigger animation"},
	{PermissionAttach, "attach"},
	{PermissionReleaseOwnership, "release ownership"},
	{PermissionChangeLinks, "change links"},
	{PermissionChangeJoints, "change joints"},
	{PermissionChangePermissions, "change permissions"},
	{PermissionTrackCamera, "track camera"},
	{PermissionControlCamera, "control camera"},
	{PermissionTeleport, "teleport"},
	{PermissionExperience, "experience"},
	{PermissionSilentEstateManage, "silent estate management"},
	{PermissionOverrideAnimations, "override animations"},
	{PermissionReturnObjects, "return objects"},
	{PermissionPrivilegedLandAccess, "privileged land access"},
}

// Has reports whether every bit in want is set.
func (p Perms) Has(want Perms) bool { return p&want == want }

// Any reports whether any bit in want is set, which is the question to
// ask about a set of things worth refusing over.
func (p Perms) Any(want Perms) bool { return p&want != 0 }

// String names the bits, so a log line says what was asked for rather
// than printing a number nobody can read.
func (p Perms) String() string {
	if p == 0 {
		return "nothing"
	}
	var out []string
	rest := p
	for _, n := range permNames {
		if p&n.bit != 0 {
			out = append(out, n.name)
			rest &^= n.bit
		}
	}
	if rest != 0 {
		out = append(out, fmt.Sprintf("unknown 0x%x", int32(rest)))
	}
	return strings.Join(out, ", ")
}

// Permission is a script asking to act on this avatar's behalf.
//
// It is answered with Grant or Deny.  A request nobody answers stays
// pending: the script waits, and whatever it wanted to do keeps
// failing, which is a quieter failure than a refusal and harder to
// diagnose from the script's end.
type Permission struct {
	At time.Time

	// Object is the object the script is inside, and Item the script
	// within it.  Both go back in the answer; the pair is what the
	// simulator matches against the pending request.
	Object msg.UUID
	Item   msg.UUID

	// ObjectName and OwnerName are what to show somebody being asked
	// to decide.  The owner arrives as a name rather than an id.
	ObjectName string
	OwnerName  string

	// Experience is set when the request comes through one, in which
	// case granting it covers every script in that experience rather
	// than this one alone.
	Experience msg.UUID

	// Wants is what was asked for.
	Wants Perms

	w *Session
}

func (q *Permission) String() string {
	return fmt.Sprintf("%s (owned by %s) wants %s", SenderObject.Label(q.ObjectName), q.OwnerName, q.Wants)
}

// Grant answers with the bits named and no others.
//
// Anything in mask that was not asked for is dropped rather than sent:
// the simulator has nothing pending to match it against, and a caller
// who names a bit the script never wanted should not be told that it
// granted one.  Granting nothing at all is a refusal, and Deny says
// that more plainly.
func (q *Permission) Grant(ctx context.Context, mask Perms) error {
	return q.answer(ctx, mask&q.Wants)
}

// GrantAll answers with everything that was asked for.
//
// Worth reading the request first: this is the call that can hand over
// the avatar's money and its movement.  The profile's rules refuse a
// grant of PermissionDebit whatever they say, since a script given it
// can take any amount; see doc/money.md#how-each-is-checked.
func (q *Permission) GrantAll(ctx context.Context) error {
	return q.answer(ctx, q.Wants)
}

// Deny refuses the whole request.
//
// The simulator is told, rather than the request being left to hang,
// so the script's llRequestPermissions can stop waiting.
func (q *Permission) Deny(ctx context.Context) error { return q.answer(ctx, 0) }

func (q *Permission) answer(ctx context.Context, granted Perms) error {
	if q.w == nil {
		return fmt.Errorf("sl: this permission request has no session to answer on")
	}
	m := &msg.ScriptAnswerYes{}
	m.AgentData.AgentID, m.AgentData.SessionID = q.w.agentBlock()
	m.Data.TaskID = q.Object
	m.Data.ItemID = q.Item
	m.Data.Questions = int32(granted)
	if err := q.w.Send(ctx, m); err != nil {
		return err
	}
	// Answered is answered, whichever bits went.  Asked is what a
	// caller reads to find what still needs deciding, and a request
	// that has had its one message back does not.  Granting part of
	// what was asked is no exception: the rest is withheld rather than
	// held over, since nothing here will send a second answer to the
	// same request.
	//
	// Only after the send, and only on the way out: a request whose
	// answer never left the house is still waiting.  Same shape as
	// forgetOffer.
	q.w.forgetPermission(q)
	return nil
}

// forgetPermission drops a request that has been answered.
//
// By identity rather than by the object and script it names.  The
// simulator files its pending request under that pair, but nothing
// stops a script asking a second time, and matching on the pair would
// then forget a request nobody has answered along with the one they
// did.
func (w *Session) forgetPermission(q *Permission) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.forgetPermissionLocked(q)
}

// forgetPermissionLocked is forgetPermission with mu held.
func (w *Session) forgetPermissionLocked(q *Permission) {
	for i, x := range w.asked {
		if x == q {
			w.asked = append(w.asked[:i], w.asked[i+1:]...)
			return
		}
	}
}

// permission builds one and hands it to whoever is listening.
func (w *Session) permission(m *msg.ScriptQuestion) {
	q := &Permission{
		At:         time.Now(),
		Object:     m.Data.TaskID,
		Item:       m.Data.ItemID,
		ObjectName: trimNul(m.Data.ObjectName),
		OwnerName:  trimNul(m.Data.ObjectOwner),
		Experience: m.Experience.ExperienceID,
		Wants:      Perms(m.Data.Questions),
		w:          w,
	}

	w.mu.Lock()
	gone := w.pruneAskedLocked(q.At, 1)
	w.asked = append(w.asked, q)
	subs := make([]*permSub, 0, len(w.permSubs))
	for _, s := range w.permSubs {
		subs = append(subs, s)
	}
	told := w.handledForLocked(gone)
	w.mu.Unlock()

	tellHandled(told, gone)
	for _, s := range subs {
		select {
		case s.ch <- q:
		default:
			s.dropped.Add(1)
		}
	}
}

// Asked returns the requests still waiting for an answer, oldest
// first.
//
// One that has been answered is gone from here, because the only thing
// this list is for is deciding what still needs deciding -- a listing
// that kept them would count a script twice over and offer to answer
// it again.  It is not a history: nothing keeps one of these after
// Grant, GrantAll or Deny has sent.  Nor after UnansweredFor with no
// answer, or once it is the oldest of more than MaxUnanswered waiting;
// see OnHandled.
func (w *Session) Asked() []*Permission {
	w.mu.Lock()
	gone := w.pruneAskedLocked(time.Now(), 0)
	out := append([]*Permission(nil), w.asked...)
	told := w.handledForLocked(gone)
	w.mu.Unlock()

	tellHandled(told, gone)
	return out
}

// pruneAskedLocked forgets the requests that are overdue, leaving room
// for room more, and says what went for OnHandled.  Called with mu held.
func (w *Session) pruneAskedLocked(now time.Time, room int) []Handled {
	drop, how := overdue(w.asked, func(q *Permission) time.Time { return q.At }, now, room)
	var out []Handled
	for i, q := range drop {
		w.forgetPermissionLocked(q)
		out = append(out, Handled{
			What: "the request from " + SenderObject.Label(orID(q.ObjectName, q.Object)) + " for " + q.Wants.String(),
			How:  how[i], By: droppedBy, At: now,
		})
	}
	return out
}

// Permissions returns a channel of scripts asking for permission, and
// it is closed when StopPermissions is called or the connection ends.
//
// The same shape as Chat: buffered to depth, and a subscription whose
// buffer is full has requests dropped rather than holding up the
// relay.  Requests are rare enough that a full buffer means nobody is
// reading; PermissionsDropped says how many went that way.
func (w *Session) Permissions(depth int) <-chan *Permission {
	if depth <= 0 {
		depth = DefaultPermissionDepth
	}
	sub := &permSub{ch: make(chan *Permission, depth)}
	if !w.addSub(func() { w.permSubs[sub.ch] = sub }) {
		close(sub.ch)
	}
	return sub.ch
}

// DefaultPermissionDepth is the buffer a subscription gets when none is
// asked for.
const DefaultPermissionDepth = 16

// StopPermissions closes a subscription, and returns once it is closed.
func (w *Session) StopPermissions(ch <-chan *Permission) {
	w.onReader(func() {
		if s := w.permSubs[ch]; s != nil {
			delete(w.permSubs, ch)
			close(s.ch)
		}
	})
}

// PermissionsDropped is how many requests a subscription missed
// because its buffer was full.
func (w *Session) PermissionsDropped(ch <-chan *Permission) uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := w.permSubs[ch]; s != nil {
		return s.dropped.Load()
	}
	return 0
}
