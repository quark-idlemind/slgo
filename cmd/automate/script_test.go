package main

// automate against a backend that is not Second Life.
//
// scripttest answers the contract offline: a pool with leases, a compiler
// with opinions, and a script that says what its source says it says.
// What is checked through it is what automate does with what it is told --
// which lines it prints, what it makes of a refusal, whether it gives its
// object back -- and never what Second Life would have said.
//
// It listens on loopback rather than being handed over in process,
// because what is being tested is the flag: --backend takes an address,
// and the dial is part of what a person typing one gets.

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/quark-idlemind/slgo/proto/scriptv1"
	"github.com/quark-idlemind/slgo/scripttest"
)

// speaks is a script that says two things and then the sentinel.
// scripttest reads what an object says out of the source, so this is a
// script in the only sense that matters here.
const speaks = `default {
	state_entry() {
		llOwnerSay("hello");
		llOwnerSay("still here");
		llOwnerSay("DONE");
	}
}`

// backendAt serves a backend on loopback and answers with its address and
// a client for asking what it is holding.
func backendAt(t *testing.T) (string, scriptv1.RunnerClient) {
	t.Helper()
	s := scripttest.New(scripttest.Options{})
	addr, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("serving a backend: %v", err)
	}
	t.Cleanup(s.Stop)

	conn, err := grpc.NewClient(addr.String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dialling the backend: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return addr.String(), scriptv1.NewRunnerClient(conn)
}

// heldGroups is how many of the pool's groups are out on lease.
func heldGroups(t *testing.T, c scriptv1.RunnerClient) int {
	t.Helper()
	p, err := c.Pool(context.Background(), &scriptv1.PoolRequest{})
	if err != nil {
		t.Fatalf("asking the backend what it is holding: %v", err)
	}
	n := 0
	for _, a := range p.GetAgents() {
		for _, g := range a.GetGroups() {
			if g.GetHeldBy() != "" {
				n++
			}
		}
	}
	return n
}

// TestABackendPrintsWhatTheScriptSaidAndNotTheSentinel: the contract
// streams the lines as the object says them, which is what automate's
// output is -- one line per line, tagged with the script that said it.
// The sentinel is the script talking to us rather than to the person
// reading, so it is not among them.
func TestABackendPrintsWhatTheScriptSaidAndNotTheSentinel(t *testing.T) {
	reset(t)
	addr, _ := backendAt(t)
	flags.Backend = addr

	run1, _, done, err := somewhereToRun(context.Background(), 1)
	if err != nil {
		t.Fatalf("somewhere to run: %v", err)
	}
	defer done()

	got := stdoutOf(t, func() {
		if !run1(0, "a.lsl", speaks) {
			t.Error("a script that said DONE was reported as having failed")
		}
	})
	for _, want := range []string{"a.lsl: hello\n", "a.lsl: still here\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "DONE") {
		t.Errorf("the sentinel was printed as output:\n%s", got)
	}
}

// TestALineThroughABackendIsPrintedBeforeTheRunHasEnded is why the
// contract streams a run rather than returning a transcript: a script
// that runs for a minute is one worth watching, and output that arrived
// all at once at the end would be indistinguishable from a hang.
//
// Nothing else in this repository checks that, on either transport.  It
// is checkable here because the backend can be told to leave a gap
// between lines, which is the one thing that makes buffering visible from
// outside.
func TestALineThroughABackendIsPrintedBeforeTheRunHasEnded(t *testing.T) {
	reset(t)
	s := scripttest.New(scripttest.Options{})
	addr, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("serving a backend: %v", err)
	}
	t.Cleanup(s.Stop)
	// A tenth of a second between lines, so a run that buffered would be
	// three tenths late with the first of them.
	s.SetBehaviour("", scripttest.Behaviour{
		LineDelay: 100 * time.Millisecond,
		Lines:     []string{"first", "second", "DONE"},
	})
	flags.Backend = addr.String()

	run1, _, done, err := somewhereToRun(context.Background(), 1)
	if err != nil {
		t.Fatalf("somewhere to run: %v", err)
	}
	defer done()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	save := os.Stdout
	os.Stdout = w

	ended := make(chan bool, 1)
	go func() { ended <- run1(0, "a.lsl", speaks) }()

	line, readErr := bufio.NewReader(r).ReadString('\n')
	early := len(ended) == 0

	ok := <-ended
	os.Stdout = save
	w.Close()
	r.Close()

	if readErr != nil {
		t.Fatalf("reading what was printed: %v", readErr)
	}
	if !ok {
		t.Error("a script that said DONE was reported as having failed")
	}
	if line != "a.lsl: first\n" {
		t.Errorf("the first line printed was %q", line)
	}
	if !early {
		t.Error("the first line was not printed until the run had finished")
	}
}

// TestSeveralScriptsTakeOneObjectAndASingleScriptTakesNone: the contract
// offers two ways of saying where, and automate picks between them by
// whether it has more than one script.  Several run in the order they
// were named and one that leaves something behind for the next has to
// find it there, so those hold an object; a single script needs nothing
// held while a person reads its output, and says so with an empty target.
//
// The other half is that the lease ends when the run does.  Holding a
// stream is the only thing keeping the objects, which is what makes a
// caller that crashed give them back -- and a caller that returned
// normally must not do worse than one that died.
func TestSeveralScriptsTakeOneObjectAndASingleScriptTakesNone(t *testing.T) {
	reset(t)
	addr, c := backendAt(t)
	flags.Backend = addr

	_, _, done, err := somewhereToRun(context.Background(), 1)
	if err != nil {
		t.Fatalf("somewhere to run: %v", err)
	}
	if n := heldGroups(t, c); n != 0 {
		t.Errorf("one script held %d groups, and needs none of them", n)
	}
	done()

	_, _, done, err = somewhereToRun(context.Background(), 2)
	if err != nil {
		t.Fatalf("somewhere to run: %v", err)
	}
	if n := heldGroups(t, c); n != 1 {
		t.Errorf("several scripts held %d groups, want the one they all run in", n)
	}
	done()
	if n := heldGroups(t, c); n != 0 {
		t.Errorf("%d groups were still held after the run", n)
	}
}

// TestAnObjectByNameIsNotSomethingABackendHas: the contract says nothing
// about rezzing or about naming an object -- a script runs inside one and
// the backend supplies it.  A person who named an object meant that
// object, so this is refused rather than quietly run somewhere else.
func TestAnObjectByNameIsNotSomethingABackendHas(t *testing.T) {
	reset(t)
	addr, _ := backendAt(t)
	flags.Backend = addr

	flags.Object = "workbench"
	if _, _, _, err := somewhereToRun(context.Background(), 1); err == nil {
		t.Error("--object was honoured by a backend that has no such thing")
	}

	flags.Object, flags.Keep = "", true
	if _, _, _, err := somewhereToRun(context.Background(), 1); err == nil {
		t.Error("--keep left behind an object that was never rezzed")
	}
}

// TestAScriptABackendWillNotCompileIsAFailedRun: the exit status is the
// only thing a caller driving this can act on without scraping stdout,
// and it must not depend on which side of the seam the script ran on.
func TestAScriptABackendWillNotCompileIsAFailedRun(t *testing.T) {
	reset(t)
	s := scripttest.New(scripttest.Options{})
	addr, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("serving a backend: %v", err)
	}
	t.Cleanup(s.Stop)
	s.SetBehaviour("", scripttest.Behaviour{
		CompileErrors: []string{"(3, 1) : ERROR : Syntax error"},
	})

	path := filepath.Join(t.TempDir(), "a.lsl")
	if err := os.WriteFile(path, []byte(speaks), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"automate", "--backend", addr.String(), path}
	t.Cleanup(func() { os.Args = []string{"automate"} })

	var runErr error
	got := stdoutOf(t, func() { runErr = run() })
	if runErr == nil {
		t.Error("a script the backend refused was reported as a run that worked")
	}
	if !strings.Contains(got, "Syntax error") {
		t.Errorf("the compiler's own words were not printed:\n%s", got)
	}
}

// TestABackendIsAskedForOneObjectUnlessJobsAsksForMore: a group of auto
// objects is four because this program put four there and knows it.
// What is behind the contract is somebody else's business -- it may have
// one object -- and asking a one-object backend for four would queue for
// three that are never coming.  So the default is one however many
// scripts there are, and --jobs is how somebody who knows what is behind
// it asks for more.
func TestABackendIsAskedForOneObjectUnlessJobsAsksForMore(t *testing.T) {
	reset(t)
	addr, _ := backendAt(t)
	flags.Backend = addr

	_, places, done, err := somewhereToRun(context.Background(), 4)
	if err != nil {
		t.Fatalf("somewhere to run: %v", err)
	}
	if places != 1 {
		t.Errorf("four scripts took %d objects from a backend that was never asked "+
			"how many it has", places)
	}
	done()

	flags.Jobs = 3
	_, places, done, err = somewhereToRun(context.Background(), 4)
	if err != nil {
		t.Fatalf("somewhere to run: %v", err)
	}
	defer done()
	if places != 3 {
		t.Errorf("--jobs 3 got %d objects", places)
	}
}

// TestJobsAsksForNoMoreObjectsThanThereAreScripts: an object held for a
// script that does not exist is an object taken from something else for
// nothing, and a backend with three objects would refuse a lease of four
// outright.
func TestJobsAsksForNoMoreObjectsThanThereAreScripts(t *testing.T) {
	reset(t)
	addr, _ := backendAt(t)
	flags.Backend = addr
	flags.Jobs = 4

	_, places, done, err := somewhereToRun(context.Background(), 2)
	if err != nil {
		t.Fatalf("somewhere to run: %v", err)
	}
	defer done()
	if places != 2 {
		t.Errorf("--jobs 4 with two scripts took %d objects", places)
	}
}

// TestScriptsThroughABackendRunAtOnceAndInDifferentObjects: the two
// halves of running several at once, end to end rather than against a
// stub -- they overlap in time, and each is in an object of its own.
// Sharing one object would be the naive version of this change, where the
// second script overwrites the first and both report the other's output.
func TestScriptsThroughABackendRunAtOnceAndInDifferentObjects(t *testing.T) {
	reset(t)
	s := scripttest.New(scripttest.Options{})
	addr, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("serving a backend: %v", err)
	}
	t.Cleanup(s.Stop)
	// Two lines a fifth of a second apart, so a run is about four
	// tenths of a second and four of them one after another would be
	// most of two seconds.
	s.SetBehaviour("", scripttest.Behaviour{
		LineDelay: 200 * time.Millisecond,
		Lines:     []string{"working", "DONE"},
	})
	flags.Backend = addr.String()
	flags.Jobs = 4

	srcs := sources(4)
	run1, places, done, err := somewhereToRun(context.Background(), len(srcs))
	if err != nil {
		t.Fatalf("somewhere to run: %v", err)
	}
	defer done()
	if places != 4 {
		t.Fatalf("%d objects to run four scripts in", places)
	}

	start := time.Now()
	var ok bool
	got := stdoutOf(t, func() { ok = runAll(srcs, places, run1) })
	took := time.Since(start)

	if !ok {
		t.Errorf("a run in which every script said DONE was reported as failed:\n%s", got)
	}
	if took > time.Second {
		t.Errorf("four scripts of about 0.4s took %v, which is one after another", took)
	}
	for _, src := range srcs {
		if !strings.Contains(got, src.path+":") {
			t.Errorf("%s said nothing:\n%s", src.path, got)
		}
	}
}
