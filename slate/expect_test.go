package slate

import (
	"fmt"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// whenSaid makes the grid do something when the tester says trigger.
func (f *fakeGrid) whenSaid(trigger string, fn func()) {
	f.replyTo(func(m msg.Message) {
		if text, _, ok := says(m); ok && text == trigger {
			fn()
		}
	})
}

// signAt is the sign's first face wearing a texture, from the start.
func signAt(f *fakeGrid, tex msg.UUID) {
	f.objects[0].TextureEntry = teOf(tex)
}

const (
	signLocal   = 101
	vendorLocal = 102
)

func TestATextureThatAlreadyMatchesPassesAtOnce(t *testing.T) {
	f := newGrid(t)
	signAt(f, idTexA)
	t0 := time.Now()
	res := play(t, f, hdr+fmt.Sprintf("say \"go\" on 0\nexpect texture sign face 0 is %s within 2s\n", idTexA))
	wantExit(t, res, 0)
	if took := time.Since(t0); took > time.Second {
		t.Errorf("took %s, want at once", took)
	}
	mustHave(t, res, "texture sign face 0 "+idTexA.String(), "slate: pass step 1")
}

func TestBecomesNeedsAChangeAndAnAlreadyTrueValueMustLeaveAndComeBack(t *testing.T) {
	src := hdr + fmt.Sprintf("say \"go\" on 0\nexpect texture sign face 0 becomes %s within 400ms\n", idTexA)

	// Already A, and nothing happens: a reading that equals A is not a
	// transition to it.
	f := newGrid(t)
	signAt(f, idTexA)
	res := play(t, f, src)
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched texture sign face 0 becomes "+idTexA.String())

	// A, then B, then A again: it left and came back.
	f = newGrid(t)
	signAt(f, idTexA)
	f.whenSaid("go", func() {
		f.changeAfter(t, 40*time.Millisecond, signLocal, withTexture(0, idTexB))
		f.changeAfter(t, 120*time.Millisecond, signLocal, withTexture(0, idTexA))
	})
	res = play(t, f, src)
	wantExit(t, res, 0)
	mustHave(t, res, "texture sign face 0 "+idTexB.String(), "texture sign face 0 "+idTexA.String())

	// B, then A: a change to the value from another.
	f = newGrid(t)
	signAt(f, idTexB)
	f.whenSaid("go", func() { f.changeAfter(t, 50*time.Millisecond, signLocal, withTexture(0, idTexA)) })
	wantExit(t, play(t, f, src), 0)
}

func TestChangesIsJudgedByTheTolerance(t *testing.T) {
	src := hdr + "say \"go\" on 0\nexpect offset sign face 0 changes within 300ms\n"
	setup := func(f *fakeGrid) {
		f.objects[0].TextureEntry = func() []byte {
			faces := sl.PlainFaces(6)
			faces[0].OffsetS = 16384
			te, _ := sl.EncodeTextureEntry(faces)
			return te
		}()
	}

	// One step of the setter's quantisation is less than 2/32767.
	f := newGrid(t)
	setup(f)
	f.whenSaid("go", func() {
		f.changeAfter(t, 30*time.Millisecond, signLocal, withFace(0, func(f *sl.Face) { f.OffsetS++ }))
	})
	res := play(t, f, src)
	wantExit(t, res, 1)

	// Three steps are more.
	f = newGrid(t)
	setup(f)
	f.whenSaid("go", func() {
		f.changeAfter(t, 30*time.Millisecond, signLocal, withFace(0, func(f *sl.Face) { f.OffsetS += 3 }))
	})
	wantExit(t, play(t, f, src), 0)
}

func TestOffsetAndRepeatsAreComparedWithTheirTolerances(t *testing.T) {
	f := newGrid(t)
	f.objects[0].TextureEntry = func() []byte {
		faces := sl.PlainFaces(6)
		faces[0].OffsetS, faces[0].OffsetT = 8192, 0 // 0.25002 and 0
		faces[0].ScaleS, faces[0].ScaleT = 2, -1
		te, _ := sl.EncodeTextureEntry(faces)
		return te
	}()
	res := play(t, f, hdr+`say "go" on 0
expect offset sign face 0 is 0.25 0 within 300ms
expect repeats sign face 0 is 2 -1 within 300ms
`)
	wantExit(t, res, 0)
	res = play(t, f, hdr+"say \"go\" on 0\nexpect repeats sign face 0 is 2.01 -1 within 150ms\n")
	wantExit(t, res, 1)
}

func TestRotationIsComparedInTurnsAsAFloat(t *testing.T) {
	setup := func(rot int16) *fakeGrid {
		f := newGrid(t)
		f.objects[0].TextureEntry = func() []byte {
			faces := sl.PlainFaces(6)
			faces[0].Rotation = rot
			te, _ := sl.EncodeTextureEntry(faces)
			return te
		}()
		return f
	}
	// 8192 is a quarter turn. An integer division would say 0.
	wantExit(t, play(t, setup(8192), hdr+"say \"go\" on 0\nexpect rotation sign face 0 is 0.25 within 300ms\n"), 0)
	wantExit(t, play(t, setup(8192), hdr+"say \"go\" on 0\nexpect rotation sign face 0 is 0 within 150ms\n"), 1)
	wantExit(t, play(t, setup(-16384), hdr+"say \"go\" on 0\nexpect rotation sign face 0 is -0.5 within 300ms\n"), 0)
	// Within two parts in 32768 of a turn, and no more.
	wantExit(t, play(t, setup(8194), hdr+"say \"go\" on 0\nexpect rotation sign face 0 is 0.25 within 300ms\n"), 0)
	wantExit(t, play(t, setup(8195), hdr+"say \"go\" on 0\nexpect rotation sign face 0 is 0.25 within 150ms\n"), 1)
}

func TestOriginalIsReadAfterBeforeEach(t *testing.T) {
	src := fmt.Sprintf(`slate 1
object sign is "Example Sign"
before each {
  say "reset" on 0
}
test "toggle" {
  say "go" on 0
  expect texture sign face 0 changes within 400ms
  say "back" on 0
  expect texture sign face 0 becomes original within 400ms
}
`)
	run := func(back msg.UUID) *Result {
		f := newGrid(t)
		signAt(f, idTexC) // the state before the reset
		f.replyTo(func(m msg.Message) {
			text, _, _ := says(m)
			switch text {
			case "reset":
				f.change(signLocal, withTexture(0, idTexA))
			case "go":
				f.change(signLocal, withTexture(0, idTexB))
			case "back":
				f.change(signLocal, withTexture(0, back))
			}
		})
		return play(t, f, src)
	}
	// Original is what the reset left, not what the test began with.
	wantExit(t, run(idTexA), 0)
	res := run(idTexC)
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched texture sign face 0 becomes original")
}

func TestABaselineTakenAfterTheArmPointSaysSo(t *testing.T) {
	f := newGrid(t)
	f.objects[0].TextureEntry = nil // nothing has described the faces
	f.changeOnRequest(signLocal, withTexture(0, idTexA))
	f.whenSaid("go", func() { f.changeAfter(t, 120*time.Millisecond, signLocal, withTexture(0, idTexB)) })
	// The first reading after the arm point is the baseline: A, then B.
	res := play(t, f, hdr+fmt.Sprintf("say \"go\" on 0\nexpect texture sign face 0 becomes %s within 800ms\n", idTexB))
	wantExit(t, res, 0)
	mustHave(t, res, "baseline for sign face 0 taken after the arm point")

	// `is` needs no baseline and says nothing of one.
	f = newGrid(t)
	f.objects[0].TextureEntry = nil
	f.changeOnRequest(signLocal, withTexture(0, idTexA))
	res = play(t, f, hdr+fmt.Sprintf("say \"go\" on 0\nexpect texture sign face 0 is %s within 800ms\n", idTexA))
	wantExit(t, res, 0)
	mustNotHave(t, res, "baseline for")
}

func TestAStaleReadingDoesNotFailEarlyAndTheRegionIsAskedAgainOnACadence(t *testing.T) {
	cfg := testCfg()
	cfg.redescribe = 80 * time.Millisecond
	src := hdr + fmt.Sprintf("say \"go\" on 0\nexpect texture sign face 0 becomes %s within 1s\n", idTexB)

	// The entry stays at A through two requests and shows B on the third.
	f := newGrid(t)
	signAt(f, idTexA)
	f.changeOnRequest(signLocal, func(*sl.Seen) {})
	f.changeOnRequest(signLocal, func(*sl.Seen) {})
	f.changeOnRequest(signLocal, withTexture(0, idTexB))
	t0 := time.Now()
	res := playWith(t, f, src, Options{}, cfg)
	wantExit(t, res, 0)
	if took := time.Since(t0); took < 150*time.Millisecond {
		t.Errorf("passed after %s: a stale entry passed early", took)
	}
	reqs := f.requests(signLocal)
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want 3", len(reqs))
	}
	for i := 1; i < len(reqs); i++ {
		if gap := reqs[i].Sub(reqs[i-1]); gap < 70*time.Millisecond {
			t.Errorf("requests %d and %d are %s apart, want at least 80ms", i-1, i, gap)
		}
	}
	// What was sent is the cache-miss request for the prim, as a viewer's.
	for _, m := range sentOf[*msg.RequestMultipleObjects](f) {
		if len(m.ObjectData) != 1 || m.ObjectData[0].CacheMissType != 0 || m.ObjectData[0].ID != signLocal {
			t.Errorf("request = %+v", m.ObjectData)
		}
		if m.AgentData.AgentID != testMe || m.AgentData.SessionID != testSession {
			t.Errorf("request agent block = %+v", m.AgentData)
		}
	}

	// Never fresh: the step fails at its own deadline and not before.
	f = newGrid(t)
	signAt(f, idTexA)
	t0 = time.Now()
	res = playWith(t, f, hdr+fmt.Sprintf("say \"go\" on 0\nexpect texture sign face 0 becomes %s within 300ms\n", idTexB), Options{}, cfg)
	wantExit(t, res, 1)
	if took := time.Since(t0); took < 300*time.Millisecond {
		t.Errorf("failed after %s, before the deadline", took)
	}
}

func TestANegativeStateExpectation(t *testing.T) {
	// A negative `changes` with a reading and no change passes at the end
	// of its window.
	f := newGrid(t)
	signAt(f, idTexA)
	t0 := time.Now()
	res := play(t, f, hdr+"say \"go\" on 0\nexpect no texture sign face 0 changes within 150ms\n")
	wantExit(t, res, 0)
	if took := time.Since(t0); took < 150*time.Millisecond {
		t.Errorf("passed after %s, before its window was over", took)
	}

	// With no reading at all it fails, at the deadline.
	f = newGrid(t)
	f.objects[0].TextureEntry = nil
	cfg := testCfg()
	cfg.redescribe = time.Hour // nothing describes the prim
	t0 = time.Now()
	res = playWith(t, f, hdr+"say \"go\" on 0\nexpect no texture sign face 0 changes within 200ms\n", Options{}, cfg)
	wantExit(t, res, 1)
	if took := time.Since(t0); took < 200*time.Millisecond {
		t.Errorf("failed after %s, before the deadline", took)
	}
	mustHave(t, res, "a negative needs one", "needs a real reading")

	// A reading that equals the forbidden value fails at once.
	f = newGrid(t)
	signAt(f, idTexA)
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withTexture(0, idTexB)) })
	t0 = time.Now()
	res = play(t, f, hdr+fmt.Sprintf("say \"go\" on 0\nexpect no texture sign face 0 is %s within 2s\n", idTexB))
	wantExit(t, res, 1)
	if took := time.Since(t0); took > time.Second {
		t.Errorf("a forbidden reading failed after %s, not at once", took)
	}
	mustHave(t, res, "forbidden no texture sign face 0 is "+idTexB.String())

	// `becomes` forbids a transition to the value, not a standing one.
	f = newGrid(t)
	signAt(f, idTexA)
	wantExit(t, play(t, f, hdr+fmt.Sprintf("say \"go\" on 0\nexpect no texture sign face 0 becomes %s within 120ms\n", idTexA)), 0)
}

func TestAClickByteIsReadOnlyWhenTheStoreKnowsIt(t *testing.T) {
	f := newGrid(t)
	described(f.objects[0], 1, true)
	res := play(t, f, hdr+"say \"go\" on 0\nexpect click sign is sit within 300ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, "click sign 1")
	// A known zero is touch.
	f = newGrid(t)
	described(f.objects[0], 0, true)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect click sign is touch within 300ms\n"), 0)
	// A byte that is not the one is printed as a number.
	f = newGrid(t)
	described(f.objects[0], 3, true)
	res = play(t, f, hdr+"say \"go\" on 0\nexpect click sign is sit within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "click sign 3")
}

func TestAClickByteTheStoreDoesNotKnowAtSetupIsExitThree(t *testing.T) {
	f := newGrid(t)
	res, err := tryPlay(t, f, hdr+"say \"go\" on 0\nexpect click sign is sit within 300ms\n", Options{}, testCfg())
	if err == nil {
		t.Error("a setup failure returned no error")
	}
	wantExit(t, res, 3)
	mustHave(t, res, `slate: setup: click action was not on any update of "Example Sign"; this slate requires the slgod that stores click and click_known`)
	if len(res.Tests) != 0 || len(f.said()) != 0 {
		t.Errorf("tests %+v said %q after a setup failure", res.Tests, f.said())
	}
	if len(f.requests(signLocal)) < 2 {
		t.Errorf("%d describes in the wait, want a cadence of them", len(f.requests(signLocal)))
	}

	// The describe answered: the byte is known and the run goes on. A zero
	// byte the grid said is touch.
	f = newGrid(t)
	f.changeOnRequest(signLocal, withClick(0))
	res = play(t, f, hdr+"say \"go\" on 0\nexpect click sign is touch within 300ms\n")
	wantExit(t, res, 0)
	if len(f.requests(signLocal)) != 1 {
		t.Errorf("%d describes, want one", len(f.requests(signLocal)))
	}

	// A binding no click expectation names is not described.
	f = newGrid(t)
	res = play(t, f, hdr+"say \"go\" on 0\n")
	wantExit(t, res, 0)
	if n := len(f.requests(signLocal)); n != 0 {
		t.Errorf("%d describes for a prim nothing reads the click of", n)
	}
}

// balloonAt is a new root the vendor rezzed: named, a prim, owned by the
// vendor's owner and not described for its click.
func balloonAt(id msg.UUID, local uint32, x float32) *sl.Seen {
	return at(prim(id, local, "Example Balloon", idStranger), x, 133, 25)
}

const rezBalloon = `say "go" on 0
expect rez name "Example Balloon" from vendor as balloon within 1s
`

func TestAnAsBoundClickIsDescribedAndHoldsTheRezStep(t *testing.T) {
	src := hdr + rezBalloon + "then\nexpect click balloon is touch within 300ms\n"
	f := newGrid(t)
	f.whenSaid("go", func() { f.appear(balloonAt(idRootA, 201, 130)) })
	// The byte is said only 200 ms after the rez step is otherwise ready.
	f.changeOnRequest(201, func(*sl.Seen) {})
	f.changeAfter(t, 250*time.Millisecond, 201, withClick(0))
	cfg := testCfg()
	cfg.click = 2 * time.Second
	t0 := time.Now()
	res := playWith(t, f, src, Options{}, cfg)
	wantExit(t, res, 0)
	if took := time.Since(t0); took < 230*time.Millisecond {
		t.Errorf("passed after %s: the rez step did not wait for the byte", took)
	}
	mustHave(t, res, "slate: pass step 1", "slate: pass step 2", "click balloon 0")
	if len(f.requests(201)) < 1 {
		t.Error("the bound prim was not described")
	}

	// A name no click expectation uses is not described.
	f = newGrid(t)
	f.whenSaid("go", func() { f.appear(balloonAt(idRootA, 201, 130)) })
	res = play(t, f, hdr+rezBalloon)
	wantExit(t, res, 0)
	if n := len(f.requests(201)); n != 0 {
		t.Errorf("%d describes of a prim nothing reads the click of", n)
	}
}

func TestAnAsBoundClickThatStaysUnknownIsExitThreeAndStopsTheRun(t *testing.T) {
	src := fmt.Sprintf(`slate 1
object sign is "Example Sign"
object vendor is "Example Tip Jar"
test "one" {
  say "go" on 0
  expect rez name "Example Balloon" from vendor as balloon within 1s
  then
  expect click balloon is touch within 300ms
}
test "two" {
  say "again" on 0
}
`)
	f := newGrid(t)
	f.whenSaid("go", func() { f.appear(balloonAt(idRootA, 201, 130)) })
	res, err := tryPlay(t, f, src, Options{}, testCfg())
	if err == nil {
		t.Log("the run stopped without an error; the transcript says why")
	}
	wantExit(t, res, 3)
	mustHave(t, res, `slate: setup: click action was not on any update of "Example Balloon"`)
	// The rez step is still open: it did not pass, no block was printed,
	// and nothing after it ran.
	mustNotHave(t, res, "slate: pass step 1")
	mustNotHave(t, res, "slate: fail ")
	mustNotHave(t, res, `slate: test "two"`)
	if got := f.said(); len(got) != 1 {
		t.Errorf("said %q", got)
	}
	if len(res.Tests) != 1 || res.Tests[0].Passed || res.Tests[0].Exit != 3 {
		t.Errorf("tests = %+v", res.Tests)
	}
}
