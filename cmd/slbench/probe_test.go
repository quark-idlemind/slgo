package main

// Taking several readings at once, over a grid that is not there.
//
// The whole of the trick is that two OBJECTS are two speakers: chat
// carries the object's name and key and no more, so two scripts in one
// object are indistinguishable and two in separate objects are not.  What
// has to hold for that to be safe is asserted here -- a probe must never
// run in the measured object, and a probe's reading goes into the one run
// cache like any other.
//
// See fake_test.go for the grid.

import (
	"strings"
	"testing"
)

// probeReset puts the cache back, since it is package level and a
// reading taken under one model would otherwise be served to the next.
func probeReset(t *testing.T) {
	t.Helper()
	put := func() {
		resetFlags()
		clear(cache)
		spentRuns, spentRounds = 0, 0
	}
	put()
	t.Cleanup(put)
}

// TestProbesRunInTheSpareObjects: taking three readings at once is the
// whole reason the spares are held, and a search that quietly ran them
// one after another would be correct and slow -- which is the kind of
// thing nothing notices.
//
// It used to matter for a second reason that has gone: a cnt>0 script
// divided against a base its own object held in linkset data, so a probe
// dropped into the measured object overwrote what the benchmark was
// anchored to, and it went on reporting plausible numbers.  The script
// says one number now, and where it ran does not change what it means.
func TestProbesRunInTheSpareObjects(t *testing.T) {
	probeReset(t)
	b, f := newFakeRunner(t, 474, 368, 3)

	pads := []int{600, 700, 800}
	got := probeBase(b, pads)
	for i, pad := range pads {
		if want := f.mem(0, pad); got[i] != want {
			t.Errorf("the base script at pad %d read %d, want %d", pad, got[i], want)
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	for local := uint32(101); local <= 103; local++ {
		if !f.objects[local].ran {
			t.Errorf("spare object %d took no probe", local)
		}
	}
}

// TestAProbesReadingIsAReadingLikeAnyOther: it goes into the one cache,
// and anything asking for that count at that pad is given it.
//
// There were two caches.  A probe taken in a spare object had no base in
// that object's linkset data to divide against, so the size and base its
// script reported were arithmetic on a zero and had to be kept somewhere
// copy mode would never read them.  The arithmetic moved into the
// program and the second cache went with it.
func TestAProbesReadingIsAReadingLikeAnyOther(t *testing.T) {
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
		if _, ok := cache[Cache{Count: 1, Padding: pad}]; !ok {
			t.Errorf("the reading from pad %d did not reach the cache", pad)
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
	f.refuse[b.places[2].obj.ID] = []string{"Internal server compile error"}

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
	if !f.objects[100].ran {
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

	// Five pads, two places to run them: three rounds, the last of them
	// holding a single pad.  The runs are what the grid was asked for and
	// the rounds are what the caller waited through, which is the whole
	// reason for counting them separately.
	if spentRuns != 5 || spentRounds != 3 {
		t.Errorf("five pads in two objects cost %d runs in %d rounds, want 5 in 3",
			spentRuns, spentRounds)
	}
}

// TestTwoPartsIsTheBisectionAndIsDeclined: a round that cuts the range
// in two spends one reading doing it, which is the bisection below.
// With one spare object -- or none, or no runner at all -- that is all
// the parts there are, so it declines rather than doing the same work
// twice.
func TestTwoPartsIsTheBisectionAndIsDeclined(t *testing.T) {
	probeReset(t)
	b, _ := newFakeRunner(t, 474, 368, 1)

	low, high := partSearch(b, 0, minpad, 0, 0, blockSize, 32)
	if low != 0 || high != blockSize {
		t.Errorf("partSearch narrowed %d..%d with one object to probe in", low, high)
	}
	if low, high := partSearch(nil, 0, minpad, 0, 0, blockSize, 4); low != 0 || high != blockSize {
		t.Errorf("partSearch narrowed %d..%d with no runner at all", low, high)
	}

	b, _ = newFakeRunner(t, 474, 368, 8)
	if low, high := partSearch(b, 0, minpad, 0, 0, blockSize, 2); low != 0 || high != blockSize {
		t.Errorf("partSearch narrowed %d..%d when asked for two parts", low, high)
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

// TestMorePartsAreFewerRounds is what --parts is for: every part is a
// reading spent to save a round, and the rounds are what a caller waits
// through.  The counts are the arithmetic -- a round of P parts divides
// what is left by P, so the whole 512-byte block takes ceil(log_P 512)
// of them -- and they are asserted rather than described because the
// point of the flag is that the number goes down.
func TestMorePartsAreFewerRounds(t *testing.T) {
	for _, c := range []struct{ parts, rounds, runs int }{
		{3, 6, 11},
		{4, 5, 13},
		{8, 3, 21},
		{16, 3, 31},
		{32, 2, 46},
	} {
		probeReset(t)
		b, f := newFakeRunner(t, 474, 368, c.parts-1)

		base := memAt(b, 0, minpad)
		rounds, runs := spentRounds, spentRuns
		low, high := partSearch(b, 0, minpad, base, 0, blockSize, c.parts)

		// The crossing itself, not merely a narrower range: the walk
		// that follows a search this exact has nothing left to do.
		// low is the last pad that did NOT grow, so it is the one below
		// the fake's crossing.
		if want := f.crossing - minpad - 1; low != want || high != want+1 {
			t.Errorf("%d parts narrowed to %d..%d, want %d..%d",
				c.parts, low, high, want, want+1)
		}
		if got, want := spentRounds-rounds, c.rounds; got != want {
			t.Errorf("%d parts took %d rounds, want %d", c.parts, got, want)
		}
		if got, want := spentRuns-runs, c.runs; got != want {
			t.Errorf("%d parts spent %d runs, want %d", c.parts, got, want)
		}
	}
}

// TestPartsAreCappedByTheObjectsThereAre: --parts asks, the objects
// decide.  A benchmark told to cut the range thirty-two ways with four
// objects to run in cuts it four ways, rather than sending eight rounds
// of four and calling it one.
func TestPartsAreCappedByTheObjectsThereAre(t *testing.T) {
	probeReset(t)
	b, _ := newFakeRunner(t, 474, 368, 3)
	base := memAt(b, 0, minpad)
	rounds := spentRounds
	low, high := partSearch(b, 0, minpad, base, 0, blockSize, 32)
	capped := spentRounds - rounds

	probeReset(t)
	b, _ = newFakeRunner(t, 474, 368, 3)
	base = memAt(b, 0, minpad)
	rounds = spentRounds
	lo4, hi4 := partSearch(b, 0, minpad, base, 0, blockSize, 4)

	if low != lo4 || high != hi4 || capped != spentRounds-rounds {
		t.Errorf("32 parts in 4 objects narrowed to %d..%d in %d rounds; "+
			"4 parts to %d..%d in %d", low, high, capped, lo4, hi4, spentRounds-rounds)
	}
}

// memAt is the base the search compares against, read the way the search
// reads it.
func memAt(b backend, cnt, pad int) int {
	var r Results
	mustRun(b, cnt, pad, &r)
	return memOf(cnt, r)
}

// TestARememberedPaddingIsConfirmedInTheRoundThatUsesIt: confirming
// costs a round, and the readings wanted next are known before the
// confirmation answers, so they travel in it.
//
// Two things are asserted and the second is the point.  Fewer ROUNDS,
// because that is what a caller waits through.  And exactly the same
// RUNS, because the readings carried are the ones the search was going
// to ask for anyway -- a version of this that carried readings for the
// case where the padding does NOT hold spent eight runs a benchmark to
// save three seconds on a case that essentially never happens, and
// measured slower overall.
func TestARememberedPaddingIsConfirmedInTheRoundThatUsesIt(t *testing.T) {
	t.Setenv("SLGO_CONFIG_DIR", t.TempDir())

	// The one-copy search that follows a confirmation, measured from a
	// file that already holds the answer.  spares is what decides
	// whether the readings can travel: warmTheSearch declines without
	// room for all of them at once.
	measure := func(spares int) (rounds, runs int) {
		probeReset(t)
		flags.Parts = 4
		t.Cleanup(func() { flags.Parts = 8 })
		b, _ := newFakeRunner(t, 474, 368, spares)

		var r Results
		if got := basePadding(b, &r); got != 473 {
			t.Fatalf("basePadding = %d, want 473", got)
		}

		// A benchmark of the same shape in a fresh process: the run
		// cache is gone and the file is not.  Everything oneMode does
		// after the padding, which is what the carrying is aimed at:
		// carry the opening of one search where two are going to run
		// and the second opens a round of its own.
		clear(cache)
		wasRounds, wasRuns := spentRounds, spentRuns
		pad := basePadding(b, &r)
		for _, c := range searchCounts() {
			var more Results
			findPadding(b, c, pad, &more)
		}
		return spentRounds - wasRounds, spentRuns - wasRuns
	}

	// Room for every search's anchor and first round beside the two
	// readings that confirm, which is what leaseSize holds, against one
	// short of it.
	warmRounds, warmRuns := measure(leaseSize() - 1)
	bareRounds, bareRuns := measure(leaseSize() - 2)

	if warmRounds >= bareRounds {
		t.Errorf("carrying the next readings in the confirmation's round took "+
			"%d rounds against %d without", warmRounds, bareRounds)
	}
	if warmRuns != bareRuns {
		t.Errorf("it took %d runs against %d: what is carried is meant to be "+
			"what the search would have asked for, not extra scripts",
			warmRuns, bareRuns)
	}
}

// TestTheSearchIsNotWarmedWithoutRoomForAWholeRound: sending the
// readings in two rounds would spend the confirmation's round and
// another beside it, which is worse than not carrying them at all.
func TestTheSearchIsNotWarmedWithoutRoomForAWholeRound(t *testing.T) {
	probeReset(t)
	t.Setenv("SLGO_CONFIG_DIR", t.TempDir())
	flags.Parts = 4
	t.Cleanup(func() { flags.Parts = 8 })

	// One short of what the carrying needs.
	b, f := newFakeRunner(t, 474, 368, flags.Parts+1)

	var r Results
	if got := basePadding(b, &r); got != 473 {
		t.Fatalf("basePadding = %d, want 473", got)
	}

	clear(cache)
	f.mu.Lock()
	f.ran = 0
	f.mu.Unlock()

	before := spentRounds
	if got := basePadding(b, &r); got != 473 {
		t.Errorf("the remembered padding came back as %d", got)
	}
	if got := spentRounds - before; got != 1 {
		t.Errorf("confirming took %d rounds, want 1", got)
	}
	f.mu.Lock()
	spent := f.ran
	f.mu.Unlock()
	if spent != 2 {
		t.Errorf("confirming cost %d runs, want the two either side of the padding", spent)
	}
}

// TestSearchesAtOnePadShareTheirRounds is the whole of warmPaddings: two
// counts anchored at the same pad narrow in lockstep, because a round
// divides a range by parts exactly and lands in the same number of
// rounds whatever the answer is. So the second search is free in the
// unit that costs time.
//
// The runs are asserted as well, and they are the two searches' runs
// added together -- nothing is saved there and nothing should be wasted
// there either. Scripts are what this spends; rounds are what it saves.
func TestSearchesAtOnePadShareTheirRounds(t *testing.T) {
	const pad = 473

	measure := func(cnts []int, share bool) (rounds, runs int) {
		probeReset(t)
		flags.Parts = 8
		searchesAtOnce = len(cnts)
		t.Cleanup(func() { flags.Parts, searchesAtOnce = 8, 1 })

		b, _ := newFakeRunner(t, 474, 368, leaseSize()-1)
		wasRounds, wasRuns := spentRounds, spentRuns
		if share {
			warmPaddings(b, cnts, pad)
		}
		for _, c := range cnts {
			var r Results
			findPadding(b, c, pad, &r)
		}
		return spentRounds - wasRounds, spentRuns - wasRuns
	}

	oneRounds, _ := measure([]int{1}, true)
	twoRounds, twoRuns := measure([]int{1, 5}, true)
	apartRounds, apartRuns := measure([]int{1, 5}, false)

	if twoRounds != oneRounds {
		t.Errorf("two searches sharing rounds took %d rounds; one takes %d",
			twoRounds, oneRounds)
	}
	if twoRounds >= apartRounds {
		t.Errorf("sharing took %d rounds and searching separately %d, so sharing "+
			"bought nothing", twoRounds, apartRounds)
	}
	if twoRuns != apartRuns {
		t.Errorf("sharing spent %d runs against %d apart: the readings shared are "+
			"meant to be the ones the searches would have asked for", twoRuns, apartRuns)
	}
}
