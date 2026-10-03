package slate

import (
	"context"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids, made with tools/new-id.
var (
	idHud      = msg.MustParseUUID("48bc7e57-7e57-c0de-0710-03c081aeeda8")
	idHudBtn1  = msg.MustParseUUID("50a57e57-7e57-c0de-1a49-bebb0b8112e8")
	idHudBtn2  = msg.MustParseUUID("5b697e57-7e57-c0de-cf18-8eb4635f438d")
	idHat      = msg.MustParseUUID("65357e57-7e57-c0de-7401-d601a8968ba8")
	idOrphan   = msg.MustParseUUID("75347e57-7e57-c0de-6f3b-ec96753262ba")
	idChair    = msg.MustParseUUID("84737e57-7e57-c0de-21fb-4080d4f64a21")
	idChairLeg = msg.MustParseUUID("a7497e57-7e57-c0de-3222-dddb4627c1b4")
)

const hudHdr = "slate 1\nobject hud is \"Example HUD\"\nobject btn is \"Example Button\"\n"

// withHud adds a worn HUD of a root and two children, and a hat worn
// beside it, to the grid.
func (f *fakeGrid) withHud() {
	root := prim(idHud, 201, "Example HUD", testMe)
	root.Parent, root.AttachPoint = 1, 35
	hat := prim(idHat, 204, "Example Hat", testMe)
	hat.Parent, hat.AttachPoint = 1, 2
	f.objects = append(f.objects, root,
		child(prim(idHudBtn1, 202, "Example Button", testMe), root),
		child(prim(idHudBtn2, 203, "Example Dial", testMe), root),
		hat)
}

// setupOf runs only the lookup and linkset steps of setup.
func setupOf(t *testing.T, f *fakeGrid, src string) (*runner, error) {
	t.Helper()
	r := &runner{sess: f.session(t), s: mustCheck(t, src), cfg: testCfg(), bind: map[string]*binding{}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.setupObjects(ctx); err != nil {
		return r, err
	}
	return r, r.setupLinksets(ctx)
}

func ids(ps []*sl.Seen) []msg.UUID {
	var out []msg.UUID
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func TestAWornHudIsItsRootAndChildrenOnly(t *testing.T) {
	f := newGrid(t)
	f.withHud()
	r, err := setupOf(t, f, hudHdr+"say \"x\" on 0\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []msg.UUID{idHud, idHudBtn1, idHudBtn2}
	for _, name := range []string{"hud", "btn"} { // btn is bound to a child and climbs
		got := ids(r.bind[name].prims())
		if len(got) != 3 || got[0] != want[0] {
			t.Fatalf("%s: linkset %v, want root first of %v", name, got, want)
		}
		for _, id := range got {
			if id == idHat || id == testMe {
				t.Errorf("%s: linkset holds %s, a sibling attachment or the avatar", name, id)
			}
		}
		if !r.bind[name].owns(idHudBtn2) || r.bind[name].owns(idHat) || !r.bind[name].named("Example Dial") {
			t.Errorf("%s: owns/named do not follow the linkset", name)
		}
	}
}

func TestAMissingParentIsASetupFailure(t *testing.T) {
	f := newGrid(t)
	o := prim(idOrphan, 301, "Example Orphan", testMe)
	o.Parent = 999
	f.objects = append(f.objects, o)
	res, err := tryPlay(t, f, "slate 1\nobject o is \"Example Orphan\"\nsay \"x\" on 0\n", Options{}, testCfg())
	if err == nil {
		t.Error("no error")
	}
	wantExit(t, res, 3)
	mustHave(t, res, `slate: setup: the parent of "Example Orphan" is not in the store`)
}

func TestTwoProbedBindingsInOneLinksetAreASetupError(t *testing.T) {
	f := newGrid(t)
	f.withHud()
	_, err := setupOf(t, f, hudHdr+"probe hud\nprobe btn\nsay \"x\" on 0\n")
	if err == nil || err.Error() != "hud and btn are in one linkset and both have a probe; one probe covers the whole linkset" {
		t.Errorf("err = %v", err)
	}
	f = newGrid(t)
	f.withHud()
	if _, err := setupOf(t, f, hudHdr+"probe hud\nsay \"x\" on 0\n"); err != nil {
		t.Errorf("one probe: %v", err)
	}
}

func TestFromObjectMatchesAChildOfTheLinkset(t *testing.T) {
	f := newGrid(t)
	f.withHud()
	f.on("go", chatMsg("Example Button", idHudBtn1, sl.ChatSay, "hi"))
	res := play(t, f, hudHdr+"say \"go\" on 0\nexpect say \"hi\" on public from object hud within 300ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, `chat public from btn: "hi"`)

	// A sibling attachment is not the HUD.
	f = newGrid(t)
	f.withHud()
	f.on("go", chatMsg("Example Hat", idHat, sl.ChatSay, "hi"))
	wantExit(t, play(t, f, hudHdr+"say \"go\" on 0\nexpect say \"hi\" on public from object hud within 100ms\n"), 1)
}

func TestADialogFromAChildMatchesTheLinkset(t *testing.T) {
	f := newGrid(t)
	f.withHud()
	// Another name, so only the id of a member can make it the HUD's.
	f.on("go", dialogMsg(idHudBtn2, "Something Else", "Pick", -5, "A"))
	res := play(t, f, hudHdr+"say \"go\" on 0\nexpect dialog from hud text \"Pick\" button \"A\" within 300ms\n")
	wantExit(t, res, 0)
}

func TestRegionPositionIsTheAncestorsWithNoOffsets(t *testing.T) {
	root := at(prim(idChair, 401, "Example Chair", testMe), 50, 60, 70)
	leg := at(child(prim(idChairLeg, 402, "Example Leg", testMe), root), 1, 1, -1)
	all := []*sl.Seen{root, leg}
	if p, ok := regionPos(all, leg); !ok || p != root.Position {
		t.Errorf("child: %v %v, want the root's %v", p, ok, root.Position)
	}
	if p, ok := regionPos(all, root); !ok || p != root.Position {
		t.Errorf("root: %v %v", p, ok)
	}
	if _, ok := regionPos([]*sl.Seen{leg}, leg); ok {
		t.Error("a walk that leaves the store gave a position")
	}
	// Nine steps up is past the eight.
	var chain []*sl.Seen
	for i := range 10 {
		s := prim(idChair, uint32(500+i), "Example Link", testMe)
		if i > 0 {
			s.Parent = uint32(500 + i - 1)
		}
		chain = append(chain, s)
	}
	if _, ok := regionPos(chain, chain[9]); ok {
		t.Error("a chain of ten gave a position")
	}
	if _, ok := regionPos(chain, chain[8]); !ok {
		t.Error("a chain of nine did not")
	}
}
