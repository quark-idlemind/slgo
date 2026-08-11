package main

// --test: the offline model, served to this program as a backend.
//
// The measurement machinery is arithmetic on readings of
// llGetUsedMemory, so anything that can answer readings can drive the
// whole of it, and a model can answer readings.  That is what --test has
// always been.  What has changed is where it answers from.
//
// It used to answer ABOVE the transport: runScript had a branch that
// computed the model's number and returned it, having sent nothing.
// Everything the answer had to pass through on its way back from Second
// Life -- the compile refusal, the fault, the sift of what the object
// said, absorbResults -- was therefore skipped under --test and reachable
// only with a grid at the far end, which is to say reachable only by
// hand.  Now the model is a script.v1 backend like any other, started in
// this process, and an offline benchmark runs the same code a live one
// does.
//
// # Reached in process, and why that is not a step back
//
// This was a real connection at first -- scripttest.Pipe, marshalling
// and all -- on the argument that the model should exercise script.go
// rather than stand in for it.  It still does: what openScript, runIn,
// sift and absorbResults do is unchanged, because what they are written
// against is scriptv1.RunnerClient and that is an interface.  What the
// connection was buying on top of that was the transport's own
// behaviour, which is worth testing exactly once and is tested in
// scripttest against Pipe, and what it cost was 100 microseconds a run
// against 5 here.  A live run costs 1 to 30 seconds and would not care;
// a --test sweep of a hundred thousand of them cares a great deal, and
// paying it here bought nothing this program is responsible for.

import (
	"context"

	"github.com/quark-idlemind/slgo/scripttest"
)

// openModel starts the offline backend in this process and takes a lease
// on it.
//
// targets is how many objects to hold, and it is --objects like any other
// backend: readings taken in spare objects are what the quartering search
// spends, and a model that granted one object would quietly measure a
// benchmark the live path does not run.  The model's objects behave as
// the real ones do in the way that matters -- only a cnt=0 script that
// ran writes the base its own object's later readings divide against --
// so a probe dropped in the wrong place comes out wrong here too, which
// is the whole point of being able to run this offline.
func openModel(m scripttest.Memory, targets int) (backend, error) {
	s := scripttest.New(scripttest.Options{
		Backend: "autobench --test",
		Memory:  m,
		// One group of exactly what was asked for.  There is no queue to
		// model: this backend serves one caller, which is us.
		GroupSize: targets,
	})
	// No agent: the model has one and made the name up, so asking for a
	// particular avatar would be asking a question it cannot answer.
	r, err := openScript(context.Background(), s.Direct(), targets, "", "autobench --test", false)
	if err != nil {
		s.Stop()
		return nil, err
	}
	r.Timeout = flags.Timeout
	r.Info = flags.Show
	r.alsoClose(s.Stop)
	return r, nil
}
