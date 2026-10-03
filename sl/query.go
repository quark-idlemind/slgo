package sl

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
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

	// Groups is every group the avatar has joined, which is what
	// ActiveGroup can be set to.
	//
	// Empty means "not told yet" as much as "belongs to none".  The
	// list is not asked for: the simulator volunteers it shortly after
	// the handshake and again whenever it changes -- see the doc
	// comments on agent.Groups and agent.WaitGroups -- so a read taken
	// in the seconds after login can find nothing and be wrong.  There
	// is no way here to tell the two apart, and anything that says
	// "this avatar is in no groups" is claiming more than it knows.
	Groups []Group

	// MaturityPreference is the highest rating of land this avatar has
	// asked to be shown, and MaturityCeiling the highest the account is
	// permitted to ask for -- in the grid's letters, MaturityGeneral,
	// MaturityModerate or MaturityAdult.  See the head of maturity.go
	// for why they are different numbers and only the first can be set.
	//
	// Both come from the login response, and the preference then
	// follows what SetMaturity is granted.  A change made anywhere else
	// is not heard of: agent.Maturity says where, and why.
	//
	// Empty is "not told": a login response that did not carry the
	// field, or a daemon built before it passed them on.  It is never
	// General, and reading it as General is how a caller comes to think
	// an avatar will be refused land it would have been let onto.
	MaturityPreference string
	MaturityCeiling    string

	// ScriptControlsTaken and ScriptControlsPassedOn are the controls
	// scripts have taken with llTakeControls, as agent.Control* bits:
	// the first the ones a script keeps to itself, which do not move
	// the avatar, and the second the ones it passes on, which do both.
	// Zero is none.  See agent.Agent.ScriptControls.
	ScriptControlsTaken    uint32
	ScriptControlsPassedOn uint32

	// Health is the avatar's health, 0 to 100, as the last
	// HealthMessage said it, and HealthKnown whether one has come: a
	// region with damage enabled sends them, and full health and not
	// having been told are different answers.
	Health      float32
	HealthKnown bool
}

// Group is one of the avatar's memberships, as AgentGroupDataUpdate
// describes it.
type Group struct {
	ID   msg.UUID
	Name string

	// Powers is what the membership is allowed to do, as the bitfield
	// the simulator sends.  Nothing in this package interprets it.
	Powers uint64
}

// Where reports the avatar's position and view.
//
// Position is exact after a teleport or entering a region, and while
// walking is whole metres across and four up and down, which is all
// CoarseLocationUpdate carries.  A seated avatar's is worked out from
// its seat; see agent.Agent.Position.
func (w *Session) Where(ctx context.Context) (*Presence, error) {
	return w.presence(ctx, 0)
}

// SetDrawDistance changes how far the simulator is asked to describe
// things, and returns the presence it ended up with.
//
// This is the "in the world" of ObjectsNamed and AllObjects: nothing
// beyond it is described at all, so nothing beyond it can be found.
//
// Neither way is at once.  Raising it brings more in over the following
// seconds, so a raise wants a Settle after it.  Lowering it lets the
// session drop what is then out of range, people apart, at a trim every
// agent.TrimInterval once agent.OutOfRangeGrace has passed -- so for
// half a minute or so what was described is still found.
// Why: doc/objects.md#before-trim
func (w *Session) SetDrawDistance(ctx context.Context, metres float32) (*Presence, error) {
	if metres <= 0 {
		return nil, fmt.Errorf("sl: draw distance must be positive, got %v", metres)
	}
	return w.presence(ctx, metres)
}

func (w *Session) presence(ctx context.Context, set float32) (*Presence, error) {
	return w.b.Presence(ctx, set)
}

// Inventory fetches the whole inventory tree over AIS.
//
// The UDP FetchInventoryDescendents this replaces was retired: the
// simulator accepts the request and never answers.
func (w *Session) Inventory(ctx context.Context) (*agent.Inventory, error) {
	inv := agent.NewInventory(w.invRoot)
	if err := agent.FetchInventory(ctx, w.b, inv, agent.FetchOptions{}); err != nil {
		return nil, fmt.Errorf("sl: reading inventory: %w", err)
	}
	return inv, nil
}

// The values of Seen.Click, Linden's published LSL CLICK_ACTION_*
// constants, as Firestorm's indra_constants.h has them too.
// CLICK_ACTION_NONE is 0, the same byte as touch.  Taken from the
// published list, not measured.
const (
	ClickTouch     uint8 = 0 // CLICK_ACTION_TOUCH
	ClickSit       uint8 = 1 // CLICK_ACTION_SIT
	ClickBuy       uint8 = 2 // CLICK_ACTION_BUY
	ClickPay       uint8 = 3 // CLICK_ACTION_PAY
	ClickOpen      uint8 = 4 // CLICK_ACTION_OPEN
	ClickPlay      uint8 = 5 // CLICK_ACTION_PLAY
	ClickOpenMedia uint8 = 6 // CLICK_ACTION_OPEN_MEDIA
	ClickZoom      uint8 = 7 // CLICK_ACTION_ZOOM
	ClickDisabled  uint8 = 8 // CLICK_ACTION_DISABLED
	ClickIgnore    uint8 = 9 // CLICK_ACTION_IGNORE
)

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

	// TextureAnim is the texture animation, still packed, and nil when
	// the object has none.
	TextureAnim []byte

	// Click is the click action byte (ClickTouch and the rest); a zero
	// Click with ClickKnown false means no update has said, and with
	// true means touch.
	Click      uint8
	ClickKnown bool

	// Sculpt marks a sculpt or a mesh, with the texture or asset that
	// holds its shape; its zero value is a prim that is neither.  It
	// is what Seen.FaceCount asks before counting a shape's faces.
	Sculpt msg.SculptMark

	// Shape is the prim's profile and path, still packed.  Form
	// unpacks it into something with names.
	Shape msg.PrimShape

	// LinkNumber is the number a viewer gives this prim in its
	// linkset: 0 when it is not linked, 1 for the root of one that has
	// children, and 2 and up for a child.  It is only as good as the
	// updates heard since the region was last flushed.
	// Why: doc/objects.md#link-numbers
	LinkNumber int

	// LinkKnown says LinkNumber can be believed: true for a prim that is
	// not linked, and for the rest the state of its linkset, which is
	// unknown after a link or unlink made while the store watched.
	// Why: doc/objects.md#link-numbers
	LinkKnown bool

	// Text is the floating text above the object.
	Text string

	// AttachPoint is where a worn object is attached and AttachItem
	// the inventory item it was worn from; both are zero when it is
	// not worn.  See Session.WornFromItem.
	AttachPoint int
	AttachItem  msg.UUID
}

// Faces unpacks the appearance, if any has been seen.
func (s *Seen) Faces(count int) ([]Face, error) {
	if len(s.TextureEntry) == 0 {
		return nil, fmt.Errorf("sl: nothing has described the faces of %s", s.ID)
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
func (w *Session) ObjectByID(ctx context.Context, id msg.UUID, timeout time.Duration) (*Seen, error) {
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	found, err := w.fetch(ctx, "", id.String())
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("sl: %s is not in the region, or is beyond the draw distance", id)
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
func (w *Session) AllObjects(ctx context.Context, timeout time.Duration) ([]*Seen, error) {
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

// ErrLinkOrderUnknown is what Linkset returns for a linkset whose order
// the store cannot vouch for: one linked or unlinked while it was
// watching, which the region describes in an order that is not the link
// order.  It is wrapped with the root's id; test it with errors.Is.
// Why: doc/objects.md#link-numbers
var ErrLinkOrderUnknown = errors.New("sl: the order of the linkset is not known")

// Linkset returns root and its children, root first and then the
// children in link order, so that each one's place in the list is its
// LinkNumber less one.
//
// root has to be the root of a linkset; a prim that is linked under
// another is refused rather than guessed at.  A prim with no children
// is a linkset of one.  The order is the one the store kept from the
// updates it heard, which is the viewer's; see Seen.LinkNumber.  While
// that order is not known it returns ErrLinkOrderUnknown rather than a
// list that may be in the wrong order.
// Why: doc/objects.md#link-numbers
func (w *Session) Linkset(ctx context.Context, root *Object) ([]*Seen, error) {
	if root == nil {
		return nil, errors.New("sl: no object to list the links of")
	}
	all, err := w.fetch(ctx, "", "")
	if err != nil {
		return nil, err
	}
	var top *Seen
	for _, s := range all {
		if s.ID == root.ID {
			top = s
			break
		}
	}
	if top == nil {
		return nil, fmt.Errorf("sl: %s is not in the region, or is beyond the draw distance", root.ID)
	}
	// A worn object's root has the avatar for its parent, and is the
	// root of its own linkset all the same.
	if top.Parent != 0 && !wornUnder(all, top.Parent) {
		return nil, fmt.Errorf("sl: %s is linked under local id %d, not the root of a linkset", root.ID, top.Parent)
	}
	// Whether the order is known is the state of the root's own set,
	// which each child carries; a worn root's own flag is the avatar's.
	out := []*Seen{top}
	for _, s := range all {
		if s.Parent == top.Local && s.PCode != pcodeAvatar {
			if !s.LinkKnown {
				return nil, fmt.Errorf("%s: %w", root.ID, ErrLinkOrderUnknown)
			}
			if s.LinkNumber >= 2 {
				out = append(out, s)
			}
		}
	}
	if top.Parent == 0 && len(out) == 1 && !top.LinkKnown {
		return nil, fmt.Errorf("%s: %w", root.ID, ErrLinkOrderUnknown)
	}
	sort.Slice(out[1:], func(i, j int) bool { return out[1+i].LinkNumber < out[1+j].LinkNumber })
	return out, nil
}

// wornUnder says whether the prim with this local id is an avatar, which
// makes what is linked under it a worn object's root.
func wornUnder(all []*Seen, local uint32) bool {
	for _, s := range all {
		if s.Local == local {
			return s.PCode == pcodeAvatar
		}
	}
	return false
}

// ObjectsNamed returns every object in the region with a given name.
//
// Every object has to have been asked about for its name to be known,
// so the first call costs what AllObjects costs.  A region with
// nothing of that name in it is not distinguishable from one where the
// thing is beyond the draw distance.
func (w *Session) ObjectsNamed(ctx context.Context, name string, timeout time.Duration) ([]*Seen, error) {
	if _, err := w.AllObjects(ctx, timeout); err != nil {
		return nil, err
	}
	return w.fetch(ctx, name, "")
}

// Known is how many objects the session has heard about, without
// naming any of them.
func (w *Session) Known(ctx context.Context) (int, error) {
	all, err := w.b.Objects(ctx, "", "")
	if err != nil {
		return 0, err
	}
	return len(all), nil
}

// fetch asks whoever holds the session for its picture of the region.
//
// Every object that comes back is marked with the visit it was found in,
// taken before asking: an answer that crosses a region change is then
// marked with the visit it may have come from, and is looked up again
// rather than trusted.  Each is a copy, so the mark is never written
// into something the backend is still holding.
//
// The order is settled here rather than left to the backend, so that
// the two answer alike: a map has no order, and one of them iterates a
// map to build the list.
func (w *Session) fetch(ctx context.Context, named, id string) ([]*Seen, error) {
	v := w.here(ctx)
	got, err := w.b.Objects(ctx, named, id)
	if err != nil {
		return nil, err
	}
	out := make([]*Seen, len(got))
	for i, s := range got {
		c := *s
		c.from = v
		out[i] = &c
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
func (w *Session) resolve(ctx context.Context, want []msg.UUID, timeout time.Duration) error {
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
			if _, ok := w.objectNames[id]; ok {
				have++
			}
		}
		w.mu.Unlock()
		if have == len(want) {
			return nil
		}
		if have == last {
			// Answers have stopped, which does not mean everything
			// that is going to be named has been: what is left may be
			// the kind this request is never answered for.  Stop
			// waiting, but go on to ask the other way.
			if quiet++; quiet >= quietRounds {
				break
			}
		} else {
			quiet = 0
		}
		last = have
		if err := w.Settle(ctx, time.Second); err != nil {
			return err
		}
	}
	return w.selectForNames(ctx, want, deadline)
}

// selectByBatch is how many objects one ObjectSelect names at a time.
// The message takes a block per object, so this is one packet rather
// than sixty-four.
const selectByBatch = 64

// selectForNames asks again, by selecting, for the ones the family
// request never answered.
//
// A RequestObjectPropertiesFamily is answered for a root prim and NOT
// for a child of a linkset -- measured: a child prim stayed nameless
// through the whole resolve, and one ObjectSelect named it
// "HearthEmbers" immediately.  That is the difference between what
// "objects" could see and what "dump" could: dump selects.
//
// Selecting is cheaper here than asking, since one message carries
// many objects where the family request carries one.  What it is not
// is free of meaning: the simulator now believes these are being
// edited, and an object another avatar has selected is one they cannot
// always move.  So the selection is given back immediately.
func (w *Session) selectForNames(ctx context.Context, want []msg.UUID, deadline time.Time) error {
	if !time.Now().Before(deadline) {
		return nil
	}
	seen, err := w.fetch(ctx, "", "")
	if err != nil {
		return err
	}
	var locals []uint32
	for _, s := range seen {
		// Avatars are named by a name lookup, not by asking the object
		// what it is called, and selecting one is a strange thing to
		// do to somebody.
		if s.Name != "" || s.IsAvatar() {
			continue
		}
		for _, id := range want {
			if s.ID == id {
				if l, err := w.local(ctx, &s.Object); err == nil {
					locals = append(locals, l)
				}
				break
			}
		}
	}
	if len(locals) == 0 {
		return nil
	}

	for i := 0; i < len(locals); i += selectByBatch {
		batch := locals[i:min(i+selectByBatch, len(locals))]
		if err := w.Send(ctx, w.selectMsg(batch...)); err != nil {
			return err
		}
		if err := w.Settle(ctx, 500*time.Millisecond); err != nil {
			return err
		}
		if err := w.Send(ctx, w.deselectMsg(batch...)); err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return nil
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

	// Flags and Extended are as the latest SimStats had them, once one
	// has come, so an estate change made while the avatar is there is
	// seen.  ObjectCapacity comes only in SimStats, and is zero until
	// the first.
	Flags          uint32
	Extended       uint64
	ObjectCapacity uint32

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
func (w *Session) Region(ctx context.Context) (*Region, error) {
	r, known, err := w.b.Region(ctx)
	if err != nil {
		return nil, err
	}
	if !known {
		return nil, fmt.Errorf("sl: the region handshake has not arrived")
	}
	return r, nil
}

// Flush empties the server's object cache.
//
// The server does this itself when the region changes.  This is for a
// client that knows the cache is wrong for a reason the server cannot
// see.
func (w *Session) Flush(ctx context.Context) (int, error) {
	return w.b.Flush(ctx)
}

// parseUUIDOrZero reads a uuid off the wire, where ids travel as
// strings, treating anything unreadable as none: a zero uuid and an
// absent one mean the same thing here -- no group, no owner.
func parseUUIDOrZero(s string) msg.UUID {
	id, err := msg.ParseUUID(s)
	if err != nil {
		return msg.UUID{}
	}
	return id
}

// ActiveGroup is the group the avatar is acting as.
func (w *Session) ActiveGroup(ctx context.Context) (msg.UUID, error) {
	p, err := w.Where(ctx)
	if err != nil {
		return msg.UUID{}, err
	}
	return p.ActiveGroup, nil
}

// Form is what this prim is shaped like: a box, a sphere, a torus with
// a hole in it.
//
// The second result says whether anything has described the prim at
// all.  An object listed by name alone -- because something asked for
// its name and nothing else -- has no shape yet, and a default box is
// a plausible wrong answer rather than an obvious gap.
func (s *Seen) Form() (Shape, bool) {
	if s.Shape.IsZero() {
		return Shape{}, false
	}
	return UnpackShape(s.Shape), true
}

// FaceCount is how many texture faces the prim has -- the number
// llGetNumberOfSides gives -- and whether that can be said: it cannot
// before an update has described the prim, nor for a sculpt or a mesh,
// whose faces the shape fields do not give.  Asking Form and then
// Shape.Faces without looking at Sculpt gets a wrong count for both.
func (s *Seen) FaceCount() (int, bool) {
	shape, ok := s.Form()
	if !ok || s.Sculpt.Kind != msg.SculptNone {
		return 0, false
	}
	return shape.Faces()
}
