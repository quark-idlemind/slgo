// Command slgo-hud rezzes a prim, names it "Test HUD", takes it into
// inventory and attaches it to a HUD point.
//
// Three separate things have to work: ObjectName has to stick,
// DeRezObject has to produce an inventory item, and
// RezSingleAttachmentFromInv has to put that item on the avatar.  Each
// is confirmed from what the simulator says rather than assumed from
// the send having succeeded.
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/internal/slhost"
	"github.com/quark-idlemind/slgo/msg"
)

var (
	addr    = flag.String("server", "", "slgod address (default: sl-host, port 7807)")
	profile = flag.String("agent", "", "hosted agent ($SLGO_AGENT, or the daemon's default)")
	at      = flag.String("at", "200,213,27", "where to rez, region local")
	point   = flag.Int("point", hudCenter1, "HUD attachment point")
	name    = flag.String("name", "Test HUD", "what to call it")
)

// Destinations for DeRezObject, from the viewer's EDeRezDestination.
const drdTakeIntoAgentInventory = 4

// HUD attachment points.  The body points are 1 to 30; the HUD points
// carry on from there.
const (
	hudCenter2 = 31 + iota
	hudTopRight
	hudTop
	hudTopLeft
	hudCenter1
	hudBottomLeft
	hudBottom
	hudBottomRight
)

// attachmentAdd asks for the attachment to be added rather than to
// replace whatever is already on the point.
const attachmentAdd = 0x80

var hudNames = map[int]string{
	hudCenter2: "HUD Center 2", hudTopRight: "HUD Top Right",
	hudTop: "HUD Top", hudTopLeft: "HUD Top Left",
	hudCenter1: "HUD Center 1", hudBottomLeft: "HUD Bottom Left",
	hudBottom: "HUD Bottom", hudBottomRight: "HUD Bottom Right",
}

// attachmentPoint pulls the point out of an ObjectUpdate's State byte,
// which stores it with its nibbles swapped.
func attachmentPoint(state uint8) int {
	return int((state&0xf0)>>4 | (state&0x0f)<<4)
}

type run struct {
	c    *client.Conn
	me   msg.UUID
	sess msg.UUID

	mu       sync.Mutex
	seen     map[msg.UUID]uint32
	owners   map[msg.UUID]msg.UUID
	names    map[msg.UUID]string
	attached map[msg.UUID]*attachment
}

// attachment is an object the simulator says is worn.
type attachment struct {
	object msg.UUID
	item   msg.UUID
	point  int
	parent uint32
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
		"ObjectUpdate", "ObjectPropertiesFamily", "ObjectProperties",
		"KillObject", "AlertMessage", "ChatFromSimulator")
	if err != nil {
		log.Fatal(err)
	}
	r := &run{
		c: c, me: msg.MustParseUUID(info.AgentId),
		sess:     msg.MustParseUUID(info.SessionId),
		seen:     map[msg.UUID]uint32{},
		owners:   map[msg.UUID]msg.UUID{},
		names:    map[msg.UUID]string{},
		attached: map[msg.UUID]*attachment{},
	}
	root := msg.MustParseUUID(info.InventoryRoot)
	fmt.Printf("%s in %s\n\n", info.AvatarName, info.Region)

	go r.read()

	fmt.Println("settling so the interest list fills")
	time.Sleep(10 * time.Second)

	// What is already worn, so the new one can be told apart and
	// nothing existing is quietly displaced.
	before := r.attachments()
	fmt.Printf("\nalready attached: %d\n", len(before))
	for _, a := range before {
		fmt.Printf("  %-16s object %s\n", pointName(a.point), a.object)
	}
	for _, a := range before {
		if a.point == *point {
			log.Fatalf("%s is already occupied by %s; pick another -point",
				pointName(*point), a.object)
		}
	}

	// The Objects folder, which is where a take lands.
	inv := agent.NewInventory(root)
	if err := agent.FetchFolder(ctx, c, inv, root); err != nil {
		log.Fatalf("reading the inventory root: %v", err)
	}
	objects, ok := inv.FindFolder("Objects")
	if !ok {
		log.Fatal("no Objects folder")
	}
	if err := agent.FetchFolder(ctx, c, inv, objects.ID); err != nil {
		log.Fatalf("reading Objects: %v", err)
	}
	had := map[msg.UUID]bool{}
	for _, it := range inv.Contents(objects.ID) {
		had[it.ID] = true
	}
	fmt.Printf("\nObjects folder %s, %d items\n", objects.ID, len(had))

	// Rez it.
	var x, y, z float32
	fmt.Sscanf(*at, "%f,%f,%f", &x, &y, &z)
	prim := r.rez(ctx, msg.Vector3{X: x, Y: y, Z: z})
	if prim == nil {
		log.Fatal("no prim of ours appeared")
	}
	fmt.Printf("\nrezzed %s  local %d\n", prim.id, prim.local)

	// Name it, and check the name took rather than trusting the send.
	rename := &msg.ObjectName{}
	rename.AgentData.AgentID, rename.AgentData.SessionID = r.me, r.sess
	rename.ObjectData = []msg.ObjectName_ObjectData{
		{LocalID: prim.local, Name: append([]byte(*name), 0)},
	}
	r.send(ctx, rename)
	time.Sleep(3 * time.Second)

	r.send(ctx, r.family(prim.id))
	got := r.waitName(prim.id, 10*time.Second)
	fmt.Printf("named  %q\n", got)
	if got != *name {
		log.Fatalf("the name did not take: wanted %q", *name)
	}

	// Take it.  A take needs the object selected.
	fmt.Println("\ntaking it into inventory")
	r.send(ctx, r.sel(prim.local))
	time.Sleep(2 * time.Second)

	take := &msg.DeRezObject{}
	take.AgentData.AgentID, take.AgentData.SessionID = r.me, r.sess
	take.AgentBlock.Destination = drdTakeIntoAgentInventory
	take.AgentBlock.DestinationID = objects.ID
	take.AgentBlock.TransactionID = randomUUID()
	take.AgentBlock.PacketCount, take.AgentBlock.PacketNumber = 1, 0
	take.ObjectData = []msg.DeRezObject_ObjectData{{ObjectLocalID: prim.local}}
	r.send(ctx, take)

	// The item shows up in the Objects folder.  Poll for it rather
	// than wait on a message: inventory changes arrive over AIS.
	item := waitItem(ctx, c, inv, objects.ID, had, *name, 40*time.Second)
	if item == nil {
		log.Fatal("nothing new turned up in Objects")
	}
	fmt.Printf("in inventory as %s  (type %d, invtype %d)\n",
		item.ID, item.Type, item.InvType)

	// Attach it.
	fmt.Printf("\nattaching to %s\n", pointName(*point))
	att := &msg.RezSingleAttachmentFromInv{}
	att.AgentData.AgentID, att.AgentData.SessionID = r.me, r.sess
	d := &att.ObjectData
	d.ItemID = item.ID
	d.OwnerID = r.me
	d.AttachmentPt = uint8(*point) | attachmentAdd
	d.ItemFlags = item.Flags
	d.GroupMask, d.EveryoneMask = item.GroupMask, item.EveryoneMask
	d.NextOwnerMask = item.NextOwnerMask
	d.Name = append([]byte(item.Name), 0)
	d.Description = append([]byte(item.Desc), 0)
	r.send(ctx, att)

	// Confirm from the simulator: an attachment carries its item id in
	// its NameValue, so the new one can be matched exactly rather than
	// guessed at.
	a := r.waitAttachment(item.ID, 30*time.Second)
	if a == nil {
		log.Fatal("the simulator never reported it as attached")
	}
	fmt.Printf("attached: object %s\n", a.object)
	fmt.Printf("  item    %s\n", a.item)
	fmt.Printf("  point   %d (%s)\n", a.point, pointName(a.point))
	fmt.Printf("  parent  local %d\n", a.parent)
	if a.point != *point {
		fmt.Printf("  NOTE: asked for %d (%s)\n", *point, pointName(*point))
	}

	fmt.Printf("\nnow attached: %d\n", len(r.attachments()))
	for _, a := range r.attachments() {
		fmt.Printf("  %-16s object %s\n", pointName(a.point), a.object)
	}
}

func pointName(p int) string {
	if n, ok := hudNames[p]; ok {
		return n
	}
	return fmt.Sprintf("point %d", p)
}

func (r *run) read() {
	for m := range r.c.Messages() {
		v, err := m.Decode()
		if err != nil || v == nil {
			continue
		}
		switch t := v.(type) {
		case *msg.ObjectUpdate:
			r.mu.Lock()
			for i := range t.ObjectData {
				o := &t.ObjectData[i]
				r.seen[o.FullID] = o.ID
				// An attachment names the inventory item it came
				// from in its NameValue.
				if item, ok := attachItem(o.NameValue); ok {
					r.attached[item] = &attachment{
						object: o.FullID, item: item,
						point:  attachmentPoint(o.State),
						parent: o.ParentID,
					}
				}
			}
			r.mu.Unlock()
		case *msg.ObjectPropertiesFamily:
			r.mu.Lock()
			r.owners[t.ObjectData.ObjectID] = t.ObjectData.OwnerID
			r.names[t.ObjectData.ObjectID] = nul(t.ObjectData.Name)
			r.mu.Unlock()
		case *msg.ObjectProperties:
			r.mu.Lock()
			for i := range t.ObjectData {
				o := &t.ObjectData[i]
				r.owners[o.ObjectID] = o.OwnerID
				r.names[o.ObjectID] = nul(o.Name)
			}
			r.mu.Unlock()
		case *msg.AlertMessage:
			fmt.Println("  [alert]", nul(t.AlertData.Message))
		case *msg.ChatFromSimulator:
			fmt.Printf("  [chat] %s: %s\n",
				nul(t.ChatData.FromName), nul(t.ChatData.Message))
		}
	}
}

// attachItem reads the AttachItemID out of an object's NameValue,
// which is a set of lines of the form
//
//	AttachItemID STRING RW SV <uuid>
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

func (r *run) attachments() []*attachment {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*attachment, 0, len(r.attached))
	for _, a := range r.attached {
		out = append(out, a)
	}
	return out
}

func (r *run) waitAttachment(item msg.UUID, timeout time.Duration) *attachment {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		a := r.attached[item]
		r.mu.Unlock()
		if a != nil {
			return a
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil
}

func (r *run) waitName(id msg.UUID, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		n := r.names[id]
		r.mu.Unlock()
		if n != "" {
			return n
		}
		time.Sleep(250 * time.Millisecond)
	}
	return ""
}

// waitItem polls the folder until an item appears that was not there
// before.
func waitItem(ctx context.Context, c *client.Conn, inv *agent.Inventory,
	folder msg.UUID, had map[msg.UUID]bool, want string, timeout time.Duration) *agent.Item {

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if err := agent.FetchFolder(ctx, c, inv, folder); err != nil {
			continue
		}
		for _, it := range inv.Contents(folder) {
			if !had[it.ID] && it.Name == want {
				return it
			}
		}
	}
	return nil
}

type found struct {
	id    msg.UUID
	local uint32
}

func (r *run) send(ctx context.Context, m msg.Message) {
	if err := r.c.Send(ctx, m, true); err != nil {
		log.Fatalf("send %s: %v", m.MsgInfo().Name, err)
	}
}

func (r *run) sel(locals ...uint32) *msg.ObjectSelect {
	m := &msg.ObjectSelect{}
	m.AgentData.AgentID, m.AgentData.SessionID = r.me, r.sess
	for _, l := range locals {
		m.ObjectData = append(m.ObjectData, msg.ObjectSelect_ObjectData{ObjectLocalID: l})
	}
	return m
}

func (r *run) family(id msg.UUID) *msg.RequestObjectPropertiesFamily {
	m := &msg.RequestObjectPropertiesFamily{}
	m.AgentData.AgentID, m.AgentData.SessionID = r.me, r.sess
	m.ObjectData.ObjectID = id
	return m
}

// rez creates a prim and returns the one we own, asking rather than
// assuming: objects stream in the whole time, so a local id that is new
// to us is not necessarily one we just made.
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

	var fresh []found
	r.mu.Lock()
	for id, local := range r.seen {
		if local != 0 && !before[local] {
			fresh = append(fresh, found{id: id, local: local})
		}
	}
	r.mu.Unlock()
	for _, f := range fresh {
		r.send(ctx, r.family(f.id))
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

func randomUUID() msg.UUID {
	var u msg.UUID
	if _, err := rand.Read(u[:]); err != nil {
		log.Fatal(err)
	}
	return u
}

func nul(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}
