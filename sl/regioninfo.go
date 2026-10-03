package sl

// What the region says about itself when asked: its estate, agent and
// object limits, terraform limits, object bonus and chat ranges.
// Why: doc/simstats.md#what-a-region-says-about-itself-regioninfo

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// regionInfoEvery is how often the reply is looked for.  The one reply
// measured took 115 ms.
const regionInfoEvery = 50 * time.Millisecond

// RegionDetails is a RegionInfo, as a region described itself.
type RegionDetails struct {
	Name           string
	EstateID       uint32
	ParentEstateID uint32

	// Flags and Extended are the region's flags; Extended is the plain
	// flags widened when the message carried no extended ones.
	Flags    uint32
	Extended uint64

	// Access is the maturity rating; AccessName gives it in words.
	Access uint8

	// AgentLimit is the U8 MaxAgents, as the viewer reads it; it never
	// reads MaxAgents32.
	AgentLimit      uint32
	HardAgentLimit  uint32
	HardObjectLimit uint32

	ObjectBonus       float32
	BillableFactor    float32
	WaterHeight       float32
	TerrainRaiseLimit float32
	TerrainLowerLimit float32
	PricePerMeter     int32

	ProductSKU  string
	ProductName string

	// Chat is nil when the message had no chat block.
	Chat *ChatRanges

	// Heard is when the message arrived.
	Heard time.Time
}

// ChatRanges is how far chat carries in the region, and from where.
type ChatRanges struct {
	Whisper, Normal, Shout                   float32
	WhisperOffset, NormalOffset, ShoutOffset float32
	Flags                                    uint32
}

// RegionDetails asks the region to describe itself, and returns the
// description that comes back.  Anybody may ask.
//
// It returns only a description heard after the question: one already
// held does not count, and on timeout it is ErrTimeout that comes back,
// never the old one.  Options.RegionInfoTimeout bounds the wait.
func (w *Session) RegionDetails(ctx context.Context) (*RegionDetails, error) {
	_, before, err := w.b.LastRegionDetails(ctx)
	if err != nil {
		return nil, err
	}
	m := &msg.RequestRegionInfo{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}
	var got *RegionDetails
	err = poll(ctx, w.regionInfoWait(), regionInfoEvery, "the region to describe itself", func(ctx context.Context) (bool, error) {
		d, n, err := w.b.LastRegionDetails(ctx)
		if err != nil || d == nil || n <= before {
			return false, err
		}
		got = d
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("sl: RegionDetails: %w", err)
	}
	return got, nil
}
