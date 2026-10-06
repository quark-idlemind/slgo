package msg

import (
	"reflect"
	"testing"
)

func f32p(f float32) *float32 { return &f }

// TestEveryOverrideKeyIsRead: each key of a face's override map, and that
// the keys it does not carry stay unset.
// Why: doc/gltf.md#the-message
func TestEveryOverrideKeyIsRead(t *testing.T) {
	t.Parallel()
	tex := MustParseUUID("76507e57-7e57-c0de-dbc1-623eccf01241")
	o := mustUpdate(t, `{'id':i77,'od':[{
		'tex':[u`+tex.String()+`,!,u`+GLTFOverrideNullTexture.String()+`],
		'bc':[r1,r0,r0,r0.5],'ec':[r0.25,r0.5,r0.75],'mf':r0.25,'rf':r0.75,'am':i2,'ac':r0.5,'ds':1,
		'ti':[{'s':[r3,r3]},{'o':[r0.5,r-0.5],'r':r1.5}]}],'te':[i2]}`).Faces[2]
	if o == nil {
		t.Fatal("face 2 has no override")
	}
	want := &GLTFOverride{
		BaseColour: &[4]float32{1, 0, 0, 0.5}, Emissive: &[3]float32{0.25, 0.5, 0.75},
		Metallic: f32p(0.25), Roughness: f32p(0.75), AlphaCutoff: f32p(0.5),
	}
	mask := GLTFAlphaMask
	yes := true
	want.AlphaMode, want.DoubleSided = &mask, &yes
	want.Textures[GLTFSlotBase] = &tex
	want.Textures[GLTFSlotMetallicRoughness] = &GLTFOverrideNullTexture
	want.Transforms[GLTFSlotBase].Scale = &[2]float32{3, 3}
	want.Transforms[GLTFSlotNormal] = GLTFTransform{Offset: &[2]float32{0.5, -0.5}, Rotation: f32p(1.5)}
	if !reflect.DeepEqual(o, want) {
		t.Errorf("override %+v, want %+v", o, want)
	}
}

// TestAZeroIsNotUnset: a factor of zero and a colour of black are
// overrides, and a key left out is not one.
func TestAZeroIsNotUnset(t *testing.T) {
	t.Parallel()
	o := mustUpdate(t, `{'id':i1,'od':[{'mf':r0,'ec':[r0,r0,r0],'ds':0,'am':i0}],'te':[i0]}`).Faces[0]
	if o == nil || o.Metallic == nil || *o.Metallic != 0 || o.Emissive == nil || o.DoubleSided == nil || *o.DoubleSided ||
		o.AlphaMode == nil || *o.AlphaMode != GLTFAlphaOpaque {
		t.Fatalf("zeros read as %+v", o)
	}
	if o.Roughness != nil || o.BaseColour != nil || o.AlphaCutoff != nil {
		t.Errorf("keys that were not sent are set: %+v", o)
	}
}

// TestTheViewersTypeTestsAreKept: mf is read only as a real, am only as an
// integer, ds only as a boolean, as isReal, isInteger and isBoolean do.
func TestTheViewersTypeTestsAreKept(t *testing.T) {
	t.Parallel()
	if o := ParseGLTFOverride(map[string]any{"mf": int64(1), "rf": "x", "ac": true, "am": 1.0, "ds": int64(1), "bc": 3.0}); !o.Empty() {
		t.Errorf("wrong types read as set: %+v", o)
	}
}

// TestAClearIsAnUndefPerFace: undef for a face, and a map that says
// nothing, are no override.
func TestAClearIsAnUndefPerFace(t *testing.T) {
	t.Parallel()
	u := mustUpdate(t, `{'id':i7357001,'od':[!,{}],'te':[i2,i3]}`)
	if u.Local != 7357001 || len(u.Faces) != 0 {
		t.Errorf("a clear read as %+v", u)
	}
}

// TestAnEmptyTeClearsEverythingAndNoTeIsMalformed.
func TestAnEmptyTeClearsEverythingAndNoTeIsMalformed(t *testing.T) {
	t.Parallel()
	u := mustUpdate(t, `{'id':i9,'od':[],'te':[]}`)
	if u.Local != 9 || len(u.Faces) != 0 {
		t.Errorf("an empty te read as %+v", u)
	}
	for _, in := range []string{`{'id':i9,'od':[{'mf':r1}]}`, `{'id':i9,'te':!}`, `[i1]`, `x`} {
		if _, err := ParseGLTFOverrideUpdate([]byte(in)); err == nil {
			t.Errorf("%q was read", in)
		}
	}
}

// TestFacesAreBoundedAsTheViewerBoundsThem: faces outside 0 to 44 and
// entries past the 45th are not read, and a later mention of a face
// replaces an earlier one.
func TestFacesAreBoundedAsTheViewerBoundsThem(t *testing.T) {
	t.Parallel()
	u := mustUpdate(t, `{'id':i1,'od':[{'mf':r1},{'mf':r2},{'mf':r3},{'mf':r4},{'rf':r1},!],'te':[i-1,i45,i3,i3,i4,i4]}`)
	if len(u.Faces) != 1 || u.Faces[3] == nil || *u.Faces[3].Metallic != 4 {
		t.Errorf("faces %+v", u.Faces)
	}
	// i4 is named by an undef last, so it is cleared.
}

// TestAMissingOdIsNoOverride: a te with no od for it names a face with
// nothing.
func TestAMissingOdIsNoOverride(t *testing.T) {
	t.Parallel()
	u := mustUpdate(t, `{'id':i1,'od':[{'mf':r1}],'te':[i0,i1]}`)
	if len(u.Faces) != 1 || u.Faces[0] == nil {
		t.Errorf("faces %+v", u.Faces)
	}
}

func mustUpdate(t *testing.T, s string) *GLTFOverrideUpdate {
	t.Helper()
	u, err := ParseGLTFOverrideUpdate([]byte(s))
	if err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	return u
}

// TestRenderMaterialsOf: the render material block, a count and a face
// and an id for each, and nothing from a block that is not there, a null
// id, or a count the bytes cannot hold.
// Why: doc/gltf.md#the-faces-material
func TestRenderMaterialsOf(t *testing.T) {
	t.Parallel()
	a := MustParseUUID("76507e57-7e57-c0de-dbc1-623eccf01241")
	b := MustParseUUID("95757e57-7e57-c0de-947b-6ebc67b68580")
	data := []byte{3, 2}
	data = append(data, a[:]...)
	data = append(data, 5)
	data = append(data, b[:]...)
	data = append(data, 1)
	data = append(data, make([]byte, 16)...)
	got, err := DecodeExtraParams(params(param(ExtraFlexible, []byte{1}), param(ExtraRenderMat, data)))
	if err != nil {
		t.Fatal(err)
	}
	if want := (map[int]UUID{2: a, 5: b}); !reflect.DeepEqual(RenderMaterialsOf(got), want) {
		t.Errorf("render materials %v, want %v", RenderMaterialsOf(got), want)
	}
	if RenderMaterialsOf(got[:1]) != nil {
		t.Error("found a material in a flexible block")
	}
	short := []ExtraParam{{Type: ExtraRenderMat, Data: append([]byte{2, 0}, a[:]...)}}
	if got := RenderMaterialsOf(short); !reflect.DeepEqual(got, map[int]UUID{0: a}) {
		t.Errorf("a count past the bytes read as %v, want the one entry there", got)
	}
	if RenderMaterialsOf([]ExtraParam{{Type: ExtraRenderMat}}) != nil {
		t.Error("an empty block names a material")
	}
}
