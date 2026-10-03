package slate

// What the grid does with a wear and a take off, as measured: the request
// is answered by an attach update after a short delay, the product's own
// reaction comes later still, and the root is dropped a little after a
// take off.

import (
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var (
	idWornHat   = msg.MustParseUUID("1bbb7e57-7e57-c0de-7ce9-c6c4ff0279f1")
	idWornRoot  = msg.MustParseUUID("49597e57-7e57-c0de-7a0b-07753b9829c4")
	idWornChild = msg.MustParseUUID("9b507e57-7e57-c0de-57b3-67ba41396349")
)

// wearGrid is the far end of a wear.
type wearGrid struct {
	f  *fakeGrid
	mu sync.Mutex
	n  int // wears so far, which make a fresh root id each time

	attach   time.Duration // from the request to the attach update
	reaction time.Duration // from the update to what the product says
	drop     time.Duration // from a take off to the root going
	says     string        // what the worn product says on attach, if anything
}

// withWearing makes the grid answer wear and take off requests. The
// inventory holds one item, Example Hat, in Objects.
func (f *fakeGrid) withWearing(t *testing.T) *wearGrid {
	t.Helper()
	f.withInventory(t).add(idObjectsFolder, idWornHat, "Example Hat", 6)
	g := &wearGrid{f: f, attach: 30 * time.Millisecond, reaction: 120 * time.Millisecond, drop: 30 * time.Millisecond}
	f.replyTo(func(m msg.Message) {
		switch x := m.(type) {
		case *msg.RezSingleAttachmentFromInv:
			item, point := x.ObjectData.ItemID, int(x.ObjectData.AttachmentPt&^sl.AttachAdd)
			time.AfterFunc(g.attach, func() { g.attached(t, item, point) })
		case *msg.DetachAttachmentIntoInv:
			item := x.ObjectData.ItemID
			time.AfterFunc(g.drop, func() { g.dropped(item) })
		}
	})
	return g
}

// root is the worn root of the item, a prim hanging off the avatar, and
// one child.
func (g *wearGrid) worn(item msg.UUID, point int, n int) []*sl.Seen {
	id := idWornRoot
	id[15] = byte(n)
	root := prim(id, uint32(300+10*n), "Example Hat", testMe)
	root.Parent, root.AttachItem, root.AttachPoint = 1, item, point
	kid := prim(idWornChild, uint32(301+10*n), "Example Hat Band", testMe)
	kid.ID[15] = byte(n)
	child(kid, root)
	return []*sl.Seen{root, kid}
}

func (g *wearGrid) attached(t *testing.T, item msg.UUID, point int) {
	g.mu.Lock()
	g.n++
	n := g.n
	g.mu.Unlock()
	objs := g.worn(item, point, n)
	g.f.mu.Lock()
	g.f.objects = append(g.f.objects, objs...)
	g.f.mu.Unlock()
	root := objs[0]
	u := &msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{
		FullID: root.ID, ID: root.Local,
		State:     uint8((point&0x0f)<<4 | (point>>4)&0x0f),
		NameValue: []byte("AttachItemID STRING RW DS " + item.String() + "\n"),
	}}}
	if err := g.f.relay(u); err != nil {
		return
	}
	if g.says != "" {
		time.AfterFunc(g.reaction, func() {
			g.f.relay(chatMsg("Example Hat", root.ID, sl.ChatSay, g.says))
		})
	}
}

// dropped removes the attachment worn from item, and tells the session
// the simulator killed it, which is what Session.TakeOff waits to hear.
// Why: doc/slsh.md#deleting-straight-after-a-take-off
func (g *wearGrid) dropped(item msg.UUID) {
	var kill msg.KillObject
	defer func() {
		if len(kill.ObjectData) > 0 {
			g.f.relay(&kill)
		}
	}()
	g.f.mu.Lock()
	defer g.f.mu.Unlock()
	var keep []*sl.Seen
	gone := map[uint32]bool{}
	for _, o := range g.f.objects {
		if o.AttachItem == item {
			gone[o.Local] = true
			kill.ObjectData = append(kill.ObjectData, msg.KillObject_ObjectData{ID: o.Local})
		}
	}
	for _, o := range g.f.objects {
		if !gone[o.Local] && !gone[o.Parent] {
			keep = append(keep, o)
		}
	}
	g.f.objects = keep
}

// wornAlready puts the item on the avatar before the run begins.
func (g *wearGrid) wornAlready(point int) {
	g.f.mu.Lock()
	g.f.objects = append(g.f.objects, g.worn(idWornHat, point, 90)...)
	g.f.mu.Unlock()
}

// listed says whether a prim of the hat is in the store.
func (f *fakeGrid) listed(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.objects {
		if o.Name == name {
			return true
		}
	}
	return false
}
