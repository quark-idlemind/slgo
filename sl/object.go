package sl

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// Object is something in the region.
//
// Both identifiers are needed and neither is enough.  Capabilities and
// property requests take the id; most of the older messages take the
// local id, which is the region's own numbering: the next region hands
// the same numbers out to its own objects, and a region that restarts
// numbers everything afresh.
//
// So an Object found here remembers which region, and which run of it,
// its local id was handed out in, and every call that sends a local id
// checks that first.  If the avatar has moved since, or the session has
// been re-established, the object is looked up again by its id in the
// region the avatar is in now, Local is brought up to date, and the new
// number is what goes out.  One that is not there is refused with
// ErrNotHere, and the old number is never sent.
//
// An Object built by hand has no region, and is always looked up before
// its local id is sent; so is one found through another Session.  That
// makes the id what identifies it, and Local only a hint.
type Object struct {
	ID    msg.UUID
	Local uint32
	Name  string

	// from is the visit Local was handed out in; see Session.local.
	from visit
}

// ErrNotHere is an object that is not in the region the avatar is in
// now, as far as that region has described it, so there is no local id
// to send for it.  A lookup that cannot settle which region it answered
// for is refused with it too, and says so in its own words.
var ErrNotHere = errors.New("not in this region")

func (o Object) String() string {
	if o.Name != "" {
		return fmt.Sprintf("%q %s (local %d)", o.Name, o.ID, o.Local)
	}
	return fmt.Sprintf("%s (local %d)", o.ID, o.Local)
}

// RezOptions tune what gets rezzed.
type RezOptions struct {
	At    msg.Vector3
	Scale msg.Vector3 // zero means half a metre cubed
}

// Rez creates a single prim and returns it, having confirmed we own it.
// It is made in the avatar's active group, as a viewer makes one.  A
// caller that gives up is handed the prim with its error if one last
// look finds it.
//
// Build does this and more; this is the short way to get one prim when
// none of the rest is wanted.
func (w *Session) Rez(ctx context.Context, opt RezOptions) (*Object, error) {
	if opt.Scale == (msg.Vector3{}) {
		opt.Scale = msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}
	}
	p, err := w.Where(ctx)
	if err != nil {
		return nil, err
	}
	if err := inRange(p, opt.At); err != nil {
		return nil, err
	}
	return w.rezAt(ctx, opt.At, opt.Scale, msg.Quaternion{})
}

// SetName renames an object and reads the name back.
//
// ObjectName has no reply, so the only way to know it took is to ask.
func (w *Session) SetName(ctx context.Context, o *Object, name string) error {
	local, err := w.local(ctx, o)
	if err != nil {
		return err
	}
	m := &msg.ObjectName{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData = []msg.ObjectName_ObjectData{
		{LocalID: local, Name: append([]byte(name), 0)},
	}
	if err := w.Send(ctx, m); err != nil {
		return err
	}

	// Clear what we think the name is, so a stale answer cannot pass
	// for a fresh one.  This is objectNames, where the names of objects
	// are: names is the avatar cache, and clearing that one left the
	// old object name in place to be read straight back, so renaming a
	// freshly rezzed prim always reported that it was still "Object".
	w.mu.Lock()
	delete(w.objectNames, o.ID)
	w.mu.Unlock()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if err := w.Send(ctx, w.familyRequest(o.ID)); err != nil {
			return err
		}
		var got string
		// A timeout here is the ordinary case and means ask again --
		// the simulator answers a family request when it feels like
		// it.  Anything else is the caller giving up, and has to come
		// back as that.  Discarding it altogether made a cancelled
		// context spin this loop flat out for the full twenty seconds
		// and then report a timeout, which is neither true nor quick.
		err := w.await(ctx, 3*time.Second, "the new name", func() bool {
			got = w.objectNames[o.ID]
			return got != ""
		})
		if err != nil && !errors.Is(err, ErrTimeout) {
			return err
		}
		if got == name {
			o.Name = name
			return nil
		}
		if got != "" {
			return fmt.Errorf("sl: renaming %s to %q left it named %q", o.ID, name, got)
		}
	}
	return fmt.Errorf("%w: the name of %s after renaming it", ErrTimeout, o.ID)
}

func (w *Session) familyRequest(id msg.UUID) *msg.RequestObjectPropertiesFamily {
	m := &msg.RequestObjectPropertiesFamily{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData.ObjectID = id
	return m
}

// Properties describes an object.
type Properties struct {
	Object          msg.UUID
	Name            string
	Description     string
	Creator         msg.UUID
	Owner           msg.UUID
	Group           msg.UUID
	Created         time.Time
	BaseMask        uint32
	OwnerMask       uint32
	GroupMask       uint32
	EveryoneMask    uint32
	NextOwnerMask   uint32
	SaleType        uint8
	SalePrice       int32
	InventorySerial int16
}

// Properties selects an object and reads its full properties, which is
// where the permission masks live.
func (w *Session) Properties(ctx context.Context, o *Object, timeout time.Duration) (*Properties, error) {
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	got := make(chan *Properties, 1)
	stop := w.onProperties(func(p *Properties) {
		if p.Object == o.ID {
			select {
			case got <- p:
			default:
			}
		}
	})
	defer stop()

	local, err := w.local(ctx, o)
	if err != nil {
		return nil, err
	}
	if err := w.Send(ctx, w.selectMsg(local)); err != nil {
		return nil, err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case p := <-got:
		return p, nil
	case <-t.C:
		return nil, fmt.Errorf("%w: properties of %s", ErrTimeout, o.ID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *Session) selectMsg(locals ...uint32) *msg.ObjectSelect {
	m := &msg.ObjectSelect{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	for _, l := range locals {
		m.ObjectData = append(m.ObjectData, msg.ObjectSelect_ObjectData{ObjectLocalID: l})
	}
	return m
}

func (w *Session) deselectMsg(locals ...uint32) *msg.ObjectDeselect {
	m := &msg.ObjectDeselect{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	for _, l := range locals {
		m.ObjectData = append(m.ObjectData, msg.ObjectDeselect_ObjectData{ObjectLocalID: l})
	}
	return m
}

// Select tells the simulator we are editing these objects, which some
// operations require.
func (w *Session) Select(ctx context.Context, objs ...*Object) error {
	locals := make([]uint32, 0, len(objs))
	for _, o := range objs {
		l, err := w.local(ctx, o)
		if err != nil {
			return err
		}
		locals = append(locals, l)
	}
	return w.Send(ctx, w.selectMsg(locals...))
}

// Link joins objects into one, with root as the root, and waits until
// the simulator agrees they are linked.
func (w *Session) Link(ctx context.Context, root *Object, children ...*Object) error {
	if len(children) == 0 {
		return fmt.Errorf("sl: linking needs something to link")
	}
	all := append([]*Object{root}, children...)
	if err := w.Select(ctx, all...); err != nil {
		return err
	}
	// A link operates on the current selection, which the simulator
	// has to have taken in first.
	if err := w.Settle(ctx, 2*time.Second); err != nil {
		return err
	}

	m := &msg.ObjectLink{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	for _, o := range all {
		local, err := w.local(ctx, o)
		if err != nil {
			return err
		}
		m.ObjectData = append(m.ObjectData, msg.ObjectLink_ObjectData{ObjectLocalID: local})
	}
	if err := w.Send(ctx, m); err != nil {
		return err
	}

	// A link shows up as the children naming the root as their parent.
	return w.await(ctx, 20*time.Second, "the children to name the root as parent", func() bool {
		for _, c := range children {
			if w.parents[c.Local] != root.Local {
				return false
			}
		}
		return true
	})
}

// Unlink frees prims from the linkset they are in, and waits until the
// simulator agrees they are loose.
//
// The prims named are the ones being FREED, which is the other way
// round from Link.  A delink goes out as individuals and a link as
// roots, and the viewer says why where it does it: "Delink needs to
// send individuals so you can unlink a single object from a linked set"
// (llselectmgr.cpp:5477), against SEND_ONLY_ROOTS in sendLink just above
// (llselectmgr.cpp:5434).  So a whole linkset comes apart by naming
// every child of it, and one prim leaves by naming that prim; which of
// those was meant is the caller's to decide.
//
// The root of a linkset is not among them.  It has no parent to lose,
// so naming it frees nothing, and it is not something that can be
// waited for either: the confirmation here is a prim saying it has no
// parent, and a prim that never had one has no reason to be described
// again.  The viewer would send it, as part of the whole selection, and
// the simulator was measured not to mind its being left out.
//
// Nor does this change a freed prim's physics shape, as the viewer does
// on the way past: the viewer's step never reaches the wire, and this
// client does not know a prim's shape to change.
// Why: doc/linking.md
func (w *Session) Unlink(ctx context.Context, prims ...*Object) error {
	if len(prims) == 0 {
		return fmt.Errorf("sl: unlinking needs something to take apart")
	}
	if err := w.Select(ctx, prims...); err != nil {
		return err
	}
	// A delink operates on the current selection, the same way a link
	// does, and the simulator has to have taken it in first.
	if err := w.Settle(ctx, 2*time.Second); err != nil {
		return err
	}

	m := &msg.ObjectDelink{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	for _, o := range prims {
		local, err := w.local(ctx, o)
		if err != nil {
			return err
		}
		m.ObjectData = append(m.ObjectData, msg.ObjectDelink_ObjectData{ObjectLocalID: local})
	}
	if err := w.Send(ctx, m); err != nil {
		return err
	}

	// A delink shows up as the freed prims naming nobody as their
	// parent.  The entry has to be present as well as zero: parents is a
	// map, and a prim this session has never heard an update about reads
	// as zero already, which would pass for a confirmation before the
	// request had reached the region.
	return w.await(ctx, 20*time.Second, "the prims to say they have no parent", func() bool {
		for _, o := range prims {
			if p, ok := w.parents[o.Local]; !ok || p != 0 {
				return false
			}
		}
		return true
	})
}

// Parent reports the local id an object says is its parent, and
// whether the simulator has mentioned it at all.
func (w *Session) Parent(o *Object) (uint32, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	local := w.localNow(o)
	if local == 0 {
		return 0, false
	}
	p, ok := w.parents[local]
	return p, ok
}

// Destinations for DeRezObject, from the viewer's EDeRezDestination
// (llselectmgr.h:83).  The two that matter here sit either side of
// values that do quite different things -- 5 is the god inventory and 6
// is the trash -- so they are named rather than written where they are
// used.
const (
	// derezAcquireCopy takes a copy and tries to leave the original
	// standing.  "Take Copy" in the viewer sends this
	// (llviewermenu.cpp:6420).
	derezAcquireCopy = 1

	derezTakeIntoInventory = 4
	derezTrash             = 6
)

// Take moves an object into inventory and returns the item it became.
//
// Nothing answers a take over UDP: the item turns up in the folder
// over AIS some seconds later, so this polls the folder for something
// that was not there before rather than waiting for a message.
//
// The item is known by the object's name, which is o.Name if it has
// one.  An object handed in without a name is asked what it is called
// first, within the same timeout, and one whose name cannot be learned
// is not taken: every fresh prim is called "Object", and the first new
// item in a folder is not necessarily this one.
func (w *Session) Take(ctx context.Context, o *Object, folder msg.UUID, timeout time.Duration) (*Item, error) {
	it, _, err := w.derezToInventory(ctx, o, folder, derezTakeIntoInventory, timeout)
	return it, err
}

// TakeCopy takes a copy and leaves the original standing.  The item is
// known by the object's name, as Take's is.
//
// The viewer calls this Take Copy and sends the same destination
// (llviewermenu.cpp:6420); the enum comments it as "try to leave copy in
// world", which reads as though the simulator might take the original
// instead when it cannot be copied.  Measured on Agni, it does not: an
// object with the owner's copy right removed produced no item and no
// change in world at all, and the request was ignored in silence.  Put
// the right back and the same call worked.
//
// So a timeout here is not a slow grid, it is a refusal, and saying so
// is the difference between a person checking their inventory and a
// person checking their permissions.
func (w *Session) TakeCopy(ctx context.Context, o *Object, folder msg.UUID, timeout time.Duration) (*Item, error) {
	it, derezzed, err := w.derezToInventory(ctx, o, folder, derezAcquireCopy, timeout)
	if err == nil {
		return it, nil
	}
	// Only a take that went out and came to nothing is a refusal: a
	// name that could not be learned times out before anything is sent.
	if !derezzed || !errors.Is(err, ErrTimeout) {
		return nil, err
	}
	if _, still := w.ObjectByID(ctx, o.ID, 5*time.Second); still == nil {
		return nil, fmt.Errorf("sl: %s was not copied and is still where it was; an object that may not be copied is refused rather than taken", o)
	}
	return nil, err
}

// derezToInventory takes an object into a folder and waits for its
// item.  The bool says whether the take went out, which is what tells a
// take that came to nothing from one that never began.
func (w *Session) derezToInventory(ctx context.Context, o *Object, folder msg.UUID, destination uint8, timeout time.Duration) (*Item, bool, error) {
	if timeout == 0 {
		timeout = 40 * time.Second
	}
	before, err := w.FolderItems(ctx, folder)
	if err != nil {
		return nil, false, err
	}
	had := map[msg.UUID]bool{}
	for _, it := range before {
		had[it.ID] = true
	}

	// The item takes the object's name, and the name is what tells it
	// from anything else new in the folder.  Asking selects the object,
	// which the derez needs anyway.
	name := o.Name
	if name == "" {
		p, err := w.Properties(ctx, o, timeout)
		if err != nil {
			return nil, false, fmt.Errorf("sl: learning what %s is called, to know its item by: %w", o, err)
		}
		name = p.Name
	} else if err := w.Select(ctx, o); err != nil {
		return nil, false, err
	}
	if err := w.Settle(ctx, 2*time.Second); err != nil {
		return nil, false, err
	}
	local, err := w.local(ctx, o)
	if err != nil {
		return nil, false, err
	}

	m := &msg.DeRezObject{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentBlock.Destination = destination
	m.AgentBlock.DestinationID = folder
	m.AgentBlock.TransactionID = randomUUID()
	m.AgentBlock.PacketCount, m.AgentBlock.PacketNumber = 1, 0
	m.ObjectData = []msg.DeRezObject_ObjectData{{ObjectLocalID: local}}
	if err := w.Send(ctx, m); err != nil {
		return nil, false, err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := w.Settle(ctx, 2*time.Second); err != nil {
			return nil, true, err
		}
		now, err := w.FolderItems(ctx, folder)
		if err != nil {
			continue
		}
		for _, it := range now {
			if !had[it.ID] && it.Name == name {
				return it, true, nil
			}
		}
	}
	return nil, true, fmt.Errorf("%w: %s to appear in inventory as %q after taking it", ErrTimeout, o, name)
}

// Delete sends an object to the trash, and returns once the region says
// it has gone.
//
// Nothing answers the derez itself.  What says the object has gone is
// the KillObject the region sends for it, and one that does not come
// within Options.DeleteTimeout, ten seconds by default, is an error
// wrapping ErrTimeout: the delete was asked for and not confirmed, and
// the object may still be there.  An object the region has already said
// has gone, in this visit and with nothing described under its local id
// since, answers at once, and nothing is sent.  Deleting a root takes
// its linkset with it, as it does in the viewer.
// Why: doc/readbacks.md#deletes
func (w *Session) Delete(ctx context.Context, o *Object, trash msg.UUID) error {
	// A kill already heard for the number, in this visit and with nothing
	// described under it since, says it has gone; the region's kill of
	// the same number in another visit is of something else, and is
	// forgotten when the visit ends.
	local, err := w.local(ctx, o)
	if err != nil {
		return err
	}
	w.mu.Lock()
	gone := w.killed[local]
	w.mu.Unlock()
	if gone {
		return nil
	}
	if err := w.Select(ctx, o); err != nil {
		return err
	}
	if err := w.Settle(ctx, time.Second); err != nil {
		return err
	}
	if local, err = w.local(ctx, o); err != nil {
		return err
	}
	w.mu.Lock()
	gone, visit := w.killed[local], w.at.n
	w.mu.Unlock()
	if gone {
		return nil
	}

	m := &msg.DeRezObject{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentBlock.Destination = derezTrash
	m.AgentBlock.DestinationID = trash
	m.AgentBlock.TransactionID = randomUUID()
	m.AgentBlock.PacketCount, m.AgentBlock.PacketNumber = 1, 0
	m.ObjectData = []msg.DeRezObject_ObjectData{{ObjectLocalID: local}}
	if err := w.Send(ctx, m); err != nil {
		return err
	}

	err = w.await(ctx, w.deleteWait(), "the region to say it has gone", func() bool {
		return w.at.n == visit && w.killed[local]
	})
	if errors.Is(err, ErrTimeout) {
		return fmt.Errorf("sl: deleting %s was asked for, not confirmed: %w", o, err)
	}
	return err
}

func randomUUID() msg.UUID {
	var u msg.UUID
	if _, err := rand.Read(u[:]); err != nil {
		// crypto/rand does not fail in practice, and a transaction id
		// that repeats is harmless here.
		binaryFallback(&u)
	}
	return u
}

func binaryFallback(u *msg.UUID) {
	n := time.Now().UnixNano()
	for i := range 8 {
		u[i] = byte(n >> (8 * i))
	}
}
