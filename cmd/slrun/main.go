// Command slrun runs LSL scripts in Second Life and prints what they
// say.
//
//	slrun a.lsl b.lsl
//	slrun --object "Test HUD" --done DONE a.lsl
//	slrun --direct --first Quark --last Idlemind a.lsl
//
// A script needs an object to run in, so slrun takes some: the shared
// auto objects the avatars wear, held for as long as the run lasts.
// --object names one object of somebody's own instead, and --rez rezzes
// a throwaway prim beside the avatar and deletes it afterwards; both of
// those are one object, and so one script at a time.
//
// Several scripts run at once, one to an object: four at a time by
// default and never more than there are scripts, and --jobs asks for a
// different number.  They are taken all together, waiting until that
// many are free, from as many avatars as it takes.  They finish in
// whatever order they finish in, so every line printed says which script
// said it.  --jobs 1 puts them back in the order they were named, which
// is what a set of scripts that leave things in the object for one
// another needs.
//
// What is printed is what the scripts said, and what became of any that
// failed.
//
// With several running at once each line carries the name of the script
// that said it, since they arrive interleaved.  One script needs no such
// name and does not get one; -v asks for it anyway, which is worth
// having when the output is being kept.
//
// Which avatar's objects they ran in is an aside about how the run was
// arranged rather than anything a script printed, so it is the second
// thing -v buys; --agent naming one makes it moot.
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/internal/version"
	"github.com/quark-idlemind/slgo/sl"
)

var flags = struct {
	Addr    string          `getopt:"--addr=HOST:PORT  the slgod to attach to; else $SLGO_ADDR, else sl-host, else this machine"`
	Agent   string          `getopt:"--agent=NAME -a   the profile to use; the only one, by default"`
	Direct  bool            `getopt:"--direct -d       log in to Second Life directly, without slgod"`
	First   string          `getopt:"--first=NAME      the avatar's first name, for --direct"`
	Last    string          `getopt:"--last=NAME       the avatar's last name, for --direct"`
	Start   string          `getopt:"--start=WHERE     where to arrive: last, home, or a region, for --direct"`
	Object  string          `getopt:"--object=NAME     run in an object of this name, instead of the worn auto objects"`
	Backend string          `getopt:"--backend=HOST:PORT run scripts through a script.v1 backend there -- a simulator or a viewer daemon -- instead of in Second Life"`
	Rez     bool            `getopt:"--rez             rez a throwaway prim instead of using the worn auto objects"`
	Script  string          `getopt:"--script=NAME     what to call the script inside the object"`
	Jobs    int             `getopt:"--jobs=N -j       how many scripts to run at once, one per object; 4 by default, 1 runs them in order"`
	Clear   bool            `getopt:"--clear          empty the scripts out of the objects the daemon says were not left clean, before running, for when something else is talking in them"`
	Done    string          `getopt:"--done=TEXT       the text that means the script has finished"`
	Timeout time.Duration   `getopt:"--timeout=DUR     how long to wait for it"`
	Wait    time.Duration   `getopt:"--wait=DUR        how long to wait for somewhere to run when every object is busy; as long as it takes by default"`
	Keep    bool            `getopt:"--keep            leave the rezzed object behind"`
	V       options.Counter `getopt:"-v                say more: once for the script name in front of every line, twice for which avatars the objects came from"`
	Help    bool            `getopt:"--help -h         show this message"`
	Version bool            `getopt:"--version         say which build this is, and exit"`
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

// errInterrupted means somebody pressed ^C.  It is a failed run: what
// the scripts had said by then is on the screen and what they would have
// said is not, so a caller that reads the output has half of one.
var errInterrupted = errors.New("interrupted")

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
	if flags.Version {
		fmt.Println(version.String("slrun"))
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

	// ^C ends a wait for somewhere to run, stops handing out scripts and
	// cancels the ones in flight, and then the deferred cleanup gives the
	// objects back.  A rezzed prim is deleted on a context of its own
	// (session.RunIn's cleanup), since an interrupt may be why it is going.
	// Why: doc/slrun.md#an-interrupt-stops-the-run
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		// The default goes back the moment the first one arrives, so a
		// second ^C kills outright.  Giving the objects back is a round
		// trip to the grid and can hang, and somebody pressing ^C twice
		// has said what they want to happen.
		stop()
	}()

	// Every line is printed with the script it came from in front of it,
	// padded so that the names read as a column.  One script is the
	// exception, since there is nothing to tell it apart from; -v asks
	// for the name anyway.
	// Why: doc/slrun.md#a-name-on-every-line-and-none-on-one-script
	untagged = len(srcs) == 1 && flags.V < 1
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

	failed := !runAll(ctx, srcs, places, run1)

	// An interrupt is a failed run whatever the scripts managed first,
	// and it is worth telling apart from a script that failed on its
	// own: nothing is wrong with the scripts.
	if ctx.Err() != nil {
		return errInterrupted
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
func runAll(ctx context.Context, srcs []source, places int, run func(place int, path, src string) bool) bool {
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
				// Handing out stops when the run does, but cannot
				// promise to: a select with both cases ready picks
				// one at random, so a script can still be handed out
				// after the run was stopped -- about one run in 350
				// of TestNothingIsStartedAfterTheRunIsStopped.  So
				// a place asks too, and a script it is handed then
				// is not started and has not got to the end.
				if ctx.Err() != nil {
					continue
				}
				got[i] = run(p, srcs[i].path, srcs[i].text)
			}
		}(p)
	}
	// Handing out stops when the run does.  The scripts already in
	// flight are cut short by the same context, since run closes over
	// it; the ones that were never started say nothing at all, which is
	// what somebody who pressed ^C asked for.
feeding:
	for i := range srcs {
		select {
		case next <- i:
		case <-ctx.Done():
			break feeding
		}
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

// tagWidth is how much room the script names take, and untagged says to
// print no name at all.  saying is held while a script's output is
// written, so that a line, or the lines of a compiler refusal, arrive
// whole rather than spliced through another script's.
var (
	tagWidth int
	untagged bool
	saying   sync.Mutex
)

// say prints to standard output under saying.
func say(format string, args ...any) {
	saying.Lock()
	defer saying.Unlock()
	fmt.Printf(format, args...)
}

// tag is a script name as it appears in front of what the script said,
// padded to the widest name on the command line.  It is empty when
// untagged: one script, and no -v.
func tag(path string) string {
	if untagged {
		return ""
	}
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
// each way of getting one has a different number to give -- the auto
// objects granted, less any that could not be worn or would not come
// clean; one named or rezzed object; whatever the backend's lease
// granted -- and the count comes back with the places so that the
// caller never has to guess.
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
		// One, unless --jobs asks for more.  Four is the grid path's
		// default because this program set up the pool it takes them
		// from; what a backend has is its own business, and asking a
		// one-object simulator for four would queue for three that are
		// never coming.
		want := 1
		if flags.Jobs > 1 {
			want = flags.Jobs
			if want > n {
				want = n
			}
		}
		// --wait holds a lease even for one script: a lease request is the
		// only one that carries a bound on the queue.
		r, err := openBackend(ctx, flags.Backend, want, n > 1 || flags.Rez || flags.Wait > 0)
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
		// In whole seconds as a backend is sent it, so that --wait
		// means the same on both.
		Wait: time.Duration(seconds(flags.Wait)) * time.Second,
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

// clearOne is clearPlaces' grid work, apart from the deciding, so that
// what it does with a failure can be tested without a fake that would
// have to compile LSL to answer a clearing.
var clearOne = session.Clear

// clearPlaces silences whatever the last holder left running in each
// place the daemon marked dirty, and does nothing at all unless --clear
// asked.
//
// Chat carries the object a line came from and never the script's name,
// so a script still talking in one of these objects is a line this run
// would print as its own.  It is off by default because that hazard is
// narrow and the cost is certain: installing over a name already stops
// that name's script, and clearing is an upload for every script an
// object holds.
//
// An object that will not come clean is dropped rather than fatal: this
// run does not know what is in it, so it does not listen to it.
// Why: doc/slots.md#clearing-an-object-before-using-it
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
	// What went wrong around the run without spoiling it -- a script
	// that could not be stopped, a copy that could not be tidied away --
	// is said whatever became of the run, an interrupted one included.
	if res != nil {
		for _, w := range res.Warnings {
			fmt.Fprintf(os.Stderr, "slrun: %s%s\n", tag(path), w)
		}
	}
	if err != nil {
		if !quiet(err) {
			say("%s%v\n", tag(path), err)
		}
		return false
	}
	var fault string
	if res.Fault != nil {
		fault = res.Fault.String()
	}
	if !res.Compiled {
		// The rest of what the capability said, when it said anything.
		// "(0, 0) : ERROR : Syntax error" here is an upload that reached
		// the compiler empty even when sl asked again -- sl puts a
		// newline in front of every script, so nothing of the caller's
		// is on line 0 -- and the rest of the answer is the only place
		// its reason could be.
		if res.State != "" && res.State != "complete" {
			say("%sthe upload came back %q\n", tag(path), res.State)
		}
		if res.Message != "" {
			say("%s%s\n", tag(path), res.Message)
		}
		if len(res.Answer) > 0 {
			// The capability's whole answer, not just the fields
			// anything here knows how to read: one line, cut at 400
			// bytes, once, on a failure.
			answer := string(res.Answer)
			if len(answer) > 400 {
				answer = answer[:400] + "..."
			}
			say("%sthe capability answered: %s\n", tag(path), answer)
		}
	}
	return verdict(path, res.Compiled, res.Errors, fault, res.Blocked, res.Finished)
}

// quiet reports whether an error is the run being stopped rather than
// anything about the script.  ^C ends every script in flight with
// "context canceled", and main says "interrupted" once instead.
//
// Cancellation only: a deadline that ran out is somebody's timeout
// expiring and is news.  Two shapes, because a run through slgod or a
// backend is a gRPC call, and a cancelled one comes back as a status
// that does not satisfy errors.Is against context.Canceled.
// Why: doc/slrun.md#one-line-saying-interrupted
func quiet(err error) bool {
	return errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled
}

// verdict prints what a run came to and says whether the script got to
// the end.
//
// It is apart from the transports because it is the whole of what
// slrun decides, and the two must not drift: a run reported as having
// succeeded when it did not is a probe that quietly measured nothing, and
// that must not depend on which side of the seam the script ran on.
func verdict(path string, compiled bool, errs []string, fault, blocked string, finished bool) bool {
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

	// Not waited out, with or without a sentinel: nothing is coming.
	case blocked != "":
		say("%s%s\n", tag(path), blocked)
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
// shared objects are wanted by something else and waiting will not do.
//
// Both of those are one object and so one script at a time.  That is a
// limit and not an oversight: --object was given an object and there is
// only the one, and rezzing a prim per job would put a heap of them
// beside the avatar for a saving the shared objects already offer.
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
	//
	// The pool is 24 objects an avatar, all on one HUD point, and a
	// lease puts on an object that is not yet worn only while the avatar
	// keeps session.AutoLeaseReserve attachment slots free.  So a lease
	// may hold fewer than --jobs on an avatar that wears much else, and
	// says so on stderr; the run goes on with the places it has.
	// Why: doc/slots.md#what-a-lease-leaves-free
	if most := session.AutoPool(); flags.Agent != "" && flags.Jobs > most {
		return nil, nil, fmt.Errorf("--jobs %d wants %d objects and one "+
			"avatar holds at most %d; leave out --agent to take them from "+
			"more than one", flags.Jobs, flags.Jobs, most)
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
	switch {
	case errors.Is(err, client.ErrStillBusy):
		return nil, nil, fmt.Errorf("every object was still busy when --wait %v ran out", flags.Wait)
	case err != nil && ctx.Err() != nil:
		return nil, nil, fmt.Errorf("%w while waiting for objects", errInterrupted)
	case err != nil:
		return nil, nil, err
	}

	var places []place
	for _, a := range as {
		for i, obj := range a.Objects {
			places = append(places, place{a.Session, obj, a.Dirty[i]})
		}
	}
	// Which avatar, when nobody said and somebody asked twice: the name
	// in front of the lines is about reading the output, and this is
	// about how the run was arranged.  --agent naming one has put the
	// answer on the command line already.
	// Why: doc/slrun.md#which-avatar-the-objects-came-from
	if o.Agent == "" && flags.V >= 2 {
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
