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

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// Hosted is a session slgod is holding.
type Hosted struct {
	conn *client.Conn
	info *Info
}

var _ Backend = (*Hosted)(nil)

// Attach connects to a slgod and attaches to one of its sessions.
//
// An empty name takes the daemon's default: of the sessions it holds,
// the one it has held longest.  Which one that was is in the returned
// info, and a client that did not name a session should say so, since
// the default depends on the daemon's history and nothing on disk
// records it.
func Attach(ctx context.Context, addr, name string, subscribe ...string) (*Hosted, error) {
	conn, err := client.Dial(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("sl: cannot reach slgod at %s: %w", addr, err)
	}
	h, err := AttachConn(ctx, conn, name, subscribe...)
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
	name = AgentName(name)
	// An empty name is passed THROUGH rather than resolved here.  The
	// daemon picks -- the session it has held longest -- and the
	// Attached frame says which, so every client agrees about what "none
	// named" means and a client too old to know the rule cannot disagree
	// with one that does.
	if len(subscribe) == 0 {
		subscribe = Subscriptions
	}
	info, err := conn.Attach(ctx, name, subscribe...)
	if err != nil {
		if name == "" {
			return nil, fmt.Errorf("sl: cannot attach to that slgod's default session: %w", err)
		}
		return nil, fmt.Errorf("sl: cannot attach to %q: %w", name, err)
	}
	return &Hosted{conn: conn, info: infoFromPB(info)}, nil
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

func (h *Hosted) Info() *Info               { return h.info }
func (h *Hosted) Messages() <-chan *Message { return h.conn.Messages() }
func (h *Hosted) Done() <-chan struct{}     { return h.conn.Done() }
func (h *Hosted) Err() error                { return h.conn.Err() }
func (h *Hosted) Close() error              { return h.conn.Close() }
func (h *Hosted) HasCap(name string) bool   { return h.conn.HasCap(name) }

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

func (h *Hosted) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	return h.conn.DoCap(ctx, r)
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
	}, nil
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
			Parent:       o.Parent,
			PCode:        uint8(o.Pcode),
			TextureEntry: o.TextureEntry,
			Text:         o.Text,
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
		Protocols: r.Protocols,
	}, r.Known, nil
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
