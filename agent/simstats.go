package agent

// How the region is doing, as its simulator says every two seconds.
//
// A SimStats carries a list of numbered statistics -- time dilation,
// frame rate, script time, how many agents and objects -- and, beside
// them, the region's flags and its object capacity.  The statistics are
// kept for the last statsKept, so that a client asking later can be
// told how they have gone and not only where they are; the flags and
// capacity go into Region, so that an estate change made while the
// avatar is there is seen without a second handshake, as the viewer
// sees it (process_sim_stats, llviewermessage.cpp).
// Why: doc/simstats.md

import (
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// statsKept is how far back SimStats keeps what the simulator said.
const statsKept = time.Minute

// statsMax bounds the samples kept whatever their age: thirty arrive
// in a statsKept, and a simulator sending faster is kept to this.
const statsMax = 256

// Stat is one statistic: its number, from the viewer's ESimStatID
// (llviewerstats.h), and its value.
type Stat struct {
	ID    uint32
	Value float32
}

// StatSample is one SimStats: when it arrived, and the statistics in
// the order it listed them.
type StatSample struct {
	At    time.Time
	Stats []Stat
}

// simStats is the history, of the one region whose handle it holds.
type simStats struct {
	mu      sync.Mutex
	handle  uint64
	samples []StatSample // oldest first
}

// SimStats returns what the simulator has said about itself over the
// last statsKept, oldest first, and the handle of the region it said
// it of.  It is empty before the first SimStats and after a move, until
// the new region's first.
func (a *Agent) SimStats() (uint64, []StatSample) {
	return a.simStatsAt(time.Now())
}

func (a *Agent) simStatsAt(now time.Time) (uint64, []StatSample) {
	here := a.RegionHandle()
	s := &a.stats
	s.mu.Lock()
	defer s.mu.Unlock()
	if here != 0 && s.handle != here {
		return here, nil
	}
	s.prune(now)
	return s.handle, append([]StatSample(nil), s.samples...)
}

// prune drops what is older than statsKept.  The caller holds mu.
func (s *simStats) prune(now time.Time) {
	i := 0
	for i < len(s.samples) && now.Sub(s.samples[i].At) > statsKept {
		i++
	}
	if len(s.samples)-i > statsMax {
		i = len(s.samples) - statsMax
	}
	if i > 0 {
		s.samples = append(s.samples[:0], s.samples[i:]...)
	}
}

// record keeps a sample of the region at handle, and starts the
// history again if the last one was of another region.
func (s *simStats) record(handle uint64, sample StatSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle != handle {
		s.handle, s.samples = handle, nil
	}
	s.samples = append(s.samples, sample)
	s.prune(sample.At)
}

// keepSimStats registers the handler that keeps the history and the
// region's flags.
//
// Only the root circuit's SimStats come here, and one naming another
// region than the avatar's is dropped: the region being left, still
// sending as a move completes.  The handle is zero only at login,
// before the first movement, and there is only one region to be of.
func (a *Agent) keepSimStats() {
	a.Disp.MustHandle("SimStats", func(p *msg.Packet) {
		m := p.Message.(*msg.SimStats)
		handle := msg.RegionHandle(m.Region.RegionX, m.Region.RegionY)

		a.mu.Lock()
		if a.here.Handle != 0 && a.here.Handle != handle {
			a.mu.Unlock()
			return
		}
		a.here.Flags = m.Region.RegionFlags
		if len(m.RegionInfo) > 0 {
			a.here.Extended = m.RegionInfo[0].RegionFlagsExtended
		}
		a.here.ObjectCapacity = m.Region.ObjectCapacity
		a.mu.Unlock()

		stats := make([]Stat, len(m.Stat))
		for i, st := range m.Stat {
			stats[i] = Stat{ID: st.StatID, Value: st.StatValue}
		}
		at := p.At
		if at.IsZero() {
			at = time.Now()
		}
		a.stats.record(handle, StatSample{At: at, Stats: stats})
	}, msg.Inline())
}
