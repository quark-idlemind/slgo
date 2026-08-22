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
// afterwards.  A probe is a reading like any other now: the script says
// what llGetUsedMemory answered and nothing else, so where it ran does
// not change what the number means.
//
// It did.  A cnt>0 script used to divide against a base its own object
// held in LINKSET DATA, and a spare object holds none, so a probe's size
// and base were arithmetic on a zero and had to be kept in a cache of
// their own that copy mode would never read.  All of that went with the
// arithmetic.

import (
	"sync"
)

// probeJob is one reading to take: which pad, and where its answer goes.
type probeJob struct{ at, pad int }

// probeMu guards the caches and the counters while probes run alongside
// each other.  Everything else in this program is sequential.
var probeMu sync.Mutex

// probeBase measures the base script at each of several pads, taking as
// many at a time as there are spare objects.
//
// It answers from the run cache where it can, so asking for a pad twice
// costs one run, and falls back to the ordinary sequential path for
// anything a probe could not do -- a benchmark with no spare objects, or
// a probe that failed, which puts the error in front of the code that
// already knows what to do about it.
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
		out[j.at] = memOf(cnt, r)
	}
	return out
}

// probed answers from the cache, whatever the count.
//
// One cache for every reading, which it was not while a script did its
// own arithmetic: a probe taken in a spare object had no base in that
// object's linkset data to divide against, so its size and base were
// arithmetic on a zero and had to be kept somewhere copy mode would
// never look.  The script says one number now, and a reading is a
// reading wherever it was taken.
func probed(cnt, pad int) (int, bool) {
	probeMu.Lock()
	defer probeMu.Unlock()
	m, ok := cache[Cache{Count: cnt, Padding: pad}]
	return m, ok
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

		probeMu.Lock()
		spentRounds++
		probeMu.Unlock()

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
				mem, ok := absorbResults(results, &r)
				if !ok {
					// It ran and said nothing about its memory.  Left
					// for the sequential path, which will run it again
					// and report properly if it does it twice.
					failed[slot] = true
					return
				}

				probeMu.Lock()
				spentRuns++
				cache[Cache{Count: cnt, Padding: j.pad}] = mem
				probeMu.Unlock()

				out[j.at] = mem
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

// memOf is the reading a run left in a Results, whichever count it was.
func memOf(cnt int, r Results) int {
	if cnt == 0 {
		return r.Base
	}
	return r.Test
}
