package slate

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

func TestGroupParsesBothForms(t *testing.T) {
	s := mustCheck(t, secondHdr+"group visitor \"Example Group\"\ngroup visitor none\n")
	a, b := s.Tests[0].Steps[0].Stimulus.Group, s.Tests[0].Steps[1].Stimulus.Group
	if a == nil || a.None || a.Avatar.Text != "visitor" || a.Group != "Example Group" {
		t.Errorf("named: %+v", a)
	}
	if b == nil || !b.None || b.Avatar.Text != "visitor" || b.Group != "" {
		t.Errorf("none: %+v", b)
	}
	// A quoted none is a group called none, and the word is not reserved.
	s = mustCheck(t, "slate 1\navatar group\nobject none is \"Example Sign\"\ngroup group \"none\"\ntouch none anywhere\n")
	if g := s.Tests[0].Steps[0].Stimulus.Group; g == nil || g.None || g.Group != "none" {
		t.Errorf("quoted none: %+v", g)
	}
}

func TestGroupParseErrors(t *testing.T) {
	parseErr(t, secondHdr+"group visitor\n", "expected a string")
	parseErr(t, secondHdr+"group visitor Example\n", "expected a string")
	parseErr(t, secondHdr+"group \"Example Group\"\n", "expected a name")
	parseErr(t, secondHdr+"group visitor matching \"x\"\n", "matching is not legal here")
}

func TestGroupChecks(t *testing.T) {
	refuses(t, secondHdr+"group tester \"Example Group\"\n", "tester", "group stays off the tester")
	refuses(t, secondHdr+"group tester none\n", "tester", "its active group decides where it may build")
	refuses(t, secondHdr+"group ghost none\n", "ghost", "ghost is not an avatar")
	refuses(t, secondHdr+"group sign none\n", "last:sign", "sign is an object, not an avatar")
	refuses(t, secondHdr+"group visitor \"\"\n", "\"\"", "non-empty string")
	refuses(t, secondHdr+"group visitor \"Example Group\" as visitor\n", "last:visitor", "an as does not follow it")
	refuses(t, hdr+"item hat is \"Example Hat\" in \"Objects\"\ngroup hat none\n", "hat none", "hat is an item, not an avatar")
}

// groupSecond is a second avatar whose grid answers ActivateGroup, with
// three groups of which it has joined two, active in the first.
type groupSecond struct {
	g     *fakeGrid
	mu    sync.Mutex
	calls int
	stay  bool // a group change after the first is ignored
	deaf  bool // every group change is ignored
}

func (f *fakeGrid) withSecondGroups(t *testing.T, active msg.UUID) (*fakeGrid, *groupSecond) {
	t.Helper()
	g := f.secondGrid(t)
	g.groups = []sl.Group{{ID: idGroupOne, Name: "Example Group"}, {ID: idGroupTwo, Name: "Example Builders"}}
	g.group = active
	gs := &groupSecond{g: g}
	g.replyTo(func(m msg.Message) {
		if x, ok := m.(*msg.ActivateGroup); ok {
			gs.mu.Lock()
			gs.calls++
			stay := gs.deaf || gs.stay && gs.calls > 1
			gs.mu.Unlock()
			if stay {
				return
			}
			g.mu.Lock()
			g.group = x.AgentData.GroupID
			g.mu.Unlock()
		}
	})
	return g, gs
}

func (g *fakeGrid) active() msg.UUID {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.group
}

func activations(g *fakeGrid) []msg.UUID {
	var out []msg.UUID
	for _, a := range sentOf[*msg.ActivateGroup](g) {
		out = append(out, a.AgentData.GroupID)
	}
	return out
}

func TestGroupSetsTheSecondAvatarsGroupAndPutsItBackAtTheEndOfTheRun(t *testing.T) {
	f := newGrid(t)
	g, _ := f.withSecondGroups(t, idGroupTwo)
	res := playWithSecond(t, f, g, secondHdr+"test \"a\" {\n  group visitor \"Example Group\"\n}\ntest \"b\" {\n  group visitor none\n}\n")
	wantExit(t, res, 0)
	mustHave(t, res, "slate: cleanup: put the group of visitor back to what it was")
	got := activations(g)
	want := []msg.UUID{idGroupOne, {}, idGroupTwo}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("the second avatar sent %v, want %v", got, want)
	}
	if g.active() != idGroupTwo {
		t.Errorf("the second avatar ends in %s, want the group it began in", g.active())
	}
	// Once, at the end of the run, and before the verdict.
	if n := strings.Count(res.Transcript, "put the group of visitor back"); n != 1 {
		t.Errorf("%d restore lines, want 1:\n%s", n, res.Transcript)
	}
	if i, j := strings.Index(res.Transcript, "put the group of visitor back"), strings.Index(res.Transcript, "slate: passed"); i < 0 || i > j {
		t.Errorf("the restore came after the verdict:\n%s", res.Transcript)
	}
	// The tester's own group is never touched.
	if n := len(sentOf[*msg.ActivateGroup](f)); n != 0 {
		t.Errorf("the tester sent %d ActivateGroup", n)
	}
}

func TestGroupRestoresNoneAndIsRestoredWhenATestFails(t *testing.T) {
	f := newGrid(t)
	g, _ := f.withSecondGroups(t, msg.UUID{})
	res := playWithSecond(t, f, g, secondHdr+"group visitor \"example group\"\nexpect texture sign face 5 is "+idTexA.String()+" within 200ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "set the group of visitor to \"example group\"", "slate: cleanup: put the group of visitor back")
	if g.active() != (msg.UUID{}) {
		t.Errorf("the second avatar ends in %s, want none", g.active())
	}
}

func TestGroupThatIsActiveAlreadyChangesNothingToPutBack(t *testing.T) {
	f := newGrid(t)
	g, _ := f.withSecondGroups(t, idGroupOne)
	res := playWithSecond(t, f, g, secondHdr+"group visitor \"Example Group\"\ngroup visitor \"Example Group\"\nexpect texture sign face 5 is "+idTexA.String()+" within 100ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "the group of visitor was already \"Example Group\"")
	mustNotHave(t, res, "slate: cleanup:")
	if n := len(sentOf[*msg.ActivateGroup](g)); n != 0 {
		t.Errorf("%d ActivateGroup for a group already active", n)
	}
}

func TestGroupTheAvatarIsNotInFailsTheStepWithoutNamingIt(t *testing.T) {
	f := newGrid(t)
	g, _ := f.withSecondGroups(t, idGroupOne)
	res := playWithSecond(t, f, g, secondHdr+"group visitor \"Example Club\"\n")
	wantExit(t, res, 1)
	mustHave(t, res, `visitor has not joined a group called "Example Club"`)
	mustNotHave(t, res, "Example Resident")
	if n := len(sentOf[*msg.ActivateGroup](g)); n != 0 {
		t.Errorf("%d ActivateGroup for a group the avatar is not in", n)
	}
	// Nothing changed, so nothing is put back.
	mustNotHave(t, res, "slate: cleanup:")
	// A list that has not arrived says so.
	f = newGrid(t)
	g, _ = f.withSecondGroups(t, msg.UUID{})
	g.mu.Lock()
	g.groups = nil
	g.mu.Unlock()
	res = playWithSecond(t, f, g, secondHdr+"group visitor \"Example Group\"\n")
	wantExit(t, res, 1)
	mustHave(t, res, "visitor has no groups that are known yet")
}

func TestGroupThatCannotBePutBackIsAWarningThatNamesTheBinding(t *testing.T) {
	f := newGrid(t)
	g, gs := f.withSecondGroups(t, idGroupTwo)
	gs.mu.Lock()
	gs.stay = true
	gs.mu.Unlock()
	old := groupUndoFor
	groupUndoFor = 300 * time.Millisecond
	t.Cleanup(func() { groupUndoFor = old })
	res := playWithSecond(t, f, g, secondHdr+"group visitor \"Example Group\"\nexpect texture sign face 5 is "+idTexA.String()+" within 500ms\n")
	wantExit(t, res, 1)
	// The change is made and the test fails on its expectation; the put
	// back is ignored, and a failure to put back is a warning that names
	// the binding.
	mustHave(t, res, "slate: cleanup: warning: the group of visitor was not put back")
	mustNotHave(t, res, "Example Resident")
}

func TestGroupNeedsTheAvatarGiven(t *testing.T) {
	f := newGrid(t)
	res, err := tryPlay(t, f, secondHdr+"group visitor none\n", Options{}, testCfg())
	if err == nil {
		t.Fatalf("a run without the avatar went on:\n%s", res.Transcript)
	}
	wantExit(t, res, 3)
	mustHave(t, res, "visitor is declared but no --avatar visitor=... was given")
}

func TestGroupThatNeverBecomesActiveFailsTheStep(t *testing.T) {
	f := newGrid(t)
	g, gs := f.withSecondGroups(t, idGroupTwo)
	gs.mu.Lock()
	gs.deaf = true
	gs.mu.Unlock()
	res := playWithSecond(t, f, g, secondHdr+"group visitor \"Example Group\"\nexpect texture sign face 5 is "+idTexA.String()+" within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "the group of visitor did not become \"Example Group\"")
	mustNotHave(t, res, "Example Resident")
}
