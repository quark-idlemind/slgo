package main

// Taking several readings at once.
//
// # Why this is possible at all
//
// Nothing a script says identifies the script that said it: chat
// carries the object's name and key and no more.  Two scripts in one
// object are therefore indistinguishable, which is why the measured
// sequence runs one script at a time.
//
// Two OBJECTS are two speakers, and sl attributes what it hears by the
// object it came from, so probes in separate objects can run together
// and be told apart with nothing added to the scripts.  That is the
// whole of the trick: no key inserted into the source, and so nothing
// added to the size of the very thing being measured.
//
// # What may be probed, and what may not
//
// Never the measured object, and never for anything but the memory
// reading the search compares.
//
// The harness measures before it reads anything: the script calls
// result(llGetUsedMemory(), ...) as its first act, so the number it
// reports is the script's own memory and does not depend on which
// object it ran in.  That is what makes a probe in another object mean
// the same thing as a run in this one, for cnt>0 as much as for cnt=0.
//
// What DOES depend on the object is everything the script works out
// afterwards.  A cnt>0 script divides against the base reading its
// object holds in LINKSET DATA, and a spare object holds none, so its
// SIZE and BASE_MEM are arithmetic on a zero.  Those must not be
// allowed anywhere near the run cache, which copy mode reads Size from
// -- so a cnt>0 probe is kept in a cache of its own that only the
// search consults, holding the one number that travels.
//
// Probes also leave lsdPad alone, which names the last cnt=0 script to
// run IN THE MEASURED OBJECT.  copyMode forces a real run there when
// what it wants is not what lsdPad names, and that is what keeps a
// probe's reading from being mistaken for the base in world.

import (
	"sync"
)

// probeJob is one reading to take: which pad, and where its answer goes.
type probeJob struct{ at, pad int }

// probeMu guards the caches and the counters while probes run alongside
// each other.  Everything else in this program is sequential.
var probeMu sync.Mutex

// probeTest holds TEST_MEM for cnt>0 probes taken in spare objects.
//
// Apart from the run cache on purpose.  Everything else in that
// Results -- Size, Base -- is what the script worked out from linkset
// data the spare object does not have, and copy mode reads Size from
// the run cache.  Only the memory reading travels, so only it is kept.
var probeTest = map[Cache]int{}

// probeBase measures the base script at each of several pads, taking as
// many at a time as there are spare objects.
//
// It answers from the run cache where it can, so asking for a pad twice
// costs one run, and falls back to the ordinary sequential path for
// anything a probe could not do -- under --test, with no spare objects,
// or when a probe failed, which puts the error in front of the code
// that already knows what to do about it.
func probeBase(b backend, pads []int) []int { return probeAt(b, 0, pads) }

// probeAt measures a script of a given copy count at several pads at
// once, and returns the reading the search compares: BASE_MEM for the
// base script, TEST_MEM for one with copies in it.
func probeAt(b backend, cnt int, pads []int) []int {
	out := make([]int, len(pads))

	var todo []probeJob
	for i, pad := range pads {
		if m, ok := probed(cnt, pad); ok {
			out[i] = m
			continue
		}
		todo = append(todo, probeJob{i, pad})
	}
	if len(todo) == 0 {
		return out
	}

	if b.Spares() > 0 {
		todo = probeConcurrently(b, cnt, todo, out)
	}

	// Whatever is left goes the ordinary way: a run that failed, or a
	// benchmark with nowhere to run in parallel.
	for _, j := range todo {
		var r Results
		mustRun(b, cnt, j.pad, &r)
		out[j.at] = reading(cnt, r)
	}
	return out
}

// reading is what a search compares for this copy count.
func reading(cnt int, r Results) int {
	if cnt == 0 {
		return r.Base
	}
	return r.Test
}

// probed answers from whichever cache holds this count.
func probed(cnt, pad int) (int, bool) {
	probeMu.Lock()
	defer probeMu.Unlock()
	key := Cache{Count: cnt, Padding: pad}
	if r, ok := cache[key]; ok {
		return reading(cnt, r), true
	}
	if cnt != 0 {
		m, ok := probeTest[key]
		return m, ok
	}
	return 0, false
}

// probeConcurrently runs what it can in the spare objects and returns
// the jobs it did not manage.
func probeConcurrently(b backend, cnt int, todo []probeJob, out []int) []probeJob {
	var left []probeJob

	for start := 0; start < len(todo); start += b.Spares() {
		end := start + b.Spares()
		if end > len(todo) {
			end = len(todo)
		}

		var wg sync.WaitGroup
		failed := make([]bool, end-start)
		for k := start; k < end; k++ {
			j, spare := todo[k], k-start
			wg.Add(1)
			go func(slot int) {
				defer wg.Done()
				src := buildScript(cnt, j.pad)
				results, _, err := b.SendSpare(spare, src)
				if err != nil {
					// Not reported here.  Running it again the ordinary
					// way puts it in front of the code that knows which
					// failures mean try something smaller.
					failed[slot] = true
					return
				}
				var r Results
				absorbResults(results, &r)

				probeMu.Lock()
				spentRuns++
				if cnt == 0 {
					// A base script reports its own memory and nothing
					// else, so all of it travels.
					cache[Cache{Count: 0, Padding: j.pad}] = r
				} else {
					probeTest[Cache{Count: cnt, Padding: j.pad}] = r.Test
				}
				probeMu.Unlock()

				out[j.at] = reading(cnt, r)
			}(k - start)
		}
		wg.Wait()

		for k := start; k < end; k++ {
			if failed[k-start] {
				left = append(left, todo[k])
			}
		}
	}
	return left
}
