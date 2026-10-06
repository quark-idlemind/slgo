package agent

// A face's GLTF material overrides, kept with the object they are on.
// Why: doc/gltf.md#what-is-kept

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	aMaterial   = msg.MustParseUUID("00297e57-7e57-c0de-c06e-7de33a028370")
	aSecondMat  = msg.MustParseUUID("47d27e57-7e57-c0de-33a8-66f518afa49a")
	overrideMsg = func(data string) *msg.GenericStreamingMessage {
		m := &msg.GenericStreamingMessage{}
		m.MethodData.Method = msg.GenericMethodGLTFMaterialOverride
		m.DataBlock.Data = []byte(data)
		return m
	}
)

// renderMaterialBlock is the extra parameters of an object whose faces
// have GLTF materials: one block, type 0x80, a count and a face and an id
// for each.
func renderMaterialBlock(faces map[uint8]msg.UUID) []byte {
	w := &blob{}
	w.u8(1)
	w.u16(0x80)
	w.u32(uint32(1 + 17*len(faces)))
	w.u8(uint8(len(faces)))
	for f := uint8(0); f < 45; f++ {
		if id, ok := faces[f]; ok {
			w.u8(f)
			w.uuid(id)
		}
	}
	return w.b
}

func describe(t *testing.T, a *Agent, id msg.UUID, local uint32, extra []byte) {
	t.Helper()
	feed(t, a, arriving(t, msg.ObjectUpdate_ObjectData{
		ID: local, FullID: id, PCode: 9, ExtraParams: extra,
		ObjectData: placement(msg.Vector3{}, msg.Quaternion{}),
	}))
}

func gltfOf(a *Agent, id msg.UUID) map[int]*msg.GLTFOverride {
	o, _ := a.Objects().Get(id)
	if o == nil {
		return nil
	}
	return o.GLTF
}

// TestAnOverrideIsKeptOnItsObject: every key arrives through the
// dispatcher and is on the object, face by face.
func TestAnOverrideIsKeptOnItsObject(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	a.SetLook(Look{Far: 128})
	describe(t, a, aPrim, 4242, renderMaterialBlock(map[uint8]msg.UUID{2: aMaterial, 3: aMaterial}))

	feed(t, a, overrideMsg(`{'id':i4242,'od':[{'bc':[r1,r0,r0,r0.5],'ds':1,'am':i1},{'mf':r0.25,'rf':r0.75,'ec':[r0,r0.5,r1],'ac':r0.5,`+
		`'tex':[u`+aSecondMat.String()+`],'ti':[{'s':[r3,r3]}]}],'te':[i2,i3]}`))
	got := gltfOf(a, aPrim)
	if len(got) != 2 {
		t.Fatalf("overrides on %d faces, want 2: %+v", len(got), got)
	}
	blend := msg.GLTFAlphaBlend
	yes := true
	if want := (&msg.GLTFOverride{BaseColour: &[4]float32{1, 0, 0, 0.5}, DoubleSided: &yes, AlphaMode: &blend}); !reflect.DeepEqual(got[2], want) {
		t.Errorf("face 2: %+v, want %+v", got[2], want)
	}
	f3 := got[3]
	if f3 == nil || *f3.Metallic != 0.25 || *f3.Roughness != 0.75 || *f3.Emissive != [3]float32{0, 0.5, 1} || *f3.AlphaCutoff != 0.5 ||
		*f3.Textures[msg.GLTFSlotBase] != aSecondMat || *f3.Transforms[msg.GLTFSlotBase].Scale != [2]float32{3, 3} {
		t.Errorf("face 3: %+v", f3)
	}
}

// TestAnOverrideMessageIsTheObjectsWholeSet: a message replaces what the
// object had, so a face it does not name loses its override.
func TestAnOverrideMessageIsTheObjectsWholeSet(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	a.SetLook(Look{Far: 128})
	describe(t, a, aPrim, 4242, nil)

	feed(t, a, overrideMsg(`{'id':i4242,'od':[{'mf':r1},{'mf':r0}],'te':[i1,i2]}`))
	feed(t, a, overrideMsg(`{'id':i4242,'od':[{'rf':r0.5}],'te':[i2]}`))
	got := gltfOf(a, aPrim)
	if len(got) != 1 || got[2] == nil || got[2].Metallic != nil || *got[2].Roughness != 0.5 {
		t.Errorf("after the second message: %+v", got)
	}
}

// TestAClearRemovesTheOverride: undef for each face, and an empty te for
// the whole object.
func TestAClearRemovesTheOverride(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	a.SetLook(Look{Far: 128})
	describe(t, a, aPrim, 4242, nil)

	feed(t, a, overrideMsg(`{'id':i4242,'od':[{'mf':r1},{'mf':r0}],'te':[i2,i3]}`))
	feed(t, a, overrideMsg(`{'id':i4242,'od':[!,!],'te':[i2,i3]}`))
	if got := gltfOf(a, aPrim); got != nil {
		t.Errorf("after a clear: %+v", got)
	}
	feed(t, a, overrideMsg(`{'id':i4242,'od':[{'mf':r1}],'te':[i2]}`))
	feed(t, a, overrideMsg(`{'id':i4242,'od':[],'te':[]}`))
	if got := gltfOf(a, aPrim); got != nil {
		t.Errorf("after an empty te: %+v", got)
	}
}

// TestAnOverrideBeforeItsObjectWaitsForIt: the region may say an override
// before the ObjectUpdate that describes the object, whichever kind of
// update that is.
func TestAnOverrideBeforeItsObjectWaitsForIt(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	a.SetLook(Look{Far: 128})

	feed(t, a, overrideMsg(`{'id':i4242,'od':[{'mf':r0.25}],'te':[i1]}`),
		overrideMsg(`{'id':i77,'od':[{'rf':r0.5}],'te':[i0]}`))
	if n := a.Objects().PendingGLTF(); n != 2 {
		t.Fatalf("%d waiting, want 2", n)
	}
	describe(t, a, aPrim, 4242, nil)
	if got := gltfOf(a, aPrim); len(got) != 1 || *got[1].Metallic != 0.25 {
		t.Errorf("a full update did not adopt it: %+v", got)
	}
	a.Objects().compressed(decodeCompressed(t, compressedObject{id: aChild, local: 77, pcode: 9}), msg.Vector3{}, 0)
	if got := gltfOf(a, aChild); len(got) != 1 || *got[0].Roughness != 0.5 {
		t.Errorf("a compressed update did not adopt it: %+v", got)
	}
	if n := a.Objects().PendingGLTF(); n != 0 {
		t.Errorf("%d still waiting", n)
	}
	// And a later description does not take it away.
	describe(t, a, aPrim, 4242, nil)
	if got := gltfOf(a, aPrim); len(got) != 1 {
		t.Errorf("a second update dropped it: %+v", got)
	}
}

// TestAClearBeforeItsObjectDropsWhatWaits: a waiting override is replaced
// by the next message for the same id, a clear included.
func TestAClearBeforeItsObjectDropsWhatWaits(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	a.SetLook(Look{Far: 128})
	feed(t, a, overrideMsg(`{'id':i4242,'od':[{'mf':r0.25}],'te':[i1]}`), overrideMsg(`{'id':i4242,'od':[!],'te':[i1]}`))
	if n := a.Objects().PendingGLTF(); n != 0 {
		t.Errorf("%d waiting after a clear", n)
	}
}

// TestAKillDropsTheOverride: the override is on the object, so the object
// going takes it, and a new object that is given the same local id does not
// start with it.  A waiting one for a killed id goes too.
func TestAKillDropsTheOverride(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	a.SetLook(Look{Far: 128})
	describe(t, a, aPrim, 4242, nil)
	feed(t, a, overrideMsg(`{'id':i4242,'od':[{'mf':r1}],'te':[i0]}`))

	kill := &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: 4242}}}
	feed(t, a, kill)
	if _, ok := a.Objects().Get(aPrim); ok {
		t.Fatal("the object survived its kill")
	}
	describe(t, a, aChild, 4242, nil)
	if got := gltfOf(a, aChild); got != nil {
		t.Errorf("an object with a reused local id has %+v", got)
	}

	feed(t, a, overrideMsg(`{'id':i99,'od':[{'mf':r1}],'te':[i0]}`))
	feed(t, a, &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: 99}}})
	if n := a.Objects().PendingGLTF(); n != 0 {
		t.Errorf("%d waiting after a kill of their id", n)
	}
}

// TestTrimAndFlushTakeTheOverridesWithTheObject: nothing outlives the
// object it is on.
func TestTrimAndFlushTakeTheOverridesWithTheObject(t *testing.T) {
	t.Parallel()
	o := newObjects()
	far := msg.Vector3{X: 900, Y: 900}
	for i, id := range []msg.UUID{aPrim, aChild} {
		o.compressed(decodeCompressed(t, compressedObject{id: id, local: uint32(10 + i), pcode: 9, position: far}), msg.Vector3{}, 0)
		o.setGLTF(&msg.GLTFOverrideUpdate{Local: uint32(10 + i), Faces: map[int]*msg.GLTFOverride{0: {Metallic: f32(1)}}})
	}
	o.setGLTF(&msg.GLTFOverrideUpdate{Local: 500, Faces: map[int]*msg.GLTFOverride{0: {Metallic: f32(1)}}})
	if n := o.Flush(); n != 2 {
		t.Fatalf("flushed %d", n)
	}
	if o.PendingGLTF() != 0 || o.Count() != 0 {
		t.Errorf("after a flush: %d waiting, %d objects", o.PendingGLTF(), o.Count())
	}

	// Trim: out of range for two ticks, then gone with its override.
	o.compressed(decodeCompressed(t, compressedObject{id: aPrim, local: 10, pcode: 9, position: far}), msg.Vector3{}, 0)
	o.setGLTF(&msg.GLTFOverrideUpdate{Local: 10, Faces: map[int]*msg.GLTFOverride{0: {Metallic: f32(1)}}})
	o.Trim(msg.Vector3{}, 64)
	if v, _ := o.Get(aPrim); v == nil || v.GLTF == nil {
		t.Fatal("on notice, the object and its override stay")
	}
	o.mu.Lock()
	o.byID[aPrim].leaving = time.Now().Add(-time.Hour)
	o.mu.Unlock()
	if n := o.Trim(msg.Vector3{}, 64); n != 1 {
		t.Fatalf("trimmed %d", n)
	}
	if _, ok := o.Get(aPrim); ok {
		t.Error("the object is still there")
	}
}

func f32(f float32) *float32 { return &f }

// TestWaitingOverridesAreBounded: no more than maxPendingGLTF wait, the
// oldest going first, and one that has waited past its age is dropped by
// Trim even when the draw distance says to trim nothing.
func TestWaitingOverridesAreBounded(t *testing.T) {
	t.Parallel()
	o := newObjects()
	now := time.Unix(1_000_000, 0)
	o.now = func() time.Time { return now }
	for i := 0; i < maxPendingGLTF+10; i++ {
		now = now.Add(time.Millisecond)
		o.setGLTF(&msg.GLTFOverrideUpdate{Local: uint32(1000 + i), Faces: map[int]*msg.GLTFOverride{0: {Metallic: f32(1)}}})
	}
	if n := o.PendingGLTF(); n != maxPendingGLTF {
		t.Fatalf("%d waiting, want the bound %d", n, maxPendingGLTF)
	}
	o.mu.RLock()
	_, oldest := o.pendingGLTF[1000]
	_, newest := o.pendingGLTF[uint32(1000+maxPendingGLTF+9)]
	o.mu.RUnlock()
	if oldest || !newest {
		t.Errorf("the oldest waiting is kept: %v, the newest is: %v", oldest, newest)
	}

	now = now.Add(pendingGLTFAge + time.Second)
	if n := o.Trim(msg.Vector3{}, 0); n != 0 || o.PendingGLTF() != 0 {
		t.Errorf("after the age: trimmed %d, %d waiting", n, o.PendingGLTF())
	}
}

// TestAnExpiredOverrideIsNotAdopted: one that has waited past its age is
// not given to an object that arrives late and has not been swept yet.
func TestAnExpiredOverrideIsNotAdopted(t *testing.T) {
	t.Parallel()
	o := newObjects()
	now := time.Unix(1_000_000, 0)
	o.now = func() time.Time { return now }
	o.setGLTF(&msg.GLTFOverrideUpdate{Local: 10, Faces: map[int]*msg.GLTFOverride{0: {Metallic: f32(1)}}})
	now = now.Add(pendingGLTFAge + time.Second)
	o.compressed(decodeCompressed(t, compressedObject{id: aPrim, local: 10, pcode: 9}), msg.Vector3{}, 0)
	if v, _ := o.Get(aPrim); v.GLTF != nil || o.PendingGLTF() != 0 {
		t.Errorf("adopted %+v, %d waiting", v.GLTF, o.PendingGLTF())
	}
}

// TestAStoreThatTakesAnotherTakesItsWaitingOverrides: an agent's store of
// its own before the region is named is joined to the region's.
func TestAStoreThatTakesAnotherTakesItsWaitingOverrides(t *testing.T) {
	t.Parallel()
	old, shared := newObjects(), newObjects()
	old.setGLTF(&msg.GLTFOverrideUpdate{Local: 10, Faces: map[int]*msg.GLTFOverride{0: {Metallic: f32(1)}}})
	shared.absorb(old)
	if shared.PendingGLTF() != 1 {
		t.Errorf("%d waiting", shared.PendingGLTF())
	}
	shared.compressed(decodeCompressed(t, compressedObject{id: aPrim, local: 10, pcode: 9}), msg.Vector3{}, 0)
	if v, _ := shared.Get(aPrim); len(v.GLTF) != 1 {
		t.Errorf("not adopted: %+v", v.GLTF)
	}
}

// TestOtherMethodsAndMalformedOnesAreIgnored.
func TestOtherMethodsAndMalformedOnesAreIgnored(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	a.SetLook(Look{Far: 128})
	describe(t, a, aPrim, 4242, nil)
	other := overrideMsg(`{'id':i4242,'od':[{'mf':r1}],'te':[i0]}`)
	other.MethodData.Method = 0x1234
	feed(t, a, other, overrideMsg(`{'id':i4242,'od':[{'mf':r1}]}`), overrideMsg(`not notation`))
	if got := gltfOf(a, aPrim); got != nil || a.Objects().PendingGLTF() != 0 {
		t.Errorf("kept %+v, %d waiting", got, a.Objects().PendingGLTF())
	}
}

// TestAFacesMaterialIsWhatTheLastFullOrCompressedUpdateSaid: the render
// material block says which faces have a GLTF material; an update without
// it says none, and a terse one leaves it alone.
// Why: doc/gltf.md#the-faces-material
func TestAFacesMaterialIsWhatTheLastFullOrCompressedUpdateSaid(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	a.SetLook(Look{Far: 128})
	mats := func(id msg.UUID) map[int]msg.UUID {
		o, _ := a.Objects().Get(id)
		return o.RenderMaterials
	}
	describe(t, a, aPrim, 4242, renderMaterialBlock(map[uint8]msg.UUID{0: aMaterial, 5: aSecondMat}))
	if got, want := mats(aPrim), (map[int]msg.UUID{0: aMaterial, 5: aSecondMat}); !reflect.DeepEqual(got, want) {
		t.Errorf("materials %v, want %v", got, want)
	}
	describe(t, a, aPrim, 4242, nil)
	if got := mats(aPrim); got != nil {
		t.Errorf("an update with no block left %v", got)
	}

	o := newObjects()
	o.compressed(decodeCompressed(t, compressedObject{id: aPrim, local: 4, pcode: 9,
		extra: renderMaterialBlock(map[uint8]msg.UUID{1: aMaterial})}), msg.Vector3{}, 0)
	if v, _ := o.Get(aPrim); !reflect.DeepEqual(v.RenderMaterials, map[int]msg.UUID{1: aMaterial}) {
		t.Errorf("a compressed update: %v", v.RenderMaterials)
	}
	if !o.moved(&msg.Terse{LocalID: 4}, nil) {
		t.Fatal("the terse update found nothing to move")
	}
	if v, _ := o.Get(aPrim); v.RenderMaterials == nil {
		t.Error("a terse update forgot the materials")
	}
}

// TestTheOverridesOfManyObjectsAreEachKept: the login burst, as the
// region sends it, with the objects described after some of it.
func TestTheOverridesOfManyObjectsAreEachKept(t *testing.T) {
	t.Parallel()
	o := newObjects()
	for i := 0; i < 100; i++ {
		o.setGLTF(&msg.GLTFOverrideUpdate{Local: uint32(i), Faces: map[int]*msg.GLTFOverride{i % 8: {Roughness: f32(float32(i))}}})
		if i%2 == 0 {
			id := msg.UUID{byte(i + 1)}
			o.compressed(decodeCompressed(t, compressedObject{id: id, local: uint32(i), pcode: 9}), msg.Vector3{}, 0)
		}
	}
	for i := 1; i < 100; i += 2 {
		id := msg.UUID{byte(i + 1)}
		o.compressed(decodeCompressed(t, compressedObject{id: id, local: uint32(i), pcode: 9}), msg.Vector3{}, 0)
	}
	for i := 0; i < 100; i++ {
		v, _ := o.Get(msg.UUID{byte(i + 1)})
		if v == nil || v.GLTF[i%8] == nil || *v.GLTF[i%8].Roughness != float32(i) {
			t.Fatalf("object %d: %+v", i, v)
		}
	}
	if o.PendingGLTF() != 0 {
		t.Error(fmt.Sprint(o.PendingGLTF(), " waiting"))
	}
}
