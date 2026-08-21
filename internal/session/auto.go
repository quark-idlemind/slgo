package session

// Finding somewhere to run, when there are several avatars and several
// groups of objects on each.
//
// # The shape of the problem
//
// A benchmark needs AutoGroupSize objects to itself for as long as it
// runs, because each carries its base reading in its own linkset data.
// One avatar wears enough for several benchmarks at once, and a daemon
// may hold several avatars.  So "where do I run?" has two answers to
// find, and the second depends on the first.
//
// # Why it is not a queue
//
// The old arrangement was one lock over one set of objects: a second
// benchmark waited for the first even when objects were going spare.
// With a pool the wait should be the LAST resort, so this tries every
// group on every avatar before it queues on any of them.
//
// # Why whole groups
//
// A group is taken with ONE lock covering all of it.  Taking objects one
// at a time would let two benchmarks each hold some and wait for the
// rest, which is a deadlock where a queue is merely slow.  Nothing here
// ever holds one lock while waiting for another, so there is nothing to
// deadlock.
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
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

// Auto is somewhere to run: a session, objects held for as long as
// Release is uncalled, and which avatar it turned out to be.
type Auto struct {
	Session *sl.Session
	Objects []*sl.Object

	// Agent is the profile the objects belong to, which a caller that
	// did not name one should say out loud.
	Agent string

	// Group is which group of the pool this is, for a person reading a
	// log and wondering what else was running.
	Group int

	release func()
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
// With an avatar named, it uses that one and waits its turn if every
// group is busy.  With none named it takes the first free group on the
// first avatar that has one, in the daemon's order -- the default
// first, then the rest -- and only queues, on the default, when nothing
// anywhere is free.
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
	if n > AutoGroupSize {
		return nil, fmt.Errorf("a benchmark takes at most %d objects at once; "+
			"asking for %d would need more than one group and could deadlock",
			AutoGroupSize, n)
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
		"every group on every avatar is busy; waiting for one on %s\n", deflt)
	a, err := useAutoOn(ctx, first, n, true)
	if err != nil {
		first.Close()
		return nil, err
	}
	return a, nil
}

// UseAutoGroups finds somewhere to run n objects' worth of work, in as
// many whole groups as that takes.
//
// This is the wider door beside UseAutoAnywhere, which takes one group
// and is what a benchmark wants: four is the size of a group because
// four is what quarterSearch uses, and a benchmark asking for more would
// be asking for objects it would leave idle.  A program that simply has
// twelve things to do at once wants twelve objects, and the group is the
// unit they come in rather than a ceiling on how many can be had.
//
// It never waits while holding anything.  The first group is taken the
// ordinary way, queue and all, because a caller that gets nothing at all
// has to wait somewhere; every group after that is taken only if it is
// free right now.  So the answer may be narrower than the question --
// four objects where twelve were asked for -- and the caller is expected
// to look at what it got rather than at what it wanted.  Waiting for the
// second group while holding the first is the hold-and-wait that the
// whole-group rule exists to prevent.
//
// Every group is on ONE avatar, the one the first group was found on.
// Spreading across avatars would run some of the scripts as somebody
// else, which is a different thing from running them faster.
//
// All of the sessions in the answer are that one session, and it is the
// caller's to close -- once.
func UseAutoGroups(ctx context.Context, o Options, n int) ([]*Auto, error) {
	if n < 1 {
		n = 1
	}
	want := n
	if want > AutoGroupSize {
		want = AutoGroupSize
	}
	first, err := UseAutoAnywhere(ctx, o, want)
	if err != nil {
		return nil, err
	}
	out := []*Auto{first}
	have := len(first.Objects)
	if have >= n {
		return out, nil
	}
	return append(out, takeGroups(ctx, first.Session, n-have, first.Group)...), nil
}

// takeGroups takes what is free of the groups on one session, in ONE
// pass, and gives back up to n objects' worth of them.
//
// One pass because a lock is re-entrant for the client holding it: the
// daemon's acquire says so in as many words, since a client that asks
// twice still holds it once.  That is right for a client asking again
// and wrong for this -- a second pass would take a group this caller
// already has, wear the same objects a second time, and hand two callers
// the same object to run a script in.  Which group each Auto came from
// is what says a group has been dealt with, so the ones already held are
// named rather than discovered.
//
// Nothing here waits.  A group that is busy is somebody else's and the
// answer is simply smaller.
func takeGroups(ctx context.Context, s *sl.Session, n int, held ...int) []*Auto {
	skip := map[int]bool{}
	for _, g := range held {
		skip[g] = true
	}

	var out []*Auto
	for g := 0; g < AutoGroups() && n > 0; g++ {
		if skip[g] {
			continue
		}
		got, _, err := s.TryLock(ctx, AutoGroupLock(g))
		if err != nil || !got {
			continue
		}
		want := n
		if want > AutoGroupSize {
			want = AutoGroupSize
		}
		a, err := wearGroup(ctx, s, g, want)
		if err != nil || len(a.Objects) == 0 {
			s.Unlock(AutoGroupLock(g))
			// Not fatal and not silent: an object that will not go on
			// is one fewer script at a time, which is slower rather
			// than wrong.
			if err != nil {
				fmt.Fprintf(os.Stderr, "group %d: %v\n", g, err)
			}
			continue
		}
		out = append(out, a)
		n -= len(a.Objects)
	}
	return out
}

// useAutoOn takes a group on one session.
//
// It tries every group without waiting first.  Only if wait is set, and
// nothing was free, does it queue -- on group 0, because a queue has to
// form somewhere and the alternative is a caller waiting on a group that
// frees later than another.
func useAutoOn(ctx context.Context, s *sl.Session, n int, wait bool) (*Auto, error) {
	for g := 0; g < AutoGroups(); g++ {
		got, holder, err := s.TryLock(ctx, AutoGroupLock(g))
		if err != nil {
			return nil, fmt.Errorf("asking for objects on %s: %w\n"+
				"        (an slgod older than the lock does not answer; --rez avoids it)",
				s.Info().Name, err)
		}
		if !got {
			_ = holder
			continue
		}
		a, err := wearGroup(ctx, s, g, n)
		if err != nil {
			s.Unlock(AutoGroupLock(g))
			return nil, err
		}
		return a, nil
	}

	if !wait {
		return nil, fmt.Errorf("every group on %s is busy", s.Info().Name)
	}

	lockCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err := s.Lock(lockCtx, AutoGroupLock(0)); err != nil {
		return nil, fmt.Errorf("waiting for objects on %s: %w", s.Info().Name, err)
	}
	a, err := wearGroup(ctx, s, 0, n)
	if err != nil {
		s.Unlock(AutoGroupLock(0))
		return nil, err
	}
	return a, nil
}

// wearGroup makes sure the group's objects exist and are on.
func wearGroup(ctx context.Context, s *sl.Session, g, n int) (*Auto, error) {
	folder, err := objectsFolder(ctx, s)
	if err != nil {
		return nil, err
	}

	slots := autoGroupSlots(g, n)
	if len(slots) == 0 {
		return nil, fmt.Errorf("group %d has no slots", g)
	}

	// The items have to exist before any of them can be worn, and the
	// highest slot in this group is how far that has to reach.
	if err := EnsureAutoItems(ctx, s, folder, slots[len(slots)-1]+1); err != nil {
		return nil, err
	}

	var objs []*sl.Object
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
	}

	name := AutoGroupLock(g)
	return &Auto{
		Session: s,
		Objects: objs,
		Agent:   s.Info().Name,
		Group:   g,
		release: func() { s.Unlock(name) },
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
