package slate

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// Invented ids, made with tools/new-id.
var (
	idSignN = msg.MustParseUUID("01907e57-7e57-c0de-5298-68af87d37a60")
	idSignS = msg.MustParseUUID("3dfd7e57-7e57-c0de-ec12-8a98ee59f833")
	idSignE = msg.MustParseUUID("8fb57e57-7e57-c0de-994c-20fa292cd18f")
)

const twinHdr = "slate 1\nobject north is \"Example Slider\" description \"north\"\nobject south is \"Example Slider\" description \"south\"\n"

// twinGrid has two objects named alike, described differently.
func twinGrid(t *testing.T, dn, ds string) *fakeGrid {
	f := newGrid(t)
	f.objects = append(f.objects, prim(idSignN, 301, "Example Slider", testMe), prim(idSignS, 302, "Example Slider", testMe))
	f.setProps(idSignN, "Example Slider", dn, testMe)
	f.setProps(idSignS, "Example Slider", ds, testMe)
	return f
}

func TestTwinsAreToldApartByDescription(t *testing.T) {
	f := twinGrid(t, "north", "south")
	r, err := setupOf(t, f, twinHdr+"say \"x\" on 0\n")
	if err != nil {
		t.Fatal(err)
	}
	if r.bind["north"].seen.ID != idSignN || r.bind["south"].seen.ID != idSignS {
		t.Errorf("north is %s and south is %s", r.bind["north"].seen.ID, r.bind["south"].seen.ID)
	}
}

func TestADescriptionMayBeAPattern(t *testing.T) {
	f := twinGrid(t, "the north one", "the south one")
	r, err := setupOf(t, f, "slate 1\nobject s is \"Example Slider\" description matching \"^the s\"\nsay \"x\" on 0\n")
	if err != nil {
		t.Fatal(err)
	}
	if r.bind["s"].seen.ID != idSignS {
		t.Errorf("bound %s", r.bind["s"].seen.ID)
	}
}

func TestADescriptionThatMatchesNoneFailsSetup(t *testing.T) {
	f := twinGrid(t, "north", "south")
	_, err := setupOf(t, f, "slate 1\nobject s is \"Example Slider\" description \"west\"\nsay \"x\" on 0\n")
	want := `"Example Slider" with description "west" matches none of 2 objects of that name`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestADescriptionThatMatchesTwoFailsSetup(t *testing.T) {
	f := twinGrid(t, "same one", "same two")
	_, err := setupOf(t, f, "slate 1\nobject s is \"Example Slider\" description matching \"^same\"\nsay \"x\" on 0\n")
	if err == nil || !strings.Contains(err.Error(), `with description "^same" matches 2 objects`) ||
		!strings.Contains(err.Error(), idSignN.String()) || !strings.Contains(err.Error(), idSignS.String()) {
		t.Fatalf("got %v", err)
	}
}

func TestAPropertiesErrorFailsSetup(t *testing.T) {
	f := newGrid(t)
	f.objects = append(f.objects, prim(idSignN, 301, "Example Slider", testMe)) // never answers
	_, err := setupOf(t, f, "slate 1\nobject s is \"Example Slider\" description \"north\"\nsay \"x\" on 0\n")
	if err == nil || !strings.Contains(err.Error(), `properties of "Example Slider" (150ms): `) {
		t.Fatalf("got %v", err)
	}
}

func TestOnePrimMatchingTwoHeadersFailsSetup(t *testing.T) {
	f := twinGrid(t, "north", "south")
	_, err := setupOf(t, f, "slate 1\nobject a is \"Example Slider\" description \"north\"\nobject b is \"Example Slider\" description matching \"nor\"\nsay \"x\" on 0\n")
	if err == nil || !strings.Contains(err.Error(), "the same object as a") {
		t.Fatalf("got %v", err)
	}
}

func TestAHeaderWithoutDescriptionStillNeedsOneMatch(t *testing.T) {
	f := newGrid(t)
	f.objects = append(f.objects, prim(idSignE, 303, "Example Slider", testMe))
	r, err := setupOf(t, f, "slate 1\nobject s is \"Example Slider\"\nsay \"x\" on 0\n")
	if err != nil || r.bind["s"].seen.ID != idSignE {
		t.Fatalf("one match: %v", err)
	}
	g := twinGrid(t, "north", "south")
	if _, err := setupOf(t, g, "slate 1\nobject s is \"Example Slider\"\nsay \"x\" on 0\n"); err == nil || !strings.Contains(err.Error(), "names 2 objects") {
		t.Fatalf("two matches: %v", err)
	}
}
