package session

// Finding somewhere to run, when there are several avatars and a pool of
// objects on each.
//
// # Who decides
//
// The daemon.  It is the only thing that can see every client at once,
// so it is the only thing that can answer "give me twelve" without two
// callers each ending up with some of what the other needs.  What
// arrives here is a GRANT: a number of places, each an avatar and a
// number, held until this connection gives them back or goes away.
// Why: doc/slots.md#locks-a-group-at-a-time
//
// # What is left here
//
// Turning a place into an object, which is the half the daemon cannot
// do: the item's name is the only thing that says an object belongs to
// the pool, names live in AIS rather than on the wire, and it is this
// side that wears a missing object and makes a missing item.
//
// # Why the avatar is still chosen here
//
// Only the caller knows whether it minds which avatar: two avatars may
// be standing in different regions, and a program running a dozen
// separate scripts does not care.  So there are two doors --
// UseAutoAnywhere, which puts everything on one avatar, and
// UseAutoSpread, which does not care -- and a named avatar is honoured
// exactly by both.  slrun and slbench both go through UseAutoSpread.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/sl"
)

// dialFor opens a session to another of the daemon's avatars.
//
// A variable because it is the one place here that dials in the middle
// of an operation -- a grant may name an avatar this caller has no
// session for -- and a test that had to stand up a daemon to reach the
// three lines after it would be testing the dialling.
var dialFor = Connect

// autoTimeout is how long a grant is asked for.
//
// Long, because what it guards against is a client that wedged without
// dying rather than one that is merely slow, and a benchmark's search
// finishes when it converges.  sl.Hosted.RenewSlots is there for work
// that outlives it, though nothing here calls it.
const autoTimeout = 30 * time.Minute

// Auto is somewhere to run: a session, objects held for as long as
// Release is uncalled, and which avatar it turned out to be.
type Auto struct {
	Session *sl.Session
	Objects []*sl.Object

	// Agent is the profile the objects belong to, which a caller that
	// did not name one should say out loud.
	Agent string

	// Slots is which of the avatar's places these objects came from.
	Slots []int

	// Dirty says, for each object in turn, that the last holder did not
	// leave it fit to use: whatever it was running may still be running,
	// and a script left running is a line this caller would read as its
	// own.  Clear it before listening to it.
	Dirty []bool

	release func()
}

// Where names the places held, for a log line.
func (a *Auto) Where() string {
	var b []byte
	for i, s := range a.Slots {
		if i > 0 {
			b = append(b, ',', ' ')
		}
		b = append(b, fmt.Sprint(s)...)
	}
	return string(b)
}

// Release gives the objects back.  So does going away: the daemon frees
// what a client holds when its stream ends.
//
// One call gives back the whole grant, however many Autos it was spread
// over, and closes the sessions opened for it along the way: those for
// the other avatars, and the one that asked when no work ran on it.  The
// session that asked, when work ran on it, is left for the caller to
// close.  Closing one of the others again does no harm.
func (a *Auto) Release() {
	if a.release != nil {
		a.release()
		a.release = nil
	}
}

// granter is a session that can ask the daemon for places.  A direct
// session cannot, and does not need to: one process, one avatar, nothing
// to contend with.
type granter interface {
	SlotsWithin(ctx context.Context, n int, timeout, wait time.Duration, agent string) (*client.Grant, error)
	TrySlots(ctx context.Context, n int, timeout time.Duration, agent string) (*client.Grant, error)
	ReleaseSlots(id string, clean bool) error
}

// UseAutoAnywhere finds somewhere to run n objects' worth of work, all
// of it on ONE avatar.
//
// With an avatar named it uses that one and waits its turn.  With none
// named it tries each avatar the daemon holds without waiting, in the
// daemon's order -- the default first -- and only queues, on the
// default, when no single avatar can supply the lot.
//
// All n or none.  A caller given four of the eight it asked for would
// either hold them while waiting for the rest, which is the deadlock, or
// give them back; the daemon gives them back for it.
//
// The caller closes the Auto's session when it is done with it; see
// Release for the sessions that closes first.
func UseAutoAnywhere(ctx context.Context, o Options, n int) (*Auto, error) {
	o.Agent = AgentName(o.Agent)
	if n < 1 {
		n = 1
	}

	s, err := Connect(ctx, o)
	if err != nil {
		return nil, err
	}
	a, err := useAutoOn(ctx, o, s, n)
	if err != nil {
		s.Close()
		return nil, err
	}
	return a, nil
}

// useAutoOn is the choosing, on a session already open.
func useAutoOn(ctx context.Context, o Options, s *sl.Session, n int) (*Auto, error) {
	g, ok := s.Backend().(granter)
	if !ok {
		// A direct session: no daemon, so nothing to arbitrate with and
		// nobody to arbitrate against.
		return wearSlots(ctx, s, countUp(n), nil)
	}

	// A named avatar is the simple case: that one, waiting.
	if o.Agent != "" {
		return grantOn(ctx, o, s, g, n, o.Agent, true)
	}

	// Nobody named.  Try each avatar without waiting before queueing on
	// any of them, which is what the pool is for.
	deflt := s.Info().Name
	names, err := sessionNames(ctx, s)
	if err != nil || len(names) <= 1 {
		return grantOn(ctx, o, s, g, n, deflt, true)
	}
	tried := map[string]bool{}
	var others []string
	for _, name := range append([]string{deflt}, names...) {
		if tried[name] {
			continue // the default is first and is also in the list
		}
		tried[name] = true
		a, err := grantOn(ctx, o, s, g, n, name, false)
		if err == nil {
			return a, nil
		}
		if name != deflt {
			others = append(others, name+": "+firstLine(err))
		}
	}

	fmt.Fprintf(os.Stderr,
		"no avatar has %d objects free; waiting for them on %s\n", n, deflt)
	a, err := grantOn(ctx, o, s, g, n, deflt, true)
	if err != nil {
		// Each avatar's own reason, one line apiece: the default's is
		// the wait just given up, and the others' their answers before.
		return nil, fmt.Errorf("%s: %w\n%s", deflt, err, strings.Join(others, "\n"))
	}
	return a, nil
}

// firstLine is an error's first line, for a list with one line each.
func firstLine(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	return line
}

// grantOn asks for n places on one avatar and wears them.
//
// The session that ASKED holds the grant, because the grant belongs to a
// stream; the session the work runs on may be another one, when the
// places turned out to be somebody else's.  Both are kept until the
// caller lets go.  On a failure the asking session is left open, being
// the caller's, and useAutoOn asks on it again.
func grantOn(ctx context.Context, o Options, asked *sl.Session, g granter, n int, agent string, wait bool) (*Auto, error) {
	var (
		got *client.Grant
		err error
	)
	if wait {
		got, err = g.SlotsWithin(ctx, n, autoTimeout, o.Wait, agent)
	} else {
		got, err = g.TrySlots(ctx, n, autoTimeout, agent)
	}
	if err != nil {
		return nil, askFailed(ctx, err)
	}
	if !got.Held() {
		return nil, fmt.Errorf("%s", why(got, n, agent))
	}

	as, err := wearGrant(ctx, o, asked, g, got)
	if err != nil {
		return nil, err
	}
	if len(as) != 1 {
		// Asked for one avatar and given several, or none, which the
		// daemon does not do.  Better to say so than to hand back half
		// of it.  The sessions opened for the others go with the grant;
		// the asking one is the caller's, and may be asked again.
		release(g, got, false)
		for _, a := range as {
			if a.Session != asked {
				a.Session.Close()
			}
		}
		if len(as) == 0 {
			return nil, fmt.Errorf("the daemon granted %d objects on %s "+
				"and named no places for them", n, agent)
		}
		return nil, fmt.Errorf("the daemon spread %d objects over %d avatars "+
			"for a request that named %s", n, len(as), agent)
	}
	return as[0], nil
}

// UseAutoSpread finds somewhere to run n objects' worth of work, from as
// many of the daemon's avatars as it takes.
//
// It is for work that is simply WIDE: a program with twelve scripts to
// run at once wants twelve objects and does not care whose they are.  A
// named avatar is honoured exactly and is one avatar.
//
// Each Auto has its own session, which the caller closes when it is done
// with it; see Release for the sessions that closes first.
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

	s, err := Connect(ctx, o)
	if err != nil {
		return nil, err
	}
	as, err := spreadOn(ctx, o, s, n)
	if err != nil {
		s.Close()
		return nil, err
	}
	return as, nil
}

// spreadOn is the asking, on a session already open.
func spreadOn(ctx context.Context, o Options, s *sl.Session, n int) ([]*Auto, error) {
	g, ok := s.Backend().(granter)
	if !ok {
		a, err := wearSlots(ctx, s, countUp(n), nil)
		if err != nil {
			return nil, err
		}
		return []*Auto{a}, nil
	}

	got, err := g.SlotsWithin(ctx, n, autoTimeout, o.Wait, "")
	if err != nil {
		return nil, askFailed(ctx, err)
	}
	if !got.Held() {
		return nil, fmt.Errorf("%s", why(got, n, ""))
	}
	return wearGrant(ctx, o, s, g, got)
}

// askFailed says what went wrong asking for places.  A bounded wait
// that ran out is client.ErrStillBusy, and a wait the caller gave up on
// is ctx's own error, for the caller to put in its own words; anything
// else is most likely a daemon too old to answer.
func askFailed(ctx context.Context, err error) error {
	if errors.Is(err, client.ErrStillBusy) || ctx.Err() != nil {
		return err
	}
	return fmt.Errorf("asking for objects: %w\n"+
		"        (an slgod older than the pool does not answer; --rez avoids it)", err)
}

// why says what a refusal came to, in the daemon's words when it gave
// any.
func why(got *client.Grant, n int, agent string) string {
	if got != nil && got.Why != "" {
		return got.Why
	}
	if agent != "" {
		return fmt.Sprintf("%s has no %d objects free", agent, n)
	}
	return fmt.Sprintf("no %d objects are free", n)
}

// wearGrant turns a grant into objects, one Auto per avatar.
//
// The session that asked holds the grant.  Sessions for the other
// avatars are opened here and closed when the grant is let go, along
// with the asking one if the work never ran on it.  A grant that cannot
// be worn goes back at once with the sessions opened here, and the
// asking one is left open: it is the caller's.
func wearGrant(ctx context.Context, o Options, asked *sl.Session, g granter, got *client.Grant) ([]*Auto, error) {
	byAgent := map[string][]client.Place{}
	var order []string
	for _, p := range got.Places {
		if _, seen := byAgent[p.Agent]; !seen {
			order = append(order, p.Agent)
		}
		byAgent[p.Agent] = append(byAgent[p.Agent], p)
	}
	sort.Strings(order)

	// One release for the whole grant, however many avatars it covers:
	// it was granted at once and it goes back at once.
	var (
		once  sync.Once
		extra []*sl.Session
		used  bool
	)
	give := func() {
		once.Do(func() {
			release(g, got, false)
			for _, s := range extra {
				s.Close()
			}
			if !used {
				asked.Close()
			}
		})
	}
	// A grant that could not be worn: the asking session is not closed,
	// whether or not objects were worn on it.  What was worn stays worn,
	// where the pool keeps its objects and where a released grant leaves
	// them too; the grant goes back dirty, so the next holder clears it.
	fail := func(name string, err error) ([]*Auto, error) {
		release(g, got, false)
		for _, s := range extra {
			s.Close()
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	var out []*Auto
	for _, name := range order {
		s := asked
		if name != asked.Info().Name {
			// The same daemon, a different avatar: what the caller
			// asked for, with the name changed.
			next := o
			next.Agent = name
			s2, err := dialFor(ctx, next)
			if err != nil {
				return fail(name, err)
			}
			extra = append(extra, s2)
			s = s2
		} else {
			used = true
		}

		places := byAgent[name]
		sort.Slice(places, func(i, j int) bool { return places[i].Slot < places[j].Slot })
		slots := make([]int, 0, len(places))
		dirty := make([]bool, 0, len(places))
		for _, p := range places {
			slots = append(slots, p.Slot)
			dirty = append(dirty, p.Dirty)
		}

		a, err := wearSlots(ctx, s, slots, dirty)
		if err != nil {
			return fail(name, err)
		}
		a.release = give
		out = append(out, a)
	}
	return out, nil
}

// release gives a grant back, saying whether the objects were left fit
// for the next caller.
func release(g granter, got *client.Grant, clean bool) {
	if got != nil && got.ID != "" {
		g.ReleaseSlots(got.ID, clean)
	}
}

// countUp is the first n places, for a session with nobody to share
// them with.
func countUp(n int) []int {
	out := make([]int, 0, n)
	for i := 0; i < n && i < len(AutoPoints); i++ {
		out = append(out, i)
	}
	return out
}

// wearSlots makes sure the objects for these places exist and are on.
func wearSlots(ctx context.Context, s *sl.Session, slots []int, dirty []bool) (*Auto, error) {
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

	a := &Auto{Session: s, Agent: s.Info().Name}
	for i, slot := range slots {
		// AttachAdd, because past the eighth slot two objects share a
		// point and a bare attach would throw the first one off.
		at, err := s.EnsureAttached(ctx, folder, AutoName(slot), AutoPoints[slot]|sl.AttachAdd)
		if err != nil {
			if len(a.Objects) == 0 {
				return nil, err
			}
			fmt.Fprintf(os.Stderr, "only %d of %d objects: %v\n", len(a.Objects), len(slots), err)
			break
		}
		obj := at.Object
		a.Objects = append(a.Objects, &obj)
		a.Slots = append(a.Slots, slot)
		a.Dirty = append(a.Dirty, i < len(dirty) && dirty[i])
	}
	return a, nil
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
