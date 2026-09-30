package sl

// How the region is doing, as its simulator says every two seconds:
// time dilation, frame rate, where the frame's time goes, and how many
// agents, objects and scripts it is carrying.  The session keeps the
// last minute of it, so that an average or a graph can be drawn without
// having been listening.
// Why: doc/simstats.md

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// StatID is one of the statistics a simulator reports, numbered as the
// viewer numbers them (ESimStatID, llviewerstats.h).
type StatID uint32

// The statistics.  Times are milliseconds of a simulator frame, rates
// are per second, and the rest are counts unless the name says
// otherwise.  A simulator does not send every one: 16, 21 to 23, 36 and
// 37 were not in the reports measured.
const (
	StatTimeDilation        StatID = 0  // simulated seconds per real one, 1 at best
	StatFPS                 StatID = 1  // simulator frames a second
	StatPhysicsFPS          StatID = 2  // physics frames a second
	StatAgentUpdates        StatID = 3  // agent updates a second
	StatFrameTime           StatID = 4  // ms: the whole frame
	StatNetTime             StatID = 5  // ms
	StatSimulationTime      StatID = 6  // ms: the viewer's "Simulation Time"
	StatPhysicsTime         StatID = 7  // ms
	StatAgentTime           StatID = 8  // ms
	StatImagesTime          StatID = 9  // ms
	StatScriptTime          StatID = 10 // ms
	StatObjects             StatID = 11 // objects in the region
	StatActiveObjects       StatID = 12 // scripted or moving ones
	StatAgents              StatID = 13 // avatars in the region
	StatChildAgents         StatID = 14 // avatars next door who can see in
	StatActiveScripts       StatID = 15 // scripts that are running
	StatLSLIPS              StatID = 16 // LSL instructions a second
	StatPacketsIn           StatID = 17 // a second
	StatPacketsOut          StatID = 18 // a second
	StatPendingDownloads    StatID = 19
	StatPendingUploads      StatID = 20
	StatVirtualSize         StatID = 21 // KB
	StatResidentSize        StatID = 22 // KB
	StatPendingLocalUploads StatID = 23
	StatUnackedData         StatID = 24 // KB, as the viewer reads it
	StatPinnedObjects       StatID = 25
	StatLowLODObjects       StatID = 26
	StatPhysicsStep         StatID = 27 // ms
	StatPhysicsShapes       StatID = 28 // ms
	StatPhysicsOther        StatID = 29 // ms
	StatPhysicsMemory       StatID = 30 // MB
	StatScriptEvents        StatID = 31 // a second
	StatSpareTime           StatID = 32 // ms left over in the frame
	StatSleepTime           StatID = 33 // ms
	StatPumpIO              StatID = 34 // ms
	StatScriptsRun          StatID = 35 // % of scripts that ran this frame
	StatRegionIdle          StatID = 36
	StatRegionIdlePossible  StatID = 37
	StatPathfinding         StatID = 38 // ms
	StatSkippedSilhouettes  StatID = 39 // a second
	StatCharactersUpdated   StatID = 40 // %
)

// statNames are the names String gives and StatNamed reads, which are
// what slsh takes.  A unit the value is in closes the name.
var statNames = map[StatID]string{
	StatTimeDilation:        "dilation",
	StatFPS:                 "fps",
	StatPhysicsFPS:          "physics-fps",
	StatAgentUpdates:        "agent-updates",
	StatFrameTime:           "frame-ms",
	StatNetTime:             "net-ms",
	StatSimulationTime:      "sim-ms",
	StatPhysicsTime:         "physics-ms",
	StatAgentTime:           "agent-ms",
	StatImagesTime:          "images-ms",
	StatScriptTime:          "script-ms",
	StatObjects:             "objects",
	StatActiveObjects:       "active-objects",
	StatAgents:              "agents",
	StatChildAgents:         "child-agents",
	StatActiveScripts:       "active-scripts",
	StatLSLIPS:              "lsl-ips",
	StatPacketsIn:           "packets-in",
	StatPacketsOut:          "packets-out",
	StatPendingDownloads:    "pending-downloads",
	StatPendingUploads:      "pending-uploads",
	StatVirtualSize:         "virtual-kb",
	StatResidentSize:        "resident-kb",
	StatPendingLocalUploads: "pending-local-uploads",
	StatUnackedData:         "unacked-kb",
	StatPinnedObjects:       "pinned-objects",
	StatLowLODObjects:       "low-lod-objects",
	StatPhysicsStep:         "physics-step-ms",
	StatPhysicsShapes:       "physics-shapes-ms",
	StatPhysicsOther:        "physics-other-ms",
	StatPhysicsMemory:       "physics-mb",
	StatScriptEvents:        "script-events",
	StatSpareTime:           "spare-ms",
	StatSleepTime:           "sleep-ms",
	StatPumpIO:              "pump-io-ms",
	StatScriptsRun:          "scripts-run-pct",
	StatRegionIdle:          "region-idle",
	StatRegionIdlePossible:  "region-idle-possible",
	StatPathfinding:         "pathfinding-ms",
	StatSkippedSilhouettes:  "skipped-silhouettes",
	StatCharactersUpdated:   "characters-updated-pct",
}

// String is the statistic's name, or its number for one this build does
// not know.
func (id StatID) String() string {
	if n, ok := statNames[id]; ok {
		return n
	}
	return fmt.Sprintf("stat-%d", uint32(id))
}

// StatNamed is the statistic a name from String means.
func StatNamed(name string) (StatID, bool) {
	for id, n := range statNames {
		if n == name {
			return id, true
		}
	}
	var n uint32
	if _, err := fmt.Sscanf(name, "stat-%d", &n); err == nil && fmt.Sprintf("stat-%d", n) == name {
		return StatID(n), true
	}
	return 0, false
}

// StatNames lists every name StatNamed knows, in the order of the ids.
func StatNames() []string {
	ids := make([]StatID, 0, len(statNames))
	for id := range statNames {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = statNames[id]
	}
	return out
}

// SimStats is what the simulator has said about how it is doing over
// the last minute: a sample every two seconds, oldest first.
type SimStats struct {
	// Handle is the region the samples are of.
	Handle uint64

	// Read is when this was asked for, and where the windows Average
	// takes end.
	Read time.Time

	Samples []StatSample
}

// StatSample is one report: when it arrived and what it said.
type StatSample struct {
	At     time.Time
	Values map[StatID]float64
}

// StatPoint is one statistic in one report.
type StatPoint struct {
	At    time.Time
	Value float64
}

// IDs lists every statistic any sample has, in the order of the ids.
func (s *SimStats) IDs() []StatID {
	seen := map[StatID]bool{}
	var ids []StatID
	for _, sm := range s.Samples {
		for id := range sm.Values {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Latest is the newest value of id, and whether any sample has one.
func (s *SimStats) Latest(id StatID) (float64, bool) {
	for i := len(s.Samples) - 1; i >= 0; i-- {
		if v, ok := s.Samples[i].Values[id]; ok {
			return v, true
		}
	}
	return 0, false
}

// Average is the mean of id over the samples that arrived in the last
// over before Read, and how many there were.  At a sample every two
// seconds a five second window holds two or three.  With none, n is
// zero and so is the mean.
func (s *SimStats) Average(id StatID, over time.Duration) (mean float64, n int) {
	from := s.Read.Add(-over)
	var sum float64
	for _, sm := range s.Samples {
		if sm.At.Before(from) {
			continue
		}
		if v, ok := sm.Values[id]; ok {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return sum / float64(n), n
}

// Series is every value of id there is, oldest first.
func (s *SimStats) Series(id StatID) []StatPoint {
	var out []StatPoint
	for _, sm := range s.Samples {
		if v, ok := sm.Values[id]; ok {
			out = append(out, StatPoint{At: sm.At, Value: v})
		}
	}
	return out
}

// SimStats reads what the simulator has said about how it is doing over
// the last minute.  It is kept by whoever holds the circuit, so it is
// all there however recently this session attached; it is empty for a
// moment after arriving in a region, until the region's first report.
func (w *Session) SimStats(ctx context.Context) (*SimStats, error) {
	return w.b.SimStats(ctx)
}
