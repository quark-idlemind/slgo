package agent

import (
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// regionInfoMsg is a RegionInfo as a region sends one, with invented
// numbers.
func regionInfoMsg(name string, max8 uint8, max32 uint32) *msg.RegionInfo {
	m := &msg.RegionInfo{}
	m.RegionInfo.SimName = []byte(name + "\x00")
	m.RegionInfo.EstateID = 101
	m.RegionInfo.ParentEstateID = 1
	m.RegionInfo.RegionFlags = 0x20
	m.RegionInfo.SimAccess = 13
	m.RegionInfo.MaxAgents = max8
	m.RegionInfo.ObjectBonusFactor = 1.5
	m.RegionInfo.WaterHeight = 21.5
	m.RegionInfo.TerrainRaiseLimit = 80
	m.RegionInfo.TerrainLowerLimit = -40
	m.RegionInfo2.ProductSKU = []byte("011\x00")
	m.RegionInfo2.ProductName = []byte("Example Product\x00")
	m.RegionInfo2.MaxAgents32 = max32
	m.RegionInfo2.HardMaxAgents = 120
	m.RegionInfo2.HardMaxObjects = 22000
	m.RegionInfo3 = []msg.RegionInfo_RegionInfo3{{RegionFlagsExtended: 0x1_0000_0020}}
	m.RegionInfo5 = []msg.RegionInfo_RegionInfo5{{
		ChatWhisperRange: 10, ChatNormalRange: 20, ChatShoutRange: 100,
		ChatWhisperOffset: 1, ChatNormalOffset: 2, ChatShoutOffset: 3, ChatFlags: 4,
	}}
	return m
}

func TestARegionInfoIsDecodedAndKept(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	inStatsRegion(a)

	if d, n := a.RegionInfo(); d != nil || n != 0 {
		t.Fatalf("%v, %d heard, before any RegionInfo", d, n)
	}
	feed(t, a, regionInfoMsg("Testville", 40, 60))

	d, n := a.RegionInfo()
	if d == nil || n != 1 || d.Seq != 1 || d.Handle != statsHere || d.At.IsZero() {
		t.Fatalf("kept %+v after %d, want one of Testville's", d, n)
	}
	if d.Name != "Testville" || d.EstateID != 101 || d.ParentEstateID != 1 ||
		d.Flags != 0x20 || d.Extended != 0x1_0000_0020 || d.Access != 13 {
		t.Errorf("identity fields %+v", d)
	}
	if d.MaxAgents != 40 || d.MaxAgents32 != 60 || d.HardMaxAgents != 120 || d.HardMaxObjects != 22000 {
		t.Errorf("limits %+v", d)
	}
	if d.ObjectBonus != 1.5 || d.WaterHeight != 21.5 || d.TerrainRaiseLimit != 80 || d.TerrainLowerLimit != -40 {
		t.Errorf("terrain fields %+v", d)
	}
	if d.ProductSKU != "011" || d.ProductName != "Example Product" {
		t.Errorf("product %q %q", d.ProductSKU, d.ProductName)
	}
	if c := d.Chat; c == nil || c.Whisper != 10 || c.Normal != 20 || c.Shout != 100 || c.ShoutOffset != 3 || c.Flags != 4 {
		t.Errorf("chat %+v", c)
	}
}

// TestARegionInfoWithoutTheOptionalBlocks: the extended flags are the
// plain ones widened and there is no chat, as the viewer reads one
// (llregioninfomodel.cpp).
func TestARegionInfoWithoutTheOptionalBlocks(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	inStatsRegion(a)
	m := regionInfoMsg("Testville", 40, 0)
	m.RegionInfo3, m.RegionInfo5 = nil, nil
	feed(t, a, m)

	d, _ := a.RegionInfo()
	if d == nil || d.Extended != 0x20 || d.Chat != nil || d.MaxAgents32 != 0 || d.MaxAgents != 40 {
		t.Errorf("kept %+v", d)
	}
}

// TestEveryRegionInfoIsKeptTheLastCurrent: asked for or not, and the
// count rises with each.
func TestEveryRegionInfoIsKeptTheLastCurrent(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	inStatsRegion(a)
	feed(t, a, regionInfoMsg("Testville", 40, 0), regionInfoMsg("Testville", 50, 0))
	d, n := a.RegionInfo()
	if d == nil || n != 2 || d.Seq != 2 || d.MaxAgents != 50 {
		t.Errorf("kept %+v after %d, want the second of two", d, n)
	}
}

// TestARegionInfoIsNotCurrentInAnotherRegion: it was the old region's.
func TestARegionInfoIsNotCurrentInAnotherRegion(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	inStatsRegion(a)
	feed(t, a, regionInfoMsg("Testville", 40, 0))

	east := msg.RegionHandle(0xAA81, 0xAA80)
	a.mu.Lock()
	a.publishArrival(Region{ID: msg.MustParseUUID("56f47e57-7e57-c0de-449e-4f975b3c4ae8"), Handle: east}, true, nil)
	a.mu.Unlock()
	if d, n := a.RegionInfo(); d != nil || n != 1 {
		t.Errorf("%+v after %d in a region that has sent none", d, n)
	}

	feed(t, a, regionInfoMsg("Testville East", 30, 0))
	if d, _ := a.RegionInfo(); d == nil || d.Handle != east || d.Name != "Testville East" {
		t.Errorf("kept %+v, want the new region's", d)
	}
}
