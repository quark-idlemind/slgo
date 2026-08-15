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
// local id, which is a per-region handle that does not survive a region
// crossing or a relog.
type Object struct {
	ID    msg.UUID
	Local uint32
	Name  string
}

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
	m := &msg.ObjectName{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData = []msg.ObjectName_ObjectData{
		{LocalID: o.Local, Name: append([]byte(name), 0)},
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

	if err := w.Send(ctx, w.selectMsg(o.Local)); err != nil {
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
		locals = append(locals, o.Local)
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
		m.ObjectData = append(m.ObjectData, msg.ObjectLink_ObjectData{ObjectLocalID: o.Local})
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
// again.  The viewer does send it, since clicking an object selects the
// whole linkset and every prim of a selection goes into the message, so
// the selection this takes -- exactly the prims being freed -- is one
// the viewer would not make, and the viewer's source cannot say whether
// the simulator minds: it is only ever the sending half.
//
// It does not mind.  Measured on Agni, in Pelmar Reach: three prims linked
// into one object, then a delink naming only the last child, with the
// root left out of both the selection and the message.  The child came
// out and stood where it had been, and what was left answered to the
// root's name as two prims.
//
// # The physics shape the viewer changes on the way past
//
// Before sending, the viewer walks the selection and, for every
// modifiable prim whose physics shape type is PHYSICS_SHAPE_NONE, sets
// it to PHYSICS_SHAPE_CONVEX_HULL and calls updateFlags()
// (llselectmgr.cpp:5458-5472).  This does not, for two reasons.
//
// The first is that the step never puts the shape on the wire.
// updateFlags() takes a physics_changed parameter which defaults to
// false (llviewerobject.h:650) and the delink passes nothing, and it is
// that parameter which decides whether the ExtraPhysics block carrying
// PhysicsShapeType is added at all (llviewerobject.cpp:7297-7305).  What
// reaches the simulator is an ObjectFlagUpdate of UsePhysics,
// IsTemporary and IsPhantom -- none of which the delink touched.  The
// new shape type stays in the viewer's own copy of the object, which is
// where its build floater reads one from.
//
// The second is that this client does not know what a prim's physics
// shape is.  It arrives in ObjectPhysicsProperties, which nothing here
// asks for or decodes, so copying the step would mean fetching the
// properties of every prim and sending a message this package does not
// have, in order to reproduce bookkeeping the viewer does for its own
// display.  What a freed prim's physics shape ends up as is the
// simulator's business and was not verifiable from the viewer's source;
// if one ever comes out of a delink shaped wrongly, that is worth
// measuring on a live region before writing code against it.
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
		m.ObjectData = append(m.ObjectData, msg.ObjectDelink_ObjectData{ObjectLocalID: o.Local})
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
	p, ok := w.parents[o.Local]
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
func (w *Session) Take(ctx context.Context, o *Object, folder msg.UUID, timeout time.Duration) (*Item, error) {
	return w.derezToInventory(ctx, o, folder, derezTakeIntoInventory, timeout)
}

// TakeCopy takes a copy and leaves the original standing.
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
	it, err := w.derezToInventory(ctx, o, folder, derezAcquireCopy, timeout)
	if err == nil {
		return it, nil
	}
	if !errors.Is(err, ErrTimeout) {
		return nil, err
	}
	if _, still := w.ObjectByID(ctx, o.ID, 5*time.Second); still == nil {
		return nil, fmt.Errorf("sl: %s was not copied and is still where it was; an object that may not be copied is refused rather than taken", o)
	}
	return nil, err
}

func (w *Session) derezToInventory(ctx context.Context, o *Object, folder msg.UUID, destination uint8, timeout time.Duration) (*Item, error) {
	if timeout == 0 {
		timeout = 40 * time.Second
	}
	before, err := w.FolderItems(ctx, folder)
	if err != nil {
		return nil, err
	}
	had := map[msg.UUID]bool{}
	for _, it := range before {
		had[it.ID] = true
	}

	if err := w.Select(ctx, o); err != nil {
		return nil, err
	}
	if err := w.Settle(ctx, 2*time.Second); err != nil {
		return nil, err
	}

	m := &msg.DeRezObject{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentBlock.Destination = destination
	m.AgentBlock.DestinationID = folder
	m.AgentBlock.TransactionID = randomUUID()
	m.AgentBlock.PacketCount, m.AgentBlock.PacketNumber = 1, 0
	m.ObjectData = []msg.DeRezObject_ObjectData{{ObjectLocalID: o.Local}}
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := w.Settle(ctx, 2*time.Second); err != nil {
			return nil, err
		}
		now, err := w.FolderItems(ctx, folder)
		if err != nil {
			continue
		}
		for _, it := range now {
			if !had[it.ID] && (o.Name == "" || it.Name == o.Name) {
				return it, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: %s to appear in inventory after taking it", ErrTimeout, o)
}

// Delete sends an object to the trash.
func (w *Session) Delete(ctx context.Context, o *Object, trash msg.UUID) error {
	if err := w.Select(ctx, o); err != nil {
		return err
	}
	if err := w.Settle(ctx, time.Second); err != nil {
		return err
	}
	m := &msg.DeRezObject{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentBlock.Destination = derezTrash
	m.AgentBlock.DestinationID = trash
	m.AgentBlock.TransactionID = randomUUID()
	m.AgentBlock.PacketCount, m.AgentBlock.PacketNumber = 1, 0
	m.ObjectData = []msg.DeRezObject_ObjectData{{ObjectLocalID: o.Local}}
	return w.Send(ctx, m)
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
