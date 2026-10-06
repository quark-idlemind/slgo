package sl

// A face's GLTF material overrides, as a client behind a daemon reads them.
// Why: doc/gltf.md

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

var (
	aShinyBox = msg.MustParseUUID("3a8f7e57-7e57-c0de-2d64-0b9e71c5a4f3")
	aShinyMat = msg.MustParseUUID("b2597e57-7e57-c0de-8a10-5fe3cd6270b9")
)

func ptr[T any](v T) *T { return &v }

// TestHostedReadsEveryFieldOfAnOverride: each field of the wire's override
// comes back typed, a field that was not sent is unset, and an entry
// that says nothing, or names a material that will not read, is left out.
func TestHostedReadsEveryFieldOfAnOverride(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.objects = []*pb.ObjectInfo{{
		Id: aShinyBox.String(), Local: 5, Pcode: 9,
		RenderMaterials: []*pb.PrimFaceMaterial{
			{Face: 1, Material: aShinyMat.String()}, {Face: 2, Material: "not an id"}, {Face: 3, Material: msg.Zero.String()},
		},
		GltfOverrides: []*pb.PrimGLTFOverride{
			{
				Face:       1,
				BaseColour: []float32{1, 0, 0, 0.5}, Emissive: []float32{0, 0.5, 1},
				Metallic: ptr(float32(0)), Roughness: ptr(float32(0.75)), AlphaMode: ptr(int32(2)),
				AlphaCutoff: ptr(float32(0.5)), DoubleSided: ptr(false),
				Textures: []string{"", "", aShinyMat.String(), ""},
				Transforms: []*pb.PrimGLTFTransform{
					{Offset: []float32{0.5, -0.5}, Scale: []float32{3, 3}}, {Rotation: ptr(float32(1.5))}, {}, {},
				},
			},
			{Face: 4},                              // says nothing
			{Face: 5, BaseColour: []float32{1, 2}}, // a colour of the wrong length is not one
		},
	}}
	seen, err := h.Objects(context.Background(), "", "")
	if err != nil || len(seen) != 1 {
		t.Fatalf("%v, %v", seen, err)
	}
	s := seen[0]
	if want := (map[int]msg.UUID{1: aShinyMat}); !reflect.DeepEqual(s.RenderMaterials, want) {
		t.Errorf("materials %v, want %v", s.RenderMaterials, want)
	}
	want := &msg.GLTFOverride{
		BaseColour: &[4]float32{1, 0, 0, 0.5}, Emissive: &[3]float32{0, 0.5, 1},
		Metallic: ptr(float32(0)), Roughness: ptr(float32(0.75)), AlphaMode: ptr(msg.GLTFAlphaMask),
		AlphaCutoff: ptr(float32(0.5)), DoubleSided: ptr(false),
	}
	want.Textures[msg.GLTFSlotMetallicRoughness] = &aShinyMat
	want.Transforms[msg.GLTFSlotBase] = msg.GLTFTransform{Offset: &[2]float32{0.5, -0.5}, Scale: &[2]float32{3, 3}}
	want.Transforms[msg.GLTFSlotNormal] = msg.GLTFTransform{Rotation: ptr(float32(1.5))}
	if len(s.GLTF) != 1 || !reflect.DeepEqual(s.GLTFOverride(1), want) {
		t.Errorf("overrides %+v, want face 1 %+v", s.GLTF, want)
	}
	if s.GLTFOverride(0) != nil || s.GLTFMaterial(0) != (msg.UUID{}) {
		t.Error("a face the object does not name has one")
	}
}

// TestAZeroFromTheWireIsNotUnset: a metallic factor of zero crosses as an
// optional field that is set, and reads as an override.
func TestAZeroFromTheWireIsNotUnset(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.objects = []*pb.ObjectInfo{{Id: aShinyBox.String(), Local: 5,
		GltfOverrides: []*pb.PrimGLTFOverride{{Face: 0, Metallic: ptr(float32(0))}}}}
	seen, _ := h.Objects(context.Background(), "", "")
	o := seen[0].GLTFOverride(0)
	if o == nil || o.Metallic == nil || *o.Metallic != 0 || o.Roughness != nil {
		t.Errorf("override %+v", o)
	}
}

// TestASessionWithoutTheCapabilityCannotReadOverrides: the daemon was not
// asked, or the region did not offer, so the answer would be none for
// every face, which is not a true one.
func TestASessionWithoutTheCapabilityCannotReadOverrides(t *testing.T) {
	t.Parallel()
	h, _ := newFakeDaemon(t)
	s, err := New(h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.HoldsOverrides() {
		t.Error("holds a capability the daemon did not list")
	}
	if _, err := s.GLTFOverrides(context.Background(), &Object{ID: aShinyBox}); !errors.Is(err, ErrNoOverridesCap) {
		t.Errorf("error %v, want ErrNoOverridesCap", err)
	}
	if _, err := s.FaceGLTF(context.Background(), &Object{ID: aShinyBox}, 0); !errors.Is(err, ErrNoOverridesCap) {
		t.Errorf("error %v, want ErrNoOverridesCap", err)
	}
}

// TestWaitGLTFTimesOutAndSeesWhatIsThere: with the capability held, a
// face that has what is asked for is returned at once and one that does
// not is a timeout.  The wait for a change that arrives later is in
// internal/server's test, against a region that sends one.
func TestWaitGLTFTimesOutAndSeesWhatIsThere(t *testing.T) {
	t.Parallel()
	d, conn := dialFakeDaemon(t)
	d.info.Caps = append(d.info.Caps, OverridesCap)
	d.objects = []*pb.ObjectInfo{{Id: aShinyBox.String(), Local: 5, Pcode: 9,
		GltfOverrides: []*pb.PrimGLTFOverride{{Face: 2, Roughness: ptr(float32(0.25))}}}}
	h, err := AttachConn(context.Background(), conn, "quark")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	s, err := New(h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.HoldsOverrides() {
		t.Fatal("the capability the daemon listed is not held")
	}
	o := &Object{ID: aShinyBox}
	f, err := s.WaitGLTF(context.Background(), o, 2, 5*time.Second, func(f FaceGLTF) bool { return f.Override != nil })
	if err != nil || *f.Override.Roughness != 0.25 {
		t.Errorf("%+v, %v", f, err)
	}
	if _, err := s.WaitGLTF(context.Background(), o, 3, 300*time.Millisecond, func(f FaceGLTF) bool { return f.Override != nil }); !errors.Is(err, ErrTimeout) {
		t.Errorf("error %v, want a timeout", err)
	}
	all, err := s.GLTFOverrides(context.Background(), o)
	if err != nil || len(all) != 1 || all[2] == nil {
		t.Errorf("%v, %v", all, err)
	}
}
