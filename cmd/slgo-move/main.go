// Command slgo-move finds a parcel in the current region that lets
// this avatar run scripts, and teleports there.
//
// the no-script parcel, where Example stands, has ALLOW_OTHER_SCRIPTS off, so a
// script Example owns will compile and then sit there.  Scripts run per
// parcel rather than per region, so the fix may be a few metres away.
//
// The region is surveyed by asking about a grid of points.
// ParcelPropertiesRequest answers about whichever parcel covers the
// point asked about, one at a time, so the only way to enumerate a
// region is to probe it.  The answers arrive over the event queue, not
// over UDP.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/internal/slhost"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

var (
	addr    = flag.String("server", "", "slgod address (default: sl-host, port 7807)")
	profile = flag.String("agent", "example", "hosted agent")
	step    = flag.Int("step", 16, "survey grid spacing in metres")
	move    = flag.Bool("move", false, "teleport to the parcel found")
	// Parcel properties do not carry the ground height, so the target
	// height is a guess.  Asking for one above the ground is the safe
	// direction: the avatar falls to land, where asking for one below
	// it risks arriving underground.
	height = flag.Float64("z", 30, "target height")
)

// probeAfterMove is a sequence id well clear of the survey's, for the
// one probe sent after the teleport.
const probeAfterMove = 100000

// Parcel flags, from the viewer's llparcelflags.h.  Only the ones that
// bear on whether a script will run are named.
const (
	pfAllowFly          = 1 << 0
	pfAllowOtherScripts = 1 << 1
	pfForSale           = 1 << 2
	pfAllowLandmark     = 1 << 3
	pfCreateObjects     = 1 << 6
	pfUseAccessGroup    = 1 << 8
	pfUseAccessList     = 1 << 9
	pfUseBanList        = 1 << 10
	pfAllowGroupScripts = 1 << 25
	pfCreateGroupObject = 1 << 26
)

// parcel is one parcel as the simulator described it.
type parcel struct {
	LocalID int64
	Name    string
	Owner   msg.UUID
	Group   msg.UUID
	Flags   uint32
	Area    int64
	Min     msg.Vector3
	Max     msg.Vector3
	Status  int64

	// Inside is a point the simulator answered about with this
	// parcel.  A parcel need not be rectangular, so the middle of the
	// bounding box may be on a neighbour; a point that was probed is
	// known to be within.
	Inside msg.Vector3
	Probed bool
}

// scriptsRun reports whether a script this avatar owns would run here.
//
// A parcel runs other people's scripts outright, or only the group's,
// in which case it depends on the parcel's group rather than on us.
func (p *parcel) scriptsRun() bool { return p.Flags&pfAllowOtherScripts != 0 }

// canBuild reports whether we could rez an object here.
func (p *parcel) canBuild() bool { return p.Flags&pfCreateObjects != 0 }

// target is where to stand on this parcel.  A probed point is known to
// be inside it; the middle of the bounding box is only a guess.
func (p *parcel) target(z float32) msg.Vector3 {
	t := msg.Vector3{
		X: (p.Min.X + p.Max.X) / 2,
		Y: (p.Min.Y + p.Max.Y) / 2,
	}
	if p.Probed {
		t.X, t.Y = p.Inside.X, p.Inside.Y
	}
	t.Z = z
	return t
}

type run struct {
	c    *client.Conn
	me   msg.UUID
	sess msg.UUID

	mu      sync.Mutex
	parcels map[int64]*parcel
	handle  uint64

	// probes maps a sequence id to the point it asked about, so a
	// reply can be tied back to somewhere known to be on the parcel.
	probes map[int32]msg.Vector3

	// here is the parcel from the last reply we did not ask for.  When
	// an avatar moves onto a different parcel the simulator says so
	// unprompted, which is a better answer to "where is Example" than
	// anything we could work out ourselves.
	here *parcel
}

func main() {
	flag.Parse()
	ctx := context.Background()

	c, err := client.Dial(ctx, slhost.MustAddr(*addr))
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	// The subscription gates event queue events by name as well as UDP
	// messages, so ParcelProperties has to be asked for whichever way
	// the simulator chooses to answer.
	info, err := c.Attach(ctx, *profile,
		"ParcelProperties",
		"ObjectUpdate", "AlertMessage", "ChatFromSimulator",
		"TeleportStart", "TeleportProgress", "TeleportLocal",
		"TeleportFailed", "TeleportFinish", "AgentMovementComplete")
	if err != nil {
		log.Fatal(err)
	}
	r := &run{
		c: c, me: msg.MustParseUUID(info.AgentId),
		sess:    msg.MustParseUUID(info.SessionId),
		parcels: map[int64]*parcel{},
		probes:  map[int32]msg.Vector3{},
	}
	fmt.Printf("%s in %s\n\n", info.AvatarName, info.Region)

	go r.readMessages()
	go r.readEvents()

	// The region handle comes free with any ObjectUpdate, which saves
	// asking for it.
	fmt.Println("waiting for the region handle")
	if !r.waitHandle(30 * time.Second) {
		log.Fatal("no ObjectUpdate arrived, so no region handle")
	}
	r.mu.Lock()
	handle := r.handle
	r.mu.Unlock()
	fmt.Printf("region handle %d\n", handle)

	fmt.Printf("\nsurveying on a %dm grid\n", *step)
	r.survey(ctx, *step)

	found := r.found()
	fmt.Printf("\n%d parcels\n\n", len(found))
	fmt.Printf("%-8s %-28s %7s  %-5s %-5s %s\n",
		"local", "name", "area", "other", "group", "flags")
	for _, p := range found {
		fmt.Printf("%-8d %-28s %7d  %-5v %-5v %#08x %s\n",
			p.LocalID, trunc(p.Name, 28), p.Area,
			p.Flags&pfAllowOtherScripts != 0,
			p.Flags&pfAllowGroupScripts != 0,
			p.Flags, describe(p.Flags))
	}

	// Where Example is now, as the simulator volunteered it.
	if h := r.where(); h != nil {
		fmt.Printf("\nExample is on %q (local %d): scripts %v\n",
			h.Name, h.LocalID, h.scriptsRun())
	}

	// Pick the biggest parcel that will run our scripts, preferring
	// one that also lets us rez: a parcel that runs scripts but takes
	// no objects is only half the answer.
	var best *parcel
	better := func(a, b *parcel) bool {
		if a.canBuild() != b.canBuild() {
			return a.canBuild()
		}
		return a.Area > b.Area
	}
	for _, p := range found {
		if !p.scriptsRun() {
			continue
		}
		if best == nil || better(p, best) {
			best = p
		}
	}
	if best == nil {
		fmt.Println("\nNo parcel in this region runs other people's scripts.")
		fmt.Println("Example would have to leave the region, which is a")
		fmt.Println("different problem from moving within it.")
		os.Exit(1)
	}

	target := best.target(float32(*height))
	fmt.Printf("\nbest: %q (local %d, %d sqm)\n", best.Name, best.LocalID, best.Area)
	fmt.Printf("  scripts %v, build %v\n", best.scriptsRun(), best.canBuild())
	fmt.Printf("  bounds %v to %v\n", best.Min, best.Max)
	fmt.Printf("  target %v (from a probed point: %v)\n", target, best.Probed)

	if !*move {
		fmt.Println("\n(survey only; pass -move to teleport)")
		return
	}

	fmt.Println("\nteleporting")
	tp := &msg.TeleportLocationRequest{}
	tp.AgentData.AgentID, tp.AgentData.SessionID = r.me, r.sess
	tp.Info.RegionHandle = handle
	tp.Info.Position = target
	tp.Info.LookAt = msg.Vector3{X: 1}
	if err := c.Send(ctx, tp, true); err != nil {
		log.Fatal(err)
	}

	// Confirm from the simulator rather than from the teleport having
	// been sent.  Moving onto a different parcel makes the simulator
	// describe it unprompted, which is what "where is Example" means.
	r.forget()
	time.Sleep(15 * time.Second)

	p, how := r.where(), "the simulator volunteered it"
	if p == nil {
		// Nothing volunteered.  Fall back to asking about the point we
		// aimed at, which says what is there but not that Example is
		// standing on it -- worth keeping those apart.
		fmt.Println("\nnothing volunteered; asking about the target point")
		r.probe(ctx, probeAfterMove, target.X, target.Y)
		time.Sleep(6 * time.Second)
		r.mu.Lock()
		for _, q := range r.parcels {
			if q.Probed && q.Inside.X == target.X && q.Inside.Y == target.Y {
				p = q
			}
		}
		r.mu.Unlock()
		how = "inferred from the parcel at the target point"
	}
	if p == nil {
		fmt.Println("\nThe simulator said nothing, so where Example ended up")
		fmt.Println("is not established.")
		os.Exit(1)
	}
	fmt.Printf("\nExample is on %q (local %d)  [%s]\n", p.Name, p.LocalID, how)
	fmt.Printf("  other scripts %v\n", p.scriptsRun())
	fmt.Printf("  group scripts %v\n", p.Flags&pfAllowGroupScripts != 0)
	fmt.Printf("  build         %v\n", p.canBuild())
	if !p.scriptsRun() {
		fmt.Println("\nThat parcel still will not run Example's scripts.")
		os.Exit(1)
	}
	if p.LocalID != best.LocalID {
		fmt.Printf("\n(not the parcel aimed at, local %d, but it runs scripts)\n",
			best.LocalID)
	}
	fmt.Println("\nExample is on a parcel that runs his scripts.")
}

func (r *run) where() *parcel {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.here
}

func (r *run) forget() {
	r.mu.Lock()
	r.here = nil
	r.mu.Unlock()
}

// survey asks about a grid of points covering the region.
//
// Parcels are at least sixteen metres on a side, so a sixteen metre
// grid cannot step over one.
func (r *run) survey(ctx context.Context, step int) {
	n := 0
	for y := step / 2; y < 256; y += step {
		for x := step / 2; x < 256; x += step {
			n++
			r.probe(ctx, int32(n), float32(x), float32(y))
			// Pace it: every answer comes back through the event
			// queue, and flooding that helps nobody.
			time.Sleep(40 * time.Millisecond)
		}
	}
	fmt.Printf("%d probes sent, waiting for the answers\n", n)
	// The replies trail the requests.
	settle := time.Now().Add(20 * time.Second)
	last := 0
	for time.Now().Before(settle) {
		time.Sleep(2 * time.Second)
		r.mu.Lock()
		got := len(r.parcels)
		r.mu.Unlock()
		if got == last && got > 0 {
			break
		}
		last = got
	}
}

func (r *run) probe(ctx context.Context, seq int32, x, y float32) {
	m := &msg.ParcelPropertiesRequest{}
	m.AgentData.AgentID, m.AgentData.SessionID = r.me, r.sess
	d := &m.ParcelData
	d.SequenceID = seq
	d.West, d.South = x, y
	d.East, d.North = x, y
	d.SnapSelection = false
	r.mu.Lock()
	r.probes[seq] = msg.Vector3{X: x, Y: y}
	r.mu.Unlock()
	if err := r.c.Send(ctx, m, true); err != nil {
		log.Fatalf("probe: %v", err)
	}
}

func (r *run) readMessages() {
	for m := range r.c.Messages() {
		v, err := m.Decode()
		if err != nil || v == nil {
			continue
		}
		switch t := v.(type) {
		case *msg.ObjectUpdate:
			r.mu.Lock()
			if r.handle == 0 {
				r.handle = t.RegionData.RegionHandle
			}
			r.mu.Unlock()
		case *msg.ParcelProperties:
			// Some simulators still answer over UDP rather than
			// through the event queue.
			d := &t.ParcelData
			r.add(&parcel{
				LocalID: int64(d.LocalID),
				Name:    nul(d.Name),
				Owner:   d.OwnerID,
				Group:   d.GroupID,
				Flags:   d.ParcelFlags,
				Area:    int64(d.Area),
				Status:  int64(d.Status),
				Min:     d.AABBMin,
				Max:     d.AABBMax,
			}, d.SequenceID)
		case *msg.AlertMessage:
			fmt.Println("  [alert]", nul(t.AlertData.Message))
		case *msg.TeleportLocal:
			fmt.Printf("  [teleport] local, flags %#x\n", t.Info.TeleportFlags)
		case *msg.TeleportFailed:
			fmt.Printf("  [teleport] failed: %s\n", nul(t.Info.Reason))
		case *msg.TeleportFinish:
			fmt.Println("  [teleport] finished")
		case *msg.AgentMovementComplete:
			fmt.Printf("  [movement] at %v\n", t.Data.Position)
		}
	}
}

// readEvents picks the parcel descriptions out of the event queue.
func (r *run) readEvents() {
	for e := range r.c.Events() {
		if e.Name != "ParcelProperties" {
			continue
		}
		body, err := e.Decode()
		if err != nil {
			continue
		}
		blocks, ok := body["ParcelData"].([]any)
		if !ok || len(blocks) == 0 {
			continue
		}
		m := llsd.Map(blocks[0])
		if m == nil {
			continue
		}
		p := &parcel{
			LocalID: llsd.Int(m, "LocalID"),
			Name:    llsd.String(m, "Name"),
			Flags:   uint32(llsd.Int(m, "ParcelFlags")),
			Area:    llsd.Int(m, "Area"),
			Status:  llsd.Int(m, "Status"),
			Min:     vec(m, "AABBMin"),
			Max:     vec(m, "AABBMax"),
		}
		p.Owner, _ = msg.ParseUUID(llsd.String(m, "OwnerID"))
		p.Group, _ = msg.ParseUUID(llsd.String(m, "GroupID"))
		r.add(p, int32(llsd.Int(m, "SequenceID")))
	}
}

// add records a parcel, tying it to the point that was asked about.
//
// A reply carrying a sequence id we never sent was not asked for, and
// is the simulator saying which parcel the avatar is standing on.
func (r *run) add(p *parcel, seq int32) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if at, ok := r.probes[seq]; ok {
		p.Inside, p.Probed = at, true
	} else {
		r.here = p
	}
	if old, ok := r.parcels[p.LocalID]; ok && old.Probed && !p.Probed {
		p.Inside, p.Probed = old.Inside, true
	}
	r.parcels[p.LocalID] = p
}

// vec reads an LLSD array of three reals.
func vec(m map[string]any, key string) msg.Vector3 {
	a, ok := m[key].([]any)
	if !ok || len(a) < 3 {
		return msg.Vector3{}
	}
	var v [3]float32
	for i := range 3 {
		switch t := a[i].(type) {
		case float64:
			v[i] = float32(t)
		case int64:
			v[i] = float32(t)
		}
	}
	return msg.Vector3{X: v[0], Y: v[1], Z: v[2]}
}

func (r *run) found() []*parcel {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*parcel, 0, len(r.parcels))
	for _, p := range r.parcels {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LocalID < out[j].LocalID })
	return out
}

func (r *run) waitHandle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		h := r.handle
		r.mu.Unlock()
		if h != 0 {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// describe names the flags that matter for standing somewhere and
// running something.
func describe(f uint32) string {
	var on []string
	for _, b := range []struct {
		bit  uint32
		name string
	}{
		{pfAllowOtherScripts, "scripts"},
		{pfAllowGroupScripts, "group-scripts"},
		{pfCreateObjects, "build"},
		{pfCreateGroupObject, "group-build"},
		{pfAllowFly, "fly"},
		{pfAllowLandmark, "landmark"},
		{pfForSale, "for-sale"},
		{pfUseAccessList, "access-list"},
		{pfUseAccessGroup, "access-group"},
		{pfUseBanList, "ban-list"},
	} {
		if f&b.bit != 0 {
			on = append(on, b.name)
		}
	}
	if len(on) == 0 {
		return "-"
	}
	s := on[0]
	for _, n := range on[1:] {
		s += "," + n
	}
	return s
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "*"
	}
	return s
}

func nul(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}
