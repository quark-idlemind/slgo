package slate

// expect animation: what the tester plays, read from AvatarAnimation and
// judged on or off over the step's window.
// Why: doc/slate-runner.md#animations

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids, made with tools/new-id.
var (
	idAnimExample = msg.MustParseUUID("256f7e57-7e57-c0de-c271-539bd101a881")
	idAnimBow     = msg.MustParseUUID("56a07e57-7e57-c0de-11b1-4c0283dab1c3")
	idAnimSpin    = msg.MustParseUUID("94d47e57-7e57-c0de-972c-60869eeffeaf")
)

// animSim is the simulator saying what the tester plays: the list it
// holds is sent about every few milliseconds, as the real one is resent
// every few seconds, and set changes it.
type animSim struct {
	mu   sync.Mutex
	list []sl.PlayingAnimation
	stop chan struct{}
	done chan struct{}
}

// animating starts the simulator's resends of the tester's list. A
// message is sent for this avatar; the grid closes it before it closes.
func (f *fakeGrid) animating(initial ...sl.PlayingAnimation) *animSim {
	a := &animSim{list: initial, stop: make(chan struct{}), done: make(chan struct{})}
	f.beforeClose = append(f.beforeClose, func() { close(a.stop); <-a.done })
	go func() {
		defer close(a.done)
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-a.stop:
				return
			case <-tick.C:
			}
			a.mu.Lock()
			m := &msg.AvatarAnimation{}
			m.Sender.ID = testMe
			for i, p := range a.list {
				m.AnimationList = append(m.AnimationList, msg.AvatarAnimation_AnimationList{AnimID: p.ID, AnimSequenceID: int32(i + 1)})
				m.AnimationSourceList = append(m.AnimationSourceList, msg.AvatarAnimation_AnimationSourceList{ObjectID: p.Source})
			}
			a.mu.Unlock()
			if err := f.relayUntil(m, a.stop); err != nil {
				return
			}
		}
	}()
	return a
}

func (a *animSim) set(list ...sl.PlayingAnimation) {
	a.mu.Lock()
	a.list = list
	a.mu.Unlock()
}

// after sets the list a while from now.
func (a *animSim) after(d time.Duration, list ...sl.PlayingAnimation) {
	time.AfterFunc(d, func() { a.set(list...) })
}

// onSay sets the list when the tester says trigger.
func (a *animSim) onSay(f *fakeGrid, trigger string, list ...sl.PlayingAnimation) {
	f.replyTo(func(m msg.Message) {
		if text, _, ok := says(m); ok && text == trigger {
			a.set(list...)
		}
	})
}

func playing(id, source msg.UUID) sl.PlayingAnimation {
	return sl.PlayingAnimation{ID: id, Source: source}
}

func TestAnimationParsesAndChecks(t *testing.T) {
	const h = "slate 1\nobject a is \"A\"\n"
	const w = "256f7e57-7e57-c0de-c271-539bd101a881"
	mustCheck(t, h+"expect animation "+w+" is on\n")
	mustCheck(t, h+"expect animation "+w+" is off from a within 2s\n")
	mustCheck(t, h+"expect animation "+w+" becomes on from a within 2s\n")
	mustCheck(t, h+"expect animation "+w+" becomes off\n")
	mustCheck(t, h+"expect animation "+w+" changes from a\n")
	mustCheck(t, h+"expect no animation "+w+" becomes on from a within 1s\n")
	// Not reserved.
	mustCheck(t, "slate 1\nobject animation is \"A\"\nexpect animation "+w+" is on from animation\n")
	parseErr(t, h+"expect animation example_anim is on\n", "expected an animation's asset UUID")
	parseErr(t, h+"expect animation "+w+" is original\n", "has no original")
	parseErr(t, h+"expect animation "+w+" is any as $x\n", "expected on or off")
	parseErr(t, h+"expect animation "+w+" is $x\n", "expected on or off")
	parseErr(t, h+"expect animation "+w+" is 1\n", "expected on or off")
	parseErr(t, h+"expect animation "+w+" from a\n", "expected is, becomes, or changes")
	checkErr(t, h+"expect animation "+w+" is on from b\n", "b is not an object")
	checkErr(t, h+"expect animation 00000000-0000-0000-0000-000000000000 is on\n", "names no animation")
	checkErr(t, h+"expect animation "+w+" is on near 1\n", "animation takes no near")
	checkErr(t, h+"expect animation "+w+" becomes on within 1s as $x\n", "no reading to bind")
	checkErr(t, h+"expect no animation "+w+" becomes on within 1s as $x\n", "a negative expectation matches nothing to bind")
}

// TestAnAnimationAnObjectStartsStartsAndThenStops: the object's script
// starts the animation when the tester says go and the next list names
// the object as its source; a later list without it is the stop.
func TestAnAnimationAnObjectStartsStartsAndThenStops(t *testing.T) {
	f := newGrid(t)
	sim := f.animating()
	sim.onSay(f, "go", playing(idAnimExample, idSign))
	src := hdr + "say \"go\" on 0\n" +
		"expect animation " + idAnimExample.String() + " becomes on from sign within 1s\n" +
		"then expect animation " + idAnimExample.String() + " is on within 1s\n"
	res := play(t, f, src)
	wantExit(t, res, 0)
	mustHave(t, res,
		"animation "+idAnimExample.String()+" started from sign",
		"slate: pass step 1")

	// And it stops after a time: the list that follows lacks it.
	f2 := newGrid(t)
	sim2 := f2.animating(playing(idAnimExample, idSign))
	sim2.after(80 * time.Millisecond)
	res = play(t, f2, hdr+"expect animation "+idAnimExample.String()+" is on from sign within 1s\n"+
		"then expect animation "+idAnimExample.String()+" becomes off from sign within 2s\n")
	wantExit(t, res, 0)
	mustHave(t, res,
		"animation "+idAnimExample.String()+" playing from sign",
		"animation "+idAnimExample.String()+" stopped from sign")
	_ = sim
}

// TestAnAnimationWithASourceIsSaidOnceWithIt: an expectation with no
// `from` and one with it watch the same animation; the transcript says
// it once, with the binding that started it, and not again without.
func TestAnAnimationWithASourceIsSaidOnceWithIt(t *testing.T) {
	f := newGrid(t)
	sim := f.animating()
	sim.onSay(f, "go", playing(idAnimExample, idSign))
	src := hdr + "say \"go\" on 0\n" +
		"expect animation " + idAnimExample.String() + " becomes on within 1s\n" +
		"expect animation " + idAnimExample.String() + " becomes on from sign within 1s\n"
	res := play(t, f, src)
	wantExit(t, res, 0)
	mustHave(t, res, "animation "+idAnimExample.String()+" started from sign")
	for _, l := range strings.Split(res.Transcript, "\n") {
		if strings.HasSuffix(l, "animation "+idAnimExample.String()+" started") {
			t.Errorf("the start was said again without its source: %q", l)
		}
	}
}

func TestAnimationIsOnAndOffAreReadings(t *testing.T) {
	f := newGrid(t)
	f.animating(playing(idAnimBow, msg.UUID{}))
	w, b := idAnimExample.String(), idAnimBow.String()
	// An animation playing with no source is playing, and is not from the sign.
	wantExit(t, play(t, f, hdr+"expect animation "+b+" is on within 1s\n"), 0)
	wantExit(t, play(t, f, hdr+"expect animation "+b+" is on from sign within 150ms\n"), 1)
	wantExit(t, play(t, f, hdr+"expect animation "+w+" is off within 1s\n"), 0)
	wantExit(t, play(t, f, hdr+"expect animation "+w+" is on within 150ms\n"), 1)
	// is on at the arm point passes; becomes on needs it to start.
	wantExit(t, play(t, f, hdr+"expect animation "+b+" becomes on within 200ms\n"), 1)
	wantExit(t, play(t, f, hdr+"expect animation "+b+" changes within 200ms\n"), 1)
}

// TestFromMatchesTheLinksetAndNotAnotherObject: the source is any prim
// of the binding's linkset, and an object that is not it does not count.
func TestFromMatchesTheLinksetAndNotAnotherObject(t *testing.T) {
	f, vendor, lid, _ := storeWorld(t)
	_ = vendor
	f.animating(playing(idAnimSpin, lid.ID))
	s := idAnimSpin.String()
	wantExit(t, play(t, f, hdr+"expect animation "+s+" is on from vendor within 1s\n"), 0)
	wantExit(t, play(t, f, hdr+"expect animation "+s+" is on from sign within 150ms\n"), 1)
	// Without from, whoever started it.
	wantExit(t, play(t, f, hdr+"expect animation "+s+" is on within 1s\n"), 0)

	f2 := newGrid(t)
	f2.animating(playing(idAnimSpin, idStranger))
	res := play(t, f2, hdr+"expect animation "+s+" is on from sign within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched animation "+s+" is on from sign within 150ms", "last reading: animation "+s+" from sign not playing")
}

func TestANegativeAnimationHoldsOverItsWindow(t *testing.T) {
	f := newGrid(t)
	sim := f.animating()
	s := idAnimExample.String()
	wantExit(t, play(t, f, hdr+"expect no animation "+s+" becomes on from sign within 150ms\n"), 0)

	sim.onSay(f, "go", playing(idAnimExample, idSign))
	res := play(t, f, hdr+"say \"go\" on 0\nexpect no animation "+s+" becomes on from sign within 400ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "animation "+s+" started")
}

// TestTheTranscriptNamesWhatABindingStartedWhetherOrNotExpected: it is
// how a test author learns an animation's id.
func TestTheTranscriptNamesWhatABindingStartedWhetherOrNotExpected(t *testing.T) {
	f := newGrid(t)
	sim := f.animating()
	sim.onSay(f, "go", playing(idAnimSpin, idSign))
	res := play(t, f, hdr+"say \"go\" on 0\nexpect animation "+idAnimExample.String()+" becomes on from sign within 250ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "animation "+idAnimSpin.String()+" started from sign")
}

// TestAChangeIsAnyDifferenceFromTheBaseline: starts and stops are both.
func TestAChangeIsAnyDifferenceFromTheBaseline(t *testing.T) {
	f := newGrid(t)
	sim := f.animating()
	sim.onSay(f, "go", playing(idAnimExample, idSign))
	s := idAnimExample.String()
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect animation "+s+" changes from sign within 1s\n"), 0)
}

func TestNoListHeardIsASetupFailureAndNoReadingIsNeverAPass(t *testing.T) {
	f := newGrid(t)
	cfg := testCfg()
	cfg.animations = 100 * time.Millisecond
	res, err := tryPlay(t, f, hdr+"expect animation "+idAnimExample.String()+" is off within 1s\n", Options{}, cfg)
	if err == nil {
		t.Fatalf("a run that heard no list went on:\n%s", res.Transcript)
	}
	wantExit(t, res, 3)
	if !strings.Contains(res.Transcript, "no AvatarAnimation was heard in 100ms") {
		t.Errorf("transcript:\n%s", res.Transcript)
	}

	// A run with no animation expectation asks for nothing and waits for nothing.
	f2 := newGrid(t)
	wantExit(t, playWith(t, f2, hdr+"expect no say \"x\" on public from object sign within 100ms\n", Options{}, cfg), 0)
}
