package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/scripttest"
)

// These tests drive the measurement machinery against the offline model, so
// no Second Life and no viewer are involved.  The model is a real script.v1
// backend running in this process, which is what --test is: every call here
// goes through buildScript, openScript, runIn, the compile verdict, sift and
// absorbResults, exactly as a live run does.  What is not real is the
// readings, and nothing else.
//
// What is also not here is a connection.  The backend is reached through
// scripttest's in-process client, because these tests are about the
// measurement and there are 440,000 runs of it; what a connection does to a
// run is scripttest's own business and is tested there, over its pipe.
//
// What the model says.  For cnt copies at pad it reports
//
//	mem(cnt, pad) = 5412 + blockSize*(floor(x/blockSize) + 1),
//	           x  = pad - Memory.Pad + cnt*Memory.CodeSize
//
// which is a staircase in pad with one blockSize step every blockSize bytes,
// exactly what live SL does.  The step for cnt=0 falls AT pad == Memory.Pad:
// that pad is one byte into a fresh block and pad-1 is the last pad still
// inside the old one.
//
// So Memory.Pad is the CROSSING pad, and the padding slbench names -- what
// Padding: prints and what --ipad takes and is confirmed against -- is one
// less.  The tests below spell that out as `crossing` rather than `pad` to
// keep the two apart, because getting them confused was a real bug, found
// against live SL.
//
// The anchor case is the live Agni reference, measured 2026-08-03 with
// --code 'foo_CNT(){llDie();}': Padding: 473, Size: 368, and the base script
// reads 5412 bytes at pad 473 and 5924 at pad 474.  That is {crossing: 474,
// codeSize: 368} here.

// modelled is a benchmark's backend together with the staircase it answers
// from, so that a test can say what a reading should have been without
// working the staircase out a second time -- two copies of it drift.
type modelled struct {
	backend
	mem scripttest.Memory
	srv *scripttest.Server
}

// noLimit is a model that refuses nothing, for a case about something other
// than the compiler.  Larger than the memory any script has, rather than
// scripttest's negative "no limit at all", so that a case which does go over
// it is a wrong answer rather than an infinite one.
const noLimit = 1 << 30

// stop hands the model back without waiting for the test to finish.
//
// t.Cleanup does not run until the test does, and the sweeps here build
// thousands of these -- so a sweep that only registered a cleanup would have
// thousands of servers standing at once.  Calling it twice is harmless,
// which is what lets the cleanup stay registered as well.
func (m *modelled) stop() {
	m.Close()
	m.srv.Stop()
}

// offline serves one case's model as a backend, and puts back everything a
// benchmark carries between runs.
//
// crossing is the pad at which the copy-free base script first tips into the
// next block; codeSize is the cost of one copy of CODE.
func offline(t *testing.T, crossing, codeSize int) *modelled {
	t.Helper()
	return offlineWith(t, scripttest.Options{
		Memory: scripttest.Memory{Pad: crossing, CodeSize: codeSize},
	})
}

// TestABackendThatKeepsNothingIsMeasured: the script says the one number
// a reading is and the base is kept here, so a backend whose objects
// forget everything between runs is leased and measures what one that
// remembers does.
func TestABackendThatKeepsNothingIsMeasured(t *testing.T) {
	b := offlineWith(t, scripttest.Options{
		Memory:        scripttest.Memory{Pad: 474, CodeSize: 368},
		NoPersistence: true,
	})
	var r Results
	if err := runScript(b, 0, 474, &r); err != nil {
		t.Fatalf("base run: %v", err)
	}
	if err := runScript(b, 1, 474, &r); err != nil {
		t.Fatalf("one-copy run: %v", err)
	}
	if want := b.mem.Reading(0, 474); r.Base != want {
		t.Errorf("Base = %d, want %d", r.Base, want)
	}
	if want := b.mem.Reading(1, 474); r.Test != want {
		t.Errorf("Test = %d, want %d", r.Test, want)
	}
}

// offlineWith is offline for a case that has more to say about the model
// than its crossing and its size -- a limit, a marginal cost, a reading that
// comes back wrong once.
//
// One object unless a case asks for more, and that is deliberate: with spare
// objects the search quarters its range instead of bisecting it, and the
// pads it reads move.  The cases below that replay a reading one block out
// name the pad their bad reading arrives at, so they would be asserting
// nothing at all against a search that no longer goes there.  What a
// search does with spares is measured against the grid transport in
// probe_test.go and against this one in
// TestASearchThroughTheModelUsesTheSpareObjects.
func offlineWith(t *testing.T, o scripttest.Options) *modelled {
	t.Helper()
	if o.GroupSize == 0 {
		o.GroupSize = 1
	}
	s := scripttest.New(o)
	// In process rather than over scripttest.Pipe.  The sweeps below run
	// about 440,000 scripts, which over a connection is 100 microseconds
	// each and about a minute of this package's test time spent almost
	// entirely on goroutine hand-off; in process it is 5.  What the
	// connection would be testing is the transport, and the transport is
	// tested in scripttest, over Pipe, once.
	b, err := openScript(context.Background(), s.Direct(), o.GroupSize, "", "test", false)
	if err != nil {
		s.Stop()
		t.Fatalf("leasing from the model: %v", err)
	}
	m := &modelled{backend: b, mem: o.Memory, srv: s}
	t.Cleanup(m.stop)

	// Everything a benchmark accumulates as it goes, put back to what a
	// fresh process would have.  The run cache is keyed on {count, pad}
	// only, so a reading taken under one model would be served to the
	// next.
	reset := func() {
		flags.IPad = 0
		// The second search is a thing a case asks for, not something
		// every offline case should pay for: the sweeps below measure
		// Size across hundreds of shapes and --extra would double them.
		flags.Extra = 0
		clear(cache)
		spentRuns, spentRereads, spentCompiles = 0, 0, 0
	}
	reset()
	t.Cleanup(reset)

	return m
}

// affine is offline for a construct whose first copy costs more than the
// ones after it, which is the whole reason --extra measures two counts.
func affine(t *testing.T, crossing, abs, marginal, limit int) *modelled {
	t.Helper()
	return offlineWith(t, scripttest.Options{Memory: scripttest.Memory{
		Pad: crossing, CodeSize: abs, Marginal: marginal, Limit: limit,
	}})
}

// noiseOnce makes the model answer wrong the first time it is asked about
// (cnt, pad) and truthfully every time after, which is the live event
// exactly: one bad reading, cached, and served back to the rest of the
// search.
//
// It returns the hook and a pointer to the fire count, so a test can insist
// the fault it arranged actually happened -- a hook that never fires makes
// every assertion after it vacuous, and a silent no-op is how a mutation test
// passes for the wrong reason.
func noiseOnce(cnt, pad, delta int) (func(int, int, int) int, *int) {
	fired := 0
	return func(c, p, reading int) int {
		if c == cnt && p == pad && fired == 0 {
			fired++
			return reading + delta
		}
		return reading
	}, &fired
}

// noisy is offline for a case whose instrument misreports itself once.
func noisy(t *testing.T, crossing, codeSize int, noise func(cnt, pad, reading int) int) *modelled {
	t.Helper()
	return offlineWith(t, scripttest.Options{
		Memory: scripttest.Memory{Pad: crossing, CodeSize: codeSize},
		Noise:  noise,
	})
}

// cases used by more than one test.  Each is a (crossing, codeSize) pair the
// machinery has to reproduce.
var modelCases = []struct {
	name     string
	crossing int
	codeSize int
}{
	{"live reference --code foo_CNT(){llDie();}", 474, 368},
	{"live --globals g --code f_CNT(){}", 446, 44},
	{"live --globals g --statement g = g;", 406, 16},
	{"live --globals g --statement g = (integer)g;", 406, 44},
	{"smallest expressible padding", minpad + 1, 100},
	{"one-byte construct", 300, 1},
	{"construct just under a block", 300, 511},
	{"padding a whole block up", 986, 368},
}

// bigModelCases are constructs of a block or more.
//
// They are apart from modelCases because they were once the hard half:
// -1 mode measured a copy as the distance between two block boundaries,
// so a copy that carried itself over a boundary left a remainder rather
// than an answer -- 1066 bytes reported as 8.  oneMode is exact on them
// now, but they stay named because a size that spans a boundary is the
// case to break first.
//
// The alignments matter and are chosen for it: a size that is exactly a
// block leaves nothing over, one byte more leaves one byte, and the two
// measured live -- 542 and 1066 -- leave 30 and 42.  The crossing is
// varied as well, since where the base script sits in its block decides
// where a copy lands in the next.
var bigModelCases = []struct {
	name     string
	crossing int
	codeSize int
}{
	{"exactly one block", 474, blockSize},
	{"one byte over a block", 474, blockSize + 1},
	{"the 250-character string, measured live", 474, 542},
	{"the 500-character string, measured live", 474, 1066},
	{"exactly two blocks", 300, 2 * blockSize},
	{"two blocks and a byte", 300, 2*blockSize + 1},
	{"a block over, crossing low in its block", minpad + 1, 600},
	{"a block over, crossing high in its block", 986, 900},
	{"far over: eight blocks", 406, 8 * blockSize},
}

// TestModelIsAStaircase checks the model itself before anything is measured
// against it: memory must be flat below the crossing pad, jump by exactly one
// block at it, and stay flat for the rest of that block.
func TestModelIsAStaircase(t *testing.T) {
	const crossing = 474
	b := offline(t, crossing, 368)

	var r Results
	read := func(pad int) int {
		mustRun(b, 0, pad, &r)
		return r.Base
	}

	below := read(crossing - 1)
	at := read(crossing)
	if at-below != blockSize {
		t.Fatalf("pad %d -> %d, pad %d -> %d: want a %d-byte step, got %d",
			crossing-1, below, crossing, at, blockSize, at-below)
	}
	if got := read(crossing - blockSize); got != below {
		t.Errorf("pad %d = %d, want %d (same block as pad %d)",
			crossing-blockSize, got, below, crossing-1)
	}
	if got := read(crossing + blockSize - 1); got != at {
		t.Errorf("pad %d = %d, want %d (same block as pad %d)",
			crossing+blockSize-1, got, at, crossing)
	}
	if got := read(crossing + blockSize); got != at+blockSize {
		t.Errorf("pad %d = %d, want %d (the next crossing)",
			crossing+blockSize, got, at+blockSize)
	}
}

// lowestCrossing is the boundary the cnt=0 search will actually land on.  The
// staircase repeats every blockSize, so a shape whose crossing is named 986 also
// crosses at 474 and at -38; the search starts from minpad and climbs, so it
// finds the lowest crossing above minpad.  That is why the padding a search
// REPORTS is always under a block, while a padding --ipad ACCEPTS need not be.
func lowestCrossing(crossing int) int {
	for crossing-blockSize > minpad {
		crossing -= blockSize
	}
	return crossing
}

// TestFindPaddingFindsTheLastPadInside pins findPadding's return convention:
// the offset of the last pad that is still inside the block, so that offset+1
// is the crossing.
func TestFindPaddingFindsTheLastPadInside(t *testing.T) {
	for _, tc := range modelCases {
		t.Run(tc.name, func(t *testing.T) {
			b := offline(t, tc.crossing, tc.codeSize)
			var r Results
			want := lowestCrossing(tc.crossing) - minpad - 1
			got, base := findPadding(b, 0, minpad, &r)
			if got != want {
				t.Errorf("findPadding(0, %d) = %d, want %d (crossing %d)",
					minpad, got, want, lowestCrossing(tc.crossing))
			}
			// The base comes back so that a caller measuring a step against it
			// gets the reading the search actually used, not one taken before
			// the search that a re-read may since have corrected.
			if want := b.mem.Reading(0, minpad); base != want {
				t.Errorf("findPadding returned base %d, want the reading at pad %d, %d",
					base, minpad, want)
			}
		})
	}
}

// TestBasePaddingNamesTheLastPadInside is what a padding is, stated as a
// test: the largest pad that still fits inside its block, so one more byte
// crosses.  basePadding must return crossing-1, never the crossing itself.
func TestBasePaddingNamesTheLastPadInside(t *testing.T) {
	for _, tc := range modelCases {
		t.Run(tc.name, func(t *testing.T) {
			b := offline(t, tc.crossing, tc.codeSize)
			var r Results
			want := lowestCrossing(tc.crossing) - 1
			if got := basePadding(b, &r); got != want {
				t.Errorf("basePadding() = %d, want %d", got, want)
			}
		})
	}
}

// TestBasePaddingTakesIPadOnTrust: a supplied --ipad is used exactly as
// given, with no search -- only the two readings that confirm it -- so a
// padding a block above the one a search would find comes back as named.
func TestBasePaddingTakesIPadOnTrust(t *testing.T) {
	b := offline(t, 474, 368)
	flags.IPad = 985
	var r Results
	if got := basePadding(b, &r); got != 985 {
		t.Errorf("basePadding() with --ipad 985 = %d, want 985", got)
	}
}

// TestOneModeReportsTheCodeSize is the whole point of oneMode: hand it a model
// whose code costs codeSize bytes and it has to say so, and it has to name the
// padding as the last pad inside the block, not the crossing, while doing it.
func TestOneModeReportsTheCodeSize(t *testing.T) {
	for _, tc := range append(append([]struct {
		name     string
		crossing int
		codeSize int
	}{}, modelCases...), bigModelCases...) {
		t.Run(tc.name, func(t *testing.T) {
			b := offline(t, tc.crossing, tc.codeSize)
			var r Results
			size, padding, _, _, _ := oneMode(b, &r)
			if size != tc.codeSize {
				t.Errorf("Size: %d, want %d", size, tc.codeSize)
			}
			if want := lowestCrossing(tc.crossing) - 1; padding != want {
				t.Errorf("Padding: %d, want %d", padding, want)
			}
		})
	}
}

// TestOneModeIsPaddingIndependent is the property that makes --ipad safe to
// reuse across shapes: the answer must not depend on WHICH boundary the base
// script was lifted to.  Verified live on 2026-08-03 with --ipad 473 and
// --ipad 985 both giving Size: 368.
// The search only ever lands on the lowest boundary, so the way to reach a
// higher one is --ipad, which is exactly how it was checked live: --ipad 473 and
// --ipad 985 are the same boundary a block apart and both gave Size: 368.
func TestOneModeIsPaddingIndependent(t *testing.T) {
	const codeSize = 368
	for _, ipad := range []int{0, 473, 473 + blockSize, 473 + 2*blockSize} {
		t.Run(fmt.Sprintf("ipad=%d", ipad), func(t *testing.T) {
			b := offline(t, 474, codeSize)
			flags.IPad = ipad
			var r Results
			size, padding, _, _, _ := oneMode(b, &r)
			if size != codeSize {
				t.Errorf("Size: %d, want %d", size, codeSize)
			}
			want := ipad
			if ipad == 0 {
				want = 473
			}
			if padding != want {
				t.Errorf("Padding: %d, want %d", padding, want)
			}
		})
	}
}

// TestACopyRunDividesByTheBaseRunBeforeIt pins where Size comes from:
// runScript computes (Test - Base)/count, and Base is the cnt=0 reading r is
// holding, not any anchor the model has inside it.  Anchoring on the constant
// once put +blockSize/count on every copy-mode Size and hid the linkset-data
// bug underneath it.
func TestACopyRunDividesByTheBaseRunBeforeIt(t *testing.T) {
	const crossing, codeSize, cnt = 474, 368, 128
	b := offline(t, crossing, codeSize)

	var r Results
	mustRun(b, 0, crossing, &r) // the base run
	base := r.Base
	mustRun(b, cnt, crossing, &r)
	if want := float64(r.Test-base) / cnt; r.Size != want {
		t.Errorf("Size = %v, want %v ((%d - %d)/%d)", r.Size, want, r.Test, base, cnt)
	}
	if r.Size != codeSize {
		t.Errorf("Size = %v, want %d", r.Size, codeSize)
	}
}

// TestExpressiblePadding pins the one rule --ipad enforces: not "is this a
// boundary" -- the caller asserts that -- but "can the filler emit exactly this
// many bytes, and one more".  In particular a padding a whole block up is
// perfectly real, so there is no upper bound to enforce.
//
// Every pad from nought up is expressible now.  1, 2 and 3 were not,
// while the jump/label pair was spent only on an odd pad: 1 and 3 both
// came out as the bare pair and measured 5, and 2 could not have the
// pad above it.  Only a negative one is left, and it is refused because
// the filler emits nothing at all for it.
func TestExpressiblePadding(t *testing.T) {
	for _, tc := range []struct {
		pad  int
		want bool
	}{
		{-4, false},
		{-1, false},
		{0, true},
		{1, true},
		{2, true},
		{3, true},
		{4, true},
		{6, true},
		{473, true},
		{480, true}, // measured live on 2026-08-22
		{985, true},
		{992, true}, // and its boundary a block up
		{1497, true},
	} {
		if got := expressiblePadding(tc.pad); got != tc.want {
			t.Errorf("expressiblePadding(%d) = %v, want %v", tc.pad, got, tc.want)
		}
	}
}

// --- a script Second Life will not compile -------------------------------

// refusesOver is the model for a case about a script Second Life would not
// compile: the same staircase, with a reading it refuses above.
//
// The model has no compiler, so what stands in for one is the thing the
// compiler is asked about -- the memory the script would use.  The refusal
// arrives where the live one does, on the run, having executed nothing.
func refusesOver(t *testing.T, crossing, codeSize, limit int) *modelled {
	t.Helper()
	return offlineWith(t, scripttest.Options{
		Memory: scripttest.Memory{Pad: crossing, CodeSize: codeSize, Limit: limit},
	})
}

// TestCompileRefusalIsNotAStackHeapCollision keeps the two apart:
// runtimeError models a RUN-TIME fault, and a compile-time refusal wearing
// that name would make a limit that stopped the script from ever starting look
// like one it hit while running.
func TestCompileRefusalIsNotAStackHeapCollision(t *testing.T) {
	b := refusesOver(t, 474, 368, 30*1024)

	err := runScript(b, 128, 474, &Results{})
	if err == nil {
		t.Fatal("128 copies over the model's limit must be refused")
	}
	var ce *compileError
	if !errors.As(err, &ce) {
		t.Errorf("refusal is %T, want *compileError", err)
	}
	var re *runtimeError
	if errors.As(err, &re) {
		t.Error("a compile refusal must not present as a run-time error")
	}
}

// TestRefusedRunIsNotCached is why runScript caches a run only after it has
// come back: a refusal is not a reading, and caching one would serve it back
// as a memory figure to whatever asked next.
func TestRefusedRunIsNotCached(t *testing.T) {
	b := refusesOver(t, 474, 368, 30*1024)

	if err := runScript(b, 128, 474, &Results{}); err == nil {
		t.Fatal("want a refusal")
	}
	if _, ok := cache[Cache{Count: 128, Padding: 474}]; ok {
		t.Error("a refused run left a reading in the cache")
	}
}

// A reading one block out.  A padding search is a chain of comparisons between
// readings of llGetUsedMemory, and on 2026-08-03 one of those readings came back
// exactly one block high.  These drive the same event through the model.
// Why: doc/scripttest.md#a-reading-one-block-high
//
// The live fault has been seen ONCE, in one reading, and 45 later asks at that
// pad all agreed with each other (TestLiveReadingIsStable, 10 runs at each of
// four pads, plus five earlier). So it cannot be provoked live to order, and a
// hook is the only way to write these at all.

// TestOneModeSurvivesAReadingOneBlockHigh replays the incident.
//
// The live shape is the model's reference case: the base script crosses at 474,
// one copy of CODE costs 368 bytes, and the one-copy script therefore crosses at
// 618 -- confirmed live, where pad 602 reads 5924 and pad 618 reads 6436.  At
// 20:01 the one-copy script at pad 602 read 6436, one block high, and the search
// took that for the crossing.
//
// The injected pad is 601 rather than the 602 of the incident: the search now
// starts at the padding rather than a byte past it, so every probe it makes has
// moved down by one.  What is being replayed is a reading one block high on the
// search's path below the crossing, which is what 601 now is.
//
// The numbers below are the two live runs, to the byte:
//
//	20:01  Result pad: 128  Size: 384   <- the anomaly believed
//	20:36  Result pad: 144  Size: 368   <- the published figure
//
// So this fails without the confirmation, and it fails with the exact wrong
// answer that was published, not merely with some wrong answer.
func TestOneModeSurvivesAReadingOneBlockHigh(t *testing.T) {
	flags.Paranoid = true
	t.Cleanup(func() { flags.Paranoid = false })
	noise, fired := noiseOnce(1, 601, blockSize)
	b := noisy(t, 474, 368, noise)

	var r Results
	size, padding, pad, _, _ := oneMode(b, &r)
	if *fired != 1 {
		t.Fatalf("the bad reading was never taken (%d times); pad 601 is not on the "+
			"search's path any more and this test is asserting nothing", *fired)
	}
	if size != 368 || padding != 473 || pad != 144 {
		t.Errorf("Size: %d Padding: %d Result pad: %d, want 368/473/144 "+
			"(384/473/128 is the anomaly being believed)", size, padding, pad)
	}
}

// TestOneModeSurvivesAReadingOneBlockLow is the other direction, which has not
// been seen live and is exactly as plausible as the one that has: the anomaly
// was one block, and a block is a block whichever way it goes.
//
// A spuriously LOW reading above the crossing reads as "still inside the block",
// so the bisection settles ABOVE the real crossing and the walk finds a step at
// the first pad it tries.  Confirming the pad that crossed is not enough to
// catch that -- it really does read high -- which is why confirmCrossing also
// re-reads the pad BELOW, the one the search accepted as inside.
func TestOneModeSurvivesAReadingOneBlockLow(t *testing.T) {
	flags.Paranoid = true
	t.Cleanup(func() { flags.Paranoid = false })
	// 729 is the bisection's first probe above the crossing at 618.
	noise, fired := noiseOnce(1, 729, -blockSize)
	b := noisy(t, 474, 368, noise)

	var r Results
	size, padding, pad, _, _ := oneMode(b, &r)
	if *fired != 1 {
		t.Fatalf("the bad reading was never taken (%d times)", *fired)
	}
	if size != 368 || padding != 473 || pad != 144 {
		t.Errorf("Size: %d Padding: %d Result pad: %d, want 368/473/144", size, padding, pad)
	}
}

// TestOneModeSurvivesAMisreadBase is the case the crossing confirmation cannot
// reach on its own.  Everything a search decides is a comparison against the
// base, so a base read one block high makes every pad below the real crossing
// look like it crossed and every pad above it look like it did not -- an
// inverted staircase, in which the step the walk eventually finds is real,
// reproducible, and in the wrong place.
//
// Two things have to work for this to come out right: the walk has to give up
// after a block rather than chase the next boundary a run per byte, and the
// search has to be run again against the corrected base instead of reporting
// the answer it had.
func TestOneModeSurvivesAMisreadBase(t *testing.T) {
	noise, fired := noiseOnce(1, 473, blockSize)
	b := noisy(t, 474, 368, noise)

	var r Results
	size, padding, pad, _, _ := oneMode(b, &r)
	if *fired != 1 {
		t.Fatalf("the bad reading was never taken (%d times)", *fired)
	}
	if size != 368 || padding != 473 || pad != 144 {
		t.Errorf("Size: %d Padding: %d Result pad: %d, want 368/473/144", size, padding, pad)
	}
	// The bound is the point.  Without it the walk climbs from the bogus base to
	// the next boundary, which is 512 runs -- instant here and an hour live.
	if spentRuns > 60 {
		t.Errorf("recovering from one misread base cost %d runs (%d re-reads); "+
			"the walk is not being bounded at a block", spentRuns, spentRereads)
	}
}

// TestConfirmationCostsAHandfulOfRuns is what --paranoid costs: a run is
// an upload, a compile, an execution and a wait, so this is the only unit
// that matters.
//
// A benchmark searches three times at --extra=4 -- the base script, the
// one-copy script and the five-copy one -- and each search
// ends by re-reading three things: the pad that crossed, the base, and
// the pad below.  Nine runs, on a benchmark that spends about four
// dozen, and one ROUND per search because the three do not depend on
// each other.
func TestConfirmationCostsAHandfulOfRuns(t *testing.T) {
	flags.Paranoid = true
	b := offline(t, 474, 368)
	// After offlineWith, which puts --extra back to nought: this is
	// about what a benchmark with a second search spends.
	flags.Extra = 4
	t.Cleanup(func() { flags.Paranoid, flags.Extra = false, 4 })
	spentRuns, spentRereads = 0, 0

	var r Results
	if size, _, _, _, _ := oneMode(b, &r); size != 368 {
		t.Fatalf("Size: %d, want 368", size)
	}
	if spentRereads != 9 {
		t.Errorf("%d re-reads, want 9: three searches confirming three readings each",
			spentRereads)
	}
	if spentRuns > 80 {
		t.Errorf("a clean -1 benchmark spent %d runs; confirmation is meant to add "+
			"a handful, not a search", spentRuns)
	}
}

// TestASearchThroughTheModelUsesTheSpareObjects: with somewhere to put
// them the search asks three pads at once, and over this transport that is
// three Run calls in three leased objects rather than one in the measured
// one.  The answer must not depend on which of those happened.
//
// What makes it hold is probe.go's premise: the script says its own
// memory and nothing else, and the base is kept here, so a reading taken
// in a spare means what one in the measured object does.
// Why: doc/scripttest.md#a-benchmark-script-says-one-number
func TestASearchThroughTheModelUsesTheSpareObjects(t *testing.T) {
	b := offlineWith(t, scripttest.Options{
		Memory:    scripttest.Memory{Pad: 474, CodeSize: 368},
		GroupSize: 4,
	})
	if b.Spares() != 3 {
		t.Fatalf("the lease granted %d spare objects, want 3", b.Spares())
	}

	var r Results
	size, padding, pad, _, _ := oneMode(b, &r)
	if size != 368 || padding != 473 || pad != 144 {
		t.Errorf("Size: %d Padding: %d Result pad: %d, want 368/473/144",
			size, padding, pad)
	}
	// A search with spares takes more readings than it sends scripts
	// sequentially; an empty cache would mean the whole thing went the
	// one-object way and this has asserted that case twice.
	if len(cache) == 0 {
		t.Error("no reading was taken at all")
	}
}

// --------------------------------------------- confirming one crossing

// confirmAt sets a search up at the point confirmCrossing is asked its
// question: cnt copies at pad, a step believed to be at pad+low, and the
// reading the search took there sitting in r.
func confirmAt(t *testing.T, b *modelled, pad, low, first int) (crossing, int, string) {
	t.Helper()
	r := Results{Test: first}
	getBase := func() int { return r.Test }
	var verdict crossing
	var base int
	said := stderrOf(t, func() {
		verdict, base = confirmCrossing(b, 1, pad, low, b.mem.Reading(1, pad), &r, getBase)
	})
	return verdict, base, said
}

// TestAConfirmedCrossingIsAPairOfReadingsAndTheBaseTheyAreAgainst: this
// is the whole of --paranoid.  A padding search is a chain of comparisons
// against readings of llGetUsedMemory, and on 2026-08-03 one of those
// came back exactly one block high -- which from inside the search looks
// precisely like the memory having grown, the event it exists to find.
// So the step is re-read before it is turned on, and so is the base.
func TestAConfirmedCrossingIsAPairOfReadingsAndTheBaseTheyAreAgainst(t *testing.T) {
	// The live reference: the one-copy script crosses at 618, which is 16
	// bytes above the pad the runs are taken at.
	const pad, low = 602, 16

	b := offline(t, 474, 368)
	verdict, _, said := confirmAt(t, b, pad, low, b.mem.Reading(1, pad+low))
	if verdict != crossingHolds {
		t.Errorf("a real crossing was not confirmed: %v", verdict)
	}
	if said != "" {
		t.Errorf("a clean confirmation complained:\n%s", said)
	}

	// The pad that looked like it crossed reads inside the block on a
	// second ask: the first reading was noise, and the walk continues past
	// it rather than turning on it.
	b = offline(t, 474, 368)
	verdict, _, said = confirmAt(t, b, pad, 8, b.mem.Reading(1, pad+low))
	if verdict != crossingLater {
		t.Errorf("a reading that did not reproduce was taken for a crossing: %v", verdict)
	}
	if !strings.Contains(said, "was noise") {
		t.Errorf("nothing was said about the reading that did not reproduce:\n%s", said)
	}

	// Both asks say memory grew and they do not agree with each other.
	// The crossing is here, and the disagreement is worth saying out loud
	// because nothing else in the output would show it.
	b = offline(t, 474, 368)
	verdict, _, said = confirmAt(t, b, pad, low, b.mem.Reading(1, pad+low)+blockSize)
	if verdict != crossingHolds {
		t.Errorf("a crossing both readings agree about was not taken: %v", verdict)
	}
	if !strings.Contains(said, "do not agree") {
		t.Errorf("two disagreeing readings passed without comment:\n%s", said)
	}
}

// TestABaseThatMovedMakesEveryComparisonSuspect: the base is what every
// decision in a search was made against, so one that reads differently
// afterwards does not invalidate the step -- it invalidates the search.
// The answer is to run it again from the corrected reading, which is
// cheap, since every reading it took is in the cache.
func TestABaseThatMovedMakesEveryComparisonSuspect(t *testing.T) {
	const pad, low = 602, 16

	// The re-read of the base is the second thing confirmCrossing asks
	// for, and this is the one that answers it differently.
	noise, fired := noiseOnce(1, pad, blockSize)
	b := noisy(t, 474, 368, noise)

	verdict, base, said := confirmAt(t, b, pad, low, b.mem.Reading(1, pad+low))
	if *fired != 1 {
		t.Fatalf("the base was never re-read (%d times), so this asserts nothing", *fired)
	}
	if verdict != crossingSuspect {
		t.Errorf("a base that moved left the search standing: %v", verdict)
	}
	if want := b.mem.Reading(1, pad) + blockSize; base != want {
		t.Errorf("the corrected base is %d, want %d", base, want)
	}
	if !strings.Contains(said, "being run again") {
		t.Errorf("nothing was said about the search being redone:\n%s", said)
	}
}

// ------------------------------------------------ what copy mode cannot do

// TestOneModeIsExactEverywhere sweeps the whole space the model can
// express, rather than the dozen shapes named above.
//
// It is here because the arithmetic changed and the old arithmetic was
// wrong in a way no hand-picked case caught for months: it reported a
// remainder, which is a small plausible number, for every construct of a
// block or more.  A sweep is what turns "the cases we thought of" into
// "the cases there are".
//
// Two passes, because the two axes fail differently: a size wrong by a
// block is the arithmetic, and a crossing wrong by anything is where the
// base script happened to sit in its block.
func TestOneModeIsExactEverywhere(t *testing.T) {
	run := func(crossing, size int) int {
		b := refusesOver(t, crossing, size, noLimit)
		got, _, _, _, _ := oneMode(b, &Results{})
		return got
	}

	// Every size from nothing to two blocks, at crossings low, middling
	// and high in their own block.
	bad := 0
	for _, crossing := range []int{minpad + 1, 300, 474, 511, 512, 513, 986} {
		for size := 0; size <= 2*blockSize && bad < 10; size++ {
			if got := run(crossing, size); got != size {
				t.Errorf("crossing %d, size %d: Size %d", crossing, size, got)
				bad++
			}
		}
	}

	// And every crossing over two blocks, at sizes chosen to sit either
	// side of a boundary.
	for _, size := range []int{0, 1, 368, 511, 512, 513, 1066} {
		for crossing := 1; crossing <= 2*blockSize && bad < 10; crossing++ {
			if got := run(crossing, size); got != size {
				t.Errorf("crossing %d, size %d: Size %d", crossing, size, got)
				bad++
			}
		}
	}
}

// ------------------------------------------ what a copy pays only once

// affineCases are constructs whose first copy costs more than the ones
// after it, which is the whole reason --extra measures two counts.
//
// The 1044/542 pair is the guide's live measurement: a 250-character
// string literal is 1044 bytes for one copy and 542 for each after it,
// because identical literals are shared and an extra copy pays only for
// what it cannot share.  The rest bracket it.
var affineCases = []struct {
	name     string
	crossing int
	abs      int // what the first copy costs outright
	marg     int // what each copy after it costs
}{
	{"nothing paid once", 474, 368, 368},
	{"the shared 250-character literal, measured live", 474, 1044, 542},
	{"almost all of it paid once", 474, 280, 22},
	{"paid once and nothing after", 300, 500, 1},
	{"a byte apart", 406, 45, 44},
	{"over a block, shared", 986, 1066, 600},
	{"an extra copy dearer than the first", 300, 22, 44},
}

// TestOneModeMeasuresTheFirstCopy is the other half of the same fact:
// oneMode uses one copy, so the Size it reports is what one costs
// outright -- the absolute cost, not the marginal one, which is what
// --extra reports.  The two answer different questions and a construct
// that pays something once is where that stops being a technicality.
func TestOneModeMeasuresTheFirstCopy(t *testing.T) {
	for _, tc := range affineCases {
		t.Run(tc.name, func(t *testing.T) {
			b := affine(t, tc.crossing, tc.abs, tc.marg, 0)

			var r Results
			size, _, _, _, _ := oneMode(b, &r)
			if size != tc.abs {
				t.Errorf("Size: %d, want the first copy's %d", size, tc.abs)
			}
		})
	}
}

// TestWithoutParanoidAReadingABlockOutIsBelieved says what the flag buys,
// with the number it buys it against.
//
// This is not a bug being pinned; it is a price. A search converges ON a
// reading a block high, so the result it produces is entirely consistent
// -- flat below, a block up at that pad and above -- and no amount of
// data already in hand distinguishes it from a real crossing. Only
// asking again does, and asking again is a round.
//
// The event has been seen once, live, on 2026-08-03, and never
// reproduced: TestLiveReadingIsStable asked 45 times at the pads
// involved and every answer agreed. The likelier account of that day is
// the driver rather than the compiler -- scripts were arriving at the
// asset store empty at the time, silently, in 3 runs out of 8. So the
// default is off, and this records what off means.
func TestWithoutParanoidAReadingABlockOutIsBelieved(t *testing.T) {
	if flags.Paranoid {
		t.Fatal("--paranoid defaults on; this test asserts what its absence costs")
	}
	noise, fired := noiseOnce(1, 601, blockSize)
	b := noisy(t, 474, 368, noise)

	var r Results
	size, _, pad, _, _ := oneMode(b, &r)
	if *fired != 1 {
		t.Fatalf("the bad reading was never taken (%d times)", *fired)
	}
	// The anomaly, to the byte, as published live on 2026-08-03.  The
	// staircase check cannot see it: every reading agrees with it.
	if size != 384+1 || pad != 128-1 {
		t.Errorf("Size: %d Result pad: %d; want the 2026-08-03 anomaly, "+
			"385/127 with the search starting a byte lower than it did then. "+
			"If this now comes out 368/144 the crossing is being caught some "+
			"other way and --paranoid may have stopped earning its round",
			size, pad)
	}
}

// ------------------------------------------- what a copy after the first costs

// TestExtraMeasuresWhatEachCopyAfterTheFirstCosts: --extra=N measures
// the script with N further copies at the same padding, and the
// difference from the one-copy script is N marginal copies and nothing
// else -- the part a construct pays once and shares cancels.
//
// That is the number copy mode existed to produce, got from a script with
// a handful of copies rather than one with up to 512.
func TestExtraMeasuresWhatEachCopyAfterTheFirstCosts(t *testing.T) {
	for _, c := range []struct{ abs, marginal, extra int }{
		{600, 44, 1}, // the smallest N there is: two scripts, one copy apart
		{600, 44, 8},
		{368, 368, 3}, // pays nothing once, so a copy costs the same either way
		{1000, 20, 5}, // pays almost all of it once
		{412, 1, 16},  // and a marginal cost of a single byte
	} {
		b := affine(t, 474, c.abs, c.marginal, 0)
		flags.Extra = c.extra
		t.Cleanup(func() { flags.Extra = 4 })

		var r Results
		size, _, _, marginal, ok := oneMode(b, &r)
		if size != c.abs {
			t.Errorf("abs %d marginal %d extra %d: Size %d, want %d",
				c.abs, c.marginal, c.extra, size, c.abs)
		}
		if !ok {
			t.Errorf("abs %d marginal %d extra %d: no marginal cost was measured",
				c.abs, c.marginal, c.extra)
			continue
		}
		if marginal != float64(c.marginal) {
			t.Errorf("abs %d marginal %d extra %d: Marginal %g, want %d",
				c.abs, c.marginal, c.extra, marginal, c.marginal)
		}
		// And the split.  What one copy costs outright is the shared
		// part plus one marginal copy, so the shared part is the
		// difference -- a number nothing else in this program reports.
		if shared, want := size-int(marginal), c.abs-c.marginal; shared != want {
			t.Errorf("abs %d marginal %d: Shared %d, want %d",
				c.abs, c.marginal, shared, want)
		}
	}
}

// TestExtraCostsScriptsAndNotRounds is why --extra is worth having at
// all rather than running the benchmark twice.
//
// The two searches are anchored at the same padding and narrow in
// lockstep, so the second rides in the first's rounds.  It spends
// scripts -- a script is a script -- and no time.
func TestExtraCostsScriptsAndNotRounds(t *testing.T) {
	measure := func(extra int) (rounds, runs int) {
		searchesAtOnce = 1
		if extra > 0 {
			searchesAtOnce = 2
		}
		flags.Parts = 8
		b := offlineWith(t, scripttest.Options{
			Memory:    scripttest.Memory{Pad: 474, CodeSize: 600, Marginal: 44},
			GroupSize: leaseSize(),
		})
		// After offlineWith, which puts --extra back to nought.
		flags.Extra = extra
		t.Cleanup(func() {
			flags.Extra, flags.Parts, searchesAtOnce = 4, 8, 1
		})
		spentRounds, spentRuns = 0, 0
		var r Results
		oneMode(b, &r)
		return spentRounds, spentRuns
	}

	alone, aloneRuns := measure(0)
	with, withRuns := measure(8)

	if with != alone {
		t.Errorf("--extra took %d rounds against %d without it; the second search "+
			"is meant to ride in the first's rounds", with, alone)
	}
	if withRuns <= aloneRuns {
		t.Errorf("--extra spent %d runs against %d: a second search is a second "+
			"script at every pad and cannot be free in scripts", withRuns, aloneRuns)
	}
}
