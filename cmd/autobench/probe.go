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
// Only the base script, and never in the measured object.
//
// A cnt=0 script writes its reading into its object's LINKSET DATA,
// which is what the cnt>0 scripts divide against.  A probe dropped into
// the measured object would overwrite that; a probe in another object
// writes into another object's data, where nothing reads it.
//
// So probes are cnt=0, in spare objects, and their readings go into the
// run cache like any other.  They deliberately do not touch lsdPad,
// which names the last cnt=0 script that ran IN THE MEASURED OBJECT --
// and copyMode already forces a real run there when what it wants is
// not what lsdPad names, which is what keeps a probe's cache entry from
// being mistaken for the base in world.

import (
	"sync"
)

// probeJob is one reading to take: which pad, and where its answer goes.
type probeJob struct{ at, pad int }

// probeMu guards the run cache and the counters while probes run
// alongside each other.  Everything else in this program is sequential.
var probeMu sync.Mutex

// probeBase measures the base script at each of several pads, taking as
// many at a time as there are spare objects.
//
// It answers from the run cache where it can, so asking for a pad twice
// costs one run, and falls back to the ordinary sequential path for
// anything a probe could not do -- under --test, with no spare objects,
// or when a probe failed, which puts the error in front of the code
// that already knows what to do about it.
func probeBase(b *runner, pads []int) []int {
	out := make([]int, len(pads))

	var todo []probeJob
	for i, pad := range pads {
		if r, ok := cachedRun(0, pad); ok {
			out[i] = r.Base
			continue
		}
		todo = append(todo, probeJob{i, pad})
	}
	if len(todo) == 0 {
		return out
	}

	if useTestInfo == nil && len(b.spare) > 0 {
		todo = probeConcurrently(b, todo, out)
	}

	// Whatever is left goes the ordinary way: the model, a run that
	// failed, or a benchmark with nowhere to run in parallel.
	for _, j := range todo {
		var r Results
		mustRun(b, 0, j.pad, &r)
		out[j.at] = r.Base
	}
	return out
}

// probeConcurrently runs what it can in the spare objects and returns
// the jobs it did not manage.
func probeConcurrently(b *runner, todo []probeJob, out []int) []probeJob {
	var left []probeJob

	for start := 0; start < len(todo); start += len(b.spare) {
		end := start + len(b.spare)
		if end > len(todo) {
			end = len(todo)
		}

		var wg sync.WaitGroup
		failed := make([]bool, end-start)
		for k := start; k < end; k++ {
			j, obj := todo[k], b.spare[k-start]
			wg.Add(1)
			go func(slot int) {
				defer wg.Done()
				src := buildScript(0, j.pad)
				results, _, err := b.sendIn(obj, src)
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
				cache[Cache{Count: 0, Padding: j.pad}] = r
				probeMu.Unlock()

				out[j.at] = r.Base
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

// cachedRun reads the run cache under the probe lock.
func cachedRun(cnt, pad int) (Results, bool) {
	probeMu.Lock()
	defer probeMu.Unlock()
	r, ok := cache[Cache{Count: cnt, Padding: pad}]
	return r, ok
}
