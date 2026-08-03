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

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

var (
	addr    = flag.String("server", "127.0.0.1:7807", "slgod address")
	profile = flag.String("agent", "example", "hosted agent")
	wear    = flag.String("hud", "Test HUD", "name of the attachment to script")
	listen  = flag.Duration("listen", 45*time.Second, "how long to listen after")

	// only narrows the run to the scripts whose names contain this.
	// Timing wants it: two timed scripts left in the object run at
	// once, and a script that shares a simulator with another doing
	// the same work is not measuring what it looks like it is.
	only = flag.String("only", "", "only scripts whose name contains this")
)

const (
	assetScript = 10
	invScript   = 10
	target      = "mono"
	// hudCenter1 is where slgo-hud put it.
	hudCenter1 = 35
)

// spec is a script to put in the attachment.
type spec struct {
	name string
	src  string
}

// Timing.
//
// The variants are generated from one template so that the only
// difference between them is the line inside the loop.  Writing them
// out separately would leave the comparison resting on two pieces of
// source being the same everywhere else, which is exactly the sort of
// thing that quietly stops being true.
//
// The accumulator is there so the loop cannot be optimised away when
// the body is otherwise empty, and it is reported so we can see it
// ran.  llResetTime and llGetTime bracket only the loop; the
// llOwnerSay that reports a run happens after the clock is read.
//
// Each script times the loop several times.  One measurement of a
// script sharing a simulator with everything else in the region is
// not worth much.
const timingTemplate = `integer N = %d;
integer R = %d;

default
{
    state_entry()
    {
        string me = llGetScriptName();
        integer r;
        integer i;
        integer acc;
        float t;
        for (r = 0; r < R; ++r)
        {
            acc = 0;
            llResetTime();
            for (i = 0; i < N; ++i)
            {
                acc = acc + i;
%s            }
            t = llGetTime();
            llOwnerSay(me + ": run " + (string)r + ": " + (string)t + " s");
        }
        llOwnerSay(me + ": done, acc " + (string)acc);
    }
}
`

const (
	timingN = 50
	timingR = 5
)

func timingScript(inner string) string {
	return timingScriptN(timingN, inner)
}

func timingScriptN(n int, inner string) string {
	return fmt.Sprintf(timingTemplate, n, timingR, inner)
}

// fastRule sets face 0 through llSetLinkPrimitiveParamsFast, which is
// the call that exists because llSetTexture sleeps.  PRIM_TEXTURE
// wants the face, the texture, then repeats, offsets and rotation.
func fastRule(texture string) string {
	return "                llSetLinkPrimitiveParamsFast(LINK_THIS,\n" +
		"                    [PRIM_TEXTURE, 0, " + texture +
		", <1.0,1.0,0.0>, <0.0,0.0,0.0>, 0.0]);\n"
}

// Every script names itself with llGetScriptName, because nothing else
// will.  ChatFromSimulator identifies the object a line came from and
// not the script inside it, so with more than one script running there
// is no way to tell from the protocol which one spoke.  Saying so in
// the message is the only attribution there is.
var specs = []spec{
	// The loop with nothing in it, which is what "sends none" means.
	{"slgo time none", timingScript("")},

	// The same loop, setting a texture each time round.
	{"slgo time set", timingScript(
		"                llSetTexture(TEXTURE_BLANK, 0);\n")},

	// The same loop again, alternating between two textures.
	//
	// Setting a face to what it already holds may cost nothing, in
	// which case the run above would be measuring a simulator
	// declining to do any work rather than the price of doing it.
	// Alternating guarantees every call is a real change.
	{"slgo time alt", timingScript(
		"                if (i % 2) llSetTexture(TEXTURE_BLANK, 0);\n" +
			"                else llSetTexture(TEXTURE_PLYWOOD, 0);\n")},

	// The same fifty sets through the call that does not sleep.
	{"slgo time fast", timingScript(fastRule("TEXTURE_BLANK"))},

	// And alternating, so this one is not measuring a simulator
	// declining to do the work either.
	{"slgo time fastalt", timingScript(
		"                if (i % 2)\n" + fastRule("TEXTURE_BLANK") +
			"                else\n" + fastRule("TEXTURE_PLYWOOD"))},

	// Fifty fast sets, each to a differently named texture that does
	// not exist, each announced first.
	//
	// The complaint quotes the name it could not find, and the
	// announcement says which one was about to be asked for, so every
	// error can be paired with the exact call that caused it rather
	// than assumed to line up.  What is being watched is whether the
	// pairing survives fifty of them arriving as fast as the script
	// can issue them.
	{"slgo time missfast", `default
{
    state_entry()
    {
        string me = llGetScriptName();
        integer i;
        for (i = 1; i <= 50; ++i)
        {
            string t = "no texture " + (string)i;
            llOwnerSay(me + ": setting " + t);
            llSetLinkPrimitiveParamsFast(LINK_THIS,
                [PRIM_TEXTURE, 0, t, <1.0,1.0,0.0>, <0.0,0.0,0.0>, 0.0]);
        }
        llOwnerSay(me + ": done");
    }
}
`},

	// The same fifty, through llSetTexture instead.
	//
	// This is the control for the run above.  If that one is silent it
	// is either because the fast call does not complain, or because
	// fifty complaints in a fraction of a second went missing
	// somewhere between the simulator and here.  The same loop through
	// a call already known to complain tells those apart, and shows
	// how the two channels interleave when they do both arrive.
	{"slgo time missslow", `default
{
    state_entry()
    {
        string me = llGetScriptName();
        integer i;
        for (i = 1; i <= 50; ++i)
        {
            string t = "no texture " + (string)i;
            llOwnerSay(me + ": setting " + t);
            llSetTexture(t, 0);
        }
        llOwnerSay(me + ": done");
    }
}
`},

	// One missing texture through each call, in that order.
	//
	// The fifty-at-once run cannot settle whether the fast call is
	// silent, because the simulator gags the debug channel after eight
	// errors and a gag from an earlier run could still be in force.
	// Two errors is far under that threshold, and putting both calls
	// in one script means they face identical conditions.  If only the
	// llSetTexture one is reported, the fast call does not report.
	{"slgo time missone", `default
{
    state_entry()
    {
        string me = llGetScriptName();
        llOwnerSay(me + ": fast, about to set 'missing A'");
        llSetLinkPrimitiveParamsFast(LINK_THIS,
            [PRIM_TEXTURE, 0, "missing A", <1.0,1.0,0.0>, <0.0,0.0,0.0>, 0.0]);
        llOwnerSay(me + ": slow, about to set 'missing B'");
        llSetTexture("missing B", 0);
        llOwnerSay(me + ": done");
    }
}
`},

	// Five fast sets to names that do not exist, then a five second
	// sleep, then one last word.
	//
	// This is the question the fifty-at-once run could not answer.
	// Five errors is well under the eight the simulator will send
	// before it gags itself, so nothing is lost to the flood limit,
	// and the sleep holds the script open long enough for a complaint
	// that is raised late to still arrive.  The line after the sleep
	// marks the boundary: anything before it was reported while the
	// script was running, anything after it was not reported at all.
	//
	// If the channel stays empty through both, the fast call really
	// does not complain.  If complaints turn up during the sleep, it
	// complains but not in step with the call, which is the thing
	// llSetTexture does differently.
	{"slgo time fastdefer", `default
{
    state_entry()
    {
        string me = llGetScriptName();
        integer i;
        for (i = 1; i <= 5; ++i)
        {
            string t = "fastmiss " + (string)i;
            llOwnerSay(me + ": setting " + t);
            llSetLinkPrimitiveParamsFast(LINK_THIS,
                [PRIM_TEXTURE, 0, t, <1.0,1.0,0.0>, <0.0,0.0,0.0>, 0.0]);
        }
        llSleep(5.0);
        llOwnerSay(me + ": awake after 5 s sleep");
    }
}
`},

	// Fifty of these may finish inside a single frame, in which case
	// the clock says zero and the cost per call is unknown rather than
	// nothing.  A thousand gives it something to measure.
	{"slgo time fast1k", timingScriptN(1000, fastRule("TEXTURE_BLANK"))},
}

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

	// hud is the attachment object, so a chat line can be checked
	// against it rather than trusted because the name looked right.
	hud msg.UUID
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

	if *only != "" {
		var keep []spec
		for _, s := range specs {
			if strings.Contains(s.name, *only) {
				keep = append(keep, s)
			}
		}
		if len(keep) == 0 {
			log.Fatalf("no script name contains %q", *only)
		}
		specs = keep
	}

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

	r.mu.Lock()
	r.hud = hud.object
	r.mu.Unlock()

	// An object keeps every copy of an item put into it and renames
	// the newcomer, so repeated runs pile up "slgo tab 1", "2", "3".
	r.removeDuplicates(ctx, hud)

	// Make sure each script is in there, reusing what is already
	// present.
	for _, s := range specs {
		if r.findTaskItem(ctx, hud, s.name) != nil {
			continue
		}
		item := r.createScript(ctx, s.name)
		fmt.Printf("  %q: new inventory item %s\n", s.name, item.ItemID)
		if _, err := r.upload(ctx, "UpdateScriptAgent",
			map[string]any{"target": target}, item.ItemID, []byte(s.src)); err != nil {
			log.Fatalf("saving %q to inventory: %v", s.name, err)
		}
		r.intoTask(ctx, hud.local, item)
		time.Sleep(6 * time.Second)
	}

	// The copy inside the object has its own item id, which is what
	// the script capability wants.
	items := r.taskItems(ctx, hud)
	fmt.Printf("\nthe HUD holds %d items:\n", len(items))
	for _, it := range items {
		fmt.Printf("  %-24s %-10s %s\n", it.Name, it.Type, it.ItemID)
	}
	inside := map[string]*taskItem{}
	for _, it := range items {
		inside[it.Name] = &taskItem{ItemID: it.ItemID, Name: it.Name, Type: it.Type}
	}

	fmt.Println("\ncompiling each in place, set to run")
	for _, s := range specs {
		task := inside[s.name]
		if task == nil {
			log.Fatalf("%q did not land in the attachment", s.name)
		}
		body, err := r.upload(ctx, "UpdateScriptTask", map[string]any{
			"task_id":           hud.object.String(),
			"is_script_running": true,
			"target":            target,
		}, task.ItemID, []byte(s.src))
		if err != nil {
			log.Fatalf("compiling %q: %v", s.name, err)
		}
		m := llsd.Map(mustLLSD(body))
		fmt.Printf("  %-16s item %s  compiled %v\n",
			s.name, task.ItemID, llsd.Bool(m, "compiled"))
		if errs, ok := m["errors"].([]any); ok {
			for _, e := range errs {
				fmt.Printf("    %v\n", e)
			}
		}
		if !llsd.Bool(m, "compiled") {
			log.Fatalf("%q did not compile", s.name)
		}
		time.Sleep(2 * time.Second)
	}

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
			d := &t.ChatData
			// Say which object this came from, and whether it is ours.
			// The message names no script -- the protocol has no field
			// for one -- so anything beyond "the HUD said it" has to
			// come from the text itself.
			r.mu.Lock()
			mine := d.SourceID == r.hud
			r.mu.Unlock()
			from := "elsewhere"
			if mine {
				from = "our HUD"
			}
			fmt.Printf("  #%-6d [%s %s] %s\n",
				m.Sequence, from, chatType(d.ChatType), nul(d.Message))
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

// chatType names the chat types, from the viewer's LLChatType.  A
// script's runtime errors come in on the debug one.
func chatType(t uint8) string {
	switch t {
	case 0:
		return "whisper"
	case 1:
		return "say"
	case 2:
		return "shout"
	case 6:
		return "DEBUG"
	case 7:
		return "region"
	case 8:
		return "owner"
	case 9:
		return "direct"
	}
	return fmt.Sprintf("type %d", t)
}

// removeDuplicates deletes the numbered copies an object accumulates.
//
// Copying an item into an object that already holds one of that name
// keeps both and renames the newcomer, so repeated runs leave a trail
// of "slgo hud script 1", "2", "3".  Only the numbered ones go: the
// unsuffixed one is the script being worked on.
func (r *run) removeDuplicates(ctx context.Context, hud *attachment) {
	items := r.taskItems(ctx, hud)
	n := 0
	for _, it := range items {
		if !isDuplicate(it.Name) {
			continue
		}
		m := &msg.RemoveTaskInventory{}
		m.AgentData.AgentID, m.AgentData.SessionID = r.me, r.sess
		m.InventoryData.LocalID = hud.local
		m.InventoryData.ItemID = it.ItemID
		r.send(ctx, m)
		fmt.Printf("  removing %q %s\n", it.Name, it.ItemID)
		n++
	}
	if n > 0 {
		// The listing is cached against the object id, so drop it or
		// the next read returns the removed items.
		r.mu.Lock()
		delete(r.taskInv, hud.object)
		r.mu.Unlock()
		time.Sleep(6 * time.Second)
	}
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
	for _, it := range r.taskItems(ctx, hud) {
		if it.Name == want {
			return &it
		}
	}
	return nil
}

// taskItems reads what an object holds.  RequestTaskInventory answers
// with a filename rather than the contents, and the file comes over
// xfer.
func (r *run) taskItems(ctx context.Context, hud *attachment) []taskItem {
	// Forget the last filename first.  Each ReplyTaskInventory names a
	// fresh file and the xfer deletes it on completion, so reading the
	// remembered name a second time asks for something that no longer
	// exists and the transfer is aborted with result -43.
	r.mu.Lock()
	delete(r.taskInv, hud.object)
	r.mu.Unlock()

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
	return parseTaskInventory(body)
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

// isDuplicate reports whether a name is a numbered copy of one of our
// scripts, which is what an object calls the second one it is given.
func isDuplicate(name string) bool {
	for _, s := range specs {
		if name == s.name {
			return false // the one being worked on
		}
	}
	// Anything else of ours is left over: a numbered copy, or a script
	// from a previous run that is no longer wanted.  Left in place it
	// would keep running and its output would be mixed in with this
	// run's.
	return strings.HasPrefix(name, "slgo ")
}
