package world

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"slgo/msg"
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

// RezOptions tune what gets built.
type RezOptions struct {
	At    msg.Vector3
	Scale msg.Vector3   // zero means half a metre cubed
	Wait  time.Duration // zero means fifteen seconds
}

// Rez creates a prim and returns it, having confirmed we own it.
//
// Confirming ownership is not caution for its own sake.  Objects stream
// in continuously, so a local id that is new to this session is not
// necessarily one we just made -- it may be somebody else's prim that
// the simulator has only now got round to mentioning.  Building on that
// mistake means editing a stranger's object, which the simulator then
// refuses in ways that look like bugs elsewhere.
func (w *World) Rez(ctx context.Context, opt RezOptions) (*Object, error) {
	if opt.Scale == (msg.Vector3{}) {
		opt.Scale = msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}
	}
	if opt.Wait == 0 {
		opt.Wait = 15 * time.Second
	}

	w.mu.Lock()
	before := make(map[uint32]bool, len(w.locals))
	for _, l := range w.locals {
		before[l] = true
	}
	w.mu.Unlock()

	m := &msg.ObjectAdd{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	d := &m.ObjectData
	d.PCode, d.Material, d.AddFlags = 9, 3, 2
	d.PathCurve, d.ProfileCurve = 16, 1
	d.PathScaleX, d.PathScaleY = 100, 100
	d.BypassRaycast = 1
	d.RayStart, d.RayEnd = opt.At, opt.At
	d.Scale = opt.Scale
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	// Wait for something new, ask who owns each candidate, and keep
	// the one that is ours.  Local id 0 is an attachment and is never
	// what was just rezzed.
	var found *Object
	asked := map[msg.UUID]bool{}
	err := w.await(ctx, opt.Wait, "a prim of ours to appear", func() bool {
		var toAsk []msg.UUID
		for id, local := range w.locals {
			if local == 0 || before[local] {
				continue
			}
			if w.owners[id] == w.me {
				found = &Object{ID: id, Local: local}
				return true
			}
			if !asked[id] {
				asked[id] = true
				toAsk = append(toAsk, id)
			}
		}
		for _, id := range toAsk {
			q := &msg.RequestObjectPropertiesFamily{}
			q.AgentData.AgentID, q.AgentData.SessionID = w.me, w.sess
			q.ObjectData.ObjectID = id
			_ = w.c.Send(ctx, q, true)
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// SetName renames an object and reads the name back.
//
// ObjectName has no reply, so the only way to know it took is to ask.
func (w *World) SetName(ctx context.Context, o *Object, name string) error {
	m := &msg.ObjectName{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData = []msg.ObjectName_ObjectData{
		{LocalID: o.Local, Name: append([]byte(name), 0)},
	}
	if err := w.Send(ctx, m); err != nil {
		return err
	}

	// Clear what we think the name is, so a stale answer cannot pass
	// for a fresh one.
	w.mu.Lock()
	delete(w.names, o.ID)
	w.mu.Unlock()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if err := w.Send(ctx, w.familyRequest(o.ID)); err != nil {
			return err
		}
		var got string
		_ = w.await(ctx, 3*time.Second, "the new name", func() bool {
			got = w.names[o.ID]
			return got != ""
		})
		if got == name {
			o.Name = name
			return nil
		}
		if got != "" {
			return fmt.Errorf("world: renaming %s to %q left it named %q", o.ID, name, got)
		}
	}
	return fmt.Errorf("%w: the name of %s after renaming it", ErrTimeout, o.ID)
}

func (w *World) familyRequest(id msg.UUID) *msg.RequestObjectPropertiesFamily {
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
func (w *World) Properties(ctx context.Context, o *Object, timeout time.Duration) (*Properties, error) {
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

func (w *World) selectMsg(locals ...uint32) *msg.ObjectSelect {
	m := &msg.ObjectSelect{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	for _, l := range locals {
		m.ObjectData = append(m.ObjectData, msg.ObjectSelect_ObjectData{ObjectLocalID: l})
	}
	return m
}

// Select tells the simulator we are editing these objects, which some
// operations require.
func (w *World) Select(ctx context.Context, objs ...*Object) error {
	locals := make([]uint32, 0, len(objs))
	for _, o := range objs {
		locals = append(locals, o.Local)
	}
	return w.Send(ctx, w.selectMsg(locals...))
}

// Link joins objects into one, with root as the root, and waits until
// the simulator agrees they are linked.
func (w *World) Link(ctx context.Context, root *Object, children ...*Object) error {
	if len(children) == 0 {
		return fmt.Errorf("world: linking needs something to link")
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

// Parent reports the local id an object says is its parent, and
// whether the simulator has mentioned it at all.
func (w *World) Parent(o *Object) (uint32, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.parents[o.Local]
	return p, ok
}

// Destinations for DeRezObject, from the viewer's EDeRezDestination.
const (
	derezTakeIntoInventory = 4
	derezTrash             = 6
)

// Take moves an object into inventory and returns the item it became.
//
// Nothing answers a take over UDP: the item turns up in the folder
// over AIS some seconds later, so this polls the folder for something
// that was not there before rather than waiting for a message.
func (w *World) Take(ctx context.Context, o *Object, folder msg.UUID, timeout time.Duration) (*Item, error) {
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
	m.AgentBlock.Destination = derezTakeIntoInventory
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
func (w *World) Delete(ctx context.Context, o *Object, trash msg.UUID) error {
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
