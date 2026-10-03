package slate

// What the fake grid does for MakeBridge: a prim rezzed where it is asked,
// a name it keeps, a take that puts an item in the folder it was sent to,
// and a delete to the Trash.

import (
	"strings"
	"sync"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids, made with tools/new-id.
var (
	idTrashFolder = msg.MustParseUUID("2c6a7e57-7e57-c0de-81d4-5f0b9e3a47c6")
	idMadePrim    = msg.MustParseUUID("5d907e57-7e57-c0de-a8f3-c21e6b04d97a")
	idMadeItem    = msg.MustParseUUID("93b17e57-7e57-c0de-4e0c-71a5d8f26b13")
)

const madeLocal = 900

// bridgeGrid is the far end of a make-bridge: which steps it answers.
type bridgeGrid struct {
	f    *fakeGrid
	inv  *fakeInv
	mu   sync.Mutex
	made *sl.Seen // the prim rezzed, until it is taken or deleted
	name string   // what it has been named
	// What it refuses.
	noRez, noName, noTake bool
	trashed               bool // the prim was sent to the Trash
	rezzedAt              msg.Vector3
	scale                 msg.Vector3
}

// withBridge serves an inventory with a Trash and makes the grid answer
// ObjectAdd, ObjectName, family requests and DeRezObject.
func withBridge(t *testing.T, f *fakeGrid) *bridgeGrid {
	t.Helper()
	inv := f.withInventory(t)
	inv.mu.Lock()
	inv.folders = append(inv.folders, invFolder{idTrashFolder, testInvRoot, "Trash"})
	inv.mu.Unlock()
	b := &bridgeGrid{f: f, inv: inv}
	f.replyTo(b.answer)
	return b
}

func (b *bridgeGrid) answer(m msg.Message) {
	switch x := m.(type) {
	case *msg.ObjectAdd:
		b.mu.Lock()
		if b.noRez || b.made != nil {
			b.mu.Unlock()
			return
		}
		p := x.ObjectData.RayEnd // lands where it was asked
		s := at(&sl.Seen{
			Object: sl.Object{ID: idMadePrim, Local: madeLocal, Name: "Object"},
			Owner:  testMe, PCode: 9, TextureEntry: plainTE,
		}, p.X, p.Y, p.Z)
		b.made, b.name, b.rezzedAt, b.scale = s, "Object", p, x.ObjectData.Scale
		b.mu.Unlock()
		b.f.appear(s)
	case *msg.ObjectName:
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.noName || b.made == nil {
			return
		}
		for _, d := range x.ObjectData {
			if d.LocalID == madeLocal {
				name := strings.TrimRight(string(d.Name), "\x00")
				b.f.change(madeLocal, func(o *sl.Seen) { o.Name = name })
				b.name = name
			}
		}
	case *msg.RequestObjectPropertiesFamily:
		b.mu.Lock()
		made := b.made
		name := b.name
		b.mu.Unlock()
		if made == nil || x.ObjectData.ObjectID != made.ID {
			return
		}
		r := &msg.ObjectPropertiesFamily{}
		r.ObjectData.ObjectID, r.ObjectData.OwnerID = made.ID, testMe
		r.ObjectData.Name = []byte(name + "\x00")
		b.f.relay(r)
	case *msg.DeRezObject:
		b.mu.Lock()
		made := b.made
		if made == nil || len(x.ObjectData) == 0 || x.ObjectData[0].ObjectLocalID != madeLocal {
			b.mu.Unlock()
			return
		}
		switch x.AgentBlock.Destination {
		case 4: // take
			if b.noTake {
				b.mu.Unlock()
				return
			}
			b.inv.add(x.AgentBlock.DestinationID, idMadeItem, b.name, 6)
		case 6: // trash
			b.trashed = true
			b.inv.add(x.AgentBlock.DestinationID, idMadeItem, b.name, 6)
		}
		b.made = nil
		b.mu.Unlock()
		b.f.disappear(madeLocal)
		b.f.relay(&msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: madeLocal}}})
	}
}

// disappear takes the prim with a local id out of the region.
func (f *fakeGrid) disappear(local uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	keep := f.objects[:0]
	for _, o := range f.objects {
		if o.Local != local {
			keep = append(keep, o)
		}
	}
	f.objects = keep
}
