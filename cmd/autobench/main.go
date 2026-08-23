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
	"math"
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
)

var flags = struct {
	Preamble  string          `getopt:"--preamble=PREAMBLE Make STR the test's preamble"`
	Postamble string          `getopt:"--postamble=POSTAMBLE Make STR the test's postamble"`
	Code      string          `getopt:"--code=CODE code to test"`
	Statement string          `getopt:"--statement=CODE statement(s) to test"`
	Addr      string          `getopt:"--addr=HOST:PORT the slgod to attach to; default sl-host, or this machine"`
	Agent     string          `getopt:"--agent=NAME -a the profile to use; the only one, by default"`
	Direct    bool            `getopt:"--direct -d log in to Second Life directly, without slgod"`
	First     string          `getopt:"--first=NAME the avatar's first name, for --direct"`
	Last      string          `getopt:"--last=NAME the avatar's last name, for --direct"`
	Start     string          `getopt:"--start=WHERE where to arrive: last, home, or a region, for --direct"`
	V         options.Counter `getopt:"--verbose -v say more: once for the readings behind the answer, twice for whose objects it ran in, three times for the scripts themselves"`
	Params    []string        `getopt:"--params=NAME,... parameters used with --statement"`
	Locals    []string        `getopt:"--locals=NAME,... locals used with --statement"`
	Globals   []string        `getopt:"--globals=NAME,... declare globals"`
	Extra     int             `getopt:"--extra=N also measure N further copies, giving what each copy after the first costs; a multiple of 4"`
	IPad      int             `getopt:"--ipad=N measure at this base padding instead of searching for one; confirmed before it is used"`
	Paranoid  bool            `getopt:"--paranoid read each crossing again before believing it"`
	Debug     bool            `getopt:"--debug enable debugging"`
	Probe     bool            `getopt:"--probe send a simple script to LSL as a probe"`
	NoCache   bool            `getopt:"--no-cache do not remember or reuse the padding for this base script"`
	Parts     int             `getopt:"--parts=N cut the padding search into N parts a round, running N-1 scripts at once; a power of 2"`
	Timeout   time.Duration   `getopt:"--timeout=DUR timeout on waiting for an LSL script to complete"`
	Test      string          `getopt:"--test=PAD,SIZE[,MARGINAL[,LIMIT]] measure against the offline model in this process, see the source code"`
	Backend   string          `getopt:"--backend=HOST:PORT run scripts through a script.v1 backend there -- a simulator or a viewer daemon -- instead of in Second Life; --test is the same contract answered by a model here"`
	Help      bool            `getopt:"--help -h show this message"`
}{
	Start:   "last",
	Timeout: time.Minute,
	// Eight parts, which is the measured knee: 8*8*8 is 512 exactly, so
	// three rounds of seven scripts land on the byte with no round
	// wasted.  Sixteen parts takes the same three rounds for ten more
	// scripts, and thirty-two saves a round and measured slower.  See
	// doc/memory.md.
	Parts: 8,

	// Eight further copies.  Code is 4-aligned, so individual copies
	// quantise around their real cost and only a multiple of four
	// averages it out; four is the smallest that does and eight is the
	// smallest that is not obviously the smallest -- it halves whatever
	// the offset is on top of that, and costs nothing in rounds.  See
	// doc/memory.md.
	Extra: 8,
}

// leaseSize is how many objects a benchmark holds.
//
// One to measure in and one per division of the search, which is --parts
// of them, and three more.  The three are what lets warmTheSearch put a
// remembered padding's confirmation and the opening of the search it may
// need into a single round: the two readings that confirm, and the one
// at the anchor the search starts from.
//
// Measured live, they are close to free -- fifteen scripts in a round
// cost 1.57s against 1.50s for seven -- and they are only ever held, not
// necessarily used.
func leaseSize() int { return searchesAtOnce*flags.Parts + 3 }

// searchesAtOnce is how many padding searches will share rounds, which
// the lease has to be big enough to carry: each wants its anchor and its
// parts-1 divisions in every round.
//
// One, until something asks for more.  Searches share rounds only when
// they are anchored at the SAME pad -- the base search cannot, because
// what it finds is where the others start from.
var searchesAtOnce = 1

// openLease asks for the places a benchmark would like and settles for
// what it can have.
//
// What it would like is one object to measure in, one per division of
// each search, and three so that a remembered padding's confirmation and
// the readings that follow it fit in one round.  None of that is
// necessary: with fewer places the searches share rounds less, and with
// one place there is no parallel search at all and the block is bisected
// -- nine rounds instead of three, and the same answer.
//
// So it halves rather than refusing.  A benchmark that cannot have
// nineteen objects should be slower, not impossible, and a backend that
// grants four is a real case rather than a hypothetical one.
//
// It says so when it settles for less, because the difference is large
// enough that somebody timing a benchmark should not have to guess.
func openLease(open func(int) (backend, error)) (backend, error) {
	want := leaseSize()
	var err error
	for n := want; n >= 1; n = n / 2 {
		var b backend
		if b, err = open(n); err == nil {
			if n < want {
				noticef("holding %d objects rather than %d; the searches will "+
					"share fewer rounds\n", n, want)
			}
			return b, nil
		}
		debugf("could not hold %d objects: %v\n", n, err)
	}
	return nil, err
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
const minpad = 0

// warmTheSearch sends, in ONE round, the two readings that confirm a
// remembered padding and the readings the benchmark will want NEXT if it
// holds.
//
// It can, because -1 mode's second search is as fixed as the first: if
// the remembered padding stands, oneMode reads the one-copy script at
// that padding and then opens a search whose first round is
// partPads(0, blockSize, parts) above it.  None of that depends on
// anything not already known.
//
// # It is a bet, and it is placed on the padding HOLDING
//
// The first version of this bet the other way -- it carried the opening
// of the SEARCH a failed confirmation would need -- and measured, live
// at 8 parts, that is a losing bet:
//
//	                              rounds  runs  time
//	cold, no cache                  14     50    21s
//	padding holds                    8     27    12s
//	padding holds, warmed for a      8     35    12.7s
//	  failure
//	padding wrong                   15     51    22s
//	padding wrong, warmed for a     13     51    19s
//	  failure
//
// Three seconds saved when the padding is wrong, seven tenths spent
// every time it is right: worth it only if a remembered padding is
// wrong about a quarter of the time.  It is not -- eight independent
// searches of one shape returned the same answer -- so the readings to
// carry are the ones wanted when it holds.
//
// The failure case is left as it was, and does not need help: a
// confirmation that fails costs 15 rounds where a cold search costs 14,
// which is a fifteenth and not the third it looks like when the search
// is counted as its three rounds of narrowing rather than the seven it
// really is.
//
// # Only for -1 mode
//
// Copy mode calls basePadding too, and what follows it there is the
// shrink ladder at a count nothing here can predict.  Warming would
// send scripts nothing is going to ask for.
func warmTheSearch(b backend, remembered int) {
	parts := usableParts(b, flags.Parts)
	if parts == 0 {
		// Nowhere to run them in parallel, so there is no round to
		// share and every reading sent here would be a round of its
		// own -- which is the cost this exists to avoid.
		return
	}

	// The question being asked, then the answers wanted if it comes back
	// yes.  A reading already known costs nothing: probeReadings reads
	// the cache first.
	want := []reading{{0, remembered}, {0, remembered + 1}, {1, remembered}}
	for _, p := range partPads(0, blockSize, parts) {
		want = append(want, reading{1, remembered + p})
	}

	if len(want) > b.Spares() {
		// One round is the whole point.  Sending these in two would
		// spend the round the confirmation was going to spend and
		// another beside it, which is worse than not warming at all.
		debugf("not warming: %d readings, %d places\n", len(want), b.Spares())
		return
	}
	probeReadings(b, want)
}

// warmPaddings takes the readings that several padding searches at one
// pad are going to ask for, sharing rounds between them.
//
// The searches are independent -- different scripts, different crossings
// -- but they are anchored at the same pad and they narrow in lockstep,
// because a round divides a range by parts EXACTLY: 512 to 64 to 8 to 1
// at eight parts, whatever the answer turns out to be.  So round two of
// one search and round two of another can travel together, and N
// searches cost the rounds of one.
//
// It decides nothing.  What it works out about each search is thrown
// away, and its only effect is that the readings are in the cache; the
// searches then run exactly as they always did and find their answers
// there.  That is deliberate -- the walk, the staircase check, the
// re-reads under --paranoid and the retry when a base has moved are the
// parts worth not having two copies of.
//
// The anchors go in the first round with the first divisions.  Their
// readings are needed to DECIDE, not to ask: the pads a first round
// wants are fixed at 0..512 for every search.
func warmPaddings(b backend, cnts []int, pad int) {
	parts := usableParts(b, flags.Parts)
	if parts == 0 || len(cnts) == 0 {
		return
	}

	lows := make([]int, len(cnts))
	highs := make([]int, len(cnts))
	for i := range cnts {
		highs[i] = blockSize
	}

	for round := 0; ; round++ {
		var want []reading
		if round == 0 {
			for _, c := range cnts {
				want = append(want, reading{c, pad})
			}
		}
		asks := make([][]int, len(cnts))
		for i, c := range cnts {
			if highs[i]-lows[i] <= 1 {
				continue
			}
			asks[i] = partPads(lows[i], highs[i], parts)
			for _, off := range asks[i] {
				want = append(want, reading{c, pad + off})
			}
		}
		if len(want) == 0 {
			return
		}
		mem := probeReadings(b, want)

		at := 0
		if round == 0 {
			at = len(cnts)
		}
		for i, c := range cnts {
			if asks[i] == nil {
				continue
			}
			base, ok := probed(c, pad)
			if !ok {
				// The anchor did not come back.  Nothing here is worth
				// failing over: the searches will ask again.
				return
			}
			got := mem[at : at+len(asks[i])]
			at += len(asks[i])

			grew := len(asks[i])
			for j, m := range got {
				if m > base {
					grew = j
					break
				}
			}
			if grew > 0 {
				lows[i] = asks[i][grew-1]
			}
			if grew < len(asks[i]) {
				highs[i] = asks[i][grew]
			}
		}
		debugf("Warm[%v] %v < ... < %v\n", cnts, lows, highs)
	}
}

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

// partSearch narrows the range by cutting it into PARTS at once instead
// of into two.
//
// A bisection cuts the range in two and spends one reading doing it.
// This cuts it into parts and spends parts-1 -- the divisions between
// them -- and those readings are taken TOGETHER, which is why it is
// worth spending them: three round trips in flight cost about what one
// costs, and nine rounds of a bisection become five at four parts and
// two at thirty-two.
//
// The readings are read in order, which is the whole of the logic: the
// first pad whose memory has grown is above the crossing and puts the
// ceiling there, and everything below the last pad that has NOT grown is
// settled. Two parts is exactly the bisection below, so it declines
// rather than doing that work twice.
//
// Parts is what the caller asked for, capped by the objects there are to
// run in: parts-1 readings need parts-1 spare objects, so --parts=32
// wants 32 objects and gets as many parts as it has places. Powers of
// two are the natural choice -- they divide the block evenly -- but
// nothing here requires one.
//
// When the range gets down to where a single round could ask about every
// remaining pad, it does exactly that: the step is 1 rather than 0, and
// the answer is exact in one more round instead of a bisection's several.
//
// It narrows to a range of one and leaves the walk and the confirmation
// to the caller. Never in the measured object: see probe.go for why that
// is safe for a script with copies in it as well as for the base.
func partSearch(b backend, cnt, pad, base, low, high, parts int) (int, int) {
	parts = usableParts(b, parts)
	if parts == 0 {
		return low, high
	}

	for high-low > 1 {
		pads := partPads(low, high, parts)
		if len(pads) == 0 {
			break
		}

		mem := probeAt(b, cnt, addTo(pads, pad))

		grew := len(pads)
		for i, m := range mem {
			if m > base {
				grew = i
				break
			}
		}
		if grew > 0 {
			low = pads[grew-1]
		}
		if grew < len(pads) {
			high = pads[grew]
		}
		debugf("Part[%d] %d < ... < %d\n", cnt, low, high)
	}
	return low, high
}

// usableParts is how many parts a search will really cut a range into:
// what was asked for, capped by the places there are to run in.  Nought
// means it will not run at all -- two parts is the bisection, which is
// below it and would be the same work done twice.
//
// It is a function of its own because the speculation in basePadding has
// to ask the same question and get the same answer: it sends the pads a
// search WOULD ask for before knowing whether the search will happen, and
// a pad it guessed differently is a run spent on nothing.
func usableParts(b backend, parts int) int {
	if b == nil {
		return 0
	}
	if n := b.Spares() + 1; n < parts {
		parts = n
	}
	if parts < 3 {
		return 0
	}
	return parts
}

// partPads is where one round's probes go: the divisions between parts
// equal pieces of low..high, at most one per interior pad.
//
// A step of nought would ask about the same pad several times and learn
// nothing, for ever, so the count comes down to what the range can hold
// -- and when it is that small the step is 1 and the round asks about
// every remaining pad, which ends the search exactly.
func partPads(low, high, parts int) []int {
	n := parts - 1
	if n > high-low-1 {
		n = high - low - 1
	}
	if n < 1 {
		return nil
	}
	step := (high - low) / (n + 1)
	pads := make([]int, n)
	for i := range pads {
		pads[i] = low + (i+1)*step
	}
	return pads
}

// addTo offsets every pad by the run padding, which is what the search
// reasons in and what a script has to be sent.
func addTo(pads []int, pad int) []int {
	out := make([]int, len(pads))
	for i, p := range pads {
		out[i] = p + pad
	}
	return out
}

// searchPadding is one attempt at findPadding: bisect the block, walk to the
// exact byte, confirm.  ok is false when the confirmation found the base itself
// to have been misread, in which case base is the corrected reading and the
// whole search has to be redone against it -- every comparison it made was
// against the wrong number.
func searchPadding(b backend, cnt, pad int, r *Results, getBase func() int) (offset, base int, ok bool) {
	// The anchor and the first round's divisions in ONE round.
	//
	// Cutting a range into N parts takes N readings, not N-1: the N-1
	// dividers and the reading at the anchor itself.  Without the anchor,
	// dividers that all read the same leave the step ambiguous -- it
	// could be below the first or above the last -- and with it, all the
	// same means the step is in the last part.
	//
	// They were two rounds, the anchor read on its own before the search
	// began, because partSearch needs the base to compare against.  It
	// needs it to compare, not to ASK: the pads are fixed and the
	// comparison happens once the answers are in hand.
	if parts := usableParts(b, flags.Parts); parts > 0 {
		want := []reading{{cnt, pad}}
		for _, off := range partPads(0, blockSize, parts) {
			want = append(want, reading{cnt, pad + off})
		}
		if len(want) <= b.Spares() {
			probeReadings(b, want)
		}
	}

	mustRun(b, cnt, pad, r)
	base = getBase()
	debugf("Base[%d] %d : %d\n", cnt, pad, base)

	var mid int
	low := 0
	high := blockSize

	// Cut the range into parts, asking about every division at once.
	// What it leaves -- everything, if there is nowhere to run in
	// parallel -- is bisected below.
	low, high = partSearch(b, cnt, pad, base, low, high, flags.Parts)

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
			// The crossing against every reading this search took.  Free:
			// they are in hand, and memory moving one block at a time
			// means a crossing says what all of them must be.
			if off, was, want, ok := brokenStair(cnt, pad, low-1, base); !ok {
				return 0, recheck(b, cnt, pad, base, off, was, want, r, getBase), false
			}
			if !flags.Paranoid {
				return low - 1, base, true
			}
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

	// All three at once.  They are independent readings of three pads and
	// none of the answers depends on another, so they are ONE round --
	// which is what they cost, and asking them one at a time cost three.
	//
	// Taking all three always, rather than stopping at the first that
	// settles the question, is two scripts more in the case where the
	// first one answers it.  Scripts inside a round already being spent
	// are close to free; rounds are not.
	step, anchor, below := rereadTogether(b, cnt, pad, low, r)

	// The reading that looked like the crossing.
	if step == base {
		noticef("the %d-copy script at pad %d read %d, and %d when asked again; "+
			"%d is what the pads around it read, so the first answer was noise "+
			"and the search continues past it\n", cnt, low+pad, first, step, base)
		return crossingLater, base
	} else if step != first {
		noticef("the %d-copy script at pad %d read %d and then %d; both say memory "+
			"grew from %d, so the crossing is here, but the readings themselves "+
			"do not agree\n", cnt, low+pad, first, step, base)
	}
	// The base every one of those comparisons was made against.
	if anchor != base {
		noticef("the %d-copy script at pad %d read %d at the start of the search and "+
			"%d now; that is the base every comparison was made against, so the "+
			"search is being run again from %d\n", cnt, pad, base, anchor, anchor)
		return crossingSuspect, anchor
	}
	// The pad below has to be INSIDE the block.  If it is not, the crossing is
	// somewhere below and the walk was above it the whole time -- which is what
	// a reading that is spuriously LOW does, and 512 low is as plausible as the
	// 512 high that was actually seen.  low == 1 needs no run: pad+0 is the base
	// script, just read.
	if low > 1 && below != base {
		noticef("the %d-copy script at pad %d was read as inside the block and "+
			"now reads %d against a base of %d; the crossing is below this, so "+
			"the search is being run again\n", cnt, low-1+pad, below, base)
		return crossingSuspect, base
	}
	return crossingHolds, base
}

// rereadTogether asks about the step, the anchor it was measured against
// and the pad below the step, in one round, going around the cache and
// leaving the new answers in it.
//
// r is left holding the reading at the step, which is what the caller
// goes on to measure with: probeReadings writes the cache and not r, so
// the run that reads it back out is a cache hit and costs nothing.
func rereadTogether(b backend, cnt, pad, low int, r *Results) (step, anchor, below int) {
	want := []reading{{cnt, low + pad}, {cnt, pad}}
	if low > 1 {
		want = append(want, reading{cnt, low - 1 + pad})
	}

	probeMu.Lock()
	for _, w := range want {
		delete(cache, Cache{Count: w.cnt, Padding: w.pad})
	}
	spentRereads += len(want)
	probeMu.Unlock()

	got := probeReadings(b, want)
	debugf("Reread[%d] %v: %v\n", cnt, want, got)

	step, anchor = got[0], got[1]
	below = got[1]
	if len(got) > 2 {
		below = got[2]
	}
	return step, anchor, below
}

// brokenStair checks the crossing against EVERY reading this search
// took, which costs nothing: they are already in hand.
//
// Memory moves one block at a time and there is exactly one step in the
// 512 bytes above any pad, so a proposed crossing says what all of them
// must be -- the base at or below it, one block up above it.  Anything
// else means the readings this search reasoned from do not describe a
// staircase, and no answer from them is worth reporting.
//
// What it does NOT catch is the reading a search converges ON.  One
// answer a block high makes the search narrow below it and stop there,
// and the result is entirely consistent: flat below, a block up at that
// pad and above.  A manufactured crossing looks exactly like a real one
// from the inside, which is what --paranoid is for and why this cannot
// replace it.
//
// What it does catch is a reading that contradicts the answer -- which
// is the shape a fault in the code DRIVING the scripts takes: a reading
// attributed to the wrong pad, an answer from the wrong object, a cache
// key that collided.  Those do not arrange themselves into a staircase.
//
// crossed is the last offset that should still read the base.
func brokenStair(cnt, pad, crossed, base int) (off, was, want int, ok bool) {
	for off := 0; off <= blockSize; off++ {
		m, known := probed(cnt, pad+off)
		if !known {
			continue
		}
		want := base
		if off > crossed {
			want = base + blockSize
		}
		if m != want {
			return off, m, want, false
		}
	}
	return 0, 0, 0, true
}

// recheck asks again about a reading that does not fit the staircase and
// about the anchor, in one round, and says what the search should be run
// against next time.
//
// The anchor is asked whatever disagreed: every comparison the search
// made was against it, so a base that has moved explains any amount of
// disagreement further up, and asking only the pad that happened to be
// noticed would correct a symptom.
func recheck(b backend, cnt, pad, base, off, was, want int, r *Results, getBase func() int) int {
	forget := []reading{{cnt, pad}}
	if off != 0 {
		forget = append(forget, reading{cnt, pad + off})
	}
	probeMu.Lock()
	for _, f := range forget {
		delete(cache, Cache{Count: f.cnt, Padding: f.pad})
	}
	spentRereads += len(forget)
	probeMu.Unlock()

	nb := probeReadings(b, forget)[0]

	noticef("the %d-copy script at pad %d read %d during the search, against a "+
		"crossing that says %d; these readings are not a staircase and the "+
		"search is being run again\n", cnt, pad+off, was, want)
	if nb != base {
		noticef("the base they were compared against, at pad %d, read %d then "+
			"and %d now\n", pad, base, nb)
	}
	return nb
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
	// --ipad names the padding to measure at: the caller has run this shape
	// before, or read one out of doc/memory.md, and is telling us what it is.
	//
	// It is confirmed rather than taken, exactly as a remembered one is, and
	// for the same reason: a padding wrong by k reports every Size wrong by k
	// with nothing in the output to show it.  It used to be checked only when
	// asked (--check-ipad) because the check cost two live runs; those two
	// readings ride in a round that is being spent anyway now, so there is
	// nothing left to decide and no flag for it.
	if flags.IPad != 0 {
		warmTheSearch(b, flags.IPad)
		checkIPad(b, flags.IPad)
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
			warmTheSearch(b, e.Padding)
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
// The filler emits 5+pad bytes for every pad from nought up: an even one
// as the 5-byte jump/label pair plus pad/2 of the 2-byte +i terms, an odd
// one as (pad+5)/2 terms and no pair.  So every non-negative count is
// expressible, to the byte, and a negative one is not -- it emits nothing
// at all, which would measure at a padding other than the one named and
// report it as the one named.
//
// It used to be less than that.  The pair was spent only when the pad was
// odd, so 1 and 3 both came out as the bare pair and measured 5, and the
// search had to start above them at a minpad of 5 -- which every padding
// this program printed then carried, putting them in [5, 517) for a block
// that is [0, 512).  Both are gone; the pair is a constant in every
// script now, which is where a constant belongs.
//
// There is deliberately no upper bound.  Boundaries repeat every blockSize
// bytes, so one shape has many paddings and a caller may legitimately name any
// of them: live on 2026-08-03, --ipad 473 and --ipad 985 were the same shape one
// block apart and both reported Size: 368.
func expressiblePadding(pad int) bool { return pad >= 0 }

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
func oneMode(b backend, r *Results) (size, padding, headroom int, marginal float64, marginalOK bool) {
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
	//
	// With --extra, the script with N further copies is searched for at
	// the same padding, and the two searches share their rounds: they
	// are anchored at the same pad and narrow in lockstep, so the second
	// one costs scripts and no time.  See warmPaddings.
	counts := []int{1}
	if flags.Extra > 0 {
		counts = append(counts, 1+flags.Extra)
	}
	warmPaddings(b, counts, padding)

	headroom, testMem := findPadding(b, 1, padding, r)

	// The base sat exactly on a boundary, so testMem - baseMem can only be
	// a whole number of blocks: the copy rounded up.  headroom is the part
	// of the last block the copy left unused.  The difference is the copy,
	// to the byte, whatever its size.
	size = (testMem - baseMem) - headroom

	// What each copy AFTER the first costs.
	//
	// A construct pays some of its cost once and shares it, so what N+1
	// copies cost is that once-paid part plus N+1 marginal ones.  One
	// copy costs the same once-paid part plus one.  The difference is N
	// marginal copies and nothing else -- the shared part cancels, which
	// is the whole reason for measuring two counts rather than dividing
	// one by its count.
	//
	// It is the number copy mode exists to produce, and it is got here
	// from a script with a handful of copies in it rather than one with
	// up to 512: measured live, installing 128 copies took 5.07s, 256
	// took 12.70s and 512 was refused.
	if flags.Extra > 0 {
		var more Results
		headroomN, testMemN := findPadding(b, 1+flags.Extra, padding, &more)
		sizeN := (testMemN - baseMem) - headroomN
		marginal = float64(sizeN-size) / float64(flags.Extra)
		marginalOK = true
	}

	// What the two labelled lines report, said outright rather than left
	// to whichever run happened to write r last.  They now mean what they
	// have always been called: the base script's memory, and the one-copy
	// script's.
	r.Base, r.Test = baseMem, testMem
	return size, padding, headroom, marginal, marginalOK
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

	// Two searches share every round when --extra is asked for, so the
	// lease has to carry both.  Set before anything is leased.
	if flags.Extra > 0 {
		searchesAtOnce = 2
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
		m := testModel(flags.Test)
		if b, err = openLease(func(n int) (backend, error) { return openModel(m, n) }); err != nil {
			errf("%v\n", err)
		}
		defer b.Close()
	case flags.Backend != "":
		var err error
		if b, err = openLease(func(n int) (backend, error) {
			return openBackend(flags.Backend, n)
		}); err != nil {
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
		places, cleanup, err := runIn(ctx, opts)
		if err != nil {
			errf("%v\n", err)
		}
		b = &runner{
			places: places, cleanup: cleanup,
			Timeout: flags.Timeout,
			Info:    flags.V >= 3, // -vvv: surface INFO: chat lines
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
	case flags.Extra < 0:
		errf("--extra %d: a count of further copies cannot be negative\n", flags.Extra)
	case flags.Parts < 2:
		// One part is not a division, and nought is not a number of
		// them.  Two is the bisection, which is allowed and is what
		// partSearch declines to duplicate.  It is also the smallest
		// lease there is: one object to measure in and one to probe in.
		errf("--parts %d: a round divides the range into at least two\n", flags.Parts)
	case flags.IPad != 0 && !expressiblePadding(flags.IPad):
		errf("--ipad %d: not an expressible padding -- a negative pad emits nothing, so it would be measured at a padding other than the one named\n", flags.IPad)
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

	size, basePad, pad, marginal, haveMarginal := oneMode(b, &r)

	// The answer, and nothing else unless asked.
	//
	// Two numbers is what a benchmark is for: what a copy of the code
	// costs, and what another one costs after it.  Everything else --
	// the readings they are differences of, the padding they are
	// anchored to, whose objects it all ran in -- is how the answer was
	// arrived at rather than the answer, and -v is how to ask.
	fmt.Printf("First Copy: %d\n", size)
	if haveMarginal {
		// A whole number or it is not a constant cost per copy.
		// Both readings are exact to the byte, so the only way this
		// divides unevenly is that the copies do not cost the same
		// as each other -- which is a fact about the code under
		// test and not a rounding error, and saying it as a
		// fraction is how it is visible at all.
		if marginal == math.Trunc(marginal) {
			fmt.Printf("Additional Copies: %d\n", int(marginal))
		} else {
			fmt.Printf("Additional Copies: %.4g\n", marginal)
			noticef("%d further copies cost %.4g bytes each, which is not a whole "+
				"number, so the copies do not all cost the same and there is no "+
				"one cost for an additional one\n", flags.Extra, marginal)
			if flags.Extra%4 != 0 {
				// Code is 4-aligned, so a copy whose true cost is
				// not a multiple of 4 is charged a little more or
				// less than its neighbours -- measured, copies of
				// llSin(1.0); cost 48, 48, 48, 44 repeating, which
				// is four copies of 47.  A multiple of four copies
				// averages that out; anything else divides the
				// alignment as well as the cost.
				noticef("code is 4-aligned, so individual copies quantise around "+
					"their real cost; --extra %d is not a multiple of 4, and one "+
					"that is would average the alignment out\n", flags.Extra)
			}
		}
	}

	if flags.V >= 1 {
		// How the answer was arrived at: the two readings it is the
		// difference of -- the base script at the padding, sitting
		// exactly on a block boundary, and the one-copy script at the
		// same padding -- the filler that copy can still carry without
		// spilling into the next block, and what the construct pays
		// once rather than per copy.
		fmt.Printf("Base mem: %d\n", r.Base)
		fmt.Printf("Result mem: %d\n", r.Test)
		fmt.Printf("Result pad: %d\n", pad)
		if haveMarginal && marginal == math.Trunc(marginal) {
			fmt.Printf("Shared: %d\n", size-int(marginal))
		}
		fmt.Printf("Padding: %d\n", basePad)
	}
}

// spentCompiles counts the scripts sent to SL's compiler and never started.
// It is kept apart from spentRuns because the two are asked for different
// reasons, not because they cost different amounts: measured, they cost nearly
// the same, which is the finding A9 turned on.
var spentCompiles int

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
	Base int
	Test int
	Size float64
}

type Cache struct {
	Count   int
	Padding int
}

// cache prevents us from running the exact same script twice.  It holds
// the READING, which is all the script says; everything else about a run
// is worked out from it here.
var cache = map[Cache]int{}

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

// spentRounds counts the ROUND TRIPS, which is the unit wall-clock time
// is paid in.  A run is what a benchmark costs the grid; a round is what
// it costs the person waiting.  They are the same number until something
// runs in parallel: the quartering search sends three scripts at once and
// waits for all three, and that is one round and three runs.
//
// It is the number to watch when asking what --parts buys: a wider
// search spends more runs to spend fewer rounds, and only one of those
// two is time.
var spentRounds int

// reportCost says what the benchmark spent, under --debug.  It is on stderr
// with the rest of the debug output so that a caller parsing the Size: line
// does not have to know about it.
func reportCost() {
	debugf("Spent %d runs (%d of them re-reads) in %d rounds, and %d compiles\n",
		spentRuns, spentRereads, spentRounds, spentCompiles)
}

// mustRun runs the script and treats any error as fatal.  Used where an error
// is not expected and not recoverable (padding search, zero-copy base run).
func mustRun(b backend, cnt, pad int, r *Results) {
	err := runScript(b, cnt, pad, r)
	if err != nil {
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

	// The filler emits exactly pad bytes more than pad 0 does, for every
	// pad from 0 upward.  Two pieces, at 5 bytes and 2:
	//
	//	pad 0	jump Z; @Z;
	//	pad 1	i + i + i;
	//	pad 2	jump Z; @Z; i;
	//	pad 3	i + i + i + i;
	//	pad 4	jump Z; @Z; i + i;
	//
	// An even pad spends the 5-byte jump/label pair and pad/2 of the
	// 2-byte terms; an odd one spends (pad+5)/2 terms and no pair.  Both
	// come to 5+pad bytes, so pad 0 is the pair on its own and every
	// count above it costs one byte more than the last.
	//
	// The pair being spent at pad NOUGHT is the whole of the difference
	// from what this used to do.  It used to be spent only when the pad
	// was odd, which made 1 and 3 cost the same 5 bytes as each other:
	// two pads that could not be told apart, a minimum pad of 5 to keep
	// the search above them, and every padding this program reported
	// carrying that 5 -- a range of [5, 517) where the block is [0, 512).
	// Moving the pair to the bottom costs the same 5 bytes in every
	// script, where a constant belongs, and buys back both.
	//
	// `integer i;` is in the harness rather than here, for the same
	// reason: it backs the chain, every script needs it, and a
	// declaration that came and went with the pad would be a step of its
	// own size at whichever pad it appeared.
	if pad >= 0 {
		terms := pad / 2
		if pad%2 != 0 {
			terms = (pad + 5) / 2
		} else {
			pb.WriteString("jump Z; @Z;\n")
		}
		if terms > 0 {
			pb.WriteString("i")
			for k := 1; k < terms; k++ {
				pb.WriteString("+i")
			}
			pb.WriteString(";\n")
		}
	}

	fmt.Fprintf(&buf, code, cnt, pad, pb.String())
	return buf.String()
}

// runScript takes one reading and works out what it means.
//
// What is cached is the READING and nothing else.  The script says one
// number and everything else is arithmetic done here, so a cache hit and
// a run are the same thing to everything downstream -- which they were
// not while the base lived in the object's linkset data, because a hit
// sent no script and so wrote no base, and the two drifted apart.
func runScript(b backend, cnt, pad int, r *Results) error {
	key := Cache{Count: cnt, Padding: pad}
	mem, ok := cache[key]
	if ok {
		debugf("Using cache for %v\n", key)
	} else {
		spentRuns++
		spentRounds++
		script := buildScript(cnt, pad)
		if flags.V >= 3 {
			fmt.Println(script)
		}
		results, info, err := b.Send(script)
		if err != nil {
			return err
		}
		if flags.V >= 3 {
			for _, s := range info {
				fmt.Println("INFO:", s)
			}
		}
		mem, ok = absorbResults(results, r)
		if !ok {
			// The script ran and did not say what it read.  Whatever
			// happened -- a line lost on the way, a script that did not
			// get that far -- a reading of nothing is not a reading of
			// zero, and taking it as one is how a benchmark reports a
			// number nobody can tell is wrong.
			return fmt.Errorf("the script at count %d padding %d said nothing "+
				"about its memory", cnt, pad)
		}
		cache[key] = mem
	}

	if cnt == 0 {
		r.Base = mem
		return nil
	}
	r.Test = mem
	if r.Base != 0 {
		r.Size = float64(r.Test-r.Base) / float64(cnt)
	}
	return nil
}

// absorbResults reads what the script reported into a Results.
//
// The runner returns EVERY line the script said, not just the RESULT:
// ones, so the convention is applied here.  It belongs in the benchmark
// and not in the transport: nothing else about running a script
// requires a script to label its output.
func absorbResults(results []string, r *Results) (mem int, ok bool) {
	const MEM = "MEM="
	for _, raw := range results {
		s, found := resultPayload(raw)
		if !found {
			continue
		}
		if strings.HasPrefix(s, MEM) {
			n, err := strconv.Atoi(s[len(MEM):])
			if err != nil {
				continue
			}
			mem, ok = n, true
		}
	}
	return mem, ok
}

// code is the boilerplate for autobench.  It is printed with 3 positional
// parameters:
//  1. the number of times the CODE was repeated
//  2. the amount of padding added
//  3. instructions to pad the code size
//
// The title used to be a fourth, said by the script and read back out of
// what it said.  A script can only ever have repeated the --title it was
// handed, so the round trip could not produce a fact -- and it put the
// caller's text in the bytecode, which is why baseKey had to blank it
// before hashing a script to identify its shape.  main prints the flag.
//
// code is the benchmark script, and everything it does not do is
// deliberate.
//
// It says ONE number: what llGetUsedMemory answered.  It used to keep
// the base reading in the object's linkset data and divide against it in
// LSL, which made the base a piece of WORLD state -- so the object that
// held it was special, only a cnt=0 script that actually RAN could write
// it, and a cache hit here left the two disagreeing.  What that cost is
// on the record: a base one block low is 512/count on every size
// reported, silently, for half of all shapes.  The arithmetic is
// arithmetic; it belongs where it can be seen.
//
// The reading is taken FIRST, before anything is said, because building
// the strings to say it allocates.
//
// Nothing else varies.  There is no count or padding in it -- the
// program chose both and does not need telling -- so the harness is
// byte-for-byte identical in every script, which is what a measurement
// made by differencing two compiles wants.  The old one put both in as
// integer literals and branched on the count, so the base script and the
// test scripts were not quite the same program.
//
// The padding goes in a timer() that nothing starts.  Measured: code in
// an event that never fires counts towards llGetUsedMemory exactly as
// code that runs does -- padding in the timer, in state_entry and in a
// function all read 4388 -- and code that never runs cannot allocate,
// cannot take time, and cannot hit a limit however much of it there is.
//
// The count and the padding go in as a COMMENT.  Measured: 604 bytes of
// comment moved llGetUsedMemory not at all, so the digits cost nothing
// and the compiled harness stays byte-for-byte identical however many
// copies or however much padding this run happens to want.  What reads
// them is anything standing in for Second Life -- see scripttest's
// Harness -- and anybody looking at --show.
//
// Printed with four positional parameters:
//  1. the copy count
//  2. the padding
//  3. statement to print title (if any)
//  4. instructions to pad the code size
var code = `
// autobench cnt=%d pad=%d
default {
    state_entry() {
        integer mem = llGetUsedMemory();
        llOwnerSay("\n");
        llOwnerSay("RESULT:MEM=" + (string)mem);
        llOwnerSay("DONE");
    }
    timer() {
        integer i;
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

// runIn gets somewhere to run scripts: as many places as the search will
// use at once, each with the session that reaches it.
//
// See automate's for why the objects are worn and kept.  Running several
// at once is this program's own reason: a padding search is a dozen
// readings of one script that do not depend on each other, and N places
// can take N of them at a time.
func runIn(ctx context.Context, o session.Options) ([]place, func(), error) {
	// N places to run scripts in, and no opinion about whose they are.
	// A benchmark that wants 32 of them is asking for more than any one
	// avatar has, and there is no reason it should have to know that:
	// the readings are independent, the arithmetic is done here, and
	// what the pool grants is places.
	//
	// Naming an avatar still gets that avatar's, because UseAutoSpread
	// takes --agent to mean it.
	//
	// (One thing this makes possible and nobody has measured: Second
	// Life runs different simulator versions on different channels, so
	// two avatars can be in regions with two LSL compilers.  Whether
	// that moves a reading is unknown -- and the same doubt already
	// applied between one run and the next.)
	// As many places as the searches would like, halving until the pool
	// can grant it; see openLease for why fewer is slower rather than
	// fatal.  The last attempt asks for one, which every avatar has.
	want := leaseSize()
	var as []*session.Auto
	var err error
	for n := want; n >= 1; n = n / 2 {
		if as, err = session.UseAutoSpread(ctx, o, n); err == nil {
			if n < want {
				noticef("holding %d objects rather than %d; the searches will "+
					"share fewer rounds\n", n, want)
			}
			break
		}
		debugf("could not hold %d objects: %v\n", n, err)
	}
	if err != nil {
		return nil, nil, err
	}

	var places []place
	for _, a := range as {
		for _, obj := range a.Objects {
			places = append(places, place{a.Session, obj})
		}
	}

	// Which avatars, when nobody said, at -vv.
	//
	// It used to be unconditional, on the grounds that a benchmark
	// attributed to the wrong avatar is not an error but a plausible
	// number.  A grant now spans several avatars as a matter of course,
	// so it is several lines of it -- and the readings turned out to
	// agree across avatars: every row of the --parts sweep in
	// doc/memory.md reported the same Size over one avatar, two and
	// three.  Which is not a proof, but it is enough that whose objects
	// these were is a detail rather than a caveat on the answer.
	if o.Agent == "" && flags.V >= 2 {
		for _, a := range as {
			fmt.Fprintf(os.Stderr, "running as %s, objects %s\n", a.Agent, a.Where())
		}
	}

	// One release for the whole grant however many avatars it covers, so
	// releasing any of them releases all of them; the rest are no-ops.
	return places, func() {
		for _, a := range as {
			a.Release()
		}
	}, nil
}
