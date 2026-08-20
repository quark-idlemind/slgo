package agent

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// Nothing in msg or client keeps per-connection state at package
// level, so one process can hold as many sessions as it likes.  These
// tests exist to keep it that way: a package level counter, cache or
// registry added later would show up here.

// TestManySessionsAtOnce runs several complete sessions against
// separate simulators concurrently and checks that none of them sees
// another's traffic.
func TestManySessionsAtOnce(t *testing.T) {
	const n = 5

	type rig struct {
		sim  *fakeSim
		sess *Agent
		acct *Account
	}
	rigs := make([]*rig, n)

	for i := range rigs {
		sim := newFakeSim(t)
		sim.regionNm = fmt.Sprintf("Region%d", i)
		go sim.run()
		rigs[i] = &rig{sim: sim}
	}
	defer func() {
		for _, r := range rigs {
			if r.sess != nil {
				r.sess.Close()
			}
			r.sim.close()
		}
	}()

	// Connect them all at the same time, which is the case that
	// would break on shared state.
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i, r := range rigs {
		wg.Add(1)
		go func(i int, r *rig) {
			defer wg.Done()
			a := testAccount(r.sim)
			// Each account is a different agent.
			a.AgentID = msg.MustParseUUID(uid(100 + i))
			a.SessionID = msg.MustParseUUID(uid(200 + i))
			a.CircuitCode = uint32(1000 + i)
			a.InventoryRoot = msg.MustParseUUID(uid(300 + i))
			a.FirstName = fmt.Sprintf("Agent%d", i)
			r.acct = a

			s, err := Connect(context.Background(), a, Options{
				Timeout:  10 * time.Second,
				SkipCaps: true,
			})
			if err != nil {
				errs[i] = err
				return
			}
			r.sess = s
		}(i, r)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}

	// Each session must have landed in its own region, with its own
	// account and its own inventory root.
	for i, r := range rigs {
		if err := r.sess.WaitForRegionHandshake(context.Background(), 5*time.Second); err != nil {
			t.Fatalf("session %d handshake: %v", i, err)
		}
		want := fmt.Sprintf("Region%d", i)
		if got := r.sess.RegionName(); got != want {
			t.Errorf("session %d is in %q, want %q", i, got, want)
		}
		if got := r.sess.Account.FirstName; got != fmt.Sprintf("Agent%d", i) {
			t.Errorf("session %d is %s", i, got)
		}
		if got := r.sess.Inventory.Root(); got != msg.MustParseUUID(uid(300+i)) {
			t.Errorf("session %d inventory root = %v", i, got)
		}
		if r.sess.sock.local().String() == rigs[0].sess.sock.local().String() && i != 0 {
			t.Errorf("session %d shares a socket with session 0", i)
		}
	}

	// Traffic must not have crossed: each simulator saw exactly one
	// circuit opened, and it was its own agent's.
	for i, r := range rigs {
		var opens int
		for _, name := range r.sim.got() {
			if name == "UseCircuitCode" {
				opens++
			}
		}
		if opens != 1 {
			t.Errorf("simulator %d saw %d UseCircuitCode, want 1: %v", i, opens, r.sim.got())
		}
	}

	// Statistics are per session, not shared.
	for i, r := range rigs {
		if got := r.sess.Send.Stats().Sent; got == 0 {
			t.Errorf("session %d sent nothing", i)
		}
		if got := r.sess.Recv.Stats().Packets; got == 0 {
			t.Errorf("session %d received nothing", i)
		}
	}
}

// TestSessionsHaveSeparateInventories fetches two inventories at once
// from two AIS servers and checks neither leaks into the other.
func TestSessionsHaveSeparateInventories(t *testing.T) {
	mk := func(prefix int, folders int) (*httptest.Server, *aisServer, string) {
		a := newAIS()
		root := uid(prefix)
		var kids []string
		for i := 1; i <= folders; i++ {
			id := uid(prefix + i)
			kids = append(kids, id)
			a.folder(id, fmt.Sprintf("p%d-f%d", prefix, i), root, nil, []string{uid(prefix + 500 + i)})
		}
		a.folder(root, fmt.Sprintf("Inventory%d", prefix), "", kids, nil)
		return httptest.NewServer(a.handler()), a, root
	}

	srvA, _, rootA := mk(1000, 6)
	defer srvA.Close()
	srvB, _, rootB := mk(2000, 9)
	defer srvB.Close()

	sa := invSession(srvA.URL+"/cap/x", msg.MustParseUUID(rootA))
	sb := invSession(srvB.URL+"/cap/x", msg.MustParseUUID(rootB))

	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() { defer wg.Done(); errA = sa.FetchInventory(context.Background(), FetchOptions{}) }()
	go func() { defer wg.Done(); errB = sb.FetchInventory(context.Background(), FetchOptions{}) }()
	wg.Wait()

	if errA != nil || errB != nil {
		t.Fatalf("fetch: %v / %v", errA, errB)
	}

	fa, ia := sa.Inventory.Counts()
	fb, ib := sb.Inventory.Counts()
	if fa != 7 || ia != 6 {
		t.Errorf("inventory A = %d folders, %d items; want 7 and 6", fa, ia)
	}
	if fb != 10 || ib != 9 {
		t.Errorf("inventory B = %d folders, %d items; want 10 and 9", fb, ib)
	}
	// Nothing from B may be visible in A.
	if _, ok := sa.Inventory.Folder(msg.MustParseUUID(uid(2001))); ok {
		t.Error("a folder from inventory B turned up in A")
	}
	if _, ok := sb.Inventory.Folder(msg.MustParseUUID(uid(1001))); ok {
		t.Error("a folder from inventory A turned up in B")
	}
}

// TestConcurrentInventoryReadsAndWrites checks the tree is safe to read
// while it is being filled, which is what OnFolder invites.
func TestConcurrentInventoryReadsAndWrites(t *testing.T) {
	a := newAIS()
	root := uid(0)
	var kids []string
	for i := 1; i <= 40; i++ {
		kids = append(kids, uid(i))
		a.folder(uid(i), fmt.Sprintf("f%d", i), root, nil, []string{uid(500 + i)})
	}
	a.folder(root, "My Inventory", "", kids, nil)

	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	s := invSession(srv.URL+"/cap/x", msg.MustParseUUID(root))

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				s.Inventory.Counts()
				s.Inventory.Children(msg.MustParseUUID(root))
				s.Inventory.Path(msg.MustParseUUID(uid(7)))
				s.Inventory.FindFolder("f9")
			}
		}()
	}

	err := s.FetchInventory(context.Background(), FetchOptions{
		Concurrency: 8,
		OnFolder: func(f *Folder, folders, items int) {
			s.Inventory.Counts()
		},
	})
	close(stop)
	readers.Wait()

	if err != nil {
		t.Fatal(err)
	}
	if f, i := s.Inventory.Counts(); f != 41 || i != 40 {
		t.Errorf("counts = %d, %d; want 41 and 40", f, i)
	}
}
