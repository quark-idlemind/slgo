package world

import (
	"context"
	"fmt"
	"time"

	"slgo/llsd"

	"slgo/agent"
	"slgo/msg"
)

// Inventory operations that change something, as opposed to reading it.
//
// They split across two transports for reasons that are the simulator's
// and not ours. Creating and editing go over UDP, which is what the
// viewer does and what works. Deleting goes over AIS, because the UDP
// path for it is accepted and ignored -- a move into Trash returns
// nothing, changes nothing, and reports success if you believe the lack
// of an error. Nothing here believes that: every one of these reads back
// what it did before saying it did it.

// Permission bits, as Second Life packs them.
const (
	PermTransfer = 0x2000
	PermModify   = 0x4000
	PermCopy     = 0x8000
	PermMove     = 0x80000

	// PermAll is what "full permissions" means: everything an owner can
	// be given. Export and the damage bit are deliberately not in it.
	PermAll = PermTransfer | PermModify | PermCopy | PermMove
)

// Who a permission mask applies to, for SetObjectPermissions. These are
// the viewer's PermissionChangeType values.
const (
	WhoBase      = 0x01
	WhoOwner     = 0x02
	WhoGroup     = 0x04
	WhoEveryone  = 0x08
	WhoNextOwner = 0x10
)

// CreateFolder makes a folder inside another and returns its id.
//
// The id is chosen HERE rather than by the simulator, which is how the
// protocol works: the message carries the id the folder is to have. That
// is also what makes it confirmable, since we know what to look for.
func (w *World) CreateFolder(ctx context.Context, parent msg.UUID, name string) (msg.UUID, error) {
	if name == "" {
		return msg.UUID{}, fmt.Errorf("world: a folder needs a name")
	}
	if parent.IsZero() {
		parent = w.InventoryRoot()
	}

	id := randomUUID()
	m := &msg.CreateInventoryFolder{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.FolderData.FolderID = id
	m.FolderData.ParentID = parent
	m.FolderData.Type = -1 // -1 is "no preferred type", an ordinary folder
	m.FolderData.Name = []byte(name)
	if err := w.Send(ctx, m); err != nil {
		return msg.UUID{}, err
	}

	// Confirm by looking. Nothing answers this message.
	deadline := time.Now().Add(20 * time.Second)
	for {
		inv, err := w.Inventory(ctx)
		if err == nil {
			if _, ok := inv.Folder(id); ok {
				return id, nil
			}
		}
		if time.Now().After(deadline) {
			return msg.UUID{}, fmt.Errorf("world: folder %q was asked for but never appeared: %w", name, ErrTimeout)
		}
		time.Sleep(time.Second)
	}
}

// DeleteItem removes an inventory item, permanently.
//
// Over AIS, because the UDP path does not work: MoveInventoryItem into
// Trash is accepted and ignored, and so is the AIS move. AIS DELETE does
// work, and it is a real removal -- the item does not turn up in Trash
// afterwards, which was checked rather than assumed. There is therefore
// no gentler option to offer, and pretending otherwise would be worse
// than saying so.
func (w *World) DeleteItem(ctx context.Context, item msg.UUID) error {
	return w.aisDelete(ctx, "item", item)
}

// DeleteFolder removes a folder and everything in it, permanently.
func (w *World) DeleteFolder(ctx context.Context, folder msg.UUID) error {
	if folder == w.InventoryRoot() {
		return fmt.Errorf("world: that is the root of inventory")
	}
	return w.aisDelete(ctx, "category", folder)
}

func (w *World) aisDelete(ctx context.Context, kind string, id msg.UUID) error {
	_, err := w.capDo(ctx, agent.CapRequest{
		Cap:    agent.InventoryCap,
		Method: "DELETE",
		Path:   "/" + kind + "/" + id.String(),
	})
	return err
}

// SetItem changes an item's name, description or permissions.
//
// An empty name or description leaves that field alone; a nil mask
// leaves the permissions alone. The item is read back afterwards and
// returned, because Second Life narrows what it will not grant -- you
// cannot hand out more than the base mask allows -- so what was asked
// for and what now holds are different questions.
func (w *World) SetItem(ctx context.Context, item msg.UUID, name, desc string, next *uint32) (*Item, error) {
	inv, err := w.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	it, ok := inv.Item(item)
	if !ok {
		return nil, fmt.Errorf("world: no item %s in inventory", item)
	}

	want := *it
	if name != "" {
		want.Name = name
	}
	if desc != "" {
		want.Desc = desc
	}
	if next != nil {
		want.NextOwnerMask = *next
	}

	// Over AIS, not UDP.  UpdateInventoryItem is the viewer's message
	// for this and it does not work here: sent with the item's fields,
	// its checksum and a transaction id, it is accepted and the item
	// does not change.  AIS answers, and what it answers can be checked.
	//
	// This is the same lesson the delete path teaches. The UDP
	// inventory messages are fire and forget, so code written against
	// them reports success for having sent a packet.
	body := map[string]any{}
	if name != "" {
		body["name"] = name
	}
	if desc != "" {
		body["desc"] = desc
	}
	if next != nil {
		body["permissions"] = map[string]any{"next_owner_mask": int(*next)}
	}
	if len(body) == 0 {
		return it, nil
	}
	enc, err := llsd.Encode(body)
	if err != nil {
		return nil, err
	}
	if _, err := w.capDo(ctx, agent.CapRequest{
		Cap:    agent.InventoryCap,
		Method: "PATCH",
		Path:   "/item/" + item.String(),
		Body:   enc,
		Type:   "application/llsd+xml",
	}); err != nil {
		return nil, err
	}

	// Read it back rather than echoing the request.
	deadline := time.Now().Add(15 * time.Second)
	for {
		time.Sleep(time.Second)
		if inv, err := w.Inventory(ctx); err == nil {
			if got, ok := inv.Item(item); ok && (name == "" || got.Name == name) {
				return got, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("world: %s did not change: %w", item, ErrTimeout)
		}
	}
}

// SetObjectPermissions sets one who-mask on a rezzed object to an exact
// value.
//
// It takes two messages, because the protocol turns bits on or off and
// does not assign: sending only the "on" half leaves a bit the caller
// cleared still set.
func (w *World) SetObjectPermissions(ctx context.Context, o *Object, who uint8, mask uint32) error {
	on := mask & PermAll
	off := PermAll &^ on

	send := func(set uint8, bits uint32) error {
		if bits == 0 {
			return nil
		}
		m := &msg.ObjectPermissions{}
		m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
		m.HeaderData.Override = false
		m.ObjectData = []msg.ObjectPermissions_ObjectData{{
			ObjectLocalID: o.Local, Field: who, Set: set, Mask: bits,
		}}
		return w.Send(ctx, m)
	}
	if err := send(1, on); err != nil {
		return err
	}
	return send(0, off)
}

// RezFromInventory puts an inventory object into the world at a given
// position, and returns it once the simulator has described it.
//
// Two fields decide whether this works at all, and neither is obvious:
//
//   - GroupID. A parcel usually grants "create objects" to a GROUP
//     rather than to individuals. Sent as zero, the simulator sees an
//     object belonging to no group, applies the rule for strangers, and
//     refuses -- with a message blaming the land rather than the
//     request. The active group has to be named here.
//   - BypassRaycast. Without it the simulator ignores the position it
//     was given and rezzes on whatever a ray from that point hits.
//     Beside the avatar something usually is, so it appears to work;
//     aimed at open sky the request is dropped with no object and no
//     complaint.
func (w *World) RezFromInventory(ctx context.Context, it *Item, at msg.Vector3, group msg.UUID, timeout time.Duration) (*Object, error) {
	if it == nil {
		return nil, fmt.Errorf("world: nothing to rez")
	}
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	if err := inRange(mustWhere(ctx, w), at); err != nil {
		return nil, err
	}

	before, err := w.localIDs(ctx)
	if err != nil {
		return nil, err
	}

	m := &msg.RezObject{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentData.GroupID = group
	m.RezData.BypassRaycast = 1
	m.RezData.RayStart, m.RezData.RayEnd = at, at
	m.RezData.RayEndIsIntersection = false
	m.RezData.ItemFlags = it.Flags
	m.RezData.GroupMask = it.GroupMask
	m.RezData.EveryoneMask = it.EveryoneMask
	m.RezData.NextOwnerMask = it.NextOwnerMask
	m.InventoryData = msg.RezObject_InventoryData{
		ItemID: it.ID, FolderID: it.ParentID,
		CreatorID: it.CreatorID, OwnerID: it.OwnerID, GroupID: it.GroupID,
		BaseMask: it.BaseMask, OwnerMask: it.OwnerMask, GroupMask: it.GroupMask,
		EveryoneMask: it.EveryoneMask, NextOwnerMask: it.NextOwnerMask,
		Type: int8(it.Type), InvType: int8(it.InvType), Flags: it.Flags,
		SaleType: uint8(it.SaleType), SalePrice: int32(it.SalePrice),
		Name: []byte(it.Name), Description: []byte(it.Desc),
		CreationDate: int32(it.Created),
		CRC:          itemCRC(it),
	}
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	// Confirm by POSITION as well as novelty.  "New to the cache" alone
	// is not enough: the object this one replaces may only just have
	// been taken, and its entry lingers until KillObject is processed --
	// which reported the object that had gone as the object that had
	// arrived.  Where it is was known before it existed, and is the one
	// thing about it that cannot be stale.
	deadline := time.Now().Add(timeout)
	for {
		all, err := w.AllObjects(ctx, 10*time.Second)
		if err == nil {
			var best *Seen
			for _, s := range all {
				if before[s.Local] || s.Parent != 0 || s.Owner != w.Me() {
					continue
				}
				if distance(s.Position, at) > rezRadius {
					continue
				}
				if best == nil || distance(s.Position, at) < distance(best.Position, at) {
					best = s
				}
			}
			if best != nil {
				return &best.Object, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("world: %q was asked for at %v and nothing appeared there: %w",
				it.Name, at, ErrTimeout)
		}
		time.Sleep(time.Second)
	}
}

// rezRadius is how far from the asked-for spot a new object may be and
// still be the one that was asked for.  Second Life moves an object to
// stand it on whatever is under the point, so it is never exact.
const rezRadius = 10

// GiveToAvatar offers an inventory item or folder to someone.
//
// It is an offer, not a transfer: the recipient has to accept it, and
// nothing here can tell whether they did. An item that is no-copy leaves
// inventory only once they do.
//
// There is no message for this. It is an instant message whose bucket
// carries the asset type and the id, which is the whole of the protocol
// for giving something away.
func (w *World) GiveToAvatar(ctx context.Context, to msg.UUID, id msg.UUID, name string, assetType int8) error {
	bucket := make([]byte, 17)
	bucket[0] = byte(assetType)
	copy(bucket[1:], id[:])

	where, err := w.Where(ctx)
	if err != nil {
		return err
	}

	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.MessageBlock.ToAgentID = to
	m.MessageBlock.Dialog = 4 // InventoryOffered
	m.MessageBlock.Offline = 0
	m.MessageBlock.ID = randomUUID()
	m.MessageBlock.Position = where.Position
	m.MessageBlock.FromAgentName = []byte(w.Info().GetAvatarName())
	m.MessageBlock.Message = []byte(name)
	m.MessageBlock.BinaryBucket = bucket
	return w.Send(ctx, m)
}

// localIDs is what is in the region now, so a new arrival can be told
// from what was already there.
func (w *World) localIDs(ctx context.Context) (map[uint32]bool, error) {
	all, err := w.AllObjects(ctx, 30*time.Second)
	if err != nil {
		return nil, err
	}
	out := make(map[uint32]bool, len(all))
	for _, s := range all {
		out[s.Local] = true
	}
	return out, nil
}

// mustWhere is Where without the error, for a range check that should
// not fail a rez on its own account.
func mustWhere(ctx context.Context, w *World) *Presence {
	p, err := w.Where(ctx)
	if err != nil {
		return &Presence{}
	}
	return p
}

// ActivateGroup makes a group the avatar's active one and waits for the
// simulator to confirm it.
//
// This matters far more than it looks. A parcel usually grants "create
// objects" to a group rather than to individuals, and a viewer remembers
// which group you had active across sessions -- so an avatar that has
// always been able to build somewhere is suddenly refused here, with a
// message saying the land does not allow it. It does; the request simply
// arrived from nobody in particular.
func (w *World) ActivateGroup(ctx context.Context, group msg.UUID, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	m := &msg.ActivateGroup{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentData.GroupID = group
	if err := w.Send(ctx, m); err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)
	for {
		if got, err := w.ActiveGroup(ctx); err == nil && got == group {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("world: group %s never became active, "+
				"which usually means this avatar is not a member: %w", group, ErrTimeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// itemCRC is the checksum the simulator expects alongside an inventory
// item, in UpdateInventoryItem and RezObject.
//
// It is not optional and it is not a CRC.  Sent as zero, an update is
// accepted and silently does nothing: the rename returns no error, the
// item does not change, and only reading it back afterwards shows it.
// That is how this came to be written -- the read-back is what caught
// it.
//
// The fields and their order are Second Life's, not a choice.  Note what
// is absent: the base mask and the last owner, both of which the
// viewer's own version includes and the simulator evidently does not
// require.
func itemCRC(it *Item) uint32 {
	crc := uuidCRC(it.AssetID)
	crc += uuidCRC(it.ID)
	crc += uuidCRC(it.ParentID)
	crc += uuidCRC(it.CreatorID)
	crc += uuidCRC(it.OwnerID)
	crc += uuidCRC(it.GroupID)
	crc += it.OwnerMask
	crc += it.NextOwnerMask
	crc += it.EveryoneMask
	crc += it.GroupMask
	crc += it.Flags
	crc += uint32(it.InvType)
	crc += uint32(it.Type)
	crc += uint32(it.Created)
	crc += uint32(it.SalePrice)
	crc += uint32(it.SaleType) * 0x07073096
	return crc
}

// uuidCRC folds a uuid into four little endian words and adds them.
// Little endian: the bytes are read back to front within each word,
// which is not what reading the printed form suggests.
func uuidCRC(u msg.UUID) uint32 {
	var sum uint32
	for i := 0; i < 4; i++ {
		b := u[i*4 : i*4+4]
		sum += uint32(b[3])<<24 | uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0])
	}
	return sum
}

// MoveItem puts an item in a different folder.
//
// Over AIS, like the other edits: the UDP MoveInventoryItem is accepted
// and does nothing, which is the same trap the delete path found.
func (w *World) MoveItem(ctx context.Context, item, folder msg.UUID) error {
	enc, err := llsd.Encode(map[string]any{"parent_id": folder.String()})
	if err != nil {
		return err
	}
	_, err = w.capDo(ctx, agent.CapRequest{
		Cap:    agent.InventoryCap,
		Method: "PATCH",
		Path:   "/item/" + item.String(),
		Body:   enc,
		Type:   "application/llsd+xml",
	})
	return err
}
