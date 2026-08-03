package agent

import (
	"context"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
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

	// Name is only known if something asked; an ObjectUpdate carries
	// none.  Owner comes either from asking or from a compressed
	// update, which does carry it.
	Name  string
	Owner msg.UUID

	// TextureEntry is the per face appearance, still packed.  Most of
	// the time it arrives only in a compressed update, since a full
	// ObjectUpdate is sent when an object first appears and appearance
	// changes after that come the compressed way.
	TextureEntry []byte

	// Text is the floating text above the object, when it has any.
	Text string

	// First and Last are when the simulator first and last said
	// anything about this object.  Last is what an age based sweep
	// would work from, if one turns out to be needed.
	First time.Time
	Last  time.Time
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
	now := time.Now()
	if v == nil {
		v = &Object{ID: id, First: now}
		o.byID[id] = v
	}
	v.Last = now
	return v
}

// Flush forgets everything.
//
// A region's objects are described once on arrival, so the cache is
// only correct for the region it was filled in.  Crossing to another
// one leaves it describing somewhere else entirely, and there is no
// message that says "forget all that".
func (o *Objects) Flush() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(o.byID)
	o.byID = map[msg.UUID]*Object{}
	return n
}

// TrimMargin is how far past the draw distance an object is kept.
//
// Trimming at exactly the draw distance would fight the simulator over
// anything sitting on the boundary: dropped, described again, dropped
// again.  The margin costs a few entries and stops the flapping.
const TrimMargin = 32

// orphanGrace is how long a child is kept whose root has not been
// described.  Object updates arrive in no particular order, so a child
// can genuinely precede its root by a moment.
const orphanGrace = time.Minute

// Trim forgets objects further from the camera than the draw distance,
// and returns how many went.
//
// This is what keeps the cache honest, and age would not do it.  The
// simulator says when an object has been destroyed, so nothing that
// still exists needs ageing out -- but it says nothing at all when one
// is merely left behind, and an object sitting still is never
// mentioned again either.  Ageing would throw away the quiet ones and
// keep the distant ones, which is exactly backwards.  Distance is the
// thing actually being asked about, and both positions are known.
//
// A child's position is relative to its root, so children are judged
// by where their root is and go with it.  An orphan whose root never
// turned up goes once it is old enough to be sure the root is not
// coming.
func (o *Objects) Trim(camera msg.Vector3, drawDistance float32) int {
	if drawDistance <= 0 {
		return 0
	}
	limit := drawDistance + TrimMargin
	limit2 := limit * limit

	o.mu.Lock()
	defer o.mu.Unlock()

	roots := make(map[uint32]msg.Vector3, len(o.byID))
	for _, v := range o.byID {
		if v.Parent == 0 {
			roots[v.Local] = v.Position
		}
	}

	n := 0
	for id, v := range o.byID {
		at := v.Position
		if v.Parent != 0 {
			p, ok := roots[v.Parent]
			if !ok {
				// An orphan: its root has not been described.  Give
				// it a grace period, since updates arrive in no
				// particular order, and then let it go -- a root that
				// has not turned up by now is one that was refused
				// for being out of range, and its children are out of
				// range too.
				if time.Since(v.First) > orphanGrace {
					delete(o.byID, id)
					n++
				}
				continue
			}
			at = p
		}
		if dist2(at, camera) > limit2 {
			delete(o.byID, id)
			n++
		}
	}
	return n
}

func dist2(a, b msg.Vector3) float32 {
	dx, dy, dz := a.X-b.X, a.Y-b.Y, a.Z-b.Z
	return dx*dx + dy*dy + dz*dz
}

// update records what an ObjectUpdate said, unless it is about
// something beyond the draw distance.
//
// Refusing it here as well as trimming later is worth the check.  The
// simulator does not describe what is out of range, so most of the
// time this rejects nothing -- but after the avatar has moved it stops
// the far end of the old view being taken back in from a stray update
// before the next trim comes round.
//
// A child is judged by its root, and a child whose root is not known
// yet is taken in: object updates arrive in no particular order, and a
// child that turns up first would otherwise be thrown away and never
// mentioned again.  Trim clears up the ones whose root never came.
func (o *Objects) update(d *msg.ObjectUpdate_ObjectData, camera msg.Vector3, drawDistance float32) {
	pos, rot, havePos := msg.DecodePlacement(d.ObjectData)

	o.mu.Lock()
	defer o.mu.Unlock()

	if drawDistance > 0 && havePos {
		limit := drawDistance + TrimMargin
		at, judge := pos, true
		if d.ParentID != 0 {
			at, judge = o.rootPosLocked(d.ParentID)
		}
		if judge && dist2(at, camera) > limit*limit {
			delete(o.byID, d.FullID)
			return
		}
	}

	v := o.seen(d.FullID)
	v.Local, v.Parent, v.PCode, v.Scale = d.ID, d.ParentID, d.PCode, d.Scale
	if havePos {
		v.Position, v.Rotation = pos, rot
	}
}

// compressed records what a compressed update said.
//
// It carries more than a full update does -- the owner, the floating
// text, the appearance -- so this fills in things nothing else would.
func (o *Objects) compressed(c *msg.Compressed, camera msg.Vector3, drawDistance float32) {
	parent := uint32(0)
	if c.ParentID != nil {
		parent = *c.ParentID
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if drawDistance > 0 {
		limit := drawDistance + TrimMargin
		at, judge := c.Position, true
		if parent != 0 {
			at, judge = o.rootPosLocked(parent)
		}
		if judge && dist2(at, camera) > limit*limit {
			delete(o.byID, c.FullID)
			return
		}
	}

	v := o.seen(c.FullID)
	v.Local, v.Parent, v.PCode = c.LocalID, parent, c.PCode
	v.Scale, v.Position, v.Rotation = c.Scale, c.Position, c.Rotation
	if !c.Owner.IsZero() {
		v.Owner = c.Owner
	}
	if len(c.TextureEntry) > 0 {
		v.TextureEntry = c.TextureEntry
	}
	if c.Text != "" {
		v.Text = c.Text
	}
}

// moved records a terse update.
//
// It only updates something already known.  A terse update names an
// object by local id alone, so one for something never described is
// not enough to make an entry with -- there would be nothing to say
// what it is.
func (o *Objects) moved(t *msg.Terse) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, v := range o.byID {
		if v.Local == t.LocalID {
			v.Position, v.Rotation = t.Position, t.Rotation
			v.Last = time.Now()
			return
		}
	}
}

// rootPosLocked finds where a root is by its local id.
func (o *Objects) rootPosLocked(local uint32) (msg.Vector3, bool) {
	for _, v := range o.byID {
		if v.Local == local && v.Parent == 0 {
			return v.Position, true
		}
	}
	return msg.Vector3{}, false
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
		l := a.Look()
		for i := range m.ObjectData {
			a.Objects.update(&m.ObjectData[i], l.Center, l.Far)
		}
	}, msg.Inline())

	// ObjectUpdateCompressed is how most updates arrive after an
	// object has first been described.  A session that ignores it sees
	// the world as it was on arrival and never learns otherwise.
	a.Disp.MustHandle("ObjectUpdateCompressed", func(p *msg.Packet) {
		m := p.Message.(*msg.ObjectUpdateCompressed)
		l := a.Look()
		for i := range m.ObjectData {
			c, err := msg.DecodeCompressed(m.ObjectData[i].Data)
			if c == nil {
				continue
			}
			// A partly decoded object still says where it is and what
			// it is, which is what the cache is for.  The error is
			// worth nothing here beyond not trusting the tail.
			_ = err
			a.Objects.compressed(c, l.Center, l.Far)
		}
	}, msg.Inline())

	// ImprovedTerseObjectUpdate is the message the simulator sends
	// most: everything that moves, several times a second.  It carries
	// only a local id and a position, so it updates what is already
	// known rather than introducing anything.
	a.Disp.MustHandle("ImprovedTerseObjectUpdate", func(p *msg.Packet) {
		m := p.Message.(*msg.ImprovedTerseObjectUpdate)
		for i := range m.ObjectData {
			t, err := msg.DecodeTerse(m.ObjectData[i].Data)
			if err != nil {
				continue
			}
			a.Objects.moved(t)
		}
	}, msg.Inline())

	a.Disp.MustHandle("KillObject", func(p *msg.Packet) {
		m := p.Message.(*msg.KillObject)
		for _, d := range m.ObjectData {
			a.Objects.kill(d.ID)
		}
	}, msg.Inline())

	// ObjectUpdateCached is the simulator saying "you have these
	// already": local ids and CRCs, and no content whatsoever.  A
	// viewer with a disk cache checks each CRC against what it stored
	// last visit and asks only for what it is missing.  This client has
	// no cache, so every one of them is a miss and every one has to be
	// asked for.
	//
	// Ignoring it costs almost everything that was in the region before
	// we arrived, while leaving freshly rezzed objects working
	// perfectly -- those arrive as full ObjectUpdates.  That asymmetry
	// is why it went unnoticed: every experiment written here rezzes
	// the object it works on.  Pointed at a prim that was already
	// there, the session could not see it at all.
	//
	// It is also why counting unhandled MESSAGES made this look
	// trivial.  ObjectData is a variable block, so the whole region can
	// arrive in one packet, and one packet is what the count showed.
	a.Disp.MustHandle("ObjectUpdateCached", func(p *msg.Packet) {
		m := p.Message.(*msg.ObjectUpdateCached)
		ids := make([]uint32, len(m.ObjectData))
		for i := range m.ObjectData {
			ids[i] = m.ObjectData[i].ID
		}
		// Off the dispatch goroutine: this can be several packets and
		// nothing else can be decoded while it sends.
		go a.requestCachedObjects(ids)
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

// requestCachedObjects asks the simulator to describe objects it
// believes we already hold.
//
// The cache miss type says what we have: 0 is nothing at all, 1 is a
// copy whose CRC disagrees.  This client caches nothing between
// sessions, so it is always 0.
//
// Sent in batches because the request has to fit in a datagram.  Each
// block is five bytes, so a hundred is comfortable, and a simulator
// that cannot parse an oversized request answers none of it rather than
// the part that fitted.
func (a *Agent) requestCachedObjects(ids []uint32) {
	const batch = 100
	for len(ids) > 0 {
		n := min(batch, len(ids))
		req := &msg.RequestMultipleObjects{}
		req.AgentData.AgentID = a.Account.AgentID
		req.AgentData.SessionID = a.Account.SessionID
		req.ObjectData = make([]msg.RequestMultipleObjects_ObjectData, n)
		for i, id := range ids[:n] {
			req.ObjectData[i] = msg.RequestMultipleObjects_ObjectData{CacheMissType: 0, ID: id}
		}
		if err := a.Send.Send(context.Background(), req); err != nil {
			return
		}
		ids = ids[n:]
	}
}
