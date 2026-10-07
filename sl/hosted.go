package sl

// The backend that talks to slgod.
//
// Everything here is a translation: the daemon answers in protobuf
// because that is what crosses the wire, and this turns it into the
// package's own types so that nothing above ever sees a generated
// struct.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// Hosted is a session slgod is holding.
type Hosted struct {
	conn *client.Conn

	// mu guards info, which is not what it was at attach time: the
	// daemon may re-establish the session under this client, and
	// Refresh is how the new identity gets here.  See Backend.Info.
	mu   sync.RWMutex
	info *Info

	// standing is what this session asked to be relayed when it
	// attached, and it is what Unwatch refuses to take away.
	//
	// Written once, before the Hosted is handed to anything, so it is
	// read without a lock.  It exists because Unwatch is a subtraction
	// from one set shared by everything using this connection: a caller
	// that named AvatarAnimation at attach time meant to keep it, and a
	// sit that borrowed the same name for a second must not hand back
	// something it was never lent.
	standing map[string]bool

	// handled is the daemon's notices about offers, in this package's
	// own type; see HandledOffers.
	handledOnce sync.Once
	handled     chan *Handled
}

var (
	_ Backend = (*Hosted)(nil)
	_ Watcher = (*Hosted)(nil)
)

// Attach connects to a slgod and attaches to one of its sessions.
//
// An empty name takes the session SLGO_AGENT names (see AgentName), and
// failing that the daemon's default: of the sessions it holds and has
// not stopped, the one it has held longest.  Which one that was is in
// the returned info, and a client that did not name a session should
// say so, since the default depends on the daemon's history and nothing
// on disk records it.
func Attach(ctx context.Context, addr, name string, subscribe ...string) (*Hosted, error) {
	return attach(ctx, addr, name, false, subscribe)
}

// AttachWeak is Attach for a client that ATTENDS an avatar rather than
// uses it.
//
// It is relayed to and counted like any other, but slgod does not
// consult it when deciding whether anybody would mind the session being
// taken away -- so "slsh logout" goes on meaning something with a
// daemon sitting attached to every avatar all day.  See Attach.weak in
// slgo.proto for why that matters more than it sounds.
func AttachWeak(ctx context.Context, addr, name string, subscribe ...string) (*Hosted, error) {
	return attach(ctx, addr, name, true, subscribe)
}

func attach(ctx context.Context, addr, name string, weak bool, subscribe []string) (*Hosted, error) {
	conn, err := client.Dial(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("sl: cannot reach slgod at %s: %w", addr, err)
	}
	h, err := attachConn(ctx, conn, name, weak, subscribe)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return h, nil
}

// EnvAgent names the session to use when nothing else does.
//
// It exists so the choice can be made once for a shell rather than
// repeated on every command.  With one avatar hosted there was nothing
// to choose; with several, every command that does not care still has
// to be pointed somewhere, and pointing it by hand each time is how the
// wrong avatar gets used.
//
// Read here rather than in each command, so that they cannot disagree
// about what it means -- the same reason ConfigDir reads
// SLGO_CONFIG_DIR rather than every caller doing it.
const EnvAgent = "SLGO_AGENT"

// AgentName resolves which session was asked for: what was named, or
// the environment's answer, or empty to leave it to the daemon.
//
// A caller that treats "named" differently from "any will do" -- one
// that would otherwise move on to another avatar when this one is busy
// -- must ask THIS rather than test the string, because an avatar named
// by the environment is still a named avatar.
func AgentName(named string) string {
	if named != "" {
		return named
	}
	return strings.TrimSpace(os.Getenv(EnvAgent))
}

// AttachConn attaches on a connection the caller already has.
func AttachConn(ctx context.Context, conn *client.Conn, name string, subscribe ...string) (*Hosted, error) {
	return attachConn(ctx, conn, name, false, subscribe)
}

func attachConn(ctx context.Context, conn *client.Conn, name string, weak bool, subscribe []string) (*Hosted, error) {
	name = AgentName(name)
	// An empty name is passed THROUGH rather than resolved here.  The
	// daemon picks -- the session it has held longest, of those not
	// stopped -- and the Attached frame says which, so every client
	// agrees about what "none named" means and a client too old to know
	// the rule cannot disagree with one that does.
	if len(subscribe) == 0 {
		subscribe = Subscriptions
	}
	att := conn.Attach
	if weak {
		att = conn.AttachWeak
	}
	info, err := att(ctx, name, subscribe...)
	if err != nil {
		if name == "" {
			return nil, fmt.Errorf("sl: cannot attach to that slgod's default session: %w", err)
		}
		return nil, fmt.Errorf("sl: cannot attach to %q: %w", name, err)
	}
	standing := make(map[string]bool, len(subscribe))
	for _, n := range subscribe {
		standing[n] = true
	}
	return &Hosted{conn: conn, info: infoFromPB(info), standing: standing}, nil
}

// Conn is the connection underneath, for the few things that are only
// meaningful through a daemon.
func (h *Hosted) Conn() *client.Conn { return h.conn }

// Sessions lists what this daemon is holding.  Hosted only: a direct
// session is the only one there is.
func (h *Hosted) Sessions(ctx context.Context) ([]string, error) {
	agents, err := h.conn.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, a.Name)
	}
	return out, nil
}

func (h *Hosted) Info() *Info {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.info
}

// Refresh asks the daemon who this session is now.
//
// Through the connection's own Refresh, so that its capability list,
// which HasCap reads, is brought up to date by the same answer.  The
// daemon builds that answer from the live session rather than from what
// it said at attach time, and it is asked by the name the attach was
// answered with, so a session attached with no name is asked about
// rather than whichever is the default by now.
func (h *Hosted) Refresh(ctx context.Context) (*Info, error) {
	a, err := h.conn.Refresh(ctx)
	if err != nil {
		return nil, err
	}
	info := infoFromPB(a)
	h.mu.Lock()
	h.info = info
	h.mu.Unlock()
	return info, nil
}
func (h *Hosted) Messages() <-chan *Message { return h.conn.Messages() }
func (h *Hosted) Done() <-chan struct{}     { return h.conn.Done() }

// Events is the grid's event queue as slgod fans it out.
//
// The daemon is the only thing polling the queue -- the sequence has to
// be acknowledged in order and a second poller would take events the
// first never sees -- and it hands each one to every attached client
// that asked for it by name.  So reading this does not starve a viewer
// attached to the same avatar; it is a copy of what the daemon already
// received.
func (h *Hosted) Events() <-chan *QueueEvent { return h.conn.Events() }

// RegionChanges is the daemon saying the avatar is somewhere else.
//
// It comes off the notice stream and not the message one, because it is
// not something the grid said: it is something that happened to the
// connection, like the session going away and coming back.  The daemon
// is the only one in a position to know -- a teleport is announced to
// the session it moves, and by the time a client could read the
// announcement the daemon has already followed it.
func (h *Hosted) RegionChanges() <-chan *RegionChange { return h.conn.RegionChanges() }

func (h *Hosted) Err() error              { return h.conn.Err() }
func (h *Hosted) Close() error            { return h.conn.Close() }
func (h *Hosted) HasCap(name string) bool { return h.conn.HasCap(name) }

// Lock takes a lock through slgod, which holds it for as long as this
// client's stream lasts.
func (h *Hosted) Lock(ctx context.Context, name string) error {
	return h.conn.Lock(ctx, name)
}

// TryLock takes one only if it is free.
func (h *Hosted) TryLock(ctx context.Context, name string) (bool, string, error) {
	return h.conn.TryLock(ctx, name)
}

// Unlock gives one back.
func (h *Hosted) Unlock(name string) error { return h.conn.Unlock(name) }

func (h *Hosted) Flush(ctx context.Context) (int, error) {
	return h.conn.Flush(ctx)
}

func (h *Hosted) Send(ctx context.Context, m msg.Message, reliable bool) error {
	return h.conn.Send(ctx, m, reliable)
}

// Control asks the daemon to send one AgentUpdate carrying these flags.
//
// The daemon owns the camera, so it is the only thing that can send an
// update that is right about everything except the bit being asked for;
// see Backend.Control.
func (h *Hosted) Control(ctx context.Context, flags uint32) error {
	return h.conn.Control(ctx, flags)
}

// Watch adds message names to what the daemon relays to this session.
//
// It travels on the same stream the session's messages go out on, and
// the daemon reads that stream in order, so a name asked for here is in
// force before anything sent afterwards ON THE STREAM.  That does not
// extend to the unary calls -- Control is one -- which are separate
// requests the daemon may take up on another goroutine; see sit.go,
// where the one place that matters is worked through.
func (h *Hosted) Watch(names ...string) error { return h.conn.Watch(names...) }

// Unwatch takes them away again, except for the ones this session asked
// for when it attached.
//
// The exception is what makes borrowing a name safe.  There is one
// subscription set per connection, so an unqualified Unwatch would let a
// command that wanted AvatarAnimation for a second take it away from a
// caller that attached asking for it and expects it for the whole
// session.
func (h *Hosted) Unwatch(names ...string) error {
	drop := make([]string, 0, len(names))
	for _, n := range names {
		if !h.standing[n] {
			drop = append(drop, n)
		}
	}
	if len(drop) == 0 {
		return nil
	}
	return h.conn.Unwatch(drop...)
}

func (h *Hosted) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	return h.conn.DoCap(ctx, r)
}

func (h *Hosted) SimAttachments(ctx context.Context, avatar msg.UUID) (*SimAttachments, error) {
	who := ""
	if !avatar.IsZero() {
		who = avatar.String()
	}
	r, err := h.conn.Attachments(ctx, who)
	if err != nil {
		return nil, err
	}
	if !r.GetKnown() {
		return nil, nil
	}
	out := &SimAttachments{
		CofVersion: int(r.GetCofVersion()),
		Heard:      time.UnixMicro(r.GetReceivedAt()),
	}
	for _, a := range r.GetAttachments() {
		// Empty is pending; anything else that will not parse is not
		// something to act on, and is left out as pending is.
		id, err := msg.ParseUUID(a.GetObjectId())
		if err != nil || id.IsZero() {
			out.Pending++
			continue
		}
		out.Objects = append(out.Objects, SimAttachment{Object: id, Point: int(a.GetPoint())})
	}
	return out, nil
}

func (h *Hosted) Presence(ctx context.Context, drawDistance float32) (*Presence, error) {
	r, err := h.conn.Presence(ctx, drawDistance)
	if err != nil {
		return nil, err
	}
	return &Presence{
		Position:     fromPB(r.Position),
		LookAt:       fromPB(r.LookAt),
		Camera:       fromPB(r.Camera),
		DrawDistance: r.DrawDistance,
		RegionHandle: r.RegionHandle,
		Region:       r.Region,
		ActiveGroup:  parseUUIDOrZero(r.ActiveGroup),
		Groups:       groupsFromPB(r.Groups),

		MaturityPreference: r.MaturityPreference,
		MaturityCeiling:    r.MaturityCeiling,

		ScriptControlsTaken:    r.ScriptControlsTaken,
		ScriptControlsPassedOn: r.ScriptControlsPassedOn,

		Health:      r.Health,
		HealthKnown: r.HealthKnown,
	}, nil
}

// groupsFromPB reads the membership list off the wire.
//
// A row whose id will not parse is dropped rather than kept as the zero
// uuid: zero is what "no group" is spelled as everywhere else here, and
// a nameless zero in the list would read as a group the avatar could
// act as.
func groupsFromPB(gs []*pb.GroupMembership) []Group {
	if len(gs) == 0 {
		return nil
	}
	out := make([]Group, 0, len(gs))
	for _, g := range gs {
		id, err := msg.ParseUUID(g.GetId())
		if err != nil || id.IsZero() {
			continue
		}
		out = append(out, Group{ID: id, Name: g.GetName(), Powers: g.GetPowers()})
	}
	return out
}

// lightFromPB and projectorFromPB are lightPB and projectorPB's inverses
// in internal/server.
func lightFromPB(l *pb.PrimLight) *msg.Light {
	if l == nil {
		return nil
	}
	return &msg.Light{
		Colour:    [3]uint8{uint8(l.Red), uint8(l.Green), uint8(l.Blue)},
		Intensity: l.Intensity, Radius: l.Radius, Cutoff: l.Cutoff, Falloff: l.Falloff,
	}
}

func projectorFromPB(p *pb.PrimProjector) *msg.LightImage {
	if p == nil {
		return nil
	}
	return &msg.LightImage{Texture: parseUUIDOrZero(p.Texture), FOV: p.Fov, Focus: p.Focus, Ambiance: p.Ambiance}
}

func (h *Hosted) Objects(ctx context.Context, named, id string) ([]*Seen, error) {
	r, err := h.conn.Objects(ctx, named, id)
	if err != nil {
		return nil, err
	}
	out := make([]*Seen, 0, len(r.Objects))
	for _, o := range r.Objects {
		oid, err := msg.ParseUUID(o.Id)
		if err != nil {
			continue
		}
		s := &Seen{
			Object:       Object{ID: oid, Local: o.Local},
			Owner:        parseUUIDOrZero(o.Owner),
			Position:     fromPB(o.Position),
			Scale:        fromPB(o.Scale),
			Rotation:     quatFromPB(o.Rotation),
			Shape:        shapeFromPB(o.Shape),
			Parent:       o.Parent,
			PCode:        uint8(o.Pcode),
			TextureEntry: o.TextureEntry,
			TextureAnim:  o.TextureAnim,
			Click:        uint8(o.Click),
			ClickKnown:   o.ClickKnown,
			Sculpt:       msg.SculptMark{Kind: msg.SculptKind(o.SculptKind), ID: parseUUIDOrZero(o.SculptId)},
			LinkNumber:   int(o.LinkNumber),
			LinkKnown:    o.LinkKnown,
			LinkNote:     o.LinkNote,
			Text:         o.Text,
			Light:        lightFromPB(o.Light),
			Projector:    projectorFromPB(o.Projector),

			RenderMaterials: renderMaterialsFromPB(o.RenderMaterials),
			GLTF:            gltfFromPB(o.GltfOverrides),
		}
		s.Object.Name = o.Name
		s.AttachPoint = int(o.AttachPoint)
		s.AttachItem = parseUUIDOrZero(o.AttachItem)
		out = append(out, s)
	}
	return out, nil
}

func (h *Hosted) Region(ctx context.Context) (*Region, bool, error) {
	r, err := h.conn.Region(ctx)
	if err != nil {
		return nil, false, err
	}
	return &Region{
		ID: parseUUIDOrZero(r.Id), Handle: r.Handle, Name: r.Name,
		Flags: r.Flags, Extended: r.FlagsExtended,
		Access: uint8(r.Access), Owner: parseUUIDOrZero(r.Owner),
		EstateManager: r.EstateManager,
		WaterHeight:   r.WaterHeight,
		ProductName:   r.ProductName, ProductSKU: r.ProductSku,
		ColoName: r.ColoName,
		CPUClass: r.CpuClass, CPURatio: r.CpuRatio,
		Protocols:      r.Protocols,
		ObjectCapacity: r.ObjectCapacity,
	}, r.Known, nil
}

// LastRegionDetails reads the RegionInfo the daemon kept.  It comes with
// its age, and is placed by this machine's clock.
func (h *Hosted) LastRegionDetails(ctx context.Context) (*RegionDetails, uint64, error) {
	r, err := h.conn.RegionDetails(ctx)
	if err != nil {
		return nil, 0, err
	}
	if !r.Known {
		return nil, r.Heard, nil
	}
	out := &RegionDetails{
		Name: r.Name, EstateID: r.EstateId, ParentEstateID: r.ParentEstateId,
		Flags: r.Flags, Extended: r.FlagsExtended, Access: uint8(r.Access),
		AgentLimit:     r.MaxAgents,
		HardAgentLimit: r.HardMaxAgents, HardObjectLimit: r.HardMaxObjects,
		ObjectBonus: r.ObjectBonus, BillableFactor: r.BillableFactor,
		WaterHeight:       r.WaterHeight,
		TerrainRaiseLimit: r.TerrainRaiseLimit, TerrainLowerLimit: r.TerrainLowerLimit,
		PricePerMeter: r.PricePerMeter,
		ProductSKU:    r.ProductSku, ProductName: r.ProductName,
		Heard: time.Now().Add(-time.Duration(r.AgeMs) * time.Millisecond),
	}
	if c := r.Chat; c != nil {
		out.Chat = &ChatRanges{
			Whisper: c.Whisper, Normal: c.Normal, Shout: c.Shout,
			WhisperOffset: c.WhisperOffset, NormalOffset: c.NormalOffset, ShoutOffset: c.ShoutOffset,
			Flags: c.Flags,
		}
	}
	return out, r.Heard, nil
}

// SimStats reads the daemon's history.  Each report comes with its age
// rather than its time, and is placed by this machine's clock.
func (h *Hosted) SimStats(ctx context.Context) (*SimStats, error) {
	r, err := h.conn.SimStats(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := &SimStats{Handle: r.Handle, Read: now, Samples: make([]StatSample, len(r.Samples))}
	for i, sm := range r.Samples {
		v := make(map[StatID]float64, len(sm.Stats))
		for _, st := range sm.Stats {
			v[StatID(st.Id)] = float64(st.Value)
		}
		out.Samples[i] = StatSample{At: now.Add(-time.Duration(sm.AgeMs) * time.Millisecond), Values: v}
	}
	return out, nil
}

// Land reads what the daemon's session was told, and rebuilds the
// overlay from the squares and the quarter mask it sent.
func (h *Hosted) Land(ctx context.Context) (*Land, error) {
	r, err := h.conn.Land(ctx)
	if err != nil {
		return nil, err
	}
	out := &Land{
		Overlay: agent.OverlayFrom(r.GetOverlay(), uint8(r.GetOverlayQuarters())),
	}
	if r.GetParcelKnown() {
		out.Told = &Told{Name: r.GetParcelName(), LocalID: r.GetParcelLocalId()}
	}
	return out, nil
}

// Ground asks the daemon, which decoded the terrain as it arrived.
func (h *Hosted) Ground(ctx context.Context, west, south, east, north float32) (float32, bool, error) {
	return h.conn.Ground(ctx, west, south, east, north)
}

func (h *Hosted) Neighbours(ctx context.Context, set *bool) (*Neighbours, error) {
	r, err := h.conn.Neighbours(ctx, set)
	if err != nil {
		return nil, err
	}
	out := &Neighbours{On: r.GetOn(), Held: make([]Neighbour, 0, len(r.GetNeighbours()))}
	for _, n := range r.GetNeighbours() {
		out.Held = append(out.Held, Neighbour{
			Handle:    n.GetHandle(),
			Addr:      n.GetAddress(),
			Name:      n.GetName(),
			Handshook: n.GetHandshook(),
			Heard:     n.GetHeard(),
		})
	}
	return out, nil
}

func (h *Hosted) Friends(ctx context.Context) ([]Friend, error) {
	fs, err := h.conn.Friends(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Friend, 0, len(fs))
	for _, f := range fs {
		id, err := msg.ParseUUID(f.Id)
		if err != nil {
			continue
		}
		out = append(out, Friend{
			ID: id, Online: f.Online,
			RightsGiven: f.RightsGiven, RightsHas: f.RightsHas,
		})
	}
	return out, nil
}

func (h *Hosted) NoteFriend(ctx context.Context, id msg.UUID, online bool) error {
	return h.conn.NoteFriend(ctx, id, online)
}

// fromPB is the only place a protobuf vector becomes one of ours.
func fromPB(v *pb.Vector3) msg.Vector3 {
	if v == nil {
		return msg.Vector3{}
	}
	return msg.Vector3{X: v.X, Y: v.Y, Z: v.Z}
}

func infoFromPB(i *pb.AgentInfo) *Info {
	return &Info{
		Name:          i.Name,
		AgentID:       parseUUIDOrZero(i.AgentId),
		SessionID:     parseUUIDOrZero(i.SessionId),
		AvatarName:    i.AvatarName,
		Region:        i.Region,
		InventoryRoot: parseUUIDOrZero(i.InventoryRoot),
		Channel:       i.ChannelVersion,
		Caps:          i.Caps,
	}
}

// quatFromPB reads a rotation, treating a missing one as none.
func quatFromPB(q *pb.Quaternion) msg.Quaternion {
	if q == nil {
		return msg.Quaternion{}
	}
	return msg.Quaternion{X: q.X, Y: q.Y, Z: q.Z}
}

// shapeFromPB reads the packed prim form.  The server leaves it out
// for an object nothing has described, and the zero value says so.
func shapeFromPB(p *pb.PrimShape) msg.PrimShape {
	if p == nil {
		return msg.PrimShape{}
	}
	return msg.PrimShape{
		PathCurve: uint8(p.PathCurve), ProfileCurve: uint8(p.ProfileCurve),
		PathBegin: uint16(p.PathBegin), PathEnd: uint16(p.PathEnd),
		PathScaleX: uint8(p.PathScaleX), PathScaleY: uint8(p.PathScaleY),
		PathShearX: uint8(p.PathShearX), PathShearY: uint8(p.PathShearY),
		PathTwist: int8(p.PathTwist), PathTwistBegin: int8(p.PathTwistBegin),
		PathRadiusOffset: int8(p.PathRadiusOffset),
		PathTaperX:       int8(p.PathTaperX), PathTaperY: int8(p.PathTaperY),
		PathRevolutions: uint8(p.PathRevolutions), PathSkew: int8(p.PathSkew),
		ProfileBegin: uint16(p.ProfileBegin), ProfileEnd: uint16(p.ProfileEnd),
		ProfileHollow: uint16(p.ProfileHollow),
	}
}

// Slots asks the daemon for n of the shared objects at once, all of them
// or none, and waits its turn.
//
// It is not part of Backend.  A direct session has no daemon and so no
// pool: there is one process and one avatar, nothing to contend with,
// and a caller that finds this missing knows it is talking to a session
// that needs no arbitration.  See internal/session.
func (h *Hosted) Slots(ctx context.Context, n int, timeout time.Duration, agent string) (*client.Grant, error) {
	return h.conn.Slots(ctx, n, timeout, agent)
}

// SetSlotsPerAgent says how many places per avatar the caller can wear;
// see client.Conn.SetSlotsPerAgent.
func (h *Hosted) SetSlotsPerAgent(n int) { h.conn.SetSlotsPerAgent(n) }

// SlotsWithin is Slots giving up with client.ErrStillBusy when the
// objects have not come free within wait.
func (h *Hosted) SlotsWithin(ctx context.Context, n int, timeout, wait time.Duration, agent string) (*client.Grant, error) {
	return h.conn.SlotsWithin(ctx, n, timeout, wait, agent)
}

// TrySlots asks and comes back at once either way.
func (h *Hosted) TrySlots(ctx context.Context, n int, timeout time.Duration, agent string) (*client.Grant, error) {
	return h.conn.TrySlots(ctx, n, timeout, agent)
}

// RenewSlots puts a grant's clock back.
func (h *Hosted) RenewSlots(ctx context.Context, id string, timeout time.Duration) (*client.Grant, error) {
	return h.conn.RenewSlots(ctx, id, timeout)
}

// ReleaseSlots gives a grant back, saying whether the objects were left
// fit for the next caller.
func (h *Hosted) ReleaseSlots(id string, clean bool) error {
	return h.conn.ReleaseSlots(id, clean)
}

// Dropped is how many messages the daemon sent that this connection
// threw away because nothing was reading them fast enough.
//
// Anything but zero means something arrived and was lost, and there is
// no asking for it again.  A run whose answer surprises you is worth
// checking against this before it is believed.
func (h *Hosted) Dropped() uint64 { return h.conn.Dropped() }

// OnDrop says what to call when one is thrown away.
func (h *Hosted) OnDrop(fn func(what string)) { h.conn.OnDrop(fn) }

// A Hosted is an OfferKeeper: slgod keeps the offers made to the avatar
// whether or not anybody is attached.  See offers.go.
var _ OfferKeeper = (*Hosted)(nil)

// KeptOffers is what the daemon handed over at attach.  False is a
// daemon too old to keep a record, or an attach that did not ask for
// instant messages, and either way a session knows only what it sees.
func (h *Hosted) KeptOffers() (*OfferRecord, []*Message, bool) {
	r := h.conn.Offers()
	if r == nil {
		return nil, nil, false
	}
	rec := &OfferRecord{
		Kept:    len(r.GetMessages()),
		Evicted: int(r.GetEvicted()),
		Limit:   int(r.GetLimit()),
	}
	if r.GetSince() != 0 {
		rec.Since = time.UnixMicro(r.GetSince())
	}
	msgs := make([]*Message, 0, len(r.GetMessages()))
	for _, m := range r.GetMessages() {
		out := &Message{
			ID:       msg.ID(m.GetId()),
			Name:     m.GetName(),
			Sequence: m.GetSequence(),
			Flags:    m.GetFlags(),
			Body:     m.GetBody(),
			Offer:    m.GetOffer(),
			Recorded: true,
		}
		if m.GetReceivedAt() != 0 {
			out.At = time.UnixMicro(m.GetReceivedAt())
		}
		msgs = append(msgs, out)
	}
	return rec, msgs, true
}

// Handled asks the daemon whether this session may deal with an offer.
func (h *Hosted) Handled(ctx context.Context, key, how string) (bool, *Handled, error) {
	r, err := h.conn.Handled(ctx, key, how, false)
	if err != nil {
		return false, nil, err
	}
	var earlier *Handled
	if e := r.GetEarlier(); e != nil {
		earlier = handledFromPB(e)
	}
	return r.GetClaimed(), earlier, nil
}

// Unhandled puts one back.
func (h *Hosted) Unhandled(ctx context.Context, key string) error {
	_, err := h.conn.Handled(ctx, key, "", true)
	return err
}

// HandledOffers is the daemon's word that offers have been dealt with,
// translated as it arrives.  One goroutine does the translating, started
// on first use and ended by the stream ending.
func (h *Hosted) HandledOffers() <-chan *Handled {
	h.handledOnce.Do(func() {
		h.handled = make(chan *Handled, 64)
		go func() {
			defer close(h.handled)
			for n := range h.conn.HandledOffers() {
				// Not for ever: a reader that has stopped must not
				// keep this goroutine after the stream has gone.
				select {
				case h.handled <- handledFromPB(n):
				case <-h.conn.Done():
					return
				}
			}
		}()
	})
	return h.handled
}

func handledFromPB(n *pb.OfferHandled) *Handled {
	out := &Handled{Key: n.GetOffer(), How: n.GetHow(), By: n.GetBy()}
	if n.GetAt() != 0 {
		out.At = time.UnixMicro(n.GetAt())
	}
	return out
}
