package agent

import (
	"sync"

	"slgo/msg"
)

// What the simulator says about the objects around the avatar is said
// once.
//
// A region describes itself when the avatar arrives and then only
// mentions what changes.  A client that attaches a minute later is
// never told about any of it, and asking the simulator to say it again
// is not something the protocol offers.  So the session remembers,
// because the session is the thing that was there to hear it.
//
// This is the same shape as the camera: state that arrives once, that
// everything else depends on, and that would be lost by a client
// restart.  It belongs on this side for that reason and no other --
// the server still has no idea what any of these objects are for.

// Object is what is known about one object in the region.
type Object struct {
	ID     msg.UUID
	Local  uint32
	Parent uint32

	// PCode says what kind of thing it is: 9 a prim, 47 an avatar.
	PCode uint8

	Scale    msg.Vector3
	Position msg.Vector3
	Rotation msg.Quaternion

	// Name and Owner are only known if something asked.  An
	// ObjectUpdate carries neither.
	Name  string
	Owner msg.UUID
}

// Objects is what the session has been told about the region.
//
// It accumulates.  Objects are added as the simulator describes them
// and removed when it says they have gone, so it tracks what is around
// rather than growing without bound -- but it is a record of what has
// been heard, not a query against the region.  Lowering the draw
// distance does not retroactively forget what was already described:
// raising it from 128 to 256 metres took the count from 195 to 217,
// and dropping it to 32 left it at 193.
type Objects struct {
	mu   sync.RWMutex
	byID map[msg.UUID]*Object
}

func newObjects() *Objects {
	return &Objects{byID: map[msg.UUID]*Object{}}
}

// All returns a copy of everything known.
func (o *Objects) All() []*Object {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]*Object, 0, len(o.byID))
	for _, v := range o.byID {
		c := *v
		out = append(out, &c)
	}
	return out
}

// Get returns one object by id.
func (o *Objects) Get(id msg.UUID) (*Object, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	v, ok := o.byID[id]
	if !ok {
		return nil, false
	}
	c := *v
	return &c, true
}

// Count is how many objects are known.
func (o *Objects) Count() int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return len(o.byID)
}

func (o *Objects) seen(id msg.UUID) *Object {
	v := o.byID[id]
	if v == nil {
		v = &Object{ID: id}
		o.byID[id] = v
	}
	return v
}

func (o *Objects) update(d *msg.ObjectUpdate_ObjectData) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v := o.seen(d.FullID)
	v.Local, v.Parent, v.PCode, v.Scale = d.ID, d.ParentID, d.PCode, d.Scale
	if pos, rot, ok := msg.DecodePlacement(d.ObjectData); ok {
		v.Position, v.Rotation = pos, rot
	}
}

func (o *Objects) named(id msg.UUID, name string, owner msg.UUID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v := o.seen(id)
	v.Name, v.Owner = name, owner
}

// kill forgets an object by local id.
//
// KillObject names the local id and not the object id, so this is a
// scan.  It happens rarely enough not to matter.
func (o *Objects) kill(local uint32) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for id, v := range o.byID {
		if v.Local == local {
			delete(o.byID, id)
			return
		}
	}
}

// trackObjects registers the handlers that keep the registry current.
//
// They are inline so that the picture is up to date before anything
// dispatched after them looks at it.
func (a *Agent) trackObjects() {
	a.Disp.MustHandle("ObjectUpdate", func(p *msg.Packet) {
		m := p.Message.(*msg.ObjectUpdate)
		for i := range m.ObjectData {
			a.Objects.update(&m.ObjectData[i])
		}
	}, msg.Inline())

	a.Disp.MustHandle("KillObject", func(p *msg.Packet) {
		m := p.Message.(*msg.KillObject)
		for _, d := range m.ObjectData {
			a.Objects.kill(d.ID)
		}
	}, msg.Inline())

	// Names and owners come only from asking, and any client may be
	// the one that asked.  Remembering the answers means the next
	// client does not have to ask again.
	a.Disp.MustHandle("ObjectPropertiesFamily", func(p *msg.Packet) {
		m := p.Message.(*msg.ObjectPropertiesFamily)
		a.Objects.named(m.ObjectData.ObjectID,
			trimNul(m.ObjectData.Name), m.ObjectData.OwnerID)
	}, msg.Inline())

	a.Disp.MustHandle("ObjectProperties", func(p *msg.Packet) {
		m := p.Message.(*msg.ObjectProperties)
		for i := range m.ObjectData {
			d := &m.ObjectData[i]
			a.Objects.named(d.ObjectID, trimNul(d.Name), d.OwnerID)
		}
	}, msg.Inline())
}
