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

	s, err := session.Connect(ctx, session.Options{
		Addr: flags.Addr, Agent: flags.Agent, Direct: flags.Direct,
		First: flags.First, Last: flags.Last, Start: flags.Start,
		Channel: "automate",
	})
	if err != nil {
		return err
	}
	defer s.Close()

	obj, cleanup, err := runIn(ctx, s)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	if flags.Keep {
		fmt.Printf("running in %s\n", obj)
	}

	failed := false
	for _, src := range srcs {
		if !once(ctx, s, obj, src.path, src.text) {
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

	switch {
	case !res.Compiled:
		for _, e := range res.Errors {
			fmt.Printf("%s: %s\n", path, e)
		}
		if len(res.Errors) == 0 {
			fmt.Printf("%s: it would not compile, and the compiler did not say why\n", path)
		}
		return false

	case res.Fault != nil:
		fmt.Printf("%s: %s\n", path, res.Fault)
		return false

	case !res.Finished && flags.Done != "":
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
func runIn(ctx context.Context, s *sl.Session) (*sl.Object, func(), error) {
	if flags.Object != "" || flags.Rez {
		return session.RunIn(ctx, s, flags.Object, flags.Keep)
	}
	return session.UseAuto(ctx, s, sl.HUDBottomLeft)
}
