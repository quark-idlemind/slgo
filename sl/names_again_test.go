package sl

// A name a script changed.  A script that renames its object puts
// nothing on the wire, so a lookup by name asks again.
// Why: doc/objects.md#a-name-a-script-changed

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	againRootA = msg.MustParseUUID("09477e57-7e57-c0de-3bf0-596024ac730d")
	againRootB = msg.MustParseUUID("1e277e57-7e57-c0de-137b-e1054c0b23ed")
	againKidA  = msg.MustParseUUID("33b17e57-7e57-c0de-2283-b73e3ba60382")
	againKidB  = msg.MustParseUUID("46e97e57-7e57-c0de-29c2-29bfc025e869")
)

// aRegionOfScripts is a simulator whose objects can be renamed by a
// script: it answers a family request for a root and a selection for
// anything, with the name the object has now, and folds each answer
// into the fake's store the way the daemon's store does.
type aRegionOfScripts struct {
	mu    sync.Mutex
	name  map[msg.UUID]string // what each object is called now
	local map[uint32]msg.UUID
	roots map[msg.UUID]bool
	quiet map[msg.UUID]bool // never answers
	gate  chan struct{}     // when set, an answer waits for it to close
}

func (r *aRegionOfScripts) rename(id msg.UUID, to string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.name[id] = to
}

// theScriptsRegion puts objects in the fake, nameless, and serves them.
// An object is a root unless it is a key of parents.
func theScriptsRegion(t *testing.T, f *fakeBackend, names map[msg.UUID]string, locals map[msg.UUID]uint32, parents map[msg.UUID]uint32) *aRegionOfScripts {
	r := &aRegionOfScripts{
		name: map[msg.UUID]string{}, local: map[uint32]msg.UUID{},
		roots: map[msg.UUID]bool{}, quiet: map[msg.UUID]bool{},
	}
	f.mu.Lock()
	for id, n := range names {
		r.name[id] = n
		r.local[locals[id]] = id
		if _, child := parents[id]; !child {
			r.roots[id] = true
		}
		f.objects = append(f.objects, &Seen{Object: Object{ID: id, Local: locals[id]}, Parent: parents[id]})
	}
	f.onSend = func(m msg.Message) {
		switch m := m.(type) {
		case *msg.RequestObjectPropertiesFamily:
			if id := m.ObjectData.ObjectID; r.roots[id] {
				if n, ok := r.answer(id); ok {
					f.setStoredName(id, n)
					f.Relay(t, familyReply(id, testAgentID, n))
				}
			}
		case *msg.ObjectSelect:
			for _, d := range m.ObjectData {
				id := r.local[d.ObjectLocalID]
				if n, ok := r.answer(id); ok {
					f.setStoredName(id, n)
					p := propertiesOf(id)
					p.ObjectData[0].Name = append([]byte(n), 0)
					f.Relay(t, p)
				}
			}
		}
	}
	f.mu.Unlock()
	return r
}

// answer is the name an object gives now, and whether it answers.
func (r *aRegionOfScripts) answer(id msg.UUID) (string, bool) {
	r.mu.Lock()
	gate, quiet, n := r.gate, r.quiet[id], r.name[id]
	r.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return n, !quiet
}

// setStoredName is the store learning a name from an answer.  It
// replaces the object rather than writing into it, since a reader may
// hold the old one.
func (f *fakeBackend) setStoredName(id msg.UUID, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, o := range f.objects {
		if o.ID == id {
			c := *o
			c.Name = name
			f.objects[i] = &c
		}
	}
}

func (f *fakeBackend) storedName(id msg.UUID) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.objects {
		if o.ID == id {
			return o.Name
		}
	}
	return ""
}

// lookup runs ObjectsNamed aside, as the relay needs.
func lookup(t *testing.T, w *Session, name string) func() []*Seen {
	t.Helper()
	wait := aside(t, func() ([]*Seen, error) {
		return w.ObjectsNamed(context.Background(), name, 60*time.Second)
	})
	return func() []*Seen {
		t.Helper()
		got, err := wait()
		if err != nil {
			t.Fatalf("ObjectsNamed(%q): %v", name, err)
		}
		return got
	}
}

func idsOf(seen []*Seen) []msg.UUID {
	var out []msg.UUID
	for _, s := range seen {
		out = append(out, s.ID)
	}
	return out
}

// TestARenamedObjectIsNotFoundByItsOldName: the store holds the name as
// last asked, and a lookup asks the candidates again.
func TestARenamedObjectIsNotFoundByItsOldName(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	r := theScriptsRegion(t, f,
		map[msg.UUID]string{againRootA: "Example Box"},
		map[msg.UUID]uint32{againRootA: 11}, nil)

	if got := lookup(t, w, "Example Box")(); len(got) != 1 {
		t.Fatalf("before the rename, %d objects are called Example Box", len(got))
	}
	r.rename(againRootA, "Example Sign")
	if n := f.storedName(againRootA); n != "Example Box" {
		t.Fatalf("the store was told of a rename nobody sent: %q", n)
	}
	if got := lookup(t, w, "Example Box")(); len(got) != 0 {
		t.Errorf("the old name still finds %v", idsOf(got))
	}
	if got := lookup(t, w, "Example Sign")(); len(got) != 1 {
		t.Errorf("the new name finds %d objects, want 1", len(got))
	}
}

// TestANewNameIsFoundByAskingEveryName: nothing has the name the store
// knows, so the whole region is asked again.
func TestANewNameIsFoundByAskingEveryName(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	r := theScriptsRegion(t, f,
		map[msg.UUID]string{againRootA: "Example Box", againRootB: "Example Chair"},
		map[msg.UUID]uint32{againRootA: 11, againRootB: 12}, nil)
	if got := lookup(t, w, "Example Box")(); len(got) != 1 {
		t.Fatalf("%d objects are called Example Box", len(got))
	}
	r.rename(againRootB, "Example Sign")
	f.Forget()

	got := lookup(t, w, "Example Sign")()
	if len(got) != 1 || got[0].ID != againRootB {
		t.Fatalf("Example Sign finds %v, want %s", idsOf(got), againRootB)
	}
	if asked := familyAsked(f); !asked[againRootA] || !asked[againRootB] {
		t.Errorf("family requests went to %v, want both roots", asked)
	}
}

// TestOneOfTwoObjectsOfAName: two objects called alike, one renamed by
// its script, are one object by that name and not two.
func TestOneOfTwoObjectsOfAName(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	r := theScriptsRegion(t, f,
		map[msg.UUID]string{againRootA: "Example Box", againRootB: "Example Box"},
		map[msg.UUID]uint32{againRootA: 11, againRootB: 12}, nil)
	if got := lookup(t, w, "Example Box")(); len(got) != 2 {
		t.Fatalf("%d objects are called Example Box, want 2", len(got))
	}
	r.rename(againRootB, "Example Chair")
	got := lookup(t, w, "Example Box")()
	if len(got) != 1 || got[0].ID != againRootA {
		t.Errorf("Example Box finds %v, want only %s", idsOf(got), againRootA)
	}
}

// TestOnlyTheCandidatesAreAskedAgain: the objects with the name are
// asked again, roots by the family request and children by selection;
// the others are left alone while something has the name.
func TestOnlyTheCandidatesAreAskedAgain(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	r := theScriptsRegion(t, f,
		map[msg.UUID]string{
			againRootA: "Example Box", againKidA: "Example Box",
			againRootB: "Example Chair", againKidB: "Example Chair",
		},
		map[msg.UUID]uint32{againRootA: 11, againKidA: 12, againRootB: 13, againKidB: 14},
		map[msg.UUID]uint32{againKidA: 11, againKidB: 13})
	if got := lookup(t, w, "Example Box")(); len(got) != 2 {
		t.Fatalf("%d objects are called Example Box, want 2", len(got))
	}
	r.rename(againKidA, "Example Sign")
	f.Forget()

	got := lookup(t, w, "Example Box")()
	if len(got) != 1 || got[0].ID != againRootA {
		t.Errorf("Example Box finds %v, want only the root", idsOf(got))
	}
	if asked := familyAsked(f); len(asked) != 1 || !asked[againRootA] {
		t.Errorf("family requests went to %v, want the root with the name", asked)
	}
	if sel := selectedLocals(f); len(sel) != 1 || !sel[12] {
		t.Errorf("selected %v, want the child with the name", sel)
	}
	everySelectIsGivenBack(t, f)
}

// TestEveryNameIsAskedAgainOncePerWindow: lookups that miss inside the
// window look at what the last full re-ask left, and one after it asks
// again.
func TestEveryNameIsAskedAgainOncePerWindow(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	const window = 3 * time.Second
	w.SetOptions(Options{NamesAskedAgainEvery: window})
	theScriptsRegion(t, f,
		map[msg.UUID]string{againRootA: "Example Box", againRootB: "Example Chair"},
		map[msg.UUID]uint32{againRootA: 11, againRootB: 12}, nil)
	if got := lookup(t, w, "Example Box")(); len(got) != 1 {
		t.Fatalf("%d objects are called Example Box", len(got))
	}
	asks := func() int { return len(sentOf[*msg.RequestObjectPropertiesFamily](f)) }

	f.Forget()
	lookup(t, w, "Example Slider")()
	if n := asks(); n != 2 {
		t.Fatalf("a first miss asked %d times, want once for each of two objects", n)
	}
	lookup(t, w, "Example Slider")()
	lookup(t, w, "Example Tip Jar")()
	if n := asks(); n != 2 {
		t.Errorf("misses inside the window asked again: %d requests, want 2", n)
	}

	// Window is measured from the end of the last re-ask; this is more
	// than the window and less than three times it.
	if err := w.Settle(context.Background(), window+window/2); err != nil {
		t.Fatal(err)
	}
	lookup(t, w, "Example Slider")()
	if n := asks(); n != 4 {
		t.Errorf("a miss after the window made %d requests in all, want 4", n)
	}
}

// TestConcurrentMissesShareOneFullReask: a lookup that misses while
// another is asking everything waits for it.
func TestConcurrentMissesShareOneFullReask(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	r := theScriptsRegion(t, f,
		map[msg.UUID]string{againRootA: "Example Box", againRootB: "Example Chair"},
		map[msg.UUID]uint32{againRootA: 11, againRootB: 12}, nil)
	if got := lookup(t, w, "Example Box")(); len(got) != 1 {
		t.Fatalf("%d objects are called Example Box", len(got))
	}
	f.Forget()

	// Hold every answer, so that the first re-ask is still going when
	// the second lookup comes.
	gate := make(chan struct{})
	r.mu.Lock()
	r.gate = gate
	r.mu.Unlock()

	first := lookup(t, w, "Example Slider")
	waitSent[*msg.RequestObjectPropertiesFamily](t, f)
	secondDone := make(chan []*Seen, 1)
	second := aside(t, func() ([]*Seen, error) {
		got, err := w.ObjectsNamed(context.Background(), "Example Tip Jar", 60*time.Second)
		secondDone <- got
		return got, err
	})
	if err := w.Settle(context.Background(), 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondDone:
		t.Fatal("the second lookup came back while the first was still asking")
	default:
	}
	close(gate)
	first()
	if _, err := second(); err != nil {
		t.Fatal(err)
	}
	if n := len(sentOf[*msg.RequestObjectPropertiesFamily](f)); n != 2 {
		t.Errorf("%d family requests for two objects, want 2: the lookups did not share", n)
	}
}

// TestAnObjectThatDoesNotAnswerKeepsItsName: the wait is for an answer
// newer than the asking, so the old name does not stand for one; and an
// object that never answers is given up on by resolve's quiet rule and
// keeps the name it had.
func TestAnObjectThatDoesNotAnswerKeepsItsName(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	r := theScriptsRegion(t, f,
		map[msg.UUID]string{againRootA: "Example Box"},
		map[msg.UUID]uint32{againRootA: 11}, nil)
	if got := lookup(t, w, "Example Box")(); len(got) != 1 {
		t.Fatalf("%d objects are called Example Box", len(got))
	}
	r.mu.Lock()
	r.quiet[againRootA] = true
	r.mu.Unlock()
	f.Forget()

	// The quiet rule is four rounds of a second; a wait for the name
	// merely being present would be over at once.
	start := time.Now()
	got := lookup(t, w, "Example Box")()
	took := time.Since(start)
	if len(got) != 1 || got[0].ID != againRootA {
		t.Errorf("Example Box finds %v, want the object that did not answer", idsOf(got))
	}
	if took < 3*time.Second {
		t.Errorf("took %v: the old name was taken for an answer", took)
	}
	if took > 15*time.Second {
		t.Errorf("took %v, over three times the quiet rule", took)
	}
}
