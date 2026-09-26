package sl

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// ErrOutOfRange is reported for a position the simulator would not
// describe back to us.
var ErrOutOfRange = errors.New("sl: beyond the draw distance")

// Prim describes one prim of an object to build.
//
// Position is in region coordinates rather than relative to the root.
// Linking preserves where things already are, so a shape is described
// by saying where each piece goes and letting the link keep it there.
type Prim struct {
	Name        string
	Description string

	// Position in region coordinates.
	Position msg.Vector3

	// Size is the prim's scale.  Zero means half a metre cubed.
	Size msg.Vector3

	// Rotation.  The zero value is no rotation: a quaternion goes over
	// the wire as three floats with the fourth recovered by
	// normalising, and all three zero recovers a W of one.
	Rotation msg.Quaternion

	// Shape is what the prim is: a box, a sphere, a torus with a hole
	// in it.  The zero value is a box, since a prim asked for with
	// nothing said about it has always been one.
	Shape Shape
}

// Built is an object that was built.
type Built struct {
	// Root is the object, and the thing to pass to anything that takes
	// one -- scripts run in the root, and the root is what gets taken
	// into inventory.
	Root *Object

	// Parts are the prims in the order they were described, so
	// Parts[0] is Root.  A single prim object has one part.
	Parts []*Object
}

// Update types for MultipleObjectUpdate, from the viewer's
// llprimitive.h.  The payload carries position, then rotation, then
// scale, in that order, twelve bytes each, and only for the bits that
// are set.
const (
	updPosition = 0x01
	updRotation = 0x02
	updScale    = 0x04
)

// Build creates an object from a list of prims and links them into
// one, with the first prim as the root.
//
// One prim or many goes through the same call: a single prim is
// returned as it is, with nothing to link.  Every prim is made in the
// avatar's active group, as a viewer makes one.
//
// Each prim is rezzed and confirmed before the next, rather than
// rezzing them all and sorting out afterwards which is which.  Objects
// stream in continuously, so the only way to know which new object is
// ours is to ask about each one as it appears, and doing that for a
// batch means matching answers to intentions with nothing to match on
// -- two prims of the same size at the same place are indistinguishable
// once both exist.
//
// A build that fails part way returns what it made alongside the
// error: every prim that was rezzed, the one that failed among them if
// it got as far as existing, unlinked if the link was not reached.
// They are standing in the region, and clearing them away is the
// caller's to decide.  A nil Built means nothing was made that this
// call knows of -- a rez that was never confirmed may still have made a
// prim, but nothing says which one it is.
func (w *Session) Build(ctx context.Context, prims []Prim) (*Built, error) {
	if len(prims) == 0 {
		return nil, fmt.Errorf("sl: Build needs at least one prim")
	}

	// Check every position before rezzing any of them.  Checking as we
	// go would leave the prims built so far standing where nothing can
	// see them, which is a worse place to stop than not having started.
	p, err := w.Where(ctx)
	if err != nil {
		return nil, err
	}
	for i := range prims {
		if err := inRange(p, prims[i].Position); err != nil {
			return nil, fmt.Errorf("sl: prim %d of %d (%q): %w",
				i+1, len(prims), prims[i].Name, err)
		}
	}

	b := &Built{Parts: make([]*Object, 0, len(prims))}
	for i, p := range prims {
		o, err := w.buildOne(ctx, p)
		if o != nil {
			b.Parts = append(b.Parts, o)
		}
		if err != nil {
			err = fmt.Errorf("sl: prim %d of %d (%q): %w",
				i+1, len(prims), p.Name, err)
			if len(b.Parts) == 0 {
				return nil, err
			}
			b.Root = b.Parts[0]
			return b, err
		}
	}
	b.Root = b.Parts[0]

	if len(b.Parts) == 1 {
		return b, nil
	}
	if err := w.Link(ctx, b.Root, b.Parts[1:]...); err != nil {
		return b, fmt.Errorf("sl: linking %d prims onto %s: %w",
			len(b.Parts)-1, b.Root, err)
	}
	return b, nil
}

// buildOne rezzes a prim and makes it match its description.
//
// Once the rez is confirmed the prim exists, so a later step failing
// returns it with the error rather than losing its ids.
func (w *Session) buildOne(ctx context.Context, p Prim) (*Object, error) {
	size := p.Size
	if size == (msg.Vector3{}) {
		size = msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}
	}

	o, err := w.rezAt(ctx, p.Position, size, p.Rotation, p.Shape)
	if err != nil {
		return nil, err
	}

	// Say where it goes rather than trusting where it landed.  The
	// simulator puts the prim's bottom on the point rather than its
	// centre, and lifts one asked for underground onto the ground.
	if err := w.Place(ctx, o, p.Position, p.Rotation, size); err != nil {
		return o, err
	}

	if p.Name != "" {
		if err := w.SetName(ctx, o, p.Name); err != nil {
			return o, err
		}
	}
	if p.Description != "" {
		if err := w.SetDescription(ctx, o, p.Description); err != nil {
			return o, err
		}
	}
	return o, nil
}

// Place sets an object's position, rotation and scale in one message.
func (w *Session) Place(ctx context.Context, o *Object, at msg.Vector3, rot msg.Quaternion, scale msg.Vector3) error {
	data := make([]byte, 0, 36)
	data = appendVector(data, at)
	data = appendQuaternion(data, rot)
	data = appendVector(data, scale)

	m := &msg.MultipleObjectUpdate{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData = []msg.MultipleObjectUpdate_ObjectData{{
		ObjectLocalID: o.Local,
		Type:          updPosition | updRotation | updScale,
		Data:          data,
	}}
	return w.Send(ctx, m)
}

// SetDescription sets an object's description and reads it back.
//
// ObjectDescription has no reply, and the description only comes back
// in the full properties, which have to be asked for by selecting the
// object.  Confirming costs a round trip, and not confirming cost more
// than that: building one prim and reading its properties straight
// afterwards reported an empty description, because the read overtook
// the write.  Building three hid it, since the other two took long
// enough for the value to land.
func (w *Session) SetDescription(ctx context.Context, o *Object, desc string) error {
	m := &msg.ObjectDescription{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData = []msg.ObjectDescription_ObjectData{
		{LocalID: o.Local, Description: append([]byte(desc), 0)},
	}
	if err := w.Send(ctx, m); err != nil {
		return err
	}

	var last string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		p, err := w.Properties(ctx, o, 5*time.Second)
		if err != nil {
			// A timeout means ask again, and has already cost five
			// seconds of the twenty.  Anything else -- the caller
			// gone, the circuit gone -- will not get better for being
			// asked faster, and this loop has no pause of its own, so
			// ignoring it span here flat out until the deadline.
			if !errors.Is(err, ErrTimeout) {
				return err
			}
			continue
		}
		if p.Description == desc {
			return nil
		}
		last = p.Description
	}
	return fmt.Errorf("%w: the description of %s, which still reads %q",
		ErrTimeout, o.ID, last)
}

// inRange refuses a position the simulator would not describe.
//
// Rezzing out there is not refused by the simulator -- the prim gets
// made, and then nothing is ever said about it, so the rez cannot be
// confirmed and cannot be told from a failure.  Better to refuse at
// once and say why than to build something invisible and time out
// looking for it.
func inRange(p *Presence, at msg.Vector3) error {
	if p.DrawDistance <= 0 {
		return nil
	}
	d := distance(at, p.Camera)
	if d <= p.DrawDistance {
		return nil
	}
	return fmt.Errorf("%w: %v is %.0f m from the camera at %v, and the draw distance is %.0f m",
		ErrOutOfRange, at, d, p.Camera, p.DrawDistance)
}

func distance(a, b msg.Vector3) float32 {
	dx, dy, dz := a.X-b.X, a.Y-b.Y, a.Z-b.Z
	return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
}

// rezAt is Rez with the scale and rotation given, and is what Build
// uses.
func (w *Session) rezAt(ctx context.Context, at, scale msg.Vector3, rot msg.Quaternion, shape ...Shape) (*Object, error) {
	// In the avatar's active group, as a viewer rezzes: land that runs
	// only its group's scripts does not run one in a prim of no group.
	// Why: doc/rez.md#the-group-a-prim-is-made-in
	group, err := w.ActiveGroup(ctx)
	if err != nil {
		return nil, err
	}

	// What the region held before, from the backend: this session has
	// only heard what was relayed since it attached.
	before, err := w.localIDs(ctx)
	if err != nil {
		return nil, err
	}

	m := &msg.ObjectAdd{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentData.GroupID = group
	d := &m.ObjectData
	d.PCode, d.Material, d.AddFlags = 9, 3, 2

	// The form to rez.  A box by default, since that is what asking
	// for a prim and saying nothing else has always meant here.
	form := DefaultShape()
	if len(shape) > 0 && shape[0].Type != "" {
		form = shape[0]
	}
	packed, err := form.Pack()
	if err != nil {
		return nil, err
	}
	packed.FillAdd(d)
	d.BypassRaycast = 1
	d.RayStart, d.RayEnd = at, at
	d.Scale = scale
	d.Rotation = rot
	if err = w.Send(ctx, m); err != nil {
		return nil, err
	}
	return w.findOurs(ctx, before, at, scale, 15*time.Second)
}

// rezSlack is how far, in metres, a new prim may be from where the
// rules in landedAt put it: more than the float32 spacing of a
// position anywhere below 8,192 m.
const rezSlack = 0.001

// rezPoll is how often findOurs looks at the region again.  Each look
// is the backend's whole object list.
const rezPoll = 250 * time.Millisecond

// landedAt reports whether a prim the region describes at p could be
// the one rezzed at at.  X and Y come back as they were sent; Z comes
// back at or above it, since the simulator puts the prim's bottom on
// the point and raises one asked for underground to rest on the ground.
// Why: doc/rez.md#where-a-new-prim-lands
func landedAt(p, at msg.Vector3) bool {
	return math.Abs(float64(p.X-at.X)) <= rezSlack &&
		math.Abs(float64(p.Y-at.Y)) <= rezSlack &&
		p.Z >= at.Z-rezSlack
}

// findOurs waits for the prim a rez at at, of this scale, made.
//
// It is a root prim that was not in the region before, stands where
// landedAt says a rez at at lands, and is owned by us.  Among several,
// the one whose centre is nearest half its height above the point is
// taken, and none is taken while a nearer one's owner is unknown.
//
// Ownership is confirmed rather than assumed: building on somebody
// else's prim by mistake fails later in ways that look like something
// else.  An owner nobody has heard is asked for, once, and never with
// the session's lock held.
// Why: doc/rez.md#how-a-rez-is-recognised
func (w *Session) findOurs(ctx context.Context, before map[uint32]bool, at, scale msg.Vector3, timeout time.Duration) (*Object, error) {
	w.mu.Lock()
	mark := len(w.alerts)
	w.mu.Unlock()

	centre := at.Z + scale.Z/2
	asked := map[msg.UUID]bool{}
	deadline := time.Now().Add(timeout)
	for {
		all, err := w.fetch(ctx, "", "")
		if err != nil {
			return nil, err
		}
		var near []*Seen
		for _, s := range all {
			// The avatar is excluded by id as well as by kind: it is
			// owned by us, and being handed it as a fresh prim is how
			// it came to be renamed once.
			if before[s.Local] || s.Parent != 0 || s.IsAvatar() || s.ID == w.me ||
				!landedAt(s.Position, at) {
				continue
			}
			near = append(near, s)
		}
		sort.SliceStable(near, func(i, j int) bool {
			return math.Abs(float64(near[i].Position.Z-centre)) <
				math.Abs(float64(near[j].Position.Z-centre))
		})

		var found *Seen
		var toAsk []msg.UUID
		w.mu.Lock()
		unknown := false
		for _, s := range near {
			owner := s.Owner
			if owner.IsZero() {
				owner = w.owners[s.ID]
			}
			if owner == w.me && !unknown {
				found = s
				break
			}
			if owner.IsZero() {
				unknown = true
				if !asked[s.ID] {
					asked[s.ID] = true
					toAsk = append(toAsk, s.ID)
				}
			}
		}
		w.mu.Unlock()

		for _, id := range toAsk {
			if err := w.Send(ctx, w.familyRequest(id)); err != nil {
				return nil, err
			}
		}
		if found != nil {
			return &Object{ID: found.ID, Local: found.Local}, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: a prim of ours to appear at %v (after %s)%s",
				ErrTimeout, at, timeout, w.alertsSince(mark))
		}
		if err := w.Settle(ctx, rezPoll); err != nil {
			return nil, err
		}
	}
}

func appendVector(b []byte, v msg.Vector3) []byte {
	b = binary.LittleEndian.AppendUint32(b, math.Float32bits(v.X))
	b = binary.LittleEndian.AppendUint32(b, math.Float32bits(v.Y))
	return binary.LittleEndian.AppendUint32(b, math.Float32bits(v.Z))
}

// appendQuaternion writes the three float wire form, which is what a
// rotation is on this protocol: the fourth component is recovered by
// normalising and is not sent.
func appendQuaternion(b []byte, q msg.Quaternion) []byte {
	b = binary.LittleEndian.AppendUint32(b, math.Float32bits(q.X))
	b = binary.LittleEndian.AppendUint32(b, math.Float32bits(q.Y))
	return binary.LittleEndian.AppendUint32(b, math.Float32bits(q.Z))
}
