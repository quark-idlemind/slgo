package slate

// expect normalmap, specularmap, glossiness and environment, over the
// fake grid's RenderMaterials (alphamode_test.go).

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/internal/llsdbin"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids, made with tools/new-id.
var (
	nrmBrick  = msg.MustParseUUID("0c507e57-7e57-c0de-8307-6b59f366d698")
	nrmWave   = msg.MustParseUUID("20287e57-7e57-c0de-e898-fb9970be9522")
	specGloss = msg.MustParseUUID("4b797e57-7e57-c0de-6a9c-5b42ab4c0ba2")
	specSteel = msg.MustParseUUID("59737e57-7e57-c0de-52af-39b0fa76a00a")

	matNormalOnly = msg.MustParseUUID("5b247e57-7e57-c0de-2eaf-cd64778d2b98")
	matBoth       = msg.MustParseUUID("82e07e57-7e57-c0de-b071-0620503a7a85")
	matBothAgain  = msg.MustParseUUID("db107e57-7e57-c0de-9253-8c595d694b89")
)

// aMaterial is the fields of a material the region holds that these
// tests read.
type aMaterial struct {
	norm, spec msg.UUID
	gloss, env int64
}

func (m aMaterial) llsd() map[string]any {
	return map[string]any{
		"DiffuseAlphaMode": int64(0), "AlphaMaskCutoff": int64(0),
		"NormMap": llsdbin.UUID(m.norm), "SpecMap": llsdbin.UUID(m.spec),
		"SpecExp": m.gloss, "EnvIntensity": m.env,
	}
}

// theMaps are materials with maps: one with only a normal map, and two
// with both, which differ in the specular map and the glossiness.
var theMaps = map[msg.UUID]aMaterial{
	matNormalOnly: {norm: nrmBrick, gloss: 51},
	matBoth:       {norm: nrmWave, spec: specSteel, gloss: 200, env: 30},
	matBothAgain:  {norm: nrmWave, spec: specGloss, gloss: 90, env: 30},
}

func TestMaterialMapsAreReadFromTheMaterial(t *testing.T) {
	f := newGrid(t)
	srv := f.serveMaterials(t)
	f.change(signLocal, withMaterial(1, matBoth))
	res := play(t, f, hdr+`expect normalmap sign face 1 is `+nrmWave.String()+` within 1s
expect specularmap sign face 1 is `+specSteel.String()+` within 1s
expect glossiness sign face 1 is 200 within 1s
expect environment sign face 1 is 30 within 1s
expect no normalmap sign face 1 is none within 100ms
expect no glossiness sign face 1 is 199 within 100ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "normalmap sign face 1 "+nrmWave.String(), "specularmap sign face 1 "+specSteel.String(),
		"glossiness sign face 1 200", "environment sign face 1 30")
	if n := srv.asked(matBoth); n != 1 {
		t.Errorf("the material was asked for %d times, want 1", n)
	}

	// Another value of a field that has one is not it.
	f = newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(1, matBoth))
	res = play(t, f, hdr+"expect glossiness sign face 1 is 201 within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched glossiness sign face 1 is 201")
	f = newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(1, matBoth))
	res = play(t, f, hdr+"expect normalmap sign face 1 is "+nrmBrick.String()+" within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched normalmap sign face 1 is "+nrmBrick.String())
}

func TestAFaceWithNoMaterialHasNoMapsAndNoLevels(t *testing.T) {
	f := newGrid(t)
	srv := f.serveMaterials(t)
	res := play(t, f, hdr+`expect normalmap sign face 0 is none within 1s
expect specularmap sign face 0 is none within 1s
expect glossiness sign face 0 is 0 within 1s
expect environment sign face 0 is 0 within 1s
expect normalmap sign face 0 is 00000000-0000-0000-0000-000000000000 within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "normalmap sign face 0 none", "specularmap sign face 0 none", "glossiness sign face 0 0")
	if len(srv.asks) != 0 {
		t.Errorf("a face with no material asked the region for %d materials", len(srv.asks))
	}
	f = newGrid(t)
	f.serveMaterials(t)
	res = play(t, f, hdr+"expect normalmap sign face 0 is "+nrmBrick.String()+" within 150ms\n")
	wantExit(t, res, 1)
}

func TestAMaterialWithoutANormalMapReadsNone(t *testing.T) {
	f := newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(0, matMask128)) // an alpha mode and nothing else
	f.change(signLocal, withMaterial(1, matNormalOnly))
	res := play(t, f, hdr+`expect normalmap sign face 0 is none within 1s
expect specularmap sign face 0 is none within 1s
expect normalmap sign face 1 is `+nrmBrick.String()+` within 1s
expect specularmap sign face 1 is none within 1s
expect glossiness sign face 1 is 51 within 1s
expect environment sign face 1 is 0 within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "specularmap sign face 1 none", "normalmap sign face 0 none")
}

func TestMaterialMapsBecomeAndChange(t *testing.T) {
	f := newGrid(t)
	f.serveMaterials(t)
	f.whenSaid("go", func() {
		f.changeAfter(t, 40*time.Millisecond, signLocal, withMaterial(1, matNormalOnly))
		f.changeAfter(t, 250*time.Millisecond, signLocal, withMaterial(1, matBoth))
		f.changeAfter(t, 500*time.Millisecond, signLocal, withMaterial(1, matBothAgain))
	})
	res := play(t, f, hdr+`say "go" on 0
expect normalmap sign face 1 becomes `+nrmBrick.String()+` within 1s
expect specularmap sign face 1 becomes `+specSteel.String()+` within 1s
expect glossiness sign face 1 becomes 200 within 1s
expect specularmap sign face 1 changes within 1s
expect glossiness sign face 1 becomes 90 within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "specularmap sign face 1 none", "glossiness sign face 1 90")

	// The environment does not change between the last two materials.
	f = newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(1, matBoth))
	f.whenSaid("go", func() { f.changeAfter(t, 40*time.Millisecond, signLocal, withMaterial(1, matBothAgain)) })
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect no environment sign face 1 changes within 400ms\n"), 0)
	f = newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(1, matBoth))
	f.whenSaid("go", func() { f.changeAfter(t, 40*time.Millisecond, signLocal, withMaterial(1, matBothAgain)) })
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect glossiness sign face 1 changes within 1s\n"), 0)

	// Back to what the face began as is original.
	f = newGrid(t)
	f.serveMaterials(t)
	f.whenSaid("go", func() {
		f.changeAfter(t, 40*time.Millisecond, signLocal, withMaterial(0, matBoth))
		f.changeAfter(t, 250*time.Millisecond, signLocal, withFace(0, func(fc *sl.Face) { fc.Material = msg.UUID{} }))
	})
	res = play(t, f, hdr+`say "go" on 0
expect normalmap sign face 0 changes within 1s
expect normalmap sign face 0 becomes original within 1s
`)
	wantExit(t, res, 0)
}

func TestMaterialLevelsTakeNear(t *testing.T) {
	f := newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(1, matBoth))
	res := play(t, f, hdr+`expect glossiness sign face 1 is 205 near 5 within 1s
expect environment sign face 1 is 33 near 10 percent within 1s
expect no glossiness sign face 1 is 205 near 4 within 100ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "(tolerance 5)")

	f = newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(1, matBoth))
	res = play(t, f, hdr+"expect glossiness sign face 1 is 210 near 5 within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "the nearest reading was 10 off, and at most 5 is allowed")
}

func TestMaterialMapsOfEveryFace(t *testing.T) {
	f := newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(1, matBoth))
	f.change(signLocal, withMaterial(2, matNormalOnly))
	res := play(t, f, hdr+`expect glossiness sign face all is any within 1s as $g
expect normalmap sign face all is any within 1s as $n
`)
	wantExit(t, res, 0)
	// The box has six faces; the three after the last set are plain.
	mustHave(t, res, "glossiness sign face all 0, 200, 51, 0, 0, 0", "capture $g = face all 0, 200, 51, 0, 0, 0 (step 1)",
		"normalmap sign face all none, "+nrmWave.String()+", "+nrmBrick.String()+", none, none, none")

	// A capture of every face is what a face all of the same field takes.
	f = newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(1, matBoth))
	res = play(t, f, hdr+"expect specularmap sign face all is any within 1s as $s\nwait 100ms\nexpect specularmap sign face all is $s within 1s\n")
	wantExit(t, res, 0)
	// A single value holds for every face that has it.
	f = newGrid(t)
	f.serveMaterials(t)
	wantExit(t, play(t, f, hdr+"expect glossiness sign face all is 0 within 1s\n"), 0)
}

func TestMaterialMapsAreCaptured(t *testing.T) {
	f := newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(1, matBoth))
	res := play(t, f, hdr+`expect glossiness sign face 1 is any within 1s as $g
expect specularmap sign face 1 is any within 1s as $s
wait 100ms
expect specularmap sign face 1 is $s within 1s
expect glossiness sign face 1 is $g within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "capture $g = 200 (step 1)", "capture $s = "+specSteel.String()+" (step 1)")
}

func TestMaterialMapsOfALink(t *testing.T) {
	f, o := world(t)
	f.serveMaterials(t)
	o.extraSay = func(m msg.Message) {
		if g, ok := m.(*msg.ObjectGrab); ok && g.ObjectData.LocalID == 201 {
			f.change(201, withMaterial(0, matBoth))
		}
	}
	res := o.play(t, probeHdr+"touch vendor link 2\nexpect normalmap vendor link 2 face 0 becomes "+nrmWave.String()+" within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "normalmap vendor link 2 face 0 "+nrmWave.String())
}

func TestMaterialMapsNeedTheCapability(t *testing.T) {
	for _, word := range []string{"normalmap sign face 1 is none", "specularmap sign face 1 is none",
		"glossiness sign face 1 is 0", "environment sign face 1 is 0"} {
		prop, _, _ := strings.Cut(word, " ")
		f := newGrid(t) // serves no RenderMaterials
		f.change(signLocal, withMaterial(1, matBoth))
		res := play(t, f, hdr+"test \"a\" {\n  expect "+word+" within 1s\n}\ntest \"b\" {\n  expect fullbright sign face 1 is off within 500ms\n}\n")
		wantExit(t, res, 3)
		mustHave(t, res, "slate: setup: this session holds no RenderMaterials capability, which "+prop+" reads a face's material from")
		mustNotHave(t, res, `slate: test "b"`)
	}
}

func TestMaterialMapsSayWhyAMaterialCouldNotBeRead(t *testing.T) {
	f := newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(1, msg.MustParseUUID("3e2f7e57-7e57-c0de-9b13-70c8d1a2e65f")))
	res := play(t, f, hdr+"expect normalmap sign face 1 is none within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "the material could not be read", "the region has no material")
}

func TestMaterialMapsStaticChecks(t *testing.T) {
	n := nrmWave.String()
	mustCheck(t, hdr+"expect normalmap sign face 1 is "+n+"\nexpect specularmap sign link 1 face 0 becomes none\nexpect normalmap sign face all changes\n"+
		"expect glossiness sign face 0 is 255\nexpect environment sign face all is 0\nexpect glossiness sign face 0 becomes original\n")
	mustCheck(t, hdr+"expect glossiness sign face 0 is any as $g\nwait 1s\nexpect environment sign face 0 is $g\n")
	mustCheck(t, hdr+"expect glossiness sign face 0 is 10 near 3\nexpect environment sign face 0 changes near 2 percent\n")
	checkErr(t, hdr+"expect normalmap sign face 1 is "+n+" near 1\n", "normalmap takes no near")
	checkErr(t, hdr+"expect specularmap sign face 1 changes near 1\n", "specularmap takes no near")
	checkErr(t, hdr+"expect glossiness sign face 1 is 256\n", "glossiness 256 is outside 0 to 255")
	checkErr(t, hdr+"expect environment sign face 1 is -1\n", "environment -1 is outside 0 to 255")
	checkErr(t, hdr+"expect glossiness sign face 1 is 12.5\n", "is not a whole number")
	checkErr(t, hdr+"expect glossiness ghost face 0 is 1\n", "ghost")
	checkErr(t, hdr+"expect normalmap sign face 0 is any\n", "is any needs as $name")
	checkErr(t, hdr+"expect no normalmap sign face 0 is any as $m\n", "a negative expectation matches nothing to bind")
	checkErr(t, hdr+"expect normalmap sign face 99999999999 is none\n", "face")
	// A map binds a uuid and a level a number, and neither is the other.
	checkErr(t, hdr+"expect glossiness sign face 0 is any as $g\nwait 1s\nexpect normalmap sign face 0 is $g\n", "capture type mismatch")
	checkErr(t, hdr+"expect normalmap sign face 0 is any as $m\nwait 1s\nexpect glossiness sign face 0 is $m\n", "capture type mismatch")
	// A face all capture is for face all.
	checkErr(t, hdr+"expect glossiness sign face all is any as $g\nwait 1s\nexpect glossiness sign face 0 is $g\n", "holds every face")
	parseErr(t, hdr+"expect normalmap sign face 0 is blend\n", "expected a UUID, none or original")
	parseErr(t, hdr+"expect normalmap sign face 0 is 5\n", "expected a UUID, none or original")
	parseErr(t, hdr+"expect specularmap sign face 0 is\n", "expected a UUID, none or original")
	parseErr(t, hdr+"expect normalmap sign face 0 changes "+n+"\n", "changes takes no value")
	parseErr(t, hdr+"expect glossiness sign face 0 is none\n", "number")
	parseErr(t, hdr+"expect normalmap sign is none\n", "face")

	s := mustCheck(t, hdr+"expect specularmap sign face 2 is none\n")
	x := s.Tests[0].Steps[0].Expect[0].Material
	if x == nil || x.Prop != "specularmap" || x.ID != nullKey || x.Face.Value != 2 || x.State.Kind != StateIs || !x.IsMap() {
		t.Errorf("parsed as %+v", x)
	}
}
