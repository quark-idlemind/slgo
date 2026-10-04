package slate

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

const rezHdr = hdr + "item box is \"Example Box\" in \"Objects\"\n"

func TestRezParsesBothForms(t *testing.T) {
	s := mustParse(t, rezHdr+"rez box at 130 128.5 -25 as made\nrez box by -1 0 2.5 as other\n")
	a, b := s.Tests[0].Steps[0].Stimulus.Rez, s.Tests[0].Steps[1].Stimulus.Rez
	if a == nil || a.By || a.Item.Text != "box" || a.X.Value != 130 || a.Y.Value != 128.5 || a.Z.Value != -25 || a.As.Text != "made" {
		t.Errorf("at form: %+v", a)
	}
	if b == nil || !b.By || b.X.Value != -1 || b.Y.Value != 0 || b.Z.Value != 2.5 || b.As.Text != "other" {
		t.Errorf("by form: %+v", b)
	}
	// The word is not reserved: a name may be rez, and expect rez is still
	// the expectation.
	mustCheck(t, rezHdr+"rez box at 1 2 3 as rez\ntouch rez anywhere\n")
	s = mustParse(t, hdr+"touch sign anywhere\nexpect rez name \"Example Box\" from sign within 1s\n")
	if st := s.Tests[0].Steps[0]; st.Stimulus.Touch == nil || st.Stimulus.Rez != nil || st.Expect[0].Rez == nil {
		t.Errorf("expect rez was not read as the expectation: %+v", st)
	}
}

func TestRezParseErrors(t *testing.T) {
	parseErr(t, rezHdr+"rez box as made\n", "expected at or by")
	parseErr(t, rezHdr+"rez box at 1 2 as made\n", "expected a number")
	parseErr(t, rezHdr+"rez box at 1 2 3\n", "expected as")
	parseErr(t, rezHdr+"rez box at 1 2 3 as\n", "expected a name")
	parseErr(t, rezHdr+"rez box at 1 2 3 by 1 2 3 as made\n", "expected as")
	parseErr(t, rezHdr+"rez at 1 2 3 as made\n", "expected")
	parseErr(t, rezHdr+"rez\n", "expected a name")
}

func TestRezChecks(t *testing.T) {
	mustCheck(t, rezHdr+"rez box at 130 128 25 as made\nexpect position made is 130 128 25\n")
	refuses(t, rezHdr+"rez sign at 1 2 3 as made\n", "sign at", "sign is an object; rez takes an item")
	refuses(t, rezHdr+"rez nothing at 1 2 3 as made\n", "nothing", "nothing is not an item")
	refuses(t, rezHdr+"rez box at 1 2 3 as sign\n", "sign\n", "sign is already bound")
	refuses(t, rezHdr+"rez box at 1 2 3 as box\n", "box\n", "box is already bound")
	refuses(t, rezHdr+"rez box at 1 2 3 as made\nrez box at 1 2 3 as made\n", "last:made", "made is already bound")
	refuses(t, rezHdr+"rez box at 99999999999999999999 2 3 as made\n", "99999999999999999999", "number is not an exact float")
	// The binding is used from the step on, and in after each only when
	// before each made it.
	refuses(t, rezHdr+"test \"t\" {\n  touch made anywhere\n  rez box at 1 2 3 as made\n}\n", "made anywhere", "made is not an object")
	mustCheck(t, rezHdr+"before each { rez box at 1 2 3 as made }\ntest \"t\" { touch made anywhere }\nafter each { touch made anywhere }\n")
	refuses(t, rezHdr+"test \"t\" { rez box at 1 2 3 as made }\nafter each { touch made anywhere }\n", "made anywhere", "made is bound in test")
	// A rezzed object is in the world, not worn.
	refuses(t, rezHdr+"rez box at 1 2 3 as made\ndrag made on screen from 1 1 to 2 2\n", "made on", "made is rezzed in the world")
}

func TestRezBindsANameAndDeletesItWhenTheTestPasses(t *testing.T) {
	f := newGrid(t)
	f.mu.Lock()
	f.group = idBoxGroup
	f.mu.Unlock()
	g := f.withRezzing(t, true)
	g.says = "rezzed"
	res := play(t, f, rezHdr+`rez box at 130 128 25 as made
expect position made is 130 128 25 within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "chat public from made: \"rezzed\"", "pass step 1",
		`slate: deleted made ("Example Box") to the Trash`, `slate: pass test "t"`)
	r := sentOf[*msg.RezObject](f)
	if len(r) != 1 || r[0].InventoryData.ItemID != idBoxItem || r[0].AgentData.GroupID != idBoxGroup ||
		r[0].RezData.RayEnd != (msg.Vector3{X: 130, Y: 128, Z: 25}) {
		t.Errorf("sent %+v", r)
	}
	d := sentOf[*msg.DeRezObject](f)
	if len(d) != 1 || d[0].AgentBlock.Destination != 6 || d[0].AgentBlock.DestinationID != idTrashFolder {
		t.Errorf("deleted with %+v", d)
	}
	if f.listed("Example Box") {
		t.Error("the box is still in the region")
	}
	// Deleted before the verdict, and the pass line is after the delete.
	if iDel, iPass := strings.Index(res.Transcript, "slate: deleted made"), strings.Index(res.Transcript, "slate: pass test"); iDel > iPass {
		t.Errorf("the delete came after the pass line:\n%s", res.Transcript)
	}
}

func TestRezByIsAnOffsetFromTheTester(t *testing.T) {
	f := newGrid(t)
	f.withRezzing(t, true)
	res := play(t, f, rezHdr+`rez box by 2 -1 3 as made
expect position made is 130 127 25 within 1s
`)
	wantExit(t, res, 0)
	f = newGrid(t)
	f.withRezzing(t, true)
	res = play(t, f, rezHdr+`rez box by 2 -1 3 as made
expect position made is 1 1 1 within 300ms
`)
	wantExit(t, res, 1)
	mustHave(t, res, `rezzed "Example Box" at <130, 127, 25> as made`)
}

func TestRezIsDeletedWhenTheTestFails(t *testing.T) {
	f := newGrid(t)
	f.withRezzing(t, true)
	res := play(t, f, rezHdr+`rez box at 130 128 25 as made
expect position made is 1 1 1 within 300ms
`)
	wantExit(t, res, 1)
	mustHave(t, res, `slate: deleted made ("Example Box") to the Trash`)
	if n := len(sentOf[*msg.DeRezObject](f)); n != 1 {
		t.Errorf("%d deletes, want 1", n)
	}
	if f.listed("Example Box") {
		t.Error("the box is still in the region")
	}
}

func TestRezIsDeletedWhenAfterEachFails(t *testing.T) {
	f := newGrid(t)
	f.withRezzing(t, true)
	res := play(t, f, rezHdr+`before each {
  rez box at 130 128 25 as made
}
test "t" {
  expect position made is 130 128 25 within 1s
}
after each {
  expect position made is 1 1 1 within 300ms
}
`)
	wantExit(t, res, 1)
	mustHave(t, res, `slate: deleted made ("Example Box") to the Trash`)
	if f.listed("Example Box") {
		t.Error("the box is still in the region")
	}
}

func TestRezInBeforeEachMakesAFreshOneForEveryTest(t *testing.T) {
	f := newGrid(t)
	f.withRezzing(t, true)
	res := play(t, f, rezHdr+`before each {
  rez box at 130 128 25 as made
}
test "one" { expect position made is 130 128 25 within 1s }
test "two" { expect position made is 130 128 25 within 1s }
after each {
  expect position made is 130 128 25
}
`)
	wantExit(t, res, 0)
	if n := len(sentOf[*msg.RezObject](f)); n != 2 {
		t.Errorf("%d rezzes, want 2", n)
	}
	if n := len(sentOf[*msg.DeRezObject](f)); n != 2 {
		t.Errorf("%d deletes, want 2", n)
	}
	if f.listed("Example Box") {
		t.Error("a box is still in the region")
	}
}

func TestARefusedRezFailsTheStepAtOnceAndDeletesNothing(t *testing.T) {
	f := newGrid(t)
	g := f.withRezzing(t, true)
	alert := "Can't rez object 'Example Box' at { 130, 128, 25 } on parcel 'Example Parcel' in region Test Region " +
		"because the owner of this land does not allow it.  Use the land tool to see land ownership."
	g.refuse = alert
	began := time.Now()
	res := play(t, f, rezHdr+`rez box at 130 128 25 as made
expect position made is 130 128 25 within 20s
`)
	wantExit(t, res, 1)
	if d := time.Since(began); d > 8*time.Second {
		t.Errorf("the refusal took %s, which is the budget and not the alert", d)
	}
	mustHave(t, res, "Can't rez object 'Example Box'", "does not allow it")
	if n := len(sentOf[*msg.DeRezObject](f)); n != 0 {
		t.Errorf("%d deletes, want none", n)
	}
	mustNotHave(t, res, "slate: deleted")
	mustNotHave(t, res, "delete failed")
}

func TestADeleteThatFailsFailsTheTest(t *testing.T) {
	f := newGrid(t)
	f.withRezzing(t, false) // no Trash folder to delete to
	res := play(t, f, rezHdr+`rez box at 130 128 25 as made
expect position made is 130 128 25 within 1s
`)
	wantExit(t, res, 1)
	if len(res.Tests) != 1 || res.Tests[0].Passed {
		t.Fatalf("tests = %+v", res.Tests)
	}
	mustHave(t, res, `slate: delete failed: made ("Example Box")`, `slate: fail test "t": could not delete made`)
	mustNotHave(t, res, `slate: pass test`)
}

func TestARezNotDescribedInTimeFailsAndIsStillSweptUp(t *testing.T) {
	f := newGrid(t)
	g := f.withRezzing(t, true)
	g.delay = 5 * time.Second
	res := play(t, f, rezHdr+`rez box at 130 128 25 as made
expect position made is 130 128 25 within 1200ms
`)
	wantExit(t, res, 1)
	mustHave(t, res, "Example Box")
}
