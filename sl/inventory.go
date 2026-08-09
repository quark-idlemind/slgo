package sl

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// Item is something in agent inventory, and Folder a category.
type (
	Item   = agent.Item
	Folder = agent.Folder
)

// TaskItem is something inside an object.
//
// The copy inside an object is not the item that was put there: it has
// its own id, and that is the one every operation on it wants.  Using
// the agent inventory id instead is the mistake this type exists to
// make hard.
type TaskItem struct {
	ID    msg.UUID
	Asset msg.UUID
	Name  string
	Type  string

	// The rest of what the contents file says. It is read because
	// changing an item in an object means sending the whole item back,
	// so anything not read here would be silently reset to zero.
	InvType   string
	Desc      string
	Flags     uint32
	Created   int64
	CreatorID msg.UUID
	OwnerID   msg.UUID
	GroupID   msg.UUID

	BaseMask      uint32
	OwnerMask     uint32
	GroupMask     uint32
	EveryoneMask  uint32
	NextOwnerMask uint32

	SaleType  string
	SalePrice int32
}

// Folder finds a folder by name under the inventory root.
func (w *Session) Folder(ctx context.Context, name string) (msg.UUID, error) {
	inv := agent.NewInventory(w.invRoot)
	if err := agent.FetchFolder(ctx, w.b, inv, w.invRoot); err != nil {
		return msg.UUID{}, fmt.Errorf("sl: reading the inventory root: %w", err)
	}
	f, ok := inv.FindFolder(name)
	if !ok {
		return msg.UUID{}, fmt.Errorf("sl: no inventory folder named %q", name)
	}
	return f.ID, nil
}

// ObjectsFolder is the inventory folder a take lands in.  It is
// named for the folder, not for objects in the region: see AllObjects
// for those.
func (w *Session) ObjectsFolder(ctx context.Context) (msg.UUID, error) {
	return w.Folder(ctx, "Objects")
}

// FolderItems lists what a folder holds, freshly fetched.
func (w *Session) FolderItems(ctx context.Context, folder msg.UUID) ([]*Item, error) {
	inv := agent.NewInventory(w.invRoot)
	if err := agent.FetchFolder(ctx, w.b, inv, folder); err != nil {
		return nil, fmt.Errorf("sl: reading folder %s: %w", folder, err)
	}
	return inv.Contents(folder), nil
}

// FindItem looks for an item by name in a folder.
func (w *Session) FindItem(ctx context.Context, folder msg.UUID, name string) (*Item, error) {
	items, err := w.FolderItems(ctx, folder)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.Name == name {
			return it, nil
		}
	}
	return nil, fmt.Errorf("sl: no item named %q in folder %s", name, folder)
}

// CreateItem makes an empty inventory item and waits for the simulator
// to confirm it, which it does by echoing back a callback id.
func (w *Session) CreateItem(ctx context.Context, name, desc string, assetType, invType int8) (*Item, error) {
	cb := uint32(time.Now().UnixNano() & 0x7fffffff)
	m := &msg.CreateInventoryItem{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	b := &m.InventoryBlock
	b.CallbackID = cb
	b.NextOwnerMask = 0x0008e000
	b.Type, b.InvType = assetType, invType
	b.Name = append([]byte(name), 0)
	b.Description = append([]byte(desc), 0)
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	var d *msg.UpdateCreateInventoryItem_InventoryData
	err := w.await(ctx, 20*time.Second, "the simulator to confirm creating "+name, func() bool {
		d = w.created[cb]
		return d != nil
	})
	if err != nil {
		return nil, err
	}
	return itemFromCreate(d), nil
}

func itemFromCreate(d *msg.UpdateCreateInventoryItem_InventoryData) *Item {
	return &Item{
		ID: d.ItemID, ParentID: d.FolderID, AssetID: d.AssetID,
		Name: trimNul(d.Name), Desc: trimNul(d.Description),
		Type: int(d.Type), InvType: int(d.InvType), Flags: d.Flags,
		CreatorID: d.CreatorID, OwnerID: d.OwnerID, GroupID: d.GroupID,
		BaseMask: d.BaseMask, OwnerMask: d.OwnerMask, GroupMask: d.GroupMask,
		EveryoneMask: d.EveryoneMask, NextOwnerMask: d.NextOwnerMask,
		SaleType: int(d.SaleType), SalePrice: int(d.SalePrice),
	}
}

// UploadResult is what a capability said about an asset that was saved.
//
// For a script, Compiled is the verdict and State is not: the state is
// "complete" and a new asset id is issued whether or not the source
// compiles.
type UploadResult struct {
	State    string
	NewAsset msg.UUID
	Compiled bool
	Errors   []string
	Body     []byte

	// NewItem is the inventory item a file upload made, and is zero
	// for the capabilities that write to an item that already exists.
	NewItem msg.UUID

	// Message is what the capability said when it was unhappy, which
	// it says instead of failing: an upload with no money behind it
	// answers 200 with a state of "error" and the reason here.
	Message string
}

// upload runs the two step asset upload: describe what is being
// written, then write it to the URL that comes back.
func (w *Session) upload(ctx context.Context, capName string, fields map[string]any, body []byte) (*UploadResult, error) {
	req, err := llsd.Encode(fields)
	if err != nil {
		return nil, err
	}
	first, err := w.capDo(ctx, agent.CapRequest{
		Cap: capName, Method: "POST", Type: "application/llsd+xml", Body: req,
	})
	if err != nil {
		return nil, err
	}
	described := llsd.Map(decodeLLSD(first))
	uploader := llsd.String(described, "uploader")
	if uploader == "" {
		// A refusal arrives here rather than as an HTTP error: the
		// capability answers 200 with state "error" and says why, and
		// "too poor" and "the fee is not what you said" are both worth
		// reading rather than reporting as a missing field.
		if why := llsd.String(described, "message"); why != "" {
			return nil, fmt.Errorf("sl: %s refused the upload: %s", capName, why)
		}
		return nil, fmt.Errorf("sl: %s gave no uploader: %s", capName, snippet(first))
	}

	second, err := w.capDo(ctx, agent.CapRequest{
		URL: uploader, Method: "POST", Type: "application/octet-stream", Body: body,
	})
	if err != nil {
		return nil, err
	}

	m := llsd.Map(decodeLLSD(second))
	res := &UploadResult{Body: second}
	if m != nil {
		res.State = llsd.String(m, "state")
		res.NewAsset, _ = msg.ParseUUID(llsd.String(m, "new_asset"))
		res.NewItem, _ = msg.ParseUUID(llsd.String(m, "new_inventory_item"))
		res.Message = llsd.String(m, "message")
		res.Compiled = llsd.Bool(m, "compiled")
		if errs, ok := m["errors"].([]any); ok {
			for _, e := range errs {
				res.Errors = append(res.Errors, fmt.Sprint(e))
			}
		}
	}
	return res, nil
}

func decodeLLSD(b []byte) any {
	v, err := llsd.Decode(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	return v
}

// SaveScript writes source to a script already in agent inventory.
//
// Saving to inventory compiles, so the result carries the verdict even
// though no object is involved.
func (w *Session) SaveScript(ctx context.Context, item msg.UUID, source string) (*UploadResult, error) {
	return w.upload(ctx, "UpdateScriptAgent", map[string]any{
		"item_id": item.String(),
		"target":  "mono",
	}, []byte(source))
}

// SaveNotecard writes a notecard in agent inventory.
func (w *Session) SaveNotecard(ctx context.Context, item msg.UUID, text string) (*UploadResult, error) {
	return w.upload(ctx, "UpdateNotecardAgentInventory",
		map[string]any{"item_id": item.String()}, notecardAsset(text))
}

// NewScript creates a script in inventory and saves source to it.
func (w *Session) NewScript(ctx context.Context, name, source string) (*Item, *UploadResult, error) {
	it, err := w.CreateItem(ctx, name, "created by slgo", int8(AssetLSLText), int8(AssetLSLText))
	if err != nil {
		return nil, nil, err
	}
	res, err := w.SaveScript(ctx, it.ID, source)
	if err != nil {
		return nil, nil, err
	}
	return it, res, nil
}

// PutInObject copies an inventory item into an object.
//
// An object keeps every copy it is given and renames the newcomer, so
// putting the same item in twice leaves "thing" and "thing 1".
func (w *Session) PutInObject(ctx context.Context, o *Object, it *Item) error {
	m := &msg.UpdateTaskInventory{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.UpdateData.LocalID = o.Local
	d := &m.InventoryData
	d.ItemID, d.FolderID = it.ID, it.ParentID
	d.CreatorID, d.OwnerID, d.GroupID = it.CreatorID, it.OwnerID, it.GroupID
	d.BaseMask, d.OwnerMask = it.BaseMask, it.OwnerMask
	d.GroupMask, d.EveryoneMask = it.GroupMask, it.EveryoneMask
	d.NextOwnerMask = it.NextOwnerMask
	d.Type, d.InvType = int8(it.Type), int8(it.InvType)
	d.Flags = it.Flags
	d.SaleType, d.SalePrice = uint8(it.SaleType), int32(it.SalePrice)
	d.Name = append([]byte(it.Name), 0)
	d.Description = append([]byte(it.Desc), 0)
	return w.Send(ctx, m)
}

// RemoveFromObject deletes an item from inside an object.
func (w *Session) RemoveFromObject(ctx context.Context, o *Object, item msg.UUID) error {
	m := &msg.RemoveTaskInventory{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.InventoryData.LocalID = o.Local
	m.InventoryData.ItemID = item
	return w.Send(ctx, m)
}

// TaskInventory reads what an object holds.
//
// RequestTaskInventory answers with the name of a file rather than the
// contents, and the file comes over the xfer protocol.  Each reply
// names a fresh file which the transfer deletes when it completes, so
// the remembered name is dropped first: reading it twice asks for a
// file that is gone and the transfer aborts.
func (w *Session) TaskInventory(ctx context.Context, o *Object) ([]TaskItem, error) {
	w.mu.Lock()
	delete(w.taskInv, o.ID)
	delete(w.taskSeen, o.ID)
	w.mu.Unlock()

	req := &msg.RequestTaskInventory{}
	req.AgentData.AgentID, req.AgentData.SessionID = w.agentBlock()
	req.InventoryData.LocalID = o.Local
	if err := w.Send(ctx, req); err != nil {
		return nil, err
	}

	var filename string
	err := w.await(ctx, 20*time.Second, "the inventory of "+o.String(), func() bool {
		if !w.taskSeen[o.ID] {
			return false
		}
		filename = w.taskInv[o.ID]
		return true
	})
	if err != nil {
		return nil, err
	}
	// An empty filename means the object holds nothing.
	if filename == "" {
		return nil, nil
	}
	body, err := w.xfers.Fetch(ctx, w.me, w.sess, filename,
		client.FilePathTaskInventory, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("sl: reading the inventory of %s: %w", o, err)
	}
	return parseTaskInventory(body), nil
}

// FindInObject looks for something inside an object by name.
func (w *Session) FindInObject(ctx context.Context, o *Object, name string) (*TaskItem, error) {
	items, err := w.TaskInventory(ctx, o)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Name == name {
			return &items[i], nil
		}
	}
	return nil, nil
}

// parseTaskInventory reads the simulator's inventory file format,
// which is nested braces of key and value.
func parseTaskInventory(b []byte) []TaskItem {
	var out []TaskItem
	var cur *TaskItem
	depth := 0
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "inv_item":
			cur, depth = &TaskItem{}, 0
			continue
		case "{":
			depth++
			continue
		case "}":
			depth--
			if cur != nil && depth <= 0 {
				if !cur.ID.IsZero() {
					out = append(out, *cur)
				}
				cur = nil
			}
			continue
		}
		if cur == nil || len(f) < 2 {
			continue
		}
		val := strings.TrimRight(strings.Join(f[1:], " "), "|")
		switch f[0] {
		case "item_id":
			cur.ID, _ = msg.ParseUUID(val)
		case "asset_id", "shadow_id":
			if cur.Asset.IsZero() {
				cur.Asset, _ = msg.ParseUUID(val)
			}
		case "type":
			if cur.Type == "" {
				cur.Type = val
			}
		case "name":
			cur.Name = val
		case "inv_type":
			cur.InvType = val
		case "desc":
			cur.Desc = val
		case "flags":
			cur.Flags = uint32(hexOrDec(val))
		case "creation_date":
			cur.Created = int64(hexOrDec(val))
		case "creator_id":
			cur.CreatorID, _ = msg.ParseUUID(val)
		case "owner_id":
			cur.OwnerID, _ = msg.ParseUUID(val)
		case "group_id":
			cur.GroupID, _ = msg.ParseUUID(val)
		case "base_mask":
			cur.BaseMask = uint32(hexOrDec(val))
		case "owner_mask":
			cur.OwnerMask = uint32(hexOrDec(val))
		case "group_mask":
			cur.GroupMask = uint32(hexOrDec(val))
		case "everyone_mask":
			cur.EveryoneMask = uint32(hexOrDec(val))
		case "next_owner_mask":
			cur.NextOwnerMask = uint32(hexOrDec(val))
		case "sale_type":
			cur.SaleType = val
		case "sale_price":
			cur.SalePrice = int32(hexOrDec(val))
		}
	}
	return out
}

// ReadAsset fetches the bytes behind an item.
//
// The ViewerAsset capability serves the content delivery network and
// answers 403 for a notecard or a script, so these come over the UDP
// transfer protocol instead.  Task is zero for something in agent
// inventory.
func (w *Session) ReadAsset(ctx context.Context, ref client.AssetRef, timeout time.Duration) ([]byte, error) {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if ref.Owner.IsZero() {
		ref.Owner = w.me
	}
	return w.transfers.Fetch(ctx, w.me, w.sess, ref, timeout)
}

// ReadTaskAsset fetches the bytes of something inside an object.
func (w *Session) ReadTaskAsset(ctx context.Context, o *Object, it *TaskItem, assetType int32, timeout time.Duration) ([]byte, error) {
	return w.ReadAsset(ctx, client.AssetRef{
		Owner: w.me, Task: o.ID, Item: it.ID, Asset: it.Asset, Type: assetType,
	}, timeout)
}

// notecardAsset wraps text in the notecard container format.
func notecardAsset(text string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Linden text version 2\n{\nLLEmbeddedItems version 1\n{\ncount 0\n}\n")
	fmt.Fprintf(&b, "Text length %d\n%s}\n", len(text), text)
	return b.Bytes()
}

// hexOrDec reads a number from the contents file.
//
// The masks are written in hex without an 0x, the dates and prices in
// decimal, and nothing in the file says which is which -- so hex is
// tried first, since every decimal string of digits is also a valid hex
// one and the masks are the values a wrong reading would corrupt.
func hexOrDec(s string) uint64 {
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseUint(s, 16, 64); err == nil {
		return n
	}
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

// RenameInObject renames an item inside a rezzed object.
//
// This is what makes an object reusable under a different name: the same
// prim and the same scripts, called something else, rather than building
// again. The object's own name is SetName; this is for what is inside
// it.
//
// The whole item goes back, not just the name, because that is what the
// message carries -- anything left out is set to zero, which for a
// permission mask means taking the rights away.
func (w *Session) RenameInObject(ctx context.Context, o *Object, it TaskItem, name string) error {
	if name == "" {
		return fmt.Errorf("sl: a name is needed")
	}
	m := &msg.UpdateTaskInventory{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.UpdateData.LocalID = o.Local
	m.UpdateData.Key = 0 // 0 selects the object's inventory

	d := &m.InventoryData
	d.ItemID = it.ID
	d.CreatorID, d.OwnerID, d.GroupID = it.CreatorID, it.OwnerID, it.GroupID
	d.BaseMask, d.OwnerMask = it.BaseMask, it.OwnerMask
	d.GroupMask, d.EveryoneMask = it.GroupMask, it.EveryoneMask
	d.NextOwnerMask = it.NextOwnerMask
	d.Type, d.InvType = assetTypeNumber(it.Type), assetTypeNumber(firstNonEmptyStr(it.InvType, it.Type))
	d.Flags = it.Flags
	d.CreationDate = int32(it.Created)
	d.SalePrice = it.SalePrice
	d.Name = append([]byte(name), 0)
	d.Description = append([]byte(it.Desc), 0)
	return w.Send(ctx, m)
}

// assetTypeNumber turns the word the contents file uses back into the
// number the protocol wants. An unknown word is reported as -1, which is
// Second Life's own "unknown".
func assetTypeNumber(word string) int8 {
	switch word {
	case "texture":
		return 0
	case "sound":
		return 1
	case "callcard":
		return 2
	case "landmark":
		return 3
	case "script":
		return 4
	case "clothing":
		return 5
	case "object":
		return 6
	case "notecard":
		return 7
	case "category", "root":
		return 8
	case "lsltext", "lsl":
		return 10
	case "bodypart":
		return 13
	case "snapshot":
		return 15
	case "attach":
		return 17
	case "wearable":
		return 18
	case "animatn", "animation":
		return 20
	case "gesture":
		return 21
	case "mesh":
		return 49
	}
	return -1
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
