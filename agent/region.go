package agent

import (
	"sync"

	"github.com/quark-idlemind/slgo/msg"
)

// The region introduces itself once, in RegionHandshake, and never
// mentions any of it again.
//
// So this is decoded and kept for the same reason the objects are: it
// arrives before any client is listening and cannot be asked for a
// second time.  It is also the thing that says when the region has
// changed, which is what makes the object cache wrong.

// Region is what the simulator said about itself.
type Region struct {
	// ID identifies the region across the grid; Handle locates it and
	// is what a teleport within the grid is addressed to.  Name is
	// what people call it.
	ID     msg.UUID
	Handle uint64
	Name   string

	// Flags carry what may be done here -- terraforming, damage, fly,
	// and so on.  Extended holds the ones that outgrew a U32.
	Flags    uint32
	Extended uint64

	// Access is the maturity rating, and Owner the estate owner.
	Access uint8
	Owner  msg.UUID

	// EstateManager says whether this avatar can administer the
	// estate, which decides whether half the region messages will be
	// accepted from us at all.
	EstateManager bool

	WaterHeight float32

	// Product describes what kind of region this is: a mainland
	// parcel, a private island, a homestead.
	ProductName string
	ProductSKU  string
	ColoName    string

	// CPUClass and CPURatio say how the region is hosted; a ratio
	// above one means it shares a core with other regions.
	CPUClass int32
	CPURatio int32

	// Protocols is what the simulator says it can speak.
	Protocols uint64
}

// regionState is the current region and a way to notice it changing.
type regionState struct {
	mu      sync.RWMutex
	current Region
	known   bool

	// handshake is the message as it arrived, kept whole.
	//
	// Region above is what this package needs and is not what a
	// viewer needs: regionFromHandshake keeps twelve fields of a
	// message that has thirty odd, and among the ones it drops are
	// the eight terrain texture ids and the eight height and range
	// floats -- precisely what decides whether the ground has
	// anything on it.  A region says this once, when the avatar
	// arrives, so a viewer handed the session later can only be told
	// what was kept.
	handshake *msg.RegionHandshake
}

// Region returns what the simulator said about itself, and whether the
// handshake has happened yet.
//
// The handle is filled in here rather than at handshake time.
// RegionHandshake does not carry it: it arrives later, with
// AgentMovementComplete, so recording it during the handshake records
// a zero.
func (a *Agent) Region() (Region, bool) {
	a.region.mu.RLock()
	r, known := a.region.current, a.region.known
	a.region.mu.RUnlock()
	r.Handle = a.RegionHandle()
	return r, known
}

// setRegion records a handshake and reports whether this is a
// different region from the last one.
//
// The comparison is on the region id rather than the name: two regions
// can share a name, and a region can be renamed without becoming a
// different place.
// Handshake is the RegionHandshake this region sent, or nil before it
// has arrived.  It is the message itself so that it can be passed on
// entire, rather than rebuilt from the part of it this package models.
func (a *Agent) Handshake() *msg.RegionHandshake {
	a.region.mu.RLock()
	defer a.region.mu.RUnlock()
	return a.region.handshake
}

// setHandshake keeps the message alongside what was decoded from it.
func (a *Agent) setHandshake(m *msg.RegionHandshake) {
	a.region.mu.Lock()
	a.region.handshake = m
	a.region.mu.Unlock()
}

func (a *Agent) setRegion(r Region) (changed bool) {
	a.region.mu.Lock()
	defer a.region.mu.Unlock()
	changed = a.region.known && a.region.current.ID != r.ID
	a.region.current = r
	a.region.known = true
	return changed
}

// first4 returns the RegionInfo4 block, which is variable and which
// an older simulator may not send at all.
func first4(bs []msg.RegionHandshake_RegionInfo4) msg.RegionHandshake_RegionInfo4 {
	if len(bs) == 0 {
		return msg.RegionHandshake_RegionInfo4{}
	}
	return bs[0]
}

// regionFromHandshake pulls the useful parts out of the message.
func regionFromHandshake(m *msg.RegionHandshake) Region {
	return Region{
		ID:            m.RegionInfo2.RegionID,
		Name:          trimNul(m.RegionInfo.SimName),
		Flags:         m.RegionInfo.RegionFlags,
		Extended:      first4(m.RegionInfo4).RegionFlagsExtended,
		Access:        m.RegionInfo.SimAccess,
		Owner:         m.RegionInfo.SimOwner,
		EstateManager: m.RegionInfo.IsEstateManager,
		WaterHeight:   m.RegionInfo.WaterHeight,
		ProductName:   trimNul(m.RegionInfo3.ProductName),
		ProductSKU:    trimNul(m.RegionInfo3.ProductSKU),
		ColoName:      trimNul(m.RegionInfo3.ColoName),
		CPUClass:      m.RegionInfo3.CPUClassID,
		CPURatio:      m.RegionInfo3.CPURatio,
		Protocols:     first4(m.RegionInfo4).RegionProtocols,
	}
}
