// Command slgo-script saves scripts that will not compile, to see what
// the simulator says about them.
//
// Both capabilities compile.  UpdateScriptAgent, which saves the copy
// in agent inventory, answers exactly what UpdateScriptTask answers for
// the copy inside a prim -- same verdict, same message, same position,
// case for case.  Saving to inventory is not merely storing text, and
// nothing has to be rezzed to find out whether source is good.
//
// Both are asked to compile for Mono.  LSL2 is not interesting.
//
// What the simulator answers, observed against the test region:
//
//	<map>
//	  <key>state</key>      <string>complete</string>
//	  <key>new_asset</key>  <uuid>...</uuid>
//	  <key>compiled</key>   <boolean>0</boolean>
//	  <key>errors</key>     <array>
//	    <string>(5, 17) : ERROR : Name not defined within scope</string>
//	  </array>
//	</map>
//
// Three things about that are worth knowing before trusting it:
//
// "state" is "complete" and a new asset id is issued whether or not the
// source compiles.  The upload succeeded; that is all "complete" means.
// "compiled" is the verdict, and it is the only thing that is.
//
// "errors" holds one error, not every error.  A script with two
// undefined names reports the first and stops, so a caller cannot fix
// everything in one pass.
//
// The two numbers are counted differently from each other.  The line is
// counted from zero and the column from one, so the string above is the
// sixth line of the script.  The column points at the end of the token
// that failed rather than its start -- it moves with the token's length,
// and for a call it lands on the open parenthesis.
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
	"github.com/quark-idlemind/slgo/internal/slhost"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

var (
	addr    = flag.String("server", "", "slgod address (default: sl-host, port 7807)")
	profile = flag.String("agent", "", "hosted agent ($SLGO_AGENT, or the daemon's default)")
	at      = flag.String("at", "200,213,27", "where to rez, region local")
)

// target is the compile target.  Mono is the only one that matters.
const target = "mono"

const (
	assetScript = 10
	invScript   = 10
)

const goodScript = `default
{
    state_entry()
    {
        llOwnerSay("slgo script alpha");
    }
}
`

// Two ways to be wrong: one the parser rejects, one the parser accepts
// and the compiler does not.
const syntaxError = `default
{
    state_entry()
    {
        llOwnerSay("unterminated
    }
}
`

const semanticError = `default
{
    state_entry()
    {
        integer n = "not an integer";
        llNoSuchFunction(n);
    }
}
`

// A single error whose position is known exactly, to find out what the
// two numbers in "(5, 4)" are counted from.  "bogus" starts on the
// sixth line, at the thirteenth column, counting both from one.
const positionProbe = `default
{
    state_entry()
    {
        integer a;
        a = bogus;
    }
}
`

// Two independent errors, to find out whether the list is every error
// or only the first one.
const twoErrors = `default
{
    state_entry()
    {
        undefinedOne();
        undefinedTwo();
    }
}
`

// nameProbe puts an undefined name on the sixth line starting at the
// thirteenth column, counting both from one, and varies only its
// length.  Whatever moves with the length is what the column counts.
func nameProbe(name string) string {
	return "default\n{\n    state_entry()\n    {\n        integer a;\n        a = " +
		name + ";\n    }\n}\n"
}

type run struct {
	c    *client.Conn
	me   msg.UUID
	sess msg.UUID
	root msg.UUID

	mu      sync.Mutex
	created map[uint32]*msg.UpdateCreateInventoryItem_InventoryData
	seen    map[msg.UUID]uint32
	owners  map[msg.UUID]msg.UUID
	taskInv map[msg.UUID]string
	xfers   *client.Xfers
}

func main() {
	flag.Parse()
	ctx := context.Background()

	c, err := client.Dial(ctx, slhost.MustAddr(*addr))
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	info, err := c.Attach(ctx, *profile,
		"UpdateCreateInventoryItem", "ObjectUpdate", "ObjectPropertiesFamily",
		"ReplyTaskInventory", "SendXferPacket", "AbortXfer",
		"ChatFromSimulator", "AlertMessage", "ScriptRunningReply")
	if err != nil {
		log.Fatal(err)
	}
	r := &run{
		c: c, me: msg.MustParseUUID(info.AgentId),
		sess:    msg.MustParseUUID(info.SessionId),
		root:    msg.MustParseUUID(info.InventoryRoot),
		created: map[uint32]*msg.UpdateCreateInventoryItem_InventoryData{},
		seen:    map[msg.UUID]uint32{},
		owners:  map[msg.UUID]msg.UUID{},
		taskInv: map[msg.UUID]string{},
	}
	r.xfers = client.NewXfers(c)
	fmt.Printf("%s in %s\n\n", info.AvatarName, info.Region)

	go r.read(ctx)
	time.Sleep(8 * time.Second)

	var x, y, z float32
	fmt.Sscanf(*at, "%f,%f,%f", &x, &y, &z)
	prim := r.rez(ctx, msg.Vector3{X: x, Y: y, Z: z})
	if prim == nil {
		log.Fatal("no prim of ours appeared")
	}
	fmt.Printf("prim %s  local %d\n\n", prim.id, prim.local)

	cases := []struct {
		name string
		src  string
	}{
		{"a script that compiles", goodScript},
		{"an unterminated string", syntaxError},
		{"a type error and an unknown function", semanticError},
		{"one error at a known line and column", positionProbe},
		{"two independent errors", twoErrors},
		{"undefined name 1 char, starts col 13", nameProbe("x")},
		{"undefined name 2 chars, starts col 13", nameProbe("xy")},
		{"undefined name 8 chars, starts col 13", nameProbe("abcdefgh")},
	}

	// The copy in agent inventory.  Saving there compiles too, so the
	// same answers should come back without a prim being involved at
	// all.
	item := r.createScript(ctx, "slgo compile test")
	fmt.Printf("script item %s\n\n", item.ItemID)
	fmt.Println("--- UpdateScriptAgent: the copy in inventory ---")
	for _, c := range cases {
		fmt.Printf("\n=== %s ===\n", c.name)
		body, err := r.upload(ctx, "UpdateScriptAgent",
			map[string]any{"target": target}, item.ItemID, msg.UUID{}, []byte(c.src))
		if err != nil {
			fmt.Printf("  request failed: %v\n", err)
			continue
		}
		report(body)
		time.Sleep(2 * time.Second)
	}

	// Leave inventory holding something that compiles, then put it in
	// the prim.
	if _, err := r.upload(ctx, "UpdateScriptAgent",
		map[string]any{"target": target}, item.ItemID, msg.UUID{}, []byte(goodScript)); err != nil {
		log.Fatalf("uploading to inventory: %v", err)
	}
	r.intoTask(ctx, prim.local, item)
	time.Sleep(5 * time.Second)

	items, err := r.taskInventory(ctx, prim)
	if err != nil {
		log.Fatalf("reading the prim: %v", err)
	}
	var task *taskItem
	for i := range items {
		if items[i].Name == "slgo compile test" {
			task = &items[i]
		}
	}
	if task == nil {
		log.Fatal("the script is not in the prim")
	}
	fmt.Printf("in the prim as item %s\n", task.ItemID)

	// The copy inside the prim.
	fmt.Println("\n--- UpdateScriptTask: the copy inside the prim ---")
	for _, c := range cases {
		fmt.Printf("\n=== %s ===\n", c.name)
		extra := map[string]any{
			"task_id":           prim.id.String(),
			"is_script_running": true,
			"target":            target,
		}
		body, err := r.upload(ctx, "UpdateScriptTask", extra, task.ItemID, prim.id, []byte(c.src))
		if err != nil {
			fmt.Printf("  request failed: %v\n", err)
			continue
		}
		report(body)
		time.Sleep(3 * time.Second)
	}

	fmt.Println("\nwatching for anything else the simulator says")
	time.Sleep(8 * time.Second)
	fmt.Println("done")
}

// report prints what the upload said about the source.
func report(body []byte) {
	v, err := llsd.Decode(bytes.NewReader(body))
	if err != nil {
		fmt.Printf("  reply is not LLSD: %v\n%s\n", err, snippet(body, 400))
		return
	}
	m := llsd.Map(v)
	if m == nil {
		fmt.Printf("  reply is %T\n", v)
		return
	}

	fmt.Printf("  state     %q\n", llsd.String(m, "state"))
	if s := llsd.String(m, "new_asset"); s != "" {
		fmt.Printf("  new asset %s\n", s)
	}
	if _, ok := m["compiled"]; ok {
		fmt.Printf("  compiled  %v\n", llsd.Bool(m, "compiled"))
	}
	if s := llsd.String(m, "message"); s != "" {
		fmt.Printf("  message   %q\n", s)
	}

	// The errors, whatever they turn out to be called.
	for _, key := range []string{"errors", "error", "compile_errors"} {
		v, ok := m[key]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case []any:
			fmt.Printf("  %s (%d):\n", key, len(t))
			for _, e := range t {
				fmt.Printf("    %v\n", e)
			}
		default:
			fmt.Printf("  %s: %v\n", key, t)
		}
	}

	// Anything else, so a field nobody expected is still seen.
	known := map[string]bool{
		"state": true, "new_asset": true, "compiled": true, "message": true,
		"errors": true, "error": true, "compile_errors": true,
	}
	for k, v := range m {
		if !known[k] {
			fmt.Printf("  (also %s: %v)\n", k, v)
		}
	}
}

type found struct {
	id    msg.UUID
	local uint32
}

type taskItem struct {
	ItemID msg.UUID
	Asset  msg.UUID
	Type   string
	Name   string
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
				r.seen[t.ObjectData[i].FullID] = t.ObjectData[i].ID
			}
			r.mu.Unlock()
		case *msg.ObjectPropertiesFamily:
			r.mu.Lock()
			r.owners[t.ObjectData.ObjectID] = t.ObjectData.OwnerID
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

	// Objects stream in the whole time, so a new local id is not
	// necessarily ours.  Ask before touching it.
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

func (r *run) createScript(ctx context.Context, name string) *msg.UpdateCreateInventoryItem_InventoryData {
	cb := uint32(time.Now().UnixNano() & 0x7fffffff)
	m := &msg.CreateInventoryItem{}
	m.AgentData.AgentID, m.AgentData.SessionID = r.me, r.sess
	b := &m.InventoryBlock
	b.CallbackID = cb
	b.NextOwnerMask = 0x0008e000
	b.Type, b.InvType = assetScript, invScript
	b.Name = append([]byte(name), 0)
	b.Description = append([]byte("slgo compile experiment"), 0)
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

// upload runs the two step asset upload and returns the second reply,
// which for a script carries the compiler's verdict.
func (r *run) upload(ctx context.Context, capName string, extra map[string]any,
	item, task msg.UUID, body []byte) ([]byte, error) {

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
	v, err := llsd.Decode(bytes.NewReader(resp.Body))
	if err != nil {
		return nil, err
	}
	uploader := llsd.String(llsd.Map(v), "uploader")
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
		return nil, fmt.Errorf("no ReplyTaskInventory")
	}
	body, err := r.xfers.Fetch(ctx, r.me, r.sess, filename, client.FilePathTaskInventory, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return parseTaskInventory(body), nil
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
