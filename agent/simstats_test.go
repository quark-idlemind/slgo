package agent

import (
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	statsRegion = msg.MustParseUUID("56c77e57-7e57-c0de-ab71-edd2cad12589")
	statsHere   = msg.RegionHandle(0xAA80, 0xAA80)
)

// simStatsMsg is a SimStats as a simulator at (x, y) sends one.
func simStatsMsg(x, y, flags, capacity uint32, stats ...Stat) *msg.SimStats {
	m := &msg.SimStats{}
	m.Region.RegionX, m.Region.RegionY = x, y
	m.Region.RegionFlags = flags
	m.Region.ObjectCapacity = capacity
	for _, st := range stats {
		m.Stat = append(m.Stat, msg.SimStats_Stat{StatID: st.ID, StatValue: st.Value})
	}
	m.RegionInfo = []msg.SimStats_RegionInfo{{RegionFlagsExtended: uint64(flags)}}
	return m
}

// inStatsRegion puts the session in the region the tests' SimStats
// are of.
func inStatsRegion(a *Agent) {
	a.mu.Lock()
	a.publishArrival(Region{ID: statsRegion, Handle: statsHere, Name: "Testville"}, true, nil)
	a.mu.Unlock()
}

func TestSimStatsAreKeptAndTheFlagsFollowThem(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	inStatsRegion(a)

	if _, got := a.SimStats(); len(got) != 0 {
		t.Fatalf("%d samples before any SimStats", len(got))
	}

	feed(t, a, simStatsMsg(0xAA80, 0xAA80, RegionSkipScripts, 15000,
		Stat{0, 0.98}, Stat{1, 44.5}, Stat{13, 3}))

	r, _ := a.Region()
	if r.Flags != RegionSkipScripts || r.Extended != RegionSkipScripts {
		t.Errorf("flags %#x, extended %#x; want both %#x", r.Flags, r.Extended, RegionSkipScripts)
	}
	if r.ObjectCapacity != 15000 {
		t.Errorf("object capacity %d, want 15000", r.ObjectCapacity)
	}

	handle, got := a.SimStats()
	if handle != statsHere {
		t.Errorf("stats of %s, want %s", gridSquare(handle), gridSquare(statsHere))
	}
	if len(got) != 1 {
		t.Fatalf("%d samples, want 1", len(got))
	}
	want := []Stat{{0, 0.98}, {1, 44.5}, {13, 3}}
	if len(got[0].Stats) != len(want) {
		t.Fatalf("stats %v, want %v", got[0].Stats, want)
	}
	for i := range want {
		if got[0].Stats[i] != want[i] {
			t.Errorf("stat %d is %v, want %v", i, got[0].Stats[i], want[i])
		}
	}

	// The estate manager lets scripts run again.
	feed(t, a, simStatsMsg(0xAA80, 0xAA80, 0, 15000, Stat{0, 1}))
	if r, _ := a.Region(); r.Flags != 0 || r.Extended != 0 {
		t.Errorf("flags %#x, extended %#x after they were cleared", r.Flags, r.Extended)
	}
	if _, got := a.SimStats(); len(got) != 2 {
		t.Errorf("%d samples, want 2", len(got))
	}
}

// TestASimStatsOfAnotherRegionIsDropped: the region just left can still
// be heard from while a move completes, and what it says of itself is
// not true of where the avatar is.
func TestASimStatsOfAnotherRegionIsDropped(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	inStatsRegion(a)

	feed(t, a, simStatsMsg(0xAA80, 0xAA80, 0, 15000, Stat{0, 1}))
	feed(t, a, simStatsMsg(0xAA7F, 0xAA80, RegionSkipScripts, 20000, Stat{0, 0.5}))

	if r, _ := a.Region(); r.Flags != 0 || r.ObjectCapacity != 15000 {
		t.Errorf("flags %#x, capacity %d: taken from the region to the west", r.Flags, r.ObjectCapacity)
	}
	_, got := a.SimStats()
	if len(got) != 1 || got[0].Stats[0].Value != 1 {
		t.Errorf("samples %v, want only this region's one", got)
	}
}

func TestSimStatsKeepAMinute(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	inStatsRegion(a)

	start := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for i := 0; i <= 45; i++ {
		a.stats.record(statsHere, StatSample{
			At:    start.Add(time.Duration(i) * 2 * time.Second),
			Stats: []Stat{{0, float32(i)}},
		})
	}
	now := start.Add(90 * time.Second)
	_, got := a.simStatsAt(now)
	// 30 s to 90 s, both ends, at two seconds apart.
	if len(got) != 31 {
		t.Fatalf("%d samples, want 31", len(got))
	}
	if first := got[0].At; now.Sub(first) != statsKept {
		t.Errorf("the oldest is %v old, want %v", now.Sub(first), statsKept)
	}

	// Asked later with nothing new, less is left.
	if _, got := a.simStatsAt(now.Add(30 * time.Second)); len(got) != 16 {
		t.Errorf("%d samples half a minute later, want 16", len(got))
	}
}

// TestSimStatsStartAgainInANewRegion: a minute of one region's numbers
// is no guide to the next's.
func TestSimStatsStartAgainInANewRegion(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	inStatsRegion(a)
	feed(t, a, simStatsMsg(0xAA80, 0xAA80, 0, 15000, Stat{0, 1}))

	// Arrived to the east, and nothing from there yet.
	east := msg.RegionHandle(0xAA81, 0xAA80)
	a.mu.Lock()
	a.publishArrival(Region{ID: msg.MustParseUUID("56f47e57-7e57-c0de-449e-4f975b3c4ae8"), Handle: east}, true, nil)
	a.mu.Unlock()
	if handle, got := a.SimStats(); handle != east || len(got) != 0 {
		t.Errorf("%d samples of %s in a region that has sent none", len(got), gridSquare(handle))
	}

	feed(t, a, simStatsMsg(0xAA81, 0xAA80, 0, 30000, Stat{0, 0.7}))
	if _, got := a.SimStats(); len(got) != 1 || got[0].Stats[0].Value != 0.7 {
		t.Errorf("samples %v, want the new region's one alone", got)
	}
	if r, _ := a.Region(); r.ObjectCapacity != 30000 {
		t.Errorf("capacity %d, want the new region's 30000", r.ObjectCapacity)
	}
}

// TestAHandshakeAgainKeepsTheCapacity: the handshake does not carry
// it, and was measured arriving twice.
func TestAHandshakeAgainKeepsTheCapacity(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	inStatsRegion(a)
	feed(t, a, simStatsMsg(0xAA80, 0xAA80, 0, 15000))

	inStatsRegion(a)
	if r, _ := a.Region(); r.ObjectCapacity != 15000 {
		t.Errorf("capacity %d after the same region's handshake again, want 15000", r.ObjectCapacity)
	}
}
