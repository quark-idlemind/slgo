package scripttest_test

// An slbench-shaped measurement, driven through the contract.
//
// This is the demonstration the backend exists for.  slbench's
// measurement machinery -- find the pad at which the base script sits on
// a block boundary, probe upward for a copy count whose memory delta
// registers, estimate how many copies fit in the memory a script has,
// back off when the answer is too big -- is arithmetic on readings, and
// until now the only way to exercise it offline was --test, which
// answers ABOVE the transport and so tests none of it.
//
// The search below is a small reimplementation rather than slbench's
// own code, on purpose.  Sharing the code would prove only that it
// agrees with itself; writing it again against nothing but the gRPC
// contract is what shows the contract carries what a measurement needs:
// a lease held for the whole benchmark, an object that keeps its base
// reading between runs, a compiler that refuses, and a fault that can be
// told to be an out-of-memory one.
//
// Every run here is a real Run call over a real stream.  The numbers are
// the model's and mean nothing about Second Life; what is being checked
// is that the ALGORITHM comes out where the model says it should.

import (
	"context"
	"fmt"
	"io"
	"math"
	"math/bits"
	"strconv"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/proto/scriptv1"
	"github.com/quark-idlemind/slgo/scripttest"
)

// The constants the search works in, which are slbench's.
const (
	blockSize = 512
	minpad    = 5
)

// results is what a run reported, in the labels the benchmark script
// uses.  A cnt=0 script reports BASE_MEM and a cnt>0 script reports
// TEST_MEM and SIZE -- the asymmetry is the harness's and the search
// depends on it.
// results is one reading and what this search makes of it.
//
// The script says the reading and nothing else; size is worked out here,
// as it is in cmd/slbench.  It used to come back from the script,
// which divided against a base its object held in linkset data -- so
// this had to model that too, and did.
type results struct {
	mem  int
	size float64
}

// refused is Second Life's compiler saying no, and collided is a script
// that compiled and then ran out of memory.  They are separate types
// because the search does different things about them at the bottom of
// its back-off: a collision at one copy is still a size limit, while a
// refusal at one copy means the code itself will not compile.
type refused struct{ errs []string }

func (e *refused) Error() string { return "will not compile: " + strings.Join(e.errs, "; ") }

type collided struct{ reason string }

func (e *collided) Error() string { return "out of memory: " + e.reason }

// bench is the caller: a lease, an object, and a cache.
type bench struct {
	t      *testing.T
	c      scriptv1.RunnerClient
	target string

	// cache stops the same script being sent twice, which is what makes
	// the run count below a real cost and not an artefact.
	cache map[[2]int]results
	runs  int
}

func newBench(t *testing.T, o scripttest.Options) (*bench, func()) {
	t.Helper()
	_, c := serve(t, overAPipe, o)
	// One lease for the whole benchmark, because the base reading
	// travels from the cnt=0 script to the cnt>0 ones inside the object.
	// A second caller in there would be read as our own base.
	g, done := lease(t, c, &scriptv1.LeaseRequest{Who: "slbench copy-count"})
	return &bench{
		t: t, c: c, target: g.GetTargets()[0].GetId(),
		cache: map[[2]int]results{},
	}, done
}

// run sends one script and reads back what it said.
func (b *bench) run(cnt, pad int) (results, error) {
	b.t.Helper()
	key := [2]int{cnt, pad}
	if r, ok := b.cache[key]; ok {
		return r, nil
	}
	b.runs++

	stream, err := b.c.Run(context.Background(), &scriptv1.RunRequest{
		Target: b.target, Name: "slbench", Source: benchScript(cnt, pad),
		Done: "DONE", TimeoutSeconds: 5,
	})
	if err != nil {
		b.t.Fatalf("Run: %v", err)
	}

	var r results
	var fault error
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			b.t.Fatalf("Run: %v", err)
		}
		switch {
		case ev.GetCompiled() != nil && !ev.GetCompiled().GetOk():
			return results{}, &refused{errs: ev.GetCompiled().GetErrors()}
		case ev.GetFault() != nil:
			f := ev.GetFault()
			if f.GetOutOfMemory() {
				fault = &collided{reason: f.GetReason()}
				continue
			}
			fault = fmt.Errorf("run-time error: %s", f.GetReason())
		case ev.GetLine() != nil:
			absorb(ev.GetLine().GetText(), &r)
		}
	}
	if fault != nil {
		return results{}, fault
	}
	b.cache[key] = r
	return r, nil
}

// absorb reads a RESULT: line into the readings.  The convention belongs
// to the benchmark and not to the backend: nothing about running a
// script requires a script to label its output.
func absorb(line string, r *results) {
	i := strings.Index(line, "RESULT:")
	if i < 0 {
		return
	}
	s := strings.TrimSpace(line[i+len("RESULT:"):])
	if strings.HasPrefix(s, "MEM=") {
		r.mem, _ = strconv.Atoi(s[len("MEM="):])
	}
}

// mustRun is for the places where a failure is not something the search
// can do anything about.
func (b *bench) mustRun(cnt, pad int) results {
	b.t.Helper()
	r, err := b.run(cnt, pad)
	if err != nil {
		b.t.Fatalf("%d copies at pad %d: %v", cnt, pad, err)
	}
	return r
}

// shrink runs cnt copies, halving until the script fits or one copy is
// left.  Both ways of being too big halve, because a smaller count is
// the thing to try either way -- but they are different limits, and a
// backend that could not tell them apart would leave this untestable.
func (b *bench) shrink(cnt, pad int, base int) (int, results) {
	b.t.Helper()
	for {
		r, err := b.run(cnt, pad)
		if err == nil {
			// What a copy costs, which the script used to work out and
			// this now does: the reading against the base, over the
			// copies between them.
			if cnt > 0 {
				r.size = float64(r.mem-base) / float64(cnt)
			}
			return cnt, r
		}
		if cnt <= 1 {
			b.t.Fatalf("one copy at pad %d: %v", pad, err)
		}
		switch err.(type) {
		case *collided, *refused:
			cnt /= 2
		default:
			b.t.Fatalf("%d copies at pad %d: %v", cnt, pad, err)
		}
	}
}

// padding finds the pad at which the base script first tips into the
// next block: the memory reading is a staircase in the padding, and this
// is where the step falls.
//
// A bisection, which is exact here because the reading only ever grows
// with the pad.  slbench walks linearly from where its bisection left
// off and then confirms the answer twice, because live an instrument can
// answer differently to the same question; that is a property of
// llGetUsedMemory rather than of the search, and this model does not
// have it.
func (b *bench) padding() int {
	b.t.Helper()
	base := b.mustRun(0, minpad).mem
	lo, hi := minpad, minpad+blockSize
	if b.mustRun(0, hi).mem == base {
		b.t.Fatalf("the reading did not grow across a whole block from pad %d", minpad)
	}
	for lo+1 < hi {
		mid := (lo + hi) / 2
		if b.mustRun(0, mid).mem > base {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

// copyCount is the rest of copy mode: settle the base, probe upward for
// a count whose delta registers, estimate how many copies fit in the
// memory a script has, and back off if that was too many.
func (b *bench) copyCount(pad, max int) (cnt int, size float64) {
	b.t.Helper()

	// A cache hit is as good as a run: the base is a number here now
	// rather than something left inside the object, so nothing depends
	// on which cnt=0 script executed last.  It did, and the entry was
	// dropped rather than trusted.
	base := b.mustRun(0, pad).mem

	// Probe upward until the difference between two readings registers.
	// Eight copies to start, because one copy of a small construct can
	// sit inside the same block as the base and read as nothing at all.
	d := min(8, max)
	capped := false
	var r results
	for {
		used, got := b.shrink(d, pad, base)
		r = got
		if used < d {
			capped, d = true, used
		}
		if r.size != 0 || capped || d >= max {
			break
		}
		d *= 2
	}

	switch {
	case capped:
		cnt = d
	case r.size == 0:
		cnt = blockSize
	default:
		// How many copies fit in what a script has, rounded down to a
		// power of two.  The half-block term is the quantisation the
		// probe's own reading carries: without it the estimate is
		// systematically high and the shrink below pays for it.
		maxMem := 62*1024 - base
		per := int(r.size) + blockSize/(2*d)
		if maxMem <= 0 || per <= 0 || maxMem < per {
			b.t.Fatalf("nothing fits: %d bytes free, %d per copy", maxMem, per)
		}
		cnt = 1 << (bits.Len(uint(maxMem/per)) - 1)
	}
	if cnt > max {
		cnt = max
	}

	cnt, r = b.shrink(cnt, pad, base)
	return cnt, r.size
}

// TestACopyCountSearchRunsThroughTheBackendAndLandsWhereTheModelSays is
// the demonstration: a measurement written against nothing but the gRPC
// contract, finding the model's own numbers.
//
// The model is the live string-literal case, which is the one worth
// using here: 1044 bytes for the first copy and 542 for each after it,
// measured on a 250-character literal, because identical literals are
// shared and an extra copy pays only for what it cannot share.  A search
// that could not tell the two apart would still pass against a model
// where they were equal, so this is the shape that has something to say.
func TestACopyCountSearchRunsThroughTheBackendAndLandsWhereTheModelSays(t *testing.T) {
	mem := scripttest.Memory{Pad: 137, CodeSize: 1044, Marginal: 542}
	b, done := newBench(t, scripttest.Options{Memory: mem})
	defer done()

	pad := b.padding()
	if pad != mem.Pad {
		t.Fatalf("the search found the boundary at pad %d, want the %d the model put it at", pad, mem.Pad)
	}

	cnt, size := b.copyCount(pad, 512)
	if cnt < 8 || cnt&(cnt-1) != 0 {
		t.Errorf("measured at %d copies, want a power of two of at least 8", cnt)
	}

	// What the reading can resolve.  Size is a difference of two
	// quantised readings over the count, so it carries blockSize/cnt of
	// quantisation; and it is the AVERAGE cost of a copy, which is the
	// marginal cost plus what the first copy paid once, spread over the
	// count.  Anything inside that band is the right answer, and a
	// tighter claim would be a claim about the instrument.
	want := float64(mem.Marginal)
	band := float64(mem.CodeSize-mem.Marginal+blockSize) / float64(cnt)
	if math.Abs(size-want) > band {
		t.Errorf("Size = %.1f, want %.1f ± %.1f at %d copies", size, want, band, cnt)
	}

	// And what it cost.  A run is an upload, a compile, an execution and
	// a wait for the script to speak, and everything else a benchmark
	// does is free beside it -- so this is the number that decides
	// whether a change to the search is worth having.  The bound is
	// loose enough not to be a tripwire and tight enough that doubling
	// the cost fails here rather than in world twenty minutes later.
	t.Logf("the search spent %d runs", b.runs)
	if b.runs > 20 {
		t.Errorf("the search spent %d runs, want no more than 20", b.runs)
	}
}

// TestTheSearchBacksOffFromAScriptThatIsTooBig covers the two ways a
// script can be too big, which are not the same limit: above one the
// compiler refuses it outright, and above the other it compiles and
// collides stack with heap the moment it runs.  Either way the answer is
// fewer copies, and a search that does not back off at all stops with no
// measurement.
func TestTheSearchBacksOffFromAScriptThatIsTooBig(t *testing.T) {
	for _, x := range []struct {
		name string
		mem  scripttest.Memory
	}{
		{"the compiler refuses it", scripttest.Memory{
			Pad: 137, CodeSize: 1044, Marginal: 542, Limit: 20000,
		}},
		{"it compiles and runs out of memory", scripttest.Memory{
			Pad: 137, CodeSize: 1044, Marginal: 542, Collide: 20000,
		}},
	} {
		t.Run(x.name, func(t *testing.T) {
			b, done := newBench(t, scripttest.Options{Memory: x.mem})
			defer done()

			pad := b.padding()
			cnt, size := b.copyCount(pad, 512)

			// It landed under the limit rather than at the estimate.
			if r := x.mem.Reading(cnt, pad); r > 20000 {
				t.Errorf("measured at %d copies, which the model says is %d bytes and over the limit", cnt, r)
			}
			if cnt >= 64 {
				t.Errorf("measured at %d copies; the estimate was too big and should have been halved", cnt)
			}
			band := float64(x.mem.CodeSize-x.mem.Marginal+blockSize) / float64(cnt)
			if math.Abs(size-float64(x.mem.Marginal)) > band {
				t.Errorf("Size = %.1f, want %.1f ± %.1f at %d copies",
					size, float64(x.mem.Marginal), band, cnt)
			}
		})
	}
}
