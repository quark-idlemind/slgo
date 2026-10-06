package agent

import (
	"context"
	"encoding/binary"
	"slices"
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

	// SalePrice is what the region last said the object is sold for,
	// and Priced says it has said: ObjectProperties and
	// ObjectPropertiesFamily carry it, an update does not.  See prices.go.
	SalePrice int32
	Priced    bool

	// TextureEntry is the per face appearance, still packed.  Both
	// kinds of update carry it, and taking it from only the compressed
	// one left most prims looking untextured: a region sends a full
	// update for plenty of objects that never get a compressed one.
	TextureEntry []byte

	// Text is the floating text above the object, when it has any, as
	// the last update of either kind said it.
	Text string

	// TextureAnim is the texture animation, still packed, as the last
	// full or compressed update said it, and nil when that said none.
	// A running one moves a face's texture on from what its entry says.
	TextureAnim []byte

	// Click is the click action byte, and ClickKnown says an update
	// carried it: zero is the touch action, so a zero Click with
	// ClickKnown false means no update has said.  Full and compressed
	// updates carry it and terse ones do not, and forgetting an
	// appearance leaves it alone.
	// Why: doc/objects.md#the-click-action
	Click      uint8
	ClickKnown bool

	// Sculpt is what the sculpt block of the extra parameters says: the
	// zero value for a prim that is neither a sculpt nor a mesh.  A
	// full or compressed update says it afresh, as it does the
	// animation, so one without a block says none; a terse update, and
	// a compressed one that would not decode, leave it alone.
	// Why: doc/objects.md#how-many-faces-a-prim-has
	Sculpt msg.SculptMark

	// Light and Projector are the light and light image blocks of the
	// extra parameters: nil while the prim has none, which is how a
	// light switched off is told, since the region leaves the block out
	// and does not send one with no intensity.  A full or compressed
	// update says them afresh, as it does Sculpt, so one without the
	// block says there is none; a terse update, and a full one whose
	// parameters would not read, leave them alone.  They are replaced
	// whole and never written through, so a copy of an Object may share
	// them.
	// Why: doc/lights.md
	Light     *msg.Light
	Projector *msg.LightImage

	// AttachPoint is where a worn object is attached, and zero when it
	// is not worn.  AttachItem is the inventory item it was worn from.
	//
	// The item is the only stable name a worn object has.  The object
	// itself is rezzed afresh -- with a new id -- every time it is put
	// on, and again every time the avatar logs in, so anything that
	// wants to find the same attachment twice has to look for the item
	// it came from.  Both are read from the update, full or compressed,
	// that describes an attachment, which is sent when it goes on and
	// again at login;
	// a program that connected afterwards never heard it, which is why
	// it is worth remembering here.
	AttachPoint int
	AttachItem  msg.UUID

	// LinkNumber is what a viewer calls this prim in its linkset: 0 when
	// it is not linked, 1 for the root of a linkset that has children,
	// and 2 and up for a child, by its place in the root's list of
	// them.  An avatar sitting on the set is numbered after all of its
	// prims, in the order the avatars sat.  The store fills it in on the
	// copy it hands out; see Objects.kids and Objects.sitters.
	// Why: doc/objects.md#link-numbers
	LinkNumber int

	// LinkKnown says LinkNumber can be believed: true for a prim that is
	// not linked, and for the rest the state of its linkset, which a
	// link made while the store watched leaves unknown.  A sitter's
	// number does not depend on the prims' order, so it is known unless
	// several sitters were described already seated.  See
	// Objects.unordered and Objects.sitUnordered.
	LinkKnown bool

	// First and Last are when the simulator first and last said
	// anything about this object.  Last is what Trim ages an orphan by.
	First time.Time
	Last  time.Time

	// leaving is when this object was first found out of everybody's
	// range, and zero while it is in somebody's.  Being out of range
	// puts an object on notice rather than out of the store: see
	// outOfRangeGrace.
	leaving time.Time

	// listed says this object is in the child list of listedUnder, and
	// is how an update that repeats its parent is told from one that
	// changes it.  See Objects.kids.
	listed      bool
	listedUnder uint32

	// heard and heardParent are the parent this object had the last time
	// an update described it, which is what tells a prim the store held
	// being linked or unlinked from one described afresh.  moved says
	// its place in its parent's list is not known: it joined with others
	// in one update, or came with a store taken from another agent.  See
	// Objects.unordered.
	heard       bool
	heardParent uint32
	moved       bool
}

// Objects is what the session has been told about the region.
//
// It accumulates.  Objects are added as the simulator describes them
// and removed when it says they have gone, so it tracks what is around
// rather than growing without bound -- but it is a record of what has
// been heard, not a query against the region.
//
// Trim drops what is out of range, and the agent calls it every
// trimInterval, so a store shrinks when the camera pulls in -- a little
// behind the camera rather than with it.
// Why: doc/objects.md#before-trim
//
// A store can be shared by several agents in the same region -- see
// Cache -- which is what viewers is for: what to keep is decided by
// everyone looking, not by whoever happened to hear the update.
type Objects struct {
	mu      sync.RWMutex
	byID    map[msg.UUID]*Object
	viewers map[string]viewpoint

	// kids is each parent's children, by the parent's local id, in link
	// order: a child is 2 plus its place here, and the root 1.  A set
	// described afresh arrives in link order, so its children are
	// appended.  A prim the store already held that is linked while we
	// watch goes to the front, as link 2: a link of one makes it link 2,
	// as llCreateLink does (measured), and the region's update for a link
	// of several is not in link order, so that set is left unknown.  A
	// child is removed when its parent changes or it goes, and the rest
	// close up.  Avatars sitting on the set are not in this list: see
	// sitters.
	//
	// A parent that is not in the store yet has a list all the same,
	// which is the viewer's orphans waiting in the order they came.
	// Only a change of parent moves a prim: the store is shared, so
	// the same update can arrive once for each agent in the region, and
	// a repeat must not reorder the linkset.
	// Why: doc/objects.md#link-numbers
	kids map[uint32][]msg.UUID

	// sitters is each parent's children that are avatars, by the
	// parent's local id, in the order they sat.  The viewer appends every
	// child, so a sitter comes after all of the prims however it arrived:
	// it is 1 plus the number of prims, root included, plus its place
	// here.  A sitter is never inserted at the front, joins no window and
	// takes no part in the prims' order; it leaves when its parent changes
	// or it goes, and the later ones close up.
	// Why: doc/objects.md#link-numbers
	sitters map[uint32][]msg.UUID

	// sitUnordered holds the parents with more than one sitter of which
	// one was described already seated, which the region sends in no
	// order we know.  It goes when one sitter or none is left.
	sitUnordered map[uint32]bool

	// unordered holds the parents whose child list is not known to be
	// in link order: a set that several prims joined in one update
	// (joinedTogether), and the sets of a store taken from another agent,
	// whose copies come in no order (absorb).  A parent comes back out as soon
	// as no child in its list came that way (Object.moved), which a kill
	// and a fresh description, or a flush, brings about.
	// Why: doc/objects.md#link-numbers
	unordered map[uint32]bool

	// misordered holds the roots, by full id, of sets that several prims
	// joined in one update.  The region goes on describing such a set in
	// the same wrong order, a flush and a fresh description included
	// (measured), so a set whose root is here stays unknown however it is
	// described.  Flush keeps it; a kill of the root, as a delete or a
	// take does, drops it, since a rez gives the object new ids.  It does
	// not survive a new store, on a region change or a daemon restart.
	// Why: doc/objects.md#link-numbers
	misordered map[msg.UUID]bool

	// links is the order an ObjectLink this session sent named a set's
	// children in, by the root's local id, until the region's update for
	// that set comes and uses it (namedLink).  Only the session that
	// linked can know it.
	// Why: doc/objects.md#link-numbers
	links map[uint32]namedLink

	// now is the clock the records in links are aged by, which a test
	// sets.
	now func() time.Time

	// lastLive is when a prim last joined each parent live, by the
	// parent's local id; see joinWindow.
	lastLive map[uint32]time.Time
}

// linkWindow is how long the order an ObjectLink named is kept for the
// region's update that makes it.
// Why: doc/objects.md#link-numbers
const linkWindow = 20 * time.Second

// joinWindow is how close together two live joins to one parent must
// be to count as one link of several.  A watching session was measured
// to get a link of several split over two packets 75 microseconds apart,
// and two agents sharing a store hear one update at nearly the same time;
// a link of one is slower than this whatever makes it, since
// llCreateLink sleeps its script for a second and a person links by hand.
// Why: doc/objects.md#link-numbers
const joinWindow = 250 * time.Millisecond

// namedLink is the children an ObjectLink named, by local id in the
// order named, and when it was sent.
type namedLink struct {
	children []uint32
	at       time.Time
}

// A viewpoint is one avatar's camera and how far it is being told
// about.  Far of zero means that avatar has no limit, so nothing is
// dropped on its account.
type viewpoint struct {
	camera msg.Vector3
	far    float32
}

func newObjects() *Objects {
	return &Objects{byID: map[msg.UUID]*Object{}, viewers: map[string]viewpoint{},
		kids: map[uint32][]msg.UUID{}, unordered: map[uint32]bool{},
		sitters: map[uint32][]msg.UUID{}, sitUnordered: map[uint32]bool{},
		misordered: map[msg.UUID]bool{}, links: map[uint32]namedLink{},
		lastLive: map[uint32]time.Time{},
		now:      time.Now}
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
	limit := far + trimMargin
	return dist2(at, camera) <= limit*limit
}

// All returns a copy of everything known.
func (o *Objects) All() []*Object {
	o.mu.RLock()
	defer o.mu.RUnlock()
	here := make(map[uint32]bool, len(o.byID))
	for _, v := range o.byID {
		here[v.Local] = true
	}
	out := make([]*Object, 0, len(o.byID))
	for _, v := range o.byID {
		c := *v
		o.numberLocked(&c, func(p uint32) bool { return here[p] })
		out = append(out, &c)
	}
	return out
}

// Linkset is the root with this local id and its children, in link
// order, the prims and then the sitters, or nil if nothing here has that id.
func (o *Objects) Linkset(root uint32) []*Object {
	o.mu.RLock()
	defer o.mu.RUnlock()
	r := o.byLocalLocked(root)
	if r == nil {
		return nil
	}
	c := *r
	o.numberLocked(&c, func(p uint32) bool { return o.byLocalLocked(p) != nil })
	out := []*Object{&c}
	for i, id := range o.kids[root] {
		if v := o.byID[id]; v != nil {
			k := *v
			k.LinkNumber, k.LinkKnown = 2+i, !o.unordered[root]
			out = append(out, &k)
		}
	}
	for i, id := range o.sitters[root] {
		if v := o.byID[id]; v != nil {
			k := *v
			k.LinkNumber, k.LinkKnown = 2+len(o.kids[root])+i, !o.sitUnordered[root]
			out = append(out, &k)
		}
	}
	return out
}

// numberLocked fills in the link number and whether to believe it on a
// copy of an object, given whether a local id is in the store.  A prim
// with no parent and no children is unlinked, which is known; the rest
// are as known as their set, which is the one under the parent, or its
// own for a root.
func (o *Objects) numberLocked(c *Object, parentHere func(uint32) bool) {
	c.LinkNumber = o.linkNumberLocked(c, parentHere)
	set := c.Parent
	if set == 0 {
		set = c.Local
	}
	c.LinkKnown = !o.unordered[set]
	// A sitter's number needs only the count of prims, not their order.
	if c.PCode == pcodeAvatar && c.Parent != 0 {
		c.LinkKnown = !o.sitUnordered[c.Parent]
	}
}

// linkNumberLocked is v's link number, given whether a local id is in
// the store.  A child whose parent is not here is an orphan, which the
// viewer does not number either.
func (o *Objects) linkNumberLocked(v *Object, parentHere func(uint32) bool) int {
	if v.Parent == 0 {
		if len(o.kids[v.Local]) > 0 || len(o.sitters[v.Local]) > 0 {
			return 1
		}
		return 0
	}
	if !v.listed || !parentHere(v.Parent) {
		return 0
	}
	if v.PCode == pcodeAvatar {
		for i, id := range o.sitters[v.Parent] {
			if id == v.ID {
				return 2 + len(o.kids[v.Parent]) + i
			}
		}
		return 0
	}
	for i, id := range o.kids[v.Parent] {
		if id == v.ID {
			return 2 + i
		}
	}
	return 0
}

// linkLocked puts v in its parent's child list, after taking it out of
// the one it was in.  Called with v.Parent set from an update: a prim
// already listed under that parent stays where it is.
//
// An object the store already held that changes parent is a live link,
// and goes to the front of its new list.  One described for the first
// time, or described again under the parent it had before its list was
// dropped, is a fresh arrival, and goes to the end.  An avatar is
// neither: it is a sitter, goes to the end of the sitters, and leaves
// the prims' order and joinWindow alone.
// Why: doc/objects.md#link-numbers
func (o *Objects) linkLocked(v *Object) {
	if v.listed && v.listedUnder == v.Parent {
		return
	}
	live := v.heard && v.heardParent != v.Parent
	described := !v.heard
	left, was := v.listedUnder, v.listed
	o.unlinkLocked(v)
	v.heard, v.heardParent, v.moved = true, v.Parent, false
	if was {
		o.settleLocked(left)
	}
	if v.Parent == 0 {
		// A root described afresh after its children: if its set is one
		// the region misorders, they are not in link order either.  A
		// root it already held says nothing new about the order.
		if described && o.misordered[v.ID] && len(o.kids[v.Local]) > 0 {
			o.misorderLocked(v.Local)
		}
		return
	}
	if v.PCode == pcodeAvatar {
		o.sitters[v.Parent] = append(o.sitters[v.Parent], v.ID)
		// Avatars seen already seated come in no order we know; ones that
		// sat while we watched are in the order they sat.
		if !live && len(o.sitters[v.Parent]) > 1 {
			o.sitUnordered[v.Parent] = true
		}
		v.listed, v.listedUnder = true, v.Parent
		return
	}
	if live {
		o.kids[v.Parent] = slices.Insert(o.kids[v.Parent], 0, v.ID)
		// A second live join close behind another is one link of several,
		// split over packets or shared between agents: its order is not
		// known, however each join on its own would number it.
		now := o.now()
		if t, ok := o.lastLive[v.Parent]; ok && now.Sub(t) < joinWindow {
			o.misorderLocked(v.Parent)
			if r := o.byLocalLocked(v.Parent); r != nil {
				o.misordered[r.ID] = true
			}
		}
		o.lastLive[v.Parent] = now
	} else {
		o.kids[v.Parent] = append(o.kids[v.Parent], v.ID)
	}
	v.listed, v.listedUnder = true, v.Parent
	// Only a fresh description comes in the region's misordered order; a
	// live link of one is link 2 whatever the set's history.
	if !live {
		if r := o.byLocalLocked(v.Parent); r != nil && o.misordered[r.ID] {
			o.misorderLocked(v.Parent)
		}
	}
}

// misorderLocked marks a parent's set unknown because its root is in
// misordered.
func (o *Objects) misorderLocked(parent uint32) {
	for _, id := range o.kids[parent] {
		if c := o.byID[id]; c != nil {
			c.moved = true
		}
	}
	o.unordered[parent] = true
}

// joinedTogether records that one update linked these prims, which the
// store already held, to one parent.  A link of several comes as one
// update whose blocks do not follow the link order (measured), so the
// set's order is not known; a link of one is known, at the front.  Unless
// this session named the order in the ObjectLink it sent (Objects.links)
// and the set is exactly the children it named, which is then the order.
// Why: doc/objects.md#link-numbers
func (o *Objects) joinedTogether(parent uint32, ids []msg.UUID) {
	if parent == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	rec, named := o.namedLocked(parent)
	// A lone prim that the record names is one of a link of several
	// that came split; one it does not name is a link of one, and an
	// extra child of the set the record is for.
	part := false
	for _, id := range ids {
		if v := o.byID[id]; v != nil && slices.Contains(rec.children, v.Local) {
			part = true
		}
	}
	if named && !part {
		delete(o.links, parent)
		named = false
	}
	if len(ids) < 2 && !part {
		return
	}
	for _, id := range ids {
		if v := o.byID[id]; v != nil && v.listed && v.listedUnder == parent {
			v.moved = true
		}
	}
	o.unordered[parent] = true
	if r := o.byLocalLocked(parent); r != nil {
		o.misordered[r.ID] = true
	}
	if named {
		o.applyLinkLocked(parent, rec)
	}
}

// A move is one prim an update gives a parent.
type move struct {
	id     msg.UUID
	parent uint32
}

// setJoin is the order a set takes when one whole set the store knows the
// order of joins it in one update, worked out before the update is applied.
type setJoin struct {
	root  msg.UUID   // the joining set's root
	order []msg.UUID // that root, then its children in their order
	old   []msg.UUID // the parent's children before, in their order
}

// planJoins says, before an update is applied, which parents several
// prims are about to join as one whole set of known order.  A set linked
// to another by llCreateLink, or by an ObjectLink naming the two roots,
// goes in right after the root that stays: the joining root, then its
// children in their order, then the staying root's old children
// (measured both ways).  The update lists the prims in another order, so
// the order is taken from the two sets as they were.  Only a parent whose
// movers are exactly one root and all of that root's children, with both
// sets known, gets a plan; anything else is left to joinedTogether.
// Why: doc/objects.md#link-numbers
func (o *Objects) planJoins(moves []move) map[uint32]*setJoin {
	o.mu.RLock()
	defer o.mu.RUnlock()
	byParent := map[uint32][]*Object{}
	for _, m := range moves {
		v := o.byID[m.id]
		if m.parent == 0 || v == nil || v.PCode == pcodeAvatar || !v.heard || v.heardParent == m.parent {
			continue
		}
		byParent[m.parent] = append(byParent[m.parent], v)
	}
	plans := map[uint32]*setJoin{}
	for parent, vs := range byParent {
		if len(vs) < 2 || o.unordered[parent] {
			continue
		}
		var root *Object
		for _, v := range vs {
			if v.Parent == 0 {
				if root != nil {
					root = nil
					break
				}
				root = v
			}
		}
		if root == nil || o.unordered[root.Local] {
			continue
		}
		kids := o.kids[root.Local]
		if len(kids) != len(vs)-1 {
			continue
		}
		whole := true
		for _, v := range vs {
			if v != root && (v.Parent != root.Local || !slices.Contains(kids, v.ID)) {
				whole = false
			}
		}
		if !whole {
			continue
		}
		plans[parent] = &setJoin{
			root:  root.ID,
			order: append([]msg.UUID{root.ID}, kids...),
			old:   slices.Clone(o.kids[parent]),
		}
	}
	return plans
}

// applyJoin gives a parent the order planJoins worked out, once the
// update has moved every prim of the joining set under it and nothing
// else has joined.  It does nothing when the linking session's own record
// already gave the order.
func (o *Objects) applyJoin(parent uint32, j *setJoin) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, named := o.links[parent]; named || !o.unordered[parent] {
		return
	}
	want := append(slices.Clone(j.order), j.old...)
	have := o.kids[parent]
	if len(have) != len(want) {
		return
	}
	for _, id := range want {
		if !slices.Contains(have, id) {
			return
		}
	}
	o.kids[parent] = want
	for _, id := range want {
		if v := o.byID[id]; v != nil {
			v.moved = false
		}
	}
	delete(o.unordered, parent)
}

// namedLocked is the record for a root, dropped and not returned once it
// is older than linkWindow.
func (o *Objects) namedLocked(parent uint32) (namedLink, bool) {
	rec, ok := o.links[parent]
	if ok && o.now().Sub(rec.at) > linkWindow {
		delete(o.links, parent)
		return namedLink{}, false
	}
	return rec, ok
}

// applyLinkLocked gives a set the order its link named, when the
// children it holds are exactly the ones named.  Children not here yet,
// or still loose, leave the record for the update that brings them; an
// extra child, or one under another parent, drops it.
func (o *Objects) applyLinkLocked(parent uint32, rec namedLink) {
	have := o.kids[parent]
	if len(have) > len(rec.children) {
		delete(o.links, parent)
		return
	}
	order := make([]msg.UUID, 0, len(rec.children))
	for _, local := range rec.children {
		v := o.byLocalLocked(local)
		switch {
		case v == nil || v.Parent == 0:
		case v.Parent != parent:
			delete(o.links, parent)
			return
		default:
			order = append(order, v.ID)
		}
	}
	if len(order) != len(rec.children) {
		for _, id := range have {
			if v := o.byID[id]; v != nil && !slices.Contains(rec.children, v.Local) {
				delete(o.links, parent)
				return
			}
		}
		return
	}
	if len(have) != len(order) {
		delete(o.links, parent)
		return
	}
	o.kids[parent] = order
	for _, id := range order {
		o.byID[id].moved = false
	}
	delete(o.unordered, parent)
	// The region's fresh descriptions of this set stay misordered, so
	// misordered keeps the root: after a flush the set is unknown again.
	delete(o.links, parent)
}

// linked records the order an ObjectLink named: the first block is the
// root and the rest are the children, in the order that numbers them.
// A link of one is not kept, since the store's front insertion is
// already right for it.
func (o *Objects) linked(m *msg.ObjectLink) {
	if len(m.ObjectData) < 3 {
		return
	}
	children := make([]uint32, 0, len(m.ObjectData)-1)
	for _, d := range m.ObjectData[1:] {
		children = append(children, d.ObjectLocalID)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	for root, rec := range o.links {
		if now.Sub(rec.at) > linkWindow {
			delete(o.links, root)
		}
	}
	o.links[m.ObjectData[0].ObjectLocalID] = namedLink{children: children, at: now}
}

// liveJoin says whether an update giving this prim this parent would be
// a live link: the store holds it, under another parent or none.  An
// avatar sitting down is not one.
func (o *Objects) liveJoin(id msg.UUID, parent uint32) bool {
	if parent == 0 {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	v := o.byID[id]
	return v != nil && v.PCode != pcodeAvatar && v.heard && v.heardParent != parent
}

// settleLocked takes a parent out of unordered once no child in its
// list is there by a live link.
func (o *Objects) settleLocked(parent uint32) {
	if !o.unordered[parent] {
		return
	}
	for _, id := range o.kids[parent] {
		if c := o.byID[id]; c != nil && c.moved {
			return
		}
	}
	delete(o.unordered, parent)
}

func (o *Objects) unlinkLocked(v *Object) {
	if !v.listed {
		return
	}
	if i := slices.Index(o.sitters[v.listedUnder], v.ID); i >= 0 {
		list := slices.Delete(o.sitters[v.listedUnder], i, i+1)
		if len(list) == 0 {
			delete(o.sitters, v.listedUnder)
		} else {
			o.sitters[v.listedUnder] = list
		}
		if len(list) < 2 {
			delete(o.sitUnordered, v.listedUnder)
		}
		v.listed = false
		return
	}
	list := o.kids[v.listedUnder]
	for i, id := range list {
		if id == v.ID {
			list = slices.Delete(list, i, i+1)
			break
		}
	}
	if len(list) == 0 {
		delete(o.kids, v.listedUnder)
		delete(o.unordered, v.listedUnder)
	} else {
		o.kids[v.listedUnder] = list
	}
	v.listed = false
}

// forgetLocked takes an object out of the store, and its child list
// with it: a viewer's children die with their parent, and the region
// kills them too.  What is left is unlisted, so that an update after
// the local id is reused lists it afresh.
func (o *Objects) forgetLocked(v *Object) {
	under, was := v.listedUnder, v.listed
	o.unlinkLocked(v)
	if was {
		o.settleLocked(under)
	}
	delete(o.unordered, v.Local)
	for _, id := range o.kids[v.Local] {
		if c := o.byID[id]; c != nil {
			c.listed = false
		}
	}
	for _, id := range o.sitters[v.Local] {
		if c := o.byID[id]; c != nil {
			c.listed = false
		}
	}
	delete(o.kids, v.Local)
	delete(o.sitters, v.Local)
	delete(o.sitUnordered, v.Local)
	delete(o.lastLive, v.Local)
	delete(o.byID, v.ID)
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
	o.numberLocked(&c, func(p uint32) bool { return o.byLocalLocked(p) != nil })
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
		// A copy knows no list of this store's, and the order it
		// arrives in is only what All gave, so its set is unordered.
		v.listed, v.heard, v.heardParent = false, false, 0
		o.byID[v.ID] = v
		o.linkLocked(v)
		if v.Parent != 0 && v.PCode != pcodeAvatar {
			v.moved = true
			o.unordered[v.Parent] = true
		}
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
	o.kids = map[uint32][]msg.UUID{}
	o.sitters = map[uint32][]msg.UUID{}
	o.sitUnordered = map[uint32]bool{}
	o.unordered = map[uint32]bool{}
	o.lastLive = map[uint32]time.Time{}
	return n
}

// trimMargin is how far past the draw distance an object is kept.
//
// Trimming at exactly the draw distance would fight the simulator over
// anything sitting on the boundary: dropped, described again, dropped
// again.  The margin costs a few entries and stops the flapping.
const trimMargin = 32

// outOfRangeGrace is how long an object found out of range is kept
// before it is dropped for it.
//
// Out of range is judged against cameras, and a camera can be wrong for
// a moment: at login it is put on the avatar before anything has said
// where the avatar is, and for a second or so it looks out from the
// region's corner -- in the seconds when the region describes the most,
// and it describes each object once.
//
// So an object out of range is noticed first and dropped only if it is
// still out of range this long afterwards.  Coming back into anybody's
// range in between takes it off notice.  Two trim ticks, so that the
// decision is always made on a camera read at least one tick after the
// one that put it on notice.  The cost is holding a little of the view
// just left for half a minute longer.
// Why: doc/objects.md#out-of-range-for-a-while
const outOfRangeGrace = 2 * trimInterval

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

	// A walk that breaks off above a person is not an orphan.  A seated
	// avatar's parent is its seat, and a seat is a prim like any other,
	// so the walk carries on up into it, and the seat may not have been
	// described.  The person is kept whatever happens (see pcodeAvatar),
	// so what it wears is kept with it until the seat turns up and there
	// is somewhere to judge them from.
	// Why: doc/objects.md#what-a-seated-avatar-wears
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
	for _, v := range o.byID {
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
			// evidence available that it is no longer worth keeping,
			// and not a time after it was first heard: a region goes on
			// describing a prim whose root it never describes to us,
			// and a thing the simulator keeps talking about is a thing
			// that is there.
			// Why: doc/objects.md#orphans
			if time.Since(v.Last) > orphanGrace {
				o.forgetLocked(v)
				n++
			}
			continue
		}
		if o.keepLocked(at, camera, drawDistance) {
			v.leaving = time.Time{}
			continue
		}
		if o.goneLocked(v, now) {
			o.forgetLocked(v)
			n++
		}
	}
	return n
}

// goneLocked puts an object that is out of range on notice, and says
// whether it has been out of range long enough to drop.  See
// outOfRangeGrace.
func (o *Objects) goneLocked(v *Object, now time.Time) bool {
	if v.leaving.IsZero() {
		v.leaving = now
		return false
	}
	return now.Sub(v.leaving) >= outOfRangeGrace
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
// The cost of the exception is a few hundred bytes per person in the
// region, which is nothing against the cost of not knowing who is
// standing next to you.
// Why: doc/objects.md#people-are-kept-whatever-the-distance
const pcodeAvatar = 47

// update records what an ObjectUpdate said, and whether it is about
// something beyond everybody's draw distance.
//
// Judging it here as well as trimming later is worth the check.  The
// simulator does not describe what is out of range, so most of the
// time this finds nothing -- but after the avatar has moved, a stray
// update from the far end of the old view is put on notice at once
// rather than taken back in as current.  It is recorded either way and
// dropped only by Trim, once outOfRangeGrace has passed: the camera it
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
	o.linkLocked(v)
	v.Shape = msg.ShapeOfUpdate(d)
	v.Click, v.ClickKnown = d.ClickAction, true
	// A block that does not read says nothing, so the last answer stays.
	if params, err := msg.DecodeExtraParams(d.ExtraParams); err == nil {
		v.Sculpt = msg.SculptMarkOf(params)
		v.Light, v.Projector = lightsOf(params)
	}
	// A full update carries the appearance as well, and it is the only
	// update most prims ever get: a region sends a compressed one for
	// what it thinks is worth compressing, so waiting for one leaves
	// everything else looking like a prim nothing has textured.
	if len(d.TextureEntry) > 0 {
		v.TextureEntry = d.TextureEntry
	}
	// An update without an animation stops one (llvovolume.cpp:406-441).
	v.TextureAnim = nil
	if len(d.TextureAnim) > 0 {
		v.TextureAnim = d.TextureAnim
	}
	if havePos {
		v.Position, v.Rotation = pos, rot
	}
	// As in a compressed update, the text is what this one says and one
	// without any has none: the viewer clears it for a Text of the
	// terminator alone or nothing (llviewerobject.cpp:1517-1553).
	v.Text = ""
	if len(d.Text) > 1 {
		v.Text = trimNul(d.Text)
	}
	// An AttachItemID in the NameValue is what says this is worn;
	// State means other things on an object that is not.
	if item, ok := attachItem(d.NameValue); ok {
		v.AttachItem, v.AttachPoint = item, attachPoint(d.State)
	}
}

// lightsOf is the light and the projector an update's extra parameters
// carry, each nil when the block is not there.
func lightsOf(params []msg.ExtraParam) (light *msg.Light, projector *msg.LightImage) {
	if l, ok := msg.LightOf(params); ok {
		light = &l
	}
	if p, ok := msg.LightImageOf(params); ok {
		projector = &p
	}
	return light, projector
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
// The owner is taken from here and nowhere else: update does not read
// one out of a full update.
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
	o.linkLocked(v)
	v.Scale, v.Position, v.Rotation = c.Scale, c.Position, c.Rotation
	v.Click, v.ClickKnown = c.Click, true
	v.Sculpt = msg.SculptMarkOf(c.ExtraParams)
	v.Light, v.Projector = lightsOf(c.ExtraParams)
	if !c.Shape.IsZero() {
		v.Shape = c.Shape
	}
	if !c.Owner.IsZero() {
		v.Owner = c.Owner
	}
	if len(c.TextureEntry) > 0 {
		v.TextureEntry = c.TextureEntry
	}
	// Absent here stops an animation too (llvovolume.cpp:608-642).
	v.TextureAnim = c.TextureAnim
	// Every update says what the text is now, and one without any has
	// none: the viewer clears it (llviewerobject.cpp:1865-1895).
	v.Text = c.Text
	// Name-values come this way too, and the viewer reads them from
	// either kind of update (llviewerobject.cpp:1466-1470 and
	// 1955-1961), so an AttachItemID here says this is worn as it does
	// in a full one.
	if item, ok := attachItem([]byte(c.NameValues)); ok {
		v.AttachItem, v.AttachPoint = item, attachPoint(c.State)
	}
}

// judgedLocked records what an update's range check found: in range
// takes the object off notice, out of range puts it on notice if it is
// not already.  It is never dropped here, only by Trim once the notice
// has run out -- the camera an update is judged by can be the moment's
// wrong one, and see outOfRangeGrace for what dropping on the spot cost.
func (o *Objects) judgedLocked(v *Object, far bool) {
	switch {
	case !far:
		v.leaving = time.Time{}
	case v.leaving.IsZero():
		v.leaving = time.Now()
	}
}

// moved records a terse update, with the texture entry its block may
// carry beside the placement.
//
// It only updates something already known.  A terse update names an
// object by local id alone, so one for something never described is
// not enough to make an entry with -- there would be nothing to say
// what it is.
//
// The entry is how the viewer is told of an appearance changed on its
// own: it reads one from a terse update for a prim, and keeps what it
// had when there is none (llvovolume.cpp:650-668).
// Why: doc/objects.md#an-appearance-on-a-terse-update
func (o *Objects) moved(t *msg.Terse, te []byte) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, v := range o.byID {
		if v.Local == t.LocalID {
			v.Position, v.Rotation, v.Velocity = t.Position, t.Rotation, t.Velocity
			v.Last = time.Now()
			v.Moved = v.Last
			if e, ok := terseTextureEntry(te); ok && v.PCode != pcodeAvatar {
				v.TextureEntry = e
			}
			return true
		}
	}
	return false
}

// terseTextureEntry is the entry in a terse update's TextureEntry field,
// which holds it behind a four byte length, as the viewer reads it
// (unpackBinaryData, lldatapacker.cpp:292-320).  An empty field, or one
// whose length does not fit, is none.
func terseTextureEntry(field []byte) ([]byte, bool) {
	if len(field) < 4 {
		return nil, false
	}
	n := binary.LittleEndian.Uint32(field)
	if n == 0 || uint64(n) > uint64(len(field)-4) {
		return nil, false
	}
	return append([]byte(nil), field[4:4+n]...), true
}

// unsure records what a compressed update that did not decode whole
// still says for certain -- its header: where the object is, how big,
// and whose -- and forgets its appearance.  What followed the point
// the blob went wrong cannot be told from what it held before, and an
// appearance the update may have changed is not kept as if it had not:
// the next reader asks the region, as sl.Session.Faces does for one it
// has not got.
func (o *Objects) unsure(c *msg.Compressed, camera msg.Vector3, drawDistance float32) {
	o.mu.Lock()
	defer o.mu.Unlock()

	v := o.seen(c.FullID)
	far := false
	if c.PCode != pcodeAvatar {
		at, judge := c.Position, true
		if v.Parent != 0 {
			at, judge = o.anchorLocked(v.Parent)
		}
		far = judge && !o.keepLocked(at, camera, drawDistance)
	}
	o.judgedLocked(v, far)
	v.Local, v.PCode = c.LocalID, c.PCode
	v.Scale, v.Position, v.Rotation = c.Scale, c.Position, c.Rotation
	// The header decoded, so the click byte is known even though the
	// body is not.
	v.Click, v.ClickKnown = c.Click, true
	if !c.Owner.IsZero() {
		v.Owner = c.Owner
	}
	v.TextureEntry = nil
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
// means turning one into an object.  See internal/server/seat.go.
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
	o.numberLocked(&c, func(p uint32) bool { return o.byLocalLocked(p) != nil })
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

// priced records what the region says an object is sold for, for an
// object that is here, as named does a name.
func (o *Objects) priced(id msg.UUID, price int32) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if v := o.byID[id]; v != nil {
		v.SalePrice, v.Priced = price, true
	}
}

// kill forgets an object by local id.
//
// KillObject names the local id and not the object id, so this is a
// scan.  It happens rarely enough not to matter.
func (o *Objects) kill(local uint32) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, v := range o.byID {
		if v.Local == local {
			delete(o.misordered, v.ID)
			o.forgetLocked(v)
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

// objectLink is the message that links prims, looked up once.
var objectLink = (&msg.ObjectLink{}).MsgInfo().ID

// sent is what the store makes of a message this session has just put
// on the wire.
//
// Two messages matter.  ObjectLink names the order a set's children are
// numbered in, which linked keeps for the update that follows.
// ObjectImage replaces every face of an
// object at once, and nothing the store read described the result when
// that was measured -- a terse update's texture entry was not read then,
// so whether the region says nothing or said it there is open.  Without
// a description the appearance held goes on describing the object as it
// was before the change -- for as long as the session lasts -- and the
// next change, which a client makes by reading every face and sending
// them all back, starts from it.
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
// Why: doc/objects.md#an-appearance-after-objectimage
func (o *Objects) sent(p *msg.Packet) {
	if p == nil || p.Message == nil {
		return
	}
	switch p.ID {
	case objectImage:
		m := &msg.ObjectImage{}
		if !sentAs(p, m) {
			return
		}
		locals := make([]uint32, 0, len(m.ObjectData))
		for i := range m.ObjectData {
			locals = append(locals, m.ObjectData[i].ObjectLocalID)
		}
		o.forgetAppearance(locals...)
	case objectLink:
		m := &msg.ObjectLink{}
		if sentAs(p, m) {
			o.linked(m)
		}
	}
}

// sentAs reads what went out into m.  A client of the daemon sends
// bytes rather than a type, so what goes on the wire for it is a
// msg.Raw, and Encode gives the body back unchanged.
func sentAs(p *msg.Packet, m interface{ Decode([]byte) error }) bool {
	b, err := p.Message.Encode()
	return err == nil && m.Decode(b) == nil
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
		joined := map[uint32][]msg.UUID{}
		store := a.Objects()
		moves := make([]move, 0, len(m.ObjectData))
		for i := range m.ObjectData {
			moves = append(moves, move{m.ObjectData[i].FullID, m.ObjectData[i].ParentID})
		}
		plans := store.planJoins(moves)
		for i := range m.ObjectData {
			d := &m.ObjectData[i]
			a.placements.count(len(d.ObjectData))
			if store.liveJoin(d.FullID, d.ParentID) {
				joined[d.ParentID] = append(joined[d.ParentID], d.FullID)
			}
			store.update(d, l.Center, l.Far)
			parents = a.orphaned(parents, d.ParentID)
		}
		for parent, ids := range joined {
			store.joinedTogether(parent, ids)
			if j := plans[parent]; j != nil {
				store.applyJoin(parent, j)
			}
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
		var unsure []uint32
		joined := map[uint32][]msg.UUID{}
		store := a.Objects()
		type decoded struct {
			c   *msg.Compressed
			err error
		}
		ds := make([]decoded, 0, len(m.ObjectData))
		var moves []move
		for i := range m.ObjectData {
			c, err := msg.DecodeCompressed(m.ObjectData[i].Data)
			ds = append(ds, decoded{c, err})
			if c != nil && err == nil && c.ParentID != nil {
				moves = append(moves, move{c.FullID, *c.ParentID})
			}
		}
		plans := store.planJoins(moves)
		for _, dc := range ds {
			c, err := dc.c, dc.err
			if c == nil {
				continue
			}
			// A partly decoded object still says where it is and what
			// it is, and nothing after the point it went wrong is
			// trusted -- its appearance included, which is asked for
			// again, as a viewer that finds an update bogus asks for
			// the whole object (llvovolume.cpp:538 and 585).
			if err != nil {
				a.Objects().unsure(c, l.Center, l.Far)
				if a.askAgain(c.LocalID) {
					unsure = append(unsure, c.LocalID)
				}
				continue
			}
			if c.ParentID != nil && store.liveJoin(c.FullID, *c.ParentID) {
				joined[*c.ParentID] = append(joined[*c.ParentID], c.FullID)
			}
			store.compressed(c, l.Center, l.Far)
			if c.ParentID != nil {
				parents = a.orphaned(parents, *c.ParentID)
			}
		}
		for parent, ids := range joined {
			store.joinedTogether(parent, ids)
			if j := plans[parent]; j != nil {
				store.applyJoin(parent, j)
			}
		}
		a.askAfter(append(parents, unsure...))
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
			if store.moved(t, m.ObjectData[i].TextureEntry) {
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
	// asked for.  Ignoring it costs almost everything that was in the
	// region before we arrived, while leaving freshly rezzed objects
	// working perfectly -- those arrive as full ObjectUpdates.
	// Why: doc/objects.md#what-the-region-believes-we-already-hold
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
		a.Objects().priced(m.ObjectData.ObjectID, m.ObjectData.SalePrice)
	}, msg.Inline())

	a.Disp.MustHandle("ObjectProperties", func(p *msg.Packet) {
		m := p.Message.(*msg.ObjectProperties)
		for i := range m.ObjectData {
			d := &m.ObjectData[i]
			a.Objects().named(d.ObjectID, trimNul(d.Name), d.OwnerID)
			a.Objects().priced(d.ObjectID, d.SalePrice)
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

// askAgainAfter is how long to leave a local id alone once it has been
// asked about.
//
// Something moving just inside the draw distance is described, kept,
// and trimmed again as it drifts back out, and without this each pass
// would ask afresh several times a second.  Long enough that the churn
// costs one request rather than a stream, short enough that a thing
// which really did arrive and was really missed is not invisible for
// long.
const askAgainAfter = 15 * time.Second

// askAgain says whether this local id may be asked about now, and
// remembers that it was.
func (a *Agent) askAgain(local uint32) bool {
	now := time.Now()

	a.askedMu.Lock()
	defer a.askedMu.Unlock()
	if a.asked == nil {
		a.asked = map[uint32]time.Time{}
	}
	if when, seen := a.asked[local]; seen && now.Sub(when) < askAgainAfter {
		return false
	}
	// Swept here rather than on a timer: this runs only when something
	// unknown turns up, which is exactly when the map grows.
	if len(a.asked) > 4096 {
		for id, when := range a.asked {
			if now.Sub(when) >= askAgainAfter {
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
