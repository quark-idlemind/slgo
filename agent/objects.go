package agent

import (
	"context"
	"strings"
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

	// Velocity is how fast it was last said to be moving, in metres a
	// second, and Moved is when that was said -- which is also when
	// Position was.  Only the terse updates carry it here, and they are
	// sent for what moves, so it is zero for anything that has only ever
	// been described.  Moved is kept apart from Last because Last moves
	// on for things that say nothing about where the object is, a name
	// arriving among them, and anything reckoning where the object has
	// got to since needs the age of the position and not of the entry.
	Velocity msg.Vector3
	Moved    time.Time

	// Shape is the prim's profile and path, still packed.  Both kinds
	// of update carry it, so it is known as soon as anything is.
	Shape msg.PrimShape

	// Name is only known if something asked; an ObjectUpdate carries
	// none.  Owner comes either from asking or from a compressed
	// update, which does carry it.
	Name  string
	Owner msg.UUID

	// TextureEntry is the per face appearance, still packed.  Both
	// kinds of update carry it, and taking it from only the compressed
	// one left most prims looking untextured: a region sends a full
	// update for plenty of objects that never get a compressed one.
	TextureEntry []byte

	// Text is the floating text above the object, when it has any.
	Text string

	// AttachPoint is where a worn object is attached, and zero when it
	// is not worn.  AttachItem is the inventory item it was worn from.
	//
	// The item is the only stable name a worn object has.  The object
	// itself is rezzed afresh -- with a new id -- every time it is put
	// on, and again every time the avatar logs in, so anything that
	// wants to find the same attachment twice has to look for the item
	// it came from.  Both are read from the ObjectUpdate that describes
	// an attachment, which is sent when it goes on and again at login;
	// a program that connected afterwards never heard it, which is why
	// it is worth remembering here.
	AttachPoint int
	AttachItem  msg.UUID

	// First and Last are when the simulator first and last said
	// anything about this object.  Last is what Trim ages an orphan by.
	First time.Time
	Last  time.Time

	// leaving is when this object was first found out of everybody's
	// range, and zero while it is in somebody's.  Being out of range
	// puts an object on notice rather than out of the store: see
	// OutOfRangeGrace.
	leaving time.Time
}

// Objects is what the session has been told about the region.
//
// It accumulates.  Objects are added as the simulator describes them
// and removed when it says they have gone, so it tracks what is around
// rather than growing without bound -- but it is a record of what has
// been heard, not a query against the region.
//
// It did not always forget what it had heard.  Measured before Trim
// was written: raising the draw distance from 128 to 256 metres took
// the count from 195 to 217, and dropping it to 32 left it at 193 --
// the store kept everything the far view had brought in.  Those
// numbers are what it did then, and are left here because they turn up
// elsewhere and would otherwise read as current.  Trim now drops what
// is out of range, and the agent calls it every TrimInterval, so a
// store does shrink when the camera pulls in -- a little behind the
// camera rather than with it.
//
// A store can be shared by several agents in the same region -- see
// Cache -- which is what viewers is for: what to keep is decided by
// everyone looking, not by whoever happened to hear the update.
type Objects struct {
	mu      sync.RWMutex
	byID    map[msg.UUID]*Object
	viewers map[string]viewpoint
}

// A viewpoint is one avatar's camera and how far it is being told
// about.  Far of zero means that avatar has no limit, so nothing is
// dropped on its account.
type viewpoint struct {
	camera msg.Vector3
	far    float32
}

func newObjects() *Objects {
	return &Objects{byID: map[msg.UUID]*Object{}, viewers: map[string]viewpoint{}}
}

// Watch registers where an agent is looking from.
//
// It is what stops one avatar's walking away from throwing out what
// another is standing in front of: every viewpoint gets a say in what
// the store keeps.  The key is the agent's, and registering again moves
// that viewpoint rather than adding one.
func (o *Objects) Watch(key string, camera msg.Vector3, far float32) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.viewers[key] = viewpoint{camera: camera, far: far}
}

// Unwatch takes an agent's viewpoint away, which is what leaving the
// region or logging out amounts to.  A viewpoint left behind would go
// on keeping objects alive for an avatar that is not there.
func (o *Objects) Unwatch(key string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.viewers, key)
}

// Watchers is how many agents are looking at this store.
func (o *Objects) Watchers() int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return len(o.viewers)
}

// keepLocked reports whether something at a point is close enough to
// anybody to be worth keeping.
//
// The caller's own viewpoint is passed rather than looked up, because
// the update being judged came with the position the camera had when it
// arrived, and because an agent whose first update lands before its
// first Watch would otherwise be judged by nobody.
func (o *Objects) keepLocked(at, camera msg.Vector3, far float32) bool {
	if within(at, camera, far) {
		return true
	}
	for _, v := range o.viewers {
		if within(at, v.camera, v.far) {
			return true
		}
	}
	return false
}

// within is one viewpoint's answer.  No draw distance means no limit,
// so everything is within it.
func within(at, camera msg.Vector3, far float32) bool {
	if far <= 0 {
		return true
	}
	limit := far + TrimMargin
	return dist2(at, camera) <= limit*limit
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

// absorb takes in what another store holds, keeping what this one
// already has.
//
// It is for the moment an agent joins a shared store: what it heard
// before the region named itself is this region's, and dropping it
// would lose objects that nothing will describe again.  What is already
// here came from an agent that has been in the region longer, so it is
// not worth overwriting with a newcomer's copy.
func (o *Objects) absorb(from *Objects) int {
	if from == nil || from == o {
		return 0
	}
	taken := from.All()

	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, v := range taken {
		if _, have := o.byID[v.ID]; have {
			continue
		}
		o.byID[v.ID] = v
		n++
	}
	return n
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

// OutOfRangeGrace is how long an object found out of range is kept
// before it is dropped for it.
//
// Out of range is judged against cameras, and a camera can be wrong for
// a moment.  At login the camera is put on the avatar before anything
// has said where the avatar is, so for a second or so it looks out from
// the region's corner; everything described in that second -- and the
// region describes the most in the seconds after arriving -- was judged
// from there and thrown away, and the region describes each object
// once.  Measured on Agni: an avatar sitting on a chair whose
// description arrived in that window stayed "sitting on something not
// described here" for as long as the session lasted.
//
// So an object out of range is noticed first and dropped only if it is
// still out of range this long afterwards.  Coming back into anybody's
// range in between takes it off notice.  Two trim ticks, so that the
// decision is always made on a camera read at least one tick after the
// one that put it on notice.  The cost is holding a little of the view
// just left for half a minute longer.
const OutOfRangeGrace = 2 * TrimInterval

// orphanGrace is how long a child is kept after the last word about it,
// when nothing here says where its root is.
//
// It has to be measured from the last mention rather than the first.
// Updates arrive in no particular order, so a child can precede its
// root -- but a region also goes on describing prims whose roots it
// never describes to us at all, and dropping those on a timer only
// means taking them straight back.
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
// turned up goes once the region has said nothing about it for
// orphanGrace, and what hangs off an avatar whose seat is not known
// stays with the avatar.
//
// Everyone watching the store gets a say: an object is kept if it is
// within ANY viewpoint's draw distance.  A store shared by three
// avatars in three corners of a region holds what all three can see,
// and none of them walking away throws out another's view.
func (o *Objects) Trim(camera msg.Vector3, drawDistance float32) int {
	if drawDistance <= 0 {
		return 0
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()

	// Where everything is, resolved once.  A child's position is an
	// offset, so what decides its fate is where its root is standing --
	// and an attachment's root hangs off an avatar rather than off
	// nothing, which is why this walks up rather than looking for a
	// parent with no parent of its own.
	byLocal := make(map[uint32]*Object, len(o.byID))
	for _, v := range o.byID {
		byLocal[v.Local] = v
	}
	//
	// A walk that breaks off above a person is not an orphan.  A seated
	// avatar's parent is its seat, and a seat is a prim like any other,
	// so the walk carries on up into it -- and when the seat had not
	// been described, every attachment the avatar wore used to count as
	// an orphan and go a minute later.  The person is kept whatever
	// happens (see pcodeAvatar), so what it wears is kept with it until
	// the seat turns up and there is somewhere to judge them from.
	// Measured on Agni: an avatar sitting on a chair nothing had
	// described, all ten attachments described at 19:39 and none of
	// them by 20:00, with the avatar never having moved.
	anchor := func(v *Object) (at msg.Vector3, known, onPerson bool) {
		for up := 0; up < 8; up++ {
			if up > 0 && v.PCode == pcodeAvatar {
				onPerson = true
			}
			if v.Parent == 0 {
				return v.Position, true, onPerson
			}
			p := byLocal[v.Parent]
			if p == nil {
				return msg.Vector3{}, false, onPerson
			}
			v = p
		}
		return msg.Vector3{}, false, onPerson
	}

	n := 0
	for id, v := range o.byID {
		if v.PCode == pcodeAvatar {
			// See pcodeAvatar: people are kept whatever the distance.
			continue
		}
		at, known, onPerson := anchor(v)
		if !known && onPerson {
			continue
		}
		if !known {
			// An orphan: nothing here says where it is.  It goes once
			// the region has stopped mentioning it, which is the only
			// evidence available that it is no longer worth keeping.
			//
			// Since it was FIRST heard was the wrong clock.  A region
			// goes on describing a prim whose root it never describes
			// to us, and this is an unjudgeable update, so it is taken
			// back in as soon as it is dropped: measured on a live
			// region, one such prim was deleted and re-created every
			// minute for hours, losing its name each time and costing
			// a fresh name lookup to get it back.  A thing the
			// simulator keeps talking about is a thing that is there.
			if time.Since(v.Last) > orphanGrace {
				delete(o.byID, id)
				n++
			}
			continue
		}
		if o.keepLocked(at, camera, drawDistance) {
			v.leaving = time.Time{}
			continue
		}
		if o.goneLocked(v, now) {
			delete(o.byID, id)
			n++
		}
	}
	return n
}

// goneLocked puts an object that is out of range on notice, and says
// whether it has been out of range long enough to drop.  See
// OutOfRangeGrace.
func (o *Objects) goneLocked(v *Object, now time.Time) bool {
	if v.leaving.IsZero() {
		v.leaving = now
		return false
	}
	return now.Sub(v.leaving) >= OutOfRangeGrace
}

func dist2(a, b msg.Vector3) float32 {
	dx, dy, dz := a.X-b.X, a.Y-b.Y, a.Z-b.Z
	return dx*dx + dy*dy + dz*dz
}

// pcodeAvatar is what the simulator calls a person.
//
// People are never dropped for distance, and that exception is worth
// its weight.  A region describes each avatar once, when the session
// arrives, and never again -- and a standing avatar sends nothing at
// all afterwards, not even the terse position updates that would give
// a re-request something to fire on.  So an avatar refused for
// distance is refused permanently: it can be three metres away, on the
// same platform, in conversation, and the session will not know it is
// there.
//
// Measured on Agni.  Quark logged in at ground level while two others
// stood on a skybox 1977m up; each session threw the others away as
// out of range at that moment, and after Quark teleported up to join
// them, all three were within six metres and none could see any of the
// others.  Logging in already beside them worked perfectly, which is
// what made it look like a viewer problem.
//
// The cost of the exception is a few hundred bytes per person in the
// region, which is nothing against the cost of not knowing who is
// standing next to you.
const pcodeAvatar = 47

// update records what an ObjectUpdate said, and whether it is about
// something beyond everybody's draw distance.
//
// Judging it here as well as trimming later is worth the check.  The
// simulator does not describe what is out of range, so most of the
// time this finds nothing -- but after the avatar has moved, a stray
// update from the far end of the old view is put on notice at once
// rather than taken back in as current.  It is recorded either way and
// dropped only by Trim, once OutOfRangeGrace has passed: the camera it
// was judged by may be the moment's wrong one, and an update refused
// here is never sent again.
//
// A child is judged by its root, and a child whose root is not known
// yet is taken in: object updates arrive in no particular order, and a
// child that turns up first would otherwise be thrown away and never
// mentioned again.  Trim clears up the ones whose root never came.
func (o *Objects) update(d *msg.ObjectUpdate_ObjectData, camera msg.Vector3, drawDistance float32) {
	pos, rot, havePos := msg.DecodePlacement(d.ObjectData)

	o.mu.Lock()
	defer o.mu.Unlock()

	far := false
	if havePos && d.PCode != pcodeAvatar {
		at, judge := pos, true
		if d.ParentID != 0 {
			at, judge = o.anchorLocked(d.ParentID)
		}
		far = judge && !o.keepLocked(at, camera, drawDistance)
	}

	v := o.seen(d.FullID)
	o.judgedLocked(v, far)
	v.Local, v.Parent, v.PCode, v.Scale = d.ID, d.ParentID, d.PCode, d.Scale
	v.Shape = msg.ShapeOfUpdate(d)
	// A full update carries the appearance as well, and it is the only
	// update most prims ever get: a region sends a compressed one for
	// what it thinks is worth compressing, so waiting for one leaves
	// everything else looking like a prim nothing has textured.
	if len(d.TextureEntry) > 0 {
		v.TextureEntry = d.TextureEntry
	}
	if havePos {
		v.Position, v.Rotation = pos, rot
	}
	// An AttachItemID in the NameValue is what says this is worn;
	// State means other things on an object that is not.
	if item, ok := attachItem(d.NameValue); ok {
		v.AttachItem, v.AttachPoint = item, attachPoint(d.State)
	}
}

// attachPoint pulls the point out of an ObjectUpdate's State byte,
// which stores it with its nibbles swapped: point 35 arrives as 0x32.
func attachPoint(state uint8) int {
	return int((state&0xf0)>>4 | (state&0x0f)<<4)
}

// attachItem reads the AttachItemID out of an object's NameValue,
// which is lines of the form
//
//	AttachItemID STRING RW DS <uuid>
func attachItem(nv []byte) (msg.UUID, bool) {
	for _, line := range strings.Split(strings.TrimRight(string(nv), "\x00"), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "AttachItemID" {
			continue
		}
		u, err := msg.ParseUUID(f[len(f)-1])
		if err != nil || u.IsZero() {
			continue
		}
		return u, true
	}
	return msg.UUID{}, false
}

// Attachments is everything known to be worn.
func (o *Objects) Attachments() []*Object {
	o.mu.RLock()
	defer o.mu.RUnlock()
	var out []*Object
	for _, v := range o.byID {
		if !v.AttachItem.IsZero() {
			c := *v
			out = append(out, &c)
		}
	}
	return out
}

// compressed records what a compressed update said.
//
// It carries things a full update does not -- the owner, the floating
// text -- so this fills in what nothing else would.
func (o *Objects) compressed(c *msg.Compressed, camera msg.Vector3, drawDistance float32) {
	parent := uint32(0)
	if c.ParentID != nil {
		parent = *c.ParentID
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	far := false
	if c.PCode != pcodeAvatar {
		at, judge := c.Position, true
		if parent != 0 {
			at, judge = o.anchorLocked(parent)
		}
		far = judge && !o.keepLocked(at, camera, drawDistance)
	}

	v := o.seen(c.FullID)
	o.judgedLocked(v, far)
	v.Local, v.Parent, v.PCode = c.LocalID, parent, c.PCode
	v.Scale, v.Position, v.Rotation = c.Scale, c.Position, c.Rotation
	if !c.Shape.IsZero() {
		v.Shape = c.Shape
	}
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

// judgedLocked records what an update's range check found: in range
// takes the object off notice, out of range puts it on notice if it is
// not already.  It is never dropped here, only by Trim once the notice
// has run out -- the camera an update is judged by can be the moment's
// wrong one, and see OutOfRangeGrace for what dropping on the spot cost.
func (o *Objects) judgedLocked(v *Object, far bool) {
	switch {
	case !far:
		v.leaving = time.Time{}
	case v.leaving.IsZero():
		v.leaving = time.Now()
	}
}

// moved records a terse update.
//
// It only updates something already known.  A terse update names an
// object by local id alone, so one for something never described is
// not enough to make an entry with -- there would be nothing to say
// what it is.
func (o *Objects) moved(t *msg.Terse) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, v := range o.byID {
		if v.Local == t.LocalID {
			v.Position, v.Rotation, v.Velocity = t.Position, t.Rotation, t.Velocity
			v.Last = time.Now()
			v.Moved = v.Last
			return true
		}
	}
	return false
}

// worthDescribing says whether something at this position is close
// enough that not knowing what it is matters.
func (o *Objects) worthDescribing(at msg.Vector3, camera msg.Vector3, far float32) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.keepLocked(at, camera, far)
}

// anchorLocked is where the thing with this local id really is.
//
// A root's position is a place; a child's is an offset from its root,
// and an attachment's is an offset from the avatar wearing it.  So the
// answer is found by walking up until something with no parent is
// reached -- a standing avatar has none, which is what makes an
// attachment judged by where its wearer is standing, and a seated one's
// parent is its seat, which the walk goes on up into.
//
// Not found means the chain leaves the store before the top, and the
// caller cannot say where this is at all.  It is a different answer
// from a position, and treating it as the origin would put everything
// unrooted in the corner of the region.
func (o *Objects) anchorLocked(local uint32) (msg.Vector3, bool) {
	for up := 0; up < 8; up++ {
		v := o.byLocalLocked(local)
		if v == nil {
			return msg.Vector3{}, false
		}
		if v.Parent == 0 {
			return v.Position, true
		}
		local = v.Parent
	}
	return msg.Vector3{}, false
}

// ByLocal is byLocal, exported for the one caller outside this package
// that has a local id and nothing else: a seated avatar names its seat
// by local id and by nothing else, so answering "what is it sitting on"
// means turning one into an object.  See server/seat.go.
func (o *Objects) ByLocal(local uint32) (*Object, bool) { return o.byLocal(local) }

// byLocal is one object by the local id the region numbers it with.
//
// It is a scan, for the reason kill is: the store is keyed by full id,
// because that is the only name an object keeps, and the messages that
// refer to one by local id alone are rare enough that a second index
// would cost more to maintain than it saved.
func (o *Objects) byLocal(local uint32) (*Object, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	v := o.byLocalLocked(local)
	if v == nil {
		return nil, false
	}
	c := *v
	return &c, true
}

// lacks says whether nothing here has this local id.
func (o *Objects) lacks(local uint32) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.byLocalLocked(local) == nil
}

func (o *Objects) byLocalLocked(local uint32) *Object {
	for _, v := range o.byID {
		if v.Local == local {
			return v
		}
	}
	return nil
}

// named records what something is called, for an object that is here.
//
// It does not make one.  A name arrives because something asked, and an
// answer can outlive its object: the reply to a question about
// something that has since been trimmed would otherwise conjure an
// entry with a name and nothing else -- no position, no shape, no
// parent -- which lists as a root prim standing at the origin and never
// goes away, since nothing will ever describe it again.  Measured on a
// live region: eight of sixty-six objects were that and nothing else.
//
// The cost is a name that arrives before the object it belongs to,
// which is dropped.  Nothing is lost by it: what asks for names asks
// again for whatever is still unnamed.
func (o *Objects) named(id msg.UUID, name string, owner msg.UUID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v := o.byID[id]
	if v == nil {
		return
	}
	v.Name, v.Owner, v.Last = name, owner, time.Now()
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

// forgetAppearance drops what the store believes these objects look
// like, keeping everything else known about them, and returns how many
// it forgot.
//
// By local id, because the message that changes an appearance names
// objects that way, so this is the same scan kill is.
func (o *Objects) forgetAppearance(locals ...uint32) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, local := range locals {
		for _, v := range o.byID {
			if v.Local != local {
				continue
			}
			if v.TextureEntry != nil {
				v.TextureEntry = nil
				n++
			}
			break
		}
	}
	return n
}

// objectImage is the message that changes an appearance, looked up once.
var objectImage = (&msg.ObjectImage{}).MsgInfo().ID

// sent is what the store makes of a message this session has just put
// on the wire.
//
// Only one message matters: ObjectImage replaces every face of an
// object at once, and the region does NOT describe the result.  Nothing
// arrives to correct the appearance held here, so what is held goes on
// describing the object as it was before the change -- for as long as
// the session lasts.
//
// Measured on a live region, on a box of six faces: every face
// coloured red, and forty seconds later the reading had not moved --
// it still described the box as it had been before the red.  Worse,
// the next change starts from that reading, since a client must send
// every face and so reads the appearance first: colouring every face
// blue and then face 0 white left face 0 white and the other five back
// at the red they had been before the blue.
//
// So the appearance is forgotten rather than corrected.  Correcting it
// means asking the region to describe the object again, which is a
// round trip nothing has asked for yet; forgetting costs nothing and
// leaves the next reader to ask, which sl.Session.Faces does.
//
// It is forgotten whether or not the change is one the region will
// accept.  A refused change is never reported -- ObjectImage has no
// reply of any kind -- so "it may not have worked" is not a state that
// can be told from "it worked", and the only cache that is honest
// about both is no cache.
func (o *Objects) sent(p *msg.Packet) {
	if p == nil || p.Message == nil || p.ID != objectImage {
		return
	}
	m, ok := p.Message.(*msg.ObjectImage)
	if !ok {
		// A client of the daemon sends bytes rather than a type: what
		// goes on the wire for it is a msg.Raw, and Encode gives the
		// body back unchanged.
		b, err := p.Message.Encode()
		if err != nil {
			return
		}
		m = &msg.ObjectImage{}
		if err := m.Decode(b); err != nil {
			return
		}
	}
	locals := make([]uint32, 0, len(m.ObjectData))
	for i := range m.ObjectData {
		locals = append(locals, m.ObjectData[i].ObjectLocalID)
	}
	o.forgetAppearance(locals...)
}

// placementWidths counts the placement blobs ObjectUpdates carry, by
// width in bytes.
type placementWidths struct {
	mu sync.Mutex
	n  map[int]uint64
}

func (w *placementWidths) count(width int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n == nil {
		w.n = map[int]uint64{}
	}
	w.n[width]++
}

// PlacementWidths is how many ObjectUpdate placement blobs this
// session has been sent, by width in bytes, unread widths included.
//
// The width says which form a blob is in, and msg.DecodePlacement
// reads 60, 124 and 32, and 76, 140 and 48 for an avatar.  No 32 byte
// blob has been recorded, and this is how a day's traffic says whether
// one is ever sent.
// Why: doc/placement.md#what-has-been-seen
func (a *Agent) PlacementWidths() map[int]uint64 {
	a.placements.mu.Lock()
	defer a.placements.mu.Unlock()
	out := make(map[int]uint64, len(a.placements.n))
	for w, n := range a.placements.n {
		out[w] = n
	}
	return out
}

// trackObjects registers the handlers that keep the registry current.
//
// They are inline so that the picture is up to date before anything
// dispatched after them looks at it.
func (a *Agent) trackObjects() {
	a.Disp.MustHandle("ObjectUpdate", func(p *msg.Packet) {
		m := p.Message.(*msg.ObjectUpdate)
		l := a.Look()
		var parents []uint32
		for i := range m.ObjectData {
			d := &m.ObjectData[i]
			a.placements.count(len(d.ObjectData))
			a.Objects().update(d, l.Center, l.Far)
			parents = a.orphaned(parents, d.ParentID)
		}
		a.askAfter(parents)
	}, msg.Inline())

	// ObjectUpdateCompressed is how most updates arrive after an
	// object has first been described.  A session that ignores it sees
	// the world as it was on arrival and never learns otherwise.
	a.Disp.MustHandle("ObjectUpdateCompressed", func(p *msg.Packet) {
		m := p.Message.(*msg.ObjectUpdateCompressed)
		l := a.Look()
		var parents []uint32
		for i := range m.ObjectData {
			c, err := msg.DecodeCompressed(m.ObjectData[i].Data)
			if c == nil {
				continue
			}
			// A partly decoded object still says where it is and what
			// it is, which is what the cache is for.  The error is
			// worth nothing here beyond not trusting the tail.
			_ = err
			a.Objects().compressed(c, l.Center, l.Far)
			if c.ParentID != nil {
				parents = a.orphaned(parents, *c.ParentID)
			}
		}
		a.askAfter(parents)
	}, msg.Inline())

	// ImprovedTerseObjectUpdate is the message the simulator sends
	// most: everything that moves, several times a second.  It carries
	// only a local id and a position, so it updates what is already
	// known rather than introducing anything.
	a.Disp.MustHandle("ImprovedTerseObjectUpdate", func(p *msg.Packet) {
		m := p.Message.(*msg.ImprovedTerseObjectUpdate)
		l := a.Look()
		store := a.Objects()
		var strangers []uint32
		for i := range m.ObjectData {
			t, err := msg.DecodeTerse(m.ObjectData[i].Data)
			if err != nil {
				continue
			}
			if store.moved(t) {
				continue
			}
			// Something the simulator believes we already hold and
			// will not describe again.  Usually that is right and this
			// is out of range anyway; but a thing that was dropped for
			// distance and has since come close -- an avatar who
			// teleported up to the same platform, most of all --
			// arrives here and nowhere else, because a terse update
			// carries a local id and a position and nothing that says
			// what it is.  Landing on nothing, it leaves the two of
			// them invisible to each other for as long as they both
			// stand there.
			if !store.worthDescribing(t.Position, l.Center, l.Far) {
				continue
			}
			if a.askAgain(t.LocalID) {
				strangers = append(strangers, t.LocalID)
			}
		}
		if len(strangers) > 0 {
			// Off the dispatch goroutine, as with any asking.
			go a.requestCachedObjects(strangers)
		}
	}, msg.Inline())

	a.Disp.MustHandle("KillObject", func(p *msg.Packet) {
		m := p.Message.(*msg.KillObject)
		for _, d := range m.ObjectData {
			a.Objects().kill(d.ID)
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
		a.Objects().named(m.ObjectData.ObjectID,
			trimNul(m.ObjectData.Name), m.ObjectData.OwnerID)
	}, msg.Inline())

	a.Disp.MustHandle("ObjectProperties", func(p *msg.Packet) {
		m := p.Message.(*msg.ObjectProperties)
		for i := range m.ObjectData {
			d := &m.ObjectData[i]
			a.Objects().named(d.ObjectID, trimNul(d.Name), d.OwnerID)
		}
	}, msg.Inline())
}

// orphaned adds a parent to the list to ask after, if it is one this
// store has never been told about and has not asked after lately.
//
// A child names its parent by local id and nothing more, and the region
// describes each object once.  A parent whose description never arrived
// -- lost in a packet this session could not read, or simply not sent
// -- is never described again of its own accord, and everything under
// it cannot be placed: a linkset's prims, or the avatar sitting on it
// and everything that avatar wears.  Measured on Agni: a seat went
// undescribed for as long as the session lasted, and one request by its
// local id brought it back at once.
//
// The viewer keeps its orphans waiting for the parent rather than
// asking; asking is cheaper than waiting for something that will not
// come.
func (a *Agent) orphaned(parents []uint32, parent uint32) []uint32 {
	if parent == 0 || !a.Objects().lacks(parent) || !a.askAgain(parent) {
		return parents
	}
	return append(parents, parent)
}

// askAfter asks for what orphaned collected, off the dispatch
// goroutine as all asking is.
func (a *Agent) askAfter(parents []uint32) {
	if len(parents) > 0 {
		go a.requestCachedObjects(parents)
	}
}

// AskAgainAfter is how long to leave a local id alone once it has been
// asked about.
//
// Something moving just inside the draw distance is described, kept,
// and trimmed again as it drifts back out, and without this each pass
// would ask afresh several times a second.  Long enough that the churn
// costs one request rather than a stream, short enough that a thing
// which really did arrive and was really missed is not invisible for
// long.
const AskAgainAfter = 15 * time.Second

// askAgain says whether this local id may be asked about now, and
// remembers that it was.
func (a *Agent) askAgain(local uint32) bool {
	now := time.Now()

	a.askedMu.Lock()
	defer a.askedMu.Unlock()
	if a.asked == nil {
		a.asked = map[uint32]time.Time{}
	}
	if when, seen := a.asked[local]; seen && now.Sub(when) < AskAgainAfter {
		return false
	}
	// Swept here rather than on a timer: this runs only when something
	// unknown turns up, which is exactly when the map grows.
	if len(a.asked) > 4096 {
		for id, when := range a.asked {
			if now.Sub(when) >= AskAgainAfter {
				delete(a.asked, id)
			}
		}
	}
	a.asked[local] = now
	return true
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

// Redescribe asks the simulator to describe every object this session
// knows about, again, and returns how many were asked for.
//
// It exists for a viewer joining a session that has been running.  A
// region describes each object once, on arrival, and this session
// consumed those descriptions hours ago; nothing will repeat them, so a
// viewer attached later sees only what happens to change while it
// watches -- which on a quiet parcel is almost nothing, and looks
// exactly like a relay that is dropping object updates.
//
// The simulator does not mind being asked.  Measured on Aditi: a store
// of 1449 objects, flushed and asked for again, came back complete in
// six seconds.  What comes back are ordinary updates, so they reach a
// viewer the same way everything else does and carry every field the
// simulator sends rather than the dozen this package keeps.
func (a *Agent) Redescribe() int {
	objs := a.Objects().All()
	ids := make([]uint32, 0, len(objs))
	for _, o := range objs {
		if o.Local != 0 {
			ids = append(ids, o.Local)
		}
	}
	if len(ids) == 0 {
		return 0
	}
	// Off the caller's goroutine: this is dozens of datagrams and the
	// caller is usually a dispatch loop that must not stall.
	go a.requestCachedObjects(ids)
	return len(ids)
}

// worldPlacement is where an object really is, resolved through
// whatever it is attached to.
//
// A child prim's position is an offset from its root, an attachment's
// from the avatar wearing it, and a seated avatar's from its seat, so
// for anything with a parent the position in the store is not a place
// in the region at all.  This composes back up the chain: each step
// turns an offset into a place by rotating it into the parent's frame
// and adding the parent's own position, which is the same arithmetic a
// viewer does to draw the thing.
//
// It gives up when the chain runs out of described objects rather than
// guessing at the missing link, since a wrong answer here is a position
// somebody would act on.  Eight steps at most, as in anchorLocked and
// Trim: a chain that deep is a loop, and a loop must not be walked for
// ever.
func (o *Objects) worldPlacement(local uint32) (msg.Vector3, msg.Quaternion, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	v := o.byLocalLocked(local)
	if v == nil {
		return msg.Vector3{}, msg.Quaternion{}, false
	}
	at, facing := v.Position, v.Rotation
	for up := 0; up < 8 && v.Parent != 0; up++ {
		p := o.byLocalLocked(v.Parent)
		if p == nil {
			return msg.Vector3{}, msg.Quaternion{}, false
		}
		at = addVec(p.Position, p.Rotation.Rotate(at))
		facing = p.Rotation.Mul(facing)
		v = p
	}
	if v.Parent != 0 {
		return msg.Vector3{}, msg.Quaternion{}, false
	}
	return at, facing, true
}

func addVec(a, b msg.Vector3) msg.Vector3 {
	return msg.Vector3{X: a.X + b.X, Y: a.Y + b.Y, Z: a.Z + b.Z}
}
