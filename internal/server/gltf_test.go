package server

// A face's GLTF material overrides, from the region to a client.
// Why: doc/gltf.md#how-slate-reaches-it

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var (
	aGlossyBox  = msg.MustParseUUID("4f1e7e57-7e57-c0de-990f-e99bacb16581")
	aGlossyMat  = msg.MustParseUUID("773e7e57-7e57-c0de-43e1-366bc84d7559")
	aGlossyTex  = msg.MustParseUUID("9fc77e57-7e57-c0de-99c0-951e34f252b6")
	overrideKey = "ModifyMaterialParams"
)

// materialBlock is the extra parameters of an object with a GLTF
// material on face 1 and face 2: one block of type 0x80.
func materialBlock(id msg.UUID) []byte {
	b := []byte{1, 0x80, 0, 35, 0, 0, 0, 2}
	for _, face := range []byte{1, 2} {
		b = append(b, face)
		b = append(b, id[:]...)
	}
	return b
}

func overrideMessage(data string) *msg.GenericStreamingMessage {
	m := &msg.GenericStreamingMessage{}
	m.MethodData.Method = msg.GenericMethodGLTFMaterialOverride
	m.DataBlock.Data = []byte(data)
	return m
}

// TestAnOverrideReachesAClientThatNeverSubscribed: the overrides are kept
// by the daemon from login, with the object, and a client that attaches
// after the region said them reads them with the object -- through the
// Objects call, with no relay of GenericStreamingMessage, which sl does
// not subscribe to.  Every field of an override crosses, and a face
// the override does not name is absent, not zero.
func TestAnOverrideReachesAClientThatNeverSubscribed(t *testing.T) {
	r := newRig(t, agent.Caps{overrideKey: "https://sim.example/cap/modify"})
	// The override first, as the login burst may: before its object.
	r.sim.send(overrideMessage(`{'id':i4242,'od':[{'bc':[r1,r0,r0,r0.5],'ec':[r0,r0.5,r1],'mf':r0.25,'rf':r0.75,'am':i2,'ac':r0.5,'ds':1,`+
		`'tex':[u`+aGlossyTex.String()+`,!,!,u`+msg.GLTFOverrideNullTexture.String()+`],'ti':[{'s':[r3,r3],'o':[r0.5,r-0.5]},{'r':r1.5}]}],'te':[i2]}`), 0)
	h, _ := r.srv.Agent("example")
	waitFor(t, 5*time.Second, "the override to be kept", func() bool { return h.Agent().Objects().PendingGLTF() == 1 })
	r.sim.send(&msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{ID: 4242, FullID: aGlossyBox, PCode: 9,
		ExtraParams: materialBlock(aGlossyMat)}}}, 0)
	waitFor(t, 5*time.Second, "the object to be described", func() bool { return h.Agent().Objects().Count() == 1 })

	ctx := context.Background()
	conn, err := client.Dial(ctx, r.ln.Addr().String(), plaintext())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hosted, err := sl.AttachConn(ctx, conn, "example")
	if err != nil {
		t.Fatal(err)
	}
	seen, err := hosted.Objects(ctx, "", aGlossyBox.String())
	if err != nil || len(seen) != 1 {
		t.Fatalf("objects %v, %v", seen, err)
	}
	s := seen[0]
	if s.GLTFMaterial(1) != aGlossyMat || s.GLTFMaterial(2) != aGlossyMat || !s.GLTFMaterial(0).IsZero() {
		t.Errorf("materials %v", s.RenderMaterials)
	}
	if len(s.GLTF) != 1 || s.GLTFOverride(0) != nil || s.GLTFOverride(1) != nil {
		t.Fatalf("overrides %+v", s.GLTF)
	}
	o := s.GLTFOverride(2)
	mask := msg.GLTFAlphaMask
	yes := true
	half, quarter, threeq := float32(0.5), float32(0.25), float32(0.75)
	rot := float32(1.5)
	want := &msg.GLTFOverride{
		BaseColour: &[4]float32{1, 0, 0, 0.5}, Emissive: &[3]float32{0, 0.5, 1},
		Metallic: &quarter, Roughness: &threeq, AlphaMode: &mask, AlphaCutoff: &half, DoubleSided: &yes,
	}
	want.Textures[msg.GLTFSlotBase] = &aGlossyTex
	want.Textures[msg.GLTFSlotEmissive] = &msg.GLTFOverrideNullTexture
	want.Transforms[msg.GLTFSlotBase] = msg.GLTFTransform{Scale: &[2]float32{3, 3}, Offset: &[2]float32{0.5, -0.5}}
	want.Transforms[msg.GLTFSlotNormal] = msg.GLTFTransform{Rotation: &rot}
	if !equalOverride(o, want) {
		t.Errorf("override %+v, want %+v", o, want)
	}

	// The session reads it the same way, and a clear is read as none.
	sess, err := sl.New(hosted)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if !sess.HoldsOverrides() {
		t.Fatal("the session does not hold the capability")
	}
	f, err := sess.FaceGLTF(ctx, &sl.Object{ID: aGlossyBox}, 2)
	if err != nil || f.Material != aGlossyMat || !equalOverride(f.Override, want) {
		t.Errorf("FaceGLTF %+v, %v", f, err)
	}
	r.sim.send(overrideMessage(`{'id':i4242,'od':[!],'te':[i2]}`), 0)
	if _, err := sess.WaitGLTF(ctx, &sl.Object{ID: aGlossyBox}, 2, 5*time.Second, func(f sl.FaceGLTF) bool { return f.Override == nil }); err != nil {
		t.Errorf("a clear: %v", err)
	}
}

func equalOverride(a, b *msg.GLTFOverride) bool { return reflect.DeepEqual(a, b) }

// TestAClientWithoutTheCapabilityIsToldSo: a session whose region offered
// no ModifyMaterialParams was sent no overrides, so reading them is an
// error and not an answer of none.
func TestAClientWithoutTheCapabilityIsToldSo(t *testing.T) {
	r := newRig(t, agent.Caps{})
	ctx := context.Background()
	conn, err := client.Dial(ctx, r.ln.Addr().String(), plaintext())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hosted, err := sl.AttachConn(ctx, conn, "example")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := sl.New(hosted)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if sess.HoldsOverrides() {
		t.Error("the session claims a capability the region did not offer")
	}
	if _, err := sess.GLTFOverrides(ctx, &sl.Object{ID: aGlossyBox}); err != sl.ErrNoOverridesCap {
		t.Errorf("error %v, want ErrNoOverridesCap", err)
	}
}
