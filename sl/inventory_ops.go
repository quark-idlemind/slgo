package sl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/llsd"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// Inventory operations that change something, as opposed to reading it.
//
// They go over two transports.  Making a folder, copying an item,
// moving an item or a folder, rezzing an item, activating a group and
// setting an object's permissions are UDP messages, as the viewer sends
// them, and nothing answers any of them: each is read back -- inventory
// listed over AIS, the region or the object asked -- until what was
// asked for shows, and is an error if it never does.  GiveToAvatar is
// the exception, since what it makes is an offer somebody else answers.
//
// Deleting and emptying, renaming a folder, and changing an item's name,
// description or next-owner mask go over AIS, which answers each request
// and says when it refuses one.  SetItem reads its change back as well.
// Why: doc/readbacks.md

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

// PermWords says which of PermAll's bits a mask has, in words -- "copy,
// modify, transfer, move" in that order -- or "nothing".
func PermWords(mask uint32) string {
	var have []string
	for _, p := range []struct {
		bit  uint32
		word string
	}{
		{PermCopy, "copy"},
		{PermModify, "modify"},
		{PermTransfer, "transfer"},
		{PermMove, "move"},
	} {
		if mask&p.bit != 0 {
			have = append(have, p.word)
		}
	}
	if len(have) == 0 {
		return "nothing"
	}
	return strings.Join(have, ", ")
}

// Who a permission mask applies to, for SetObjectPermissions. These are
// the viewer's PermissionChangeType values.
const (
	WhoBase      = 0x01
	WhoOwner     = 0x02
	WhoGroup     = 0x04
	WhoEveryone  = 0x08
	WhoNextOwner = 0x10
)

// whoMasks is each who-value SetObjectPermissions takes: what it is
// called, and which of an object's masks it reads back as.
var whoMasks = map[uint8]struct {
	name string
	of   func(*Properties) uint32
}{
	WhoBase:      {"base", func(p *Properties) uint32 { return p.BaseMask }},
	WhoOwner:     {"owner", func(p *Properties) uint32 { return p.OwnerMask }},
	WhoGroup:     {"group", func(p *Properties) uint32 { return p.GroupMask }},
	WhoEveryone:  {"everyone", func(p *Properties) uint32 { return p.EveryoneMask }},
	WhoNextOwner: {"next owner", func(p *Properties) uint32 { return p.NextOwnerMask }},
}

// CreateFolder makes a folder inside another and returns its id.
//
// The id is chosen HERE rather than by the simulator, which is how the
// protocol works: the message carries the id the folder is to have. That
// is also what makes it confirmable, since we know what to look for.
//
// A caller that gives up while this waits gets ctx.Err(), and the id as
// well if the folder turned out to have been made, so that it can be
// deleted again.
func (w *Session) CreateFolder(ctx context.Context, parent msg.UUID, name string) (msg.UUID, error) {
	if name == "" {
		return msg.UUID{}, fmt.Errorf("sl: a folder needs a name")
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
	// Terminated, like every other string on this protocol.  Without
	// the NUL the simulator reads the length and takes the last byte
	// for the terminator, so "slgo path probe" becomes "slgo path
	// prob" -- a folder one character short of what was asked for,
	// which nothing then finds by name.
	m.FolderData.Name = append([]byte(name), 0)
	if err := w.Send(ctx, m); err != nil {
		return msg.UUID{}, err
	}

	// Confirm by looking. Nothing answers this message.
	look := func(ctx context.Context) (bool, error) {
		inv, err := w.Inventory(ctx)
		if err != nil {
			return false, err
		}
		_, ok := inv.Folder(id)
		return ok, nil
	}
	err := poll(ctx, 20*time.Second, time.Second, fmt.Sprintf("folder %q to appear", name), look)
	if err != nil && !lastLook(ctx, look) {
		return msg.UUID{}, err
	}
	return id, err
}

// DeleteItem removes an inventory item, permanently.
//
// Over AIS, because the UDP path does not work: MoveInventoryItem into
// Trash is accepted and ignored, and so is the AIS move. AIS DELETE does
// work, and it is a real removal -- the item does not turn up in Trash
// afterwards, which was checked rather than assumed. There is therefore
// no gentler option to offer, and pretending otherwise would be worse
// than saying so.
func (w *Session) DeleteItem(ctx context.Context, item msg.UUID) error {
	return w.aisDelete(ctx, "item", item)
}

// CopyItem duplicates an inventory item under a new name and returns
// the copy.
//
// This is how an avatar that may not rez still gets more objects: an
// object it already owns can be duplicated in inventory and worn,
// without the land ever being asked whether a new one may be created.
// The item has to be copyable -- a no-copy item has nothing to
// duplicate, and the grid simply does not answer.
//
// The new id is the grid's to choose, unlike a folder's, so the copy is
// found by looking for its name rather than by knowing it in advance.
// An empty name means the same name as the original, which inside one
// folder gives two items a person cannot tell apart; it is allowed
// because the protocol allows it, but naming the copy is better.
//
// A caller that gives up while this waits gets ctx.Err(), and the copy
// as well if it turned out to have been made.
func (w *Session) CopyItem(ctx context.Context, item, folder msg.UUID, name string, timeout time.Duration) (*Item, error) {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if folder.IsZero() {
		return nil, fmt.Errorf("sl: a copy needs a folder to go in")
	}

	before, err := w.FolderItems(ctx, folder)
	if err != nil {
		return nil, err
	}
	had := make(map[msg.UUID]bool, len(before))
	for _, it := range before {
		had[it.ID] = true
	}

	m := &msg.CopyInventoryItem{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.InventoryData = []msg.CopyInventoryItem_InventoryData{{
		OldAgentID:  w.me,
		OldItemID:   item,
		NewFolderID: folder,
		NewName:     append([]byte(name), 0),
	}}
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	// Read it back rather than believe the send: nothing in this file
	// reports success on the strength of no error.
	var copied *Item
	look := func(ctx context.Context) (bool, error) {
		items, err := w.FolderItems(ctx, folder)
		if err != nil {
			return false, err
		}
		for _, it := range items {
			if !had[it.ID] && (name == "" || it.Name == name) {
				copied = it
				return true, nil
			}
		}
		return false, nil
	}
	err = poll(ctx, timeout, time.Second, fmt.Sprintf("the copy of %s to appear, "+
		"which it never does for a no-copy item", item), look)
	if err != nil && !lastLook(ctx, look) {
		return nil, err
	}
	return copied, err
}

// FolderTrash is the preferred type the grid gives the trash.
//
// A folder is the trash because of its preferred type, not its name: it
// can be renamed, and an account made through a viewer in another
// language never called it "Trash" in the first place.
const FolderTrash = 14

// TrashFolder finds the trash.
func (w *Session) TrashFolder(ctx context.Context) (msg.UUID, error) {
	es, err := w.ListFolder(ctx, w.InventoryRoot(), 0)
	if err != nil {
		return msg.UUID{}, err
	}
	for _, e := range es {
		if e.Folder && e.Type == FolderTrash {
			return e.ID, nil
		}
	}
	return msg.UUID{}, fmt.Errorf("sl: this inventory has no trash folder")
}

// PurgeFolder throws away everything inside a folder, permanently,
// leaving the folder itself.
//
// This is what emptying the trash is: the trash is a folder like any
// other and is not meant to be deleted, only emptied.  It works on any
// folder, because the grid makes no distinction -- so it will empty
// Objects as readily as Trash, and there is no undo.
func (w *Session) PurgeFolder(ctx context.Context, folder msg.UUID) error {
	if folder.IsZero() {
		return fmt.Errorf("sl: no folder to empty")
	}
	if folder == w.InventoryRoot() {
		return fmt.Errorf("sl: that is the root of inventory")
	}
	_, err := w.capDo(ctx, agent.CapRequest{
		Cap:    agent.InventoryCap,
		Method: "DELETE",
		Path:   "/category/" + folder.String() + "/children",
	})
	return err
}

// DeleteFolder removes a folder and everything in it, permanently.
func (w *Session) DeleteFolder(ctx context.Context, folder msg.UUID) error {
	if folder == w.InventoryRoot() {
		return fmt.Errorf("sl: that is the root of inventory")
	}
	return w.aisDelete(ctx, "category", folder)
}

func (w *Session) aisDelete(ctx context.Context, kind string, id msg.UUID) error {
	_, err := w.capDo(ctx, agent.CapRequest{
		Cap:    agent.InventoryCap,
		Method: "DELETE",
		Path:   "/" + kind + "/" + id.String(),
	})
	return err
}

// SetItem changes an item's name, description or next-owner mask.
//
// An empty name or description leaves that field alone; a nil mask
// leaves the next owner's permissions alone. The item is read back
// afterwards and returned, because Second Life narrows what it will not
// grant -- you cannot hand out more than the base mask allows -- so what
// was asked for and what now holds are different questions.
func (w *Session) SetItem(ctx context.Context, item msg.UUID, name, desc string, next *uint32) (*Item, error) {
	inv, err := w.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	it, ok := inv.Item(item)
	if !ok {
		return nil, fmt.Errorf("sl: no item %s in inventory", item)
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

	// Read it back rather than echoing the request, and read back
	// everything that was asked for.
	//
	// Testing the name alone was worse than testing nothing: a
	// description-only change names nothing, so the condition was
	// vacuously true and the first item that could be read was
	// returned -- the item as it was.  A grid that had refused outright
	// came back as a success carrying a stale item, which is the exact
	// failure this file exists to prevent.
	arrived := func(got *Item) bool {
		return (name == "" || got.Name == want.Name) &&
			(desc == "" || got.Desc == want.Desc) &&
			(next == nil || got.NextOwnerMask == want.NextOwnerMask)
	}

	var got *Item
	err = poll(ctx, 15*time.Second, time.Second, fmt.Sprintf("%s to change", item),
		func(ctx context.Context) (bool, error) {
			inv, err := w.Inventory(ctx)
			if err != nil {
				return false, err
			}
			it, ok := inv.Item(item)
			if !ok || !arrived(it) {
				return false, nil
			}
			got = it
			return true, nil
		})
	if err != nil {
		return nil, err
	}
	return got, nil
}

// SetObjectPermissions sets one who-mask on a rezzed object to an exact
// value, and returns what the mask allows once the object's properties
// say the change has landed.
//
// It takes two messages, because the protocol turns bits on or off and
// does not assign: sending only the "on" half leaves a bit the caller
// cleared still set.  Only the bits in PermAll are set or compared.
//
// Nothing answers either message, so the object's properties are read
// for up to fifteen seconds until the mask is what the permission rules
// make of what was sent.  The rules adjust rather than refuse -- a next
// owner who may not copy may always transfer, and everyone is never
// given modify -- so what is returned may differ from what was asked
// for, and is what the mask now allows.  A mask that never reads as the
// rules would make it is an error wrapping ErrTimeout that says what it
// allows instead.
// Why: doc/readbacks.md#object-permissions
func (w *Session) SetObjectPermissions(ctx context.Context, o *Object, who uint8, mask uint32) (uint32, error) {
	field, ok := whoMasks[who]
	if !ok {
		return 0, fmt.Errorf("sl: %#x names no permission mask", who)
	}
	on := mask & PermAll
	off := PermAll &^ on
	local, err := w.local(ctx, o)
	if err != nil {
		return 0, err
	}

	send := func(set uint8, bits uint32) error {
		if bits == 0 {
			return nil
		}
		m := &msg.ObjectPermissions{}
		m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
		m.HeaderData.Override = false
		m.ObjectData = []msg.ObjectPermissions_ObjectData{{
			ObjectLocalID: local, Field: who, Set: set, Mask: bits,
		}}
		return w.Send(ctx, m)
	}
	if err := send(1, on); err != nil {
		return 0, err
	}
	if err := send(0, off); err != nil {
		return 0, err
	}

	// Read until it holds: a single read can overtake the write.
	var got, want uint32
	var read bool
	err = poll(ctx, 15*time.Second, time.Second,
		fmt.Sprintf("the %s mask of %s to take %s", field.name, o, PermWords(on)),
		func(ctx context.Context) (bool, error) {
			p, err := w.Properties(ctx, o, 5*time.Second)
			if err != nil {
				return false, err
			}
			var compared uint32
			want, compared = expectMask(who, on, p)
			got, read = field.of(p)&PermAll, true
			return got&compared == want, nil
		})
	if err != nil {
		if read && errors.Is(err, ErrTimeout) {
			return 0, fmt.Errorf("%w; it allows %s, and the permission rules make what was sent %s",
				err, PermWords(got), PermWords(want))
		}
		return 0, err
	}
	return got, nil
}

// expectMask is what the permission rules make of the bits sent for
// who, given the object's masks as they read now, and which of PermAll's
// bits the rules settle; only those are compared.
//
// The rules are the viewer's copy of them, llinventory/llpermissions.cpp:
// fix (:155-173) keeps the owner's mask within the base, the group's and
// everyone's within the owner's and the next owner's within the base,
// never gives everyone modify, and takes copy from the group and
// everyone when the base has no transfer; setNextOwnerBits (:446-475)
// gives transfer to a next owner without copy; and setBaseBits
// (:328-351) lets only the system change a base mask, so one is expected
// as sent.  Two bits the source does not settle are not compared: copy
// for the group and everyone when the base has no transfer, since fix
// leaves it to a group-owned object and ObjectProperties does not say
// plainly whether this is one; and move for a next owner, which
// fixFairUse (:178-190) adds to one that is not empty, at points the
// source does not show being the simulator's.
// Why: doc/readbacks.md#object-permissions
func expectMask(who uint8, sent uint32, p *Properties) (want, compared uint32) {
	base, owner := p.BaseMask&PermAll, p.OwnerMask&PermAll
	want, compared = sent&PermAll, uint32(PermAll)
	switch who {
	case WhoOwner:
		want &= base
	case WhoGroup, WhoEveryone:
		want &= owner
		if who == WhoEveryone {
			want &^= PermModify
		}
		if base&PermTransfer == 0 {
			compared &^= PermCopy
		}
	case WhoNextOwner:
		want &= base
		if want&PermCopy == 0 {
			want |= PermTransfer
		}
		want &= base
		if want != 0 {
			compared &^= PermMove
		}
	}
	return want & compared, compared
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
//
// A caller that gives up while this waits gets ctx.Err(), and the
// object as well if it turned out to be standing there already.
func (w *Session) RezFromInventory(ctx context.Context, it *Item, at msg.Vector3, group msg.UUID, timeout time.Duration) (*Object, error) {
	if it == nil {
		return nil, fmt.Errorf("sl: nothing to rez")
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
	var made *Object
	look := func(ctx context.Context) (bool, error) {
		all, err := w.AllObjects(ctx, 10*time.Second)
		if err != nil {
			return false, err
		}
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
		if best == nil {
			return false, nil
		}
		made = &best.Object
		return true, nil
	}
	err = poll(ctx, timeout, time.Second, fmt.Sprintf("%q to appear at %v", it.Name, at), look)
	if err != nil && !lastLook(ctx, look) {
		return nil, err
	}
	return made, err
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
func (w *Session) GiveToAvatar(ctx context.Context, to msg.UUID, id msg.UUID, name string, assetType int8) error {
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
	// Nul-terminated, like every other string on this protocol.  Sent
	// without it the simulator takes the last character AS the
	// terminator, and the offer arrives naming "aut" -- observed, not
	// theorised.  The item itself is unharmed, since its name travels
	// with the asset and not in this field, so the damage is confined
	// to what the recipient is shown.
	m.MessageBlock.FromAgentName = append([]byte(w.Info().AvatarName), 0)
	m.MessageBlock.Message = append([]byte(name), 0)
	m.MessageBlock.BinaryBucket = bucket
	return w.Send(ctx, m)
}

// localIDs is what is in the region now, so a new arrival can be told
// from what was already there.
//
// The backend's list as it stands, without asking for names: that
// waits for the answers to stop, which is seconds whenever anything in
// the region goes unanswered, and a local id is all this is for.
func (w *Session) localIDs(ctx context.Context) (map[uint32]bool, error) {
	all, err := w.fetch(ctx, "", "")
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
func mustWhere(ctx context.Context, w *Session) *Presence {
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
func (w *Session) ActivateGroup(ctx context.Context, group msg.UUID, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	m := &msg.ActivateGroup{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentData.GroupID = group
	if err := w.Send(ctx, m); err != nil {
		return err
	}

	return poll(ctx, timeout, 500*time.Millisecond, fmt.Sprintf("group %s to become active; "+
		"one that never does usually means this avatar is not a member", group),
		func(ctx context.Context) (bool, error) {
			got, err := w.ActiveGroup(ctx)
			return err == nil && got == group, err
		})
}

// itemCRC is the checksum the simulator expects alongside an inventory
// item, which RezObject carries.  UpdateInventoryItem carries one too,
// and is not sent: see SetItem.
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

// MoveItem puts an item in a different folder, renaming it on the way if
// a name is given, and returns once the folder lists it there.
//
// Over UDP, as the viewer moves one: AIS refuses to change an item's
// parent.  The message carries a new name, so moving and renaming is one
// message rather than two, and an empty name leaves the name alone.
//
// Nothing answers the message, and a move into Trash is accepted and
// ignored, so the destination is listed until the item is in it -- under
// the new name, if one was given -- for up to fifteen seconds.  One that
// never arrives is an error wrapping ErrTimeout.
// Why: doc/readbacks.md#moves
func (w *Session) MoveItem(ctx context.Context, item, folder msg.UUID, newName ...string) error {
	if folder.IsZero() {
		return fmt.Errorf("sl: a move needs a folder to go in")
	}
	var name string
	if len(newName) > 0 {
		name = newName[0]
	}
	m := &msg.MoveInventoryItem{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentData.Stamp = false // true would re-date the item
	m.InventoryData = []msg.MoveInventoryItem_InventoryData{{
		ItemID: item, FolderID: folder, NewName: append([]byte(name), 0),
	}}
	if err := w.Send(ctx, m); err != nil {
		return err
	}
	return w.arrives(ctx, folder, item, false, name)
}

// moveFor bounds the wait for a move to show in its destination.
const moveFor = 15 * time.Second

// arrives lists folder until id is in it -- a folder or an item as
// isFolder says, and under name unless name is empty -- which is how a
// move is confirmed.
func (w *Session) arrives(ctx context.Context, folder, id msg.UUID, isFolder bool, name string) error {
	what := fmt.Sprintf("%s in %s", id, folder)
	if name != "" {
		what += fmt.Sprintf(" as %q", name)
	}
	var there bool   // listed, whatever it was called
	var named string // what it was called
	err := poll(ctx, moveFor, time.Second, what, func(ctx context.Context) (bool, error) {
		es, err := w.ListFolder(ctx, folder, 0)
		if err != nil {
			return false, err
		}
		there = false
		for _, e := range es {
			if e.ID == id && e.Folder == isFolder {
				there, named = true, e.Name
				return name == "" || e.Name == name, nil
			}
		}
		return false, nil
	})
	if !errors.Is(err, ErrTimeout) {
		return err
	}
	if there {
		return fmt.Errorf("sl: a move was asked for and arrived named %q: %w", named, err)
	}
	return fmt.Errorf("sl: a move was asked for and never arrived: %w", err)
}

// RenameFolder changes a folder's name.
//
// Over AIS, unlike a move: the refusal that keeps moves on UDP was about
// parent_id, not about PATCH, so a category takes a new name this way
// and the viewer prefers it too.
//
// A system folder -- Objects, Notecards, Trash, anything with a
// preferred type -- is another matter: the viewer refuses to ask at
// all (LLFolderType::lookupIsProtectedType), and what the grid would
// say has not been tested here, because finding out means renaming
// somebody's Objects folder to see whether it comes back.
func (w *Session) RenameFolder(ctx context.Context, folder msg.UUID, name string) error {
	if name == "" {
		return fmt.Errorf("sl: a folder needs a name")
	}
	if folder == w.invRoot {
		return fmt.Errorf("sl: the inventory root cannot be renamed")
	}
	enc, err := llsd.Encode(map[string]any{"name": name})
	if err != nil {
		return err
	}
	_, err = w.capDo(ctx, agent.CapRequest{
		Cap:    agent.InventoryCap,
		Method: "PATCH",
		Path:   "/category/" + folder.String(),
		Body:   enc,
		Type:   "application/llsd+xml",
	})
	return err
}

// MoveFolder puts a folder inside another one, and returns once the
// parent lists it there.
//
// Over UDP, and read back, for the same reasons MoveItem is.  A system
// folder cannot be moved either.
// Why: doc/readbacks.md#moves
func (w *Session) MoveFolder(ctx context.Context, folder, parent msg.UUID) error {
	if parent.IsZero() {
		return fmt.Errorf("sl: a move needs a folder to go in")
	}
	m := &msg.MoveInventoryFolder{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentData.Stamp = false
	m.InventoryData = []msg.MoveInventoryFolder_InventoryData{{
		FolderID: folder, ParentID: parent,
	}}
	if err := w.Send(ctx, m); err != nil {
		return err
	}
	return w.arrives(ctx, parent, folder, true, "")
}
