package agent

import (
	"sync"

	"github.com/quark-idlemind/slgo/msg"
)

// Land layer types.  L is the original and M is what a variable sized
// region uses; the others on this message are weather.
const (
	layerLand    = 'L'
	layerLandVar = 'M'
)

// TerrainLimit bounds what is kept, in bytes.
//
// A whole region measured on Aditi was 38 packets and 41,905 bytes, so
// this is generous by a factor of six.  It exists because terraforming
// sends more land after the first burst and there is nothing here that
// can tell an edit from a fresh patch -- so without a bound, a session
// left running on a region somebody is landscaping would grow without
// end.
const TerrainLimit = 256 << 10

// Terrain is the land the simulator described, kept as it arrived.
//
// It is kept because it cannot be asked for again.  A region sends its
// heightmap once, in the first seconds after the avatar arrives, and
// answers nothing that asks for it a second time -- a second
// RegionHandshakeReply produced not one further patch in thirty seconds
// of watching.  A viewer handed a session that has been up for hours
// would therefore have no ground under it, which is not a subtle failure
// but the grey void itself.
//
// The bodies are kept as they came, because a viewer wants the
// simulator's own bytes rather than anything this package might make of
// them.  Each is also decoded as it arrives, into the heights HeightAt
// and Highest read: decoded then, because a body dropped for the limit
// is gone, and the land it described with it.
type Terrain struct {
	mu      sync.Mutex
	patches []Patch
	bytes   int
	dropped int

	// heights is the ground at each whole metre, row by row from the
	// south west, allocated with the first land; have says which 16
	// metre patches of it have arrived.
	heights []float32
	have    [groundPatches * groundPatches]bool
}

// Patch is one LayerData body and the layer it belongs to.
//
// The type is kept with the bytes because a variable sized region sends
// its land as M rather than L, and a viewer told the wrong one is being
// handed a patch it will decode as something else.
type Patch struct {
	Type uint8
	Data []byte
}

// note keeps one LayerData body if it is land.
//
// Weather is skipped, and that is not an optimisation: wind is sent for
// as long as the session lives, so recording it would grow without bound
// and would drown the land it is mixed in with.
func (t *Terrain) note(m *msg.LayerData) {
	switch m.LayerID.Type {
	case layerLand, layerLandVar:
	default:
		return
	}
	body := append([]byte(nil), m.LayerData.Data...)

	t.mu.Lock()
	defer t.mu.Unlock()
	t.decode(m.LayerID.Type, body)
	t.patches = append(t.patches, Patch{Type: m.LayerID.Type, Data: body})
	t.bytes += len(body)
	// Oldest first, which is the wrong end to lose and the only end
	// that can be found without decoding.  Reaching this at all means
	// the region is being terraformed hard enough that the first burst
	// is no longer what a viewer should be told.
	for t.bytes > TerrainLimit && len(t.patches) > 1 {
		t.bytes -= len(t.patches[0].Data)
		t.patches = t.patches[1:]
		t.dropped++
	}
}

// Patches returns the land bodies in the order they arrived, which is
// the order to send them in.
func (t *Terrain) Patches() []Patch {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Patch, len(t.patches))
	copy(out, t.patches)
	return out
}

// Stats is how many patches are held, how many bytes they come to, and
// how many were dropped for the limit.
func (t *Terrain) Stats() (patches, bytes, dropped int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.patches), t.bytes, t.dropped
}

// forget drops everything, for a region crossing: the land of the region
// just left describes somewhere else entirely.
func (t *Terrain) forget() {
	t.mu.Lock()
	t.patches, t.bytes, t.dropped = nil, 0, 0
	t.heights, t.have = nil, [groundPatches * groundPatches]bool{}
	t.mu.Unlock()
}

// Terrain is the land of the region this agent is in.
func (a *Agent) Terrain() *Terrain { return &a.terrain }
