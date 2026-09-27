package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/internal/pick"
	"github.com/quark-idlemind/slgo/msg"
)

// aisServer serves a synthetic inventory tree over the AIS v3 shape.
type aisServer struct {
	mu       sync.Mutex
	requests int
	peak     int
	live     int

	tree    map[string][]string // folder -> child folders
	items   map[string][]string // folder -> item ids
	names   map[string]string
	parents map[string]string
	fail    map[string]bool

	// gateAt makes every request after the first wait until that
	// many are in flight, so a test for overlapping requests does
	// not depend on the handler being slow enough to overlap by
	// luck.
	gateAt   int
	gate     chan struct{}
	gateOnce sync.Once
}

func uid(n int) string { return fmt.Sprintf("b7cd7e57-7e57-c0de-2c0a-%012d", n) }

func newAIS() *aisServer {
	return &aisServer{
		tree:    map[string][]string{},
		items:   map[string][]string{},
		names:   map[string]string{},
		parents: map[string]string{},
		fail:    map[string]bool{},
		gate:    make(chan struct{}),
	}
}

// folder sets what a folder holds.  Called again, it changes it, which
// is how a test moves or deletes something between two reads.
func (a *aisServer) folder(id, name string, parent string, kids []string, items []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.names[id] = name
	a.tree[id] = kids
	a.items[id] = items
	a.parents[id] = parent
}

func (a *aisServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /cap/xyz/category/<id>/children
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		var id string
		for i, p := range parts {
			if p == "category" && i+1 < len(parts) {
				id = parts[i+1]
			}
		}

		a.mu.Lock()
		a.requests++
		a.live++
		if a.live > a.peak {
			a.peak = a.live
		}
		bad := a.fail[id]
		nth := a.requests
		a.mu.Unlock()

		defer func() {
			a.mu.Lock()
			a.live--
			a.mu.Unlock()
		}()

		// The first request is the root, and nothing can overlap
		// it: the folders it names are not known until it
		// answers.  Everything after it waits for company.
		if a.gateAt > 0 && nth > 1 {
			a.mu.Lock()
			enough := a.live >= a.gateAt
			a.mu.Unlock()
			if enough {
				a.gateOnce.Do(func() { close(a.gate) })
			}
			select {
			case <-a.gate:
			case <-time.After(10 * time.Second):
			}
		}

		if bad {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}

		depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))

		a.mu.Lock()
		parent := a.parents[id]
		if parent == "" {
			parent = uid(0)
		}
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" ?><llsd><map>`)
		fmt.Fprintf(&b, `<key>category_id</key><string>%s</string>`, id)
		fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string>`, parent)
		fmt.Fprintf(&b, `<key>name</key><string>%s</string>`, a.names[id])
		b.WriteString(`<key>type_default</key><integer>-1</integer>`)
		b.WriteString(`<key>version</key><integer>3</integer>`)
		a.embedded(&b, id, 0, depth)
		a.mu.Unlock()
		b.WriteString(`</map></llsd>`)

		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprint(w, b.String())
	})
}

// embedded writes what a folder holds, nesting what its folders hold
// down to depth, as AIS answers a request with ?depth=.  The caller
// holds a.mu.
func (a *aisServer) embedded(b *strings.Builder, id string, level, depth int) {
	b.WriteString(`<key>_embedded</key><map><key>categories</key><map>`)
	for _, k := range a.tree[id] {
		fmt.Fprintf(b, `<key>%s</key><map>`, k)
		fmt.Fprintf(b, `<key>category_id</key><string>%s</string>`, k)
		fmt.Fprintf(b, `<key>parent_id</key><string>%s</string>`, id)
		fmt.Fprintf(b, `<key>name</key><string>%s</string>`, a.names[k])
		b.WriteString(`<key>type_default</key><integer>-1</integer>`)
		b.WriteString(`<key>version</key><integer>1</integer>`)
		b.WriteString(`<key>_links</key><map><key>self</key><map><key>href</key><uri>/category/x</uri></map></map>`)
		if level < depth {
			a.embedded(b, k, level+1, depth)
		}
		b.WriteString(`</map>`)
	}
	b.WriteString(`</map><key>items</key><map>`)
	for _, it := range a.items[id] {
		fmt.Fprintf(b, `<key>%s</key><map>`, it)
		fmt.Fprintf(b, `<key>item_id</key><string>%s</string>`, it)
		fmt.Fprintf(b, `<key>parent_id</key><string>%s</string>`, id)
		fmt.Fprintf(b, `<key>asset_id</key><string>%s</string>`, uid(999))
		fmt.Fprintf(b, `<key>name</key><string>item %s</string>`, it[len(it)-3:])
		b.WriteString(`<key>desc</key><string></string>`)
		b.WriteString(`<key>type</key><integer>7</integer>`)
		b.WriteString(`<key>inv_type</key><integer>7</integer>`)
		b.WriteString(`<key>flags</key><integer>0</integer>`)
		b.WriteString(`<key>created_at</key><integer>1265521621</integer>`)
		b.WriteString(`<key>permissions</key><map>`)
		fmt.Fprintf(b, `<key>creator_id</key><string>%s</string>`, uid(777))
		fmt.Fprintf(b, `<key>owner_id</key><string>%s</string>`, uid(778))
		b.WriteString(`<key>base_mask</key><integer>2147483647</integer>`)
		b.WriteString(`<key>next_owner_mask</key><integer>532480</integer>`)
		b.WriteString(`</map>`)
		b.WriteString(`<key>sale_info</key><map><key>sale_type</key><integer>0</integer><key>sale_price</key><integer>10</integer></map>`)
		b.WriteString(`</map>`)
	}
	b.WriteString(`</map><key>links</key><map/></map>`)
}

// invSession makes a Agent with only the HTTP parts wired up, which
// is all the inventory needs.
func invSession(base string, root msg.UUID) *Agent {
	a := &Agent{
		Account:   &Account{InventoryRoot: root},
		Inventory: newInventory(root),
	}
	a.SetCaps(Caps{"InventoryAPIv3": base})
	return a
}

func TestFetchInventoryTree(t *testing.T) {
	a := newAIS()
	root := uid(0)
	// root -> two folders, one of which has a child; items scattered.
	a.folder(root, "My Inventory", "", []string{uid(1), uid(2)}, []string{uid(100)})
	a.folder(uid(1), "Objects", root, []string{uid(3)}, []string{uid(101), uid(102)})
	a.folder(uid(2), "Notecards", root, nil, []string{uid(103)})
	a.folder(uid(3), "Boxes", uid(1), nil, nil)

	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	s := invSession(srv.URL+"/cap/x", msg.MustParseUUID(root))
	if err := s.FetchInventory(context.Background(), FetchOptions{}); err != nil {
		t.Fatal(err)
	}

	folders, items := s.Inventory.Counts()
	if folders != 4 || items != 4 {
		t.Errorf("counts = %d folders, %d items; want 4 and 4", folders, items)
	}
	if a.requests != 4 {
		t.Errorf("%d requests, want one per folder", a.requests)
	}

	// Structure.
	kids := s.Inventory.Children(msg.MustParseUUID(root))
	if len(kids) != 2 || kids[0].Name != "Notecards" || kids[1].Name != "Objects" {
		t.Errorf("root children = %v", names(kids))
	}
	if got := s.Inventory.Children(msg.MustParseUUID(uid(1))); len(got) != 1 || got[0].Name != "Boxes" {
		t.Errorf("Objects children = %v", names(got))
	}
	if got := s.Inventory.Contents(msg.MustParseUUID(uid(1))); len(got) != 2 {
		t.Errorf("Objects items = %d", len(got))
	}

	// Item detail survived.
	its := s.Inventory.Contents(msg.MustParseUUID(uid(2)))
	if len(its) != 1 {
		t.Fatalf("Notecards has %d items", len(its))
	}
	it := its[0]
	if it.Type != 7 || it.InvType != 7 {
		t.Errorf("type = %d/%d", it.Type, it.InvType)
	}
	if it.CreatorID.String() != uid(777) || it.OwnerID.String() != uid(778) {
		t.Errorf("permissions = %v / %v", it.CreatorID, it.OwnerID)
	}
	if it.BaseMask != 2147483647 || it.NextOwnerMask != 532480 {
		t.Errorf("masks = %d %d", it.BaseMask, it.NextOwnerMask)
	}
	if it.SalePrice != 10 {
		t.Errorf("sale price = %d", it.SalePrice)
	}
	if it.Created == 0 {
		t.Error("no creation date")
	}

	// Paths and lookup.
	if got := s.Inventory.Path(msg.MustParseUUID(uid(3))); got != "My Inventory/Objects/Boxes" {
		t.Errorf("path = %q", got)
	}
	if f, err := s.Inventory.FindFolder("Boxes"); err != nil || f.ID.String() != uid(3) {
		t.Errorf("FindFolder = %v %v", f, err)
	}
}

// TestFindFolderTakesOneFolderOrRefuses: a name two folders carry is
// refused with both ids rather than answered with the first found, and
// a name nothing carries names what differs from it only in case.
func TestFindFolderTakesOneFolderOrRefuses(t *testing.T) {
	root := msg.MustParseUUID(uid(1))
	inv := NewInventory(root)
	inv.mu.Lock()
	for _, f := range []*Folder{
		{ID: msg.MustParseUUID(uid(2)), ParentID: root, Name: "Probe"},
		{ID: msg.MustParseUUID(uid(3)), ParentID: root, Name: "Objects"},
		{ID: msg.MustParseUUID(uid(4)), ParentID: msg.MustParseUUID(uid(3)), Name: "Probe"},
	} {
		inv.putFolder(f)
	}
	inv.mu.Unlock()

	if f, err := inv.FindFolder("Objects"); err != nil || f.ID.String() != uid(3) {
		t.Errorf("FindFolder(Objects) = %v, %v", f, err)
	}
	_, err := inv.FindFolder("Probe")
	var ne *pick.NameError
	if !errors.As(err, &ne) || len(ne.IDs) != 2 ||
		!strings.Contains(err.Error(), uid(2)) || !strings.Contains(err.Error(), uid(4)) {
		t.Errorf("FindFolder(Probe) = %v; want both folders' ids", err)
	}
	if _, err := inv.FindFolder("objects"); err == nil || !strings.Contains(err.Error(), `did you mean "Objects"?`) {
		t.Errorf("FindFolder(objects) = %v; want the case variant offered", err)
	}
}

func names(fs []*Folder) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Name
	}
	return out
}

// TestFetchInventoryConcurrent proves the fetch really overlaps
// requests.  The C client did them one at a time and took about fifty
// seconds.
func TestFetchInventoryConcurrent(t *testing.T) {
	a := newAIS()
	root := uid(0)
	var kids []string
	for i := 1; i <= 24; i++ {
		kids = append(kids, uid(i))
		a.folder(uid(i), fmt.Sprintf("f%d", i), root, nil, nil)
	}
	a.folder(root, "My Inventory", "", kids, nil)
	a.gateAt = 2

	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	s := invSession(srv.URL+"/cap/x", msg.MustParseUUID(root))
	if err := s.FetchInventory(context.Background(), FetchOptions{Concurrency: 6}); err != nil {
		t.Fatal(err)
	}

	a.mu.Lock()
	peak := a.peak
	a.mu.Unlock()
	if peak < 2 {
		t.Errorf("peak concurrency %d: nothing overlapped", peak)
	}
	if peak > 6 {
		t.Errorf("peak concurrency %d exceeds the limit of 6", peak)
	}
	if f, _ := s.Inventory.Counts(); f != 25 {
		t.Errorf("%d folders, want 25", f)
	}
}

func TestFetchInventoryProgress(t *testing.T) {
	a := newAIS()
	root := uid(0)
	a.folder(root, "My Inventory", "", []string{uid(1)}, nil)
	a.folder(uid(1), "Objects", root, nil, []string{uid(100)})

	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	s := invSession(srv.URL+"/cap/x", msg.MustParseUUID(root))
	var calls atomic.Int64
	err := s.FetchInventory(context.Background(), FetchOptions{
		OnFolder: func(f *Folder, folders, items int) { calls.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("progress called %d times, want 2", calls.Load())
	}
}

// TestFetchInventoryTolerates: one bad folder should not lose the tree.
func TestFetchInventoryTolerates(t *testing.T) {
	a := newAIS()
	root := uid(0)
	a.folder(root, "My Inventory", "", []string{uid(1), uid(2)}, nil)
	a.folder(uid(1), "Broken", root, nil, nil)
	a.folder(uid(2), "Fine", root, nil, []string{uid(100)})
	a.fail[uid(1)] = true

	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	s := invSession(srv.URL+"/cap/x", msg.MustParseUUID(root))
	err := s.FetchInventory(context.Background(), FetchOptions{Concurrency: 1})
	if err == nil {
		t.Error("the failure should be reported")
	}
	// The rest of the tree still arrived.
	if _, ok := s.Inventory.Folder(msg.MustParseUUID(uid(2))); !ok {
		t.Error("a sibling of the broken folder was lost")
	}
	if _, items := s.Inventory.Counts(); items != 1 {
		t.Errorf("%d items, want the one from the good folder", items)
	}
}

func TestFetchInventoryGivesUp(t *testing.T) {
	a := newAIS()
	root := uid(0)
	var kids []string
	for i := 1; i <= 30; i++ {
		kids = append(kids, uid(i))
		a.folder(uid(i), "x", root, nil, nil)
		a.fail[uid(i)] = true
	}
	a.folder(root, "My Inventory", "", kids, nil)

	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	s := invSession(srv.URL+"/cap/x", msg.MustParseUUID(root))
	if err := s.FetchInventory(context.Background(), FetchOptions{MaxFailures: 3}); err == nil {
		t.Fatal("expected an error")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.requests > 20 {
		t.Errorf("made %d requests after giving up at 3 failures", a.requests)
	}
}

func TestFetchInventoryNeedsTheCapability(t *testing.T) {
	s := &Agent{Account: &Account{}, Inventory: newInventory(msg.UUID{})}
	err := s.FetchInventory(context.Background(), FetchOptions{})
	if err == nil || !strings.Contains(err.Error(), "InventoryAPIv3") {
		t.Errorf("err = %v", err)
	}
}

func TestFetchInventoryLinks(t *testing.T) {
	root := uid(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<llsd><map>
		  <key>category_id</key><string>%s</string>
		  <key>name</key><string>Current Outfit</string>
		  <key>_embedded</key><map>
		    <key>categories</key><map/>
		    <key>items</key><map/>
		    <key>links</key><map>
		      <key>%s</key><map>
		        <key>item_id</key><string>%s</string>
		        <key>parent_id</key><string>%s</string>
		        <key>name</key><string>a link</string>
		        <key>type</key><integer>24</integer>
		        <key>inv_type</key><integer>18</integer>
		        <key>linked_id</key><string>%s</string>
		      </map>
		    </map>
		  </map></map></llsd>`, root, uid(5), uid(5), root, uid(9))
	}))
	defer srv.Close()

	s := invSession(srv.URL+"/cap/x", msg.MustParseUUID(root))
	if err := s.FetchInventory(context.Background(), FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	it, ok := s.Inventory.Item(msg.MustParseUUID(uid(5)))
	if !ok {
		t.Fatal("the link was not recorded")
	}
	if !it.IsLink {
		t.Error("link not marked as one")
	}
	// linked_id lands where asset_id would, as the UDP path did.
	if it.AssetID.String() != uid(9) {
		t.Errorf("linked asset = %v, want %v", it.AssetID, uid(9))
	}
}

// TestFetchOneFolderWithoutDescending: a client that wants the contents
// of one folder should not have to walk the whole tree to get them, and
// asking for a depth is one round trip however deep it goes -- which is
// the difference between reading a folder and taking the better part of
// a minute over an inventory.
func TestFetchOneFolderWithoutDescending(t *testing.T) {
	a := newAIS()
	root, kid, grandkid := uid(0), uid(1), uid(2)
	a.folder(root, "My Inventory", "", []string{kid}, nil)
	a.folder(kid, "Objects", root, []string{grandkid}, []string{uid(10)})
	a.folder(grandkid, "Deeper", kid, nil, []string{uid(11)})

	hs := httptest.NewServer(a.handler())
	defer hs.Close()

	// One folder, no descent: its child is named but not fetched, so
	// nothing inside the child is known.
	s := invSession(hs.URL, msg.MustParseUUID(root))
	if err := s.FetchFolder(context.Background(), msg.MustParseUUID(kid)); err != nil {
		t.Fatal(err)
	}
	if a.requests != 1 {
		t.Errorf("%d requests for one folder", a.requests)
	}
	if _, ok := s.Inventory.Item(msg.MustParseUUID(uid(11))); ok {
		t.Error("something inside the child folder was fetched")
	}

	// With a depth, the reply nests each child's own contents inside it,
	// and reading only the outer one would throw away everything the
	// extra round trip was avoided for.
	deep := newInventory(msg.MustParseUUID(root))
	before := a.requests
	if err := FetchFolderDepth(context.Background(), s, deep, msg.MustParseUUID(kid), 2); err != nil {
		t.Fatal(err)
	}
	if n := a.requests - before; n != 1 {
		t.Errorf("a depth request cost %d round trips", n)
	}

	// A depth below zero is no depth at all rather than an error.
	if err := FetchFolderDepth(context.Background(), s, deep, msg.MustParseUUID(kid), -1); err != nil {
		t.Fatal(err)
	}

	// And none of it is possible without the capability.
	none := &Agent{Account: &Account{}, Inventory: newInventory(msg.UUID{})}
	if err := FetchFolder(context.Background(), none, none.Inventory, msg.UUID{}); err == nil {
		t.Error("expected an error with no InventoryAPIv3")
	}
	if err := none.FetchFolder(context.Background(), msg.UUID{}); err == nil {
		t.Error("expected an error with no InventoryAPIv3")
	}
}

// TestWalkingTheTree: the same descent an "ls -R" would do, and the only
// thing that visits a folder and everything under it in order.
func TestWalkingTheTree(t *testing.T) {
	a := newAIS()
	root, objects, clothes, deeper := uid(0), uid(1), uid(2), uid(3)
	a.folder(root, "My Inventory", "", []string{objects, clothes}, nil)
	a.folder(objects, "Objects", root, []string{deeper}, nil)
	a.folder(deeper, "Boxes", objects, nil, nil)
	a.folder(clothes, "Clothing", root, nil, nil)

	hs := httptest.NewServer(a.handler())
	defer hs.Close()

	s := invSession(hs.URL, msg.MustParseUUID(root))
	if err := s.FetchInventory(context.Background(), FetchOptions{}); err != nil {
		t.Fatal(err)
	}

	var seen []string
	s.Inventory.Walk(func(f *Folder, depth int) bool {
		seen = append(seen, fmt.Sprintf("%d:%s", depth, f.Name))
		return true
	})
	want := []string{"0:My Inventory", "1:Clothing", "1:Objects", "2:Boxes"}
	if len(seen) != len(want) {
		t.Fatalf("walked %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("walked %v, want %v", seen, want)
			break
		}
	}

	// Returning false stops the descent into that folder and nothing
	// else, which is what makes the walk usable for a partial listing.
	seen = nil
	s.Inventory.Walk(func(f *Folder, depth int) bool {
		seen = append(seen, f.Name)
		return f.Name != "Objects"
	})
	for _, name := range seen {
		if name == "Boxes" {
			t.Errorf("the walk descended into a folder it was told not to: %v", seen)
		}
	}

	// A tree whose root has never been fetched still walks whatever is
	// under it rather than refusing.
	empty := NewInventory(msg.MustParseUUID(root))
	empty.Walk(func(*Folder, int) bool {
		t.Error("an empty tree walked something")
		return true
	})

	// And a walk stopped at the root goes no further.
	s.Inventory.Walk(func(f *Folder, depth int) bool { return false })
}

// TestFetchingTheSameFolderTwiceUpdatesRatherThanDuplicates: a fetch may
// be asked for again after something changed, and the second answer has
// to replace the first rather than list every item twice.
func TestFetchingTheSameFolderTwiceUpdatesRatherThanDuplicates(t *testing.T) {
	a := newAIS()
	root := uid(0)
	a.folder(root, "My Inventory", "", nil, []string{uid(10), uid(11)})

	hs := httptest.NewServer(a.handler())
	defer hs.Close()

	s := invSession(hs.URL, msg.MustParseUUID(root))
	for range 2 {
		if err := s.FetchFolder(context.Background(), msg.MustParseUUID(root)); err != nil {
			t.Fatal(err)
		}
	}
	if _, items := s.Inventory.Counts(); items != 2 {
		t.Errorf("%d items after fetching the same folder twice", items)
	}
	if got := s.Inventory.Contents(msg.MustParseUUID(root)); len(got) != 2 {
		t.Errorf("the folder lists %d items", len(got))
	}
}

// holds is what a folder lists, child folders and items alike, by id in
// order.
func holds(inv *Inventory, id string) []string {
	var out []string
	for _, f := range inv.Children(msg.MustParseUUID(id)) {
		out = append(out, f.ID.String())
	}
	for _, i := range inv.Contents(msg.MustParseUUID(id)) {
		out = append(out, i.ID.String())
	}
	slices.Sort(out)
	return out
}

func wantHolds(t *testing.T, inv *Inventory, id, name string, want ...string) {
	t.Helper()
	if got := holds(inv, id); !slices.Equal(got, want) {
		t.Errorf("%s holds %v, want %v", name, got, want)
	}
}

// TestReReadingAfterAnItemMoved: an item read again with a new parent is
// listed under that one and not the old, without the old folder being
// read again -- the viewer moves it the same way.
func TestReReadingAfterAnItemMoved(t *testing.T) {
	a := newAIS()
	root, from, to, thing := uid(0), uid(1), uid(2), uid(100)
	a.folder(root, "My Inventory", "", []string{from, to}, nil)
	a.folder(from, "From", root, nil, []string{thing})
	a.folder(to, "To", root, nil, nil)

	hs := httptest.NewServer(a.handler())
	defer hs.Close()
	ctx := context.Background()

	s := invSession(hs.URL, msg.MustParseUUID(root))
	if err := s.FetchInventory(ctx, FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	wantHolds(t, s.Inventory, from, "the first folder", thing)

	a.folder(from, "From", root, nil, nil)
	a.folder(to, "To", root, nil, []string{thing})

	if err := s.FetchFolder(ctx, msg.MustParseUUID(to)); err != nil {
		t.Fatal(err)
	}
	wantHolds(t, s.Inventory, from, "the folder it left")
	wantHolds(t, s.Inventory, to, "the folder it went to", thing)
	if it, ok := s.Inventory.Item(msg.MustParseUUID(thing)); !ok || it.ParentID.String() != to {
		t.Errorf("the item's record = %+v", it)
	}

	// Reading the whole tree again comes to the same.
	if err := s.FetchInventory(ctx, FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	wantHolds(t, s.Inventory, from, "the folder it left")
	wantHolds(t, s.Inventory, to, "the folder it went to", thing)
	if _, items := s.Inventory.Counts(); items != 1 {
		t.Errorf("%d items, want 1", items)
	}
}

// TestReReadingAfterAFolderMoved: the same for a folder, which takes
// what is inside it along.  Reading the folder it left afterwards does
// not lose it: it is no longer listed there to be lost.
func TestReReadingAfterAFolderMoved(t *testing.T) {
	a := newAIS()
	root, from, to, moved, inside := uid(0), uid(1), uid(2), uid(3), uid(100)
	a.folder(root, "My Inventory", "", []string{from, to}, nil)
	a.folder(from, "From", root, []string{moved}, nil)
	a.folder(to, "To", root, nil, nil)
	a.folder(moved, "Moved", from, nil, []string{inside})

	hs := httptest.NewServer(a.handler())
	defer hs.Close()
	ctx := context.Background()

	s := invSession(hs.URL, msg.MustParseUUID(root))
	if err := s.FetchInventory(ctx, FetchOptions{}); err != nil {
		t.Fatal(err)
	}

	a.folder(from, "From", root, nil, nil)
	a.folder(to, "To", root, []string{moved}, nil)
	a.folder(moved, "Moved", to, nil, []string{inside})

	for _, id := range []string{to, from} {
		if err := s.FetchFolder(ctx, msg.MustParseUUID(id)); err != nil {
			t.Fatal(err)
		}
	}
	wantHolds(t, s.Inventory, from, "the folder it left")
	wantHolds(t, s.Inventory, to, "the folder it went to", moved)
	wantHolds(t, s.Inventory, moved, "the folder that moved", inside)
	if got := s.Inventory.Path(msg.MustParseUUID(moved)); got != "My Inventory/To/Moved" {
		t.Errorf("path = %q", got)
	}

	// And a whole reading, in whatever order the folders answer.
	if err := s.FetchInventory(ctx, FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	wantHolds(t, s.Inventory, from, "the folder it left")
	wantHolds(t, s.Inventory, to, "the folder it went to", moved)
	wantHolds(t, s.Inventory, moved, "the folder that moved", inside)
	if folders, items := s.Inventory.Counts(); folders != 4 || items != 1 {
		t.Errorf("%d folders and %d items, want 4 and 1", folders, items)
	}
}

// TestReReadingAfterAnItemWasDeleted: a folder read whole again no longer
// lists an item that has gone from it, and the item is forgotten.
func TestReReadingAfterAnItemWasDeleted(t *testing.T) {
	a := newAIS()
	root, kept, gone := uid(0), uid(10), uid(11)
	a.folder(root, "My Inventory", "", nil, []string{kept, gone})

	hs := httptest.NewServer(a.handler())
	defer hs.Close()
	ctx := context.Background()

	s := invSession(hs.URL, msg.MustParseUUID(root))
	if err := s.FetchFolder(ctx, msg.MustParseUUID(root)); err != nil {
		t.Fatal(err)
	}
	a.folder(root, "My Inventory", "", nil, []string{kept})
	if err := s.FetchFolder(ctx, msg.MustParseUUID(root)); err != nil {
		t.Fatal(err)
	}

	wantHolds(t, s.Inventory, root, "the folder", kept)
	if _, ok := s.Inventory.Item(msg.MustParseUUID(gone)); ok {
		t.Error("the deleted item is still known")
	}
	if _, items := s.Inventory.Counts(); items != 1 {
		t.Errorf("%d items, want 1", items)
	}
}

// TestReReadingAfterAFolderWasDeleted: the same for a folder, which
// takes everything under it with it and leaves its siblings alone.
func TestReReadingAfterAFolderWasDeleted(t *testing.T) {
	a := newAIS()
	root, gone, deeper, kept := uid(0), uid(1), uid(2), uid(3)
	a.folder(root, "My Inventory", "", []string{gone, kept}, nil)
	a.folder(gone, "Gone", root, []string{deeper}, []string{uid(100)})
	a.folder(deeper, "Deeper", gone, nil, []string{uid(101)})
	a.folder(kept, "Kept", root, nil, []string{uid(102)})

	hs := httptest.NewServer(a.handler())
	defer hs.Close()
	ctx := context.Background()

	s := invSession(hs.URL, msg.MustParseUUID(root))
	if err := s.FetchInventory(ctx, FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	a.folder(root, "My Inventory", "", []string{kept}, nil)
	if err := s.FetchFolder(ctx, msg.MustParseUUID(root)); err != nil {
		t.Fatal(err)
	}

	wantHolds(t, s.Inventory, root, "the root", kept)
	wantHolds(t, s.Inventory, kept, "the folder beside it", uid(102))
	for _, id := range []string{gone, deeper} {
		if _, ok := s.Inventory.Folder(msg.MustParseUUID(id)); ok {
			t.Errorf("folder %s is still known", id)
		}
		wantHolds(t, s.Inventory, id, "a folder that went")
	}
	for _, id := range []string{uid(100), uid(101)} {
		if _, ok := s.Inventory.Item(msg.MustParseUUID(id)); ok {
			t.Errorf("item %s, which was inside it, is still known", id)
		}
	}
	if folders, items := s.Inventory.Counts(); folders != 2 || items != 1 {
		t.Errorf("%d folders and %d items, want 2 and 1", folders, items)
	}
}

// TestADeeperReadingIsWholeDownToItsDepth: a reply to ?depth=1 carries
// all of each child folder, so what has gone from one goes here too; it
// carries nothing of the folders below those, so what is known of them
// stays until they are read.
func TestADeeperReadingIsWholeDownToItsDepth(t *testing.T) {
	a := newAIS()
	root, kid, grandkid := uid(0), uid(1), uid(2)
	a.folder(root, "My Inventory", "", []string{kid}, nil)
	a.folder(kid, "Kid", root, []string{grandkid}, []string{uid(100), uid(101)})
	a.folder(grandkid, "Grandkid", kid, nil, []string{uid(102), uid(103)})

	hs := httptest.NewServer(a.handler())
	defer hs.Close()
	ctx := context.Background()

	s := invSession(hs.URL, msg.MustParseUUID(root))
	if err := s.FetchInventory(ctx, FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	a.folder(kid, "Kid", root, []string{grandkid}, []string{uid(100)})
	a.folder(grandkid, "Grandkid", kid, nil, []string{uid(102)})

	if err := FetchFolderDepth(ctx, s, s.Inventory, msg.MustParseUUID(root), 1); err != nil {
		t.Fatal(err)
	}
	wantHolds(t, s.Inventory, kid, "the child", grandkid, uid(100))
	wantHolds(t, s.Inventory, grandkid, "the grandchild, not read", uid(102), uid(103))

	if err := FetchFolderDepth(ctx, s, s.Inventory, msg.MustParseUUID(root), 2); err != nil {
		t.Fatal(err)
	}
	wantHolds(t, s.Inventory, grandkid, "the grandchild, read", uid(102))
}

// TestADeeperReadingOfAMove: one reply to ?depth=1 reads whole both the
// folder a folder left and the one it went to, and names the one that
// moved without its contents.  It keeps them, whichever of its two
// parents the reply is read in first; a folder the reply no longer
// names anywhere goes, with what was in it.  The parents are read in
// map order, so each round is a fresh draw of it.
func TestADeeperReadingOfAMove(t *testing.T) {
	root, from, to, moved, gone := uid(0), uid(1), uid(2), uid(3), uid(4)
	inside, lost, thing := uid(100), uid(101), uid(102)
	a := newAIS()
	hs := httptest.NewServer(a.handler())
	defer hs.Close()
	ctx := context.Background()

	for range 32 {
		a.folder(root, "My Inventory", "", []string{from, to}, nil)
		a.folder(from, "From", root, []string{moved, gone}, []string{thing})
		a.folder(to, "To", root, nil, nil)
		a.folder(moved, "Moved", from, nil, []string{inside})
		a.folder(gone, "Gone", from, nil, []string{lost})
		s := invSession(hs.URL, msg.MustParseUUID(root))
		if err := s.FetchInventory(ctx, FetchOptions{}); err != nil {
			t.Fatal(err)
		}

		a.folder(from, "From", root, nil, nil)
		a.folder(to, "To", root, []string{moved}, []string{thing})
		a.folder(moved, "Moved", to, nil, []string{inside})
		if err := FetchFolderDepth(ctx, s, s.Inventory, msg.MustParseUUID(root), 1); err != nil {
			t.Fatal(err)
		}

		wantHolds(t, s.Inventory, from, "the folder it left")
		wantHolds(t, s.Inventory, to, "the folder it went to", moved, thing)
		wantHolds(t, s.Inventory, moved, "the folder that moved", inside)
		if _, ok := s.Inventory.Folder(msg.MustParseUUID(gone)); ok {
			t.Error("the folder that went is still known")
		}
		if _, ok := s.Inventory.Item(msg.MustParseUUID(lost)); ok {
			t.Error("the item that was in the folder that went is still known")
		}
		if folders, items := s.Inventory.Counts(); folders != 4 || items != 2 {
			t.Errorf("%d folders and %d items, want 4 and 2", folders, items)
		}
		if t.Failed() {
			return
		}
	}
}

// TestAPartOfAFolderIsNotTakenForAllOfIt: only a listing that is plainly
// the whole of a folder loses anything from it.  The viewer takes a
// folder's contents as known only when AIS sends categories, items and
// links all three, within the depth that was asked for.
func TestAPartOfAFolderIsNotTakenForAllOfIt(t *testing.T) {
	root, kid, thing, inside := uid(0), uid(1), uid(100), uid(101)
	a := newAIS()
	a.folder(root, "My Inventory", "", []string{kid}, []string{thing})
	a.folder(kid, "Kid", root, nil, []string{inside})
	full := httptest.NewServer(a.handler())
	defer full.Close()
	ctx := context.Background()

	self := func(id, parent string) string {
		return fmt.Sprintf(`<key>category_id</key><string>%s</string>`+
			`<key>parent_id</key><string>%s</string>`+
			`<key>name</key><string>My Inventory</string>`, id, parent)
	}
	emptyKid := fmt.Sprintf(`<key>%s</key><map><key>category_id</key><string>%s</string>`+
		`<key>parent_id</key><string>%s</string><key>name</key><string>Kid</string>`+
		`<key>_embedded</key><map><key>categories</key><map/><key>items</key><map/><key>links</key><map/></map>`+
		`</map>`, kid, kid, root)
	plainKid := fmt.Sprintf(`<key>%s</key><map><key>category_id</key><string>%s</string>`+
		`<key>parent_id</key><string>%s</string><key>name</key><string>Kid</string></map>`, kid, kid, root)

	for _, c := range []struct {
		name string
		body string
	}{
		{"no links", self(root, root) + `<key>_embedded</key><map><key>categories</key><map>` + plainKid +
			`</map><key>items</key><map/></map>`},
		{"no items", self(root, root) + `<key>_embedded</key><map><key>categories</key><map>` + plainKid +
			`</map><key>links</key><map/></map>`},
		{"no categories", self(root, root) + `<key>_embedded</key><map><key>items</key><map/><key>links</key><map/></map>`},
		{"a child's contents below the depth asked for", self(root, root) +
			`<key>_embedded</key><map><key>categories</key><map>` + emptyKid +
			fmt.Sprintf(`</map><key>items</key><map><key>%s</key><map><key>item_id</key><string>%s</string>`+
				`<key>parent_id</key><string>%s</string><key>name</key><string>item 100</string></map></map>`, thing, thing, root) +
			`<key>links</key><map/></map>`},
		{"a reply naming another folder", self(uid(5), uid(6)) +
			`<key>_embedded</key><map><key>categories</key><map/><key>items</key><map/><key>links</key><map/></map>`},
		{"a reply naming no folder", `<key>name</key><string>My Inventory</string>` +
			`<key>_embedded</key><map><key>categories</key><map/><key>items</key><map/><key>links</key><map/></map>`},
	} {
		t.Run(c.name, func(t *testing.T) {
			inv := NewInventory(msg.MustParseUUID(root))
			if err := FetchInventory(ctx, invSession(full.URL, msg.MustParseUUID(root)), inv, FetchOptions{}); err != nil {
				t.Fatal(err)
			}

			part := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `<llsd><map>`+c.body+`</map></llsd>`)
			}))
			defer part.Close()
			if err := FetchFolder(ctx, invSession(part.URL, msg.MustParseUUID(root)), inv, msg.MustParseUUID(root)); err != nil {
				t.Fatal(err)
			}

			wantHolds(t, inv, root, "the root", kid, thing)
			wantHolds(t, inv, kid, "the child", inside)
			if _, items := inv.Counts(); items != 2 {
				t.Errorf("%d items, want 2", items)
			}
		})
	}
}

// TestAnInventoryReplyThatIsNotOne: this is HTTPS against a capability,
// so anything at all can come back -- a proxy's error page, a truncated
// body, a simulator answering a different question.
func TestAnInventoryReplyThatIsNotOne(t *testing.T) {
	long := strings.Repeat("this explanation goes on and on. ", 40)

	for _, c := range []struct {
		name   string
		status int
		body   string
		says   string
	}{
		{"a refusal with a great deal to say", http.StatusForbidden, long, "status 403"},
		{"a body that is not LLSD", http.StatusOK, "<not-llsd", "inventory"},
		{"LLSD that is not a map", http.StatusOK, `<llsd><string>hello</string></llsd>`, "wanted a map"},
	} {
		t.Run(c.name, func(t *testing.T) {
			hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				fmt.Fprint(w, c.body)
			}))
			defer hs.Close()

			s := invSession(hs.URL, msg.MustParseUUID(uid(0)))
			err := s.FetchFolder(context.Background(), msg.MustParseUUID(uid(0)))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("err = %v, should mention %q", err, c.says)
			}
			// The explanation is kept, but not all of it.
			if len(err.Error()) > 512 {
				t.Errorf("the error is %d characters long", len(err.Error()))
			}
		})
	}
}

// TestAnEntryThatOnlyItsKeyNames: AIS keys the embedded maps by id, so
// an entry that leaves the id out of its own body is still an entry --
// the key is the id.  An entry that has it neither way is nothing at
// all, and taking one in would put something with the zero id in the
// tree, where it would be the parent of everything else with no parent.
func TestAnEntryThatOnlyItsKeyNames(t *testing.T) {
	root, named, itemKeyed := uid(0), uid(1), uid(20)

	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<llsd><map>
			<key>category_id</key><string>%s</string>
			<key>name</key><string>My Inventory</string>
			<key>_embedded</key><map>
				<key>categories</key><map>
					<key>%s</key><map>
						<key>category_id</key><string>%s</string>
						<key>name</key><string>Named In Its Body</string>
					</map>
					<key>not-a-uuid</key><map><key>name</key><string>Named By Nothing</string></map>
					<key>%s</key><string>not a map</string>
				</map>
				<key>items</key><map>
					<key>%s</key><map><key>name</key><string>Keyed Item</string></map>
					<key>not-a-uuid</key><map><key>name</key><string>Unidentifiable</string></map>
					<key>%s</key><string>not a map</string>
				</map>
			</map>
		</map></llsd>`, root, named, named, uid(9), itemKeyed, uid(19))
	}))
	defer hs.Close()

	s := invSession(hs.URL, msg.MustParseUUID(root))
	if err := s.FetchFolder(context.Background(), msg.MustParseUUID(root)); err != nil {
		t.Fatal(err)
	}

	if f, ok := s.Inventory.Folder(msg.MustParseUUID(named)); !ok || f.Name != "Named In Its Body" {
		t.Errorf("folder = %+v", f)
	}
	if it, ok := s.Inventory.Item(msg.MustParseUUID(itemKeyed)); !ok || it.Name != "Keyed Item" {
		t.Errorf("the item named only by its key was lost: %+v", it)
	}

	// Nothing that could not be identified came in, and in particular
	// nothing came in under the zero id.
	folders, items := s.Inventory.Counts()
	if folders != 2 || items != 1 {
		t.Errorf("%d folders and %d items; something unidentifiable was taken in", folders, items)
	}
	if _, ok := s.Inventory.Folder(msg.UUID{}); ok {
		t.Error("a folder with the zero id was taken in")
	}
	if _, ok := s.Inventory.Item(msg.UUID{}); ok {
		t.Error("an item with the zero id was taken in")
	}
}

// TestAFolderNamedOnlyByItsKeyIsKept: a category whose body omits
// category_id takes its id from the key it is filed under, which is
// what absorb's fallback was written for.
//
// The fallback could not run.  folderFrom refused a category with no
// category_id before the caller ever got the chance, so such a folder
// was dropped -- while an item in the same position was kept, because
// itemFrom refuses nothing.  Two halves of one reply treated
// differently by accident.
func TestAFolderNamedOnlyByItsKeyIsKept(t *testing.T) {
	root, keyed := uid(0), uid(2)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<llsd><map>
			<key>category_id</key><string>%s</string>
			<key>name</key><string>My Inventory</string>
			<key>_embedded</key><map><key>categories</key><map>
				<key>%s</key><map><key>name</key><string>Named By Its Key</string></map>
			</map></map>
		</map></llsd>`, root, keyed)
	}))
	defer hs.Close()

	s := invSession(hs.URL, msg.MustParseUUID(root))
	if err := s.FetchFolder(context.Background(), msg.MustParseUUID(root)); err != nil {
		t.Fatal(err)
	}
	if f, ok := s.Inventory.Folder(msg.MustParseUUID(keyed)); !ok || f.Name != "Named By Its Key" {
		t.Errorf("the folder named only by its key was lost: %+v", f)
	}
}
