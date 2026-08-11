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
// this process and reached over a pipe, and an offline benchmark runs the
// same code a live one does.
//
// The pipe is not ceremony.  Over it the messages are marshalled and the
// lease is a real stream, so the model exercises script.go rather than
// standing in for it; scripttest's own documentation makes the same
// argument at more length.  It costs about 90 microseconds a run
// measured here, against the 1 to 30 seconds a live one costs.

import (
	"context"
	"fmt"

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
	conn, err := s.Pipe()
	if err != nil {
		s.Stop()
		return nil, fmt.Errorf("serving the offline model: %w", err)
	}
	// No agent: the model has one and made the name up, so asking for a
	// particular avatar would be asking a question it cannot answer.
	r, err := openScript(context.Background(), conn, targets, "", "autobench --test", false)
	if err != nil {
		s.Stop()
		return nil, err
	}
	r.Timeout = flags.Timeout
	r.Info = flags.Show
	r.alsoClose(s.Stop)
	return r, nil
}
