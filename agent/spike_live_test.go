package agent

// Spikes for the viewer-frontend plan.  Neither of these is a test in
// the usual sense: they ask the grid a question the protocol documents
// do not answer, and print what it said.  They are kept because the
// answers decide how two later stages are built, and because a
// simulator that changes its mind about either would otherwise break
// the relay silently.
//
//	SLGO_PROFILE=holt-beta go test ./agent -run TestLiveRedescribe -v -timeout 5m
//	SLGO_PROFILE=holt-beta go test ./agent -run TestLiveTerrain -v -timeout 5m

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// landLayer reports whether a LayerData carries ground rather than
// weather.
//
// The distinction matters for anything that waits for terrain to go
// quiet: wind and cloud are streamed for as long as the session lives,
// so a wait for "no more LayerData" never returns.  L is the original
// land layer and M is the one a variable-sized region uses.
func landLayer(t uint8) bool { return t == 'L' || t == 'M' }

func layerName(t uint8) string {
	switch t {
	case 'L':
		return "land"
	case 'M':
		return "land (varregion)"
	case 'W':
		return "water"
	case 'O':
		return "water (varregion)"
	case '7':
		return "wind"
	case 'X':
		return "wind (varregion)"
	case '8':
		return "cloud"
	case 'Z':
		return "cloud (varregion)"
	}
	return "unknown"
}

// counter is what the packet tap writes into.  A tap runs on the
// dispatch goroutine, so everything it touches is behind a mutex and
// nothing it does may block.
type counter struct {
	mu sync.Mutex

	full       int // ObjectUpdate blocks
	compressed int // ObjectUpdateCompressed blocks
	cached     int // ObjectUpdateCached blocks
	cachedMsgs int // ObjectUpdateCached packets

	layers   map[uint8]int
	bytes    map[uint8]int
	lastLand time.Time
}

func newCounter() *counter {
	return &counter{layers: map[uint8]int{}, bytes: map[uint8]int{}}
}

func (c *counter) tap(p *msg.Packet) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch m := p.Message.(type) {
	case *msg.ObjectUpdate:
		c.full += len(m.ObjectData)
	case *msg.ObjectUpdateCompressed:
		c.compressed += len(m.ObjectData)
	case *msg.ObjectUpdateCached:
		c.cached += len(m.ObjectData)
		c.cachedMsgs++
	case *msg.LayerData:
		c.layers[m.LayerID.Type]++
		c.bytes[m.LayerID.Type] += len(m.LayerData.Data)
		if landLayer(m.LayerID.Type) {
			c.lastLand = p.At
		}
	}
}

func (c *counter) objects() (full, compressed, cached, cachedMsgs int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.full, c.compressed, c.cached, c.cachedMsgs
}

func (c *counter) land() (n int, last time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for t, k := range c.layers {
		if landLayer(t) {
			n += k
		}
	}
	return n, c.lastLand
}

func (c *counter) report(t *testing.T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for typ, n := range c.layers {
		t.Logf("  LayerData %q %-18s %3d packets %6d bytes", typ, layerName(typ), n, c.bytes[typ])
	}
}

// dialSpike logs in and gets as far as being in the region, with a tap
// counting what arrives.
func dialSpike(ctx context.Context, t *testing.T, c *counter) *Agent {
	t.Helper()

	profile := os.Getenv("SLGO_PROFILE")
	if profile == "" {
		t.Skip("set SLGO_PROFILE to a profile under ~/.config/slgo (holt-beta)")
	}

	login, err := LoadProfile(profile)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if login.Channel == "" {
		login.Channel = "slgo"
	}
	if login.Version == "" {
		login.Version = "slgo spike"
	}

	acct, err := login.Do(ctx)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	t.Logf("logged in as %s (%s) on %s", acct.Name(), acct.AgentID, acct.SimAddr())

	a, err := Connect(ctx, acct, Options{
		Timeout: 45 * time.Second,
		Tap:     c.tap,
		// The default draw distance is what slgod itself uses, so
		// the store this fills is the store a viewer would inherit.
		DrawDistance: DefaultDrawDistance,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Logf("in region %q at %+v", a.RegionName(), a.Position())
	return a
}

// TestLiveRedescribe asks whether RequestMultipleObjects will describe
// objects the simulator believes the agent already holds.
//
// This is the question the whole viewer-frontend design rests on.  A
// region describes each object exactly once, so a viewer attached to a
// session that has already been running sees nothing -- unless the
// objects can simply be asked for again.  The existing use of this
// message (requestCachedObjects) only ever asks for objects the
// simulator has just said we are missing, which is a different question
// and does not answer this one.
//
// The method: let the store fill, take the local ids, forget everything,
// ask for those ids, and see what comes back.
func TestLiveRedescribe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	c := newCounter()
	a := dialSpike(ctx, t, c)
	defer func() {
		if err := a.Logout(ctx, 20*time.Second); err != nil {
			t.Logf("logout: %v", err)
		}
	}()

	// Let the region describe itself.  It arrives in a burst and then
	// tails off, so this waits for the store to stop growing rather
	// than for a fixed time.
	settle := time.Now()
	prev := -1
	for {
		select {
		case <-time.After(5 * time.Second):
		case <-a.Done():
			t.Fatalf("session ended early: %v", a.Err())
		}
		n := a.Objects().Count()
		t.Logf("  settling: %d objects", n)
		if n == prev && n > 0 {
			break
		}
		if time.Since(settle) > 90*time.Second {
			t.Logf("  settling: giving up waiting for quiet at %d", n)
			break
		}
		prev = n
	}

	before := a.Objects().All()
	if len(before) == 0 {
		t.Fatal("no objects to ask about; is the region empty?")
	}
	locals := make([]uint32, 0, len(before))
	for _, o := range before {
		locals = append(locals, o.Local)
	}
	want := make(map[uint32]bool, len(locals))
	for _, l := range locals {
		want[l] = true
	}
	t.Logf("store settled at %d objects in %s", len(before), time.Since(settle).Round(time.Second))

	// The baseline for the confounder: if the simulator sends
	// ObjectUpdateCached during the measurement, the registered
	// handler asks for those on its own and the refill is not
	// attributable to this test's request.
	_, _, _, cachedBefore := c.objects()
	fullBefore, compBefore, _, _ := c.objects()

	n := a.Objects().Flush()
	t.Logf("flushed %d objects; store now %d", n, a.Objects().Count())

	// The question, asked.
	a.requestCachedObjects(locals)
	asked := time.Now()

	// Watch it refill.
	deadline := time.After(30 * time.Second)
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	settledAt := 0
watch:
	for {
		select {
		case <-tick.C:
			got := a.Objects().Count()
			t.Logf("  +%4s: %d objects", time.Since(asked).Round(time.Second), got)
			if got >= len(before) {
				settledAt = got
				break watch
			}
		case <-deadline:
			settledAt = a.Objects().Count()
			break watch
		case <-a.Done():
			t.Fatalf("session ended early: %v", a.Err())
		}
	}

	after := a.Objects().All()
	matched := 0
	for _, o := range after {
		if want[o.Local] {
			matched++
		}
	}

	fullAfter, compAfter, _, cachedAfter := c.objects()
	t.Logf("")
	t.Logf("asked for            %d local ids", len(locals))
	t.Logf("store refilled to    %d (%d before the flush)", settledAt, len(before))
	t.Logf("of which asked for   %d", matched)
	t.Logf("update blocks in the window: %d full, %d compressed",
		fullAfter-fullBefore, compAfter-compBefore)
	t.Logf("ObjectUpdateCached packets in the window: %d (the confounder; %d is clean)",
		cachedAfter-cachedBefore, 0)

	switch {
	case matched >= len(before)*9/10:
		t.Logf("")
		t.Logf("ANSWER: yes.  RequestMultipleObjects re-describes objects the")
		t.Logf("simulator believes we hold.  Stage 4 of the plan stands: a")
		t.Logf("viewer attaching to a live session gets the world by asking")
		t.Logf("the simulator for it, and nothing is synthesized from cache.")
	case matched == 0:
		t.Errorf("ANSWER: no.  Nothing came back.  Stage 4 must synthesize " +
			"ObjectUpdate from the cache instead, which costs about a week " +
			"and cannot carry the fields the cache does not keep.")
	default:
		t.Errorf("ANSWER: partial -- %d of %d.  Worth understanding before "+
			"stage 4 is built.", matched, len(before))
	}
}

// TestLiveTerrain asks whether a second RegionHandshakeReply makes the
// simulator send the land layers again.
//
// A region sends its heightmap once, on arrival, and slgo has never
// looked at LayerData at all.  So a viewer attached to a running session
// would have no ground under it -- literally the grey void.  If the
// simulator can be asked again, the fix is one line; if not, slgod has
// to record the raw land patches from the moment it connects and replay
// them, which is a per-region store and a good deal more work.
//
// OpenSim answers a RegionHandshakeReply with SendLayerData.  Linden's
// simulator is not OpenSim and does not document this either way.
func TestLiveTerrain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	c := newCounter()
	a := dialSpike(ctx, t, c)
	defer func() {
		if err := a.Logout(ctx, 20*time.Second); err != nil {
			t.Logf("logout: %v", err)
		}
	}()

	// Wait for the land to stop arriving.  Only land counts: wind and
	// cloud never stop, so waiting on LayerData as a whole would wait
	// forever.
	const quiet = 15 * time.Second
	start := time.Now()
	for {
		select {
		case <-time.After(5 * time.Second):
		case <-a.Done():
			t.Fatalf("session ended early: %v", a.Err())
		}
		n, last := c.land()
		t.Logf("  %d land patches, last %s ago", n, time.Since(last).Round(time.Second))
		if n > 0 && time.Since(last) > quiet {
			break
		}
		if time.Since(start) > 2*time.Minute {
			t.Fatalf("no land layers at all after %s; the region sent none", time.Since(start))
		}
	}

	before, _ := c.land()
	t.Logf("")
	t.Logf("land quiet at %d patches after %s", before, time.Since(start).Round(time.Second))
	c.report(t)

	// Ask again, exactly as the handshake handler does.
	reply := &msg.RegionHandshakeReply{}
	reply.AgentData.AgentID = a.Account.AgentID
	reply.AgentData.SessionID = a.Account.SessionID
	if err := a.Send.SendReliable(ctx, reply); err != nil {
		t.Fatalf("second RegionHandshakeReply: %v", err)
	}
	asked := time.Now()
	t.Logf("sent a second RegionHandshakeReply")

	deadline := time.After(30 * time.Second)
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
wait:
	for {
		select {
		case <-tick.C:
			n, _ := c.land()
			t.Logf("  +%4s: %d land patches (%+d)",
				time.Since(asked).Round(time.Second), n, n-before)
			if n > before {
				break wait
			}
		case <-deadline:
			break wait
		case <-a.Done():
			t.Fatalf("session ended early: %v", a.Err())
		}
	}

	after, _ := c.land()
	t.Logf("")
	t.Logf("land patches before %d, after %d, gained %d", before, after, after-before)

	if after > before {
		t.Logf("")
		t.Logf("ANSWER: yes.  The simulator re-sends the land on a second")
		t.Logf("RegionHandshakeReply.  Stage 3 forwards the viewer's own reply")
		t.Logf("once on attach and needs no terrain store at all.")
	} else {
		t.Logf("")
		t.Logf("ANSWER: no.  The land does not come back.  Stage 3 grows a")
		t.Logf("per-region store of raw land LayerData bodies, recorded from")
		t.Logf("the moment slgod connects and replayed on attach.")
	}
}
