package world

import (
	"context"
	"fmt"
	"sort"
	"time"

	"slgo/agent"
	"slgo/msg"
	pb "slgo/proto/slgov1"
)

// Presence is where the avatar is and how far it is being asked to
// see.
type Presence struct {
	Position msg.Vector3
	LookAt   msg.Vector3

	// Camera is what the simulator is told we are looking from.  It
	// follows the avatar, and it rather than Position is what decides
	// which objects get described to us.
	Camera msg.Vector3

	// DrawDistance is how far, in metres, the simulator is asked to
	// describe things.
	DrawDistance float32

	RegionHandle uint64
	Region       string

	// ActiveGroup is the group the avatar is acting as. Zero means
	// none, which is what a headless login starts with -- and what
	// makes a parcel refuse to let it build.
	ActiveGroup msg.UUID
}

// Where reports the avatar's position and view.
//
// Position is exact after a teleport or entering a region, and to the
// nearest metre while walking, which is all CoarseLocationUpdate
// carries.
func (w *World) Where(ctx context.Context) (*Presence, error) {
	return w.presence(ctx, 0)
}

// SetDrawDistance changes how far the simulator is asked to describe
// things, and returns the presence it ended up with.
//
// This is the "in the world" of ObjectsNamed and AllObjects: nothing
// beyond it is described at all, so nothing beyond it can be found.
//
// It works one way and slowly.  Raising it brings more in, but over
// the following seconds rather than at once, so a raise wants a Settle
// after it -- 128 to 256 metres took the count from 195 to 217.
// Lowering it does not take anything away, because what has already
// been described has already been heard: 32 metres left the count at
// 193.  Treat it as a floor on what can be found, not a filter.
func (w *World) SetDrawDistance(ctx context.Context, metres float32) (*Presence, error) {
	if metres <= 0 {
		return nil, fmt.Errorf("world: draw distance must be positive, got %v", metres)
	}
	return w.presence(ctx, metres)
}

func (w *World) presence(ctx context.Context, set float32) (*Presence, error) {
	r, err := w.c.Presence(ctx, set)
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

func fromPB(v *pb.Vector3) msg.Vector3 {
	if v == nil {
		return msg.Vector3{}
	}
	return msg.Vector3{X: v.X, Y: v.Y, Z: v.Z}
}

// Inventory fetches the whole inventory tree over AIS.
//
// The UDP FetchInventoryDescendents this replaces was retired: the
// simulator accepts the request and never answers.
func (w *World) Inventory(ctx context.Context) (*agent.Inventory, error) {
	inv := agent.NewInventory(w.invRoot)
	if err := agent.FetchInventory(ctx, w.c, inv, agent.FetchOptions{}); err != nil {
		return nil, fmt.Errorf("world: reading inventory: %w", err)
	}
	return inv, nil
}

// Seen is what is known about an object in the region.
//
// "In the region" means within the draw distance: the simulator
// describes what is near the camera and nothing else, so an object
// further away is not merely unnamed here, it is unknown.
type Seen struct {
	Object

	Owner    msg.UUID
	Position msg.Vector3
	Rotation msg.Quaternion
	Scale    msg.Vector3

	// Parent is the local id of the root this is linked to, or zero if
	// it is a root itself.
	Parent uint32

	// PCode says what kind of thing it is: 9 is a prim, 47 an avatar.
	PCode uint8

	// TextureEntry is the packed per face appearance, when one has
	// been seen.  DecodeTextureEntry unpacks it.
	TextureEntry []byte

	// Text is the floating text above the object.
	Text string
}

// Faces unpacks the appearance, if any has been seen.
func (s *Seen) Faces(count int) ([]Face, error) {
	if len(s.TextureEntry) == 0 {
		return nil, fmt.Errorf("world: nothing has described the faces of %s", s.ID)
	}
	return DecodeTextureEntry(s.TextureEntry, count)
}

// IsAvatar reports whether this is an avatar rather than a prim.
func (s *Seen) IsAvatar() bool { return s.PCode == pcodeAvatar }

// IsRoot reports whether this is not linked under anything.
func (s *Seen) IsRoot() bool { return s.Parent == 0 }

const (
	pcodePrim   = 9
	pcodeAvatar = 47
)

// ObjectByID returns what is known about one object.
//
// The name is asked for if nobody has asked before, which is a round
// trip: an ObjectUpdate carries no name, so a name is only ever had by
// asking.
func (w *World) ObjectByID(ctx context.Context, id msg.UUID, timeout time.Duration) (*Seen, error) {
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	found, err := w.fetch(ctx, "", id.String())
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("world: %s is not in the region, or is beyond the draw distance", id)
	}
	if found[0].Name == "" {
		if err := w.resolve(ctx, []msg.UUID{id}, timeout); err != nil {
			return nil, err
		}
		if again, err := w.fetch(ctx, "", id.String()); err == nil && len(again) > 0 {
			found = again
		}
	}
	return found[0], nil
}

// AllObjects returns everything the region has described, with names
// filled in.
//
// Filling the names is the expensive part: one request for every
// object nobody has asked about yet, which on a first run is all of
// them.  They are asked for in batches so a scan does not go out as a
// flood, and the answers are remembered by the server, so the second
// caller pays nothing.
func (w *World) AllObjects(ctx context.Context, timeout time.Duration) ([]*Seen, error) {
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	found, err := w.fetch(ctx, "", "")
	if err != nil {
		return nil, err
	}

	var unnamed []msg.UUID
	for _, s := range found {
		if s.Name == "" {
			unnamed = append(unnamed, s.ID)
		}
	}
	if len(unnamed) == 0 {
		return found, nil
	}
	if err := w.resolve(ctx, unnamed, timeout); err != nil {
		return nil, err
	}
	return w.fetch(ctx, "", "")
}

// ObjectsNamed returns every object in the region with a given name.
//
// Every object has to have been asked about for its name to be known,
// so the first call costs what AllObjects costs.  A region with
// nothing of that name in it is not distinguishable from one where the
// thing is beyond the draw distance.
func (w *World) ObjectsNamed(ctx context.Context, name string, timeout time.Duration) ([]*Seen, error) {
	if _, err := w.AllObjects(ctx, timeout); err != nil {
		return nil, err
	}
	return w.fetch(ctx, name, "")
}

// Known is how many objects the session has heard about, without
// naming any of them.
func (w *World) Known(ctx context.Context) (int, error) {
	r, err := w.c.Objects(ctx, "", "")
	if err != nil {
		return 0, err
	}
	return int(r.Known), nil
}

// fetch asks the server for its picture of the region.
func (w *World) fetch(ctx context.Context, named, id string) ([]*Seen, error) {
	r, err := w.c.Objects(ctx, named, id)
	if err != nil {
		return nil, err
	}
	out := make([]*Seen, 0, len(r.Objects))
	for _, o := range r.Objects {
		oid, err := msg.ParseUUID(o.Id)
		if err != nil {
			continue
		}
		owner, _ := msg.ParseUUID(o.Owner)
		out = append(out, &Seen{
			Object:       Object{ID: oid, Local: o.Local, Name: o.Name},
			Owner:        owner,
			Position:     fromPB(o.Position),
			Scale:        fromPB(o.Scale),
			Parent:       o.Parent,
			PCode:        uint8(o.Pcode),
			TextureEntry: o.TextureEntry,
			Text:         o.Text,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Local < out[j].Local })
	return out, nil
}

// resolveBatch is how many name requests go out before pausing.  The
// simulator will answer a burst, but a region with several hundred
// objects in it is worth asking about in instalments.
const resolveBatch = 40

// resolve asks for the names of these objects and waits for the
// answers to stop arriving.
func (w *World) resolve(ctx context.Context, want []msg.UUID, timeout time.Duration) error {
	if len(want) == 0 {
		return nil
	}
	for i := 0; i < len(want); i += resolveBatch {
		end := min(i+resolveBatch, len(want))
		for _, id := range want[i:end] {
			if err := w.Send(ctx, w.familyRequest(id)); err != nil {
				return err
			}
		}
		if end < len(want) {
			if err := w.Settle(ctx, 300*time.Millisecond); err != nil {
				return err
			}
		}
	}

	// Wait for as many as are going to come.  Some never will -- an
	// object can go away between being described and being asked about
	// -- so this gives up when the answers stop rather than insisting
	// on all of them.
	//
	// Giving up needs several quiet rounds and not one.  The first
	// answer takes a moment, and stopping at the first second with
	// nothing new in it stops before anything has arrived at all,
	// which looks exactly like a region full of nameless objects.
	const quietRounds = 4
	deadline := time.Now().Add(timeout)
	last, quiet := -1, 0
	for time.Now().Before(deadline) {
		w.mu.Lock()
		have := 0
		for _, id := range want {
			if _, ok := w.names[id]; ok {
				have++
			}
		}
		w.mu.Unlock()
		if have == len(want) {
			return nil
		}
		if have == last {
			if quiet++; quiet >= quietRounds {
				return nil
			}
		} else {
			quiet = 0
		}
		last = have
		if err := w.Settle(ctx, time.Second); err != nil {
			return err
		}
	}
	return nil
}

// Region is what the simulator said about itself when the avatar
// arrived.
type Region struct {
	ID     msg.UUID
	Handle uint64
	Name   string

	Flags    uint32
	Extended uint64

	Access        uint8
	Owner         msg.UUID
	EstateManager bool

	WaterHeight float32

	ProductName string
	ProductSKU  string
	ColoName    string
	CPUClass    int32
	CPURatio    int32
	Protocols   uint64
}

// Region reports what the simulator said about itself.
//
// It says it once, in the handshake, before any client is listening,
// so this comes from the server rather than from anything a client
// could have heard.
func (w *World) Region(ctx context.Context) (*Region, error) {
	r, err := w.c.Region(ctx)
	if err != nil {
		return nil, err
	}
	if !r.Known {
		return nil, fmt.Errorf("world: the region handshake has not arrived")
	}
	id, _ := msg.ParseUUID(r.Id)
	owner, _ := msg.ParseUUID(r.Owner)
	return &Region{
		ID: id, Handle: r.Handle, Name: r.Name,
		Flags: r.Flags, Extended: r.FlagsExtended,
		Access: uint8(r.Access), Owner: owner,
		EstateManager: r.EstateManager,
		WaterHeight:   r.WaterHeight,
		ProductName:   r.ProductName, ProductSKU: r.ProductSku,
		ColoName: r.ColoName,
		CPUClass: r.CpuClass, CPURatio: r.CpuRatio,
		Protocols: r.Protocols,
	}, nil
}

// Flush empties the server's object cache.
//
// The server does this itself when the region changes.  This is for a
// client that knows the cache is wrong for a reason the server cannot
// see.
func (w *World) Flush(ctx context.Context) (int, error) {
	return w.c.Flush(ctx)
}

// parseUUIDOrZero reads a uuid, treating anything unreadable as none.
// The wire carries it as a string because a zero uuid and an absent one
// mean the same thing here: no group.
func parseUUIDOrZero(s string) msg.UUID {
	id, err := msg.ParseUUID(s)
	if err != nil {
		return msg.UUID{}
	}
	return id
}

// ActiveGroup is the group the avatar is acting as.
func (w *World) ActiveGroup(ctx context.Context) (msg.UUID, error) {
	p, err := w.Where(ctx)
	if err != nil {
		return msg.UUID{}, err
	}
	return p.ActiveGroup, nil
}
