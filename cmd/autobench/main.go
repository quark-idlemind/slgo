// Command autobench measures the memory cost of LSL constructs.
//
// It is the slgo build of the slrun program of the same name: same
// measurements, same output, but scripts go into Second Life through sl rather
// than through slrund. The measurement machinery -- the padding search, the
// block-boundary arithmetic, the shrink-on-overflow retry -- is unchanged,
// because it is about LSL and not about how a script reaches the grid.
//
// # The transport
//
// One session, one object, every run: a prim is rezzed beside the avatar and
// each script replaces the last inside it, unless --object names an object
// already in the region. See runner.go.
//
// Two things follow that are worth knowing before reading a number off this:
//
//   - The DONE contract is enforced HERE. Nothing on the far side decides a
//     script has finished, so the wait for the sentinel, and the timeout on not
//     getting one, are this program's. --timeout is that bound.
//   - Every run of one benchmark must happen in the same object, because the
//     base reading travels from the cnt=0 script to the cnt>0 scripts through
//     the object's LINKSET DATA. Holding one object for the life of the process
//     is what makes that true here; under slrund it was a promise the server
//     made and the client had to check.
//
// Flags that are gone, and where they went:
//
//	--sim                the eLSL simulator, which lives in elsl
//	--lsl, --chatlog     the viewer slot file and chat log, from before slrund
//	--runscript          slrund's slot transport; there is one transport now
//	--slot, --prim       named a slot in somebody's object; --object names one
package main

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pborman/getopt/v2"
	"github.com/pborman/options"

	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/scripttest"
	"github.com/quark-idlemind/slgo/sl"
)

var flags = struct {
	Preamble  string        `getopt:"--preamble=PREAMBLE Make STR the test's preamble"`
	Postamble string        `getopt:"--postamble=POSTAMBLE Make STR the test's postamble"`
	Code      string        `getopt:"--code=CODE code to test"`
	Statement string        `getopt:"--statement=CODE statement(s) to test"`
	Pad       string        `getopt:"--pad=PAD padding instructions"`
	Addr      string        `getopt:"--addr=HOST:PORT the slgod to attach to; default sl-host, or this machine"`
	Agent     string        `getopt:"--agent=NAME -a the profile to use; the only one, by default"`
	Direct    bool          `getopt:"--direct -d log in to Second Life directly, without slgod"`
	First     string        `getopt:"--first=NAME the avatar's first name, for --direct"`
	Last      string        `getopt:"--last=NAME the avatar's last name, for --direct"`
	Start     string        `getopt:"--start=WHERE where to arrive: last, home, or a region, for --direct"`
	Object    string        `getopt:"--object=NAME run in this object, instead of the shared one"`
	Rez       bool          `getopt:"--rez rez a throwaway prim instead of using the shared auto object"`
	Keep      bool          `getopt:"--keep leave the rezzed object behind"`
	Show      bool          `getopt:"-v show the source before each execution"`
	Title     string        `getopt:"--title=NAME name of benchmark"`
	Params    []string      `getopt:"--params=NAME,... parameters used with --statement"`
	Locals    []string      `getopt:"--locals=NAME,... locals used with --statement"`
	Globals   []string      `getopt:"--globals=NAME,... declare globals"`
	One       bool          `getopt:"-1 Using padding and a single copy of CODE"`
	IPad      int           `getopt:"--ipad=N the base padding, as reported by an earlier Padding: line"`
	ICheck    bool          `getopt:"--check-ipad spend two runs confirming --ipad before using it"`
	Max       int           `getopt:"--max=N maximum number of copies"`
	Fast      bool          `getopt:"--fast skip looking for pad"`
	Debug     bool          `getopt:"--debug enable debugging"`
	Probe     bool          `getopt:"--probe send a simple script to LSL as a probe"`
	NoCache   bool          `getopt:"--no-cache do not remember or reuse the padding for this base script"`
	Objects   int           `getopt:"--objects=N how many objects to take readings in at once"`
	Timeout   time.Duration `getopt:"--timeout=DUR timeout on waiting for an LSL script to complete"`
	Test      string        `getopt:"--test=PAD,SIZE[,MARGINAL[,LIMIT]] measure against the offline model in this process, see the source code"`
	Backend   string        `getopt:"--backend=HOST:PORT run scripts through a script.v1 backend there -- a simulator or a viewer daemon -- instead of in Second Life; --test is the same contract answered by a model here"`
	Help      bool          `getopt:"--help -h show this message"`
}{
	Start:   "last",
	Timeout: time.Minute,
	Max:     512,
	// Four: one to measure in and three to take readings in.  Three is
	// what quarters a range, and quartering is what turns the nine
	// rounds of a bisection into four or five.
	Objects: 4,
}

// testModel reads --test=PAD,SIZE[,MARGINAL[,LIMIT]] into the model the
// offline backend answers from.
//
// The fields are scripttest's, and the reasoning for each of them lives
// there beside the arithmetic that uses them.  What is worth saying here
// is what the flag is FOR: the measurement machinery -- the padding
// search, the block arithmetic, the copy count, the backing off -- is
// about LSL and about readings, and none of it needs Second Life to be
// exercised.  This is how it is exercised without one.
//
// It describes a model and not a place, which is why it takes no address:
// --backend is for pointing this program at something that runs scripts
// somewhere else.
func testModel(spec string) scripttest.Memory {
	const usage = "Usage: --test=PAD,SIZE[,MARGINAL[,LIMIT]]\n"
	f := strings.Split(spec, ",")
	if len(f) < 2 || len(f) > 4 {
		errf(usage)
	}
	n := make([]int, len(f))
	for i, s := range f {
		v, err := strconv.Atoi(s)
		if err != nil {
			errf(usage)
		}
		n[i] = v
	}
	// SIZE is what the first copy costs and MARGINAL what each one after
	// it costs; leaving MARGINAL out makes them equal, which is a
	// construct that pays nothing once and shares nothing.  LIMIT left out
	// is scripttest's default, which is the 64KB a Mono script has.
	m := scripttest.Memory{Pad: n[0], CodeSize: n[1]}
	if len(n) >= 3 {
		m.Marginal = n[2]
	}
	if len(n) == 4 {
		m.Limit = n[3]
	}
	return m
}

func errf(format string, v ...any) {
	fmt.Fprintf(os.Stderr, format, v...)
	os.Exit(1)
}

func debugf(format string, v ...any) {
	if flags.Debug {
		fmt.Fprintf(os.Stderr, format, v...)
	}
}

const plusminus = "±"
const blockSize = int(512)
const minpad = 5

// findPadding returns an OFFSET FROM pad, not a pad: the largest offset at
// which cnt copies of the code under test still fit inside the 512-byte block
// they occupy at pad.  One more byte crosses, so pad+findPadding(...)+1 is where
// memory first grows.  Both callers want that +1 and both add it themselves --
// which is where the one byte between the padding autobench names and the pad it
// runs at comes from.
//
// It also returns the reading at pad, which is the bottom of the step it found.
// The caller cannot keep that reading itself: every run overwrites r wholesale,
// and confirming the crossing re-runs the base script, so a value read before
// the search can be stale by the time it returns.  See oneMode.
//
// It costs about a dozen runs: a binary search across the block, then a linear
// walk from where the search left off so the answer is exact to the byte, then
// two or three more confirming the answer -- see confirmCrossing for why those
// are worth paying for.
//
// Every run writes r, so on return r holds the reading from the crossing run --
// one block above the reading at pad, which is the step -1 mode measures.
func findPadding(b backend, cnt, pad int, r *Results) (offset, base int) {
	debugf("FindPadding\n")
	defer debugf("FindPadding Done\n")

	getBase := func() int { return r.Base }
	if cnt > 0 {
		getBase = func() int { return r.Test }
	}
	// A search whose base turns out to have been misread is run again from the
	// corrected one.  The retries are cheap -- every reading it took is in the
	// cache, and the one that moved has been corrected there -- but they are
	// bounded anyway: a reading that will not settle is a broken instrument, and
	// the honest answer to that is to stop rather than to keep asking.
	for attempt := 0; ; attempt++ {
		offset, base, ok := searchPadding(b, cnt, pad, r, getBase)
		if ok {
			return offset, base
		}
		if attempt == 2 {
			errf("the %d-copy script at pad %d read %d bytes on the last of %d attempts "+
				"and something else on the ones before; llGetUsedMemory is not answering "+
				"the same thing twice, and every size autobench reports is a difference "+
				"of two of its answers\n", cnt, pad, base, attempt+1)
		}
		debugf("FindPadding restart %d: base is now %d\n", attempt+1, base)
	}
}

// quarterSearch narrows the range by asking three pads at once instead
// of one at a time.
//
// A bisection halves the range per round and spends one reading doing
// it.  This quarters it and spends three -- and three readings taken
// together cost about what one costs, since they are three round trips
// in flight rather than three in a row.  Nine rounds become four or
// five.
//
// The three answers are read in order, which is the whole of the logic:
// the first pad whose memory has grown is above the crossing and puts
// the ceiling there, and everything below the last pad that has NOT
// grown is settled.
//
// It stops while the range is still wider than the probes can usefully
// split.  Below that the quarters collide -- at a range of one they are
// all the same pad, and a round that learns nothing would repeat for
// ever -- so the last few bytes go to the bisection, which is one or
// two more rounds and has the walk and the confirmation after it.
//
// Never in the measured object: see probe.go for why that is safe for a
// script with copies in it as well as for the base.
func quarterSearch(b backend, cnt, pad, base, low, high int) (int, int) {
	if b == nil || b.Spares() < 3 {
		return low, high
	}

	for high-low >= 8 {
		q := (high - low) / 4
		p := []int{low + q, low + 2*q, low + 3*q}

		mem := probeAt(b, cnt, []int{p[0] + pad, p[1] + pad, p[2] + pad})

		switch {
		case mem[0] > base:
			high = p[0]
		case mem[1] > base:
			low, high = p[0], p[1]
		case mem[2] > base:
			low, high = p[1], p[2]
		default:
			low = p[2]
		}
		debugf("Quarter[%d] %d < ... < %d\n", cnt, low, high)
	}
	return low, high
}

// searchPadding is one attempt at findPadding: bisect the block, walk to the
// exact byte, confirm.  ok is false when the confirmation found the base itself
// to have been misread, in which case base is the corrected reading and the
// whole search has to be redone against it -- every comparison it made was
// against the wrong number.
func searchPadding(b backend, cnt, pad int, r *Results, getBase func() int) (offset, base int, ok bool) {
	mustRun(b, cnt, pad, r)
	base = getBase()
	debugf("Base[%d] %d : %d\n", cnt, pad, base)

	var mid int
	low := 0
	high := blockSize

	// Quarter the range while there is enough of it to quarter, asking
	// three pads at once.  What is left is bisected below.
	low, high = quarterSearch(b, cnt, pad, base, low, high)

	for high-low > 1 {
		mid = low + (high-low)/2
		mustRun(b, cnt, mid+pad, r)
		debugf("Check[%d] %d < %d < %d + %d : %d\n", cnt, low, mid, high, pad, getBase())
		if base == getBase() {
			low = mid
		} else {
			high = mid
		}
	}
	for {
		if low > blockSize {
			// Memory moves a block at a time, so a crossing has to lie within
			// one block of any pad; walking past that means these readings are
			// not a staircase.  By far the likeliest reason is that base was
			// misread -- a base one block HIGH reads as "inside the block" for
			// every pad above the real crossing, and the walk chases the next
			// boundary up -- so ask it again before giving up.
			//
			// The bound is what keeps that cheap.  Without it the walk is a run
			// per byte until it meets the boundary the bogus base named, which
			// offline is instant and live is hundreds of scripts.
			if nb := reread(b, cnt, pad, r, getBase); nb != base {
				noticef("the %d-copy script at pad %d read %d at the start of the "+
					"search and %d now; no crossing was found in the %d bytes above "+
					"it, which is what a base read one block out does\n",
					cnt, pad, base, nb, blockSize)
				return 0, nb, false
			}
			errf("no crossing found in the %d bytes above pad %d for %d copies, "+
				"and the base still reads %d; memory should grow within one block "+
				"of any pad\n", blockSize, pad, cnt, base)
		}
		mustRun(b, cnt, low+pad, r)
		debugf("Got[%d] %d: %d\n", cnt, low, getBase())
		if base != getBase() {
			switch verdict, nb := confirmCrossing(b, cnt, pad, low, base, r, getBase); verdict {
			case crossingHolds:
				// Leave r holding the crossing reading, which is what the
				// caller measures the step with.  Free: confirmCrossing put
				// that reading in the cache.
				mustRun(b, cnt, low+pad, r)
				return low - 1, base, true
			case crossingLater:
				low++
				continue
			default: // crossingSuspect
				return 0, nb, false
			}
		}
		low++
	}
}

// crossing is what confirmCrossing made of the step the search just found.
type crossing int

const (
	// crossingHolds: read again, the step is still there.  Take it.
	crossingHolds crossing = iota
	// crossingLater: the pad that looked like it crossed reads inside the
	// block on a second ask.  The first reading was noise; keep walking.
	crossingLater
	// crossingSuspect: something the search had already settled moved under
	// it -- the base, or a pad it had accepted as inside the block.  Every
	// comparison since is suspect; search again.
	crossingSuspect
)

// confirmCrossing re-reads the step the search is about to turn on, and says
// whether it is still there.
//
// This is the whole of A12.  A padding search is a chain of comparisons against
// readings of llGetUsedMemory, and on 2026-08-03 one of those readings came back
// exactly one block high: the one-copy reference script at pad 602 read 6436
// where it reads 5924 every other time it has been asked.  Nothing about that is
// visible from inside the search.  It looks precisely like the memory having
// grown, which is the event the search exists to find, so the search believed it
// -- and the run cache then served the same wrong reading back for the rest of
// the walk, so it never got a second chance.  The published Size moved 16 bytes,
// twice, with nothing in the output to say why.
//
// A crossing is a PAIR of readings, so confirming it means confirming both ends
// and the base they are compared against:
//
//	pad+low-1  must still read the same as the base   (inside the block)
//	pad+low    must still read differently            (outside it)
//	pad        must still read what it read           (the base itself)
//
// Two or three runs, against the dozen the search already spent and the twenty
// a benchmark spends.  What it buys is that a single bad reading costs a re-read
// instead of an answer.
//
// It cannot make the instrument reliable -- two identical wrong readings still
// agree with each other -- and it is not meant to.  It turns a silent wrong
// answer into either a right one or a loud complaint, which is the difference
// that matters for a number nobody can check afterwards.
func confirmCrossing(b backend, cnt, pad, low, base int, r *Results, getBase func() int) (crossing, int) {
	first := getBase()
	// The reading that looked like the crossing.
	if again := reread(b, cnt, low+pad, r, getBase); again == base {
		noticef("the %d-copy script at pad %d read %d, and %d when asked again; "+
			"%d is what the pads around it read, so the first answer was noise "+
			"and the search continues past it\n", cnt, low+pad, first, again, base)
		return crossingLater, base
	} else if again != first {
		noticef("the %d-copy script at pad %d read %d and then %d; both say memory "+
			"grew from %d, so the crossing is here, but the readings themselves "+
			"do not agree\n", cnt, low+pad, first, again, base)
	}
	// The base every one of those comparisons was made against.
	if nb := reread(b, cnt, pad, r, getBase); nb != base {
		noticef("the %d-copy script at pad %d read %d at the start of the search and "+
			"%d now; that is the base every comparison was made against, so the "+
			"search is being run again from %d\n", cnt, pad, base, nb, nb)
		return crossingSuspect, nb
	}
	// The pad below has to be INSIDE the block.  If it is not, the crossing is
	// somewhere below and the walk was above it the whole time -- which is what
	// a reading that is spuriously LOW does, and 512 low is as plausible as the
	// 512 high that was actually seen.  low == 1 needs no run: pad+0 is the base
	// script, just read.
	if low > 1 {
		if below := reread(b, cnt, low-1+pad, r, getBase); below != base {
			noticef("the %d-copy script at pad %d was read as inside the block and "+
				"now reads %d against a base of %d; the crossing is below this, so "+
				"the search is being run again\n", cnt, low-1+pad, below, base)
			return crossingSuspect, base
		}
	}
	return crossingHolds, base
}

// reread runs (cnt, pad) again and returns the reading, going around the cache
// so that a second opinion is a second RUN.  Serving it from the cache would
// return the reading being questioned, which is not an opinion at all.
//
// The reading it gets replaces the cached one, so a run that has been corrected
// stays corrected for the rest of the benchmark.
func reread(b backend, cnt, pad int, r *Results, getBase func() int) int {
	delete(cache, Cache{Count: cnt, Padding: pad})
	spentRereads++
	mustRun(b, cnt, pad, r)
	debugf("Reread[%d] %d: %d\n", cnt, pad, getBase())
	return getBase()
}

// noticef reports something a caller reading a Size off stdout would otherwise
// never learn: a reading that was not reproducible, and what was done about it.
//
// It is unconditional, unlike debugf.  The whole complaint in A12 is that this
// class of event was silent, and a benchmark that had to argue with its
// instrument should say so whether or not anybody asked for --debug.  stderr,
// so that a caller parsing stdout is unaffected.
func noticef(format string, v ...any) {
	fmt.Fprintf(os.Stderr, "autobench: "+format, v...)
}

// basePadding returns the base padding: the largest pad the copy-free base
// script can carry and still fit inside its 512-byte memory block, so that one
// more byte crosses into the next.  Everything autobench measures is anchored
// to it, it is what the Padding: line reports, and it is what --ipad and
// --check-ipad name.
//
// It is a property of the benchmark's *shape* -- the harness plus --preamble,
// --postamble and --globals -- and not of the code under test, so every
// benchmark built on the same shape has the same base padding.  Measuring it
// costs about a dozen live runs, which is why --ipad exists: measure it once,
// read it off the Padding: line, and hand it to every later run of that shape.
// That is the flag's purpose and the reason it skips the search.
//
// This is the padding autobench *names*.  The pad it *runs* at is one byte
// more; both callers add that themselves.
func basePadding(b backend, r *Results) int {
	// --ipad is an assertion, not a hint: the caller has run this shape before
	// and is telling us what it measured.  Take it.  Verification costs two
	// live runs and is available on request (--check-ipad), but it is the
	// caller's call to spend them, not ours to spend on their behalf.
	if flags.IPad != 0 {
		if flags.ICheck {
			checkIPad(b, flags.IPad)
		}
		return flags.IPad
	}

	// A padding already found for this base script is worth two runs to
	// confirm and saves about a dozen.  It is confirmed rather than trusted:
	// what SL's compiler does can change, and a padding that is wrong by k
	// reports every Size wrong by k with nothing in the output to show it.
	//
	// Only for readings that are Second Life's.  The file is read by every
	// later benchmark on this account, and a model's paddings are
	// arithmetic while a simulator's are its own -- either would poison it
	// with numbers that never came from the grid.  The backend is asked
	// rather than the flags: the question is about where the readings came
	// from, which is the transport's to answer and not this program's to
	// infer from how it was invoked.
	key := baseKey()
	if !flags.NoCache && b.Grid() {
		if e, ok := loadPadCache()[key]; ok {
			if held, at, above := paddingHolds(b, e.Padding); held {
				debugf("padding %d remembered and confirmed (%d -> %d)\n",
					e.Padding, at, above)
				return e.Padding
			}
			noticef("the remembered padding %d no longer holds; searching again", e.Padding)
			forgetPadding(key)
		}
	}

	// findPadding searches from minpad and returns an offset, so add
	// minpad back: the sum is the last pad the base script still fits in,
	// which is the padding.  Nothing is subtracted here -- findPadding has
	// already stepped back off the crossing.
	off, _ := findPadding(b, 0, minpad, r)
	pad := off + minpad
	if !flags.NoCache && b.Grid() {
		rememberPadding(key, pad, r.Base)
	}
	return pad
}

// paddingHolds runs the base script either side of a padding and says
// whether it is one: the largest pad still inside a 512-byte block, so
// that one more byte crosses and memory grows.
//
// The two runs are independent, so they are taken at the same time in
// two objects when there are two to take them in.
func paddingHolds(b backend, pad int) (held bool, at, above int) {
	if !expressiblePadding(pad) {
		return false, 0, 0
	}
	// Both at once when there is somewhere to put them: they are two
	// independent readings of the base script and neither depends on
	// the other.
	mem := probeBase(b, []int{pad, pad + 1})
	return mem[1] > mem[0], mem[0], mem[1]
}

// checkIPad confirms that a supplied --ipad names a padding boundary, and is
// run only when the caller asks for it with --check-ipad.
//
// A padding is the largest pad that still fits inside a 512-byte block, so the
// test is that one more byte crosses: run the base script -- no copies of the
// code under test -- at pad and at pad+1, and require the memory to grow.  Two
// live runs, against the dozen the search costs.
//
// This is exactly the pair of runs the search itself ends on, so the value it
// confirms is the value Padding: prints: hand a reported padding straight back
// to --ipad --check-ipad and it passes.
func checkIPad(b backend, pad int) {
	held, atMem, aboveMem := paddingHolds(b, pad)
	debugf("IPad[%d] %d : %d, %d : %d\n", pad, pad, atMem, pad+1, aboveMem)
	if held {
		return
	}
	at, above := Results{Base: atMem}, Results{Base: aboveMem}
	errf(`--ipad %d is not confirmed as a padding boundary.

	pad %d: %d bytes
	pad %d: %d bytes

A padding is the largest pad that still fits inside a 512-byte block, so one
more byte has to cross into the next one and memory has to grow between those
two.  It did not.

Every size autobench reports is measured from this constant, so a padding that
is off by k reports every Size off by k, in the same direction, with nothing in
the output to show it.

Drop --check-ipad to use %d anyway.
`, pad, pad, at.Base, pad+1, above.Base, pad)
}

// expressiblePadding reports whether the filler can emit exactly pad bytes --
// and exactly pad+1 as well, since that is where the runs are taken.
//
// It is not a judgement about whether pad names a boundary; that is the
// caller's to assert, and --check-ipad is how they can have it verified.  This
// is the one thing autobench does know better than the caller: whether it can
// carry out the instruction at all.
//
// The filler emits an even count as a chain of +i in 2-byte steps, and an odd
// one as a 5-byte jump/label pair plus a chain for the remaining pad-5.  So it
// expresses 0, 2, 4 and every count from minpad up, and nothing else: 1 and 3
// both fall back to the bare jump/label pair and come out as 5, and a negative
// pad emits nothing at all.  Accepting one of those would measure at a padding
// other than the one named and report it as the one named.
//
// The runs happen at pad+1, so both pad and pad+1 have to be expressible.  That
// leaves 4 -- emitted as i+i, with its runs at minpad's jump/label pair -- and
// everything from minpad up.  4 rests on no assumption that minpad does not:
// reading 5 and 6 as one byte apart already requires a bare `i;` to cost 2,
// which is the same thing that makes 4 and 5 one byte apart.
//
// 4 is NOT refused on the grounds that no Padding: line can print it (true --
// the search starts at minpad, so basePadding cannot return less).  --ipad is
// not restricted to numbers Padding: has printed: 985 was never printed by any
// search either, and it is a perfectly good padding.  Refusing a value autobench
// can emit, because of where its own search happens to start, is second-guessing
// the caller, and the caller owns that call.
//
// There is deliberately no upper bound.  Boundaries repeat every blockSize
// bytes, so one shape has many paddings and a caller may legitimately name any
// of them: live on 2026-08-03, --ipad 473 and --ipad 985 were the same shape one
// block apart and both reported Size: 368.
func expressiblePadding(pad int) bool { return pad >= minpad-1 }

// oneMode is the whole of -1 mode: one copy of CODE, measured as the memory it
// added rounded up to a whole block, less the part of that last block it did
// not use.  It returns the size to report, the padding to report, and the
// headroom left above the copy, which is printed as "Result pad:".
//
// # Why this is only three readings
//
// A padding is the most filler a script can carry WITHOUT spilling into the
// next block.  So base-plus-BASEPAD sits exactly ON a boundary, and BASEMEM is
// where that boundary is.  Everything follows from that one fact:
//
//   - TESTMEM - BASEMEM is the copy rounded UP to a whole block.  It can only
//     be a whole number of blocks, because the measurement started on one.
//   - the headroom above the copy -- the most filler IT can carry without
//     spilling -- is exactly how much of that last block the copy did not use.
//   - so the copy is the one less the other, to the byte, at any size.
//
// There is one pad in this mode and the runs happen AT it.  An earlier version
// ran a byte past the padding, which put the base script a block up and needed
// a third reading and a count of whole blocks to take the offset back out
// again.  Measured against the model, both answer the same thing everywhere;
// this one is shorter and says why it works.
//
// It is a function rather than a block inside main so that a test can drive it
// against the offline model and no Second Life at hand.  See autobench_test.go.
func oneMode(b backend, r *Results) (size, padding, headroom int) {
	// BASEPAD: the most filler the base script carries without spilling.
	// This is the number Padding: reports and --ipad takes, and the runs
	// happen at it rather than a byte past it -- there is one pad in this
	// mode and it is the one the answer is defined against.
	padding = basePadding(b, r)

	// BASEMEM: where the boundary under that padding is.
	//
	// Into a Results of its own, not r.  Every run round-trips r through
	// the cache and a hit assigns the WHOLE struct back, so a reading kept
	// in r would be overwritten by the first cached run of the search
	// below.  That has been a bug twice.
	//
	// The search that found the padding has already read this pad on its
	// way to the crossing, so this is usually free; with --ipad, where
	// there was no search, it is the one run that establishes the
	// boundary.
	var at Results
	mustRun(b, 0, padding, &at)
	baseMem := at.Base

	// TESTMEM, and the headroom above the copy.
	//
	// findPadding answers both: the reading at the pad it starts from,
	// and the most that can be added to it before memory grows.
	headroom, testMem := findPadding(b, 1, padding, r)

	// The base sat exactly on a boundary, so testMem - baseMem can only be
	// a whole number of blocks: the copy rounded up.  headroom is the part
	// of the last block the copy left unused.  The difference is the copy,
	// to the byte, whatever its size.
	size = (testMem - baseMem) - headroom

	// What the two labelled lines report, said outright rather than left
	// to whichever run happened to write r last.  They now mean what they
	// have always been called: the base script's memory, and the one-copy
	// script's.
	r.Base, r.Test = baseMem, testMem
	return size, padding, headroom
}

const probeScript = `
default {
	state_entry() {
		llOwnerSay("RESULT:Hello");
		llOwnerSay("DONE");
	}
}
`

func main() {
	defer func() {
		if p := recover(); p != nil {
			errf("%v\n", p)
		}
	}()
	args := options.RegisterAndParse(&flags)

	// --help before anything is decided, so that asking what the flags
	// are never rezzes a prim or dials anything.
	//
	// It has to be a flag of its own: without one, getopt treats --help
	// as an option it has never heard of, which prints this same usage
	// with "unknown option: --help" on the front of it and exits 1 --
	// an error report for somebody who asked the question correctly.
	// To stdout, as automate does it, because here it is the answer and
	// not a complaint.
	if flags.Help {
		getopt.PrintUsage(os.Stdout)
		return
	}

	// What this benchmark cost, in the unit the cost is paid in.  Deferred so
	// it is reported even when the benchmark ends in a panic -- a run that
	// failed still spent everything it spent.
	defer reportCost()

	// Where the scripts run.  The choice is made once, here, and nothing
	// above backend.go knows which was made: a benchmark is arithmetic on
	// readings, and where the readings come from is a transport.
	//
	// --test used to be answered ABOVE the transport -- runScript had a
	// branch that returned the model's number without ever sending
	// anything -- so the compile-error path, the fault path,
	// absorbResults and the whole of the transport were reachable only
	// with a grid at the far end.  It is a backend now, served in this
	// process over a pipe, and the offline path runs the same code the
	// live one does.
	var b backend
	switch {
	case flags.Test != "" && flags.Backend != "":
		// Both name where scripts run, and they are not the same
		// somewhere.  Picking one would mean quietly measuring against
		// something other than what was asked for.
		errf("Only one of --test or --backend may be specified\n")
	case flags.Test != "":
		var err error
		if b, err = openModel(testModel(flags.Test), flags.Objects); err != nil {
			errf("%v\n", err)
		}
		defer b.Close()
	case flags.Backend != "":
		var err error
		if b, err = openBackend(flags.Backend, flags.Objects); err != nil {
			errf("%v\n", err)
		}
		defer b.Close()
	default:
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		opts := session.Options{
			Addr: flags.Addr, Agent: flags.Agent, Direct: flags.Direct,
			First: flags.First, Last: flags.Last, Start: flags.Start,
			Channel: "autobench",
		}
		s, obj, spare, cleanup, err := runIn(ctx, opts)
		if err != nil {
			errf("%v\n", err)
		}
		if flags.Keep {
			fmt.Printf("running in %s\n", obj)
		}
		b = &runner{
			s: s, obj: obj, spare: spare, cleanup: cleanup,
			Timeout: flags.Timeout,
			Info:    flags.Show, // -v: surface INFO: chat lines (COUNT/PADDING/*_MEM)
		}
		defer b.Close()
	}
	if flags.Probe {
		ch := make(chan struct{})
		var (
			results []string
			perr    error
		)
		go func() {
			results, _, perr = b.Send(probeScript)
			close(ch)
		}()
		select {
		case <-ch:
			if perr != nil {
				errf("%v\n", perr)
			}
			if len(results) == 0 {
				errf("Script returned no results")
			}
			if got, _ := resultPayload(results[0]); got != "Hello" {
				errf("Got %q, want %q\n", got, "Hello")
			}
			// What answered, rather than what usually answers: --probe
			// reaches whatever backend was chosen, and saying "SL Live"
			// about the offline model would be a lie in the one place a
			// person is checking whether they are talking to Second Life.
			if b.Grid() {
				fmt.Printf("SL Live\n")
			} else {
				fmt.Printf("Backend live\n")
			}
			return
		case <-time.After(time.Minute):
			errf("timed out waiting for Second Life\n")
		}
		return
	}
	switch {
	case len(args) > 1:
		errf("At most 1 test file may be specified\n")
	case len(args) < 1 && flags.Code == "" && flags.Statement == "":
		errf("Either --code, --statement or a file must be specified\n")
	case len(args) == 1 && flags.Code != "":
		errf("Only one of --code or a file may be specified\n")
	case len(args) == 1 && flags.Statement != "":
		errf("Only one of --statement or a file may be specified\n")
	case flags.Code != "" && flags.Statement != "":
		errf("Only one of --code or --statement may be specified\n")
	case flags.Code == "" && flags.Statement == "":
		data, err := os.ReadFile(args[0])
		if err != nil {
			errf("%v\n", err)
		}
		flags.Code = string(data)
	case flags.Statement != "":
		flags.Code = flags.Statement
	}
	switch {
	case flags.IPad != 0 && flags.Fast:
		// --fast asks for no padding at all; --ipad names the padding to
		// use.  Honouring both is impossible, so say so rather than pick.
		errf("Only one of --fast or --ipad may be specified\n")
	case flags.Max < 1:
		// The copy search starts AT the cap, so a cap below one has nowhere to
		// start.  Refuse rather than quietly measuring something else: --max is
		// how a caller bounds a benchmark, and silently ignoring it was A3.
		errf("--max %d: a benchmark needs at least one copy of the code under test\n", flags.Max)
	case flags.IPad != 0 && !expressiblePadding(flags.IPad):
		errf("--ipad %d: not an expressible padding -- the filler emits 5 bytes for 1 and for 3, 2 is measured at 3, and a negative pad emits nothing; 0 means search for it\n", flags.IPad)
	}
	var globals string
	for _, g := range flags.Globals {
		g, err := mkVar(g)
		if err != nil {
			errf("%v\n", err)
		}
		globals += g + ";\n"
	}
	flags.Code = strings.TrimSpace(flags.Code)
	flags.Preamble = globals + strings.TrimSpace(flags.Preamble)
	flags.Postamble = strings.TrimSpace(flags.Postamble)
	if flags.Statement != "" {
		flags.Preamble += "\n_("
		for i, p := range flags.Params {
			p, err := mkVar(p)
			if err != nil {
				errf("%v\n", err)
			}
			if strings.Contains(p, "=") {
				errf("%s: parameters must not be initialized", p)
			}
			if i > 0 {
				flags.Preamble += ", " + p
			} else {
				flags.Preamble += p
			}
		}
		flags.Preamble += ") {\n"
		for _, v := range flags.Locals {
			v, err := mkVar(v)
			if err != nil {
				errf("%v\n", err)
			}
			flags.Preamble += v + ";\n"
		}
		flags.Postamble = "\n}\n" + flags.Postamble
	}

	var r Results

	if flags.One {
		size, basePad, pad := oneMode(b, &r)

		if r.Title != "" {
			fmt.Printf("Title: %s\n", r.Title)
		}
		// The two readings the size is the difference of: the base script
		// at the padding, sitting exactly on a block boundary, and the
		// one-copy script at the same padding.  Result pad is the filler
		// that copy can still carry without spilling into the next block.
		fmt.Printf("Base mem: %d\n", r.Base)
		fmt.Printf("Result mem: %d\n", r.Test)
		fmt.Printf("Result pad: %d\n", pad)

		fmt.Printf("Size: %d\n", size)
		fmt.Printf("Padding: %d\n", basePad)
		return
	}

	padding, cnt, first := copyMode(b, &r)
	if cnt == 0 {
		// Nothing was measured, and copyMode has said why.  There is
		// no size to report, and 511/cnt below is the second way this
		// used to end in a crash rather than an explanation.
		return
	}
	if r.Title != "" {
		fmt.Printf("Title: %s\n", r.Title)
	}
	if padding != 0 {
		// Report it here too, not just in -1 mode: this is the number to feed
		// back with --ipad on the next benchmark of the same shape, and copy
		// mode paid for it as surely as padding mode did.
		fmt.Printf("Padding: %d\n", padding)
	}
	if flags.Fast {
		// No boundary to measure against, so this is still the blend of
		// the two costs, to the quantisation it earned.
		fmt.Printf("Size: %d %s%d\n", int(r.Size), plusminus, 511/cnt)
		return
	}
	// Size is what each copy after the first costs, which is what copy
	// mode has always been for.
	fmt.Printf("Size: %d\n", int(r.Size))
	// And what one costs outright, when the two could be told apart.
	// They differ by whatever the construct pays once and shares, which
	// is nothing for most things -- and when it is small it is below what
	// these readings can resolve, so copyMode withholds it rather than
	// print a number that moves with the copy count.
	if first > 0 {
		fmt.Printf("First copy: %d\n", first)
	}
}

// copyMode is the whole of copy mode: many copies of CODE at one pad, measured
// as a difference of two memory readings divided by the copy count.  It returns
// the padding to report and the copy count actually used, and leaves the
// per-copy size in r.Size, where the benchmark script itself computed it.
//
// Like oneMode it is a function rather than a block inside main so that a test
// can drive it against the offline model.  It was a block inside main until
// 2026-08-03, and that is not incidental: -1 mode had offline tests and copy
// mode had none, which is how copy mode carried a systematic +blockSize/count
// on half of all shapes without anyone being able to see it.
func copyMode(b backend, r *Results) (padding, cnt, first int) {
	// We should really always pad the base.  The base padding is the same
	// quantity here as in -1 mode -- the same base script, the same boundary --
	// so a Padding: value read off either mode may be given to --ipad in either
	// mode.
	pad := 0
	runpad := 0
	if !flags.Fast {
		// AT the padding, as -1 mode does, because that is where the base
		// script sits exactly on a block boundary -- and everything below
		// depends on it.  This used to run a byte past, on the argument
		// that an incidental byte of harness difference between the base
		// script and the test script would otherwise tip a whole block.
		// Modelled, it does not: a difference of d shifts the answer by
		// d/count and never by a block, either side of the boundary.
		pad = basePadding(b, r)
		runpad = pad
	}
	// Get the base result.  This is also what leaves the base memory in linkset
	// data for the copy runs to divide against, so it has to be the last cnt=0
	// run to actually EXECUTE -- and a cache hit does not execute.  The search
	// ends on runpad only in the sense that runpad is the last pad it reads: it
	// meets the crossing partway through its bisection and then narrows below
	// it, so the last script it really sent is usually the one at the padding,
	// one block down.  Serving this run from that cache entry gets the right
	// reading and leaves the wrong base in world, which is +512/count on every
	// Size, silently, for half of all shapes.  So force the run when the world
	// is not already anchored here; when it is -- --ipad, --check-ipad, or a
	// search that happened to end on the crossing -- this costs nothing.
	if lsdPad != runpad {
		delete(cache, Cache{Count: 0, Padding: runpad})
	}
	mustRun(b, 0, runpad, r)

	// Probe upward for a copy count whose memory delta registers (Size != 0).
	// runShrink backs off on a Stack-Heap Collision, so if the probe count
	// overflows it returns a smaller count that fit -- that becomes the ceiling
	// and we stop doubling.
	// Start at 8 copies, but never exceed an explicit --max: the cap is also a
	// loop-exit test below, which alone would let the first probe run 8 copies
	// even when --max is smaller.
	//
	// A9 asked for this to be replaced by SL's compiler: install 512 copies
	// without running them, halve on a refusal, and take the answer.  It was
	// written, run live, and is not here.  What is wrong with it is NOT what it
	// first looked like, and the difference is worth having straight.
	//
	// Measured on Agni 2026-08-03, one session throughout, so none of
	// this is session overhead -- setup is 0.09-0.65s and teardown 0.00-0.01s:
	//
	//	create a script item (24 bytes)     8.10s
	//	update it            (24 bytes)     1.08 - 1.20s
	//
	// The floor is ITEM CREATION, not compilation: sl.InstallScript creates
	// the script, puts it in the object and Settles up to six seconds waiting
	// for the object to admit it is there, and only then uploads.  An item that
	// exists is one upload.  Both a compile and a run pay whichever of those
	// applies, so it cancels out of every comparison here.
	//
	// What does not cancel is source size, and it is superlinear.  Installing
	// into an item that is already there (TestLiveLadderOnOneItem):
	//
	//	  1 copy    1326 bytes   1.22s
	//	  8 copies  1466 bytes   1.12s
	//	 32 copies  1947 bytes   1.31s
	//	 64 copies  2587 bytes   2.66s
	//	128 copies  3868 bytes   5.07s
	//	256 copies  6428 bytes  12.70s
	//	512 copies 11548 bytes  30.96s   REFUSED
	//
	// Six times the bytes, twenty-four times the time.  So A9's cost claim --
	// "~0.5-1.9s" for a compile -- is RIGHT, for a small script: 1.1s.  What
	// does not follow is that the search is cheap, because the search does not
	// ask about small scripts.  It starts at 512 and halves, so it asks 512,
	// 256 and 128: 48.7s measured, against ONE run of the 8-copy script the
	// probe below sends -- 1.1s to install it and 0.7s to run it, the 0.7s
	// being the whole difference between compiling a script and running it
	// (128 copies: compile 16.79s, run 17.46s, both creating their item).
	//
	// Live, end to end, the two versions of this function were 4m12 and 2m10,
	// and the compiler-driven one then had to throw a run away: it was given
	// 256 copies, which SL compiles and which collide stack with heap the
	// moment they run.
	//
	// What the compiler does still settle is what to do when a script is
	// refused, which is a question about the run that just failed rather than a
	// reason to spend a run asking.  That is in runShrink.  See
	// LOCAL/inworld-tasks/A9-results.md.
	D := min(8, flags.Max)
	capped := false
	for {
		used := runShrink(b, D, runpad, r)
		if used < D {
			capped = true
			D = used
		}
		if r.Size != 0 || capped || D >= flags.Max {
			break
		}
		D *= 2
	}
	switch {
	case capped:
		// D copies were the most that fit during probing; don't estimate higher.
		cnt = D
	case r.Size == 0:
		cnt = blockSize
	default:
		maxMem := 62*1024 - r.Base
		per := int(r.Size) + blockSize/(2*D)
		// Whether one copy fits has to be settled BEFORE the shift.
		// bits.Len(0)-1 is -1, and shifting by a negative amount
		// panics -- one line above the check that exists to report
		// this, so the message was unreachable and the user got
		// "negative shift amount" instead.  A base larger than the
		// memory a script has makes maxMem negative, which the same
		// shift turns into an enormous count rather than a refusal.
		if maxMem <= 0 || per <= 0 || maxMem < per {
			fmt.Print("Unable to benchmark\n")
			return pad, 0, 0
		}
		cnt = int(1 << (bits.Len(uint(maxMem/per)) - 1))
	}
	if cnt > flags.Max {
		cnt = flags.Max
	}

	// --fast skipped the padding, so there is no boundary underneath any
	// of this and the exact scheme below cannot be run: it needs the base
	// script to sit ON one.  The old measurement is still available and
	// still says what it always said, to the quantisation it prints.
	//
	// The searches would not work here anyway.  They read every pad from
	// the run pad upwards, and the filler cannot emit 1 or 3 bytes -- at
	// a run pad of nought two of the first four readings would be of a
	// script other than the one asked for.
	if flags.Fast {
		return pad, runShrink(b, cnt, runpad, r), 0
	}

	// The 62KB estimate can still overshoot; runShrink halves the count on a
	// Stack-Heap Collision until it fits, returning the count actually used.
	//
	// Shrunk against the TOP of the range the searches will read rather
	// than against the run pad itself.  They walk a whole block above it,
	// so a count that only just fits at the padding would be refused part
	// way up -- and a refusal inside a search is a panic, not a retry.
	cnt = runShrink(b, cnt, runpad+blockSize-1, r)

	// What the copies actually cost, exactly, at two counts.
	//
	// One reading is not enough and never was.  A copy count of C answers
	// with what C copies cost together, and that is not C times what one
	// costs: a construct pays some of its cost once and shares it, so the
	// total is an initial cost plus C marginal ones.  Dividing by C gives
	// the marginal cost plus the initial one spread over C, which is the
	// blend copy mode has always reported.
	//
	// Measured at C and at C/2 the two separate exactly.  See below.
	mustRun(b, 0, runpad, r)
	baseMem := r.Base

	full, _ := copiesCost(b, cnt, runpad, baseMem, r)
	half, ok := copiesCost(b, cnt/2, runpad, baseMem, r)
	if !ok {
		// One copy fitted and no more, so there is no second count to
		// take a difference against and no marginal cost to be had:
		// what a copy costs on its own and what another one costs after
		// it are two questions and this can only answer the first.
		noticef("only one copy of the code under test fits, so what an ADDITIONAL " +
			"copy would cost cannot be measured; Size below is what one costs " +
			"outright, which is what -1 mode reports\n")
		r.Size = float64(full)
		return pad, cnt, full
	}

	// full = F + C*m and half = F + C/2*m, so
	//
	//	2*half - full = F               the initial cost
	//	(full - F)/C  = m               each copy after the first
	//	F + m                           the first copy, outright
	//
	// F is what a construct pays once and shares.  It is zero for most
	// things and large for anything with a literal in it: a 250-character
	// string is 1044 bytes for one copy and 542 for each after.
	initial := 2*half - full
	size := (full - initial) / cnt
	r.Size = float64(size)

	// C*m has to divide by C.  That it does not means the copies are not
	// an initial cost plus a constant marginal one, so the split is a fit
	// to a shape the construct does not have.
	//
	// The marginal cost survives that -- it is a difference of two
	// readings and the first-order term -- but the initial cost does not.
	// It is the SECOND-order term, and measured live it moves with the
	// copy count when it should not: for one construct 371, 367 and 344
	// at three counts, and negative with a preamble.  So it is reported
	// only when the arithmetic says the shape fits, and withheld
	// otherwise.  A number nobody can rely on is worse than no number.
	if (full-initial)%cnt != 0 {
		noticef("%d copies cost %d bytes and %d cost %d, which does not resolve "+
			"into an initial cost and a constant one per copy; Size is rounded and "+
			"what one copy costs on its own is not reported -- -1 mode measures it "+
			"directly\n", cnt, full, cnt/2, half)
		return pad, cnt, 0
	}
	return pad, cnt, initial + size
}

// copiesCost is what cnt copies add to the base script, exactly.
//
// The base script at runpad sits on a block boundary, so the reading
// with the copies in it, less the boundary, is their cost rounded UP to
// a whole block -- and the headroom above them is how much of that last
// block they did not use.  The difference is the cost, to the byte.
//
// It answers false for a count below one, which is the caller having
// nothing to halve.
func copiesCost(b backend, cnt, runpad, baseMem int, r *Results) (int, bool) {
	if cnt < 1 {
		return 0, false
	}
	headroom, mem := findPadding(b, cnt, runpad, r)
	return (mem - baseMem) - headroom, true
}

// spentCompiles counts the scripts sent to SL's compiler and never started.
// It is kept apart from spentRuns because the two are asked for different
// reasons, not because they cost different amounts: measured, they cost nearly
// the same, which is the finding A9 turned on.
var spentCompiles int

// oneCopyVerdict caches what SL said about a single copy.  The code under test
// does not change during a benchmark, so neither can this.
var oneCopyVerdict *compilation

// oneCopyCompiles asks SL whether ONE copy of the code under test compiles.
//
// It is asked in exactly one situation: a run has just been refused by the
// compiler, and the question is which of the two things that means.  A script
// too large to compile and a script that is not valid LSL come back from SL as
// the same event, and at 512 copies of a real benchmark shape SL's entire
// message is "Internal server compile error" -- no line, no column, and nothing
// in it to tell the two apart by.
//
// One copy is the smallest script a benchmark can be.  If that compiles, the
// refusal was about size and a smaller count is worth trying; if it does not,
// the code itself is being refused, and halving nine more times would spend
// nine more uploads asking the same question.
//
// Not being able to ask is fatal.  The caller is already handling a failed run,
// and a diagnosis that cannot be obtained is not one to guess at.
//
// Whatever is at the far end answers it.  The offline model has no compiler, so
// what stands in for one there is the thing the compiler is being asked about --
// the memory the model says the script would use, against the model's limit --
// and it answers that through the same call, having installed nothing.
func oneCopyCompiles(b backend, pad int) *compilation {
	if oneCopyVerdict != nil {
		return oneCopyVerdict
	}
	spentCompiles++
	c, err := b.Compile(buildScript(1, pad))
	if err != nil {
		panic(err)
	}
	oneCopyVerdict = c
	debugf("Compile[1] accepted=%v\n", oneCopyVerdict.OK)
	return oneCopyVerdict
}

// resultPayload returns the text following "RESULT:" in a message, and whether
// the message carried one at all. The generated script labels the numbers it
// wants read; everything else it says (INFO: lines, the leading blank) is not a
// measurement and is skipped.
func resultPayload(s string) (string, bool) {
	i := strings.Index(s, "RESULT:")
	if i < 0 {
		return "", false
	}
	return strings.TrimSpace(s[i+len("RESULT:"):]), true
}

// Results holds the readings from the most recent run.  One of these is threaded
// through everything and every run overwrites it WHOLESALE -- cache hits
// included, since a hit assigns the stored struct back over it (see runScript).
// So a reading that has to outlive the next run belongs in a local, not in a
// field here; oneMode keeps its base memory that way, and not doing so was a
// real bug that only showed up on a cache hit.
type Results struct {
	Title string
	Base  int
	Test  int
	Size  float64
}

type Cache struct {
	Count   int
	Padding int
}

// cache prevents us from running the exact same script twice.
var cache = map[Cache]Results{}

// lsdPad is the pad of the last cnt=0 script that ACTUALLY RAN, or -1 if none
// has.  It shadows the "mem" key of the benchmark's linkset data, which is the
// base every cnt>0 script divides against and which only an executing cnt=0
// script writes.
//
// The cache is why this has to be tracked rather than assumed.  A cache hit
// returns the reading and sends no script, so the cache and the linkset data
// drift apart -- and they do: the padding search finds the crossing partway
// through its bisection and then keeps narrowing BELOW it, so the last cnt=0
// script to really run is usually the one at the padding, a block lower, while
// the run at runPad is served from cache.  A base one block low is 512/count on
// every Size copy mode reports, with nothing in the output to show it.
var lsdPad = -1

// spentRuns counts the scripts a benchmark actually SENT -- cache hits
// excluded, because a hit costs nothing.  It is the price of a benchmark in the
// only unit that matters here: a run is an upload, a compile, an execution and
// a wait for the script to speak, and everything else this program does is free
// beside it.
//
// It counts runs against the offline model too.  The model is a stand-in for a
// run and is sent one at exactly the points a run would be, so the count is the
// same count; that is what lets a change be costed offline before it is spent
// live.
var spentRuns int

// spentRereads counts the runs spent asking a question a second time --
// confirming a crossing, or the base it is measured against.  They are runs like
// any other and are counted in spentRuns too; this says how many of them bought
// confidence rather than an answer.
var spentRereads int

// reportCost says what the benchmark spent, under --debug.  It is on stderr
// with the rest of the debug output so that a caller parsing the Size: line
// does not have to know about it.
func reportCost() {
	debugf("Spent %d runs (%d of them re-reads) and %d compiles\n",
		spentRuns, spentRereads, spentCompiles)
}

// mustRun runs the script and treats any error as fatal.  Used where an error
// is not expected and not recoverable (padding search, zero-copy base run).
func mustRun(b backend, cnt, pad int, r *Results) {
	err := runScript(b, cnt, pad, r)
	if err != nil {
		panic(err)
	}
}

// runShrink runs cnt copies, halving the count until the script fits or the
// count reaches 1.  It returns the count actually measured, so the caller can
// report the right error margin.  Anything else is fatal.
//
// There are two ways a script can be too big, and they are not the same limit.
// A Stack-Heap Collision is the RUN-TIME one: SL compiled the script, started
// it, and it ran out of the 64KB a Mono script has.  A compile refusal is the
// COMPILER's, and it is looser -- measured on Agni 2026-08-03, 256
// copies of the reference shape compiled and then collided stack with heap the
// moment they ran, while 512 copies were refused outright.  Either way a
// smaller count is the thing to try, so both halve.
//
// The difference is at the bottom.  A collision at one copy is still a size
// limit and there is nothing smaller to fall back to.  A REFUSAL at one copy is
// not a size limit at all -- one copy is the smallest script a benchmark can
// be -- so it is the code under test that SL will not take, and saying so is
// worth more than another nine uploads finding out.  oneCopyCompiles is what
// tells the two apart, and it is asked at most once per benchmark.
func runShrink(b backend, cnt, pad int, r *Results) int {
	for {
		err := runScript(b, cnt, pad, r)
		if err == nil {
			return cnt
		}
		var re *runtimeError
		if cnt > 1 && errors.As(err, &re) && re.OutOfMemory() {
			debugf("Stack-Heap Collision at %d copies; retrying at %d\n", cnt, cnt/2)
			cnt /= 2
			continue
		}
		var ce *compileError
		if errors.As(err, &ce) {
			if one := oneCopyCompiles(b, pad); !one.OK {
				errf(`Second Life will not compile one copy of the code under test:

%s

One copy is the smallest script a benchmark can be, so this is not a size
limit -- it is Second Life refusing the code itself.  Any line and column above
are SL's, counted in the generated script: the preamble comes first, then the
copies of CODE, then the harness, then the padding.
`, one.Error())
			}
			if cnt > 1 {
				// With the reason: "too big" and "not valid LSL"
				// arrive as the same event, and halving blindly
				// hides which one is happening.
				debugf("compile refused at %d copies (%v); retrying at %d\n",
					cnt, ce, cnt/2)
				cnt /= 2
				continue
			}
		}
		panic(err)
	}
}

// buildScript renders the benchmark: cnt copies of CODE between the preamble
// and the postamble, then the harness, then pad bytes of filler.
//
// It is a function of (cnt, pad) and the flags and of nothing else -- no world,
// no cache, no reading -- which is what lets the compile check ask about a
// script without running it.  The script it renders for a given (cnt, pad) is
// byte for byte the script runScript would send for that (cnt, pad), and it has
// to stay that way: a compile check that answers about a different script from
// the one that will run is worse than no check, because it is confident.
func buildScript(cnt, pad int) string {
	var buf strings.Builder
	if flags.Preamble != "" {
		buf.WriteString(flags.Preamble)
		buf.WriteString("\n")
	}
	for i := 0; i < cnt; i++ {
		fmt.Fprintln(&buf, strings.Replace(flags.Code, "CNT", fmt.Sprintf("%03d", i), -1))
	}
	if flags.Postamble != "" {
		buf.WriteString(flags.Postamble)
		buf.WriteString("\n")
	}
	var title string
	if flags.Title != "" {
		title = `llOwnerSay("\nRESULT: TITLE=` + flags.Title + `");`
	}
	// The pad the harness REPORTS is the one it was asked for; the filler
	// below consumes its copy.  Keep them apart.
	reported := pad
	// A builder rather than a string appended to in the loop below.  The
	// +i chain adds two bytes at a time, and appending to a string copies
	// the whole of it each time, so rendering a pad of n cost n^2/4 bytes
	// of copying -- 53 kilobytes of garbage for a middling script.  It
	// went unnoticed while --test answered above the transport and built
	// no script at all; the offline sweeps build 440,000 and it was the
	// largest single thing they spent.  The bytes emitted are the same
	// bytes, which is the only thing about this function that may not
	// change.
	var pb strings.Builder
	pb.WriteString(flags.Pad)
	// `integer i;` backs the +i filler chain below.  Emit it unconditionally
	// so its cost is a fixed constant across every pad value; the old
	// `if pad > 0` made the pad 0 -> 1 transition a variable-declaration-sized
	// jump instead of one byte.
	pb.WriteString("integer i;\n")
	// The +i chain moves in 2-byte steps, so on its own it can only express
	// even byte counts.  A jump/label pair is the one odd-sized (5-byte)
	// filler, so spend it whenever pad is odd; pad-5 is then even.  Callers
	// keep pad >= minpad -- the searches start there and expressiblePadding
	// holds --ipad to it -- so this never drives pad negative and every count
	// from minpad up is representable to the byte, with no upper limit.
	if pad%2 != 0 {
		pb.WriteString("jump Z; @Z;\n")
		pad -= 5
	}
	if pad >= 2 {
		pb.WriteString("i")
		pad -= 2
		for pad >= 2 {
			pb.WriteString("+i")
			pad -= 2
		}
		pb.WriteString(";\n")
	}

	fmt.Fprintf(&buf, code, title, cnt, reported, pb.String())
	return buf.String()
}

func runScript(b backend, cnt, pad int, r *Results) error {
	key := Cache{Count: cnt, Padding: pad}
	if or, ok := cache[key]; ok {
		debugf("Using cache for %v\n", key)
		*r = or
		return nil
	}
	spentRuns++
	script := buildScript(cnt, pad)
	if flags.Show {
		fmt.Println(script)
	}
	results, info, err := b.Send(script)
	if err != nil {
		return err
	}
	if cnt == 0 {
		// This script wrote linkset data; record where, so a later base run
		// that a cache hit would silence can be forced to run instead.
		lsdPad = key.Padding
	}
	if flags.Show {
		for _, s := range info {
			fmt.Println("INFO:", s)
		}
	}
	absorbResults(results, r)
	cache[key] = *r
	return nil
}

// absorbResults reads what the script reported into a Results.
//
// The runner returns EVERY line the script said, not just the RESULT:
// ones, so the convention is applied here.  It belongs in the benchmark
// and not in the transport: nothing else about running a script
// requires a script to label its output.
func absorbResults(results []string, r *Results) {
	const (
		BM    = "BASE_MEM="
		TM    = "TEST_MEM="
		SZ    = "SIZE="
		TITLE = "TITLE="
	)
	for _, raw := range results {
		s, ok := resultPayload(raw)
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(s, BM):
			r.Base, _ = strconv.Atoi(s[len(BM):])
		case strings.HasPrefix(s, TM):
			r.Test, _ = strconv.Atoi(s[len(TM):])
		case strings.HasPrefix(s, SZ):
			r.Size, _ = strconv.ParseFloat(s[len(SZ):], 64)
		case strings.HasPrefix(s, TITLE):
			r.Title = s[len(TITLE):]
		}
	}
}

// code is the boilerplate for autobench.  It is printed with 4 positional
// parameters:
//  1. statement to print title (if any)
//  2. the number of times the CODE was repeated
//  3. the amount of padding added
//  4. instructions to pad the code size
var code = `
result(integer mem, integer count, integer padding) {
	llOwnerSay("\n");
    %s                      // Title
    if (count) {
        integer old = (integer)llLinksetDataRead("mem");
        string s = llLinksetDataRead("name");
		llOwnerSay("INFO:COUNT=" + (string)count);
		llOwnerSay("RESULT:SIZE=" + (string)((float)(mem - old)/(float)count));
		llOwnerSay("RESULT:TEST_MEM=" + (string)mem);
		llOwnerSay("INFO:BASE_MEM=" + (string)old);
    } else {
        llLinksetDataWrite("mem", (string)mem);
        llOwnerSay("RESULT:BASE_MEM=" + (string)mem);
    }
    llOwnerSay("INFO:LAST_MEM=" + (string)llGetUsedMemory());
	llOwnerSay("INFO:PADDING=" + (string)padding);
    llOwnerSay("DONE");
	return;
}

default {
    state_entry() {
		result(llGetUsedMemory(), %d, %d);
        %s                  // Padding
    }
}`

func mkVar(s string) (string, error) {
	if s == "" {
		return "", errors.New("missing variable name")
	}
	switch s[0] | ' ' {
	case 'a', 'l':
		return "list " + s, nil
	case 'f', 'g':
		return "float " + s, nil
	case 'i', 'j', 'x':
		return "integer " + s, nil
	case 'k':
		return "key " + s, nil
	case 'q', 'r':
		return "quaternion " + s, nil
	case 's':
		return "string " + s, nil
	case 'v':
		return "vector " + s, nil
	default:
		return "", fmt.Errorf("%s: invalid variable name", s)
	}
}

// runIn gets somewhere to run scripts: the measured object, and any
// spare objects to take readings in alongside it.
//
// See automate's for why the object is worn and kept.  The spares are
// this program's own: a padding search is a dozen readings of one
// script that do not depend on each other, and two objects can take two
// of them at once.
func runIn(ctx context.Context, o session.Options) (*sl.Session, *sl.Object, []*sl.Object, func(), error) {
	if flags.Object != "" || flags.Rez {
		s, err := session.Connect(ctx, o)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		obj, cleanup, err := session.RunIn(ctx, s, flags.Object, flags.Keep)
		if err != nil {
			s.Close()
			return nil, nil, nil, nil, err
		}
		return s, obj, nil, func() { cleanup(); s.Close() }, nil
	}

	a, err := session.UseAutoAnywhere(ctx, o, flags.Objects)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	// Which avatar, when nobody said.  With several hosted this is the
	// daemon's choice and the reader cannot work it out; and a benchmark
	// attributed to the wrong avatar is not an error, it is a plausible
	// number.
	if o.Agent == "" {
		fmt.Fprintf(os.Stderr, "running as %s, objects %d-%d\n",
			a.Agent, a.Group*session.AutoGroupSize,
			a.Group*session.AutoGroupSize+len(a.Objects)-1)
	}
	return a.Session, a.Objects[0], a.Objects[1:], a.Release, nil
}
