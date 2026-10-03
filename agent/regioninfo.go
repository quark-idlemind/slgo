package agent

// What a region says about itself when asked: RegionInfo.
//
// The handshake says a region's name, flags and product once; RegionInfo
// adds its estate, agent and object limits, terraform limits, object
// bonus and chat ranges, and it comes whenever somebody asks with
// RequestRegionInfo -- anybody, not only an estate manager.  The viewer
// takes every one that arrives, asked for or not, and so does this.
// Why: doc/simstats.md#what-a-region-says-about-itself-regioninfo

import (
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// ChatRanges is the RegionInfo5 block: how far chat carries here.
type ChatRanges struct {
	Whisper, Normal, Shout                   float32
	WhisperOffset, NormalOffset, ShoutOffset float32
	Flags                                    uint32
}

// RegionDetails is the last RegionInfo a region sent, decoded.
type RegionDetails struct {
	// Handle is the region the avatar was in when it arrived, and At
	// when.  Seq counts the RegionInfo this session has heard, from 1,
	// so that a caller can tell one heard after its request from one
	// heard before.
	Handle uint64
	At     time.Time
	Seq    uint64

	Name           string
	EstateID       uint32
	ParentEstateID uint32

	// Flags are RegionFlags; Extended is RegionFlagsExtended from the
	// RegionInfo3 block, or Flags widened when the message has none,
	// as the viewer reads it.
	Flags    uint32
	Extended uint64

	// Access is SimAccess, the maturity rating.
	Access uint8

	// MaxAgents is the U8 the viewer reads, and MaxAgents32 the wider
	// one from RegionInfo2, zero when the message does not set it.
	MaxAgents      uint8
	MaxAgents32    uint32
	HardMaxAgents  uint32
	HardMaxObjects uint32

	ObjectBonus       float32
	BillableFactor    float32
	WaterHeight       float32
	TerrainRaiseLimit float32
	TerrainLowerLimit float32
	PricePerMeter     int32

	ProductSKU  string
	ProductName string

	// Chat is nil when the message has no RegionInfo5 block.
	Chat *ChatRanges
}

// regionInfoKept is the last RegionInfo, and the region it was heard in.
type regionInfoKept struct {
	mu   sync.Mutex
	seq  uint64
	last *RegionDetails
}

// RegionInfo returns the last RegionInfo heard in the region the avatar
// is in now, or nil if there is none: none yet, or the last was heard in
// another region.  The second value counts every one heard, whatever
// region, so a caller can wait for one newer than a count it took.
func (a *Agent) RegionInfo() (*RegionDetails, uint64) {
	here := a.RegionHandle()
	k := &a.info
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.last == nil || (here != 0 && k.last.Handle != here) {
		return nil, k.seq
	}
	d := *k.last
	if d.Chat != nil {
		c := *d.Chat
		d.Chat = &c
	}
	return &d, k.seq
}

// keepRegionInfo registers the handler that keeps the last RegionInfo.
// Only the root circuit's come here, and the message does not name its
// region, so it is kept as the region the avatar is in.
func (a *Agent) keepRegionInfo() {
	a.Disp.MustHandle("RegionInfo", func(p *msg.Packet) {
		m := p.Message.(*msg.RegionInfo)
		d := &RegionDetails{
			At:             p.At,
			Name:           trimNul(m.RegionInfo.SimName),
			EstateID:       m.RegionInfo.EstateID,
			ParentEstateID: m.RegionInfo.ParentEstateID,
			Flags:          m.RegionInfo.RegionFlags,
			Extended:       uint64(m.RegionInfo.RegionFlags),
			Access:         m.RegionInfo.SimAccess,
			MaxAgents:      m.RegionInfo.MaxAgents,

			MaxAgents32:    m.RegionInfo2.MaxAgents32,
			HardMaxAgents:  m.RegionInfo2.HardMaxAgents,
			HardMaxObjects: m.RegionInfo2.HardMaxObjects,
			ProductSKU:     trimNul(m.RegionInfo2.ProductSKU),
			ProductName:    trimNul(m.RegionInfo2.ProductName),

			ObjectBonus:       m.RegionInfo.ObjectBonusFactor,
			BillableFactor:    m.RegionInfo.BillableFactor,
			WaterHeight:       m.RegionInfo.WaterHeight,
			TerrainRaiseLimit: m.RegionInfo.TerrainRaiseLimit,
			TerrainLowerLimit: m.RegionInfo.TerrainLowerLimit,
			PricePerMeter:     m.RegionInfo.PricePerMeter,
		}
		if d.At.IsZero() {
			d.At = time.Now()
		}
		if len(m.RegionInfo3) > 0 {
			d.Extended = m.RegionInfo3[0].RegionFlagsExtended
		}
		if len(m.RegionInfo5) > 0 {
			c := m.RegionInfo5[0]
			d.Chat = &ChatRanges{
				Whisper: c.ChatWhisperRange, Normal: c.ChatNormalRange, Shout: c.ChatShoutRange,
				WhisperOffset: c.ChatWhisperOffset, NormalOffset: c.ChatNormalOffset, ShoutOffset: c.ChatShoutOffset,
				Flags: c.ChatFlags,
			}
		}
		d.Handle = a.RegionHandle()

		k := &a.info
		k.mu.Lock()
		k.seq++
		d.Seq = k.seq
		k.last = d
		k.mu.Unlock()
	}, msg.Inline())
}
