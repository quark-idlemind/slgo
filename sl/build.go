package sl

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
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
// returned as it is, with nothing to link.
//
// Each prim is rezzed and confirmed before the next, rather than
// rezzing them all and sorting out afterwards which is which.  Objects
// stream in continuously, so the only way to know which new object is
// ours is to ask about each one as it appears, and doing that for a
// batch means matching answers to intentions with nothing to match on
// -- two prims of the same size at the same place are indistinguishable
// once both exist.
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
		if err != nil {
			return nil, fmt.Errorf("sl: prim %d of %d (%q): %w",
				i+1, len(prims), p.Name, err)
		}
		b.Parts = append(b.Parts, o)
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
func (w *Session) buildOne(ctx context.Context, p Prim) (*Object, error) {
	size := p.Size
	if size == (msg.Vector3{}) {
		size = msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}
	}

	o, err := w.rezAt(ctx, p.Position, size, p.Rotation)
	if err != nil {
		return nil, err
	}

	// Say where it goes rather than trusting where it landed.  A rez
	// is a request: the simulator places the prim near the ray and
	// need not put it exactly there.
	if err := w.Place(ctx, o, p.Position, p.Rotation, size); err != nil {
		return nil, err
	}

	if p.Name != "" {
		if err := w.SetName(ctx, o, p.Name); err != nil {
			return nil, err
		}
	}
	if p.Description != "" {
		if err := w.SetDescription(ctx, o, p.Description); err != nil {
			return nil, err
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
func (w *Session) rezAt(ctx context.Context, at, scale msg.Vector3, rot msg.Quaternion) (*Object, error) {
	w.mu.Lock()
	before := make(map[uint32]bool, len(w.locals))
	for _, l := range w.locals {
		before[l] = true
	}
	w.mu.Unlock()

	m := &msg.ObjectAdd{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	d := &m.ObjectData
	d.PCode, d.Material, d.AddFlags = 9, 3, 2
	d.PathCurve, d.ProfileCurve = 16, 1
	d.PathScaleX, d.PathScaleY = 100, 100
	d.BypassRaycast = 1
	d.RayStart, d.RayEnd = at, at
	d.Scale = scale
	d.Rotation = rot
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}
	return w.findOurs(ctx, before, 15*time.Second)
}

// findOurs waits for an object we own that was not there before.
//
// Ownership is confirmed rather than assumed.  A local id that is new
// to this session is not necessarily one we just made: objects stream
// in the whole time, and building on somebody else's prim by mistake
// fails later in ways that look like something else.
func (w *Session) findOurs(ctx context.Context, before map[uint32]bool, timeout time.Duration) (*Object, error) {
	var found *Object
	asked := map[msg.UUID]bool{}
	err := w.await(ctx, timeout, "a prim of ours to appear", func() bool {
		var toAsk []msg.UUID
		for id, local := range w.locals {
			// Local id 0 is an attachment and is never a fresh rez.
			if local == 0 || before[local] {
				continue
			}
			if w.owners[id] == w.me {
				found = &Object{ID: id, Local: local}
				return true
			}
			if !asked[id] {
				asked[id] = true
				toAsk = append(toAsk, id)
			}
		}
		for _, id := range toAsk {
			q := &msg.RequestObjectPropertiesFamily{}
			q.AgentData.AgentID, q.AgentData.SessionID = w.me, w.sess
			q.ObjectData.ObjectID = id
			_ = w.b.Send(ctx, q, true)
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	return found, nil
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
