package session

// Finding somewhere to run, when there are several avatars and a pool of
// objects on each.
//
// # The shape of the problem
//
// A benchmark needs several objects to itself for as long as it runs,
// because each carries its base reading in its own linkset data; a
// program running scripts wants one object per script it runs at once.
// One avatar wears enough for several of either, and a daemon may hold
// several avatars.  So "where do I run?" has two answers to find, and
// the second depends on the first.
//
// # Why it is not a queue
//
// The old arrangement was one lock over one set of objects: a second
// benchmark waited for the first even when objects were going spare.
// With a pool the wait should be the LAST resort, so this tries every
// avatar before it queues on any of them.
//
// # Why an allocation lock, and not a lock per group
//
// Objects locked one at a time deadlock: four callers each wanting four
// of twelve can end up holding three apiece and waiting for a fourth
// that nobody is going to give back.  Taking them in fixed groups of
// four avoids that, and was what this did, but it makes the group the
// unit of everything -- a caller wanting six has to hold eight, a caller
// wanting twelve cannot be served at all, and four is a number that came
// from what one benchmark's search happens to use.
//
// The deadlock is not caused by the granularity.  It is caused by
// callers taking objects incrementally while others do the same.  One
// more lock settles it: an allocation lock that one caller holds at a
// time, under which a caller takes ALL of the objects it wants or none
// of them.  Nothing is ever held while waiting for anything else -- the
// allocation lock is dropped, and everything taken under it given back,
// before the caller waits -- so there is no cycle to deadlock on.
//
// What is left is starvation: a caller wanting twelve can in principle
// be stepped over for ever by a stream of callers wanting one.  Nothing
// here prevents that, deliberately.  These are a handful of programs run
// by hand or from a script, not a service under load; the requests
// drain, and a scheme that could not be reasoned about would be the
// worse trade.
//
// # Why the avatar is chosen here and not by the daemon
//
// Only the caller knows what it wants -- how many objects, and whether
// it minds which avatar.  The daemon supplies the ORDER (oldest session
// first, which is the default) and the locks; the choosing is arithmetic
// on top of that.  A named avatar is honoured exactly, including the
// waiting: asking for qi and being given example would be worse than being
// slow.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

// lockWait is how long to queue for a lock before giving up.  Long
// enough for a benchmark ahead of us to finish, short enough that a
// daemon holding a lock nobody will release is noticed the same day.
const lockWait = 15 * time.Minute

// Auto is somewhere to run: a session, objects held for as long as
// Release is uncalled, and which avatar it turned out to be.
type Auto struct {
	Session *sl.Session
	Objects []*sl.Object

	// Agent is the profile the objects belong to, which a caller that
	// did not name one should say out loud.
	Agent string

	// Slots is which of the pool's places these objects came from, for a
	// person reading a log and wondering what else was running.
	Slots []int

	release func()
}

// Where names the places held, for a log line.
func (a *Auto) Where() string {
	var b strings.Builder
	for i, s := range a.Slots {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Itoa(s))
	}
	return b.String()
}

// Release gives the objects back.  So does going away: the daemon frees
// what a client holds when its stream ends.
//
// It does NOT close the session.  The session is the caller's from here
// -- it is what the work runs on -- and closing it is that code's
// business, done once, wherever it already closes sessions.
func (a *Auto) Release() {
	if a.release != nil {
		a.release()
		a.release = nil
	}
}

// UseAutoAnywhere finds somewhere to run n objects' worth of work.
//
// With an avatar named, it uses that one and waits its turn if the
// objects are busy.  With none named it takes the first avatar that can
// supply all n at once, in the daemon's order -- the default first, then
// the rest -- and only queues, on the default, when no avatar can.
//
// All n or none.  A caller given four of the eight it asked for would
// either hold them while waiting for the rest, which is the deadlock, or
// give them back -- which is what this does for it.
//
// The session it returns belongs to the caller and must be closed by
// it.  Sessions opened to avatars that turned out to be busy are closed
// here.
func UseAutoAnywhere(ctx context.Context, o Options, n int) (*Auto, error) {
	// Before the branch below, because an avatar named by the
	// environment is still a named avatar: falling over to a different
	// one would be ignoring what somebody asked for.
	o.Agent = AgentName(o.Agent)

	if n < 1 {
		n = 1
	}
	if n > AutoPool() {
		return nil, fmt.Errorf("%d objects were asked for and an avatar has %d; "+
			"waiting for objects that do not exist would be waiting for ever",
			n, AutoPool())
	}

	// A named avatar, or a direct login, is the simple case: there is
	// nothing to choose between.
	if o.Agent != "" || o.Direct {
		s, err := Connect(ctx, o)
		if err != nil {
			return nil, err
		}
		a, err := useAutoOn(ctx, s, n, true)
		if err != nil {
			s.Close()
			return nil, err
		}
		return a, nil
	}

	// Nobody named.  Ask the daemon who it has, in its own order.
	first, err := Connect(ctx, o)
	if err != nil {
		return nil, err
	}
	names, err := sessionNames(ctx, first)
	if err != nil || len(names) <= 1 {
		// One avatar, or a daemon too old to say: use what we have.
		a, err := useAutoOn(ctx, first, n, true)
		if err != nil {
			first.Close()
			return nil, err
		}
		return a, nil
	}

	// The default is whatever the daemon attached us to, so try it
	// without reconnecting.
	if a, err := useAutoOn(ctx, first, n, false); err == nil {
		return a, nil
	}
	deflt := first.Info().Name

	for _, name := range names {
		if name == deflt {
			continue // already tried, above
		}
		next := o
		next.Agent = name
		s, err := Connect(ctx, next)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			continue
		}
		a, err := useAutoOn(ctx, s, n, false)
		if err != nil {
			s.Close()
			continue
		}
		first.Close()
		return a, nil
	}

	// Everything is busy.  Queue on the default rather than on whichever
	// avatar was asked last, so that waiting is predictable.
	fmt.Fprintf(os.Stderr,
		"no avatar has %d objects free; waiting for them on %s\n", n, deflt)
	a, err := useAutoOn(ctx, first, n, true)
	if err != nil {
		first.Close()
		return nil, err
	}
	return a, nil
}

// useAutoOn takes n objects on one session and puts them on.
func useAutoOn(ctx context.Context, s *sl.Session, n int, wait bool) (*Auto, error) {
	slots, err := takeSlots(ctx, s, n, wait)
	if err != nil {
		return nil, err
	}
	a, err := wearSlots(ctx, s, slots)
	if err != nil {
		releaseSlots(s, slots)
		return nil, err
	}
	return a, nil
}

// tryOn takes up to want of one avatar's places, without waiting for
// anything at all.
//
// The allocation lock is what makes taking places one at a time safe.
// One caller holds it, and under it every place is TRIED and none is
// waited for.  That is the invariant everything else here rests on:
// NOTHING EVER BLOCKS WHILE HOLDING AN ALLOCATION LOCK, so a caller can
// wait for one while holding places, and two callers can gather from the
// same avatars in any order, without a cycle being possible.
//
// What is excluded is other CLIENTS.  The daemon's lock is re-entrant --
// a client that asks for a place it already holds is given it -- so a
// program that called this twice would be handed the same objects twice.
// A program takes what it needs in one call, which is also the only way
// all-or-nothing can mean anything.
//
// busy is something to wait on when the answer was short: a place
// somebody else holds, or this avatar's allocation lock when another
// caller was choosing at that moment.  Either is a thing that will come
// free, which is all a waiter needs.
func tryOn(ctx context.Context, s *sl.Session, want int) (got []int, busy string, err error) {
	if want < 1 {
		return nil, "", nil
	}
	ok, _, err := s.TryLock(ctx, AutoAllocLock)
	if err != nil {
		return nil, "", fmt.Errorf("asking for objects on %s: %w\n"+
			"        (an slgod older than the lock does not answer; --rez avoids it)",
			s.Info().Name, err)
	}
	if !ok {
		// Somebody else is choosing.  They cannot be waiting for
		// anything while they do it, so this is worth looking at again
		// in a moment rather than queueing behind their whole run.
		return nil, AutoAllocLock, nil
	}
	defer s.Unlock(AutoAllocLock)

	for i := 0; i < AutoPool() && len(got) < want; i++ {
		ok, _, err := s.TryLock(ctx, AutoSlotLock(i))
		if err != nil {
			releaseSlots(s, got)
			return nil, "", fmt.Errorf("asking for objects on %s: %w\n"+
				"        (an slgod older than the lock does not answer; --rez avoids it)",
				s.Info().Name, err)
		}
		if ok {
			got = append(got, i)
			continue
		}
		if busy == "" {
			busy = AutoSlotLock(i)
		}
	}
	return got, busy, nil
}

// waitFor queues for one lock and gives it straight back.
//
// There is no message for "something was given back", and this says the
// same thing with the locks that already exist: the wait ends when that
// lock comes free, which is the moment it is worth looking again.  It is
// handed back at once, because keeping it would be holding one thing
// while waiting for the others -- the deadlock this is all for.
//
// The caller must be holding NOTHING when it calls this.
func waitFor(ctx context.Context, s *sl.Session, name string) error {
	waitCtx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	if err := s.Lock(waitCtx, name); err != nil {
		return fmt.Errorf("waiting for objects on %s: %w", s.Info().Name, err)
	}
	s.Unlock(name)
	return nil
}

// takeSlots takes n of one avatar's places, all of them or none.
func takeSlots(ctx context.Context, s *sl.Session, n int, wait bool) ([]int, error) {
	said := false
	for {
		got, busy, err := tryOn(ctx, s, n)
		if err != nil {
			return nil, err
		}
		if len(got) == n {
			return got, nil
		}

		// Not enough.  Everything taken goes back before anything is
		// waited for.
		releaseSlots(s, got)

		if !wait {
			return nil, fmt.Errorf("%s has %d of the %d objects free",
				s.Info().Name, len(got), n)
		}
		if busy == "" {
			// Nothing is held by anybody and there is still not enough:
			// only possible if the pool is smaller than the ask, which
			// is refused before now.  Saying so beats waiting for ever.
			return nil, fmt.Errorf("%s has %d objects free of the %d asked for, "+
				"and nothing is holding the rest", s.Info().Name, len(got), n)
		}
		if !said {
			// Said out loud, because from outside a queue and a hang
			// look the same and one of them is worth waiting through.
			fmt.Fprintf(os.Stderr, "%d of the %d objects on %s are busy; waiting\n",
				n-len(got), n, s.Info().Name)
			said = true
		}
		if err := waitFor(ctx, s, busy); err != nil {
			return nil, err
		}
	}
}

// releaseSlots gives back places that were taken and not used.
func releaseSlots(s *sl.Session, slots []int) {
	for _, i := range slots {
		s.Unlock(AutoSlotLock(i))
	}
}

// wearSlots makes sure the objects for these places exist and are on.
func wearSlots(ctx context.Context, s *sl.Session, slots []int) (*Auto, error) {
	if len(slots) == 0 {
		return nil, fmt.Errorf("no objects to run in")
	}
	folder, err := objectsFolder(ctx, s)
	if err != nil {
		return nil, err
	}

	// The items have to exist before any of them can be worn, and the
	// highest place asked for is how far that has to reach.
	if err := EnsureAutoItems(ctx, s, folder, slots[len(slots)-1]+1); err != nil {
		return nil, err
	}

	var (
		objs []*sl.Object
		used []int
	)
	for _, slot := range slots {
		// AttachAdd, because past the eighth slot two objects share a
		// point and a bare attach would throw the first one off.
		a, err := s.EnsureAttached(ctx, folder, AutoName(slot), AutoPoints[slot]|sl.AttachAdd)
		if err != nil {
			if len(objs) == 0 {
				return nil, err
			}
			fmt.Fprintf(os.Stderr, "only %d of %d objects: %v\n", len(objs), len(slots), err)
			break
		}
		obj := a.Object
		objs = append(objs, &obj)
		used = append(used, slot)
	}

	// A place whose object would not go on goes back at once: a lock
	// over an object nobody got is a place taken from somebody who could
	// have used it.
	if len(used) < len(slots) {
		releaseSlots(s, slots[len(used):])
	}

	return &Auto{
		Session: s,
		Objects: objs,
		Agent:   s.Info().Name,
		Slots:   used,
		release: func() { releaseSlots(s, used) },
	}, nil
}

// sessionNames is who the daemon is holding, in its own order.
func sessionNames(ctx context.Context, s *sl.Session) ([]string, error) {
	type sessioner interface {
		Sessions(context.Context) ([]string, error)
	}
	h, ok := s.Backend().(sessioner)
	if !ok {
		return nil, fmt.Errorf("not a hosted session")
	}
	return h.Sessions(ctx)
}

// ------------------------------------------------- across avatars

// UseAutoSpread finds n places to run, taking them from as many of the
// daemon's avatars as it needs -- four on qi and four on example, if
// that is what is free.
//
// It is for work that is simply WIDE: a program with twelve scripts to
// run at once wants twelve objects and does not care whose they are.  It
// is not for work whose parts have to agree with one another, which is
// why autobench does not use it: its objects are compared against each
// other, and two avatars may be standing in different regions.
//
// A named avatar, or a direct login, is honoured exactly and is one
// avatar -- asking for qi and being given some of example would be worse
// than being slow.
//
// All n or none, as with one avatar, and for the same reason.  What
// makes gathering across several safe is the invariant tryOn describes:
// nothing ever blocks while an allocation lock is held, and this holds
// one at a time in any case.  When it cannot be served it gives back
// everything it took on every avatar before it waits.
//
// That is about the ALLOCATION.  An object that will not go on afterwards
// is the same as it has always been: fewer places than were asked for,
// which is a slower run rather than a failure, and the caller is
// expected to count what it got.
//
// Each Auto in the answer has its own session, which the caller must
// close -- once each, and they may be the same one.  Sessions opened to
// avatars that turned out not to be used are closed here.
func UseAutoSpread(ctx context.Context, o Options, n int) ([]*Auto, error) {
	o.Agent = AgentName(o.Agent)
	if n < 1 {
		n = 1
	}
	if o.Agent != "" || o.Direct {
		a, err := UseAutoAnywhere(ctx, o, n)
		if err != nil {
			return nil, err
		}
		return []*Auto{a}, nil
	}

	first, err := Connect(ctx, o)
	if err != nil {
		return nil, err
	}
	names, err := sessionNames(ctx, first)
	if err != nil || len(names) <= 1 {
		// One avatar, or a daemon too old to say: there is nothing to
		// spread across.
		a, err := useAutoOn(ctx, first, n, true)
		if err != nil {
			first.Close()
			return nil, err
		}
		return []*Auto{a}, nil
	}
	if most := AutoPool() * len(names); n > most {
		first.Close()
		return nil, fmt.Errorf("%d objects were asked for and the daemon's %d avatars "+
			"have %d between them; waiting for objects that do not exist would be "+
			"waiting for ever", n, len(names), most)
	}

	sp := &spread{o: o, deflt: first.Info().Name, open: map[string]*sl.Session{}}
	sp.open[sp.deflt] = first
	defer sp.closeUnused()

	as, err := sp.gather(ctx, names, n)
	if err != nil {
		return nil, err
	}
	return as, nil
}

// spread is the sessions a gathering across avatars has opened.
//
// They are opened as they are wanted and closed at the end unless
// something was taken on them: a connection to an avatar we did not use
// is a stream the daemon is holding for nothing.
type spread struct {
	o     Options
	deflt string
	open  map[string]*sl.Session
	used  map[string]bool
}

// session dials an avatar, or answers with the one already open.
func (sp *spread) session(ctx context.Context, name string) (*sl.Session, error) {
	if s, ok := sp.open[name]; ok {
		return s, nil
	}
	next := sp.o
	next.Agent = name
	s, err := Connect(ctx, next)
	if err != nil {
		return nil, err
	}
	sp.open[name] = s
	return s, nil
}

func (sp *spread) closeUnused() {
	for name, s := range sp.open {
		if !sp.used[name] {
			s.Close()
		}
	}
}

// gather takes n places across the avatars, all of them or none.
func (sp *spread) gather(ctx context.Context, names []string, n int) ([]*Auto, error) {
	said := false
	for {
		var (
			took     = map[string][]int{}
			total    int
			waitOn   *sl.Session
			waitName string
		)
		for _, name := range names {
			if total >= n {
				break
			}
			s, err := sp.session(ctx, name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
				continue
			}
			got, busy, err := tryOn(ctx, s, n-total)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
				continue
			}
			if len(got) > 0 {
				took[name] = got
				total += len(got)
			}
			if busy != "" && waitName == "" {
				waitOn, waitName = s, busy
			}
		}

		if total >= n {
			return sp.wear(ctx, names, took)
		}

		// Short.  Everything goes back, everywhere, before anything is
		// waited for: places held on one avatar while waiting for
		// another is exactly the shape this is built to avoid.
		for name, slots := range took {
			releaseSlots(sp.open[name], slots)
		}
		if waitName == "" {
			return nil, fmt.Errorf("%d objects are free of the %d asked for, "+
				"and nothing is holding the rest", total, n)
		}
		if !said {
			fmt.Fprintf(os.Stderr,
				"%d of the %d objects are busy across %d avatars; waiting\n",
				n-total, n, len(names))
			said = true
		}
		if err := waitFor(ctx, waitOn, waitName); err != nil {
			return nil, err
		}
	}
}

// wear puts on what was taken, in the daemon's order of avatars so that
// the places come back in a settled order rather than a map's.
func (sp *spread) wear(ctx context.Context, names []string, took map[string][]int) ([]*Auto, error) {
	sp.used = map[string]bool{}
	var out []*Auto
	for _, name := range names {
		slots := took[name]
		if len(slots) == 0 {
			continue
		}
		s := sp.open[name]
		a, err := wearSlots(ctx, s, slots)
		if err != nil {
			// One avatar's objects failing is the end of it: the
			// caller asked for a number and this is fewer.  Everything
			// taken goes back, here and on the others.
			releaseSlots(s, slots)
			for _, done := range out {
				done.Release()
			}
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		sp.used[name] = true
		out = append(out, a)
	}
	return out, nil
}
