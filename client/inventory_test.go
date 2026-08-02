package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"slgo/msg"
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

func (a *aisServer) folder(id, name string, parent string, kids []string, items []string) {
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

		a.mu.Lock()
		kids := a.tree[id]
		items := a.items[id]
		name := a.names[id]
		parent := a.parents[id]
		if parent == "" {
			parent = uid(0)
		}
		a.mu.Unlock()

		var b strings.Builder
		b.WriteString(`<?xml version="1.0" ?><llsd><map>`)
		fmt.Fprintf(&b, `<key>category_id</key><string>%s</string>`, id)
		fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string>`, parent)
		fmt.Fprintf(&b, `<key>name</key><string>%s</string>`, name)
		b.WriteString(`<key>type_default</key><integer>-1</integer>`)
		b.WriteString(`<key>version</key><integer>3</integer>`)
		b.WriteString(`<key>_embedded</key><map><key>categories</key><map>`)
		for _, k := range kids {
			fmt.Fprintf(&b, `<key>%s</key><map>`, k)
			fmt.Fprintf(&b, `<key>category_id</key><string>%s</string>`, k)
			fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string>`, id)
			fmt.Fprintf(&b, `<key>name</key><string>%s</string>`, a.names[k])
			b.WriteString(`<key>type_default</key><integer>-1</integer>`)
			b.WriteString(`<key>version</key><integer>1</integer>`)
			b.WriteString(`<key>_links</key><map><key>self</key><map><key>href</key><uri>/category/x</uri></map></map>`)
			b.WriteString(`</map>`)
		}
		b.WriteString(`</map><key>items</key><map>`)
		for _, it := range items {
			fmt.Fprintf(&b, `<key>%s</key><map>`, it)
			fmt.Fprintf(&b, `<key>item_id</key><string>%s</string>`, it)
			fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string>`, id)
			fmt.Fprintf(&b, `<key>asset_id</key><string>%s</string>`, uid(999))
			fmt.Fprintf(&b, `<key>name</key><string>item %s</string>`, it[len(it)-3:])
			b.WriteString(`<key>desc</key><string></string>`)
			b.WriteString(`<key>type</key><integer>7</integer>`)
			b.WriteString(`<key>inv_type</key><integer>7</integer>`)
			b.WriteString(`<key>flags</key><integer>0</integer>`)
			b.WriteString(`<key>created_at</key><integer>1265521621</integer>`)
			b.WriteString(`<key>permissions</key><map>`)
			fmt.Fprintf(&b, `<key>creator_id</key><string>%s</string>`, uid(777))
			fmt.Fprintf(&b, `<key>owner_id</key><string>%s</string>`, uid(778))
			b.WriteString(`<key>base_mask</key><integer>2147483647</integer>`)
			b.WriteString(`<key>next_owner_mask</key><integer>532480</integer>`)
			b.WriteString(`</map>`)
			b.WriteString(`<key>sale_info</key><map><key>sale_type</key><integer>0</integer><key>sale_price</key><integer>10</integer></map>`)
			b.WriteString(`</map>`)
		}
		b.WriteString(`</map><key>links</key><map/></map></map></llsd>`)

		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprint(w, b.String())
	})
}

// invSession makes a Session with only the HTTP parts wired up, which
// is all the inventory needs.
func invSession(base string, root msg.UUID) *Session {
	return &Session{
		Account:   &Account{InventoryRoot: root},
		Caps:      Caps{"InventoryAPIv3": base},
		Inventory: newInventory(root),
	}
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
	if f, ok := s.Inventory.FindFolder("Boxes"); !ok || f.ID.String() != uid(3) {
		t.Errorf("FindFolder = %v %v", f, ok)
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
	s := &Session{Account: &Account{}, Caps: Caps{}, Inventory: newInventory(msg.UUID{})}
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
