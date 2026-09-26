package agent

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// Inventory over the AIS v3 capability.
//
// The UDP FetchInventoryDescendents that the C client used was retired
// by Linden Lab; the simulator accepts the request and never answers.
// AIS is an ordinary HTTPS GET returning LLSD:
//
//	GET <InventoryAPIv3>/category/<folder-id>/children
//
// describing the folder and carrying an _embedded map of its child
// categories, items and links.

// Folder is an inventory category.
type Folder struct {
	ID       msg.UUID
	ParentID msg.UUID
	Name     string
	Type     int // the template's type_default
	Version  int
}

// Item is an inventory item.  A link is an item too, with IsLink set
// and AssetID pointing at what it links to.
type Item struct {
	ID       msg.UUID
	ParentID msg.UUID
	AssetID  msg.UUID
	Name     string
	Desc     string
	Type     int
	InvType  int
	Flags    uint32
	Created  int64

	CreatorID msg.UUID
	OwnerID   msg.UUID
	GroupID   msg.UUID

	BaseMask      uint32
	OwnerMask     uint32
	GroupMask     uint32
	EveryoneMask  uint32
	NextOwnerMask uint32

	SaleType  int
	SalePrice int

	IsLink bool
}

// Inventory is an agent's folder tree.  Every method is safe to call
// while a fetch is running.
//
// Fetching into one that already holds something brings it up to date.
// A folder or item that arrives again is listed under the parent it
// names now, so one that has moved leaves the folder it was in.  And a
// folder whose whole contents are read -- by FetchInventory, FetchFolder,
// or FetchFolderDepth down to the depth asked for -- no longer lists
// what has gone from it, and what has gone is forgotten: a folder, with
// everything under it.  What one reply shows in another folder has
// moved, and is not forgotten, even where the same reply reads the
// folder it left.
type Inventory struct {
	mu      sync.RWMutex
	root    msg.UUID
	folders map[msg.UUID]*Folder
	items   map[msg.UUID]*Item

	// Each folder and item is listed once, under the parent its record
	// names; a folder that names itself as its parent is not listed.
	children map[msg.UUID][]msg.UUID // folder -> child folders
	contents map[msg.UUID][]msg.UUID // folder -> items
}

// NewInventory makes an empty tree rooted at a folder.  A client
// building its own from the far end of a link needs this; an Agent gets
// one made for it.
func NewInventory(root msg.UUID) *Inventory { return newInventory(root) }

func newInventory(root msg.UUID) *Inventory {
	return &Inventory{
		root:     root,
		folders:  make(map[msg.UUID]*Folder),
		items:    make(map[msg.UUID]*Item),
		children: make(map[msg.UUID][]msg.UUID),
		contents: make(map[msg.UUID][]msg.UUID),
	}
}

// Root is the top of the tree.
func (inv *Inventory) Root() msg.UUID { return inv.root }

// Folder returns a folder by id.
func (inv *Inventory) Folder(id msg.UUID) (*Folder, bool) {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	f, ok := inv.folders[id]
	return f, ok
}

// Item returns an item by id.
func (inv *Inventory) Item(id msg.UUID) (*Item, bool) {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	i, ok := inv.items[id]
	return i, ok
}

// Children lists the folders directly inside one, by name.
func (inv *Inventory) Children(id msg.UUID) []*Folder {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	out := make([]*Folder, 0, len(inv.children[id]))
	for _, c := range inv.children[id] {
		if f := inv.folders[c]; f != nil {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Contents lists the items directly inside a folder, by name.
func (inv *Inventory) Contents(id msg.UUID) []*Item {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	out := make([]*Item, 0, len(inv.contents[id]))
	for _, c := range inv.contents[id] {
		if i := inv.items[c]; i != nil {
			out = append(out, i)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Counts reports how much is known.
func (inv *Inventory) Counts() (folders, items int) {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	return len(inv.folders), len(inv.items)
}

// FindFolder returns the first folder with this name, searching from
// the root breadth first.
func (inv *Inventory) FindFolder(name string) (*Folder, bool) {
	queue := []msg.UUID{inv.root}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, f := range inv.Children(id) {
			if f.Name == name {
				return f, true
			}
			queue = append(queue, f.ID)
		}
	}
	return nil, false
}

// Path is the slash separated path to a folder, from the root.
func (inv *Inventory) Path(id msg.UUID) string {
	inv.mu.RLock()
	defer inv.mu.RUnlock()

	var parts []string
	for cur := id; !cur.IsZero(); {
		f := inv.folders[cur]
		if f == nil {
			break
		}
		parts = append(parts, f.Name)
		if cur == inv.root || f.ParentID == cur {
			break
		}
		cur = f.ParentID
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "/")
}

// Walk calls fn for every folder, depth first from the root.  Returning
// false stops the descent into that folder.
func (inv *Inventory) Walk(fn func(f *Folder, depth int) bool) {
	var descend func(id msg.UUID, depth int)
	descend = func(id msg.UUID, depth int) {
		for _, f := range inv.Children(id) {
			if fn(f, depth) {
				descend(f.ID, depth+1)
			}
		}
	}
	if f, ok := inv.Folder(inv.root); ok {
		if !fn(f, 0) {
			return
		}
	}
	descend(inv.root, 1)
}

// putFolder records a folder and lists it under its parent, taking it
// out of the folder it was listed in if that has changed, as the viewer
// does (llinventorymodel.cpp:1832-1846).  The caller holds mu.
func (inv *Inventory) putFolder(f *Folder) {
	old, seen := inv.folders[f.ID]
	inv.folders[f.ID] = f
	if seen {
		if old.ParentID == f.ParentID {
			return
		}
		unlist(inv.children, old.ParentID, f.ID)
	}
	if f.ParentID != f.ID {
		inv.children[f.ParentID] = append(inv.children[f.ParentID], f.ID)
	}
}

// putItem is putFolder for an item (llinventorymodel.cpp:1642-1660).
func (inv *Inventory) putItem(i *Item) {
	old, seen := inv.items[i.ID]
	inv.items[i.ID] = i
	if seen {
		if old.ParentID == i.ParentID {
			return
		}
		unlist(inv.contents, old.ParentID, i.ID)
	}
	inv.contents[i.ParentID] = append(inv.contents[i.ParentID], i.ID)
}

// keepOnly notes in r, for take to drop, every child folder and item a
// folder lists that is not in listed, which is what a full reading of
// it found.  The caller holds mu.
func (inv *Inventory) keepOnly(folder msg.UUID, listed map[msg.UUID]bool, r *reply) {
	for _, id := range inv.children[folder] {
		if !listed[id] {
			r.folders = append(r.folders, id)
		}
	}
	for _, id := range inv.contents[folder] {
		if !listed[id] {
			r.items = append(r.items, id)
		}
	}
}

// dropFolder forgets a folder and everything listed under it.  The
// caller holds mu.
func (inv *Inventory) dropFolder(id msg.UUID) {
	f, ok := inv.folders[id]
	if !ok {
		return
	}
	delete(inv.folders, id)
	unlist(inv.children, f.ParentID, id)
	kids, items := inv.children[id], inv.contents[id]
	delete(inv.children, id)
	delete(inv.contents, id)
	for _, k := range kids {
		inv.dropFolder(k)
	}
	for _, i := range items {
		inv.dropItem(i)
	}
}

// dropItem forgets an item.  The caller holds mu.
func (inv *Inventory) dropItem(id msg.UUID) {
	if it, ok := inv.items[id]; ok {
		delete(inv.items, id)
		unlist(inv.contents, it.ParentID, id)
	}
}

// unlist takes id out of what index holds under parent.
func unlist(index map[msg.UUID][]msg.UUID, parent, id msg.UUID) {
	l := index[parent]
	if i := slices.Index(l, id); i >= 0 {
		l = slices.Delete(l, i, i+1)
	}
	if len(l) == 0 {
		delete(index, parent)
		return
	}
	index[parent] = l
}

// FetchOptions tune an inventory fetch.
type FetchOptions struct {
	// Concurrency is how many folders are fetched at once.  The C
	// client fetched them one at a time and took about fifty
	// seconds over 119 requests.  Default 8.
	Concurrency int

	// MaxFailures gives up after this many folders fail, so one bad
	// folder does not cost the whole tree and a broken capability
	// does not retry forever.  Default 8.
	MaxFailures int

	// OnFolder, if set, is called as each folder is fetched.  It
	// may be called from several goroutines.
	OnFolder func(f *Folder, folders, items int)
}

// InventoryCap is the capability inventory is read through.
const InventoryCap = "InventoryAPIv3"

// FetchInventory walks the whole tree from the root.
//
// It takes a CapDoer rather than reaching for the Agent's HTTP client,
// so the identical code runs in the process holding the grid connection
// or in a client on the far end of a link to it.
func FetchInventory(ctx context.Context, d CapDoer, inv *Inventory, opts FetchOptions) error {
	return fetchFrom(ctx, d, inv, inv.Root(), opts)
}

// FetchFolder fetches one folder's children, without descending.
func FetchFolder(ctx context.Context, d CapDoer, inv *Inventory, id msg.UUID) error {
	return FetchFolderDepth(ctx, d, inv, id, 0)
}

// FetchFolderDepth fetches a folder and, with a depth above zero, what
// is inside its folders as well.
//
// The depth goes on the request, so this is one round trip however deep
// it goes.  Walking instead costs a request per folder, which is what
// makes reading a whole inventory take the better part of a minute.
//
// The simulator caps how deep it will go and says so by answering with
// less than was asked for; nothing here treats that as an error, since
// a short answer is still an answer.  A folder whose contents it does
// not carry in full keeps what was known of them.
func FetchFolderDepth(ctx context.Context, d CapDoer, inv *Inventory, id msg.UUID, depth int) error {
	if !d.HasCap(InventoryCap) {
		return fmt.Errorf("agent: no %s capability", InventoryCap)
	}
	if depth < 0 {
		depth = 0
	}
	_, err := fetchDepth(ctx, d, inv, id, depth)
	return err
}

// FetchInventory fills the agent's own tree.
func (a *Agent) FetchInventory(ctx context.Context, opts FetchOptions) error {
	return FetchInventory(ctx, a, a.Inventory, opts)
}

// FetchFolder fetches one folder of the agent's own tree.
func (a *Agent) FetchFolder(ctx context.Context, id msg.UUID) error {
	return FetchFolder(ctx, a, a.Inventory, id)
}

func fetchFrom(ctx context.Context, d CapDoer, inv *Inventory, root msg.UUID, opts FetchOptions) error {
	if !d.HasCap(InventoryCap) {
		return fmt.Errorf("agent: no %s capability", InventoryCap)
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 8
	}
	if opts.MaxFailures <= 0 {
		opts.MaxFailures = 8
	}

	w := &invWalk{
		d:    d,
		inv:  inv,
		opts: opts,
		sem:  make(chan struct{}, opts.Concurrency),
		seen: map[msg.UUID]bool{root: true},
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w.cancel = cancel

	w.walk(ctx, root)
	w.wg.Wait()

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

type invWalk struct {
	d      CapDoer
	inv    *Inventory
	opts   FetchOptions
	sem    chan struct{}
	wg     sync.WaitGroup
	cancel context.CancelFunc

	mu       sync.Mutex
	seen     map[msg.UUID]bool
	failures int
	err      error
}

// walk fetches one folder and descends into whatever it finds.  Each
// folder gets its own goroutine, but only Concurrency of them hold a
// slot and do a request at a time; the rest are parked on the
// semaphore, which costs a few kilobytes each and nothing else.
func (w *invWalk) walk(ctx context.Context, id msg.UUID) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()

		select {
		case w.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-w.sem }()

		kids, err := fetchOne(ctx, w.d, w.inv, id)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.mu.Lock()
			w.failures++
			if w.err == nil {
				w.err = err
			}
			give := w.failures >= w.opts.MaxFailures
			w.mu.Unlock()
			if give {
				w.cancel()
			}
			return
		}

		if w.opts.OnFolder != nil {
			f, _ := w.inv.Folder(id)
			nf, ni := w.inv.Counts()
			w.opts.OnFolder(f, nf, ni)
		}

		for _, k := range kids {
			w.mu.Lock()
			fresh := !w.seen[k]
			w.seen[k] = true
			w.mu.Unlock()
			if fresh {
				w.walk(ctx, k)
			}
		}
	}()
}

// fetchOne requests one folder's children and records them, returning
// the child folders found.
func fetchOne(ctx context.Context, d CapDoer, inv *Inventory, id msg.UUID) ([]msg.UUID, error) {
	return fetchDepth(ctx, d, inv, id, 0)
}

func fetchDepth(ctx context.Context, d CapDoer, inv *Inventory, id msg.UUID, depth int) ([]msg.UUID, error) {
	path := "/category/" + id.String() + "/children"
	if depth > 0 {
		path += "?depth=" + strconv.Itoa(depth)
	}
	resp, err := d.DoCap(ctx, CapRequest{
		Cap:  InventoryCap,
		Path: path,
	})
	if err != nil {
		return nil, fmt.Errorf("agent: inventory %s: %w", id, err)
	}
	if !resp.OK() {
		snippet := resp.Body
		if len(snippet) > 256 {
			snippet = snippet[:256]
		}
		return nil, fmt.Errorf("agent: inventory %s: status %d: %s",
			id, resp.Status, strings.TrimSpace(string(snippet)))
	}

	v, err := llsd.Decode(bytes.NewReader(resp.Body))
	if err != nil {
		return nil, fmt.Errorf("agent: inventory %s: %w", id, err)
	}
	m := llsd.Map(v)
	if m == nil {
		return nil, fmt.Errorf("agent: inventory %s: reply was %T, wanted a map", id, v)
	}

	return inv.take(id, m, depth), nil
}

// reply is what one reply put into the tree, and what the folders it
// read whole no longer list.
type reply struct {
	put            map[msg.UUID]bool
	folders, items []msg.UUID
}

// take records a reply to a request for a folder's children at a depth,
// returning the child folders found.  It holds the lock throughout, so
// nothing reads a reply half recorded, and a folder's listing is set
// against the tree as it is.
//
// What a folder read whole no longer lists is dropped once the whole
// reply is in, and only if the reply did not put it somewhere else: a
// folder can leave one parent for another within one reply, and the
// two are read in map order.  The viewer too applies a reply only once
// it has read all of it, and deletes last (llaisapi.cpp:850-851,
// 1778-1784).
func (inv *Inventory) take(id msg.UUID, m map[string]any, depth int) []msg.UUID {
	inv.mu.Lock()
	defer inv.mu.Unlock()

	r := &reply{put: map[msg.UUID]bool{}}
	// The folder describes itself at the top level.  There is no key to
	// fall back on here -- this is the body of the reply, not an entry
	// in a map -- so one that does not name itself is passed over, and
	// what it holds is not taken as all that the folder asked for holds.
	self := folderFrom(m)
	if !self.ID.IsZero() {
		inv.putFolder(self)
		r.put[self.ID] = true
	}
	folder := id
	if self.ID != id {
		folder = msg.UUID{}
	}
	kids := absorb(inv, r, folder, llsd.Map(m["_embedded"]), 0, depth)
	for _, gone := range r.folders {
		if !r.put[gone] {
			inv.dropFolder(gone)
		}
	}
	for _, gone := range r.items {
		if !r.put[gone] {
			inv.dropItem(gone)
		}
	}
	return kids
}

// absorb records what an _embedded map holds and returns the child
// folders found at this level.  The map is what folder holds, level
// folders below the one a request at depth asked for; a zero folder is
// one the reply did not name.  What it puts, and what a folder it reads
// whole no longer lists, it notes in r.  The caller holds inv.mu.
//
// It recurses, which is the whole point of asking for a depth: a reply
// to depth=2 nests each child category's own _embedded inside it, and
// reading only the outer one throws away everything the extra round
// trip was avoided for.  The request costs the same either way; the
// difference is only whether the answer is kept.
func absorb(inv *Inventory, r *reply, folder msg.UUID, emb map[string]any, level, depth int) []msg.UUID {
	if emb == nil {
		return nil
	}

	var kids []msg.UUID
	listed := map[msg.UUID]bool{}
	for cid, cv := range llsd.Map(emb["categories"]) {
		cm := llsd.Map(cv)
		if cm == nil {
			continue
		}
		f := folderFrom(cm)
		if f.ID.IsZero() {
			if u, err := msg.ParseUUID(cid); err == nil {
				f.ID = u
			}
		}
		if f.ID.IsZero() {
			continue
		}
		inv.putFolder(f)
		r.put[f.ID] = true
		kids = append(kids, f.ID)
		listed[f.ID] = true

		// Whatever came down with it, however deep.
		absorb(inv, r, f.ID, llsd.Map(cm["_embedded"]), level+1, depth)
	}

	// Items and links are the same thing to us: a link is an item
	// whose asset is what it points at, which is how the UDP
	// message delivered them too.
	for _, key := range []string{"items", "links"} {
		for iid, iv := range llsd.Map(emb[key]) {
			im := llsd.Map(iv)
			if im == nil {
				continue
			}
			it := itemFrom(im)
			if it.ID.IsZero() {
				if u, err := msg.ParseUUID(iid); err == nil {
					it.ID = u
				}
			}
			if it.ID.IsZero() {
				continue
			}
			it.IsLink = key == "links"
			inv.putItem(it)
			r.put[it.ID] = true
			listed[it.ID] = true
		}
	}

	if !folder.IsZero() && whole(emb, level, depth) {
		inv.keepOnly(folder, listed, r)
	}
	return kids
}

// whole reports whether an _embedded map is everything its folder
// holds: it is within the depth asked for, and carries categories,
// items and links all three.  Anything less is a part, and what was
// known of the folder stays.  The viewer takes a folder's contents as
// known on the same two conditions (llaisapi.cpp:1423-1426, 1500-1510).
func whole(emb map[string]any, level, depth int) bool {
	if level > depth {
		return false
	}
	for _, key := range []string{"categories", "items", "links"} {
		if llsd.Map(emb[key]) == nil {
			return false
		}
	}
	return true
}

// folderFrom reads one category, whether or not it names itself.
//
// A zero ID is left for the caller to deal with rather than turned into
// a nil here.  AIS sometimes names a category only by the key it is
// filed under and omits category_id from the body, and refusing here
// meant the caller's fallback to that key could never run -- so such a
// folder was dropped, while an item in the same position was kept,
// because itemFrom does not refuse either.  Two halves of one reply
// treated differently by accident.
func folderFrom(m map[string]any) *Folder {
	f := &Folder{
		Name:    llsd.String(m, "name"),
		Type:    int(llsd.Int(m, "type_default")),
		Version: int(llsd.Int(m, "version")),
	}
	f.ID, _ = msg.ParseUUID(llsd.String(m, "category_id"))
	f.ParentID, _ = msg.ParseUUID(llsd.String(m, "parent_id"))
	return f
}

func itemFrom(m map[string]any) *Item {
	it := &Item{
		Name:    llsd.String(m, "name"),
		Desc:    llsd.String(m, "desc"),
		Type:    int(llsd.Int(m, "type")),
		InvType: int(llsd.Int(m, "inv_type")),
		Flags:   uint32(llsd.Int(m, "flags")),
		Created: llsd.Int(m, "created_at"),
	}
	it.ID, _ = msg.ParseUUID(llsd.String(m, "item_id"))
	it.ParentID, _ = msg.ParseUUID(llsd.String(m, "parent_id"))
	it.AssetID, _ = msg.ParseUUID(llsd.String(m, "asset_id"))

	// A link carries linked_id instead of asset_id.
	if it.AssetID.IsZero() {
		it.AssetID, _ = msg.ParseUUID(llsd.String(m, "linked_id"))
	}

	if p := llsd.Map(m["permissions"]); p != nil {
		it.CreatorID, _ = msg.ParseUUID(llsd.String(p, "creator_id"))
		it.OwnerID, _ = msg.ParseUUID(llsd.String(p, "owner_id"))
		it.GroupID, _ = msg.ParseUUID(llsd.String(p, "group_id"))
		it.BaseMask = uint32(llsd.Int(p, "base_mask"))
		it.OwnerMask = uint32(llsd.Int(p, "owner_mask"))
		it.GroupMask = uint32(llsd.Int(p, "group_mask"))
		it.EveryoneMask = uint32(llsd.Int(p, "everyone_mask"))
		it.NextOwnerMask = uint32(llsd.Int(p, "next_owner_mask"))
	}
	if sale := llsd.Map(m["sale_info"]); sale != nil {
		it.SaleType = int(llsd.Int(sale, "sale_type"))
		it.SalePrice = int(llsd.Int(sale, "sale_price"))
	}
	return it
}
