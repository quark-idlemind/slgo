package slate

// The physical material reading, expect substance: a prim's
// Seen.Material, which a script sets with PRIM_MATERIAL, compared by name.
// Why: doc/slate-runner.md#physical-material

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

// withSubstance is a prim whose update said the material byte b.
func withSubstance(b uint8) func(*sl.Seen) {
	return func(o *sl.Seen) { o.Material, o.MaterialKnown = b, true }
}

func TestSubstanceParseAndCheck(t *testing.T) {
	const h = "slate 1\nobject a is \"Example Sign\"\n"
	mustCheck(t, h+"expect substance a changes\n")
	for _, n := range SubstanceNames {
		mustCheck(t, h+"expect substance a is "+n+"\n")
		mustCheck(t, h+"expect substance a becomes "+n+" within 2s\n")
	}
	mustCheck(t, h+"expect substance a link 2 becomes wood\n")
	mustCheck(t, h+"expect substance a becomes original\n")
	mustCheck(t, h+"expect no substance a changes within 1s\n")
	mustCheck(t, h+"expect no substance a is glass within 1s\n")
	mustCheck(t, h+"expect substance a is any within 1s as $m\nthen expect substance a becomes $m\n")
	// A name that is not one of the eight, and a number, are refused.
	parseErr(t, h+"expect substance a is granite\n", "expected a material name")
	parseErr(t, h+"expect substance a is 3\n", "expected a material name")
	parseErr(t, h+"expect substance a is Wood\n", "expected a material name")
	parseErr(t, h+"expect substance a face 0 is wood\n", "expected is, becomes, or changes")
	checkErr(t, h+"expect substance a is any\n", "is any needs as")
	checkErr(t, h+"expect substance a becomes any as $m\n", "any is a reading of is")
	checkErr(t, h+"expect substance a is $nothing\n", "is not bound")
	checkErr(t, h+"expect substance b is wood\n", "b is not an object")
	checkErr(t, h+"expect substance a link -1 is wood\n", "link")
	checkErr(t, h+"expect substance a is wood near 1\n", "substance takes no near")
	// A material is its own capture type.
	checkErr(t, h+"expect substance a is any within 1s as $m\nthen expect click a is $m\n", "capture type mismatch")
	checkErr(t, h+"expect click a is any within 1s as $c\nthen expect substance a is $c\n", "capture type mismatch")
	checkErr(t, h+"expect substance a is any within 1s as $m\nthen expect text a is $m\n", "capture type mismatch")
	// The words are not reserved.
	mustCheck(t, "slate 1\nobject substance is \"A\"\nexpect substance substance is wood\n")
	mustCheck(t, "slate 1\nobject wood is \"A\"\nexpect substance wood is wood\n")
}

func TestEachNameIsTheByteLSLGivesIt(t *testing.T) {
	// PRIM_MATERIAL_STONE .. PRIM_MATERIAL_LIGHT, in order.
	want := map[string]uint8{"stone": 0, "metal": 1, "glass": 2, "wood": 3, "flesh": 4, "plastic": 5, "rubber": 6, "light": 7}
	if len(SubstanceBytes) != len(want) {
		t.Fatalf("SubstanceBytes has %d names, want 8", len(SubstanceBytes))
	}
	for n, b := range want {
		if SubstanceBytes[n] != b {
			t.Errorf("%s is %d, want %d", n, SubstanceBytes[n], b)
		}
		if SubstanceNames[b] != n {
			t.Errorf("byte %d is %q, want %q", b, SubstanceNames[b], n)
		}
	}
}

func TestEachMaterialIsReadByItsName(t *testing.T) {
	for b, n := range SubstanceNames {
		f := newGrid(t)
		f.change(signLocal, withSubstance(uint8(b)))
		res := play(t, f, hdr+"say \"go\" on 0\nexpect substance sign is "+n+" within 400ms\n")
		wantExit(t, res, 0)
		mustHave(t, res, "substance sign "+n)
		// Another name fails, and the line says what it read.
		other := SubstanceNames[(b+1)%len(SubstanceNames)]
		res = play(t, f, hdr+"say \"go\" on 0\nexpect substance sign is "+other+" within 150ms\n")
		wantExit(t, res, 1)
		mustHave(t, res, "unmatched substance sign is "+other, "substance sign "+n)
	}
}

func TestAMaterialByteOutsideTheEightIsItsNumber(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withSubstance(9))
	res := play(t, f, hdr+"say \"go\" on 0\nexpect substance sign is wood within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "substance sign 9")
	// It is a reading all the same: a change from it is seen, and it is a
	// capture that holds the number.
	f = newGrid(t)
	f.change(signLocal, withSubstance(9))
	moves(t, f, map[string]func(*sl.Seen){"go": withSubstance(SubstanceBytes["glass"])})
	res = play(t, f, hdr+`expect substance sign is any within 500ms as $m
say "go" on 0
expect substance sign changes within 1s
expect substance sign becomes $m within 150ms
`)
	wantExit(t, res, 1)
	mustHave(t, res, "capture $m = 9 (step 1)", "substance sign glass")
	// The byte's high nibble is kept: the viewer keeps the byte whole.
	f = newGrid(t)
	f.change(signLocal, withSubstance(0x13))
	res = play(t, f, hdr+"say \"go\" on 0\nexpect substance sign is wood within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "substance sign 19")
}

func TestAMaterialNoUpdateHasSaidIsNotStone(t *testing.T) {
	f := newGrid(t)
	// Material 0, not known: what an older slgod, or a prim never described,
	// says.
	f.objects[0].Material, f.objects[0].MaterialKnown = 0, false
	res := play(t, f, hdr+"say \"go\" on 0\nexpect substance sign is stone within 200ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "no update has said the material of sign", "the slgod that stores material and material_known")
	if strings.Contains(res.Transcript, "substance sign stone") {
		t.Errorf("an unknown material was read as stone:\n%s", res.Transcript)
	}
	// A negative needs a reading to judge.
	res = play(t, f, hdr+"say \"go\" on 0\nexpect no substance sign is stone within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "no reading was taken")
	// Known stone reads.
	f = newGrid(t)
	f.change(signLocal, withSubstance(0))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect substance sign is stone within 300ms\n"), 0)
}

func TestSubstanceBecomesChangesAndOriginal(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withSubstance(SubstanceBytes["stone"]))
	moves(t, f, map[string]func(*sl.Seen){"go": withSubstance(SubstanceBytes["glass"]), "back": withSubstance(SubstanceBytes["stone"])})
	res := play(t, f, hdr+`expect substance sign is stone within 500ms
say "go" on 0
expect substance sign changes within 1s
expect substance sign is glass within 300ms
say "back" on 0
expect substance sign becomes original within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "substance sign stone", "substance sign glass")

	// A becomes needs a change to the name: a prim that already is it fails.
	f = newGrid(t)
	f.change(signLocal, withSubstance(SubstanceBytes["wood"]))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect substance sign becomes wood within 150ms\n"), 1)

	// A forbidden change fails at once when it comes, and one that does not
	// come passes.
	f = newGrid(t)
	f.change(signLocal, withSubstance(SubstanceBytes["stone"]))
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withSubstance(SubstanceBytes["metal"])) })
	res = play(t, f, hdr+"say \"go\" on 0\nexpect no substance sign changes within 300ms\n")
	wantExit(t, res, 1)
	f = newGrid(t)
	f.change(signLocal, withSubstance(SubstanceBytes["stone"]))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect no substance sign changes within 200ms\n"), 0)
	f = newGrid(t)
	f.change(signLocal, withSubstance(SubstanceBytes["stone"]))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect no substance sign is metal within 200ms\n"), 0)
}

func TestSubstanceCaptureCarriesTheReading(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withSubstance(SubstanceBytes["rubber"]))
	moves(t, f, map[string]func(*sl.Seen){"go": withSubstance(SubstanceBytes["light"]), "back": withSubstance(SubstanceBytes["rubber"])})
	res := play(t, f, hdr+`expect substance sign is any within 500ms as $m
say "go" on 0
expect substance sign becomes light within 1s
then expect no substance sign is $m within 150ms
say "back" on 0
expect substance sign becomes $m within 1s as $again
`)
	wantExit(t, res, 0)
	mustHave(t, res, "capture $m = rubber (step 1)", "capture $again = rubber")
}

func TestSubstanceOfALink(t *testing.T) {
	f, _, _, _ := storeWorld(t)
	f.change(201, withSubstance(SubstanceBytes["wood"]))
	f.change(202, withSubstance(SubstanceBytes["metal"]))
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, 201, withSubstance(SubstanceBytes["flesh"])) })
	res := play(t, f, hdr+`expect substance vendor link 2 is wood within 500ms
say "go" on 0
expect substance vendor link 2 becomes flesh within 1s
expect no substance vendor link 3 changes within 200ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "substance vendor link 2 flesh")
}

func TestEveryNameIsInTheLanguageDocument(t *testing.T) {
	// The word list of doc/slate-language.md must carry the new words, and
	// the grammar the production.
	raw, err := os.ReadFile("../doc/slate-language.md")
	if err != nil {
		t.Fatal(err)
	}
	b := string(raw)
	for _, w := range append([]string{"substance"}, SubstanceNames[:]...) {
		if !strings.Contains(b, " "+w) && !strings.Contains(b, "\n"+w) {
			t.Errorf("doc/slate-language.md does not mention %q", w)
		}
	}
	if !strings.Contains(b, "substanceexp") {
		t.Error("doc/slate-language.md has no substanceexp production")
	}
}
