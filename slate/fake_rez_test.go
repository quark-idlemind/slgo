package slate

// What the fake grid does with a rez from inventory: the object is
// described where it was asked, a refusal is an alert and no object, and a
// delete to the Trash takes the object away.

import (
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids, made with tools/new-id.
var (
	idBoxItem   = msg.MustParseUUID("13447e57-7e57-c0de-3996-3f3df30b5872")
	idRezzedBox = msg.MustParseUUID("1e587e57-7e57-c0de-8d5c-def1cd2a2588")
	idBoxGroup  = msg.MustParseUUID("81a67e57-7e57-c0de-b6e6-970467a92b38")
)

const rezzedLocal = 950

// rezGrid is the far end of a rez step.
type rezGrid struct {
	f  *fakeGrid
	mu sync.Mutex
	n  int // rezzes so far, which make a fresh id and local id each time

	delay  time.Duration // from the request to the object being described
	refuse string        // an alert answered in place of an object
	says   string        // what the rezzed object says when it is rezzed
	noKill bool          // a delete to the Trash is not answered
}

// withRezzing makes the grid answer RezObject and DeRezObject. The
// inventory holds one item, Example Box, in Objects, and a Trash.
func (f *fakeGrid) withRezzing(t *testing.T, trash bool) *rezGrid {
	t.Helper()
	inv := f.withInventory(t)
	inv.add(idObjectsFolder, idBoxItem, "Example Box", 6)
	if trash {
		inv.mu.Lock()
		inv.folders = append(inv.folders, invFolder{idTrashFolder, testInvRoot, "Trash"})
		inv.mu.Unlock()
	}
	g := &rezGrid{f: f, delay: 30 * time.Millisecond}
	f.replyTo(func(m msg.Message) {
		switch x := m.(type) {
		case *msg.RezObject:
			at := x.RezData.RayEnd
			time.AfterFunc(g.delay, func() { g.rezzed(at) })
		case *msg.DeRezObject:
			if x.AgentBlock.Destination != 6 || g.noKill {
				return
			}
			for _, d := range x.ObjectData {
				g.f.disappear(d.ObjectLocalID)
				g.f.relay(&msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: d.ObjectLocalID}}})
			}
		}
	})
	return g
}

func (g *rezGrid) rezzed(at msg.Vector3) {
	g.mu.Lock()
	refuse, says := g.refuse, g.says
	g.n++
	n := g.n
	g.mu.Unlock()
	if refuse != "" {
		m := &msg.AlertMessage{}
		m.AlertData.Message = append([]byte(refuse), 0)
		g.f.relay(m)
		return
	}
	id := idRezzedBox
	id[15] = byte(n)
	o := prim(id, uint32(rezzedLocal+n-1), "Example Box", testMe)
	o.Position = at
	g.f.appear(o)
	if says != "" {
		g.f.relay(chatMsg("Example Box", id, sl.ChatSay, says))
	}
}
