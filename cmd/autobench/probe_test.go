package main

// Taking several readings at once, over a grid that is not there.
//
// The whole of the trick is that two OBJECTS are two speakers: chat
// carries the object's name and key and no more, so two scripts in one
// object are indistinguishable and two in separate objects are not.  What
// has to hold for that to be safe is asserted here -- a probe must never
// run in the measured object, and a cnt>0 probe's SIZE and BASE_MEM are
// arithmetic on a zero and must never reach the run cache copy mode reads
// Size from.
//
// See fake_test.go for the grid.

import (
	"strings"
	"testing"
)

// probeReset puts the caches back, since both are package level and a
// reading taken under one model would otherwise be served to the next.
func probeReset(t *testing.T) {
	t.Helper()
	resetFlags()
	clear(cache)
	clear(probeTest)
	lsdPad = -1
	spentRuns = 0
	t.Cleanup(func() {
		resetFlags()
		clear(cache)
		clear(probeTest)
		lsdPad = -1
		spentRuns = 0
	})
}

// TestProbesRunInTheSpareObjectsAndNeverInTheMeasuredOne: a cnt>0 script
// divides against the base reading its object holds in linkset data, and
// the measured object's is the one the whole benchmark is anchored to.  A
// probe dropped into it would overwrite that, and the benchmark would go
// on reporting plausible numbers.
func TestProbesRunInTheSpareObjectsAndNeverInTheMeasuredOne(t *testing.T) {
	probeReset(t)
	b, f := newFakeRunner(t, 474, 368, 3)

	// The measured object is carrying the base reading a benchmark would
	// have left in it.
	var r Results
	mustRun(b, 0, 474, &r)
	f.mu.Lock()
	anchored := f.objects[100].mem
	f.mu.Unlock()

	pads := []int{600, 700, 800}
	got := probeBase(b, pads)
	for i, pad := range pads {
		if want := f.mem(0, pad); got[i] != want {
			t.Errorf("the base script at pad %d read %d, want %d", pad, got[i], want)
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.objects[100].mem != anchored {
		t.Errorf("a probe wrote %d over the measured object's base of %d",
			f.objects[100].mem, anchored)
	}
	// And they really did go elsewhere: each spare ran one of them.
	for local := uint32(101); local <= 103; local++ {
		if !f.objects[local].hasMem {
			t.Errorf("spare object %d took no probe", local)
		}
	}
}

// TestOnlyTheReadingTravelsFromACopyProbe: everything else a cnt>0 script
// reports -- SIZE, BASE_MEM -- is worked out from linkset data the spare
// object does not have, so it is arithmetic on a zero.  Only the memory
// reading means anything, so only it is kept, and it is kept apart from
// the run cache.
func TestOnlyTheReadingTravelsFromACopyProbe(t *testing.T) {
	probeReset(t)
	b, f := newFakeRunner(t, 474, 368, 3)

	pads := []int{600, 700}
	got := probeAt(b, 1, pads)
	for i, pad := range pads {
		if want := f.mem(1, pad); got[i] != want {
			t.Errorf("one copy at pad %d read %d, want %d", pad, got[i], want)
		}
	}

	for _, pad := range pads {
		key := Cache{Count: 1, Padding: pad}
		if _, ok := cache[key]; ok {
			t.Errorf("a probe's Size reached the run cache at pad %d", pad)
		}
		if _, ok := probeTest[key]; !ok {
			t.Errorf("the reading from pad %d did not travel", pad)
		}
	}
}

// TestAskingForAPadTwiceCostsOneRun: a probe answers from whichever cache
// holds its count, because a run is an upload, a compile, an execution
// and a wait, and everything else this program does is free beside it.
func TestAskingForAPadTwiceCostsOneRun(t *testing.T) {
	probeReset(t)
	b, f := newFakeRunner(t, 474, 368, 3)

	probeBase(b, []int{600, 700})
	f.mu.Lock()
	first := f.ran
	f.mu.Unlock()

	probeBase(b, []int{600, 700})
	probeAt(b, 1, []int{800})
	probeAt(b, 1, []int{800})

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ran != first+1 {
		t.Errorf("%d scripts were sent, want the %d already spent plus one new pad",
			f.ran, first)
	}
}

// TestAProbeThatFailedIsRunTheOrdinaryWay: a probe swallows its error on
// purpose, because running it again the ordinary way puts the failure in
// front of the code that knows which failures mean try something smaller.
// So a spare object that will not compile anything costs a run, not an
// answer.
func TestAProbeThatFailedIsRunTheOrdinaryWay(t *testing.T) {
	probeReset(t)
	b, f := newFakeRunner(t, 474, 368, 3)
	f.refuse[b.spare[1].ID] = []string{"Internal server compile error"}

	pads := []int{600, 700, 800}
	got := probeBase(b, pads)
	for i, pad := range pads {
		if want := f.mem(0, pad); got[i] != want {
			t.Errorf("the base script at pad %d read %d, want %d", pad, got[i], want)
		}
	}
	// The one that failed was taken in the measured object instead, which
	// is where the ordinary path runs.
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.objects[100].hasMem {
		t.Error("the failed probe was not taken again the ordinary way")
	}
}

// TestMoreProbesThanObjectsGoInRounds: the pool is however many spare
// objects there are, and a search asking for three readings with two
// objects to take them in has to take two and then one rather than lose
// the third.
func TestMoreProbesThanObjectsGoInRounds(t *testing.T) {
	probeReset(t)
	b, f := newFakeRunner(t, 474, 368, 2)

	pads := []int{600, 700, 800, 900, 1000}
	got := probeBase(b, pads)
	for i, pad := range pads {
		if want := f.mem(0, pad); got[i] != want {
			t.Errorf("the base script at pad %d read %d, want %d", pad, got[i], want)
		}
	}
}

// TestQuarteringNeedsThreePlacesToPutItsProbes: the round asks three pads
// at once, and with fewer objects than that the readings would be taken
// one at a time -- which is the bisection, and the bisection is already
// below it.  So it declines rather than doing the same work twice.
func TestQuarteringNeedsThreePlacesToPutItsProbes(t *testing.T) {
	probeReset(t)
	b, _ := newFakeRunner(t, 474, 368, 2)

	low, high := quarterSearch(b, 0, minpad, 0, 0, blockSize)
	if low != 0 || high != blockSize {
		t.Errorf("quarterSearch narrowed %d..%d with two objects to probe in", low, high)
	}
	if low, high := quarterSearch(nil, 0, minpad, 0, 0, blockSize); low != 0 || high != blockSize {
		t.Errorf("quarterSearch narrowed %d..%d with no runner at all", low, high)
	}
}

// TestAWholeSearchThroughTheTransport is the end this file exists for:
// the padding search, the quartering, the probes and the confirmation,
// run against a grid rather than against the model that sits above the
// transport.  It has to find the same boundary either way.
func TestAWholeSearchThroughTheTransport(t *testing.T) {
	probeReset(t)
	t.Setenv("SLGO_CONFIG_DIR", t.TempDir())
	b, f := newFakeRunner(t, 474, 368, 3)

	var r Results
	if got := basePadding(b, &r); got != 473 {
		t.Errorf("basePadding = %d, want 473 (the last pad inside the block)", got)
	}
	f.mu.Lock()
	spent := f.ran
	f.mu.Unlock()
	if spent < 5 {
		t.Errorf("the search spent %d runs, which is too few to have searched", spent)
	}
}

// TestARememberedPaddingIsConfirmedAndNotTrusted: finding a padding costs
// about a dozen runs and recognising one costs a hash, so it is worth
// remembering -- but what SL's compiler does can change, and a padding
// that is wrong by k reports every Size wrong by k with nothing in the
// output to show it.  So it is confirmed on every use, which is two runs.
func TestARememberedPaddingIsConfirmedAndNotTrusted(t *testing.T) {
	probeReset(t)
	t.Setenv("SLGO_CONFIG_DIR", t.TempDir())
	b, f := newFakeRunner(t, 474, 368, 3)

	var r Results
	if got := basePadding(b, &r); got != 473 {
		t.Fatalf("basePadding = %d, want 473", got)
	}

	// A second benchmark of the same shape, in a fresh process: the run
	// cache is gone and the file is not.
	clear(cache)
	f.mu.Lock()
	f.ran = 0
	f.mu.Unlock()

	if got := basePadding(b, &r); got != 473 {
		t.Errorf("the remembered padding came back as %d", got)
	}
	f.mu.Lock()
	spent := f.ran
	f.mu.Unlock()
	if spent != 2 {
		t.Errorf("confirming a remembered padding cost %d runs, want the two either side of it", spent)
	}
}

// TestARememberedPaddingThatNoLongerHoldsIsThrownAway: the point of
// confirming is what happens when the confirmation fails, and it has to
// be a search rather than a refusal -- the answer has moved, not gone.
// It says so out loud, because a benchmark that had to argue with its
// instrument should say so whether or not anybody asked.
func TestARememberedPaddingThatNoLongerHoldsIsThrownAway(t *testing.T) {
	probeReset(t)
	dir := t.TempDir()
	t.Setenv("SLGO_CONFIG_DIR", dir)
	b, _ := newFakeRunner(t, 474, 368, 3)

	// A padding from a shape this no longer is: 400 is inside the block,
	// so the byte after it does not cross.
	rememberPadding(baseKey(), 400, 5412)

	var r Results
	var got int
	said := stderrOf(t, func() { got = basePadding(b, &r) })
	if got != 473 {
		t.Errorf("basePadding = %d, want the searched answer 473", got)
	}
	if !strings.Contains(said, "no longer holds") {
		t.Errorf("nothing was said about the remembered padding:\n%s", said)
	}
	// And it was forgotten, so the next benchmark does not pay for it
	// again.
	if e, ok := loadPadCache()[baseKey()]; ok && e.Padding == 400 {
		t.Error("the padding that did not hold is still remembered")
	}
}

// TestNoCacheSearchesEveryTime: --no-cache is for when the caller does
// not want an answer from a file at all, which is the flag to reach for
// when the file itself is what is under suspicion.
func TestNoCacheSearchesEveryTime(t *testing.T) {
	probeReset(t)
	t.Setenv("SLGO_CONFIG_DIR", t.TempDir())
	b, f := newFakeRunner(t, 474, 368, 3)

	flags.NoCache = true
	t.Cleanup(func() { flags.NoCache = false })

	var r Results
	if got := basePadding(b, &r); got != 473 {
		t.Fatalf("basePadding = %d, want 473", got)
	}
	if _, ok := loadPadCache()[baseKey()]; ok {
		t.Error("--no-cache wrote the answer to the file anyway")
	}

	clear(cache)
	f.mu.Lock()
	f.ran = 0
	f.mu.Unlock()
	if got := basePadding(b, &r); got != 473 {
		t.Errorf("basePadding = %d on the second search", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ran <= 2 {
		t.Errorf("the second run spent %d runs, so it read an answer it was told not to", f.ran)
	}
}
