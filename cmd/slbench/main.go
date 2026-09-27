// Command slbench measures the memory cost of LSL constructs.
//
// It began as the slgo build of the slrun program of the same name, with
// scripts going into Second Life through sl rather than through slrund. The
// measurement machinery -- the padding search and the block-boundary
// arithmetic -- is about LSL and not about how a script reaches the grid.
//
// # The transport
//
// Scripts run in objects the daemon's pool lends, as many as the search can
// use at once and from as many avatars as it takes, each script replacing
// the last inside its object (runner.go).  --backend runs them through a
// script.v1 backend instead (script.go), and --test through the offline
// model in this process (model.go); backend.go is what all three answer.
//
// Two things are worth knowing before reading a number off this:
//
//   - Nothing in Second Life decides a script has finished. The script says
//     DONE, and whatever runs it waits for that sentinel, up to --timeout.
//   - A script says one number, what llGetUsedMemory answered, and every size
//     is arithmetic done here on two of them, so a reading means the same in
//     whichever object it was taken. See probe.go.
//
// # The code under test
//
// Two slots in the generated script.  Above the harness go the declarations
// and functions -- --code takes them, --statement takes a single statement
// and wraps a function around it, and a lone operand or standard input is a
// file of them.  After the harness go states, which --states takes, because
// that is where LSL puts every state but the one a script starts in.
//
//	slbench --code "integer gCNT;"
//	slbench --statement "llSin(1.0);"
//	slbench bench.lsl
//	slbench < bench.lsl
//	generate-it | slbench
//	slbench --states "state sCNT { state_entry() { } }"
//
// CNT in either slot becomes the copy number, which is what makes cnt copies
// of a thing that has to be named: two states called s will not compile.
//
// With nothing naming the first slot the code is read from standard input --
// but only when standard input is not a terminal.  A bare slbench at a
// prompt is somebody who has not said what to measure, and a program that
// answered it by waiting silently for typing would look like one that had
// hung.  A lone "-" says standard input in so many words, and works at a
// terminal too.
//
// Every flag that takes LSL takes the name of a file holding it instead,
// spelt as a path: "/x.lsl", "./x.lsl" or "../x.lsl".  That is --code,
// --statement, --states, --preamble and --postamble.  See lslOrPath for why
// it is the prefix that decides and not anything about what LSL looks like.
//
// The flags go in front of the file.  Option parsing stops at the first
// argument that is not a flag, and here that argument is the file, so
// anything after it is read as a second file rather than as an option.
//
// Flags that are gone, and where they went:
//
//	--sim                the eLSL simulator, which lives in elsl
//	--lsl, --chatlog     the viewer slot file and chat log, from before slrund
//	--runscript          slrund's slot transport; there is one transport now
//	--slot, --prim       named a slot in somebody's object; the pool lends objects now
//	--object, --rez      a named object or a throwaway prim, from before the pool
//
// The rest, copy mode's among them, are in doc/memory.md#one-mode.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pborman/getopt/v2"
	"github.com/pborman/options"
	"golang.org/x/term"

	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/internal/version"
	"github.com/quark-idlemind/slgo/scripttest"
)

var flags = struct {
	Preamble  string          `getopt:"--preamble=PREAMBLE text placed before the copies, or ./FILE holding it"`
	Postamble string          `getopt:"--postamble=POSTAMBLE text placed after the copies, or ./FILE holding it"`
	Code      string          `getopt:"--code=CODE code to test, or ./FILE holding it"`
	Statement string          `getopt:"--statement=CODE statement(s) to test, or ./FILE holding them"`
	States    string          `getopt:"--states=CODE states after the default state, or ./FILE holding them"`
	Addr      string          `getopt:"--addr=HOST:PORT the slgod to attach to; default sl-host, or this machine"`
	Agent     string          `getopt:"--agent=NAME -a the profile to use; the only one, by default"`
	Direct    bool            `getopt:"--direct -d log in to Second Life directly, without slgod"`
	First     string          `getopt:"--first=NAME the avatar's first name, for --direct"`
	Last      string          `getopt:"--last=NAME the avatar's last name, for --direct"`
	Start     string          `getopt:"--start=WHERE where to arrive: last, home, or a region, for --direct"`
	V         options.Counter `getopt:"-v say more: once for the readings behind the answer, twice for whose objects it ran in, three times for the scripts themselves"`
	Params    []string        `getopt:"--params=NAME,... parameters used with --statement"`
	Locals    []string        `getopt:"--locals=NAME,... locals used with --statement"`
	Globals   []string        `getopt:"--globals=NAME,... declare globals"`
	Extra     int             `getopt:"--extra=N also measure N further copies, giving what each copy after the first costs; a multiple of 4"`
	IPad      int             `getopt:"--ipad=N measure at this base padding instead of searching for one; confirmed before it is used"`
	Paranoid  bool            `getopt:"--paranoid read each crossing again before believing it"`
	Debug     bool            `getopt:"--debug enable debugging"`
	Probe     bool            `getopt:"--probe send a simple script to LSL as a probe"`
	NoCache   bool            `getopt:"--no-cache do not remember or reuse the padding for this base script"`
	Parts     int             `getopt:"--parts=N cut the padding search into N parts a round, running N-1 scripts at once; a power of 2 divides the block evenly, but any N works"`
	Timeout   time.Duration   `getopt:"--timeout=DUR timeout on waiting for an LSL script to complete"`
	Test      string          `getopt:"--test=PAD,SIZE[,MARGINAL[,LIMIT]] measure against the offline model in this process, see the source code"`
	Backend   string          `getopt:"--backend=HOST:PORT run scripts through a script.v1 backend there -- a simulator or a viewer daemon -- instead of in Second Life; --test is the same contract answered by a model here"`
	Help      bool            `getopt:"--help -h show this message"`
	Version   bool            `getopt:"--version say which build this is, and exit"`
}{
	Start:   "last",
	Timeout: time.Minute,
	// Eight parts, which is the measured knee: 8*8*8 is 512 exactly, so
	// three rounds of seven scripts land on the byte with no round
	// wasted.  Sixteen parts takes the same three rounds for ten more
	// scripts, and thirty-two saves a round and measured slower.
	// Why: doc/memory.md#what---parts-buys-and-where-it-stops
	Parts: 8,

	// Eight further copies.  Code is 4-aligned, so individual copies
	// quantise around their real cost and only a multiple of four
	// averages it out; four is the smallest that does and eight is the
	// smallest that is not obviously the smallest -- it halves whatever
	// the offset is on top of that, and costs nothing in rounds.
	// Why: doc/memory.md#n-should-be-a-multiple-of-4
	Extra: 8,
}

// leaseSize is how many objects a benchmark holds.
//
// --parts for each search that shares a round -- its anchor and its
// parts-1 divisions -- and three more: one to measure in, and the two
// readings that confirm a remembered padding.  That is what lets
// warmTheSearch put the confirmation and the opening of every search
// into a single round.  At the defaults it is nineteen.
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
// What it would like is leaseSize.  None of that is necessary: with fewer
// places the searches share rounds less, and with one place there is no
// parallel search at all and the block is bisected -- nine rounds instead
// of three, and the same answer.
//
// So it halves rather than refusing.  A benchmark that cannot have the
// nineteen objects the defaults ask for should be slower, not impossible,
// and a backend that grants four is a real case rather than a
// hypothetical one.
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
// search and the block arithmetic -- is about LSL and about readings, and
// none of it needs Second Life to be exercised.  This is how it is
// exercised without one.
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

// inputComplaint says what is wrong with how the code under test was
// named, or "" if nothing is.  The three ways in -- --code, --statement
// and a file -- are one input by different routes, so naming two of them
// is a line that means two things.
//
// It is a function rather than a switch inside main so that a test can
// read what it says: every arm ends in errf, and errf ends in os.Exit.
//
// piped says whether standard input is something to read rather than a
// terminal.  It is the fourth way in, and the only one nobody types: with
// no --code, no --statement and no file, the code comes from there.  A
// terminal is not one, because a bare slbench at a prompt has not said
// what to measure, and answering that by waiting for typing is
// indistinguishable from having hung.
func inputComplaint(args []string, code, statement, states string, piped bool) string {
	switch {
	case len(args) > 1 && strings.HasPrefix(args[1], "-"):
		// Option parsing stopped at the file, so what follows it was
		// never read as a flag.  Telling somebody who gave one file and
		// a flag that they gave two files describes a line they did not
		// type.
		return fmt.Sprintf("The flags go before the file: option parsing stops at the file, so %q was read as a second one", args[1])
	case len(args) > 1:
		return "At most 1 test file may be specified"
	case len(args) < 1 && code == "" && statement == "" && states == "" && !piped:
		return "Either --code, --statement, --states, a file or something on standard input must be specified"
	case len(args) == 1 && code != "":
		return "Only one of --code or a file may be specified"
	case len(args) == 1 && statement != "":
		return "Only one of --statement or a file may be specified"
	case code != "" && statement != "":
		return "Only one of --code or --statement may be specified"
	}
	return ""
}

// lslOrPath answers with the LSL in s, or with the contents of the file
// s names.
//
// --code, --statement, --states, --preamble and --postamble each take LSL
// on the command line, and each of them is a thing somebody keeps in a
// file: there is one operand and there are five slots, so without this the
// only slot that could be read from a file is whichever one the operand
// fills.
//
// A value beginning "/", "./" or "../" is a path, and nothing else is.
// No LSL begins with any of those -- with the one exception that decides
// the rule: "//" opens a comment, so "// what this measures" is code and
// not a file at the root of the disk.  Testing for the absence of a
// semicolon instead would have read "state sCNT { state_entry() { } }"
// as a filename, and that is the shape --states exists to measure.
func lslOrPath(what, s string) string {
	if !looksLikePath(s) {
		return s
	}
	data, err := os.ReadFile(s)
	if err != nil {
		errf("%s: %v\n", what, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		errf("%s: %s is empty\n", what, s)
	}
	return string(data)
}

// looksLikePath is the whole of the rule.  See lslOrPath.
func looksLikePath(s string) bool {
	switch {
	case strings.HasPrefix(s, "//"):
		return false
	case strings.HasPrefix(s, "/"), strings.HasPrefix(s, "./"), strings.HasPrefix(s, "../"):
		return true
	}
	return false
}

// readCode reads the code under test: from the file named, or from
// standard input when nothing names one or the name is "-".
//
// Empty is refused whichever route it came by.  An empty file measured
// as though it were a benchmark, and printed the two numbers an empty
// benchmark costs, which is a report of nothing that looks exactly like
// a report of something; --code "" has always been refused, and these
// are the same input by different routes.
func readCode(args []string) string {
	from, read := "standard input", func() ([]byte, error) { return io.ReadAll(os.Stdin) }
	if len(args) == 1 && args[0] != "-" {
		from, read = args[0], func() ([]byte, error) { return os.ReadFile(args[0]) }
	}
	data, err := read()
	if err != nil {
		errf("%v\n", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		errf("%s held nothing to measure\n", from)
	}
	return string(data)
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

// searchCounts is what a benchmark searches for at the base padding: the
// one-copy script, and the one with --extra further copies when there is
// one.
//
// In one place because oneMode, which runs the searches, and
// warmTheSearch, which carries their openings in the confirmation's
// round, have to agree about it: a search the warmer does not carry
// opens a round of its own.
func searchCounts() []int {
	counts := []int{1}
	if flags.Extra > 0 {
		counts = append(counts, 1+flags.Extra)
	}
	return counts
}

// warmTheSearch sends, in ONE round, the two readings that confirm a
// remembered padding and the readings the benchmark will want NEXT if it
// holds.
//
// It can, because what follows is fixed: if the remembered padding
// stands, oneMode opens a search at it for each of searchCounts, each
// reading its anchor and a first round of partPads(0, blockSize, parts)
// above it.  None of that depends on anything not already known.
//
// It is a bet placed on the padding HOLDING: when it does not, the
// readings carried are wasted and the search opens a round of its own.
// Why: doc/memory.md#confirming-a-remembered-padding
func warmTheSearch(b backend, remembered int) {
	parts := usableParts(b, flags.Parts)
	if parts == 0 {
		// Nowhere to run them in parallel, so there is no round to
		// share and every reading sent here would be a round of its
		// own -- which is the cost this exists to avoid.
		return
	}

	// The question being asked, then the answers wanted if it comes back
	// yes: every search's anchor and every search's first round.  A
	// reading already known costs nothing -- probeReadings reads the
	// cache first -- and leaseSize holds exactly this many, which is
	// what it is for.
	want := []reading{{0, remembered}, {0, remembered + 1}}
	first := partPads(0, blockSize, parts)
	for _, c := range searchCounts() {
		want = append(want, reading{c, remembered})
		for _, p := range first {
			want = append(want, reading{c, remembered + p})
		}
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
// memory first grows.  Both callers use the offset as it is.
//
// It also returns the reading at pad, which is the bottom of the step it found.
// The caller cannot keep that reading itself: every run of cnt copies writes
// its reading into r, and a search whose anchor turns out to have moved is run
// again against the corrected one, so a value read before the search can be
// stale by the time it returns.  See oneMode.
//
// It narrows the block a round at a time with readings taken together
// (partSearch), bisects whatever that leaves, then walks from where that left
// off so the answer is exact to the byte, and checks the crossing against
// every reading it took (brokenStair).  Under --paranoid it reads the crossing
// again as well -- see confirmCrossing for why that is worth paying for.
//
// Every run writes r, so on return r holds the reading from the crossing run --
// one block above the reading at pad, which is the step oneMode measures.
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
				"the same thing twice, and every size slbench reports is a difference "+
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
// It is a function of its own because warmTheSearch, warmPaddings and
// searchPadding have to ask the same question and get the same answer:
// they send the pads a search WOULD ask for before it runs, and a pad
// guessed differently is a run spent on nothing.
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

// searchPadding is one attempt at findPadding: narrow the block, walk to the
// exact byte, check the crossing.  ok is false when a reading the search
// relied on turned out not to hold -- the base itself, or one that does not
// fit the staircase -- in which case base is the reading to search against
// next and the whole search has to be redone against it.
func searchPadding(b backend, cnt, pad int, r *Results, getBase func() int) (offset, base int, ok bool) {
	// The anchor and the first round's divisions in ONE round.
	//
	// Cutting a range into N parts takes N readings, not N-1: the N-1
	// dividers and the reading at the anchor itself.  Without the anchor,
	// dividers that all read the same leave the step ambiguous -- it
	// could be below the first or above the last -- and with it, all the
	// same means the step is in the last part.  partSearch needs the base
	// to compare, not to ASK: the pads are fixed and the comparison
	// happens once the answers are in hand.
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
// A padding search is a chain of comparisons against readings of
// llGetUsedMemory, and a reading one block high looks precisely like the memory
// having grown, which is the event the search exists to find.  It is asked
// only under --paranoid.
// Why: doc/scripttest.md#a-reading-one-block-high
//
// A crossing is a PAIR of readings, so confirming it means confirming both ends
// and the base they are compared against:
//
//	pad+low-1  must still read the same as the base   (inside the block)
//	pad+low    must still read differently            (outside it)
//	pad        must still read what it read           (the base itself)
//
// Two or three runs, in one round.  What it buys is that a single bad reading
// costs a re-read instead of an answer.
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

// noticef reports something a caller reading the answer off stdout would
// otherwise never learn: a reading that was not reproducible, and what was
// done about it.
//
// It is unconditional, unlike debugf: a benchmark that had to argue with its
// instrument should say so whether or not anybody asked for --debug.  stderr,
// so that a caller parsing stdout is unaffected.
func noticef(format string, v ...any) {
	fmt.Fprintf(os.Stderr, "slbench: "+format, v...)
}

// basePadding returns the base padding: the largest pad the copy-free base
// script can carry and still fit inside its 512-byte memory block, so that one
// more byte crosses into the next.  Everything slbench measures is anchored
// to it, it is what the Padding: line reports, and it is what --ipad
// names.
//
// It is a property of the benchmark's *shape* -- the harness plus --preamble,
// --postamble and --globals -- and not of the code under test, so every
// benchmark built on the same shape has the same base padding.  Measuring it
// costs a search of several rounds, which is why --ipad exists: measure it
// once, read it off the Padding: line, and hand it to every later run of that
// shape.  That is the flag's purpose and the reason it skips the search.
//
// oneMode runs at this padding, not a byte past it.
func basePadding(b backend, r *Results) int {
	// --ipad names the padding to measure at: the caller has run this shape
	// before, or read one out of doc/memory.md, and is telling us what it is.
	//
	// It is confirmed rather than taken, exactly as a remembered one is, and
	// for the same reason: a padding wrong by k reports every size wrong by k
	// with nothing in the output to show it.  Always: the two readings ride
	// in a round that is being spent anyway.
	if flags.IPad != 0 {
		warmTheSearch(b, flags.IPad)
		checkIPad(b, flags.IPad)
		return flags.IPad
	}

	// A padding already found for this base script is worth two runs to
	// confirm and saves a search.  It is confirmed rather than trusted:
	// what SL's compiler does can change, and a padding that is wrong by k
	// reports every size wrong by k with nothing in the output to show it.
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

// checkIPad confirms that a supplied --ipad names a padding boundary.
//
// Always, not on request: the two readings ride in a round that is being
// spent anyway, so there is nothing left for a flag to decide.
//
// A padding is the largest pad that still fits inside a 512-byte block, so the
// test is that one more byte crosses: run the base script -- no copies of the
// code under test -- at pad and at pad+1, and require the memory to grow.  Two
// live runs, against the dozen the search costs.
//
// This is exactly the pair of runs the search itself ends on, so the value it
// confirms is the value Padding: prints: hand a reported padding straight back
// to --ipad and it passes.
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

Every size slbench reports is measured from this constant, so a padding that
is off by k reports every Size off by k, in the same direction, with nothing in
the output to show it.

Measure somewhere else, or find the padding by leaving --ipad off.
`, pad, pad, at.Base, pad+1, above.Base)
}

// expressiblePadding reports whether the filler can emit exactly pad bytes --
// and exactly pad+1 as well, since that is where the runs are taken.
//
// It is not a judgement about whether pad names a boundary; that is the
// caller's to assert, and checkIPad is what verifies it.  This
// is the one thing slbench does know better than the caller: whether it can
// carry out the instruction at all.
//
// The filler emits 5+pad bytes for every pad from nought up: an even one
// as the 5-byte jump/label pair plus pad/2 of the 2-byte +i terms, an odd
// one as (pad+5)/2 terms and no pair.  So every non-negative count is
// expressible, to the byte, and a negative one is not -- it emits nothing
// at all, which would measure at a padding other than the one named and
// report it as the one named.
// Why: doc/memory.md#what-this-replaced
//
// There is deliberately no upper bound.  Boundaries repeat every blockSize
// bytes, so one shape has many paddings and a caller may legitimately name any
// of them.
// Why: doc/memory.md#a-padding-a-block-up
func expressiblePadding(pad int) bool { return pad >= 0 }

// oneMode is the benchmark: one copy of CODE, measured as the memory it added
// rounded up to a whole block, less the part of that last block it did not
// use.  It returns the size to report, the padding to report, the headroom
// left above the copy, which is printed as "Result pad:", and with --extra
// what each further copy costs.
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
// There is one pad and the runs happen AT it, not a byte past it.
// Why: doc/memory.md#one-mode
//
// It is a function rather than a block inside main so that a test can drive it
// against the offline model and no Second Life at hand.  See slbench_test.go.
func oneMode(b backend, r *Results) (size, padding, headroom int, marginal float64, marginalOK bool) {
	// BASEPAD: the most filler the base script carries without spilling.
	// This is the number Padding: reports and --ipad takes, and the runs
	// happen at it: it is the pad the answer is defined against.
	padding = basePadding(b, r)

	// BASEMEM: where the boundary under that padding is.
	//
	// Into a Results of its own, not r: every run of the base script
	// writes its reading into r.Base, so one kept there lasts only until
	// the next.
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
	warmPaddings(b, searchCounts(), padding)

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
	// It is the number copy mode existed to produce, got here from a
	// script with a handful of copies in it rather than one with up to 512.
	// Why: doc/memory.md#what-a-copy-after-the-first-costs-without-a-big-script
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

	// The operand is the file to measure.  Without this the usage line
	// offers "[parameters ...]", which names nothing this program takes
	// and reads as a plural of --params, so the file -- one of the three
	// ways to say what to measure -- appeared in no listing of them.
	getopt.SetParameters("[FILE]")

	// --help before anything is decided, so that asking what the flags
	// are never leases an object or dials anything.
	//
	// It has to be a flag of its own: without one, getopt treats --help
	// as an option it has never heard of, which prints this same usage
	// with "unknown option: --help" on the front of it and exits 1 --
	// an error report for somebody who asked the question correctly.
	// To stdout, as slrun does it, because here it is the answer and
	// not a complaint.
	if flags.Help {
		getopt.PrintUsage(os.Stdout)
		return
	}
	if flags.Version {
		fmt.Println(version.String("slbench"))
		return
	}

	// Two searches share every round when --extra is asked for, so the
	// lease has to carry both.  Set before anything is leased.
	if flags.Extra > 0 {
		searchesAtOnce = 2
	}

	// Everything that can refuse the line goes here, in front of the
	// session and the lease, so that a line that cannot be run -- a bare
	// "slbench" among them -- never dials slgod or leases an object.
	//
	// --probe asks whether scripts run at all, and measures nothing, so
	// it is the one run with no code to be told about.
	if !flags.Probe {
		// Standard input is a source of code only when it is not a
		// terminal.  See inputComplaint.
		piped := !term.IsTerminal(int(os.Stdin.Fd()))
		if c := inputComplaint(args, flags.Code, flags.Statement, flags.States, piped); c != "" {
			errf("%s\n", c)
		}
		// A flag that takes LSL takes the name of a file holding it.
		flags.Code = lslOrPath("--code", flags.Code)
		flags.Statement = lslOrPath("--statement", flags.Statement)
		flags.States = lslOrPath("--states", flags.States)
		flags.Preamble = lslOrPath("--preamble", flags.Preamble)
		flags.Postamble = lslOrPath("--postamble", flags.Postamble)
		switch {
		case flags.Statement != "":
			flags.Code = flags.Statement
		case len(args) == 1:
			flags.Code = readCode(args)
		case flags.Code == "" && flags.States == "" && piped:
			// Standard input is read only when nothing else says what
			// to measure.  A --states run with a pipe left open behind
			// it has said what to measure already.
			flags.Code = readCode(nil)
		}
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

	// What this benchmark cost, in the unit the cost is paid in.  Deferred so
	// it is reported even when the benchmark ends in a panic -- a run that
	// failed still spent everything it spent.
	defer reportCost()

	// Where the scripts run.  The choice is made once, here, and nothing
	// above backend.go knows which was made: a benchmark is arithmetic on
	// readings, and where the readings come from is a transport.  --test
	// is a backend like the others, reached in this process, so the
	// offline path runs the same code the live one does.
	// Why: doc/scripttest.md#slbenchs---test-is-a-backend
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
			Channel: "slbench",
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
// Nothing in a benchmark sends one now -- Compile is asked only by the live
// tests -- so it stays at nought.  It is kept apart from spentRuns because the
// two are asked for different reasons, not because they cost different
// amounts: measured, they cost nearly the same (TestLiveCompileIsNotRunning).
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

// Results holds the readings from the most recent runs: Base from the last run
// of the base script, Test and Size from the last run with copies in it, cache
// hits included (see runScript).  So a reading that has to outlive the next run
// of its count belongs in a local, not in a field here; oneMode keeps its base
// memory that way.
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
// runs in parallel: a round of the part search sends parts-1 scripts at once
// and waits for all of them, and that is one round and parts-1 runs.
//
// It is the number to watch when asking what --parts buys: a wider
// search spends more runs to spend fewer rounds, and only one of those
// two is time.
var spentRounds int

// reportCost says what the benchmark spent, under --debug.  It is on stderr
// with the rest of the debug output so that a caller parsing the answer on
// stdout does not have to know about it.
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
// and the postamble, then the harness with pad bytes of filler in it, then cnt
// copies of the states.
//
// It is a function of (cnt, pad) and the flags and of nothing else -- no world,
// no cache, no reading -- which is what lets Compile be asked about a script
// without running it, as the live tests do.  The script it renders for a given
// (cnt, pad) is byte for byte the script runScript would send for that
// (cnt, pad), and it has to stay that way: a compile check that answers about a
// different script from the one that will run is worse than no check, because
// it is confident.
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
	// A builder rather than a string appended to in the loop below, which
	// copies the whole of it for every two bytes of the +i chain.  The
	// bytes emitted may not change.
	// Why: doc/scripttest.md#slbenchs---test-is-a-backend
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
	// count above it costs one byte more than the last.  The pair is spent
	// at pad NOUGHT so that it is a constant in every script.
	// Why: doc/memory.md#what-this-replaced
	//
	// `integer i;` is in the harness rather than here: it backs the chain,
	// every script needs it, and a declaration that came and went with the
	// pad would be a step of its own size at whichever pad it appeared.
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

	// The states go after the default state, which is where LSL wants
	// them: default is the state a script starts in, and the rest
	// follow it.  A copy each, CNT substituted as it is in the code
	// above, because two states of one name will not compile -- so
	// "state sCNT" is how a benchmark asks for cnt of them.
	//
	// Nothing at all when there are none.  A blank line per copy would
	// be a difference between the base script and the test scripts,
	// which is the one thing this function may not have.
	if flags.States != "" {
		for i := 0; i < cnt; i++ {
			fmt.Fprintf(&buf, "\n%s\n", strings.Replace(flags.States, "CNT", fmt.Sprintf("%03d", i), -1))
		}
	}
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

// absorbResults finds the reading in what the script said, and says
// whether there was one.  r is not written.
//
// A backend returns every line the script said, not just the RESULT:
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

// code is the benchmark script, printed with three positional parameters:
//  1. the copy count
//  2. the padding
//  3. the filler that pads the code size
//
// It says ONE number, what llGetUsedMemory answered, and the arithmetic is
// done here where it can be seen.  The reading is taken FIRST, before
// anything is said, because building the strings to say it allocates.
// Why: doc/scripttest.md#a-benchmark-script-says-one-number
//
// The harness is byte-for-byte identical in every compiled script, which is
// what a measurement made by differencing two compiles wants: the count and
// the padding go in as a COMMENT, which the compiler throws away, and are
// there for scripttest's Harness and for anybody reading the script at -vvv.
// Why: doc/scripttest.md#the-harness-line-is-a-comment
//
// The padding goes in a timer() that nothing starts: code in an event that
// never fires counts towards llGetUsedMemory as code that runs does, and it
// cannot allocate, take time or hit a limit however much of it there is.
// Why: doc/memory.md#the-filler-and-what-a-jump-costs
var code = `
// slbench cnt=%d pad=%d
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
// See slrun's runIn for why the objects are worn and kept.  Running
// several at once is this program's own reason: a padding search is many
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
	// Second Life runs different simulator versions on different
	// channels, so two avatars can be in regions with two LSL compilers;
	// measured once, their readings agreed.
	// Why: doc/memory.md#what-is-not-measured-here
	//
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

	// Which avatars, when nobody said, at -vv: readings have agreed across
	// avatars, so whose objects these were is a detail rather than a
	// caveat on the answer.
	// Why: doc/memory.md#what-is-not-measured-here
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
