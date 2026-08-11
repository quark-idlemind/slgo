package main

// Running a script through the script.v1 contract, when what runs it is
// not this program's own grid session.
//
// proto/script.proto is the seam: a backend is anything that can be
// handed a script and made to say whether it compiled, what it said,
// whether it faulted and whether it got to the end.  That is the whole of
// what automate wants of Second Life, which is why automate can be
// pointed at the eLSL simulator or a viewer daemon instead and print the
// same output.
//
// # Where a script runs, through a contract that has no prims
//
// The contract deliberately says nothing about rezzing or about naming an
// object: a script runs inside one, the backend supplies it, and how it
// came to exist is the backend's business.  So --object cannot be
// honoured here and is refused rather than quietly ignored -- a person
// who named an object meant that object.
//
// What is left is the two ways the contract offers of saying where:
//
//   - A lease of one target, held for the whole run.  Every script goes
//     in the same object, which is what the grid path does and what a
//     series of scripts that leave things for one another needs.
//   - An empty target, which means anywhere: the backend takes somewhere,
//     runs, and gives it back.  It holds nothing while a person reads the
//     output, and it is the whole of what a single script needs.
//
// The first is taken when there is more than one script to run or when
// --rez asks for a place of automate's own; otherwise the second.
//
// # Why this is not shared with autobench's copy
//
// The two ask different questions of the same contract.  A benchmark
// needs several objects at once, needs the one it measures in to keep
// what a script left there, and turns a fault into a size limit; automate
// needs one object or none and prints what it hears.  The overlap is the
// dial and a lease loop, and a package holding those two would be a
// package whose callers each ignore half of it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/quark-idlemind/slgo/proto/scriptv1"
)

// remote is a script.v1 backend and the place it gave us to run in.
type remote struct {
	c scriptv1.RunnerClient

	// target is where to run.  Empty is "anywhere", which is the
	// contract's own word for it: the backend takes somewhere, runs, and
	// gives it back.
	target string

	// shut is what closing has to undo, innermost first.  Cancelling the
	// lease is how the object goes back, and so is dying -- which is the
	// point of its being a stream at all.
	shut []func()
}

// openBackend dials a backend and gets somewhere to run.
//
// hold asks for a lease rather than a target of "anywhere", which is what
// several scripts on one command line need: they run in the order they
// were named, in the same object, and one that leaves something behind
// for the next finds it there.
//
// Insecure, and deliberately: the contract carries no credentials and the
// backends it is for are a simulator or a viewer daemon on this machine
// or a trusted one.  A backend that wants authentication puts something
// in front of it; inventing a scheme here would be inventing one nobody
// else implements.
func openBackend(addr string, hold bool) (*remote, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dialling the backend at %s: %w", addr, err)
	}
	r := &remote{c: scriptv1.NewRunnerClient(conn)}
	r.shut = append(r.shut, func() { conn.Close() })

	h, err := r.c.Health(context.Background(), &scriptv1.HealthRequest{})
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("asking the backend what it is: %w", err)
	}
	if !h.GetReady() {
		// Not an error the backend made: it is up and cannot run anything
		// yet -- a viewer not started, a session still logging in.  There
		// is nothing useful to do about that but say which of the two it
		// is.
		r.Close()
		return nil, fmt.Errorf("the %s backend is not ready to run anything: %s",
			h.GetBackend(), h.GetWhy())
	}
	if !hold {
		return r, nil
	}
	if err := r.lease(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// lease holds one object until this process ends.
func (r *remote) lease() error {
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := r.c.Lease(ctx, &scriptv1.LeaseRequest{
		Targets: 1, Agent: flags.Agent, Who: "automate",
	})
	if err != nil {
		cancel()
		return fmt.Errorf("asking for somewhere to run: %w", err)
	}
	for {
		ev, err := stream.Recv()
		if err != nil {
			cancel()
			return fmt.Errorf("waiting for somewhere to run: %w", err)
		}
		if q := ev.GetQueued(); q != nil {
			// Said out loud, because from outside a queue and a hang look
			// the same and one of them is worth waiting through.
			fmt.Fprintf(os.Stderr, "every group is busy; %d callers ahead\n", q.GetAhead())
			continue
		}
		g := ev.GetGranted()
		if g == nil {
			continue
		}
		if len(g.GetTargets()) == 0 {
			cancel()
			return fmt.Errorf("the backend granted a lease with nothing in it")
		}
		r.target = g.GetTargets()[0].GetId()
		// Which avatar, when nobody said.  The grid path says the same
		// thing for the same reason: with several hosted, the choice is
		// the far side's and the reader cannot work it out.
		if flags.Agent == "" && g.GetAgent() != "" {
			fmt.Fprintf(os.Stderr, "running as %s\n", g.GetAgent())
		}
		r.shut = append(r.shut, cancel)
		return nil
	}
}

// seconds is the timeout as the contract counts it.  Whole seconds is the
// grid's unit and is right for one; the rounding up is so that a caller
// asking for less than a second gets the shortest run the contract can
// express rather than the backend's default.
func seconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	if n := int64(d / time.Second); n > 0 {
		return n
	}
	return 1
}

// once runs one script and prints what it said, reporting whether it got
// to the end.
//
// Lines are printed as they arrive rather than at the end, which is what
// the streaming in the contract is for: a script that runs for a minute
// is one worth watching.  The sentinel itself is not output -- it is the
// script talking to us, not to the person reading -- and neither is the
// debug channel, where the region comments on the script rather than the
// script speaking.
func (r *remote) once(ctx context.Context, path, src string) bool {
	stream, err := r.c.Run(ctx, &scriptv1.RunRequest{
		Target: r.target, Name: flags.Script, Source: src,
		Done: flags.Done, TimeoutSeconds: seconds(flags.Timeout),
	})
	if err != nil {
		fmt.Printf("%s: %v\n", path, err)
		return false
	}

	var (
		compiled = true
		errs     []string
		fault    string
		finished bool
	)
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fmt.Printf("%s: %v\n", path, err)
			return false
		}
		switch {
		case ev.GetCompiled() != nil:
			compiled = ev.GetCompiled().GetOk()
			errs = ev.GetCompiled().GetErrors()
		case ev.GetLine() != nil:
			l := ev.GetLine()
			if l.GetDebug() || (flags.Done != "" && strings.Contains(l.GetText(), flags.Done)) {
				continue
			}
			fmt.Printf("%s: %s\n", path, l.GetText())
		case ev.GetFault() != nil:
			f := ev.GetFault()
			fault = f.GetScript() + ": run-time error"
			if f.GetReason() != "" {
				fault = f.GetScript() + ": " + f.GetReason()
			}
		case ev.GetFinished() != nil:
			finished = ev.GetFinished().GetSentinel()
		}
	}
	return verdict(path, compiled, errs, fault, finished)
}

// Close gives the object back and undoes whatever getting it took.
func (r *remote) Close() {
	for i := len(r.shut) - 1; i >= 0; i-- {
		r.shut[i]()
	}
	r.shut = nil
}
