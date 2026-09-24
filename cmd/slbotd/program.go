package main

// Running one of the programs the configuration allows.
//
// These are the benchmark and script runners -- slbench and slrun, which
// were called autobench and automate until 2026-08-22 and are still
// called that by the people who use them.  They are separate programs
// and they stay separate programs: each is several thousand lines of
// measurement machinery with its own command line, and a daemon that
// reimplemented a tenth of one would be a daemon whose numbers
// disagreed with the real thing.
//
// What is run is never what somebody typed.  The configuration names
// the programs by path and the words after the command are arguments to
// that path, handed over as an argv: there is no shell here, so nothing
// in an instant message can start a second program, redirect anything,
// or expand into a filename.  A command naming a program the
// configuration does not list is refused before anything is started.
//
// The avatar is passed in the environment rather than on the command
// line.  SLGO_AGENT is what every program in this tree reads to decide
// which session to attach to, and a flag the sender typed still wins
// over it -- which is the right way round: the daemon says which avatar
// it is acting as, and somebody who names another one on purpose has
// named it on purpose.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

// OutputLimit is how much of a program's output is kept.
//
// The answer is cut to a few instant messages anyway, so this is not
// about what is sent: it is about a program that prints a megabyte a
// second not being allowed to fill the daemon's memory with it.  The
// head is kept rather than the tail, because a run that went wrong went
// wrong at the start -- a script that would not compile says so in its
// first line -- and a benchmark's own result is a handful of lines.
const OutputLimit = 256 << 10

// programWaitDelay is how long a run is waited for once its program has
// exited or been stopped, while something it started still holds the
// output open.
//
// stopTogether kills the whole process group, which is every child that
// did not go out of its way to leave it.  One that did -- a daemon that
// called setsid -- is out of reach of the signal, and without this the
// answer would wait for it however long it lives.  After the delay the
// pipe is closed from this end and the run is over; whatever the stray
// child prints afterwards is lost, which is the right trade.
//
// A variable so that a test need not spend five seconds finding out.
var programWaitDelay = 5 * time.Second

// runProgram runs one of the configured programs and prints what it
// said.
func runProgram(ctx context.Context, r *req, out io.Writer, p *Program, args []string) error {
	// The session is not used here -- the program makes its own -- but
	// it has to be up.  A benchmark started against an avatar this
	// daemon has lost would attach to whatever slgod hands it, or to
	// nothing, minutes after the answer could have said so.
	if _, err := r.bot.Need(); err != nil {
		return err
	}

	// The run's own deadline, from the daemon's context rather than
	// from the command's.  A benchmark takes minutes and the command
	// timeout is a bound on a listing; carrying that one in would kill
	// every run at two minutes, which is not a setting anybody would
	// find by reading either number.
	base := r.base
	if base == nil {
		base = ctx
	}
	run, cancel := context.WithTimeout(base, r.d.cfg.RunTimeout)
	defer cancel()

	argv := append(append([]string{}, p.Argv...), args...)
	cmd := exec.CommandContext(run, argv[0], argv[1:]...)
	stopTogether(cmd)
	cmd.WaitDelay = programWaitDelay
	// Appended rather than filtered: os/exec keeps the last of a
	// repeated name, so this wins over an SLGO_AGENT the daemon was
	// started with.  It has to: that one names whatever avatar the
	// operator's shell was pointed at, and every run here would
	// otherwise be measured as that avatar whoever asked for it.
	cmd.Env = append(os.Environ(), sl.EnvAgent+"="+r.bot.Name())
	cmd.Stdin = nil

	// One writer for both, which is also what makes it safe: os/exec
	// gives Stdout and Stderr separate goroutines unless they are the
	// same comparable value, in which case one pipe and one goroutine
	// serve both.  They are the same pointer here, so nothing writes to
	// this buffer from two places at once.  It is what a person wants
	// as well -- a refusal printed on standard error belongs in the
	// answer, in the order it was printed.
	var buf limitedBuffer
	buf.limit = OutputLimit
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	started := time.Now()
	err := cmd.Run()
	took := time.Since(started).Round(time.Second)

	text := strings.TrimRight(buf.b.String(), "\n")
	if text != "" {
		fmt.Fprintln(out, text)
	}
	if buf.cut > 0 {
		fmt.Fprintf(out, "(%d more bytes of output were dropped)\n", buf.cut)
	}

	switch {
	case err == nil:
		fmt.Fprintf(out, "%s finished in %s\n", p.Name, took)
		return nil
	case errors.Is(err, exec.ErrWaitDelay):
		// The program itself exited 0, and something it left behind
		// was still holding the output when programWaitDelay ran out.
		// The run succeeded; the only thing to say is that output may
		// be missing from the end.
		fmt.Fprintf(out, "%s finished in %s, leaving something running that still had its output open\n", p.Name, took)
		return nil
	case errors.Is(run.Err(), context.DeadlineExceeded):
		return fmt.Errorf("%s was still running after %s and was stopped",
			p.Name, r.d.cfg.RunTimeout)
	case errors.Is(run.Err(), context.Canceled):
		return fmt.Errorf("%s was stopped after %s", p.Name, took)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("%s exited %d after %s", p.Name, exit.ExitCode(), took)
	}
	// Not a program that ran and failed: one that could not be started
	// at all.  Say which path, since the configuration chose it and
	// nobody typing the command can see what it was.
	return fmt.Errorf("cannot run %s (%s): %w", p.Name, argv[0], err)
}

// limitedBuffer collects output up to a limit and counts what it threw
// away.
//
// It never reports a write error.  A program whose output is refused
// halfway through gets a broken pipe and usually dies of it, and the
// point here is to bound the memory rather than to stop the run: the
// run is the thing somebody asked for.
type limitedBuffer struct {
	b     bytes.Buffer
	limit int
	cut   int
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	room := w.limit - w.b.Len()
	if room <= 0 {
		w.cut += len(p)
		return len(p), nil
	}
	if len(p) > room {
		w.b.Write(p[:room])
		w.cut += len(p) - room
		return len(p), nil
	}
	return w.b.Write(p)
}
