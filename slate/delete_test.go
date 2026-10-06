package slate

import (
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// The delete stimulus over the fake grid: a vendor rezzes a balloon the
// tester owns, and the test names it to be put in the Trash.

const deleteClaim = `say "go" on 0
expect rez name "Example Balloon" from vendor as balloon within 1s
`

// alsoWhenSaid is whenSaid that keeps the handler already set, which is
// what answers a delete to the Trash: replyTo replaces it.
func (f *fakeGrid) alsoWhenSaid(trigger string, fn func()) {
	f.mu.Lock()
	prev := f.onSend
	f.mu.Unlock()
	f.replyTo(func(m msg.Message) {
		if prev != nil {
			prev(m)
		}
		if text, _, ok := says(m); ok && text == trigger {
			fn()
		}
	})
}

// ownVendor makes the tester the owner of the vendor, so that what it
// rezzes is the tester's.
func ownVendor(f *fakeGrid) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.objects {
		if o.Name == "Example Tip Jar" {
			o.Owner = testMe
		}
	}
}

// ownBalloon is the vendor's balloon, owned by the tester.
func ownBalloon(f *fakeGrid) {
	ownVendor(f)
	b := balloonAt(idRootA, 201, 130)
	b.Owner = testMe
	f.alsoWhenSaid("go", func() { f.appear(b) })
}

func TestDeleteTakesAClaimedObjectToTheTrash(t *testing.T) {
	f := newGrid(t)
	f.withRezzing(t, true)
	ownBalloon(f)
	res := play(t, f, rezHdr+deleteClaim+"delete balloon\n")
	wantExit(t, res, 0)
	mustHave(t, res, `slate: deleted balloon ("Example Balloon") to the Trash`, "slate: pass step 2")
	d := sentOf[*msg.DeRezObject](f)
	if len(d) != 1 || d[0].AgentBlock.Destination != 6 || d[0].AgentBlock.DestinationID != idTrashFolder {
		t.Errorf("deleted with %+v", d)
	}
	if f.listed("Example Balloon") {
		t.Error("the balloon is still in the region")
	}
}

// A live region's updates carry no name, only Properties does: the line
// names the object by the name the claim matched.
func TestDeleteNamesAnObjectWhoseUpdatesCarryNoName(t *testing.T) {
	f := newGrid(t)
	f.withRezzing(t, true)
	ownVendor(f)
	b := balloonAt(idRootA, 201, 130)
	b.Owner, b.Name = testMe, ""
	f.setProps(idRootA, "Example Balloon", "", testMe)
	f.alsoWhenSaid("go", func() { f.appear(b) })
	res := play(t, f, rezHdr+deleteClaim+"delete balloon\n")
	wantExit(t, res, 0)
	mustHave(t, res, `slate: deleted balloon ("Example Balloon") to the Trash`)
}

func TestDeleteOfAnObjectAlreadyGoneIsALine(t *testing.T) {
	f := newGrid(t)
	f.withRezzing(t, true)
	ownBalloon(f)
	f.alsoWhenSaid("pop", func() { f.disappear(201) })
	res := play(t, f, rezHdr+deleteClaim+"say \"pop\" on 0\ndelete balloon\n")
	wantExit(t, res, 0)
	mustHave(t, res, `slate: balloon ("Example Balloon") is already gone; nothing deleted`)
	mustNotHave(t, res, "slate: deleted")
	if n := len(sentOf[*msg.DeRezObject](f)); n != 0 {
		t.Errorf("%d deletes, want none", n)
	}
}

func TestDeleteRefusesAnObjectThatIsNotTheTesters(t *testing.T) {
	f := newGrid(t)
	f.withRezzing(t, true)
	// The vendor and the balloon are somebody else's.
	b := balloonAt(idRootA, 201, 130)
	f.alsoWhenSaid("go", func() { f.appear(b) })
	res := play(t, f, rezHdr+deleteClaim+"delete balloon\n")
	wantExit(t, res, 1)
	mustHave(t, res, `balloon ("Example Balloon") is not the tester's; it was not deleted`)
	mustNotHave(t, res, idStranger.String())
	if n := len(sentOf[*msg.DeRezObject](f)); n != 0 {
		t.Errorf("%d deletes, want none", n)
	}
	if !f.listed("Example Balloon") {
		t.Error("the balloon was deleted")
	}
}

func TestDeleteNamesOnlyWhatARezBound(t *testing.T) {
	checkErr(t, rezHdr+"delete vendor\n", "vendor is an object header; delete takes a name a rez expectation")
	checkErr(t, rezHdr+"delete box\n", "box is an item")
	checkErr(t, rezHdr+"delete nothing\n", "nothing is not an object")
	checkErr(t, rezHdr+"delete balloon\nexpect rez name \"Example Balloon\" from vendor as balloon within 1s\n", "balloon is bound in this step")
	checkErr(t, rezHdr+"wear box on \"Chest\" as worn\ndelete worn\n", "worn is not bound by a rez in this test")
	// A rez step and a rez expectation both bind a name delete takes.
	mustCheck(t, rezHdr+"rez box at 130 128 25 as made\ndelete made\n")
	mustCheck(t, rezHdr+deleteClaim+"delete balloon\n")
}

func TestDeleteInAfterEachAfterATestThatBoundTheName(t *testing.T) {
	f := newGrid(t)
	f.withRezzing(t, true)
	ownBalloon(f)
	src := rezHdr + `test "t" {
` + deleteClaim + `}
after each {
  delete balloon
}
`
	mustCheck(t, src)
	res := play(t, f, src)
	wantExit(t, res, 0)
	mustHave(t, res, `slate: deleted balloon ("Example Balloon") to the Trash`)
	if f.listed("Example Balloon") {
		t.Error("the balloon is still in the region")
	}
}

func TestDeleteInAfterEachAfterATestThatFailedBeforeBindingIt(t *testing.T) {
	f := newGrid(t)
	f.withRezzing(t, true)
	// Nothing rezzes: the claim of the first test fails, and the name is
	// never bound there. The second test binds it, so the check allows it.
	ownVendor(f)
	f.alsoWhenSaid("go2", func() {
		b := balloonAt(idRootA, 201, 130)
		b.Owner = testMe
		f.appear(b)
	})
	res := play(t, f, rezHdr+`test "fails" {
  say "go" on 0
  expect rez name "Example Balloon" from vendor as balloon within 300ms
}
test "binds" {
  say "go2" on 0
  expect rez name "Example Balloon" from vendor as balloon within 1s
}
after each {
  delete balloon
}
`)
	wantExit(t, res, 1)
	mustHave(t, res, "slate: balloon was not bound in this test; nothing deleted",
		`slate: deleted balloon ("Example Balloon") to the Trash`, `test "fails" step 1`, `slate: pass test "binds"`, "slate: failed 1 of 2 tests")
	mustNotHave(t, res, "delete failed")
	if n := len(sentOf[*msg.DeRezObject](f)); n != 1 {
		t.Errorf("%d deletes, want 1", n)
	}
}

func TestDeleteOfARezStepObjectIsNotDeletedAgainAtTheEnd(t *testing.T) {
	f := newGrid(t)
	f.withRezzing(t, true)
	res := play(t, f, rezHdr+"rez box at 130 128 25 as made\ndelete made\n")
	wantExit(t, res, 0)
	mustHave(t, res, `slate: deleted made ("Example Box") to the Trash`, `slate: pass test "t"`)
	mustNotHave(t, res, "delete failed")
	if n := len(sentOf[*msg.DeRezObject](f)); n != 1 {
		t.Errorf("%d deletes, want 1", n)
	}
}

// The worked example of doc/slate-language.md, "A vendor's balloon, deleted
// afterwards", passes the check, and runs over the fake.
func TestTheDeleteWorkedExample(t *testing.T) {
	src := `slate 1

object vendor is "Example Tip Jar"

test "a balloon pops" {
  say "balloon" on 0
  expect rez name "Example Balloon" from vendor as balloon within 5s

  touch balloon anywhere
  expect say "pop" on public from object balloon within 3s
}

after each {
  delete balloon
}
`
	mustCheck(t, src)
}
