package agent

// What the region said about itself, and whether the session heard it
// right.
//
// A region describes its objects once, when the avatar arrives, and the
// protocol offers no way to ask again -- so a field misread here is
// gone for the life of the session, and it goes wrong quietly.  The
// blobs an ObjectUpdate and its compressed cousin carry are packed by
// hand, with optional parts that are present only when a flag says so,
// and skipping one by the wrong number of bytes reads the next field
// out of the middle of the last.  The result still looks like an
// object.
//
// So the bytes below are laid out from the field layout rather than
// produced by the encoder that would read them back: a round trip
// through one piece of code proves only that it disagrees with nothing.
// What is asserted is that a blob with a known position in it comes out
// of the cache holding that position.

import (
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// blob writes the packed little endian forms the object messages use.
type blob struct{ b []byte }

func (w *blob) u8(v uint8)    { w.b = append(w.b, v) }
func (w *blob) u16(v uint16)  { w.b = binary.LittleEndian.AppendUint16(w.b, v) }
func (w *blob) u32(v uint32)  { w.b = binary.LittleEndian.AppendUint32(w.b, v) }
func (w *blob) f32(v float32) { w.u32(math.Float32bits(v)) }
func (w *blob) uuid(u msg.UUID) {
	w.b = append(w.b, u[:]...)
}
func (w *blob) vec(v msg.Vector3)     { w.f32(v.X); w.f32(v.Y); w.f32(v.Z) }
func (w *blob) quat(q msg.Quaternion) { w.f32(q.X); w.f32(q.Y); w.f32(q.Z) }
func (w *blob) cstr(s string)         { w.b = append(w.b, s...); w.b = append(w.b, 0) }
func (w *blob) raw(b []byte)          { w.b = append(w.b, b...) }

// placement lays out the sixty byte form an ObjectUpdate carries for a
// prim: position, velocity, acceleration, rotation and angular
// velocity, three floats each.  The width is the only thing that says
// which form it is, so the length matters as much as the contents.
func placement(pos msg.Vector3, rot msg.Quaternion) []byte {
	w := &blob{}
	w.vec(pos)
	w.vec(msg.Vector3{}) // velocity
	w.vec(msg.Vector3{}) // acceleration
	w.quat(rot)
	w.vec(msg.Vector3{}) // angular velocity
	return w.b
}

// terseBlob lays out the forty-four byte form an
// ImprovedTerseObjectUpdate carries for a prim.  The position is three
// plain floats and everything after it is sixteen bit fractions of a
// range, which is the part that is easy to get wrong.
func terseBlob(local uint32, pos msg.Vector3) []byte {
	w := &blob{}
	w.u32(local)
	w.u8(0) // state
	w.u8(0) // not an avatar, so no collision plane follows
	w.vec(pos)
	for range 6 { // velocity and acceleration, three each
		w.u16(32767)
	}
	// Rotation runs from minus one to one, and all four components are
	// sent here even though only three are kept.
	w.u16(65535)  // X, the top of the range
	w.u16(0)      // Y, the bottom
	w.u16(65535)  // Z
	w.u16(0)      // W, dropped on the way in
	for range 3 { // angular velocity
		w.u16(32767)
	}
	return w.b
}

// compressedObject is one object as ObjectUpdateCompressed packs it.
// Everything optional is present only when its flag is, and in the
// order the flags are numbered, so the fields below are written in the
// order they are read.
type compressedObject struct {
	id       msg.UUID
	local    uint32
	pcode    uint8
	scale    msg.Vector3
	position msg.Vector3
	rotation msg.Quaternion
	owner    msg.UUID
	parent   *uint32
	text     string
	texture  []byte
}

// Flags from the viewer's llviewerobject.h, repeated here so the bytes
// are not laid out from the same constants that read them.
const (
	hasText   = 0x04
	hasParent = 0x20
)

func (c compressedObject) bytes() []byte {
	var flags uint32
	if c.parent != nil {
		flags |= hasParent
	}
	if c.text != "" {
		flags |= hasText
	}

	w := &blob{}
	w.uuid(c.id)
	w.u32(c.local)
	w.u8(c.pcode)
	w.u8(0)           // State
	w.u32(0xdeadbeef) // CRC
	w.u8(3)           // Material
	w.u8(0)           // ClickAction
	w.vec(c.scale)
	w.vec(c.position)
	w.quat(c.rotation)
	w.u32(flags)
	w.uuid(c.owner)

	if c.parent != nil {
		w.u32(*c.parent)
	}
	if c.text != "" {
		w.cstr(c.text)
		w.u8(255) // the colour it is drawn in
		w.u8(255)
		w.u8(255)
		w.u8(0)
	}
	w.u8(0) // no extra parameters

	// The shape: eighteen fields whose widths are as easy to get wrong
	// as anything optional, and everything after them moves if they are.
	w.u8(16)  // PathCurve
	w.u8(1)   // ProfileCurve
	w.u16(0)  // PathBegin
	w.u16(0)  // PathEnd
	w.u8(100) // PathScaleX
	w.u8(100) // PathScaleY
	w.u8(0)   // PathShearX
	w.u8(0)   // PathShearY
	w.u8(0)   // PathTwist
	w.u8(0)   // PathTwistBegin
	w.u8(0)   // PathRadiusOffset
	w.u8(0)   // PathTaperX
	w.u8(0)   // PathTaperY
	w.u8(0)   // PathRevolutions
	w.u8(0)   // PathSkew
	w.u16(0)  // ProfileBegin
	w.u16(0)  // ProfileEnd
	w.u16(0)  // ProfileHollow

	w.u32(uint32(len(c.texture)))
	w.raw(c.texture)
	return w.b
}

var (
	aPrim    = msg.MustParseUUID("14ff7e57-7e57-c0de-3d5f-1eb0c71e7610")
	aChild   = msg.MustParseUUID("68887e57-7e57-c0de-1dc8-91c8603a504d")
	anOwner  = msg.MustParseUUID("c8f07e57-7e57-c0de-9629-da597abcbade")
	someone  = msg.MustParseUUID("10007e57-7e57-c0de-f13f-8c5ee4b6668a")
	anItem   = msg.MustParseUUID("97da7e57-7e57-c0de-b787-9e0b48a226f1")
	someText = "hello from the prim"
)

// arriving builds an ObjectUpdate holding these blocks, as it comes off
// the wire.
func arriving(t *testing.T, ds ...msg.ObjectUpdate_ObjectData) *msg.ObjectUpdate {
	t.Helper()
	m := &msg.ObjectUpdate{}
	m.ObjectData = ds
	return onTheWire(t, m).(*msg.ObjectUpdate)
}

// TestAFullUpdateIsRememberedFromItsBytes: an ObjectUpdate is the only
// description most objects ever get, and it arrives before any client
// is listening.  Everything the cache holds about a prim comes from
// this one block.
func TestAFullUpdateIsRememberedFromItsBytes(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	a.SetLook(Look{Center: msg.Vector3{X: 128, Y: 128, Z: 25}, Far: 128})

	pos := msg.Vector3{X: 130.5, Y: 127.25, Z: 26}
	rot := msg.Quaternion{X: 0.25, Y: -0.5, Z: 0.75}
	feed(t, a, arriving(t, msg.ObjectUpdate_ObjectData{
		ID:         4242,
		FullID:     aPrim,
		PCode:      9,
		Scale:      msg.Vector3{X: 1, Y: 2, Z: 3},
		ObjectData: placement(pos, rot),
	}))

	got, ok := a.Objects().Get(aPrim)
	if !ok {
		t.Fatal("the object was not remembered")
	}
	if got.Local != 4242 || got.PCode != 9 || got.Parent != 0 {
		t.Errorf("local %d, pcode %d, parent %d", got.Local, got.PCode, got.Parent)
	}
	if got.Scale != (msg.Vector3{X: 1, Y: 2, Z: 3}) {
		t.Errorf("scale = %+v", got.Scale)
	}
	if got.Position != pos {
		t.Errorf("position = %+v, want %+v", got.Position, pos)
	}
	if got.Rotation != rot {
		t.Errorf("rotation = %+v, want %+v", got.Rotation, rot)
	}
	if got.First.IsZero() || got.Last.IsZero() {
		t.Errorf("first %v last %v, both should be set", got.First, got.Last)
	}
	if a.Objects().Count() != 1 || len(a.Objects().All()) != 1 {
		t.Errorf("count = %d, all = %d", a.Objects().Count(), len(a.Objects().All()))
	}
}

// TestTheNarrowPlacementFormsAreAcceptedToo: the placement blob comes in
// several widths and the width is the only thing that says which.  An
// avatar's is sixteen bytes longer than a prim's because it starts with
// a collision plane, and the narrow forms are integers quantised over
// the region rather than floats -- so zero is the bottom of the range
// and not the origin.
func TestTheNarrowPlacementFormsAreAcceptedToo(t *testing.T) {
	t.Parallel()

	// Thirty-two bytes of zeroes: position and rotation both at the
	// bottom of their ranges, which for a position is half a region
	// below the corner.
	quantised := make([]byte, 32)
	// The same, with a collision plane in front of it, which is what an
	// avatar sends.
	avatar := make([]byte, 48)

	for _, c := range []struct {
		name string
		blob []byte
	}{
		{"a prim", quantised},
		{"an avatar, behind its collision plane", avatar},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := newObjects()
			d := &msg.ObjectUpdate_ObjectData{FullID: aPrim, ObjectData: c.blob}
			o.update(d, msg.Vector3{}, 0)

			got, ok := o.Get(aPrim)
			if !ok {
				t.Fatal("not remembered")
			}
			if want := (msg.Vector3{X: -128, Y: -128, Z: -128}); got.Position != want {
				t.Errorf("position = %+v, want %+v", got.Position, want)
			}
			if want := (msg.Quaternion{X: -1, Y: -1, Z: -1}); got.Rotation != want {
				t.Errorf("rotation = %+v, want %+v", got.Rotation, want)
			}
		})
	}
}

// TestAnUnreadablePlacementStillNamesTheObject: a blob of a width
// nothing recognises is not a reason to lose the object.  What it is
// and where it hangs are still known; only where it is is not.
func TestAnUnreadablePlacementStillNamesTheObject(t *testing.T) {
	t.Parallel()

	o := newObjects()
	o.update(&msg.ObjectUpdate_ObjectData{
		ID: 7, FullID: aPrim, PCode: 9, ObjectData: make([]byte, 12),
	}, msg.Vector3{}, 0)

	got, ok := o.Get(aPrim)
	if !ok {
		t.Fatal("an object with an unreadable position was thrown away")
	}
	if got.Local != 7 || got.PCode != 9 {
		t.Errorf("local %d pcode %d", got.Local, got.PCode)
	}
	if got.Position != (msg.Vector3{}) {
		t.Errorf("position = %+v, should have been left alone", got.Position)
	}
}

// TestAWornObjectRemembersTheItemItCameFrom: an attachment is rezzed
// afresh with a new id every time it is put on, so the inventory item is
// the only stable name it has.  It arrives in the NameValue block of the
// update that describes it and nowhere else.
func TestAWornObjectRemembersTheItemItCameFrom(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	a.SetLook(Look{Far: 128})

	feed(t, a, arriving(t, msg.ObjectUpdate_ObjectData{
		ID:         11,
		FullID:     aPrim,
		PCode:      9,
		State:      0x32, // point 35, with its nibbles swapped
		ObjectData: placement(msg.Vector3{}, msg.Quaternion{}),
		NameValue:  []byte("AttachItemID STRING RW DS " + anItem.String() + "\n\x00"),
	}))

	got, ok := a.Objects().Get(aPrim)
	if !ok {
		t.Fatal("not remembered")
	}
	if got.AttachItem != anItem {
		t.Errorf("item = %v, want %v", got.AttachItem, anItem)
	}
	if got.AttachPoint != 35 {
		t.Errorf("attach point = %d, want 35", got.AttachPoint)
	}

	worn := a.Objects().Attachments()
	if len(worn) != 1 || worn[0].ID != aPrim {
		t.Errorf("Attachments = %v", worn)
	}
}

// TestAnObjectThatIsNotWornIsNotAnAttachment: State means other things
// on an object that is not worn, so the AttachItemID is what decides.
func TestAnObjectThatIsNotWornIsNotAnAttachment(t *testing.T) {
	t.Parallel()

	o := newObjects()
	o.update(&msg.ObjectUpdate_ObjectData{
		FullID: aPrim, State: 0x32, ObjectData: placement(msg.Vector3{}, msg.Quaternion{}),
	}, msg.Vector3{}, 0)

	if worn := o.Attachments(); len(worn) != 0 {
		t.Errorf("Attachments = %v, want none", worn)
	}
}

// TestAnUpdateBeyondTheDrawDistanceIsRefused: the simulator does not
// describe what is out of range, so this rejects nothing most of the
// time -- but after the avatar has moved it stops the far end of the old
// view being taken back in by a stray update, and it forgets what is
// already held rather than leaving it.
func TestAnUpdateBeyondTheDrawDistanceIsRefused(t *testing.T) {
	t.Parallel()

	o := newObjects()
	camera := msg.Vector3{X: 128, Y: 128, Z: 25}

	near := &msg.ObjectUpdate_ObjectData{
		ID: 1, FullID: aPrim,
		ObjectData: placement(msg.Vector3{X: 130, Y: 128, Z: 25}, msg.Quaternion{}),
	}
	o.update(near, camera, 64)
	if o.Count() != 1 {
		t.Fatal("something within the draw distance was refused")
	}

	// The same object, now well past the draw distance and its margin.
	far := &msg.ObjectUpdate_ObjectData{
		ID: 1, FullID: aPrim,
		ObjectData: placement(msg.Vector3{X: 900, Y: 128, Z: 25}, msg.Quaternion{}),
	}
	o.update(far, camera, 64)
	if o.Count() != 0 {
		t.Errorf("an object that moved out of range is still held: %d", o.Count())
	}
}

// TestAChildIsJudgedByWhereItsRootIs: a child's position is relative to
// its root, so judging it by its own coordinates would throw away
// everything linked to a prim more than a few metres from the origin.
func TestAChildIsJudgedByWhereItsRootIs(t *testing.T) {
	t.Parallel()

	camera := msg.Vector3{X: 128, Y: 128, Z: 25}

	t.Run("its root is out of range, so it is too", func(t *testing.T) {
		o := newObjects()
		o.update(&msg.ObjectUpdate_ObjectData{
			ID: 1, FullID: aPrim,
			ObjectData: placement(msg.Vector3{X: 130, Y: 128, Z: 25}, msg.Quaternion{}),
		}, camera, 1000)
		// Now judged against a draw distance the root fails.
		o.update(&msg.ObjectUpdate_ObjectData{
			ID: 2, ParentID: 1, FullID: aChild,
			ObjectData: placement(msg.Vector3{X: 0, Y: 0, Z: 1}, msg.Quaternion{}),
		}, msg.Vector3{X: 900}, 64)
		if _, ok := o.Get(aChild); ok {
			t.Error("a child of an out of range root was taken in")
		}
	})

	t.Run("its root has not been described, so it is kept", func(t *testing.T) {
		o := newObjects()
		o.update(&msg.ObjectUpdate_ObjectData{
			ID: 2, ParentID: 99, FullID: aChild,
			ObjectData: placement(msg.Vector3{X: 0, Y: 0, Z: 1}, msg.Quaternion{}),
		}, camera, 64)
		// Updates arrive in no particular order: a child that turned up
		// before its root would otherwise be thrown away and never
		// mentioned again.
		if _, ok := o.Get(aChild); !ok {
			t.Error("an orphan was refused rather than waited for")
		}
	})
}

// TestACompressedUpdateCarriesWhatAFullOneDoesNot: the owner, the
// floating text and the appearance arrive only this way.  A session that
// ignores ObjectUpdateCompressed sees the world as it was on arrival and
// never learns otherwise.
func TestACompressedUpdateCarriesWhatAFullOneDoesNot(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	a.SetLook(Look{Center: msg.Vector3{X: 128, Y: 128, Z: 25}, Far: 128})

	root := uint32(4242)
	m := &msg.ObjectUpdateCompressed{}
	m.ObjectData = []msg.ObjectUpdateCompressed_ObjectData{{
		Data: compressedObject{
			id:       aChild,
			local:    4243,
			pcode:    9,
			scale:    msg.Vector3{X: 4, Y: 5, Z: 6},
			position: msg.Vector3{X: 129, Y: 128, Z: 25},
			rotation: msg.Quaternion{X: 0.5},
			owner:    anOwner,
			parent:   &root,
			text:     someText,
			texture:  []byte{1, 2, 3, 4, 5},
		}.bytes(),
	}}
	feed(t, a, m)

	got, ok := a.Objects().Get(aChild)
	if !ok {
		t.Fatal("the compressed update was not recorded")
	}
	if got.Local != 4243 || got.Parent != root || got.PCode != 9 {
		t.Errorf("local %d parent %d pcode %d", got.Local, got.Parent, got.PCode)
	}
	if got.Scale != (msg.Vector3{X: 4, Y: 5, Z: 6}) {
		t.Errorf("scale = %+v", got.Scale)
	}
	if got.Position != (msg.Vector3{X: 129, Y: 128, Z: 25}) {
		t.Errorf("position = %+v", got.Position)
	}
	if got.Rotation != (msg.Quaternion{X: 0.5}) {
		t.Errorf("rotation = %+v", got.Rotation)
	}
	if got.Owner != anOwner {
		t.Errorf("owner = %v, want %v", got.Owner, anOwner)
	}
	if got.Text != someText {
		t.Errorf("text = %q, want %q", got.Text, someText)
	}
	if string(got.TextureEntry) != string([]byte{1, 2, 3, 4, 5}) {
		t.Errorf("texture entry = %v", got.TextureEntry)
	}
}

// TestACompressedUpdateWithNothingNewLeavesWhatIsKnown: an object with no
// owner, no text and no appearance in the blob must not blank out what an
// earlier update supplied.  Absent and empty are different here.
func TestACompressedUpdateWithNothingNewLeavesWhatIsKnown(t *testing.T) {
	t.Parallel()

	o := newObjects()
	o.compressed(decodeCompressed(t, compressedObject{
		id: aPrim, local: 1, pcode: 9, owner: anOwner,
		text: someText, texture: []byte{7},
	}), msg.Vector3{}, 0)

	o.compressed(decodeCompressed(t, compressedObject{
		id: aPrim, local: 1, pcode: 9,
	}), msg.Vector3{}, 0)

	got, _ := o.Get(aPrim)
	if got.Owner != anOwner || got.Text != someText || len(got.TextureEntry) != 1 {
		t.Errorf("a quiet update erased what was known: %+v", got)
	}
}

// decodeCompressed puts a blob through the decoder the handler uses.
func decodeCompressed(t *testing.T, c compressedObject) *msg.Compressed {
	t.Helper()
	got, err := msg.DecodeCompressed(c.bytes())
	if err != nil {
		t.Fatalf("the blob this test laid out would not decode: %v", err)
	}
	return got
}

// TestACompressedUpdateBeyondTheDrawDistanceIsRefused: the same rule as
// for a full update, and for the same reason.
func TestACompressedUpdateBeyondTheDrawDistanceIsRefused(t *testing.T) {
	t.Parallel()

	camera := msg.Vector3{X: 128, Y: 128, Z: 25}
	o := newObjects()

	o.compressed(decodeCompressed(t, compressedObject{
		id: aPrim, local: 1, position: msg.Vector3{X: 130, Y: 128, Z: 25},
	}), camera, 64)
	if o.Count() != 1 {
		t.Fatal("something in range was refused")
	}

	o.compressed(decodeCompressed(t, compressedObject{
		id: aPrim, local: 1, position: msg.Vector3{X: 900, Y: 128, Z: 25},
	}), camera, 64)
	if o.Count() != 0 {
		t.Error("an object that moved out of range is still held")
	}

	// A child is judged by its root, and one whose root is not known is
	// taken in rather than lost.
	root := uint32(99)
	o.compressed(decodeCompressed(t, compressedObject{
		id: aChild, local: 2, parent: &root, position: msg.Vector3{X: 900},
	}), camera, 64)
	if _, ok := o.Get(aChild); !ok {
		t.Error("an orphan was refused rather than waited for")
	}
}

// TestATruncatedCompressedBlobIsDropped: these come off the network and
// a short one must cost nothing more than itself.
func TestATruncatedCompressedBlobIsDropped(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	a.SetLook(Look{Far: 128})

	full := compressedObject{id: aPrim, local: 1, pcode: 9}.bytes()

	m := &msg.ObjectUpdateCompressed{}
	m.ObjectData = []msg.ObjectUpdateCompressed_ObjectData{
		{Data: full[:20]}, // not even a header
		{Data: full[:84]}, // the header exactly, and nothing after it
	}
	feed(t, a, m)

	// The first is unusable and goes.  The second decoded far enough to
	// say what the object is and where it is, which is what the cache is
	// for, so the error on its tail is worth nothing here.
	if a.Objects().Count() != 1 {
		t.Fatalf("count = %d, want 1", a.Objects().Count())
	}
	got, ok := a.Objects().Get(aPrim)
	if !ok || got.PCode != 9 {
		t.Errorf("a partly decoded object was lost: %+v", got)
	}
}

// TestATerseUpdateMovesSomethingAlreadyKnown: this is the message the
// simulator sends most, several times a second for everything that
// moves.  It names an object by local id alone, so it can only move what
// has already been described.
func TestATerseUpdateMovesSomethingAlreadyKnown(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	a.SetLook(Look{Far: 128})

	feed(t, a, arriving(t, msg.ObjectUpdate_ObjectData{
		ID: 4242, FullID: aPrim, PCode: 9,
		ObjectData: placement(msg.Vector3{X: 1, Y: 2, Z: 3}, msg.Quaternion{}),
	}))

	m := &msg.ImprovedTerseObjectUpdate{}
	m.ObjectData = []msg.ImprovedTerseObjectUpdate_ObjectData{
		{Data: terseBlob(4242, msg.Vector3{X: 40, Y: 50, Z: 60})},
		// One for something never described: there is nothing to say
		// what it is, so it must not make an entry.
		{Data: terseBlob(9999, msg.Vector3{X: 1, Y: 1, Z: 1})},
		// And one too short to hold even a header.
		{Data: []byte{1, 2, 3}},
	}
	feed(t, a, m)

	if a.Objects().Count() != 1 {
		t.Fatalf("count = %d, want 1", a.Objects().Count())
	}
	got, _ := a.Objects().Get(aPrim)
	if want := (msg.Vector3{X: 40, Y: 50, Z: 60}); got.Position != want {
		t.Errorf("position = %+v, want %+v", got.Position, want)
	}
	// The rotation is sixteen bit fractions of minus one to one, so the
	// ends of the range are exact.
	if want := (msg.Quaternion{X: 1, Y: -1, Z: 1}); got.Rotation != want {
		t.Errorf("rotation = %+v, want %+v", got.Rotation, want)
	}
}

// TestKillObjectForgetsIt: KillObject names the local id and not the
// object id, so forgetting one is a scan.
func TestKillObjectForgetsIt(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	a.SetLook(Look{Far: 128})

	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: aPrim,
			ObjectData: placement(msg.Vector3{}, msg.Quaternion{})},
		msg.ObjectUpdate_ObjectData{ID: 2, FullID: aChild,
			ObjectData: placement(msg.Vector3{}, msg.Quaternion{})},
	))

	k := &msg.KillObject{}
	k.ObjectData = []msg.KillObject_ObjectData{{ID: 1}, {ID: 12345}}
	feed(t, a, k)

	if _, ok := a.Objects().Get(aPrim); ok {
		t.Error("the killed object is still held")
	}
	if _, ok := a.Objects().Get(aChild); !ok {
		t.Error("a kill for an unknown local id took something else with it")
	}
}

// TestNamesAndOwnersComeOnlyFromAsking: an ObjectUpdate carries no name
// at all.  Any client may be the one that asked for it, so the answer is
// kept here and the next one does not have to ask again.
func TestNamesAndOwnersComeOnlyFromAsking(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)

	family := &msg.ObjectPropertiesFamily{}
	family.ObjectData.ObjectID = aPrim
	family.ObjectData.OwnerID = anOwner
	family.ObjectData.Name = []byte("a nameless prim\x00")

	props := &msg.ObjectProperties{}
	props.ObjectData = []msg.ObjectProperties_ObjectData{{
		ObjectID: aChild,
		OwnerID:  someone,
		Name:     []byte("the other one\x00"),
	}}
	feed(t, a, family, props)

	got, ok := a.Objects().Get(aPrim)
	if !ok || got.Name != "a nameless prim" || got.Owner != anOwner {
		t.Errorf("ObjectPropertiesFamily gave %+v", got)
	}
	got, ok = a.Objects().Get(aChild)
	if !ok || got.Name != "the other one" || got.Owner != someone {
		t.Errorf("ObjectProperties gave %+v", got)
	}
}

// TestTrimForgetsWhatIsOutOfSight: the simulator says when an object has
// been destroyed but says nothing at all when one is merely left behind,
// and an object sitting still is never mentioned again either.  Ageing
// would throw away the quiet ones and keep the distant ones, which is
// exactly backwards, so distance is what decides.
func TestTrimForgetsWhatIsOutOfSight(t *testing.T) {
	t.Parallel()

	camera := msg.Vector3{X: 128, Y: 128, Z: 25}
	o := newObjects()

	// A root nearby, a root far away, a child of each, and an orphan
	// whose root was never described.
	place := func(id msg.UUID, local, parent uint32, at msg.Vector3) {
		o.update(&msg.ObjectUpdate_ObjectData{
			ID: local, ParentID: parent, FullID: id,
			ObjectData: placement(at, msg.Quaternion{}),
		}, camera, 0)
	}
	near := msg.MustParseUUID("05217e57-7e57-c0de-c87d-5ffa1f2a6d4b")
	nearKid := msg.MustParseUUID("05ac7e57-7e57-c0de-7516-153fed37deef")
	far := msg.MustParseUUID("06a87e57-7e57-c0de-9322-a36c76ad7f40")
	farKid := msg.MustParseUUID("07267e57-7e57-c0de-1a27-e99d1a02e300")
	orphan := msg.MustParseUUID("09077e57-7e57-c0de-e989-1e65417d64ff")
	oldOrphan := msg.MustParseUUID("0aef7e57-7e57-c0de-58f9-75f50bcffe84")

	place(near, 1, 0, msg.Vector3{X: 130, Y: 128, Z: 25})
	place(nearKid, 2, 1, msg.Vector3{X: 0, Y: 0, Z: 1})
	place(far, 3, 0, msg.Vector3{X: 900, Y: 128, Z: 25})
	place(farKid, 4, 3, msg.Vector3{X: 0, Y: 0, Z: 1})
	place(orphan, 5, 77, msg.Vector3{X: 0, Y: 0, Z: 1})
	place(oldOrphan, 6, 88, msg.Vector3{X: 0, Y: 0, Z: 1})

	// A draw distance of nothing means nothing is known about what is
	// visible, and guessing would be worse than leaving it alone.
	if n := o.Trim(camera, 0); n != 0 || o.Count() != 6 {
		t.Fatalf("trimming with no draw distance removed %d", n)
	}

	// An orphan is given a grace period, because updates arrive in no
	// particular order; one older than that is a root that was refused
	// for being out of range, and its children are out of range too.
	o.byID[oldOrphan].First = time.Now().Add(-2 * orphanGrace)

	if n := o.Trim(camera, 64); n != 3 {
		t.Errorf("Trim removed %d, want 3", n)
	}
	for _, c := range []struct {
		id   msg.UUID
		name string
		want bool
	}{
		{near, "a root in range", true},
		{nearKid, "its child", true},
		{far, "a root out of range", false},
		{farKid, "the child that went with it", false},
		{orphan, "a fresh orphan", true},
		{oldOrphan, "an orphan past its grace period", false},
	} {
		if _, ok := o.Get(c.id); ok != c.want {
			t.Errorf("%s: held = %v, want %v", c.name, ok, c.want)
		}
	}
}

// TestFlushForgetsTheRegion: the cache is only correct for the region it
// was filled in, and crossing to another leaves it describing somewhere
// else entirely.  Nothing on the wire says "forget all that".
func TestFlushForgetsTheRegion(t *testing.T) {
	t.Parallel()

	o := newObjects()
	o.update(&msg.ObjectUpdate_ObjectData{FullID: aPrim,
		ObjectData: placement(msg.Vector3{}, msg.Quaternion{})}, msg.Vector3{}, 0)

	if n := o.Flush(); n != 1 {
		t.Errorf("Flush reported %d, want 1", n)
	}
	if o.Count() != 0 {
		t.Error("something survived the flush")
	}
}

// TestTheCacheHandsOutCopies: a caller that changed what it was given
// would be editing the session's picture of the region from outside it.
func TestTheCacheHandsOutCopies(t *testing.T) {
	t.Parallel()

	o := newObjects()
	o.named(aPrim, "as described", anOwner)

	if _, ok := o.Get(someone); ok {
		t.Error("an object nobody mentioned was found")
	}

	got, _ := o.Get(aPrim)
	got.Name = "not as described"
	for _, v := range o.All() {
		v.Name = "nor this"
	}
	if again, _ := o.Get(aPrim); again.Name != "as described" {
		t.Errorf("the cache was edited from outside: %q", again.Name)
	}
}

// TestCachedObjectsAreAskedForOneBatchAtATime: ObjectUpdateCached is the
// simulator saying "you have these already" -- local ids and CRCs and no
// content whatsoever.  This client keeps no disk cache, so every one is
// a miss and every one has to be asked for; ignoring the message costs
// almost everything that was in the region before the avatar arrived.
//
// The request has to fit in a datagram, and a simulator that cannot
// parse an oversized one answers none of it rather than the part that
// fitted, so it goes in hundreds.
func TestCachedObjectsAreAskedForOneBatchAtATime(t *testing.T) {
	t.Parallel()

	a, sent := offlineSession(t)

	m := &msg.ObjectUpdateCached{}
	for i := range 250 {
		m.ObjectData = append(m.ObjectData, msg.ObjectUpdateCached_ObjectData{
			ID: uint32(i + 1), CRC: 7,
		})
	}
	feed(t, a, m)

	// The handler sends off the dispatch goroutine, since this can be
	// several packets and nothing else could be decoded while it did.
	sent.waitFor(t, 3)

	var asked []uint32
	for _, out := range sent.messages(t) {
		req, ok := out.(*msg.RequestMultipleObjects)
		if !ok {
			t.Fatalf("sent a %s, wanted RequestMultipleObjects", out.MsgInfo().Name)
		}
		if n := len(req.ObjectData); n > 100 {
			t.Errorf("a batch held %d blocks, which will not fit", n)
		}
		if req.AgentData.AgentID != a.Account.AgentID {
			t.Error("the request did not say who was asking")
		}
		for _, d := range req.ObjectData {
			if d.CacheMissType != 0 {
				t.Errorf("cache miss type %d, want 0: this client caches nothing", d.CacheMissType)
			}
			asked = append(asked, d.ID)
		}
	}
	if len(asked) != 250 {
		t.Fatalf("asked for %d objects, want 250", len(asked))
	}
	for i, id := range asked {
		if id != uint32(i+1) {
			t.Fatalf("asked for %d at position %d; the batches do not tile", id, i)
		}
	}
}

// TestCachedObjectsStopWhenTheCircuitHasGone: the batches would
// otherwise keep being queued for a session that has ended.
func TestCachedObjectsStopWhenTheCircuitHasGone(t *testing.T) {
	t.Parallel()

	sent := &sentPackets{}
	a := &Agent{Account: &Account{}, Send: msg.NewSender(sent)}

	// Run the sender and stop it, so a send fails the way it does on a
	// session that has gone rather than being quietly buffered.
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); _ = a.Send.Run(ctx) }()
	cancel()
	<-stopped

	ids := make([]uint32, 250)
	for i := range ids {
		ids[i] = uint32(i + 1)
	}
	a.requestCachedObjects(ids)

	if n := sent.count(); n != 0 {
		t.Errorf("%d datagrams went out on a closed circuit", n)
	}
}
