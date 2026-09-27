package agent

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// TestLiveLogin logs in to a real grid, stays a moment, and logs out.
//
// It runs against the profile named by SLGO_PROFILE and is skipped
// without one, so no credential ever appears on a command line or in
// the test source:
//
//	SLGO_PROFILE=example go test ./agent -run TestLiveLogin -v
func TestLiveLogin(t *testing.T) {
	profile := os.Getenv("SLGO_PROFILE")
	if profile == "" {
		t.Skip("set SLGO_PROFILE to the name of a profile under ~/.config/slgo")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	login, err := LoadProfile(profile)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if login.Channel == "" {
		login.Channel = "slgo"
	}
	if login.Version == "" {
		login.Version = "slgo 0.1"
	}

	acct, err := login.Do(ctx)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	t.Logf("logged in as %s (%s)", acct.Name(), acct.AgentID)
	t.Logf("simulator %s, circuit %d", acct.SimAddr(), acct.CircuitCode)
	t.Logf("message of the day: %s", acct.Message)

	unhandled := map[string]int{}
	s, err := Connect(ctx, acct, Options{
		Timeout: 45 * time.Second,
		OnUnhandled: func(p *msg.Packet) {
			unhandled[p.ID.String()]++
		},
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	if err := s.WaitForRegionHandshake(ctx, 30*time.Second); err != nil {
		t.Errorf("region handshake: %v", err)
	}
	t.Logf("in region %q at %+v", s.RegionName(), s.Position())
	t.Logf("simulator build %q", s.ChannelVersion())
	t.Logf("capabilities: %d offered", len(s.Caps()))
	if _, ok := s.Caps().Get("InventoryAPIv3"); !ok {
		t.Errorf("no InventoryAPIv3 among %v", s.Caps().Names())
	}

	// Fetch the whole inventory tree over AIS.
	invStart := time.Now()
	if err := s.FetchInventory(ctx, FetchOptions{Concurrency: 8}); err != nil {
		t.Errorf("inventory: %v", err)
	}
	folders, items := s.Inventory.Counts()
	t.Logf("inventory: %d folders, %d items in %s", folders, items, time.Since(invStart).Round(time.Millisecond))
	if folders == 0 {
		t.Error("no inventory")
	}

	// Show the top of the tree, as ls would.
	for _, f := range s.Inventory.Children(s.Inventory.Root()) {
		kids := s.Inventory.Children(f.ID)
		its := s.Inventory.Contents(f.ID)
		t.Logf("  %-44s %2d folders %3d items", f.Name, len(kids), len(its))
	}

	// Watch a few seconds of traffic.
	chat := 0
	_ = s.Handle("ChatFromSimulator", func(p *msg.Packet) { chat++ })
	select {
	case <-time.After(15 * time.Second):
	case <-s.Done():
		t.Fatalf("session ended early: %v", s.Err())
	}

	rs := s.Recv.Stats()
	ss := s.Send.Stats()
	ds := s.Disp.Stats()
	t.Logf("in:  %d packets, %d bytes, %d undecodable, %d unknown",
		rs.Packets, rs.Bytes, rs.Failed, rs.Unknown)
	t.Logf("out: %d packets, %d bytes, %d resent, %d abandoned",
		ss.Sent, ss.Bytes, ss.Resent, ss.Abandoned)
	t.Logf("acks: %d carried on other packets, %d alone in %d PacketAck",
		ss.AcksCarried, ss.AcksAlone, ss.AckPackets)
	t.Logf("dispatch: %d handled, %d duplicates, %d unhandled",
		ds.Dispatched, ds.Duplicates, ds.Unhandled)
	for name, n := range unhandled {
		t.Logf("  no handler for %s (%d)", name, n)
	}

	if rs.Packets == 0 {
		t.Error("nothing arrived from the simulator")
	}
	if rs.Failed > 0 {
		t.Errorf("%d packets would not decode", rs.Failed)
	}

	if err := s.Logout(ctx, 20*time.Second); err != nil {
		t.Errorf("logout: %v", err)
	}
	t.Log("logged out")
}
