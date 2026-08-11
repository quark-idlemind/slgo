// Command automate runs LSL scripts in Second Life and prints what they
// say.
//
//	automate a.lsl b.lsl
//	automate --object "Test HUD" --done DONE a.lsl
//	automate --direct --first Quark --last Idlemind a.lsl
//
// A script needs an object to run in, so automate finds one or makes
// one: --object names an object already in the region, and without it a
// prim is rezzed beside the avatar for the run and deleted afterwards.
//
// Second Life is not the only thing that runs LSL, and none of what
// automate does is particular to it -- put a script somewhere, watch what
// it says, find out whether it compiled and whether it got to the end.
// --backend says that to something else through the script.v1 contract:
// the eLSL simulator with no grid under it, or a viewer driven from
// outside.  See script.go.
//
// The contract with the script is one line: it says DONE when it has
// finished.  Without that there is nothing to wait for but the clock,
// and every run costs the whole timeout.  Set --done to change the word
// or "" to wait out the timeout deliberately.
//
// This is the slgo build of the slrun program of the same name.  What
// it does not have, and where that went:
//
//	--sim                the eLSL simulator, which lives in elsl
//	-O, --optimize       the eLSL compiler's, likewise
//	--std                likewise
//	--slot, --prim       slrund's slot pool; --object names one instead
//	--notecard           a viewer file shuttle, no part of talking to a grid
//
// The compiler here is the grid's: the source goes up through
// UpdateScriptTask and comes back compiled or refused.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pborman/getopt/v2"
	"github.com/pborman/options"

	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/sl"
)

var flags = struct {
	Addr    string        `getopt:"--addr=HOST:PORT  the slgod to attach to; default sl-host, or this machine"`
	Agent   string        `getopt:"--agent=NAME -a   the profile to use; the only one, by default"`
	Direct  bool          `getopt:"--direct -d       log in to Second Life directly, without slgod"`
	First   string        `getopt:"--first=NAME      the avatar's first name, for --direct"`
	Last    string        `getopt:"--last=NAME       the avatar's last name, for --direct"`
	Start   string        `getopt:"--start=WHERE     where to arrive: last, home, or a region, for --direct"`
	Object  string        `getopt:"--object=NAME     run in an object of this name, instead of the shared one"`
	Backend string        `getopt:"--backend=HOST:PORT run scripts through a script.v1 backend there -- a simulator or a viewer daemon -- instead of in Second Life"`
	Rez     bool          `getopt:"--rez             rez a throwaway prim instead of using the shared auto object"`
	Script  string        `getopt:"--script=NAME     what to call the script inside the object"`
	Done    string        `getopt:"--done=TEXT       the text that means the script has finished"`
	Timeout time.Duration `getopt:"--timeout=DUR     how long to wait for it"`
	Keep    bool          `getopt:"--keep            leave the rezzed object behind"`
	Help    bool          `getopt:"--help -h         show this message"`
}{
	Script:  "automate",
	Done:    "DONE",
	Start:   "last",
	Timeout: time.Minute,
}

// errScript means a script failed, which has already been reported
// line by line and needs no second telling.
var errScript = errors.New("a script failed")

func main() {
	err := run()
	if err != nil && !errors.Is(err, errScript) {
		fmt.Fprintf(os.Stderr, "automate: %v\n", err)
	}
	if err != nil {
		os.Exit(1)
	}
}

func run() error {
	args := options.RegisterAndParse(&flags)
	if flags.Help {
		getopt.PrintUsage(os.Stdout)
		return nil
	}
	if len(args) == 0 {
		return fmt.Errorf("nothing to run\nusage: automate [options] script.lsl...")
	}

	// Read every script before connecting.  A typo in a filename is
	// worth finding out about now rather than after a login.
	type source struct{ path, text string }
	srcs := make([]source, 0, len(args))
	for _, path := range args {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		srcs = append(srcs, source{path, string(data)})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Where the scripts run.  Both transports come down to the same
	// thing -- a function that runs one script, prints what it said as it
	// says it, and reports whether it got to the end -- which is what
	// lets everything else here be written once.
	run1, done, err := somewhereToRun(ctx, len(srcs))
	if err != nil {
		return err
	}
	defer done()

	failed := false
	for _, src := range srcs {
		if !run1(src.path, src.text) {
			failed = true
		}
	}
	// A failed script is a failed run.  The original always exited 0,
	// which left a caller no way to tell without scraping stdout.
	// Returning rather than exiting here is what lets the deferred
	// cleanup run: a failed run is exactly when a stray prim is least
	// welcome.
	if failed {
		return errScript
	}
	return nil
}

// somewhereToRun gets a place for the scripts and answers with the one
// thing the rest of this program needs of it.
//
// n is how many scripts there are, which the contract path turns into
// whether to hold an object: several scripts run in the order they were
// named and one that leaves something behind for the next has to find it
// there.  The grid path holds one object either way, because that is what
// it has -- a session with an object in it.
func somewhereToRun(ctx context.Context, n int) (run func(path, src string) bool, done func(), err error) {
	if flags.Backend != "" {
		switch {
		case flags.Object != "":
			// A backend supplies the object and names it itself; there is
			// nothing in the contract that asks for one by name.  Refused
			// rather than ignored: a person who named an object meant it.
			return nil, nil, fmt.Errorf("--object names an object in a region, " +
				"which a script.v1 backend does not have: it supplies the object " +
				"and how it came to exist is its business")
		case flags.Keep:
			return nil, nil, fmt.Errorf("--keep leaves a rezzed prim behind, and a " +
				"script.v1 backend rezzes nothing: the object it ran in is its own")
		}
		r, err := openBackend(flags.Backend, n > 1 || flags.Rez)
		if err != nil {
			return nil, nil, err
		}
		return func(path, src string) bool { return r.once(ctx, path, src) }, r.Close, nil
	}

	opts := session.Options{
		Addr: flags.Addr, Agent: flags.Agent, Direct: flags.Direct,
		First: flags.First, Last: flags.Last, Start: flags.Start,
		Channel: "automate",
	}
	s, obj, cleanup, err := runIn(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	if flags.Keep {
		fmt.Printf("running in %s\n", obj)
	}
	return func(path, src string) bool { return once(ctx, s, obj, path, src) },
		func() {
			if cleanup != nil {
				cleanup()
			}
			s.Close()
		}, nil
}

// once runs one script and prints what it said, reporting whether it
// got to the end.
//
// Lines are printed as they arrive rather than at the end, because a
// script that runs for a minute is one worth watching.  The sentinel
// itself is not output -- it is the script talking to us, not to the
// person reading -- and neither is the debug channel, where the
// simulator comments on the script rather than the script speaking.
func once(ctx context.Context, s *sl.Session, obj *sl.Object, path, src string) bool {
	res, err := s.Run(ctx, sl.Script{
		In:      obj,
		Name:    flags.Script,
		Source:  src,
		Done:    flags.Done,
		Timeout: flags.Timeout,
		OnLine: func(l sl.Line) {
			if l.Debug() || (flags.Done != "" && strings.Contains(l.Text, flags.Done)) {
				return
			}
			fmt.Printf("%s: %s\n", path, l.Text)
		},
	})
	if err != nil {
		fmt.Printf("%s: %v\n", path, err)
		return false
	}
	var fault string
	if res.Fault != nil {
		fault = res.Fault.String()
	}
	return verdict(path, res.Compiled, res.Errors, fault, res.Finished)
}

// verdict prints what a run came to and says whether the script got to
// the end.
//
// It is apart from the transports because it is the whole of what
// automate decides, and the two must not drift: a run reported as having
// succeeded when it did not is a probe that quietly measured nothing, and
// that must not depend on which side of the seam the script ran on.
func verdict(path string, compiled bool, errs []string, fault string, finished bool) bool {
	switch {
	case !compiled:
		for _, e := range errs {
			fmt.Printf("%s: %s\n", path, e)
		}
		if len(errs) == 0 {
			fmt.Printf("%s: it would not compile, and the compiler did not say why\n", path)
		}
		return false

	case fault != "":
		fmt.Printf("%s: %s\n", path, fault)
		return false

	case !finished && flags.Done != "":
		fmt.Printf("%s: it did not say %s within %v\n", path, flags.Done, flags.Timeout)
		return false
	}
	return true
}

// runIn gets somewhere to run scripts.
//
// The shared auto object by default: it is worn, so it costs nothing to
// find, and the script inside it already exists, which is the seconds
// that matter.  --object names a different one, and --rez goes back to
// a throwaway prim per run, which is what to use when the shared object
// is wanted by something else and waiting will not do.
func runIn(ctx context.Context, o session.Options) (*sl.Session, *sl.Object, func(), error) {
	if flags.Object != "" || flags.Rez {
		s, err := session.Connect(ctx, o)
		if err != nil {
			return nil, nil, nil, err
		}
		obj, cleanup, err := session.RunIn(ctx, s, flags.Object, flags.Keep)
		if err != nil {
			s.Close()
			return nil, nil, nil, err
		}
		return s, obj, cleanup, nil
	}

	// One object is all a script needs, but it is taken as a whole
	// GROUP: the group is the unit of exclusion, and taking a single
	// object out of one would let a benchmark holding that group use it
	// at the same time.
	a, err := session.UseAutoAnywhere(ctx, o, 1)
	if err != nil {
		return nil, nil, nil, err
	}
	if o.Agent == "" {
		fmt.Fprintf(os.Stderr, "running as %s\n", a.Agent)
	}
	return a.Session, a.Objects[0], a.Release, nil
}
