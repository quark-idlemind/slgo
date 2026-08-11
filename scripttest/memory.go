package scripttest

// What a benchmark script costs, and what it says when it runs.
//
// autobench measures LSL by asking Second Life how much memory a script
// uses, and everything it does above that -- the padding search, the
// block arithmetic, the copy count, the backing off when a script is
// refused -- is arithmetic on those readings.  So a backend that can
// answer the readings can drive the whole program, and that is what this
// is: the same staircase autobench's --test model uses, in a form a
// gRPC backend can serve.
//
// The model is deliberately a MODEL.  It says nothing about what Second
// Life would report; what it buys is that a caller's control flow can be
// exercised offline, which is the half of autobench that used to be
// reachable only with a grid at the other end.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Memory is what cnt copies of the code under test cost, as a function
// of the copy count and the padding.
//
// The fields are autobench's testInfo, which is where they were worked
// out and where the reasoning for each of them lives:
//
//   - Pad is the padding the zero-copy script needs to sit exactly on a
//     block boundary.  Everything else is measured from there.
//   - CodeSize is what the FIRST copy costs outright.
//   - Marginal is what each copy after the first costs, and is CodeSize
//     when left at zero.  The two differ whenever a construct pays a
//     cost once that the rest share -- measured live, a 250-character
//     string literal is 1044 bytes for one copy and 542 for each after
//     it, because identical literals are shared.  A model that could not
//     express that would assert the two are equal and quietly hold every
//     test written against it to the same assumption.
//   - Drift adds one byte every Drift copies, for a construct whose
//     per-copy cost is not quite constant.  Live, 32 of one construct
//     cost 11008 bytes and 16 cost 5516, which is 343.25 each; copy mode
//     has a guard for exactly that, and a model without drift can never
//     fire it.
//   - Limit is where the model refuses the script, standing in for the
//     compiler refusing one that is too large.
//
// Collide has no counterpart in testInfo and is here because the live
// behaviour has two size limits rather than one: above Collide the
// script compiles and then runs out of memory, above Limit the compiler
// will not take it at all.  Measured on Agni 2026-08-03, 256 copies of
// the reference shape compiled and then collided stack with heap, and
// 512 were refused outright.  A caller that backs off differently for
// the two -- autobench's runShrink does -- cannot be tested against a
// model that only has one.
type Memory struct {
	Pad      int
	CodeSize int
	Marginal int
	Drift    int

	// Limit is the reading above which the script will not compile.
	// Zero means DefaultLimit; a negative value means no limit.
	Limit int

	// Collide is the reading above which the script compiles and then
	// faults out of memory.  Zero means no such limit, which is the old
	// model exactly.
	Collide int

	// Anchor is where the staircase starts and Block is the step.  They
	// are fields rather than constants so a caller can model a different
	// quantisation, but nothing depends on the values: Anchor only has
	// to be larger than any pad it is asked about.
	Anchor int
	Block  int
}

// The model's defaults, which are autobench's constants.  5412 is a
// value that has been seen live, 512 is the block llGetUsedMemory
// reports in, and 64KB is what a Mono script has -- so a caller that
// names none of them gets a staircase whose refusal falls roughly where
// the live one does.
const (
	DefaultAnchor = 5412
	DefaultBlock  = 512
	DefaultLimit  = 64 * 1024
)

func (m Memory) filled() Memory {
	if m.Anchor == 0 {
		m.Anchor = DefaultAnchor
	}
	if m.Block == 0 {
		m.Block = DefaultBlock
	}
	if m.Limit == 0 {
		m.Limit = DefaultLimit
	}
	return m
}

// used is what cnt copies cost before quantising: the first copy
// outright, the marginal cost for each one after it, and the drift.
func (m Memory) used(cnt int) int {
	if cnt <= 0 {
		return 0
	}
	marginal := m.Marginal
	if marginal == 0 {
		marginal = m.CodeSize
	}
	used := m.CodeSize + (cnt-1)*marginal
	if m.Drift > 0 {
		used += cnt / m.Drift
	}
	return used
}

// Reading is what llGetUsedMemory would report for cnt copies at pad: a
// staircase in pad, one Block step every Block bytes, with the step for
// cnt=0 falling AT pad == Pad.
//
// It is exported because a test that drives a search through this
// backend usually wants to say what the answer should have been, and
// working the staircase out a second time in the test is how two copies
// of it drift apart.
func (m Memory) Reading(cnt, pad int) int {
	m = m.filled()
	return m.Anchor + ((m.used(cnt) + pad - m.Pad + m.Block) &^ (m.Block - 1))
}

// refuses reports whether the compiler would refuse this script, and
// with what, and collides whether it would run out of memory.
func (m Memory) refuses(cnt, pad int) (string, bool) {
	f := m.filled()
	if f.Limit < 0 {
		return "", false
	}
	if r := m.Reading(cnt, pad); r > f.Limit {
		// Worded like autobench's own model message, so a person reading
		// a failing test sees the same sentence either side of the seam.
		return fmt.Sprintf("model: %d copies at pad %d would use %d bytes, over the %d-byte limit",
			cnt, pad, r, f.Limit), true
	}
	return "", false
}

func (m Memory) collides(cnt, pad int) bool {
	return m.Collide > 0 && m.Reading(cnt, pad) > m.Collide
}

// harnessCall reads the copy count and the pad back out of a rendered
// benchmark script.
//
// autobench's buildScript emits both into the harness call, so a backend
// knows what it is being asked to run without being told separately --
// and a script whose harness call went missing is a script the benchmark
// could not read the answer from either.  cmd/autobench's protocol-level
// fake matches on the same line for the same reason; the convention is
// the script's, not either fake's.
var harnessCall = regexp.MustCompile(`result\(llGetUsedMemory\(\), (\d+), (-?\d+)\)`)

// Harness is the copy count and padding a benchmark script announces in
// its source, and whether it announced any.
func Harness(src string) (cnt, pad int, ok bool) {
	m := harnessCall.FindStringSubmatch(src)
	if m == nil {
		return 0, 0, false
	}
	cnt, _ = strconv.Atoi(m[1])
	pad, _ = strconv.Atoi(m[2])
	return cnt, pad, true
}

// transcript is the benchmark script speaking: the same labels in the
// same order as the harness in cmd/autobench, because the caller parses
// them and a transcript that differed would be testing the parser
// against itself.
//
// old is what the last cnt=0 run left behind, which live travels from
// the cnt=0 script to the cnt>0 ones through the object's linkset data.
func (m Memory) transcript(cnt, pad, mem, old int, done string) []string {
	out := []string{""}
	if cnt == 0 {
		out = append(out, fmt.Sprintf("RESULT:BASE_MEM=%d", mem))
	} else {
		out = append(out,
			fmt.Sprintf("INFO:COUNT=%d", cnt),
			fmt.Sprintf("RESULT:SIZE=%f", float64(mem-old)/float64(cnt)),
			fmt.Sprintf("RESULT:TEST_MEM=%d", mem),
			fmt.Sprintf("INFO:BASE_MEM=%d", old))
	}
	out = append(out,
		fmt.Sprintf("INFO:LAST_MEM=%d", mem),
		fmt.Sprintf("INFO:PADDING=%d", pad))
	if done != "" {
		out = append(out, done)
	}
	return out
}

// says matches an LSL call that makes the object speak, so that a script
// that is not a benchmark -- automate runs arbitrary ones -- still says
// what it was written to say.
//
// It is a regexp over the source and not a compiler: it finds a single
// string literal argument and nothing else, so a script that builds its
// output by concatenation says nothing here.  That is the judgement
// call.  Anything better is an LSL front end, which is what the
// simulator backend is for; what this has to cover is a test script
// written to print three lines and then say DONE, and for that a literal
// is what people write.
var says = regexp.MustCompile(`ll(?:Owner)?(?:Say|Shout|Whisper|RegionSay)\s*\(\s*(?:[^,()"]*,\s*)?"((?:\\.|[^"\\])*)"\s*\)`)

var unescape = strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\"`, `"`, `\\`, `\`)

// spoken is what a script with no benchmark harness in it would say.
func spoken(src string) []string {
	var out []string
	for _, m := range says.FindAllStringSubmatch(src, -1) {
		out = append(out, unescape.Replace(m[1]))
	}
	return out
}
