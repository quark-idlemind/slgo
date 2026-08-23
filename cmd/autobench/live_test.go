package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/internal/session"
)

// Live tests need a grid and a session to reach it with, so they are opt-in:
//
//	SLGO_LIVE=1 go test -count=1 ./cmd/autobench/ -run Live -v
//
// SLGO_ADDR overrides the slgod address, and SLGO_OBJECT names an object to run
// in rather than rezzing one.
//
// What they are for is the one thing the offline model cannot express: SL's own
// compiler.  The model has no compiler and no size limit, so every claim about
// what Second Life will and will not accept has to be made here or not at all.
//
// The timings recorded in the comments below were measured through slrund and a
// viewer, which is not this transport.  They are kept as the questions they
// answered, not as figures to compare a run of this against.
func liveBench(t *testing.T) *runner {
	t.Helper()
	if os.Getenv("SLGO_LIVE") == "" {
		t.Skip("set SLGO_LIVE=1 to run against a live grid")
	}
	addr := os.Getenv("SLGO_ADDR")
	if addr == "" {
		addr = "localhost:7807"
	}

	ctx := context.Background()
	s, err := session.Connect(ctx, session.Options{Addr: addr, Channel: "autobench"})
	if err != nil {
		t.Fatal(err)
	}
	obj, cleanup, err := session.RunIn(ctx, s, os.Getenv("SLGO_OBJECT"), false)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	b := &runner{
		places:  []place{{s, obj}},
		cleanup: func() { cleanup(); s.Close() },
		Timeout: 2 * time.Minute,
	}
	t.Cleanup(func() { b.Close() })
	return b
}

// TestLiveCompileIsNotRunning is the first pass at A9's measurement, kept
// because it is the figure two later corrections were about: what a compile
// costs against what a run costs, for the same script.
//
// Read it with what came after.  Each call here opens its own session AND
// creates its own script item, so every number is item creation plus upload --
// see TestLiveItemCreationIsTheFloor for the split and TestLiveLadderOnOneItem
// for the same ladder without it.  The one comparison it makes on equal footing
// is the one it was for: at 128 copies, compile 16.8s against run 17.5s, both
// creating their item, so running a script is the 0.7s after installing it.
//
// It also records what SL SAYS about a script it will not take, which nothing
// else in this repository has ever captured.
func TestLiveCompileIsNotRunning(t *testing.T) {
	b := liveBench(t)
	flags.Code = "foo_CNT(){llDie();}"
	flags.Preamble, flags.Postamble = "", ""

	for _, cnt := range []int{1, 128, 256, 512} {
		src := buildScript(cnt, 474)
		start := time.Now()
		c, err := b.Compile(src)
		took := time.Since(start)
		if err != nil {
			t.Fatalf("%d copies: could not ask: %v", cnt, err)
		}
		t.Logf("%d copies (%d bytes of source): compiled=%v in %v", cnt, len(src), c.OK, took.Round(time.Millisecond))
		if !c.OK {
			for _, e := range c.Errors {
				t.Logf("%d copies: SL said: %q", cnt, e)
			}
		}
		if cnt == 1 && !c.OK {
			t.Errorf("one copy of the reference shape must compile; SL said %q", c.Error())
		}
	}

	// The same script, run.  This is the number the compile times above are to
	// be read against.
	src := buildScript(128, 474)
	start := time.Now()
	_, _, err := b.Send(src)
	t.Logf("128 copies RUN in %v (err %v)", time.Since(start).Round(time.Millisecond), err)
}

// TestLiveReadingIsStable asks the same script for the same number, many times,
// at several pads.
//
// It exists because two identical -1 runs of the reference shape disagreed on
// 2026-08-03: at 20:01 the one-copy script crossed into the next block at pad
// 602 and Size came out 384, and at 20:36 it crossed at 618 and Size came out
// 368, which is the published figure.  A bisection is only as good as the
// readings it bisects, and one bad reading decides where the crossing is found.
//
// So the question is how often llGetUsedMemory answers differently for a FIXED
// script.  It is asked at four pads, not one: 602 is where the two runs
// disagreed, 618 is the crossing they were arguing about, and 473/474 are the
// base padding and the pad the runs are taken at -- the reading every published
// Size is anchored to.  A fault at any of them moves an answer.
//
// It is a frequency measurement, so it reports the distribution whatever it
// finds; a pad that answered two different things is the failure.  N is
// SLGO_STABLE_N (default 10).  Set SLRUN_SLOT_OBJECT=Worn to run in the
// permanent slot, which is what makes 40 runs affordable -- an item that
// already exists is updated in 1.2s instead of created in 8.3s (A13).
func TestLiveReadingIsStable(t *testing.T) {
	b := liveBench(t)
	flags.Code = "foo_CNT(){llDie();}"
	flags.Preamble, flags.Postamble = "", ""

	n := 10
	if s := os.Getenv("SLGO_STABLE_N"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 1 {
			t.Fatalf("SLGO_STABLE_N=%q: want a positive count", s)
		}
		n = v
	}

	// cnt=1 throughout: the one-copy script is what the second search bisects,
	// and it is the script that disagreed.
	const cnt = 1
	total, odd := 0, 0
	for _, pad := range []int{473, 474, 602, 618} {
		src := buildScript(cnt, pad)
		seen := map[string]int{}
		for i := 0; i < n; i++ {
			results, _, err := b.Send(src)
			if err != nil {
				t.Fatalf("pad %d run %d: %v", pad, i, err)
			}
			var mem string
			for _, raw := range results {
				if s, ok := resultPayload(raw); ok && strings.HasPrefix(s, "TEST_MEM=") {
					mem = s[len("TEST_MEM="):]
				}
			}
			seen[mem]++
			total++
		}
		t.Logf("pad %4d: %d runs -> %v", pad, n, seen)
		if len(seen) != 1 {
			odd++
			t.Errorf("pad %d: the same script read %d different values: %v", pad, len(seen), seen)
		}
	}
	t.Logf("STABILITY %d readings over 4 pads, %d pad(s) disagreed with themselves", total, odd)
}

// trivialScript is as small as an LSL script gets.  Compiling it is essentially
// all fixed cost: find or make the item, upload, hear the verdict.  What it
// measures is what every per-script figure has added to it.
const trivialScript = `default{state_entry(){}}`

func rd(d time.Duration) time.Duration { return d.Round(10 * time.Millisecond) }

// Three tests that were here under slrund are not, because their subject was
// slrund: TestLiveCompileCostIsSetupPlusSize and its batching questions were
// about what a per-compile SESSION cost and what batching several compiles into
// one session saved.  There is no session per compile here -- the process holds
// one for its whole life -- so batching saves nothing and there is nothing left
// to measure.  What survived is the part that was never about the transport:
// item creation, below, and the readings themselves.

// TestLiveItemCreationIsTheFloor separates the two things every timing here
// otherwise adds together.
//
// sl.InstallScript looks for an item of the name it is given.  If there is not
// one it creates the script, puts it in the object, and Settles for up to six
// seconds waiting for the object to admit it is there.  If there IS one it
// sends a single UpdateScriptTask.  So the several seconds a 24-byte script
// costs the first time is not compilation -- it is making an item.
//
// The same source, four times, under one name: the first install creates the
// item and the rest update it, and the difference is that floor.
func TestLiveItemCreationIsTheFloor(t *testing.T) {
	b := liveBench(t)
	b.Timeout = 10 * time.Minute

	var first, last time.Duration
	for i := 0; i < 4; i++ {
		c, err := b.Compile(trivialScript)
		if err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
		what := "update"
		if i == 0 {
			what = "CREATE"
		}
		t.Logf("ITEM    install %d (%s): %v compiled=%v", i, what, rd(c.Elapsed), c.OK)
		if i == 0 {
			first = c.Elapsed
		}
		last = c.Elapsed
	}
	if last >= first {
		t.Errorf("reusing an item (%v) was not cheaper than creating one (%v); "+
			"either the name is not being reused or the cost is not item creation",
			last, first)
	}
}

// TestLiveLadderOnOneItem costs the halving search a compiler-driven copy count
// would walk, with item creation paid ONCE instead of ten times.
func TestLiveLadderOnOneItem(t *testing.T) {
	b := liveBench(t)
	b.Timeout = 10 * time.Minute
	flags.Code = "foo_CNT(){llDie();}"
	flags.Preamble, flags.Postamble = "", ""

	// A trivial script first, so the item exists and every figure after it is
	// an UPDATE.  Its own cost is the creation and is reported as such.
	c0, err := b.Compile(trivialScript)
	if err != nil {
		t.Fatalf("creating the item: %v", err)
	}
	t.Logf("ONEITEM create  (%6d bytes): %v", len(trivialScript), rd(c0.Elapsed))

	counts := []int{512, 256, 128, 64, 32, 16, 8, 4, 2, 1}
	var total, search time.Duration
	for _, cnt := range counts {
		src := buildScript(cnt, 474)
		c, err := b.Compile(src)
		if err != nil {
			t.Fatalf("%d copies: %v", cnt, err)
		}
		total += c.Elapsed
		if cnt == 512 || cnt == 256 || cnt == 128 {
			search += c.Elapsed
		}
		t.Logf("ONEITEM %4d copies (%6d bytes): %v compiled=%v %v",
			cnt, len(src), rd(c.Elapsed), c.OK, c.Errors)
	}
	t.Logf("ONEITEM the whole ladder costs %v with the item already there", rd(total))
	t.Logf("ONEITEM the 512/256/128 search costs %v with the item already there", rd(search))
}
