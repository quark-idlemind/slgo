package agent

import (
	"bytes"
	"context"
	"fmt"
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
type Inventory struct {
	mu       sync.RWMutex
	root     msg.UUID
	folders  map[msg.UUID]*Folder
	items    map[msg.UUID]*Item
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

// addFolder records a folder, reporting whether it was new.
func (inv *Inventory) addFolder(f *Folder) bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if _, seen := inv.folders[f.ID]; seen {
		inv.folders[f.ID] = f
		return false
	}
	inv.folders[f.ID] = f
	if f.ParentID != f.ID {
		inv.children[f.ParentID] = append(inv.children[f.ParentID], f.ID)
	}
	return true
}

func (inv *Inventory) addItem(i *Item) bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if _, seen := inv.items[i.ID]; seen {
		inv.items[i.ID] = i
		return false
	}
	inv.items[i.ID] = i
	inv.contents[i.ParentID] = append(inv.contents[i.ParentID], i.ID)
	return true
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
// a short answer is still an answer.
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

	// The folder describes itself at the top level.
	if self := folderFrom(m); self != nil && !self.ID.IsZero() {
		inv.addFolder(self)
	}

	kids := absorb(inv, llsd.Map(m["_embedded"]))
	return kids, nil
}

// absorb records what an _embedded map holds and returns the child
// folders found at this level.
//
// It recurses, which is the whole point of asking for a depth: a reply
// to depth=2 nests each child category's own _embedded inside it, and
// reading only the outer one throws away everything the extra round
// trip was avoided for.  The request costs the same either way; the
// difference is only whether the answer is kept.
func absorb(inv *Inventory, emb map[string]any) []msg.UUID {
	if emb == nil {
		return nil
	}

	var kids []msg.UUID
	for cid, cv := range llsd.Map(emb["categories"]) {
		cm := llsd.Map(cv)
		if cm == nil {
			continue
		}
		f := folderFrom(cm)
		if f == nil {
			continue
		}
		if f.ID.IsZero() {
			if u, err := msg.ParseUUID(cid); err == nil {
				f.ID = u
			}
		}
		if f.ID.IsZero() {
			continue
		}
		inv.addFolder(f)
		kids = append(kids, f.ID)

		// Whatever came down with it, however deep.
		absorb(inv, llsd.Map(cm["_embedded"]))
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
			inv.addItem(it)
		}
	}
	return kids
}

func folderFrom(m map[string]any) *Folder {
	f := &Folder{
		Name:    llsd.String(m, "name"),
		Type:    int(llsd.Int(m, "type_default")),
		Version: int(llsd.Int(m, "version")),
	}
	f.ID, _ = msg.ParseUUID(llsd.String(m, "category_id"))
	f.ParentID, _ = msg.ParseUUID(llsd.String(m, "parent_id"))
	if f.ID.IsZero() {
		return nil
	}
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
