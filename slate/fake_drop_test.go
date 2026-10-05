package slate

// What the fake grid does with a drop: the contents of a prim come over
// the xfer protocol as the simulator writes them, an UpdateTaskInventory
// adds a copy under a fresh id (renamed when the name is taken), a
// RemoveTaskInventory takes one out, and an ObjectImage changes the
// prim's textures. A second avatar's ActivateGroup changes its group.

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids, made with tools/new-id.
var (
	idSwatchItem  = msg.MustParseUUID("19197e57-7e57-c0de-80e0-f36fc114930a")
	idSwatchAsset = msg.MustParseUUID("1d717e57-7e57-c0de-391d-414295048913")
	idHeldItem    = msg.MustParseUUID("6a807e57-7e57-c0de-9df4-14493ab6b2a5")
	idTaskCopy    = msg.MustParseUUID("71517e57-7e57-c0de-cdc1-307835be83f9")
	idGroupOne    = msg.MustParseUUID("7a217e57-7e57-c0de-bab9-72f8102ea318")
	idGroupTwo    = msg.MustParseUUID("7f9c7e57-7e57-c0de-f523-707b6f5b075f")
	idGroupThree  = msg.MustParseUUID("b2a07e57-7e57-c0de-ff78-d17d266870b4")
)

// Permissions of an item, as the viewer reads them.
const (
	permAll       = sl.PermCopy | sl.PermTransfer | sl.PermModify | sl.PermMove
	permNoTrans   = sl.PermCopy | sl.PermModify | sl.PermMove
	permNoCopy    = sl.PermTransfer | sl.PermModify | sl.PermMove
	swatchTexture = 0 // the inventory type of a texture
)

// heldItem is one thing in a prim's contents.
type heldItem struct {
	id    msg.UUID
	name  string
	asset msg.UUID
	typ   string // as the contents file says: texture, object
}

// dropGrid is the far end of a drop.
type dropGrid struct {
	f   *fakeGrid
	inv *fakeInv
	mu  sync.Mutex
	n   int // copies made so far

	cur    uint32                // the prim the last read was of
	held   map[uint32][]heldItem // the contents of a prim, by local id
	delay  time.Duration         // from an UpdateTaskInventory to the copy showing
	refuse bool                  // an UpdateTaskInventory is ignored
	// product is what the prim's own script puts in as the copy arrives,
	// ahead of it in the contents: a product reacting to the drop.
	product []heldItem
	stuck   bool // a RemoveTaskInventory is ignored
}

// withDropping makes the grid answer reads of a prim's contents, and the
// messages that change them. The inventory holds Example Red Swatch, a
// texture the tester owns with every permission, in Objects.
func (f *fakeGrid) withDropping(t *testing.T) *dropGrid {
	t.Helper()
	inv := f.withInventory(t)
	inv.addAsset(idObjectsFolder, idSwatchItem, "Example Red Swatch", swatchTexture, idSwatchAsset, permAll)
	g := &dropGrid{f: f, inv: inv, held: map[uint32][]heldItem{}}
	f.replyTo(func(m msg.Message) {
		switch x := m.(type) {
		case *msg.RequestTaskInventory:
			local := x.InventoryData.LocalID
			time.AfterFunc(0, func() { g.reply(local) })
		case *msg.RequestXfer:
			id := x.XferID.ID
			time.AfterFunc(0, func() { g.f.relay(xferPacket(id, 0, true, g.file())) })
		case *msg.UpdateTaskInventory:
			g.update(x)
		case *msg.RemoveTaskInventory:
			g.remove(x.InventoryData.LocalID, x.InventoryData.ItemID)
		case *msg.ObjectImage:
			for _, d := range x.ObjectData {
				te := d.TextureEntry
				f.change(d.ObjectLocalID, func(o *sl.Seen) { o.TextureEntry = te })
			}
		case *msg.ActivateGroup:
			f.mu.Lock()
			f.group = x.AgentData.GroupID
			f.mu.Unlock()
		}
	})
	return g
}

// holds puts something in a prim's contents before the run.
func (g *dropGrid) holds(local uint32, h ...heldItem) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.held[local] = append(g.held[local], h...)
}

// contents is the names in a prim's contents.
func (g *dropGrid) contents(local uint32) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, h := range g.held[local] {
		out = append(out, h.name)
	}
	return out
}

// reply names the file the contents are written to; the object that
// holds nothing is answered with no name.
func (g *dropGrid) reply(local uint32) {
	var id msg.UUID
	g.f.mu.Lock()
	for _, o := range g.f.objects {
		if o.Local == local {
			id = o.ID
		}
	}
	g.f.mu.Unlock()
	g.mu.Lock()
	g.cur = local
	empty := len(g.held[local]) == 0
	g.mu.Unlock()
	name := "inventory_37c9.tmp"
	if empty {
		name = ""
	}
	g.f.relay(replyTaskInventory(id, name))
}

// file is the contents file of the prim last asked about.
func (g *dropGrid) file() []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	var b strings.Builder
	b.WriteString("\tinv_object\t0\n\t{\n\t\tobj_id\t00000000-0000-0000-0000-000000000000\n\t\ttype\tcategory\n\t\tname\tContents|\n\t}\n")
	for _, h := range g.held[g.cur] {
		fmt.Fprintf(&b, "\tinv_item\t0\n\t{\n\t\titem_id\t%s\n\t\tpermissions 0\n\t\t{\n\t\t\tbase_mask\t7fffffff\n\t\t\towner_mask\t7fffffff\n\t\t}\n"+
			"\t\tasset_id\t%s\n\t\ttype\t%s\n\t\tinv_type\t%s\n\t\tname\t%s|\n\t}\n", h.id, h.asset, h.typ, h.typ, h.name)
	}
	return []byte(b.String())
}

// update adds the copy: the asset of the item the message names comes
// from the tester's inventory, which the message does not carry.
func (g *dropGrid) update(x *msg.UpdateTaskInventory) {
	if g.refuse {
		return
	}
	name := strings.TrimRight(string(x.InventoryData.Name), "\x00")
	local := x.UpdateData.LocalID
	var asset msg.UUID
	typ := "object"
	g.inv.mu.Lock()
	for _, it := range g.inv.items {
		if it.id == x.InventoryData.ItemID {
			asset, typ = it.asset, "object"
			if it.typ == swatchTexture {
				typ = "texture"
			}
		}
	}
	g.inv.mu.Unlock()
	add := func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.n++
		id := idTaskCopy
		id[15] = byte(g.n)
		taken := false
		for _, h := range g.held[local] {
			taken = taken || h.name == name
		}
		if taken {
			name += " 1"
		}
		g.held[local] = append(g.held[local], g.product...)
		g.held[local] = append(g.held[local], heldItem{id: id, name: name, asset: asset, typ: typ})
	}
	if g.delay > 0 {
		time.AfterFunc(g.delay, add)
		return
	}
	add()
}

func (g *dropGrid) remove(local uint32, item msg.UUID) {
	if g.stuck {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var keep []heldItem
	for _, h := range g.held[local] {
		if h.id != item {
			keep = append(keep, h)
		}
	}
	g.held[local] = keep
}

// faceTexture is the texture a face of a prim has in the fake's store.
func (f *fakeGrid) faceTexture(local uint32, face int) msg.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.objects {
		if o.Local == local {
			faces, err := o.Faces(6)
			if err != nil || face >= len(faces) {
				return msg.UUID{}
			}
			return faces[face].Texture
		}
	}
	return msg.UUID{}
}

// replyTaskInventory is the simulator naming the file it has written an
// object's contents to.
func replyTaskInventory(task msg.UUID, filename string) *msg.ReplyTaskInventory {
	m := &msg.ReplyTaskInventory{}
	m.InventoryData.TaskID = task
	m.InventoryData.Serial = 1
	m.InventoryData.Filename = append([]byte(filename), 0)
	return m
}

// xferPacket is one packet of a file arriving over the xfer protocol: the
// first carries a four byte length that is not part of the file, and the
// last is marked in the top bit of its number.
func xferPacket(id uint64, seq uint32, last bool, data []byte) *msg.SendXferPacket {
	m := &msg.SendXferPacket{}
	m.XferID.ID = id
	m.XferID.Packet = seq
	if last {
		m.XferID.Packet |= 0x80000000
	}
	if seq == 0 {
		m.DataPacket.Data = binary.LittleEndian.AppendUint32(nil, uint32(len(data)))
	}
	m.DataPacket.Data = append(m.DataPacket.Data, data...)
	return m
}
