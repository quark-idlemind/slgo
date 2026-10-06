package slate

// expect gltf: a face's GLTF material and the override on it, read from the
// object store the way a prim's light is.
// Why: doc/slate-runner.md#gltf-materials

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids, made with tools/new-id.
var (
	gltfMat     = msg.MustParseUUID("0ac57e57-7e57-c0de-fe29-bd3fcc189b2c")
	gltfOtherMt = msg.MustParseUUID("4eba7e57-7e57-c0de-3d3d-a81d61c78b6d")
	gltfTexA    = msg.MustParseUUID("506b7e57-7e57-c0de-3e5f-26a661753a1b")
	gltfTexB    = msg.MustParseUUID("c1547e57-7e57-c0de-25ff-98a055567e54")
)

// grantOverrides makes the session hold ModifyMaterialParams.
func (f *fakeGrid) grantOverrides() {
	f.ex.mu.Lock()
	defer f.ex.mu.Unlock()
	f.ex.overrides = true
}

// withGLTFMaterial gives a face a GLTF material, as the render material
// extra parameter does.
func withGLTFMaterial(face int, id msg.UUID) func(*sl.Seen) {
	return func(o *sl.Seen) {
		m := map[int]msg.UUID{}
		for k, v := range o.RenderMaterials {
			m[k] = v
		}
		m[face] = id
		o.RenderMaterials = m
	}
}

// withOverride is what the region last said is overridden on a face.
func withOverride(face int, ov *msg.GLTFOverride) func(*sl.Seen) {
	return func(o *sl.Seen) {
		m := map[int]*msg.GLTFOverride{}
		for k, v := range o.GLTF {
			m[k] = v
		}
		if ov == nil {
			delete(m, face)
		} else {
			m[face] = ov
		}
		o.GLTF = m
	}
}

func fl(v float32) *float32 { return &v }

// aSetOverride is an override that sets one of everything.
func aSetOverride() *msg.GLTFOverride {
	mask, yes := msg.GLTFAlphaMask, true
	o := &msg.GLTFOverride{
		BaseColour: &[4]float32{1, 0.5, 0, 0.25}, Emissive: &[3]float32{0, 0.5, 1},
		Metallic: fl(0), Roughness: fl(0.75), AlphaMode: &mask, AlphaCutoff: fl(0.5), DoubleSided: &yes,
	}
	o.Textures[msg.GLTFSlotBase] = &gltfTexA
	o.Textures[msg.GLTFSlotEmissive] = &msg.GLTFOverrideNullTexture
	return o
}

func both(fns ...func(*sl.Seen)) func(*sl.Seen) {
	return func(o *sl.Seen) {
		for _, fn := range fns {
			fn(o)
		}
	}
}

func TestGLTFParseAndCheck(t *testing.T) {
	const h = "slate 1\nobject a is \"A\"\n"
	for _, ok := range []string{
		"expect gltf override a face 0 is on\n",
		"expect gltf override a face 2 is off within 2s\n",
		"expect gltf override a link 2 face 2 becomes on within 2s\n",
		"expect gltf override a face 2 changes\n",
		"expect gltf override a face 2 becomes original\n",
		"expect no gltf override a face 2 is on within 1s\n",
		"expect gltf material a face 1 is 0ac57e57-7e57-c0de-fe29-bd3fcc189b2c\n",
		"expect gltf material a face 1 is none\n",
		"expect gltf colour a face 1 is 1 0.5 0\n",
		"expect gltf colour a face 1 becomes 0 0 1 near 0.01 within 2s\n",
		"expect gltf colour a face 1 is none\n",
		"expect gltf alpha a face 1 is 0.25\n",
		"expect gltf emissive a face 1 is 0 0.5 1\n",
		"expect gltf metallic a face 44 is 0\n",
		"expect gltf roughness a face 1 is 0.75 near 10 percent\n",
		"expect gltf roughness a face 1 is none\n",
		"expect gltf alphamode a face 1 is mask\n",
		"expect gltf alphamode a face 1 becomes blend within 1s\n",
		"expect gltf alphamode a face 1 is opaque\n",
		"expect gltf alphamode a face 1 is none\n",
		"expect gltf cutoff a face 1 is 0.5\n",
		"expect gltf doublesided a face 1 is on\n",
		"expect gltf doublesided a face 1 is off\n",
		"expect gltf doublesided a face 1 is none\n",
		"expect gltf basetexture a face 1 is 506b7e57-7e57-c0de-3e5f-26a661753a1b\n",
		"expect gltf normaltexture a face 1 is none\n",
		"expect gltf ormtexture a face 1 changes\n",
		"expect gltf emissivetexture a face 1 is ffffffff-ffff-ffff-ffff-ffffffffffff\n",
		"expect gltf metallic a face 1 is any within 1s as $v\nthen expect gltf roughness a face 1 is $v\n",
		"expect gltf colour a face 1 is any within 1s as $c\nthen expect gltf emissive a face 1 is $c\n",
		"expect gltf basetexture a face 1 is any within 1s as $t\nthen expect gltf material a face 1 is $t\nexpect texture a face 1 is $t\n",
		"expect gltf override a face 1 is any within 1s as $o\nthen expect gltf doublesided a face 1 is $o\n",
		"expect gltf alphamode a face 1 is any within 1s as $m\nthen expect gltf alphamode a face 1 is $m\n",
	} {
		mustCheck(t, h+ok)
	}
	// gltf and its props are words in their own place only.
	mustCheck(t, "slate 1\nobject gltf is \"A\"\nobject colour is \"B\"\nexpect gltf colour gltf face 0 is none\nexpect fullbright colour face 0 is on\n")
	parseErr(t, h+"expect gltf a face 0 is on\n", "expected override, material, colour")
	parseErr(t, h+"expect gltf sparkle a face 0 is on\n", "expected override, material, colour")
	parseErr(t, h+"expect gltf override a is on\n", "expected face")
	parseErr(t, h+"expect gltf override a face 0 is none\n", "expected on, off, original, or a capture")
	parseErr(t, h+"expect gltf override a face 0 is maybe\n", "expected on, off, original, or a capture")
	parseErr(t, h+"expect gltf doublesided a face 0 is 1\n", "expected on, off, none, original, or a capture")
	parseErr(t, h+"expect gltf colour a face 0 is 1 0\n", "expected a number")
	parseErr(t, h+"expect gltf colour a face 0 is 1 0 0 0.5\n", "expected a stimulus, expect, then, or do, found 0.5")
	parseErr(t, h+"expect gltf alpha a face 0 is\n", "expected a number")
	parseErr(t, h+"expect gltf alphamode a face 0 is mask 0.5\n", "expected a stimulus, expect, then, or do, found 0.5")
	parseErr(t, h+"expect gltf alphamode a face 0 is none-ish\n", "expected opaque, blend, mask, none, original, or a capture")
	parseErr(t, h+"expect gltf alphamode a face 0 is default\n", "expected opaque, blend, mask, none, original, or a capture")
	parseErr(t, h+"expect gltf material a face 0 is blend\n", "expected a UUID, none, original, or a capture")
	parseErr(t, h+"expect gltf metallic a face 0 changes 1\n", "changes takes no value")
	checkErr(t, h+"expect gltf metallic a face all is 1\n", "there is no face all")
	checkErr(t, h+"expect gltf metallic a face 45 is 1\n", "face 45 is past the 45 faces a prim has")
	checkErr(t, h+"expect gltf metallic a face 0 is any\n", "is any needs as")
	checkErr(t, h+"expect gltf colour a face 0 is 1.5 0 0\n", "gltf colour 1.5 is outside 0 to 1")
	checkErr(t, h+"expect gltf emissive a face 0 is 0 -0.5 0\n", "gltf emissive -0.5 is outside 0 to 1")
	checkErr(t, h+"expect gltf metallic a face 0 is 2\n", "gltf metallic 2 is outside 0 to 1")
	checkErr(t, h+"expect gltf alpha a face 0 is -1\n", "gltf alpha -1 is outside 0 to 1")
	checkErr(t, h+"expect gltf cutoff a face 0 is 1.1\n", "gltf cutoff 1.1 is outside 0 to 1")
	checkErr(t, h+"expect gltf metallic a face 0 is none as $x\n", "none is no value to bind")
	checkErr(t, h+"expect gltf override a face 0 is on near 1\n", "gltf override takes no near")
	checkErr(t, h+"expect gltf alphamode a face 0 is mask near 1\n", "gltf alphamode takes no near")
	checkErr(t, h+"expect gltf basetexture a face 0 is none near 1\n", "gltf basetexture takes no near")
	checkErr(t, h+"expect gltf metallic a face 0 is any near 1 within 1s as $n\n", "is any takes no near")
	checkErr(t, h+"expect gltf metallic a face 0 is 0.5 near 0\n", "near 0 is not above 0")
	checkErr(t, h+"expect gltf metallic a face 0 is any within 1s as $n\nthen expect gltf colour a face 0 is $n\n", "capture type mismatch")
	checkErr(t, h+"expect gltf override a face 0 is any within 1s as $n\nthen expect gltf metallic a face 0 is $n\n", "capture type mismatch")
	checkErr(t, h+"expect gltf alphamode a face 0 is any within 1s as $n\nthen expect gltf cutoff a face 0 is $n\n", "capture type mismatch")
	checkErr(t, h+"expect gltf metallic b face 0 is 0\n", "b is not")
}

func TestAnOverrideIsOnAFaceThatHasOne(t *testing.T) {
	f := newGrid(t)
	f.grantOverrides()
	f.change(signLocal, both(withGLTFMaterial(2, gltfMat), withOverride(2, aSetOverride()), withGLTFMaterial(3, gltfMat)))
	res := play(t, f, hdr+`expect gltf override sign face 2 is on within 500ms
expect gltf override sign face 3 is off within 500ms
expect gltf override sign face 0 is off within 500ms
expect no gltf override sign face 2 is off within 200ms
expect no gltf override sign face 3 is on within 200ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "gltf override sign face 2 on", "gltf override sign face 3 off", "gltf override sign face 0 off")
	res = play(t, f, hdr+"expect gltf override sign face 3 is on within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched gltf override sign face 3 is on within 150ms", "gltf override sign face 3 off")
}

func TestAFacesGLTFMaterialIsItsId(t *testing.T) {
	f := newGrid(t)
	f.grantOverrides()
	f.change(signLocal, withGLTFMaterial(2, gltfMat))
	res := play(t, f, hdr+`expect gltf material sign face 2 is `+gltfMat.String()+` within 500ms
expect gltf material sign face 3 is none within 500ms
expect no gltf material sign face 2 is `+gltfOtherMt.String()+` within 200ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "gltf material sign face 2 "+gltfMat.String(), "gltf material sign face 3 none")
	res = play(t, f, hdr+"expect gltf material sign face 2 is "+gltfOtherMt.String()+" within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched", "gltf material sign face 2 "+gltfMat.String())
}

func TestEveryFieldOfAnOverrideIsRead(t *testing.T) {
	f := newGrid(t)
	f.grantOverrides()
	f.change(signLocal, both(withGLTFMaterial(2, gltfMat), withOverride(2, aSetOverride())))
	res := play(t, f, hdr+`expect gltf colour sign face 2 is 1 0.5 0 within 500ms
expect gltf alpha sign face 2 is 0.25 within 500ms
expect gltf emissive sign face 2 is 0 0.5 1 within 500ms
expect gltf metallic sign face 2 is 0 within 500ms
expect gltf roughness sign face 2 is 0.75 within 500ms
expect gltf alphamode sign face 2 is mask within 500ms
expect gltf cutoff sign face 2 is 0.5 within 500ms
expect gltf doublesided sign face 2 is on within 500ms
expect gltf basetexture sign face 2 is `+gltfTexA.String()+` within 500ms
expect gltf emissivetexture sign face 2 is `+msg.GLTFOverrideNullTexture.String()+` within 500ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "gltf colour sign face 2 1 0.5 0", "gltf alpha sign face 2 0.25", "gltf emissive sign face 2 0 0.5 1",
		"gltf metallic sign face 2 0", "gltf roughness sign face 2 0.75", "gltf alphamode sign face 2 mask",
		"gltf cutoff sign face 2 0.5", "gltf doublesided sign face 2 on", "gltf basetexture sign face 2 "+gltfTexA.String())
}

func TestAFieldTheOverrideDoesNotSetIsNone(t *testing.T) {
	f := newGrid(t)
	f.grantOverrides()
	ov := &msg.GLTFOverride{Roughness: fl(0.5)}
	f.change(signLocal, both(withGLTFMaterial(2, gltfMat), withOverride(2, ov)))
	res := play(t, f, hdr+`expect gltf roughness sign face 2 is 0.5 within 500ms
expect gltf metallic sign face 2 is none within 500ms
expect gltf colour sign face 2 is none within 500ms
expect gltf alpha sign face 2 is none within 500ms
expect gltf emissive sign face 2 is none within 500ms
expect gltf alphamode sign face 2 is none within 500ms
expect gltf cutoff sign face 2 is none within 500ms
expect gltf doublesided sign face 2 is none within 500ms
expect gltf basetexture sign face 2 is none within 500ms
expect gltf normaltexture sign face 2 is none within 500ms
expect gltf ormtexture sign face 2 is none within 500ms
expect gltf emissivetexture sign face 2 is none within 500ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "gltf metallic sign face 2 none")
	// A zero is a value and none is not one: neither is the other.
	f.change(signLocal, withOverride(2, &msg.GLTFOverride{Metallic: fl(0)}))
	wantExit(t, play(t, f, hdr+"expect gltf metallic sign face 2 is 0 within 300ms\n"), 0)
	res = play(t, f, hdr+"expect gltf metallic sign face 2 is none within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "gltf metallic sign face 2 0")
	f.change(signLocal, withOverride(2, &msg.GLTFOverride{Roughness: fl(1)}))
	res = play(t, f, hdr+"expect gltf metallic sign face 2 is 0 within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "gltf metallic sign face 2 none")
	// A face with no GLTF material has no override to be set on.
	f.change(signLocal, withOverride(4, &msg.GLTFOverride{Metallic: fl(1)}))
	f.change(signLocal, func(o *sl.Seen) { o.RenderMaterials = map[int]msg.UUID{2: gltfMat} })
	wantExit(t, play(t, f, hdr+"expect gltf metallic sign face 4 is 1 within 150ms\n"), 0)
}

func TestAnOverrideChangesAndBecomes(t *testing.T) {
	f := newGrid(t)
	f.grantOverrides()
	f.change(signLocal, both(withGLTFMaterial(2, gltfMat), withOverride(2, &msg.GLTFOverride{Metallic: fl(0.25)})))
	moves(t, f, map[string]func(*sl.Seen){
		"set":   withOverride(2, &msg.GLTFOverride{Metallic: fl(0.75), Roughness: fl(0.5)}),
		"clear": withOverride(2, nil),
		"flip": withOverride(2, func() *msg.GLTFOverride {
			m := msg.GLTFAlphaBlend
			return &msg.GLTFOverride{AlphaMode: &m}
		}()),
	})
	res := play(t, f, hdr+`expect gltf metallic sign face 2 is 0.25 within 500ms
say "set" on 0
expect gltf metallic sign face 2 becomes 0.75 within 1s
expect gltf roughness sign face 2 becomes 0.5 within 1s
expect gltf override sign face 2 is on within 500ms
say "clear" on 0
expect gltf override sign face 2 becomes off within 1s
expect gltf metallic sign face 2 becomes none within 1s
say "flip" on 0
expect gltf alphamode sign face 2 becomes blend within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "gltf metallic sign face 2 0.25", "gltf metallic sign face 2 0.75", "gltf metallic sign face 2 none", "gltf alphamode sign face 2 blend")
	// changes sees a field going from none to set, and a set value moving.
	f = newGrid(t)
	f.grantOverrides()
	f.change(signLocal, both(withGLTFMaterial(2, gltfMat), withOverride(2, &msg.GLTFOverride{Roughness: fl(0.5)})))
	moves(t, f, map[string]func(*sl.Seen){"go": withOverride(2, &msg.GLTFOverride{Roughness: fl(0.5), Metallic: fl(1)})})
	res = play(t, f, hdr+`say "go" on 0
expect gltf metallic sign face 2 changes within 1s
expect no gltf roughness sign face 2 changes within 300ms
`)
	wantExit(t, res, 0)
	// A value that never comes is a failure that says what was read.
	res = play(t, newGridWithOverride(t, withOverride(2, &msg.GLTFOverride{Metallic: fl(0.25)})), hdr+"expect gltf metallic sign face 2 becomes 0.75 within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched gltf metallic sign face 2 becomes 0.75 within 300ms", "gltf metallic sign face 2 0.25")
}

func newGridWithOverride(t *testing.T, fn func(*sl.Seen)) *fakeGrid {
	t.Helper()
	f := newGrid(t)
	f.grantOverrides()
	f.change(signLocal, both(withGLTFMaterial(2, gltfMat), fn))
	return f
}

func TestAnOverrideBecomesOriginal(t *testing.T) {
	f := newGridWithOverride(t, withOverride(2, &msg.GLTFOverride{Metallic: fl(0.25)}))
	moves(t, f, map[string]func(*sl.Seen){
		"away": withOverride(2, &msg.GLTFOverride{Metallic: fl(1)}),
		"back": withOverride(2, &msg.GLTFOverride{Metallic: fl(0.25)}),
	})
	res := play(t, f, hdr+`expect gltf metallic sign face 2 is 0.25 within 500ms
say "away" on 0
expect gltf metallic sign face 2 becomes 1 within 1s
say "back" on 0
expect gltf metallic sign face 2 becomes original within 1s
`)
	wantExit(t, res, 0)
}

func TestGLTFNumbersAreNearAsOtherNumbersAre(t *testing.T) {
	f := newGridWithOverride(t, withOverride(2, &msg.GLTFOverride{Roughness: fl(0.7), BaseColour: &[4]float32{0.5, 0.5, 0.5, 1}}))
	wantExit(t, play(t, f, hdr+"expect gltf roughness sign face 2 is 0.75 near 0.1 within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"expect gltf roughness sign face 2 is 0.75 near 10 percent within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"expect gltf colour sign face 2 is 0.45 0.55 0.5 near 0.1 within 300ms\n"), 0)
	res := play(t, f, hdr+"expect gltf roughness sign face 2 is 0.75 within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched gltf roughness sign face 2 is 0.75 within 150ms", "gltf roughness sign face 2 0.7")
	res = play(t, f, hdr+"expect gltf roughness sign face 2 is 0.75 near 0.01 within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "tolerance 0.01")
	// A float32 is read whole: the literal is what the session holds.
	g := newGridWithOverride(t, withOverride(2, &msg.GLTFOverride{Roughness: fl(0.3)}))
	wantExit(t, play(t, g, hdr+"expect gltf roughness sign face 2 is 0.3 within 300ms\n"), 0)
}

func TestGLTFReadingsAreCaptured(t *testing.T) {
	f := newGridWithOverride(t, withOverride(2, &msg.GLTFOverride{Metallic: fl(0.25), BaseColour: &[4]float32{1, 0.5, 0, 1}}))
	res := play(t, f, hdr+`expect gltf metallic sign face 2 is any within 500ms as $m
then expect gltf metallic sign face 2 is $m within 500ms
expect gltf colour sign face 2 is any within 500ms as $c
then expect gltf colour sign face 2 is $c within 500ms
expect gltf override sign face 2 is any within 500ms as $o
then expect gltf override sign face 2 is $o within 500ms
expect gltf material sign face 2 is any within 500ms as $id
then expect gltf material sign face 2 is $id within 500ms
expect gltf alphamode sign face 2 is any within 300ms as $am
`)
	// The last is on a field nothing set, which has no value to be any of.
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched gltf alphamode sign face 2 is any within 300ms as $am", "gltf alphamode sign face 2 none")
	mustNotHave(t, res, "unmatched gltf metallic")
}

func TestGLTFNeedsTheCapability(t *testing.T) {
	f := newGrid(t) // does not hold ModifyMaterialParams
	f.change(signLocal, both(withGLTFMaterial(2, gltfMat), withOverride(2, aSetOverride())))
	res := play(t, f, hdr+"test \"a\" {\n  expect gltf metallic sign face 2 is 0 within 1s\n}\ntest \"b\" {\n  expect fullbright sign face 1 is off within 500ms\n}\n")
	// The environment, not the product: exit 3, said as setup, and the
	// run stops before the next test.
	wantExit(t, res, 3)
	mustHave(t, res, "slate: setup: this session holds no ModifyMaterialParams capability",
		"slgod is likely older than this slate, or the session logged in before it was upgraded: restart it from the same release")
	mustNotHave(t, res, `slate: test "b"`)
	// Another expectation on the same grid is not touched by it.
	wantExit(t, play(t, f, hdr+"expect fullbright sign face 1 is off within 500ms\n"), 0)
	// Nor is a negative one a way round it.
	res = play(t, f, hdr+"expect no gltf override sign face 2 is off within 200ms\n")
	wantExit(t, res, 3)
	if !strings.Contains(res.Transcript, "ModifyMaterialParams") {
		t.Errorf("the transcript does not name the capability:\n%s", res.Transcript)
	}
}

func TestAnOverrideOfALink(t *testing.T) {
	f, o := world(t)
	f.grantOverrides()
	o.extraSay = func(m msg.Message) {
		if g, ok := m.(*msg.ObjectGrab); ok && g.ObjectData.LocalID == 201 {
			f.change(201, both(withGLTFMaterial(0, gltfMat), withOverride(0, &msg.GLTFOverride{Roughness: fl(0.125)})))
		}
	}
	res := o.play(t, probeHdr+"touch vendor link 2\nexpect gltf roughness vendor link 2 face 0 becomes 0.125 within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "gltf roughness vendor link 2 face 0 0.125")
}

func TestAnOverrideOnANegativeIsAWindow(t *testing.T) {
	f := newGridWithOverride(t, withOverride(2, &msg.GLTFOverride{Metallic: fl(0.25)}))
	wantExit(t, play(t, f, hdr+"expect no gltf metallic sign face 2 is 0.75 within 300ms\n"), 0)
	res := play(t, f, hdr+"expect no gltf metallic sign face 2 is 0.25 within 300ms\n")
	wantExit(t, res, 1)
}
