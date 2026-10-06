package slate

// expect sound: the sounds objects play, read from what the region says
// of them, as a sound heard and as a loop's state.
// Why: doc/slate-runner.md#sounds

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids, made with tools/new-id.  The built-ins are Linden's, in
// tools/known-uuids: UISndClick and UISndAlert.
var (
	idSndBell  = msg.MustParseUUID("c9e37e57-7e57-c0de-45f9-6f937891c0cf")
	idSndHum   = msg.MustParseUUID("cc407e57-7e57-c0de-2da1-b2b362de118c")
	idSndOther = msg.MustParseUUID("ee7d7e57-7e57-c0de-e98c-771b165a63d7")
	idSndClick = msg.MustParseUUID("4c8c3c77-de8d-bde2-b9b8-32635e0fd4a6")
	idSndAlert = msg.MustParseUUID("ed124764-705d-d497-167a-182cd9fa2e6c")
)

func triggerMsg(sound, object msg.UUID, gain float32) *msg.SoundTrigger {
	m := &msg.SoundTrigger{}
	m.SoundData = msg.SoundTrigger_SoundData{SoundID: sound, OwnerID: idStranger, ObjectID: object, Gain: gain}
	return m
}

func attachedMsg(sound, object msg.UUID, gain float32, flags uint8) *msg.AttachedSound {
	m := &msg.AttachedSound{}
	m.DataBlock = msg.AttachedSound_DataBlock{SoundID: sound, ObjectID: object, OwnerID: idStranger, Gain: gain, Flags: flags}
	return m
}

func TestSoundParsesAndChecks(t *testing.T) {
	const h = "slate 1\nobject a is \"A\"\n"
	const w = "c9e37e57-7e57-c0de-45f9-6f937891c0cf"
	mustCheck(t, h+"expect sound "+w+" within 2s\n")
	mustCheck(t, h+"expect sound "+w+" from a within 2s\n")
	mustCheck(t, h+"expect sound "+w+" from a gain 0.5 within 2s\n")
	mustCheck(t, h+"expect sound "+w+" gain 1 near 0.1 within 2s\n")
	mustCheck(t, h+"expect sound "+w+" gain 0.5 near 20 percent within 2s\n")
	mustCheck(t, h+"expect no sound "+w+" from a within 1s\n")
	mustCheck(t, h+"expect sound "+w+" is looping\n")
	mustCheck(t, h+"expect sound "+w+" is stopped from a within 2s\n")
	mustCheck(t, h+"expect sound "+w+" becomes looping from a within 2s\n")
	mustCheck(t, h+"expect sound "+w+" becomes stopped\n")
	mustCheck(t, h+"expect sound "+w+" changes from a\n")
	mustCheck(t, h+"expect no sound "+w+" becomes looping from a within 1s\n")
	// Not reserved.
	mustCheck(t, "slate 1\nobject sound is \"A\"\nexpect sound "+w+" from sound within 1s\n")
	mustCheck(t, "slate 1\nobject gain is \"A\"\nexpect sound "+w+" from gain gain 1 within 1s\n")
	parseErr(t, h+"expect sound example_bell within 1s\n", "expected a sound's asset UUID")
	parseErr(t, h+"expect sound "+w+" is original\n", "has no original")
	parseErr(t, h+"expect sound "+w+" is any\n", "expected looping or stopped")
	parseErr(t, h+"expect sound "+w+" is on\n", "expected looping or stopped")
	parseErr(t, h+"expect sound "+w+" is $x\n", "expected looping or stopped")
	parseErr(t, h+"expect sound "+w+" changes looping\n", "found looping")
	parseErr(t, h+"expect sound "+w+" gain\n", "expected a number")
	checkErr(t, h+"expect sound "+w+" from b within 1s\n", "b is not an object")
	checkErr(t, h+"expect sound 00000000-0000-0000-0000-000000000000 within 1s\n", "names no sound")
	checkErr(t, h+"expect sound "+w+" gain 1.5 within 1s\n", "gain is from 0 to 1")
	checkErr(t, h+"expect sound "+w+" gain -1 within 1s\n", "gain is from 0 to 1")
	checkErr(t, h+"expect sound "+w+" near 1 within 1s\n", "sound takes no near")
	checkErr(t, h+"expect sound "+w+" is looping near 1 within 1s\n", "sound takes no near")
	checkErr(t, h+"expect sound "+w+" within 1s as $x\n", "no reading to bind")
}

// TestAGainIsNotWrittenBesideALoopsState: a gain belongs to a play.
func TestAGainIsNotWrittenBesideALoopsState(t *testing.T) {
	const h = "slate 1\nobject a is \"A\"\n"
	parseErr(t, h+"expect sound c9e37e57-7e57-c0de-45f9-6f937891c0cf is looping gain 1\n", "found gain")
}

// TestASoundTheObjectTriggersIsHeard: a one-shot at a place, from the
// object that played it, and the transcript says it with its gain.
func TestASoundTheObjectTriggersIsHeard(t *testing.T) {
	f := newGrid(t)
	f.on("go", triggerMsg(idSndBell, idSign, 0.5))
	res := play(t, f, hdr+"say \"go\" on 0\n"+
		"expect sound "+idSndBell.String()+" from sign within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "sound "+idSndBell.String()+" triggered from sign at gain 0.5", "slate: pass step 1")
}

// TestAnAttachedSoundIsHeardToo: llPlaySound is an attached sound, and a
// loop is one that plays.
func TestAnAttachedSoundIsHeardToo(t *testing.T) {
	f := newGrid(t)
	f.on("go", attachedMsg(idSndClick, idSign, 1, 0))
	res := play(t, f, hdr+"say \"go\" on 0\n"+
		"expect sound "+idSndClick.String()+" from sign gain 1 within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "sound "+idSndClick.String()+" played from sign at gain 1")

	f2 := newGrid(t)
	f2.on("go", attachedMsg(idSndHum, idSign, 0.25, sl.SoundFlagLoop))
	res = play(t, f2, hdr+"say \"go\" on 0\n"+
		"expect sound "+idSndHum.String()+" from sign gain 0.25 within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "sound "+idSndHum.String()+" looping from sign at gain 0.25")
}

// TestASoundWithoutFromComesFromAnything.
func TestASoundWithoutFromComesFromAnything(t *testing.T) {
	f := newGrid(t)
	f.on("go", triggerMsg(idSndBell, idStranger, 1))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect sound "+idSndBell.String()+" within 1s\n"), 0)
}

// TestAnotherObjectsSoundIsNotThisOnes: from names an object, and a sound
// of the id from another is not it; the failure says what was heard.
func TestAnotherObjectsSoundIsNotThisOnes(t *testing.T) {
	f := newGrid(t)
	f.on("go", triggerMsg(idSndBell, idStranger, 1))
	res := play(t, f, hdr+"say \"go\" on 0\nexpect sound "+idSndBell.String()+" from sign within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched sound "+idSndBell.String()+" from sign within 300ms", "heard from another object at gain 1")

	// A sound of another id from the sign is not it either.
	f2 := newGrid(t)
	f2.on("go", triggerMsg(idSndOther, idSign, 1))
	res = play(t, f2, hdr+"say \"go\" on 0\nexpect sound "+idSndBell.String()+" from sign within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "no sound of that id was heard")
}

// TestAGainIsMatchedWithinATolerance.
func TestAGainIsMatchedWithinATolerance(t *testing.T) {
	b := idSndBell.String()
	for _, c := range []struct {
		near string
		want int
	}{
		{"gain 0.5", 0},
		{"gain 0.75", 1},
		{"gain 0.75 near 0.3", 0},
		{"gain 0.6 near 25 percent", 0},
		{"gain 0.9 near 25 percent", 1},
	} {
		f := newGrid(t)
		f.on("go", triggerMsg(idSndBell, idSign, 0.5))
		res := play(t, f, hdr+"say \"go\" on 0\nexpect sound "+b+" "+c.near+" within 300ms\n")
		wantExit(t, res, c.want)
		if c.want == 1 {
			mustHave(t, res, "heard at gain 0.5")
		}
	}
}

// TestANegativeSoundHoldsOverItsWindow.
func TestANegativeSoundHoldsOverItsWindow(t *testing.T) {
	b := idSndBell.String()
	f := newGrid(t)
	wantExit(t, play(t, f, hdr+"expect no sound "+b+" from sign within 200ms\n"), 0)

	f2 := newGrid(t)
	f2.on("go", triggerMsg(idSndBell, idSign, 1))
	wantExit(t, play(t, f2, hdr+"say \"go\" on 0\nexpect no sound "+b+" from sign within 400ms\n"), 1)

	// Another object's, or another id, does not break it.
	f3 := newGrid(t)
	f3.on("go", triggerMsg(idSndBell, idStranger, 1), triggerMsg(idSndOther, idSign, 1))
	wantExit(t, play(t, f3, hdr+"say \"go\" on 0\nexpect no sound "+b+" from sign within 300ms\n"), 0)
}

// TestASoundBeforeTheStepIsNotThisStepsSound: an expectation sees what is
// heard after its arm point.
func TestASoundBeforeTheStepIsNotThisStepsSound(t *testing.T) {
	f := newGrid(t)
	f.on("go", triggerMsg(idSndBell, idSign, 1))
	b := idSndBell.String()
	res := play(t, f, hdr+"say \"go\" on 0\nexpect sound "+b+" within 1s\n"+
		"then expect sound "+b+" within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "slate: pass step 1")
}

// TestALoopStartsAndStops: the object loops the sound when the tester
// says go, and stops it, with a null sound, when the tester says stop.
func TestALoopStartsAndStops(t *testing.T) {
	f := newGrid(t)
	h := idSndHum.String()
	f.replyTo(func(m msg.Message) {
		text, _, ok := says(m)
		switch {
		case ok && text == "go":
			f.relay(attachedMsg(idSndHum, idSign, 1, sl.SoundFlagLoop))
		case ok && text == "stop":
			f.relay(attachedMsg(msg.UUID{}, idSign, 0, sl.SoundFlagStop))
		}
	})
	res := play(t, f, hdr+"expect sound "+h+" is stopped from sign within 1s\n"+
		"say \"go\" on 0\n"+
		"expect sound "+h+" becomes looping from sign within 1s\n"+
		"say \"stop\" on 0\n"+
		"expect sound "+h+" becomes stopped from sign within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res,
		"sound "+h+" looping from sign at gain 1",
		"sound "+h+" stopped from sign",
		"slate: pass step 3")
}

// TestALoopIsLoopingUntilItStops: is looping holds from the start, and a
// loop at gain 0 is still one.
func TestALoopIsLoopingUntilItStops(t *testing.T) {
	f := newGrid(t)
	h := idSndHum.String()
	f.replyTo(func(m msg.Message) {
		text, _, ok := says(m)
		switch {
		case ok && text == "go":
			f.relay(attachedMsg(idSndHum, idSign, 1, sl.SoundFlagLoop))
		case ok && text == "quiet":
			g := &msg.AttachedSoundGainChange{}
			g.DataBlock.ObjectID = idSign
			f.relay(g)
		}
	})
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect sound "+h+" is looping from sign within 1s\n"+
		"say \"quiet\" on 0\nexpect sound "+h+" is looping from sign within 1s\n"), 0)

	f2 := newGrid(t)
	wantExit(t, play(t, f2, hdr+"expect sound "+h+" is looping from sign within 200ms\n"), 1)
}

// TestALoopFromAnotherObjectIsNotThisOnes.
func TestALoopFromAnotherObjectIsNotThisOnes(t *testing.T) {
	f := newGrid(t)
	h := idSndHum.String()
	f.on("go", attachedMsg(idSndHum, idStranger, 1, sl.SoundFlagLoop))
	// Without from, whoever loops it.
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect sound "+h+" becomes looping within 1s\n"), 0)
	f = newGrid(t)
	f.on("go", attachedMsg(idSndHum, idStranger, 1, sl.SoundFlagLoop))
	res := play(t, f, hdr+"say \"go\" on 0\nexpect sound "+h+" becomes looping from sign within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "last reading: sound "+h+" not looping from sign")
}

// TestAOneShotIsNoLoop.
func TestAOneShotIsNoLoop(t *testing.T) {
	f := newGrid(t)
	f.on("go", attachedMsg(idSndHum, idSign, 1, 0))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect sound "+idSndHum.String()+" becomes looping within 300ms\n"), 1)
}

func TestANegativeLoopHolds(t *testing.T) {
	f := newGrid(t)
	h := idSndHum.String()
	wantExit(t, play(t, f, hdr+"expect no sound "+h+" becomes looping from sign within 200ms\n"), 0)
	f = newGrid(t)
	f.on("go", attachedMsg(idSndHum, idSign, 1, sl.SoundFlagLoop))
	res := play(t, f, hdr+"say \"go\" on 0\nexpect no sound "+h+" becomes looping from sign within 400ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "sound "+h+" started looping from sign")
}

// TestAChangeIsAStartOrAStop.
func TestAChangeIsAStartOrAStop(t *testing.T) {
	f := newGrid(t)
	f.on("go", attachedMsg(idSndHum, idSign, 1, sl.SoundFlagLoop))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect sound "+idSndHum.String()+" changes from sign within 1s\n"), 0)
}

// TestTheTranscriptNamesWhatABoundObjectPlaysWhetherOrNotExpected: it is
// how a test author learns a sound's id.  What no bound object played and
// no expectation names is not said.
func TestTheTranscriptNamesWhatABoundObjectPlaysWhetherOrNotExpected(t *testing.T) {
	f := newGrid(t)
	f.on("go", triggerMsg(idSndOther, idSign, 1), triggerMsg(idSndAlert, idStranger, 1),
		attachedMsg(idSndHum, idSign, 1, sl.SoundFlagLoop))
	res := play(t, f, hdr+"say \"go\" on 0\nexpect sound "+idSndBell.String()+" from sign within 250ms\n")
	wantExit(t, res, 1)
	mustHave(t, res,
		"sound "+idSndOther.String()+" triggered from sign at gain 1",
		"sound "+idSndHum.String()+" looping from sign at gain 1")
	if strings.Contains(res.Transcript, idSndAlert.String()) {
		t.Errorf("a stranger's sound that nothing names was said:\n%s", res.Transcript)
	}
}

// TestASoundAnExpectationNamesIsSaidWhoeverPlaysIt.
func TestASoundAnExpectationNamesIsSaidWhoeverPlaysIt(t *testing.T) {
	f := newGrid(t)
	f.on("go", triggerMsg(idSndBell, idStranger, 1))
	res := play(t, f, hdr+"say \"go\" on 0\nexpect sound "+idSndBell.String()+" within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "sound "+idSndBell.String()+" triggered at gain 1")
}

// TestALoopTakenOverByAnotherSoundStops.
func TestALoopTakenOverByAnotherSoundStops(t *testing.T) {
	f := newGrid(t)
	f.replyTo(func(m msg.Message) {
		text, _, ok := says(m)
		switch {
		case ok && text == "go":
			f.relay(attachedMsg(idSndHum, idSign, 1, sl.SoundFlagLoop))
		case ok && text == "bell":
			f.relay(attachedMsg(idSndBell, idSign, 1, 0))
		}
	})
	h := idSndHum.String()
	res := play(t, f, hdr+"say \"go\" on 0\nexpect sound "+h+" becomes looping from sign within 1s\n"+
		"say \"bell\" on 0\nexpect sound "+h+" becomes stopped from sign within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "sound "+h+" stopped from sign")
}

// TestALoopAlreadyPlayingIsTheBaseline: a loop the session knew of before
// the test, from the full update of the prim, is the first reading, so
// `is looping` holds and `becomes looping` needs it to stop first.
func TestALoopAlreadyPlayingIsTheBaseline(t *testing.T) {
	h := idSndHum.String()
	for src, want := range map[string]int{
		hdr + "expect sound " + h + " is looping from sign within 1s\n":         0,
		hdr + "expect sound " + h + " becomes looping from sign within 200ms\n": 1,
	} {
		f := newGrid(t)
		w := f.session(t)
		u := &msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{
			FullID: idSign, ID: 101, Sound: idSndHum, OwnerID: testMe, Gain: 1, Flags: sl.SoundFlagLoop,
		}}}
		if err := f.relay(u); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		res, err := run(ctx, w, mustCheck(t, src), Options{}, testCfg())
		cancel()
		if err != nil {
			t.Fatalf("Run: %v\n%s", err, res.Transcript)
		}
		wantExit(t, res, want)
		if want == 0 {
			mustHave(t, res, "sound "+h+" looping from sign at gain 1")
		}
	}
}

// TestARunThatNamesNoSoundSaysNone: nothing is asked for, and nothing is
// said.
func TestARunThatNamesNoSoundSaysNone(t *testing.T) {
	f := newGrid(t)
	f.on("go", triggerMsg(idSndBell, idSign, 1))
	res := play(t, f, hdr+"say \"go\" on 0\nexpect no say \"x\" on public from object sign within 200ms\n")
	wantExit(t, res, 0)
	if strings.Contains(res.Transcript, idSndBell.String()) {
		t.Errorf("a run with no sound expectation said a sound:\n%s", res.Transcript)
	}
}
