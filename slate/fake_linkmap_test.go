package slate

// What the fake grid does with a link map: the store takes a script's
// order as the real one does, and a drop of the link map script into a
// root makes the object say its links, in the order the test says the
// region numbers them, as owner chat.
// Why: doc/slate-runner.md#link-order-from-the-objects-own-script

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids, made with tools/new-id.
var idLinkMapItem = msg.MustParseUUID("a3d47e57-7e57-c0de-9f2e-0c4a5b8d1e63")

const (
	linkMapItemName    = "slgo linkmap"
	linkMapItemVersion = "slgo linkmap v2"
)

// ConfirmLinkOrder is the store's: the keys, root first, are taken when
// they are the children the store holds, in its order or another.
func (f *fakeGrid) ConfirmLinkOrder(_ context.Context, root msg.UUID, keys []msg.UUID) (*sl.LinkConfirmation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirmCalls = append(f.confirmCalls, append([]msg.UUID(nil), keys...))
	if f.confirmRefuse > 0 {
		f.confirmRefuse--
		return nil, fmt.Errorf("%w: refused by the test", sl.ErrLinkSetDiffers)
	}
	var r *sl.Seen
	for _, o := range f.objects {
		if o.ID == root {
			r = o
		}
	}
	if r == nil {
		return nil, sl.ErrNotHere
	}
	f.numberLinks(f.objects)
	var have []*sl.Seen
	for _, o := range f.objects {
		if o.Parent == r.Local && o.PCode == pcodePrim {
			have = append(have, o)
		}
	}
	sort.SliceStable(have, func(i, j int) bool { return have[i].LinkNumber < have[j].LinkNumber })
	if len(keys) == 0 || keys[0] != root || len(keys)-1 != len(have) {
		return nil, fmt.Errorf("%w: %d prims, the store has %d", sl.ErrLinkSetDiffers, len(keys), len(have)+1)
	}
	c := &sl.LinkConfirmation{At: time.Now()}
	var order []msg.UUID
	for i, k := range keys[1:] {
		found := false
		for _, h := range have {
			found = found || h.ID == k
		}
		if !found {
			return nil, fmt.Errorf("%w: %s", sl.ErrLinkSetDiffers, k)
		}
		if have[i].ID != k {
			c.Corrected = true
			c.Moved = append(c.Moved, 2+i)
		}
		order = append(order, k)
	}
	if f.linkOrder == nil {
		f.linkOrder = map[uint32][]msg.UUID{}
	}
	f.linkOrder[r.Local] = order
	if f.confirmed == nil {
		f.confirmed = map[uint32]*sl.LinkConfirmation{}
	}
	f.confirmed[r.Local] = c
	return c, nil
}

// confirmedKeys is the keys of each ConfirmLinkOrder.
func (f *fakeGrid) confirmedKeys() [][]msg.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]msg.UUID(nil), f.confirmCalls...)
}

// linkMapWorld is the region numbering links for a script that asks.
type linkMapWorld struct {
	f *fakeGrid
	g *dropGrid

	mu sync.Mutex
	// truth is the region's own order of a root's prims, root first, by
	// the root's local id; a root not here is numbered as f.objects lists
	// it.
	truth  map[uint32][]msg.UUID
	drops  []uint32 // the roots the script was dropped into
	silent bool     // the script says nothing
	leaves bool     // the script does not remove itself
	seated int      // avatars sitting on each root
}

// withLinkMaps makes the grid answer a drop of the link map script into a
// root, which is in the tester's Scripts folder at the current version.
func (g *dropGrid) withLinkMaps() *linkMapWorld {
	w := &linkMapWorld{f: g.f, g: g, truth: map[uint32][]msg.UUID{}}
	g.inv.mu.Lock()
	g.inv.items = append(g.inv.items, invItem{id: idLinkMapItem, folder: idScriptsFolder,
		name: linkMapItemName, typ: greeterScript, desc: linkMapItemVersion})
	g.inv.mu.Unlock()
	g.scriptHook = w.rez
	return w
}

// own gives the tester the object, modifiable or not.
func (w *linkMapWorld) own(root *sl.Seen, mayModify bool) {
	mask := uint32(permAll)
	if !mayModify {
		mask &^= sl.PermModify
	}
	w.f.setProps(root.ID, root.Name, "", testMe)
	w.f.setOwnerMaskOf(root.ID, mask)
}

// numbered says the region numbers a root's prims in this order.
func (w *linkMapWorld) numbered(root *sl.Seen, prims ...*sl.Seen) {
	w.mu.Lock()
	defer w.mu.Unlock()
	keys := []msg.UUID{root.ID}
	for _, p := range prims {
		keys = append(keys, p.ID)
	}
	w.truth[root.Local] = keys
}

func (w *linkMapWorld) dropped() []uint32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]uint32(nil), w.drops...)
}

// rez answers the script's drop and says whether it was the script's.
func (w *linkMapWorld) rez(x *msg.RezScript) bool {
	if strings.TrimRight(string(x.InventoryBlock.Name), "\x00") != linkMapItemName {
		return false
	}
	local := x.UpdateBlock.ObjectLocalID
	w.mu.Lock()
	w.drops = append(w.drops, local)
	keys := w.truth[local]
	silent, leaves, seated := w.silent, w.leaves, w.seated
	w.mu.Unlock()

	w.f.mu.Lock()
	var root *sl.Seen
	byID := map[msg.UUID]*sl.Seen{}
	for _, o := range w.f.objects {
		byID[o.ID] = o
		if o.Local == local {
			root = o
		}
	}
	if keys == nil && root != nil {
		keys = []msg.UUID{root.ID}
		for _, o := range w.f.objects {
			if o.Parent == local && o.PCode == pcodePrim {
				keys = append(keys, o.ID)
			}
		}
	}
	w.f.mu.Unlock()
	if root == nil || silent {
		return true
	}
	var say []string
	first := 1
	if len(keys) == 1 && seated == 0 {
		first = 0
	}
	for i, k := range keys {
		name := ""
		if o := byID[k]; o != nil {
			name = o.Name
		}
		say = append(say, fmt.Sprintf("LINKMAP %d %s %s", first+i, k, name))
	}
	say = append(say, fmt.Sprintf("LINKMAP done %d %d", len(keys), seated))
	time.AfterFunc(0, func() {
		for _, l := range say {
			w.f.relay(chatMsg("a prim", root.ID, sl.ChatOwner, l))
		}
	})
	if leaves {
		w.g.mu.Lock()
		w.g.held[local] = append(w.g.held[local], heldItem{id: idTaskCopy, name: linkMapItemName, asset: idGreeterText, typ: "lsltext"})
		w.g.mu.Unlock()
	}
	return true
}
