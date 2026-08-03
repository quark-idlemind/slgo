// Command slgo-asset creates a notecard and a script in inventory,
// uploads their contents, puts them inside a prim, reads the prim's
// inventory back, and pulls the notecard out again to check it survived.
//
// All of it is client side.  The server relays messages it does not
// decode and proxies capability requests it does not understand.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

var (
	addr    = flag.String("server", "127.0.0.1:7806", "slgod address")
	profile = flag.String("agent", "example", "hosted agent")
	at      = flag.String("at", "194,207,27", "where to rez, region local")
)

// Inventory asset types, from the viewer's llassettype.h.
const (
	assetNotecard = 7
	assetScript   = 10 // LSL text
	invNotecard   = 7
	invScript     = 10
)

// notecardAsset wraps text in the notecard container format.  A
// notecard is not stored as plain text: the viewer will not open one
// that is missing this.
func notecardAsset(text string) []byte {
	var b bytes.Buffer
	b.WriteString("Linden text version 2\n{\n")
	b.WriteString("LLEmbeddedItems version 1\n{\ncount 0\n}\n")
	fmt.Fprintf(&b, "Text length %d\n", len(text))
	b.WriteString(text)
	b.WriteString("}\n")
	return b.Bytes()
}

type run struct {
	c    *client.Conn
	me   msg.UUID
	sess msg.UUID
	root msg.UUID

	mu      sync.Mutex
	created map[uint32]*msg.UpdateCreateInventoryItem_InventoryData
	byName  map[string]itemRef
	seen    map[msg.UUID]uint32
	taskInv map[msg.UUID]string // task id -> inventory filename
	owners  map[msg.UUID]msg.UUID
	xfers   *client.Xfers
	assets  *client.Transfers
}

func main() {
	flag.Parse()
	ctx := context.Background()

	c, err := client.Dial(ctx, *addr)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	info, err := c.Attach(ctx, *profile,
		"UpdateCreateInventoryItem", "ObjectUpdate", "ObjectPropertiesFamily",
		"ReplyTaskInventory", "SendXferPacket", "AbortXfer",
		"BulkUpdateInventory", "TransferInfo", "TransferPacket",
		"ChatFromSimulator", "AlertMessage")
	if err != nil {
		log.Fatal(err)
	}
	r := &run{
		c:       c,
		me:      msg.MustParseUUID(info.AgentId),
		sess:    msg.MustParseUUID(info.SessionId),
		root:    msg.MustParseUUID(info.InventoryRoot),
		created: map[uint32]*msg.UpdateCreateInventoryItem_InventoryData{},
		byName:  map[string]itemRef{},
		seen:    map[msg.UUID]uint32{},
		taskInv: map[msg.UUID]string{},
		owners:  map[msg.UUID]msg.UUID{},
	}
	r.xfers = client.NewXfers(c)
	r.assets = client.NewTransfers(c)
	fmt.Printf("%s in %s\n\n", info.AvatarName, info.Region)

	go r.read(ctx)
	time.Sleep(8 * time.Second) // let the interest list fill

	// 1. A prim to put things in.
	var x, y, z float32
	fmt.Sscanf(*at, "%f,%f,%f", &x, &y, &z)
	prim := r.rez(ctx, msg.Vector3{X: x, Y: y, Z: z})
	if prim == nil {
		log.Fatal("no prim appeared")
	}
	fmt.Printf("prim %s  local %d\n\n", prim.id, prim.local)

	// 2. A notecard, with contents.
	const cardText = "slgo notecard test\nline two\nthe quick brown fox\n"
	card := r.createItem(ctx, "slgo card", "written by slgo", assetNotecard, invNotecard)
	fmt.Printf("notecard item %s\n", card.ItemID)
	cardAsset, err := r.upload(ctx, "UpdateNotecardAgentInventory", card.ItemID, msg.UUID{}, notecardAsset(cardText))
	if err != nil {
		log.Fatalf("notecard upload: %v", err)
	}
	fmt.Printf("  contents uploaded, asset %s\n", cardAsset)

	// 3. A script, with contents.  It will not run: the no-script parcel has
	//    ALLOW_OTHER_SCRIPTS off.
	const scriptText = "default\n{\n    state_entry()\n    {\n        llOwnerSay(\"slgo script\");\n    }\n}\n"
	script := r.createItem(ctx, "slgo script alpha", "written by slgo", assetScript, invScript)
	fmt.Printf("script item   %s\n", script.ItemID)
	if _, err := r.upload(ctx, "UpdateScriptAgent", script.ItemID, msg.UUID{}, []byte(scriptText)); err != nil {
		fmt.Printf("  script upload failed: %v\n", err)
	} else {
		fmt.Println("  contents uploaded")
	}

	// 4. Put both inside the prim.
	fmt.Println("\nputting both into the prim")
	r.intoTask(ctx, prim.local, card)
	r.intoTask(ctx, prim.local, script)
	time.Sleep(5 * time.Second)

	// 5. Read the prim's inventory back.
	fmt.Println("\nreading the prim's inventory")
	items, err := r.taskInventory(ctx, prim)
	if err != nil {
		log.Fatalf("  %v", err)
	}
	var taskCard *taskItem
	for i := range items {
		it := &items[i]
		fmt.Printf("  %-20s %-10s item %s\n", it.Name, it.Type, it.ItemID)
		if it.Name == "slgo card" {
			taskCard = it
		}
	}
	if taskCard == nil {
		log.Fatal("the notecard is not in the prim")
	}

	// 6. Edit the notecard where it now sits, inside the prim.
	const edited = "slgo notecard test, edited in world\nsecond line\nzork\n"
	fmt.Println("\nediting the notecard inside the prim")
	// The task's own item id, not the agent inventory one.
	if _, err := r.upload(ctx, "UpdateNotecardTaskInventory", taskCard.ItemID, prim.id, notecardAsset(edited)); err != nil {
		fmt.Printf("  edit failed: %v\n", err)
	} else {
		fmt.Println("  edited")
	}
	time.Sleep(4 * time.Second)

	// 7. Pull a copy back out and read it, to see whether the edit
	//    really took.
	fmt.Println("\npulling the notecard back into inventory")
	mv := &msg.MoveTaskInventory{}
	mv.AgentData.AgentID, mv.AgentData.SessionID = r.me, r.sess
	mv.AgentData.FolderID = r.root
	mv.InventoryData.LocalID = prim.local
	mv.InventoryData.ItemID = taskCard.ItemID
	r.send(ctx, mv)

	// The asset changes when the notecard is edited, so wait for one
	// that is not the asset we uploaded.
	back := r.awaitItem(ctx, "slgo card", cardAsset, 40*time.Second)
	if back == nil {
		fmt.Println("  the simulator never confirmed the move")
		fmt.Println("\ndone")
		return
	}
	fmt.Printf("  back as item %s, asset %s\n", back.item, back.asset)

	body, err := r.fetchAsset(ctx, back.item, back.asset, client.AssetNotecard)
	if err != nil {
		fmt.Printf("  could not read it: %v\n", err)
		fmt.Println("\ndone")
		return
	}
	text, ok := notecardText(body)
	if !ok {
		fmt.Printf("  %d bytes, but not a notecard container\n", len(body))
		fmt.Println("\ndone")
		return
	}
	fmt.Printf("  %d bytes read back\n\n--- contents ---\n%s----------------\n", len(body), text)
	if text == edited {
		fmt.Println("MATCHES what was written in world")
	} else {
		fmt.Printf("DIFFERS from what was written:\n%q\nvs\n%q\n", text, edited)
	}

	fmt.Println("\ndone")
}

// awaitItem waits for the simulator to mention an inventory item by
// name with an asset other than the one given, which is how a copy
// edited in world announces itself on the way back.
func (r *run) awaitItem(ctx context.Context, name string, not msg.UUID, d time.Duration) *itemRef {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		it, ok := r.byName[name]
		r.mu.Unlock()
		if ok && !it.asset.IsZero() && it.asset != not {
			return &it
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

// itemRef is an inventory item and the asset behind it.
type itemRef struct {
	item  msg.UUID
	asset msg.UUID
}

type found struct {
	id    msg.UUID
	local uint32
}

func (r *run) read(ctx context.Context) {
	for m := range r.c.Messages() {
		if r.xfers.Handle(ctx, m) || r.assets.Handle(m) {
			continue
		}
		v, err := m.Decode()
		if err != nil || v == nil {
			continue
		}
		switch t := v.(type) {
		case *msg.ObjectUpdate:
			r.mu.Lock()
			for i := range t.ObjectData {
				r.seen[t.ObjectData[i].FullID] = t.ObjectData[i].ID
			}
			r.mu.Unlock()
		case *msg.UpdateCreateInventoryItem:
			r.mu.Lock()
			for i := range t.InventoryData {
				d := t.InventoryData[i]
				r.created[d.CallbackID] = &d
				r.byName[nul(d.Name)] = itemRef{d.ItemID, d.AssetID}
			}
			r.mu.Unlock()
		case *msg.BulkUpdateInventory:
			// A move out of a prim is announced here rather
			// than by UpdateCreateInventoryItem.
			r.mu.Lock()
			for i := range t.ItemData {
				d := t.ItemData[i]
				r.byName[nul(d.Name)] = itemRef{d.ItemID, d.AssetID}
			}
			r.mu.Unlock()
		case *msg.ObjectPropertiesFamily:
			r.mu.Lock()
			r.owners[t.ObjectData.ObjectID] = t.ObjectData.OwnerID
			r.mu.Unlock()
		case *msg.ReplyTaskInventory:
			r.mu.Lock()
			r.taskInv[t.InventoryData.TaskID] = nul(t.InventoryData.Filename)
			r.mu.Unlock()
		case *msg.ChatFromSimulator:
			fmt.Printf("  [chat] %s: %s\n", nul(t.ChatData.FromName), nul(t.ChatData.Message))
		case *msg.AlertMessage:
			fmt.Printf("  [alert] %s\n", nul(t.AlertData.Message))
		}
	}
}

func (r *run) send(ctx context.Context, m msg.Message) {
	if err := r.c.Send(ctx, m, true); err != nil {
		log.Fatalf("send %s: %v", m.MsgInfo().Name, err)
	}
}

func (r *run) rez(ctx context.Context, at msg.Vector3) *found {
	before := map[uint32]bool{}
	r.mu.Lock()
	for _, l := range r.seen {
		before[l] = true
	}
	r.mu.Unlock()

	m := &msg.ObjectAdd{}
	m.AgentData.AgentID, m.AgentData.SessionID = r.me, r.sess
	d := &m.ObjectData
	d.PCode, d.Material, d.AddFlags = 9, 3, 2
	d.PathCurve, d.ProfileCurve = 16, 1
	d.PathScaleX, d.PathScaleY = 100, 100
	d.BypassRaycast = 1
	d.RayStart, d.RayEnd = at, at
	d.Scale = msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}
	r.send(ctx, m)
	time.Sleep(6 * time.Second)

	// Objects stream in the whole time, so "new to us" is not the
	// same as "ours".  Taking the first unseen local id picked up a
	// stranger's prim and tried to put a notecard in it, which the
	// simulator answered with "Unable to edit this!".
	var fresh []found
	r.mu.Lock()
	for id, local := range r.seen {
		if local != 0 && !before[local] {
			fresh = append(fresh, found{id: id, local: local})
		}
	}
	r.mu.Unlock()

	for _, f := range fresh {
		q := &msg.RequestObjectPropertiesFamily{}
		q.AgentData.AgentID, q.AgentData.SessionID = r.me, r.sess
		q.ObjectData.ObjectID = f.id
		r.send(ctx, q)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, f := range fresh {
			if r.owners[f.id] == r.me {
				r.mu.Unlock()
				return &f
			}
		}
		r.mu.Unlock()
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

// createItem makes an empty item in inventory.  The simulator answers
// with UpdateCreateInventoryItem carrying the item id we then upload to.
func (r *run) createItem(ctx context.Context, name, desc string, aType, iType int8) *msg.UpdateCreateInventoryItem_InventoryData {
	cb := uint32(time.Now().UnixNano() & 0x7fffffff)
	m := &msg.CreateInventoryItem{}
	m.AgentData.AgentID, m.AgentData.SessionID = r.me, r.sess
	b := &m.InventoryBlock
	b.CallbackID = cb
	b.FolderID = msg.UUID{} // the simulator files it by type
	b.TransactionID = msg.UUID{}
	b.NextOwnerMask = 0x0008e000
	b.Type, b.InvType = aType, iType
	b.WearableType = 0
	b.Name = append([]byte(name), 0)
	b.Description = append([]byte(desc), 0)
	r.send(ctx, m)

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		d := r.created[cb]
		r.mu.Unlock()
		if d != nil {
			return d
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Fatalf("the simulator never confirmed creating %q", name)
	return nil
}

// upload puts an asset's bytes behind an existing inventory item.
//
// It is two steps: post the item id to a capability, which answers with
// a one-shot uploader URL, then post the bytes there.  The second step
// is why the server's capability proxy takes an absolute URL.
func (r *run) upload(ctx context.Context, capName string, item, task msg.UUID, body []byte) (msg.UUID, error) {
	fields := map[string]any{"item_id": item.String()}
	if !task.IsZero() {
		// Editing the copy inside a prim rather than the one in
		// inventory: same capability, one more field.
		fields["task_id"] = task.String()
	}
	req, err := llsd.Encode(fields)
	if err != nil {
		return msg.UUID{}, err
	}
	resp, err := r.c.DoCap(ctx, agent.CapRequest{Cap: capName, Method: "POST", Type: "application/llsd+xml", Body: req})
	if err != nil {
		return msg.UUID{}, err
	}
	if !resp.OK() {
		return msg.UUID{}, fmt.Errorf("%s: status %d: %s", capName, resp.Status, snippet(resp.Body))
	}
	v, err := llsd.Decode(bytes.NewReader(resp.Body))
	if err != nil {
		return msg.UUID{}, err
	}
	m := llsd.Map(v)
	uploader := llsd.String(m, "uploader")
	if uploader == "" {
		return msg.UUID{}, fmt.Errorf("%s: no uploader in %s", capName, snippet(resp.Body))
	}

	resp, err = r.c.DoCap(ctx, agent.CapRequest{URL: uploader, Method: "POST", Type: "application/octet-stream", Body: body})
	if err != nil {
		return msg.UUID{}, err
	}
	if !resp.OK() {
		return msg.UUID{}, fmt.Errorf("upload: status %d: %s", resp.Status, snippet(resp.Body))
	}
	v, err = llsd.Decode(bytes.NewReader(resp.Body))
	if err != nil {
		return msg.UUID{}, err
	}
	m = llsd.Map(v)
	if st := llsd.String(m, "state"); st != "complete" {
		return msg.UUID{}, fmt.Errorf("upload state %q: %s", st, snippet(resp.Body))
	}
	asset, _ := msg.ParseUUID(llsd.String(m, "new_asset"))
	return asset, nil
}

// fetchAsset reads an asset's bytes over the UDP asset transfer.
//
// The ViewerAsset capability will not do: it serves the content
// delivery network -- textures, meshes, sounds -- and answers 403 for a
// notecard.  A notecard or a script has to come this way, which is also
// the path the C client uses.
func (r *run) fetchAsset(ctx context.Context, item, asset msg.UUID, aType int32) ([]byte, error) {
	return r.assets.Fetch(ctx, r.me, r.sess, client.AssetRef{
		Owner: r.me,
		Item:  item,
		Asset: asset,
		Type:  aType,
	}, 45*time.Second)
}

// notecardText pulls the text back out of the notecard container.
func notecardText(b []byte) (string, bool) {
	s := string(b)
	i := strings.Index(s, "Text length ")
	if i < 0 {
		return "", false
	}
	rest := s[i+len("Text length "):]
	j := strings.IndexByte(rest, '\n')
	if j < 0 {
		return "", false
	}
	var n int
	if _, err := fmt.Sscanf(rest[:j], "%d", &n); err != nil {
		return "", false
	}
	body := rest[j+1:]
	if n > len(body) {
		n = len(body)
	}
	return body[:n], true
}

// intoTask copies an inventory item into a prim.
func (r *run) intoTask(ctx context.Context, local uint32, it *msg.UpdateCreateInventoryItem_InventoryData) {
	m := &msg.UpdateTaskInventory{}
	m.AgentData.AgentID, m.AgentData.SessionID = r.me, r.sess
	m.UpdateData.LocalID = local
	m.UpdateData.Key = 0 // 0 means the item comes from agent inventory
	d := &m.InventoryData
	d.ItemID, d.FolderID = it.ItemID, it.FolderID
	d.CreatorID, d.OwnerID, d.GroupID = it.CreatorID, it.OwnerID, it.GroupID
	d.BaseMask, d.OwnerMask = it.BaseMask, it.OwnerMask
	d.GroupMask, d.EveryoneMask, d.NextOwnerMask = it.GroupMask, it.EveryoneMask, it.NextOwnerMask
	d.GroupOwned = it.GroupOwned
	d.TransactionID = msg.UUID{}
	d.Type, d.InvType = it.Type, it.InvType
	d.Flags, d.SaleType, d.SalePrice = it.Flags, it.SaleType, it.SalePrice
	d.Name, d.Description = it.Name, it.Description
	d.CreationDate = it.CreationDate
	d.CRC = it.CRC
	r.send(ctx, m)
	fmt.Printf("  sent %q\n", nul(it.Name))
}

// taskItem is one entry in a prim's inventory.
//
// The item id here is not the one the agent inventory uses: copying an
// item into a prim gives it a new id, and that is the one every later
// request about it has to name.  Using the agent's id gets a NotFound
// from the simulator.
type taskItem struct {
	ItemID msg.UUID
	Asset  msg.UUID
	Type   string
	Name   string
}

// parseTaskInventory reads the simulator's own inventory notation.
func parseTaskInventory(b []byte) []taskItem {
	var out []taskItem
	var cur *taskItem
	depth := 0
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "inv_item":
			cur = &taskItem{}
			depth = 0
			continue
		case "{":
			depth++
			continue
		case "}":
			depth--
			// The outer close ends the item.  Nested blocks
			// (permissions, sale_info) close first.
			if cur != nil && depth <= 0 {
				if !cur.ItemID.IsZero() {
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
			cur.ItemID, _ = msg.ParseUUID(val)
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
		}
	}
	return out
}

// taskInventory reads a prim's contents.
//
// RequestTaskInventory answers with a filename, not the contents; the
// file itself comes over the xfer protocol.
func (r *run) taskInventory(ctx context.Context, prim *found) ([]taskItem, error) {
	req := &msg.RequestTaskInventory{}
	req.AgentData.AgentID, req.AgentData.SessionID = r.me, r.sess
	req.InventoryData.LocalID = prim.local
	r.send(ctx, req)

	var filename string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		filename = r.taskInv[prim.id]
		r.mu.Unlock()
		if filename != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if filename == "" {
		return nil, fmt.Errorf("no ReplyTaskInventory for %s", prim.id)
	}
	fmt.Printf("  inventory file %q\n", filename)

	body, err := r.xfers.Fetch(ctx, r.me, r.sess, filename, client.FilePathTaskInventory, 30*time.Second)
	if err != nil {
		return nil, err
	}
	fmt.Printf("  %d bytes over xfer\n", len(body))

	items := parseTaskInventory(body)
	if len(items) == 0 {
		return nil, fmt.Errorf("no items in a %d byte inventory file", len(body))
	}
	return items, nil
}

func snippet(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return strings.TrimSpace(string(b))
}

func nul(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}
