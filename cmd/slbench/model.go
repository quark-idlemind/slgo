package main

// --test: the offline model, served to this program as a backend.
//
// The measurement machinery is arithmetic on readings of
// llGetUsedMemory, so anything that can answer readings can drive the
// whole of it, and a model can answer readings.  The model is a script.v1
// backend like any other, started in this process and reached through
// scripttest's Direct rather than a connection, so an offline benchmark
// runs the same code a live one does: what openScript, runIn, sift and
// absorbResults do is written against scriptv1.RunnerClient, and that is
// an interface.
// Why: doc/scripttest.md#slbenchs---test-is-a-backend

import (
	"context"

	"github.com/quark-idlemind/slgo/internal/scripttest"
)

// openModel starts the offline backend in this process and takes a lease
// on it.
//
// targets is how many objects to hold, which openLease works out as it
// does for any other backend: readings taken in spare objects are what
// the part search spends, and a model that granted one object would
// quietly measure a benchmark the live path does not run.
func openModel(m scripttest.Memory, targets int) (backend, error) {
	s := scripttest.New(scripttest.Options{
		Backend: "slbench --test",
		Memory:  m,
		// One group of exactly what was asked for.  There is no queue to
		// model: this backend serves one caller, which is us.
		GroupSize: targets,
	})
	// No agent: the model has one and made the name up, so asking for a
	// particular avatar would be asking a question it cannot answer.
	r, err := openScript(context.Background(), s.Direct(), targets, "", "slbench --test", false)
	if err != nil {
		s.Stop()
		return nil, err
	}
	r.Timeout = flags.Timeout
	r.Info = flags.V >= 3
	r.alsoClose(s.Stop)
	return r, nil
}
