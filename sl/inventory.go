package sl

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
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
	ID msg.UUID
	// Asset is the asset id, decoded from the shadow id the file gives
	// in its place for an item the avatar may not copy.
	Asset msg.UUID
	Name  string
	Type  string

	// The rest of what the contents file says. It is read because
	// changing an item in an object means sending the whole item back,
	// so anything not read here would be silently reset to zero.
	InvType     string
	Desc        string
	Flags       uint32
	Created     int64
	CreatorID   msg.UUID
	OwnerID     msg.UUID
	LastOwnerID msg.UUID
	GroupID     msg.UUID
	GroupOwned  bool

	BaseMask      uint32
	OwnerMask     uint32
	GroupMask     uint32
	EveryoneMask  uint32
	NextOwnerMask uint32

	// SaleType is the contents file's word for it: not, orig, copy or
	// cntn.  RenameInObject sends it back as the number the word stands
	// for.
	SaleType  string
	SalePrice int32
}

// Folder finds a folder by name under the inventory root.  The name has
// to pick out one folder, as PickNamed rules; the refusal is a
// *NameError.
func (w *Session) Folder(ctx context.Context, name string) (msg.UUID, error) {
	inv := agent.NewInventory(w.invRoot)
	if err := agent.FetchFolder(ctx, w.b, inv, w.invRoot); err != nil {
		return msg.UUID{}, fmt.Errorf("sl: reading the inventory root: %w", err)
	}
	f, err := inv.FindFolder(name)
	if err != nil {
		return msg.UUID{}, fmt.Errorf("sl: %w", err)
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

// FindItem looks for an item by name in a folder.  The name has to pick
// out one item, as PickNamed rules; the refusal is a *NameError.
func (w *Session) FindItem(ctx context.Context, folder msg.UUID, name string) (*Item, error) {
	items, err := w.FolderItems(ctx, folder)
	if err != nil {
		return nil, err
	}
	it, err := PickNamedFunc(items, name, "item", "in folder "+folder.String(), itemNamed)
	if err != nil {
		return nil, fmt.Errorf("sl: %w", err)
	}
	return it, nil
}

// itemNamed is an inventory item's name and id, for PickNamedFunc.
func itemNamed(it *Item) (string, msg.UUID) { return it.Name, it.ID }

// callbackSeq numbers the items this process asks to be created, as
// the viewer's LLInventoryCallbackManager does (llviewerinventory.cpp:
// 1060-1071).  It starts at a random point rather than at one, because
// slgod relays the reply to every client of the avatar, and two clients
// counting from the same place would each take the other's item.
var callbackSeq atomic.Uint32

func init() {
	u := randomUUID()
	callbackSeq.Store(binary.LittleEndian.Uint32(u[:4]))
}

// nextCallbackID is the id for one creation.  Never zero, which the
// viewer takes as no callback at all, and kept to 31 bits as the ids
// before it were, so that it survives a signed 32 bit integer.
func nextCallbackID() uint32 {
	for {
		if id := callbackSeq.Add(1) & 0x7fffffff; id != 0 {
			return id
		}
	}
}

// CreateItem makes an empty inventory item and waits for the simulator
// to confirm it, which it does by echoing back a callback id.
func (w *Session) CreateItem(ctx context.Context, name, desc string, assetType, invType int8) (*Item, error) {
	cb := nextCallbackID()

	// Waited for from before the request goes, and forgotten when the
	// wait ends however it ends, so that a reply arriving late is not
	// kept for a request that is not waiting.
	w.mu.Lock()
	w.created[cb] = nil
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.created, cb)
		w.mu.Unlock()
	}()

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

	// Errors is what the compiler said, verbatim, in its own form:
	//
	//	(4, 10) : ERROR : Syntax error
	//
	// The two numbers are the line and the column and BOTH COUNT FROM
	// ZERO.  Measured: a bad token on the fifth line of a script is
	// reported as line 4.  It is worth knowing before writing anything
	// that acts on the numbers, and worth knowing for a second reason:
	//
	// An EMPTY upload -- a body that did not arrive -- comes back as
	// "(0, 0) : ERROR : Syntax error", and so does a real syntax error
	// on the first character of a real script.  Measured, both, and they
	// are indistinguishable.  So nothing here may treat (0, 0) as
	// evidence that the upload was empty and ask again: for the person
	// whose script has a typo in its first line, that would be a second
	// upload and the same answer.
	Errors []string

	Body []byte

	// NewItem is the inventory item a file upload made, and is zero
	// for the capabilities that write to an item that already exists.
	NewItem msg.UUID

	// Message is what the capability said when it was unhappy, which
	// it says instead of failing: an upload with no money behind it
	// answers 200 with a state of "error" and the reason here.
	Message string

	// Warnings is what went wrong around an install without spoiling
	// it, as Result.Warnings is for a run: InstallScript's copy in
	// inventory that could not be deleted.
	Warnings []string
}

// uploadRetryWait is how long to leave it before asking a capability
// again that answered with a server error.
//
// Long enough that a service having a bad moment has had one, short
// enough that a person waiting on a script install does not notice.  It
// is a guess: what was measured is that the failures come in bursts
// under load, not how long a burst lasts.
const uploadRetryWait = 500 * time.Millisecond

// upload runs the two step asset upload: describe what is being
// written, then write it to the URL that comes back.
func (w *Session) upload(ctx context.Context, capName string, fields map[string]any, body []byte) (*UploadResult, error) {
	req, err := llsd.Encode(fields)
	if err != nil {
		return nil, err
	}
	first, err := w.describeUpload(ctx, capName, req)
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

// describeUpload is the first half of an upload -- saying what is about
// to be written and being told where -- and asks a second time when the
// far end answers with a server error.
//
// Only this half, and only a 5xx.  The first half writes NOTHING, so
// asking again cannot install, make or charge for anything twice; an
// answer to the second half that went missing may have been an upload
// that landed.  Once, and then the error stands: a far end still failing
// a moment later is having more than a moment.
// Why: doc/scripts.md#asking-the-first-half-of-an-upload-again
func (w *Session) describeUpload(ctx context.Context, capName string, req []byte) ([]byte, error) {
	ask := func() ([]byte, error) {
		return w.capDo(ctx, agent.CapRequest{
			Cap: capName, Method: "POST", Type: "application/llsd+xml", Body: req,
		})
	}

	got, err := ask()
	if err == nil {
		return got, nil
	}
	var ce *CapError
	if !errors.As(err, &ce) || !ce.Temporary() {
		return nil, err
	}

	select {
	case <-time.After(uploadRetryWait):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return ask()
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
//
// When the item was made and the source could not be saved, the item is
// returned with the error: it is in inventory, and only the caller knows
// whether to delete it or save to it again.
func (w *Session) NewScript(ctx context.Context, name, source string) (*Item, *UploadResult, error) {
	it, err := w.CreateItem(ctx, name, "created by slgo", int8(AssetLSLText), int8(AssetLSLText))
	if err != nil {
		return nil, nil, err
	}
	res, err := w.SaveScript(ctx, it.ID, source)
	if err != nil {
		return it, nil, err
	}
	return it, res, nil
}

// PutInObject copies an inventory item into an object.
//
// An object keeps every copy it is given and renames the newcomer, so
// putting the same item in twice leaves "thing" and "thing 1".
//
// The item goes as the viewer builds it (LLViewerObject::updateInventory,
// llviewerobject.cpp:3735-3766): with the object as its folder, its
// creation date, and the checksum taskItemCRC gives, which sums the
// asset and the last owner the message does not carry.
func (w *Session) PutInObject(ctx context.Context, o *Object, it *Item) error {
	local, err := w.local(ctx, o)
	if err != nil {
		return err
	}
	m := &msg.UpdateTaskInventory{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.UpdateData.LocalID = local
	m.UpdateData.Key = 0 // 0 selects the object's inventory
	d := &m.InventoryData
	d.ItemID, d.FolderID = it.ID, o.ID
	d.CreatorID, d.OwnerID, d.GroupID = it.CreatorID, it.OwnerID, it.GroupID
	d.BaseMask, d.OwnerMask = it.BaseMask, it.OwnerMask
	d.GroupMask, d.EveryoneMask = it.GroupMask, it.EveryoneMask
	d.NextOwnerMask = it.NextOwnerMask
	d.Type, d.InvType = int8(it.Type), int8(it.InvType)
	d.Flags = it.Flags
	d.CreationDate = int32(it.Created)
	d.SaleType, d.SalePrice = uint8(it.SaleType), int32(it.SalePrice)
	d.Name = append([]byte(it.Name), 0)
	d.Description = append([]byte(it.Desc), 0)
	d.CRC = taskItemCRC(d, it.AssetID, it.LastOwnerID)
	return w.Send(ctx, m)
}

// RemoveFromObject deletes an item from inside an object.
func (w *Session) RemoveFromObject(ctx context.Context, o *Object, item msg.UUID) error {
	local, err := w.local(ctx, o)
	if err != nil {
		return err
	}
	m := &msg.RemoveTaskInventory{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.InventoryData.LocalID = local
	m.InventoryData.ItemID = item
	return w.Send(ctx, m)
}

// FetchFromObject brings an item out of an object and into a folder of
// inventory, and returns the inventory item it became.  It is what
// dragging one out of an object's Contents tab does, and the viewer's
// Copy to Inventory (llviewerobject.cpp, moveInventory).
//
// The simulator copies an item the avatar may copy, and MOVES one it may
// not: the object no longer holds it afterwards.  Nothing in the message
// says which is wanted, so the only way to keep an object whole is not to
// ask for an item that would be moved; MayCopyOut says which that is.
//
// Nothing answers the message either.  The item is waited for in the
// folder it was sent to, the way a take is, and one that never appears
// is reported as a timeout.
func (w *Session) FetchFromObject(ctx context.Context, o *Object, it TaskItem, folder msg.UUID, timeout time.Duration) (*Item, error) {
	if folder.IsZero() {
		// Zero asks the simulator to choose, and then there is no
		// telling where to look for what it chose.
		return nil, fmt.Errorf("sl: a folder to fetch %q into is needed", it.Name)
	}
	if timeout == 0 {
		timeout = 40 * time.Second
	}
	before, err := w.FolderItems(ctx, folder)
	if err != nil {
		return nil, err
	}
	had := map[msg.UUID]bool{}
	for _, b := range before {
		had[b.ID] = true
	}
	local, err := w.local(ctx, o)
	if err != nil {
		return nil, err
	}

	m := &msg.MoveTaskInventory{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentData.FolderID = folder
	m.InventoryData.LocalID = local
	m.InventoryData.ItemID = it.ID
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
		for _, n := range now {
			if !had[n.ID] && n.Name == it.Name {
				return n, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: %q from %s to appear in inventory", ErrTimeout, it.Name, o)
}

// MayCopyOut says whether fetching an item out of an object would copy
// it, leaving the object as it was.  It is the viewer's test, the same
// two questions in the same order (llinventorybridge.cpp,
// move_inv_category_world_to_agent): may this avatar copy it, and may it
// be passed to this avatar at all.  An item that fails either is moved
// out instead, when it is moved at all.
func MayCopyOut(it TaskItem, me, group msg.UUID) bool {
	var mayCopy bool
	switch {
	case it.OwnerID == me:
		mayCopy = it.OwnerMask&PermCopy != 0
	case !group.IsZero() && it.GroupID == group && it.GroupMask&PermCopy != 0:
		mayCopy = true
	default:
		mayCopy = it.EveryoneMask&PermCopy != 0
	}
	mayPass := it.OwnerID == me || it.OwnerMask&PermTransfer != 0
	return mayCopy && mayPass
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

	local, err := w.local(ctx, o)
	if err != nil {
		return nil, err
	}
	req := &msg.RequestTaskInventory{}
	req.AgentData.AgentID, req.AgentData.SessionID = w.agentBlock()
	req.InventoryData.LocalID = local
	if err := w.Send(ctx, req); err != nil {
		return nil, err
	}

	var filename string
	err = w.await(ctx, 20*time.Second, "the inventory of "+o.String(), func() bool {
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
	body, err := w.xfers.Fetch(ctx, w.me, w.Session(), filename,
		client.FilePathTaskInventory, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("sl: reading the inventory of %s: %w", o, err)
	}
	return parseTaskInventory(body), nil
}

// FindInObject looks for something inside an object by name, matched
// exactly, in the case it has.  An object renames an exact duplicate,
// so there is at most one.  Why: doc/names.md#measured
//
// Nothing of that name is a nil item and no error, because Run and
// InstallScript act on that answer: not finding the script is how they
// decide to put one in.  A caller that means to find something wants
// PickNamed over TaskInventory, which refuses instead.
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
		case "asset_id":
			if cur.Asset.IsZero() {
				cur.Asset, _ = msg.ParseUUID(val)
			}
		case "shadow_id":
			if cur.Asset.IsZero() {
				cur.Asset, _ = msg.ParseUUID(val)
				cur.Asset = unshadow(cur.Asset)
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
		case "last_owner_id":
			cur.LastOwnerID, _ = msg.ParseUUID(val)
		case "group_id":
			cur.GroupID, _ = msg.ParseUUID(val)
		case "group_owned":
			n, _ := strconv.Atoi(val)
			cur.GroupOwned = n != 0
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
	return w.transfers.Fetch(ctx, w.me, w.Session(), ref, timeout)
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

// shadowPad is the viewer's MAGIC_ID (llinventory.cpp:73), which a
// shadow id is the asset id XORed with.
var shadowPad = msg.MustParseUUID("3c115e51-04f4-523c-9fa6-98aff1034730")

// unshadow decodes a shadow id as the viewer does
// (llinventory.cpp:803-808): XOR with shadowPad, byte by byte.
func unshadow(u msg.UUID) msg.UUID {
	for i := range u {
		u[i] ^= shadowPad[i]
	}
	return u
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
// permission mask means taking the rights away and for the sale type
// means taking it off sale.  So it is the item as TaskInventory or
// FindInObject read it, and one whose sale type is not a word the
// contents file uses is refused before anything is sent.  The rest is
// filled as the viewer fills it (llviewerobject.cpp:3735-3766): the
// folder is the object, and the checksum is worked out over the item.
//
// Nothing answers the message, so the contents are read back until the
// item, found by its id, carries the new name.  One that has not by the
// timeout (0 means fifteen seconds) is ErrTimeout; the viewer offers no
// rename of an item the avatar may not modify
// (llpanelobjectinventory.cpp:298-323).  The id is the one it had: the
// viewer, renaming, keeps its own copy of the contents under the same id
// and expects the simulator's to agree (llviewerobject.cpp:2831-2877).
func (w *Session) RenameInObject(ctx context.Context, o *Object, it TaskItem, name string, timeout time.Duration) error {
	if name == "" {
		return fmt.Errorf("sl: a name is needed")
	}
	sale, err := saleTypeNumber(it.SaleType)
	if err != nil {
		return fmt.Errorf("sl: renaming %q in %s: %w", it.Name, o, err)
	}
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	local, err := w.local(ctx, o)
	if err != nil {
		return err
	}
	m := &msg.UpdateTaskInventory{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.UpdateData.LocalID = local
	m.UpdateData.Key = 0 // 0 selects the object's inventory

	d := &m.InventoryData
	d.ItemID, d.FolderID = it.ID, o.ID
	d.CreatorID, d.OwnerID, d.GroupID = it.CreatorID, it.OwnerID, it.GroupID
	d.GroupOwned = it.GroupOwned
	d.BaseMask, d.OwnerMask = it.BaseMask, it.OwnerMask
	d.GroupMask, d.EveryoneMask = it.GroupMask, it.EveryoneMask
	d.NextOwnerMask = it.NextOwnerMask
	d.Type, d.InvType = assetTypeNumber(it.Type), assetTypeNumber(firstNonEmptyStr(it.InvType, it.Type))
	d.Flags = it.Flags
	d.CreationDate = int32(it.Created)
	d.SaleType, d.SalePrice = sale, it.SalePrice
	d.Name = append([]byte(name), 0)
	d.Description = append([]byte(it.Desc), 0)
	d.CRC = taskItemCRC(d, it.Asset, it.LastOwnerID)
	if err := w.Send(ctx, m); err != nil {
		return err
	}

	return poll(ctx, timeout, time.Second, fmt.Sprintf("%q in %s to be called %q; "+
		"the viewer renames nothing the avatar may not modify", it.Name, o, name),
		func(ctx context.Context) (bool, error) {
			items, err := w.TaskInventory(ctx, o)
			if err != nil {
				return false, err
			}
			for _, now := range items {
				if now.ID == it.ID {
					return now.Name == name, nil
				}
			}
			return false, nil
		})
}

// taskItemCRC is the viewer's checksum of an item
// (llinventory.cpp:460-485, llpermissions.cpp:129-137,
// llsaleinfo.cpp:74-79): a sum over what the message carries, and the
// asset and last owner it does not.  The next-owner mask, the name and
// the description are left out, as the viewer leaves them, and an item
// read from an object's contents has no thumbnail to add.  itemCRC, for
// agent inventory, is a different sum; its comment says how.
func taskItemCRC(d *msg.UpdateTaskInventory_InventoryData, asset, lastOwner msg.UUID) uint32 {
	crc := uuidCRC(d.ItemID) + uuidCRC(d.FolderID)
	crc += uuidCRC(d.CreatorID) + uuidCRC(d.OwnerID) + uuidCRC(lastOwner) + uuidCRC(d.GroupID)
	crc += d.BaseMask + d.OwnerMask + d.EveryoneMask + d.GroupMask
	crc += uuidCRC(asset)
	crc += uint32(int32(d.Type)) + uint32(int32(d.InvType)) + d.Flags
	crc += uint32(d.SalePrice) + uint32(d.SaleType)*0x07073096
	crc += uint32(d.CreationDate)
	return crc
}

// saleTypeNumber turns the contents file's word for a sale type back
// into the number the protocol wants, which is the word's place in the
// viewer's FOR_SALE_NAMES (llsaleinfo.cpp:43-49).  The viewer reads a
// word it does not know as "not for sale"; this refuses one, because
// sending that back would take something off sale.
func saleTypeNumber(word string) (uint8, error) {
	for n, known := range []string{"not", "orig", "copy", "cntn"} {
		if word == known {
			return uint8(n), nil
		}
	}
	return 0, fmt.Errorf("the sale type %q is none of not, orig, copy and cntn", word)
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
