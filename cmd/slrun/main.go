// Command slrun runs LSL scripts in Second Life and prints what they
// say.
//
//	slrun a.lsl b.lsl
//	slrun --object "Test HUD" --done DONE a.lsl
//	slrun --direct --first Quark --last Idlemind a.lsl
//
// A script needs an object to run in, so slrun takes some: the shared
// auto objects the avatar wears, four of them held for as long as the
// run lasts.  --object names one object of somebody's own instead, and
// --rez rezzes a throwaway prim beside the avatar and deletes it
// afterwards; both of those are one object, and so one script at a time.
//
// Several scripts run at once, one to an object: four of them by
// default, and --jobs asks for more -- as many as are free at the time,
// from as many avatars as it takes.  They
// finish in whatever order they finish in, so every line printed says
// which script said it.  --jobs 1 puts them back in the order they were
// named, which is what a set of scripts that leave things in the object
// for one another needs.
//
// What is printed is what the scripts said and nothing else.  Which
// avatar's objects they ran in is an aside about how the run was
// arranged rather than anything a script printed, so -v asks for it and
// a plain run does not carry it; --agent naming one makes it moot.
//
// Second Life is not the only thing that runs LSL, and none of what
// slrun does is particular to it -- put a script somewhere, watch what
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
	"sync"
	"syscall"
	"time"

	"github.com/pborman/getopt/v2"
	"github.com/pborman/options"

	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/sl"
)

var flags = struct {
	Addr    string          `getopt:"--addr=HOST:PORT  the slgod to attach to; default sl-host, or this machine"`
	Agent   string          `getopt:"--agent=NAME -a   the profile to use; the only one, by default"`
	Direct  bool            `getopt:"--direct -d       log in to Second Life directly, without slgod"`
	First   string          `getopt:"--first=NAME      the avatar's first name, for --direct"`
	Last    string          `getopt:"--last=NAME       the avatar's last name, for --direct"`
	Start   string          `getopt:"--start=WHERE     where to arrive: last, home, or a region, for --direct"`
	Object  string          `getopt:"--object=NAME     run in an object of this name, instead of the shared one"`
	Backend string          `getopt:"--backend=HOST:PORT run scripts through a script.v1 backend there -- a simulator or a viewer daemon -- instead of in Second Life"`
	Rez     bool            `getopt:"--rez             rez a throwaway prim instead of using the shared auto object"`
	Script  string          `getopt:"--script=NAME     what to call the script inside the object"`
	Jobs    int             `getopt:"--jobs=N -j       how many scripts to run at once, one per object; 4 by default, 1 runs them in order"`
	Clear   bool            `getopt:"--clear          empty every script out of the objects before running, for when something else is talking in them"`
	Done    string          `getopt:"--done=TEXT       the text that means the script has finished"`
	Timeout time.Duration   `getopt:"--timeout=DUR     how long to wait for it"`
	Keep    bool            `getopt:"--keep            leave the rezzed object behind"`
	V       options.Counter `getopt:"-v                say more: which avatars the objects came from"`
	Help    bool            `getopt:"--help -h         show this message"`
}{
	Script:  "slrun",
	Done:    "DONE",
	Start:   "last",
	Timeout: time.Minute,
}

// source is a script as it was named on the command line and as it was
// read.  The path is what every line it says is printed under: with
// several running at once it is the only thing saying which said what.
type source struct{ path, text string }

// errScript means a script failed, which has already been reported
// line by line and needs no second telling.
var errScript = errors.New("a script failed")

func main() {
	err := run()
	if err != nil && !errors.Is(err, errScript) {
		fmt.Fprintf(os.Stderr, "slrun: %v\n", err)
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
		return fmt.Errorf("nothing to run\nusage: slrun [options] script.lsl...")
	}

	// Read every script before connecting.  A typo in a filename is
	// worth finding out about now rather than after a login.
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

	// Every line is printed with the script it came from in front of it,
	// so the names are lined up: with several running at once the tags
	// are a column the eye follows down the page rather than a word at
	// the start of each line.
	tagWidth = 0
	for _, src := range srcs {
		if w := len(src.path) + 2; w > tagWidth {
			tagWidth = w
		}
	}

	// Where the scripts run.  Both transports come down to the same
	// thing -- a function that runs one script in one of the places
	// there are, prints what it said as it says it, and reports whether
	// it got to the end -- which is what lets everything else here be
	// written once.
	run1, places, done, err := somewhereToRun(ctx, len(srcs))
	if err != nil {
		return err
	}
	defer done()

	failed := !runAll(srcs, places, run1)
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

// runAll runs every script, at most one in each place at a time, and
// says whether all of them got to the end.
//
// A place runs its next script the moment it is free, so a long script
// holds up nothing but the object it is in.  What that costs is order:
// the lines come out as they are said, and with four running at once
// they are interleaved.  That is what the tag on every line is for, and
// why the tag is padded -- the alternative would be holding a script's
// output until it finished, which would mean watching nothing happen for
// a minute in a program whose whole point is watching a script run.
//
// The exit status does not depend on the order at all: it is whether
// every script got to the end.
func runAll(srcs []source, places int, run func(place int, path, src string) bool) bool {
	if places > len(srcs) {
		places = len(srcs)
	}
	if places < 1 {
		places = 1
	}

	got := make([]bool, len(srcs))
	next := make(chan int)
	var wg sync.WaitGroup
	wg.Add(places)
	for p := 0; p < places; p++ {
		go func(p int) {
			defer wg.Done()
			// Taken from a channel rather than sliced up in advance:
			// scripts are not the same length, and a place that has
			// finished its share should take somebody else's rather
			// than sit idle.
			for i := range next {
				got[i] = run(p, srcs[i].path, srcs[i].text)
			}
		}(p)
	}
	for i := range srcs {
		next <- i
	}
	close(next)
	wg.Wait()

	for _, ok := range got {
		if !ok {
			return false
		}
	}
	return true
}

// tagWidth is how much room the script names take, and say is how
// anything is printed.
//
// Both are about several scripts printing at once.  A line and the lines
// of a compiler refusal have to arrive whole and together rather than
// spliced through somebody else's, which is one lock held for as long as
// it takes to write them.
var (
	tagWidth int
	saying   sync.Mutex
)

func say(format string, args ...any) {
	saying.Lock()
	defer saying.Unlock()
	fmt.Printf(format, args...)
}

// tag is a script name as it appears in front of what the script said.
// It pads to whatever the widest name on the command line was, and to
// its own width when nobody has said -- one script needs no column.
func tag(path string) string {
	w := tagWidth
	if least := len(path) + 2; w < least {
		w = least
	}
	return fmt.Sprintf("%-*s", w, path+":")
}

// somewhereToRun gets places for the scripts and answers with the one
// thing the rest of this program needs of them.
//
// n is how many scripts there are, and it is a ceiling on how many
// places are worth having: a second object for a single script is an
// object taken from something else for nothing.
//
// How many places there are is answered here and not asked for, because
// each way of getting one has a different number to give -- four of
// four, a named object, a lease of whatever the backend granted -- and
// the count comes back with the places so that the caller never has to
// guess.
func somewhereToRun(ctx context.Context, n int) (run func(place int, path, src string) bool, places int, done func(), err error) {
	if flags.Backend != "" {
		switch {
		case flags.Object != "":
			// A backend supplies the object and names it itself; there is
			// nothing in the contract that asks for one by name.  Refused
			// rather than ignored: a person who named an object meant it.
			return nil, 0, nil, fmt.Errorf("--object names an object in a region, " +
				"which a script.v1 backend does not have: it supplies the object " +
				"and how it came to exist is its business")
		case flags.Keep:
			return nil, 0, nil, fmt.Errorf("--keep leaves a rezzed prim behind, and a " +
				"script.v1 backend rezzes nothing: the object it ran in is its own")
		}
		// One, unless somebody asked for more.  A run of auto objects is
		// four because this program put four there; what a backend has is
		// its own business, and asking a one-object simulator for four
		// would queue for three that are never coming.
		want := 1
		if flags.Jobs > 1 {
			want = flags.Jobs
			if want > n {
				want = n
			}
		}
		r, err := openBackend(flags.Backend, want, n > 1 || flags.Rez)
		if err != nil {
			return nil, 0, nil, err
		}
		return func(place int, path, src string) bool { return r.once(ctx, place, path, src) },
			r.places(), r.Close, nil
	}

	opts := session.Options{
		Addr: flags.Addr, Agent: flags.Agent, Direct: flags.Direct,
		First: flags.First, Last: flags.Last, Start: flags.Start,
		Channel: "slrun",
	}
	ps, cleanup, err := runIn(ctx, opts, n)
	if err != nil {
		return nil, 0, nil, err
	}
	if flags.Keep {
		for _, p := range ps {
			fmt.Printf("running in %s\n", p.object)
		}
	}
	return func(place int, path, src string) bool {
		p := ps[place]
		return once(ctx, p.session, p.object, path, src)
	}, len(ps), cleanup, nil
}

// place is one object and the session it lives on.
//
// The session is part of it because the objects need not all belong to
// one avatar: twelve scripts at once may be eight of qi's objects and
// four of somebody else's, and a script is run by the session that holds
// the object it is going in.
type place struct {
	session *sl.Session
	object  *sl.Object

	// dirty says the daemon could not promise this object was left fit
	// to use.  See clearPlaces.
	dirty bool
}

// clearPlaces silences whatever the last holder left running, and does
// nothing at all unless asked.
//
// The worry it answers is that chat carries the OBJECT a line came from
// and never the script's name, so a script still talking in an object
// this run was given is a line this run would print as its own.  That is
// real, and it is also narrower than it sounds: installing a script over
// one of the same name destroys what was there, measured, so the
// previous run's script -- which is nearly always another slrun --
// stops the moment this one starts.  What is left is a script under a
// DIFFERENT name that goes on saying things after it has finished, and
// neither of the two programs that share these objects writes one: both
// say what they have to say from state_entry and fall silent.
//
// Against that, the cost is certain.  Measured on Agni: clearing writes
// an empty script over every script an object holds, and the pool
// objects hold two -- "auto -n" makes the extra objects by COPYING the
// first, so each carries whatever that one had.  Thirty scripts meant
// ninety uploads rather than thirty, and it was the clearing that broke:
// thirty installs on their own ran clean, while thirty installs behind
// sixty clears failed every time, on Second Life's own capability,
// with 500s carrying a Python stack trace and with compile errors
// against an empty body.
//
// So it is --clear, and off: a certain two thirds of the upload budget
// spent on a hazard nobody here has yet seen.  What it is for is the day
// somebody DOES see foreign lines in their output.
//
// An object that will not come clean is dropped rather than fatal.  It
// used to end the whole run, and thirty scripts went nowhere because one
// object out of thirty would not answer -- which is a worse answer than
// running twenty-nine.  What is left is narrower and still true: this
// caller does not know what is in that object, so it does not listen to
// it.
// clearOne is the grid work, apart from the deciding, so that what this
// does with a failure can be tested without a fake that would have to
// compile LSL to answer a clearing.
var clearOne = session.Clear

func clearPlaces(ctx context.Context, places []place) ([]place, error) {
	if !flags.Clear {
		return places, nil
	}
	var (
		wg   sync.WaitGroup
		errs = make([]error, len(places))
	)
	for i, p := range places {
		if !p.dirty {
			continue
		}
		wg.Add(1)
		go func(i int, p place) {
			defer wg.Done()
			errs[i] = clearOne(ctx, p.session, p.object)
		}(i, p)
	}
	wg.Wait()

	kept := make([]place, 0, len(places))
	for i, p := range places {
		if errs[i] != nil {
			fmt.Fprintf(os.Stderr, "not using %s: %v\n", p.object, errs[i])
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("none of the %d objects could be made ready\n"+
			"        (--rez takes one of its own)", len(places))
	}
	if len(kept) < len(places) {
		// Said out loud: fewer places is a slower run, and a person who
		// asked for thirty at once should know they got fewer rather
		// than wonder why it took longer.
		fmt.Fprintf(os.Stderr, "%d of %d objects are usable; running %d at a time\n",
			len(kept), len(places), len(kept))
	}
	return kept, nil
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
			say("%s%s\n", tag(path), l.Text)
		},
	})
	if err != nil {
		say("%s%v\n", tag(path), err)
		return false
	}
	var fault string
	if res.Fault != nil {
		fault = res.Fault.String()
	}
	if !res.Compiled {
		// The rest of what the capability said, when it said anything.
		// "(0, 0) : ERROR : Syntax error" is the compiler's answer to a
		// script wrong at its first character AND to an upload that
		// reached it empty, so everything else in the answer is worth
		// putting in front of whoever has to tell those apart.
		if res.State != "" && res.State != "complete" {
			say("%sthe upload came back %q\n", tag(path), res.State)
		}
		if res.Message != "" {
			say("%s%s\n", tag(path), res.Message)
		}
		if len(res.Answer) > 0 {
			// Everything the capability said, not just the fields
			// anything here knows how to read.  It is one line, once, on
			// a failure -- and when the failure is "(0, 0) : ERROR :
			// Syntax error" against a script that is not wrong, it is
			// the only place the reason could be hiding.
			answer := string(res.Answer)
			if len(answer) > 400 {
				answer = answer[:400] + "..."
			}
			say("%sthe capability answered: %s\n", tag(path), answer)
		}
	}
	return verdict(path, res.Compiled, res.Errors, fault, res.Finished)
}

// verdict prints what a run came to and says whether the script got to
// the end.
//
// It is apart from the transports because it is the whole of what
// slrun decides, and the two must not drift: a run reported as having
// succeeded when it did not is a probe that quietly measured nothing, and
// that must not depend on which side of the seam the script ran on.
func verdict(path string, compiled bool, errs []string, fault string, finished bool) bool {
	switch {
	case !compiled:
		// Under one lock, so that a refusal several lines long stays in
		// one piece with another script printing at the same time.
		saying.Lock()
		defer saying.Unlock()
		for _, e := range errs {
			fmt.Printf("%s%s\n", tag(path), e)
		}
		if len(errs) == 0 {
			fmt.Printf("%sit would not compile, and the compiler did not say why\n", tag(path))
		}
		return false

	case fault != "":
		say("%s%s\n", tag(path), fault)
		return false

	case !finished && flags.Done != "":
		say("%sit did not say %s within %v\n", tag(path), flags.Done, flags.Timeout)
		return false
	}
	return true
}

// runIn gets somewhere to run n scripts.
//
// The shared auto objects by default: they are worn, so they cost
// nothing to find, and the script inside each already exists, which is
// the seconds that matter.  --object names one object of somebody's own,
// and --rez goes back to a throwaway prim, which is what to use when the
// shared group is wanted by something else and waiting will not do.
//
// Both of those are one object and so one script at a time.  That is a
// limit and not an oversight: --object was given an object and there is
// only the one, and rezzing a prim per job would put a heap of them
// beside the avatar for a saving the shared group already offers.
func runIn(ctx context.Context, o session.Options, n int) ([]place, func(), error) {
	if flags.Object != "" || flags.Rez {
		if flags.Jobs > 1 {
			what := "--rez rezzes one prim"
			if flags.Object != "" {
				what = "--object names one object"
			}
			return nil, nil, fmt.Errorf("%s, so there is one place to run and "+
				"--jobs %d has nowhere to put the other %d: the shared auto objects "+
				"are the ones there are several of",
				what, flags.Jobs, flags.Jobs-1)
		}
		s, err := session.Connect(ctx, o)
		if err != nil {
			return nil, nil, err
		}
		obj, cleanup, err := session.RunIn(ctx, s, flags.Object, flags.Keep)
		if err != nil {
			s.Close()
			return nil, nil, err
		}
		// Not dirty: --object was named by somebody who knows what is in
		// it, and --rez made it a moment ago.  Neither is a place taken
		// from a pool where somebody else was last.
		return []place{{session: s, object: obj}}, func() {
			if cleanup != nil {
				cleanup()
			}
			s.Close()
		}, nil
	}

	// An avatar named is an avatar honoured exactly, so its pool is the
	// ceiling and it is known without asking anybody.  Unnamed, the
	// ceiling is every avatar the daemon holds and the refusal comes
	// from there, where the count is.
	if most := session.AutoPool(); flags.Agent != "" && flags.Jobs > most {
		return nil, nil, fmt.Errorf("--jobs %d wants %d objects and %s "+
			"has %d; \"slsh auto -n %d\" is what makes more",
			flags.Jobs, flags.Jobs, flags.Agent, most, flags.Jobs)
	}

	// As many objects as there are scripts to run at once, taken all
	// together or not at all, and from more than one avatar if that is
	// what it takes.  Four by default because it is a useful width
	// without being anybody's whole pool, and because it leaves room for
	// a benchmark alongside.
	want := flags.Jobs
	if want < 1 {
		want = session.AutoGroupSize
	}
	if want > n {
		want = n
	}
	as, err := session.UseAutoSpread(ctx, o, want)
	if err != nil {
		return nil, nil, err
	}

	var places []place
	for _, a := range as {
		for i, obj := range a.Objects {
			places = append(places, place{a.Session, obj, a.Dirty[i]})
		}
	}
	// Which avatar, when nobody said and somebody asked.
	//
	// It was unconditional, and the ordinary run of slrun is a script
	// and its output: a line about whose objects it borrowed arrives in
	// the middle of that, on every run, saying something that changes
	// nothing about what the script printed.  -v is where the asides
	// live.  It stays off when --agent named one, because then the
	// answer is on the command line already.
	if o.Agent == "" && flags.V >= 1 {
		fmt.Fprintf(os.Stderr, "running as %s\n", whose(as))
	}

	// Only when asked.  See clearPlaces.
	places, err = clearPlaces(ctx, places)
	if err != nil {
		for _, a := range as {
			a.Release()
			a.Session.Close()
		}
		return nil, nil, err
	}
	return places, func() {
		for _, a := range as {
			a.Release()
			a.Session.Close()
		}
	}, nil
}

// whose names the avatars the objects came from, with how many each
// supplied when there is more than one -- because "running as qi" is a
// different thing from "running as qi (8) and example (4)", and a person
// reading the output of a script that misbehaved on one avatar needs to
// know it ran on two.
func whose(as []*session.Auto) string {
	if len(as) == 1 {
		return as[0].Agent
	}
	var b strings.Builder
	for i, a := range as {
		switch {
		case i == len(as)-1 && i > 0:
			b.WriteString(" and ")
		case i > 0:
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s (%d)", a.Agent, len(a.Objects))
	}
	return b.String()
}
