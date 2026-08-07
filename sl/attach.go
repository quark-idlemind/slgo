package sl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// Attachment points.  Body points are 1 to 30; the HUD points carry on
// from there.
const (
	HUDCenter2 = 31 + iota
	HUDTopRight
	HUDTop
	HUDTopLeft
	HUDCenter1
	HUDBottomLeft
	HUDBottom
	HUDBottomRight
)

// AttachAdd asks for an attachment to be added rather than to replace
// whatever is on the point.
const AttachAdd = 0x80

// Attached is a worn object.
type Attached struct {
	Object Object
	Item   msg.UUID // the inventory item it came from
	Point  int
}

// attachPoint pulls the point out of an ObjectUpdate's State byte,
// which stores it with its nibbles swapped: point 35 arrives as 0x32.
func attachPoint(state uint8) int {
	return int((state&0xf0)>>4 | (state&0x0f)<<4)
}

// attachItem reads the AttachItemID out of an object's NameValue,
// which is lines of the form
//
//	AttachItemID STRING RW DS <uuid>
//
// This is the only thing tying a worn object back to the inventory
// item it came from.  The object gets a fresh id every time it is
// attached, so it cannot be recognised by that.
func attachItem(nv []byte) (msg.UUID, bool) {
	for _, line := range strings.Split(trimNul(nv), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "AttachItemID" {
			continue
		}
		u, err := msg.ParseUUID(f[len(f)-1])
		if err != nil || u.IsZero() {
			continue
		}
		return u, true
	}
	return msg.UUID{}, false
}

// Attachments returns what is known to be worn.
//
// This is only what the simulator has mentioned since we connected.
// An attachment is described when it goes on, and a client that
// connected afterwards will never have been told about it, so an empty
// result does not mean nothing is worn.  Wear will say so.
func (w *Session) Attachments() []*Attached {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*Attached, 0, len(w.attach))
	for _, a := range w.attach {
		out = append(out, a)
	}
	return out
}

// WornFrom returns the worn object that came from an inventory item, if
// the simulator has mentioned it.
func (w *Session) WornFrom(item msg.UUID) (*Attached, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	a, ok := w.attach[item]
	return a, ok
}

// Wear attaches an inventory item and returns the object it became.
//
// It is matched on the AttachItemID rather than on being a new object,
// which is exact: attaching produces a brand new object id, and other
// objects arrive continuously.
func (w *Session) Wear(ctx context.Context, it *Item, point int, timeout time.Duration) (*Attached, error) {
	if timeout == 0 {
		timeout = 40 * time.Second
	}
	w.mu.Lock()
	delete(w.attach, it.ID)
	w.mu.Unlock()

	m := &msg.RezSingleAttachmentFromInv{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	d := &m.ObjectData
	d.ItemID, d.OwnerID = it.ID, w.me
	d.AttachmentPt = uint8(point)
	d.ItemFlags = it.Flags
	d.GroupMask, d.EveryoneMask = it.GroupMask, it.EveryoneMask
	d.NextOwnerMask = it.NextOwnerMask
	d.Name = append([]byte(it.Name), 0)
	d.Description = append([]byte(it.Desc), 0)
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	var got *Attached
	err := w.await(ctx, timeout, "the simulator to report "+it.Name+" as worn", func() bool {
		got = w.attach[it.ID]
		return got != nil
	})
	if err != nil {
		return nil, err
	}
	return got, nil
}

// WornFromItem finds the worn object that an inventory item is being
// worn as, asking whoever was connected when it was put on.
//
// This is the one way to find the same attachment twice.  A worn object
// is rezzed afresh with a new id every time it is put on, and again
// every time the avatar logs in, so its id is worth nothing between
// sessions; the item it came from does not change.
//
// It differs from Attachments, which is what THIS session has been told
// since it connected.  An attachment is described when it goes on and
// at login, so a program that started afterwards has heard nothing --
// and asking the backend reaches the session that did hear it.
func (w *Session) WornFromItem(ctx context.Context, item msg.UUID) (*Attached, bool) {
	if item.IsZero() {
		return nil, false
	}
	seen, err := w.b.Objects(ctx, "", "")
	if err != nil {
		return nil, false
	}
	for _, s := range seen {
		if s.AttachItem == item {
			return &Attached{Object: s.Object, Item: item, Point: s.AttachPoint}, true
		}
	}
	return nil, false
}

// EnsureAttached returns a worn object of a name, making one if it has
// to and putting it on if it is not on.
//
// This is for a program that wants somewhere to run scripts and does
// not care what: rezzing a prim, waiting for the region to describe it,
// and taking it away again costs seconds on EVERY run, and an
// attachment costs that once and then never again.  It also goes where
// the avatar goes, so a script in it is not left behind by a teleport.
//
// Three states, in the order they cost: worn already, which is nothing
// but a lookup; in inventory but not on, which is a wear; and not there
// at all, which is a rez, a take and a wear.  The last happens once in
// the life of an account.
func (w *Session) EnsureAttached(ctx context.Context, folder msg.UUID, name string, point int) (*Attached, error) {
	items, err := w.FolderItems(ctx, folder)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.Name != name {
			continue
		}
		// Already worn is the ordinary case, and asking costs one
		// question.  Only when it is not worn is anything disturbed:
		// Worn takes it off and puts it back on, which is how a
		// session that never heard about it learns its id, and costs
		// ten seconds.
		if a, ok := w.WornFromItem(ctx, it.ID); ok {
			return a, nil
		}
		return w.Worn(ctx, folder, name, point)
	}

	// Nothing of that name: build one and keep it.
	where, err := w.Where(ctx)
	if err != nil {
		return nil, err
	}
	at := where.Position
	at.X += 1.5
	at.Z += 0.5

	obj, err := w.Rez(ctx, RezOptions{At: at})
	if err != nil {
		return nil, fmt.Errorf("sl: making %q to attach: %w", name, err)
	}
	if err := w.SetName(ctx, obj, name); err != nil {
		return nil, fmt.Errorf("sl: naming %q: %w", name, err)
	}
	it, err := w.Take(ctx, obj, folder, 40*time.Second)
	if err != nil {
		return nil, fmt.Errorf("sl: taking %q into inventory: %w", name, err)
	}
	a, err := w.Wear(ctx, it, point, 40*time.Second)
	if err != nil {
		return nil, fmt.Errorf("sl: putting %q on: %w", name, err)
	}
	return a, nil
}

// TakeOff detaches a worn item back into inventory.
func (w *Session) TakeOff(ctx context.Context, item msg.UUID) error {
	m := &msg.DetachAttachmentIntoInv{}
	m.ObjectData.AgentID = w.me
	m.ObjectData.ItemID = item
	if err := w.Send(ctx, m); err != nil {
		return err
	}
	w.mu.Lock()
	delete(w.attach, item)
	w.mu.Unlock()
	return nil
}

// Worn finds a worn object by the name of the inventory item it came
// from, putting it back on if the simulator has not mentioned it.
//
// The re-wearing is the point.  A worn attachment is described only
// when it is attached, and a client sees only what is relayed after it
// connects, so anything put on before this program started has never
// been mentioned and its local id is unknown.  Taking it off and
// putting it back on is the way to be told.
func (w *Session) Worn(ctx context.Context, folder msg.UUID, name string, point int) (*Attached, error) {
	it, err := w.FindItem(ctx, folder, name)
	if err != nil {
		return nil, err
	}
	if a, ok := w.WornFrom(it.ID); ok {
		return a, nil
	}

	// Take it off first.  Attaching something already worn is not
	// reliably an event, and the event is what is wanted.
	if err := w.TakeOff(ctx, it.ID); err != nil {
		return nil, err
	}
	if err := w.Settle(ctx, 10*time.Second); err != nil {
		return nil, err
	}
	a, err := w.Wear(ctx, it, point, 40*time.Second)
	if err != nil {
		return nil, fmt.Errorf("sl: putting %q back on: %w", name, err)
	}
	return a, nil
}
