package session

// Turning a grant into objects.
//
// What the daemon decides is tested where it is decided, over the wire
// that carries it -- see server/slots_test.go.  What is left here is the
// half the daemon cannot do and the choosing this side still makes:
// which avatar to ask about, what to do when one cannot supply the lot,
// and turning a place into an object that exists and is worn.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/sl"
)

// grantingGrid is a backend that answers for places the way slgod does.
//
// It is a fakeGrid with the three methods that make a session a granter,
// which is how this package tells a hosted session from a direct one: a
// direct session has no daemon, so there is nobody to ask and nothing to
// share with.
type grantingGrid struct {
	*fakeGrid
	names []string

	mu sync.Mutex

	// free is how many places each avatar has, by name.  An avatar that
	// is not in it has none.
	free map[string]int

	// dirty marks places that come back needing clearing.
	dirty map[string]bool

	asked    []string // "agent:n:try", in order
	released []string
	nextID   int
}

func newGranting(t *testing.T, names ...string) (*sl.Session, *grantingGrid) {
	t.Helper()
	g := &grantingGrid{
		fakeGrid: newFakeGrid(t),
		names:    names,
		free:     map[string]int{},
		dirty:    map[string]bool{},
	}
	g.fakeGrid.stock(len(AutoPoints))
	for _, n := range names {
		g.free[n] = SlotsPerAgentForTest
	}
	if len(names) > 0 {
		g.fakeGrid.info.Name = names[0]
	}
	s, err := sl.New(g)
	if err != nil {
		t.Fatalf("sl.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// A grant may name an avatar this session is not attached to, and
	// opening one for it is the only thing here that dials.  Hand back a
	// session of the test's own instead.
	was := dialFor
	dialFor = func(ctx context.Context, o Options) (*sl.Session, error) {
		other := &grantingGrid{
			fakeGrid: newFakeGrid(t),
			names:    names,
			free:     g.free,
			dirty:    g.dirty,
		}
		other.fakeGrid.stock(len(AutoPoints))
		other.fakeGrid.info.Name = o.Agent
		s2, err := sl.New(other)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { s2.Close() })
		return s2, nil
	}
	t.Cleanup(func() { dialFor = was })

	return s, g
}

// SlotsPerAgentForTest is what these fakes pretend a daemon holds.  It
// is the pool's number and not this package's: what this side knows is
// how to wear a place, not how many there are.
const SlotsPerAgentForTest = 12

func (g *grantingGrid) Sessions(context.Context) ([]string, error) { return g.names, nil }

func (g *grantingGrid) Slots(ctx context.Context, n int, d time.Duration, agent string) (*client.Grant, error) {
	return g.grant(n, agent, false)
}

func (g *grantingGrid) TrySlots(ctx context.Context, n int, d time.Duration, agent string) (*client.Grant, error) {
	return g.grant(n, agent, true)
}

func (g *grantingGrid) ReleaseSlots(id string, clean bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.released = append(g.released, fmt.Sprintf("%s:%v", id, clean))
	return nil
}

// grant hands out places the way the daemon would: from one avatar when
// one was named, and from wherever they are otherwise.
func (g *grantingGrid) grant(n int, agent string, try bool) (*client.Grant, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, fmt.Sprintf("%s:%d:%v", agent, n, try))

	from := g.names
	if agent != "" {
		from = []string{agent}
	}
	var places []client.Place
	for _, name := range from {
		for i := 0; i < g.free[name] && len(places) < n; i++ {
			places = append(places, client.Place{
				Agent: name, Slot: i, Dirty: g.dirty[name],
			})
		}
	}
	if len(places) < n {
		return &client.Grant{Why: fmt.Sprintf("only %d free", len(places))}, nil
	}
	g.nextID++
	return &client.Grant{
		ID:      fmt.Sprintf("g%d", g.nextID),
		Places:  places,
		Expires: time.Now().Add(time.Hour),
	}, nil
}

func (g *grantingGrid) askedFor() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.asked...)
}

func (g *grantingGrid) gaveBack() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.released...)
}

// TestReleasingTwiceGivesBackOneGrant: Release is called from a defer
// and often from the caller as well, and a second one would give back a
// grant that by then belongs to whoever was waiting for it.
func TestReleasingTwiceGivesBackOneGrant(t *testing.T) {
	n := 0
	a := &Auto{release: func() { n++ }}
	a.Release()
	a.Release()
	if n != 1 {
		t.Errorf("the objects were given back %d times", n)
	}

	// One that never took anything has nothing to give back and must
	// not say so by panicking.
	(&Auto{}).Release()
}

// TestASessionWithNoDaemonNeedsNoArbitration: a direct login is one
// process and one avatar.  There is nobody to contend with and nobody to
// ask, so it takes the first places and gets on with it.
func TestASessionWithNoDaemonNeedsNoArbitration(t *testing.T) {
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))

	a, err := useAutoOn(context.Background(), Options{Direct: true}, s, 4)
	if err != nil {
		t.Fatalf("useAutoOn: %v", err)
	}
	if len(a.Objects) != 4 {
		t.Errorf("wore %d objects", len(a.Objects))
	}
	// Nothing was said to be dirty, because nobody said anything: with
	// one process there is nobody whose script could still be running.
	for i, d := range a.Dirty {
		if d {
			t.Errorf("object %d came back dirty from a session with no daemon", i)
		}
	}
}

// TestANamedAvatarIsAskedForByName: asking for qi and being given
// example would be worse than being slow, so the name goes to the daemon
// rather than being something this side hopes for.
func TestANamedAvatarIsAskedForByName(t *testing.T) {
	s, g := newGranting(t, "quark", "example")

	a, err := useAutoOn(context.Background(), Options{Agent: "example"}, s, 4)
	if err != nil {
		t.Fatalf("useAutoOn: %v", err)
	}
	if a.Agent != "example" {
		t.Errorf("ran on %q", a.Agent)
	}
	asked := g.askedFor()
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "example:4:") {
		t.Errorf("asked %v, want one request naming the avatar", asked)
	}
	// And it waits for that avatar rather than falling over to another.
	if strings.HasSuffix(asked[0], ":true") {
		t.Error("a named avatar was asked about without waiting")
	}
}

// TestEveryAvatarIsTriedBeforeAnyIsWaitedFor: this is what the pool is
// for.  Queueing on the first avatar while another is idle is the
// arrangement it replaced.
func TestEveryAvatarIsTriedBeforeAnyIsWaitedFor(t *testing.T) {
	s, g := newGranting(t, "quark", "example")
	g.free["quark"] = 0 // busy

	a, err := useAutoOn(context.Background(), Options{}, s, 4)
	if err != nil {
		t.Fatalf("useAutoOn: %v", err)
	}
	if a.Agent != "example" {
		t.Errorf("ran on %q, want the avatar that was free", a.Agent)
	}
	for _, ask := range g.askedFor() {
		if strings.HasSuffix(ask, ":false") {
			t.Errorf("waited on %q with an avatar going spare", ask)
		}
	}
}

// TestNoAvatarFreeIsAWaitOnTheDefault: the wait is the last resort, and
// it forms on the default rather than on whichever avatar was asked
// last, so that waiting is predictable.
func TestNoAvatarFreeIsAWaitOnTheDefault(t *testing.T) {
	s, g := newGranting(t, "quark", "example")
	g.free["quark"] = 0
	g.free["example"] = 0

	if _, err := useAutoOn(context.Background(), Options{}, s, 4); err == nil {
		t.Fatal("objects came out of a daemon holding none free")
	}
	asked := g.askedFor()
	last := asked[len(asked)-1]
	if !strings.HasPrefix(last, "quark:4:") || !strings.HasSuffix(last, ":false") {
		t.Errorf("the last request was %q, want a wait on the default", last)
	}
}

// TestOneRequestCanBeAnsweredOutOfSeveralAvatars: the whole reason the
// daemon does the deciding.  Twelve scripts at once is twelve objects
// from wherever they are, and each avatar's are worn on that avatar's
// own session.
func TestOneRequestCanBeAnsweredOutOfSeveralAvatars(t *testing.T) {
	s, g := newGranting(t, "quark", "example")
	g.free["quark"] = 2
	g.free["example"] = 4

	as, err := spreadOn(context.Background(), Options{}, s, 6)
	if err != nil {
		t.Fatalf("spreadOn: %v", err)
	}
	if len(as) != 2 {
		t.Fatalf("six objects came back in %d lots, want one per avatar", len(as))
	}
	total := 0
	for _, a := range as {
		total += len(a.Objects)
		if a.Session == nil {
			t.Errorf("%s came back with no session to run on", a.Agent)
		}
		if a.Agent != a.Session.Info().Name {
			t.Errorf("%s's objects are on %s's session", a.Agent, a.Session.Info().Name)
		}
	}
	if total != 6 {
		t.Errorf("%d objects between them, want 6", total)
	}
}

// TestOneGrantIsGivenBackOnce: a grant is granted at once and goes back
// at once, however many avatars it turned out to cover.  Giving it back
// once per avatar would hand the rest to somebody else while this caller
// was still using them.
func TestOneGrantIsGivenBackOnce(t *testing.T) {
	s, g := newGranting(t, "quark", "example")
	g.free["quark"] = 2
	g.free["example"] = 4

	as, err := spreadOn(context.Background(), Options{}, s, 6)
	if err != nil {
		t.Fatalf("spreadOn: %v", err)
	}
	for _, a := range as {
		a.Release()
	}
	if got := g.gaveBack(); len(got) != 1 {
		t.Errorf("the grant was given back %d times: %v", len(got), got)
	}
}

// TestAPlaceThatWasNotLeftCleanSaysSo: a script left running in an
// object is a line the next holder reads as its own, so what the daemon
// says about a place has to reach the caller that has to clear it.
func TestAPlaceThatWasNotLeftCleanSaysSo(t *testing.T) {
	s, g := newGranting(t, "quark")
	g.dirty["quark"] = true

	a, err := useAutoOn(context.Background(), Options{}, s, 2)
	if err != nil {
		t.Fatalf("useAutoOn: %v", err)
	}
	if len(a.Dirty) != len(a.Objects) {
		t.Fatalf("%d objects and %d of them said to be dirty or not",
			len(a.Objects), len(a.Dirty))
	}
	for i, d := range a.Dirty {
		if !d {
			t.Errorf("object %d came back clean when the daemon said otherwise", i)
		}
	}
}

// TestWearingMakesTheItemsFirst: the items have to exist before any of
// them can be worn, and the highest place asked for is how far that has
// to reach -- a place at the end of the pool needs every item below it
// to exist as well, because they are all copies of the first.
func TestWearingMakesTheItemsFirst(t *testing.T) {
	s, f := newFakeSession(t)
	f.stock(1)
	answerCopies(f)

	a, err := wearSlots(context.Background(), s, []int{4, 5, 6, 7}, nil)
	if err != nil {
		t.Fatalf("wearSlots: %v", err)
	}
	if len(a.Objects) != 4 {
		t.Errorf("wore %d objects", len(a.Objects))
	}
	items, err := s.FolderItems(context.Background(), testObjects)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 8 {
		t.Errorf("%d items exist, want everything up to the highest place", len(items))
	}
}

// TestWearingNothingIsRefused: nowhere to run is not somewhere to run
// with no objects in it, and a caller handed an empty Auto would divide
// by the length of it.
func TestWearingNothingIsRefused(t *testing.T) {
	s, _ := newFakeSession(t)
	if _, err := wearSlots(context.Background(), s, nil, nil); err == nil {
		t.Error("wearSlots answered with nowhere to run")
	}
}

// TestWearingStopsAtTheFirstObjectAndCarriesOnAfterIt: no objects at all
// is nowhere to run; some of them is a slower run, and the caller
// already copes with getting fewer than it asked for.
func TestWearingStopsAtTheFirstObjectAndCarriesOnAfterIt(t *testing.T) {
	t.Run("nothing to wear", func(t *testing.T) {
		s, f := newFakeSession(t)
		f.stock(4)
		f.mu.Lock()
		f.objectsErr = fmt.Errorf("the circuit went away")
		f.sendErr = fmt.Errorf("the circuit is gone")
		f.mu.Unlock()

		if _, err := wearSlots(context.Background(), s, fourSlots, nil); err == nil {
			t.Error("wearSlots reported success without its first object")
		}
	})

	t.Run("some of them", func(t *testing.T) {
		s, f := newFakeSession(t)
		// Two objects exist and are on; nothing can be copied to make
		// the rest, and nothing can be built either.
		f.stock(2)
		f.mu.Lock()
		f.sendErr = fmt.Errorf("the circuit is gone")
		f.presenceErr = fmt.Errorf("the parcel will not have it")
		f.mu.Unlock()

		a, err := wearSlots(context.Background(), s, fourSlots, nil)
		if err != nil {
			t.Fatalf("wearSlots: %v", err)
		}
		if len(a.Objects) != 2 {
			t.Errorf("wore %d objects, want the two that exist", len(a.Objects))
		}
		if len(a.Slots) != 2 {
			t.Errorf("%d places came back for %d objects", len(a.Slots), len(a.Objects))
		}
	})
}

// fourSlots is a benchmark's worth of the pool, for the tests that are
// about wearing objects rather than about which places they came from.
var fourSlots = []int{0, 1, 2, 3}

// TestOnlyAHostedSessionKnowsWhoElseThereIs: a direct session is the
// only session there is, and mistaking it for a daemon holding one
// avatar would have this looking for others that cannot exist.
func TestOnlyAHostedSessionKnowsWhoElseThereIs(t *testing.T) {
	s, _ := newFakeSession(t)
	if _, err := sessionNames(context.Background(), s); err == nil {
		t.Error("a direct session was asked who else the daemon holds")
	}
}
