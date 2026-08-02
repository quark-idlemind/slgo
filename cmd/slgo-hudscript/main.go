// Command slgo-hudscript puts a script into an attachment and listens
// for it to run.
//
// An attachment belongs to the avatar rather than to the ground it is
// standing on, so its scripts run where an in-world object's would not.
// That is what the HUD is for: the no-script parcel has ALLOW_OTHER_SCRIPTS
// off, and rezzing a scripted prim there gets a script that compiles
// and then sits still.
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

	"slgo/agent"
	"slgo/client"
	"slgo/llsd"
	"slgo/msg"
)

var (
	addr    = flag.String("server", "127.0.0.1:7807", "slgod address")
	profile = flag.String("agent", "example", "hosted agent")
	wear    = flag.String("hud", "Test HUD", "name of the attachment to script")
	listen  = flag.Duration("listen", 45*time.Second, "how long to listen after")
)

const (
	assetScript = 10
	invScript   = 10
	target      = "mono"
	scriptName  = "slgo hud script"

	// hudCenter1 is where slgo-hud put it.
	hudCenter1 = 35
)

// script reports through llOwnerSay, which only the owner hears.
// llSay would put this in open chat, where everyone within twenty
// metres has to read our test output.
const script = `default
{
    state_entry()
    {
        llOwnerSay("slgo: state_entry ran in the HUD");
        llSetTimerEvent(10.0);
    }

    timer()
    {
        llOwnerSay("slgo: timer fired, so it is still running");
    }
}
`

type run struct {
	c    *client.Conn
	me   msg.UUID
	sess msg.UUID

	mu       sync.Mutex
	created  map[uint32]*msg.UpdateCreateInventoryItem_InventoryData
	attached map[msg.UUID]*attachment // by object id
	names    map[msg.UUID]string
	taskInv  map[msg.UUID]string
	xfers    *client.Xfers
}

type attachment struct {
	object msg.UUID
	local  uint32
	item   msg.UUID
	point  int
}

type taskItem struct {
	ItemID msg.UUID
	Type   string
	Name   string
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
		"ObjectUpdate", "ObjectPropertiesFamily", "UpdateCreateInventoryItem",
		"ReplyTaskInventory", "SendXferPacket", "AbortXfer",
		"ChatFromSimulator", "AlertMessage", "ScriptRunningReply",
		"ScriptQuestion", "ScriptDialog")
	if err != nil {
		log.Fatal(err)
	}
	r := &run{
		c: c, me: msg.MustParseUUID(info.AgentId),
		sess:     msg.MustParseUUID(info.SessionId),
		created:  map[uint32]*msg.UpdateCreateInventoryItem_InventoryData{},
		attached: map[msg.UUID]*attachment{},
		names:    map[msg.UUID]string{},
		taskInv:  map[msg.UUID]string{},
	}
	r.xfers = client.NewXfers(c)
	fmt.Printf("%s in %s\n\n", info.AvatarName, info.Region)

	go r.read(ctx)

	// Find the attachment.  Its name does not come with the object
	// update, so each candidate has to be asked about.
	//
	// A worn attachment is only described when it is attached, and a
	// client sees what the server relays from the moment it connects,
	// so something worn before this program started may never be
	// mentioned.  Re-attaching from inventory makes the simulator
	// describe it again, which is the only way to learn its local id.
	//
	// It is matched on the inventory item it came from, which every
	// attachment carries in its NameValue.  Matching on the object's
	// name would mean a round trip per candidate to learn the names,
	// and an attachment does not reliably answer that.
	fmt.Printf("looking for %q\n", *wear)
	want := r.inventoryItem(ctx, msg.MustParseUUID(info.InventoryRoot), *wear)
	fmt.Printf("  inventory item %s\n", want.ID)

	hud := r.findAttachment(want.ID, 15*time.Second)
	if hud == nil {
		fmt.Println("  not mentioned; taking it off and putting it back on")
		r.reattach(ctx, want)
		hud = r.findAttachment(want.ID, 40*time.Second)
	}
	if hud == nil {
		log.Fatalf("no attachment for item %s; is it still worn?", want.ID)
	}
	fmt.Printf("  object %s\n  local  %d\n  item   %s\n",
		hud.object, hud.local, hud.item)

	// If the script is already in there, reuse it.  Copying one in
	// every run piles up "slgo hud script 1", "2", "3": an object
	// keeps every copy and renames the duplicates.
	task := r.findTaskItem(ctx, hud, scriptName)
	if task == nil {
		item := r.createScript(ctx, scriptName)
		fmt.Printf("\nscript item %s\n", item.ItemID)
		if _, err := r.upload(ctx, "UpdateScriptAgent",
			map[string]any{"target": target}, item.ItemID, []byte(script)); err != nil {
			log.Fatalf("saving to inventory: %v", err)
		}
		r.intoTask(ctx, hud.local, item)
		time.Sleep(6 * time.Second)

		// The copy inside the object has its own item id, which is
		// what the script capability wants.
		task = r.findTaskItem(ctx, hud, scriptName)
	}
	if task == nil {
		log.Fatal("the script did not land in the attachment")
	}
	fmt.Printf("in the HUD as item %s\n", task.ItemID)

	fmt.Println("\ncompiling it in place, set to run")
	body, err := r.upload(ctx, "UpdateScriptTask", map[string]any{
		"task_id":           hud.object.String(),
		"is_script_running": true,
		"target":            target,
	}, task.ItemID, []byte(script))
	if err != nil {
		log.Fatalf("compiling: %v", err)
	}
	m := llsd.Map(mustLLSD(body))
	fmt.Printf("  compiled %v\n", llsd.Bool(m, "compiled"))
	if errs, ok := m["errors"].([]any); ok {
		for _, e := range errs {
			fmt.Printf("  %v\n", e)
		}
	}
	if !llsd.Bool(m, "compiled") {
		log.Fatal("it did not compile, so nothing can be concluded about running")
	}

	// Ask the simulator whether it considers the script running, which
	// is a different question from whether it says anything.
	q := &msg.GetScriptRunning{}
	q.Script.ObjectID = hud.object
	q.Script.ItemID = task.ItemID
	r.send(ctx, q)

	fmt.Printf("\nlistening for %s\n", *listen)
	fmt.Println("-----")
	time.Sleep(*listen)
	fmt.Println("-----")
	fmt.Println("done")
}

func (r *run) read(ctx context.Context) {
	for m := range r.c.Messages() {
		if r.xfers.Handle(ctx, m) {
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
				o := &t.ObjectData[i]
				if item, ok := attachItem(o.NameValue); ok {
					r.attached[o.FullID] = &attachment{
						object: o.FullID, local: o.ID, item: item,
						point: int((o.State&0xf0)>>4 | (o.State&0x0f)<<4),
					}
				}
			}
			r.mu.Unlock()
		case *msg.ObjectPropertiesFamily:
			r.mu.Lock()
			r.names[t.ObjectData.ObjectID] = nul(t.ObjectData.Name)
			r.mu.Unlock()
		case *msg.UpdateCreateInventoryItem:
			r.mu.Lock()
			for i := range t.InventoryData {
				d := t.InventoryData[i]
				r.created[d.CallbackID] = &d
			}
			r.mu.Unlock()
		case *msg.ReplyTaskInventory:
			r.mu.Lock()
			r.taskInv[t.InventoryData.TaskID] = nul(t.InventoryData.Filename)
			r.mu.Unlock()
		case *msg.ChatFromSimulator:
			fmt.Printf("  [chat/%d] %s: %s\n", t.ChatData.ChatType,
				nul(t.ChatData.FromName), nul(t.ChatData.Message))
		case *msg.AlertMessage:
			fmt.Printf("  [alert] %s\n", nul(t.AlertData.Message))
		case *msg.ScriptRunningReply:
			fmt.Printf("  [running] script %s: %v\n",
				t.Script.ItemID, t.Script.Running)
		case *msg.ScriptQuestion:
			fmt.Printf("  [question] %s wants %#x\n",
				nul(t.Data.ObjectName), t.Data.Questions)
		}
	}
}

// findAttachment waits for a worn object that came from a given
// inventory item.
func (r *run) findAttachment(item msg.UUID, timeout time.Duration) *attachment {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		var hit *attachment
		for _, a := range r.attached {
			if a.item == item {
				hit = a
			}
		}
		r.mu.Unlock()
		if hit != nil {
			return hit
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

// inventoryItem finds an item by name in the Objects folder.
func (r *run) inventoryItem(ctx context.Context, root msg.UUID, want string) *agent.Item {
	inv := agent.NewInventory(root)
	if err := agent.FetchFolder(ctx, r.c, inv, root); err != nil {
		log.Fatalf("reading the inventory root: %v", err)
	}
	objects, ok := inv.FindFolder("Objects")
	if !ok {
		log.Fatal("no Objects folder")
	}
	if err := agent.FetchFolder(ctx, r.c, inv, objects.ID); err != nil {
		log.Fatalf("reading Objects: %v", err)
	}
	for _, cand := range inv.Contents(objects.ID) {
		if cand.Name == want {
			return cand
		}
	}
	log.Fatalf("no inventory item named %q in Objects", want)
	return nil
}

// reattach takes the object off and puts it back on, so the simulator
// describes it while we are listening.
func (r *run) reattach(ctx context.Context, it *agent.Item) {
	off := &msg.DetachAttachmentIntoInv{}
	off.ObjectData.AgentID = r.me
	off.ObjectData.ItemID = it.ID
	r.send(ctx, off)
	time.Sleep(10 * time.Second)

	m := &msg.RezSingleAttachmentFromInv{}
	m.AgentData.AgentID, m.AgentData.SessionID = r.me, r.sess
	d := &m.ObjectData
	d.ItemID, d.OwnerID = it.ID, r.me
	// No add bit: replace whatever is on the point, so re-running this
	// does not pile up copies.
	d.AttachmentPt = hudCenter1
	d.ItemFlags = it.Flags
	d.GroupMask, d.EveryoneMask = it.GroupMask, it.EveryoneMask
	d.NextOwnerMask = it.NextOwnerMask
	d.Name = append([]byte(it.Name), 0)
	d.Description = append([]byte(it.Desc), 0)
	r.send(ctx, m)
}

func (r *run) findTaskItem(ctx context.Context, hud *attachment, want string) *taskItem {
	req := &msg.RequestTaskInventory{}
	req.AgentData.AgentID, req.AgentData.SessionID = r.me, r.sess
	req.InventoryData.LocalID = hud.local
	r.send(ctx, req)

	var filename string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		filename = r.taskInv[hud.object]
		r.mu.Unlock()
		if filename != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if filename == "" {
		return nil
	}
	body, err := r.xfers.Fetch(ctx, r.me, r.sess, filename,
		client.FilePathTaskInventory, 30*time.Second)
	if err != nil {
		log.Printf("reading the HUD inventory: %v", err)
		return nil
	}
	items := parseTaskInventory(body)
	fmt.Printf("  the HUD holds %d items:\n", len(items))
	for _, it := range items {
		fmt.Printf("    %-24s %-10s %s\n", it.Name, it.Type, it.ItemID)
	}
	for _, it := range items {
		if it.Name == want {
			return &it
		}
	}
	return nil
}

func (r *run) send(ctx context.Context, m msg.Message) {
	if err := r.c.Send(ctx, m, true); err != nil {
		log.Fatalf("send %s: %v", m.MsgInfo().Name, err)
	}
}

func (r *run) createScript(ctx context.Context, name string) *msg.UpdateCreateInventoryItem_InventoryData {
	cb := uint32(time.Now().UnixNano() & 0x7fffffff)
	m := &msg.CreateInventoryItem{}
	m.AgentData.AgentID, m.AgentData.SessionID = r.me, r.sess
	b := &m.InventoryBlock
	b.CallbackID = cb
	b.NextOwnerMask = 0x0008e000
	b.Type, b.InvType = assetScript, invScript
	b.Name = append([]byte(name), 0)
	b.Description = append([]byte("slgo hud script"), 0)
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

func (r *run) intoTask(ctx context.Context, local uint32, it *msg.UpdateCreateInventoryItem_InventoryData) {
	m := &msg.UpdateTaskInventory{}
	m.AgentData.AgentID, m.AgentData.SessionID = r.me, r.sess
	m.UpdateData.LocalID = local
	d := &m.InventoryData
	d.ItemID, d.FolderID = it.ItemID, it.FolderID
	d.CreatorID, d.OwnerID, d.GroupID = it.CreatorID, it.OwnerID, it.GroupID
	d.BaseMask, d.OwnerMask = it.BaseMask, it.OwnerMask
	d.GroupMask, d.EveryoneMask, d.NextOwnerMask = it.GroupMask, it.EveryoneMask, it.NextOwnerMask
	d.GroupOwned = it.GroupOwned
	d.Type, d.InvType = it.Type, it.InvType
	d.Flags, d.SaleType, d.SalePrice = it.Flags, it.SaleType, it.SalePrice
	d.Name, d.Description = it.Name, it.Description
	d.CreationDate, d.CRC = it.CreationDate, it.CRC
	r.send(ctx, m)
}

func (r *run) upload(ctx context.Context, capName string, extra map[string]any,
	item msg.UUID, body []byte) ([]byte, error) {

	fields := map[string]any{"item_id": item.String()}
	for k, v := range extra {
		fields[k] = v
	}
	req, err := llsd.Encode(fields)
	if err != nil {
		return nil, err
	}
	resp, err := r.c.DoCap(ctx, agent.CapRequest{
		Cap: capName, Method: "POST", Type: "application/llsd+xml", Body: req,
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, fmt.Errorf("%s: status %d: %s", capName, resp.Status, snippet(resp.Body, 300))
	}
	uploader := llsd.String(llsd.Map(mustLLSD(resp.Body)), "uploader")
	if uploader == "" {
		return nil, fmt.Errorf("%s: no uploader in %s", capName, snippet(resp.Body, 300))
	}
	resp, err = r.c.DoCap(ctx, agent.CapRequest{
		URL: uploader, Method: "POST", Type: "application/octet-stream", Body: body,
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, fmt.Errorf("upload: status %d: %s", resp.Status, snippet(resp.Body, 300))
	}
	return resp.Body, nil
}

func attachItem(nv []byte) (msg.UUID, bool) {
	for _, line := range strings.Split(nul(nv), "\n") {
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
			cur, depth = &taskItem{}, 0
			continue
		case "{":
			depth++
			continue
		case "}":
			depth--
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

func mustLLSD(b []byte) any {
	v, err := llsd.Decode(bytes.NewReader(b))
	if err != nil {
		log.Fatalf("reply is not LLSD: %v\n%s", err, snippet(b, 300))
	}
	return v
}

func snippet(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return strings.TrimSpace(string(b))
}

func nul(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}
