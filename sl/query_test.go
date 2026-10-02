package sl

// Asking what is in the region, and what any of it is called.
//
// Almost none of this is a question the simulator answers.  An
// ObjectUpdate carries no name, so a name is only ever had by asking for
// it one object at a time and waiting for replies that may never come --
// an object can go away between being described and being asked about.
// That is why resolve gives up when the answers stop rather than
// insisting on all of them, and why it does so only after several quiet
// rounds: stopping at the first second with nothing new in it stops
// before anything has arrived at all, which looks exactly like a region
// full of nameless objects.
//
// The calls that wait for names run aside, and the fake answers the
// property requests the way a simulator does; see fake_test.go.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// answerFamily makes the fake reply to RequestObjectPropertiesFamily the
// way a simulator does, from a table of what is called what.
//
// A backend that never answers turns every name lookup into the whole of
// its timeout, which is the difference between a test that takes a
// moment and one that takes ninety seconds.
func answerFamily(t *testing.T, f *fakeBackend, names map[msg.UUID]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		q, ok := m.(*msg.RequestObjectPropertiesFamily)
		if !ok {
			return
		}
		name, ok := names[q.ObjectData.ObjectID]
		if !ok {
			return // an object that has gone away is never answered for
		}
		f.Relay(t, familyReply(q.ObjectData.ObjectID, testAgentID, name))
	}
}

// TestWhereAndTheDrawDistanceAreTheSameQuestion: the draw distance is
// part of the presence rather than a setting of its own, because setting
// it is what the simulator is told and the answer is what it ended up
// with.
func TestWhereAndTheDrawDistanceAreTheSameQuestion(t *testing.T) {
	w, f := newFakeSession(t)

	p, err := w.Where(context.Background())
	if err != nil {
		t.Fatalf("Where: %v", err)
	}
	if p.Region != "Test Region" || p.Position.X != 128 {
		t.Errorf("Where = %+v", p)
	}

	p, err = w.SetDrawDistance(context.Background(), 256)
	if err != nil {
		t.Fatalf("SetDrawDistance: %v", err)
	}
	if p.DrawDistance != 256 {
		t.Errorf("the draw distance came back as %v", p.DrawDistance)
	}

	// Zero would mean "leave it as it is" to the backend, so asking for
	// it here is a caller that meant something else.
	if _, err := w.SetDrawDistance(context.Background(), 0); err == nil {
		t.Error("SetDrawDistance accepted a distance of nothing")
	}
	if _, err := w.SetDrawDistance(context.Background(), -1); err == nil {
		t.Error("SetDrawDistance accepted a negative distance")
	}

	f.mu.Lock()
	f.presenceErr = errors.New("nothing knows where we are")
	f.mu.Unlock()
	if _, err := w.SetDrawDistance(context.Background(), 64); err == nil {
		t.Error("SetDrawDistance reported a presence it could not read")
	}
}

// TestActiveGroupComesFromThePresence: a fresh login has no group
// active, and a parcel usually grants building to a group rather than to
// individuals -- so this is the answer to "why will it not let me
// build", and it must not be confused with a presence that could not be
// read at all.
func TestActiveGroupComesFromThePresence(t *testing.T) {
	w, f := newFakeSession(t)

	got, err := w.ActiveGroup(context.Background())
	if err != nil {
		t.Fatalf("ActiveGroup: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("a session that never activated one is acting as %s", got)
	}

	f.mu.Lock()
	f.presence.ActiveGroup = theOther
	f.presenceErr = nil
	f.mu.Unlock()
	if got, err = w.ActiveGroup(context.Background()); err != nil || got != theOther {
		t.Errorf("ActiveGroup = %s, %v", got, err)
	}

	f.mu.Lock()
	f.presenceErr = errors.New("nothing knows where we are")
	f.mu.Unlock()
	if _, err := w.ActiveGroup(context.Background()); err == nil {
		t.Error("ActiveGroup answered from a presence it could not read")
	}
}

// TestWhatIsSeenSaysWhatKindOfThingItIs: a region describes avatars and
// prims through the same message, and the two want entirely different
// treatment -- so the distinction has to be readable off what came back
// rather than guessed from the name.
func TestWhatIsSeenSaysWhatKindOfThingItIs(t *testing.T) {
	prim := &Seen{Object: Object{ID: thePrim, Local: 77}, PCode: pcodePrim}
	if prim.IsAvatar() || !prim.IsRoot() {
		t.Errorf("an unlinked prim reads as %+v", prim)
	}
	child := &Seen{Object: Object{ID: theChild, Local: 78}, PCode: pcodePrim, Parent: 77}
	if child.IsRoot() {
		t.Error("a prim linked under another was reported as a root")
	}
	av := &Seen{Object: Object{ID: theOther}, PCode: pcodeAvatar}
	if !av.IsAvatar() {
		t.Error("an avatar was reported as a prim")
	}
}

// TestFacesNeedsSomethingToHaveDescribedThem: the appearance travels in
// an ObjectUpdate and nothing asks for it, so an object nobody has heard
// described has no faces to unpack -- which is a different answer from
// an object with no faces.
func TestFacesNeedsSomethingToHaveDescribedThem(t *testing.T) {
	s := &Seen{Object: Object{ID: thePrim}}
	if _, err := s.Faces(6); err == nil {
		t.Error("Faces unpacked an appearance nothing had described")
	}

	s.TextureEntry = teBytes(t, defaultBoxTE)
	faces, err := s.Faces(6)
	if err != nil {
		t.Fatalf("Faces: %v", err)
	}
	if len(faces) != 6 {
		t.Errorf("got %d faces, want 6", len(faces))
	}
}

// TestObjectByIdAsksForTheNameItWasNotGiven: an ObjectUpdate carries no
// name, so an object the region has described is known and nameless
// until somebody asks -- and a caller handed the nameless answer would
// go on to look it up itself.
func TestObjectByIdAsksForTheNameItWasNotGiven(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	answerFamily(t, f, map[msg.UUID]string{thePrim: "workbench"})
	f.mu.Lock()
	f.objects = []*Seen{{Object: Object{ID: thePrim, Local: 77}}}
	f.mu.Unlock()

	wait := aside(t, func() (*Seen, error) {
		return w.ObjectByID(context.Background(), thePrim, 5*time.Second)
	})
	got, err := wait()
	if err != nil {
		t.Fatalf("ObjectByID: %v", err)
	}
	if got.ID != thePrim {
		t.Errorf("ObjectByID = %+v", got)
	}
	if len(sentOf[*msg.RequestObjectPropertiesFamily](f)) == 0 {
		t.Error("nothing was asked about an object with no name")
	}

	// One that already has a name costs nothing.
	f.Forget()
	f.mu.Lock()
	f.objects = []*Seen{{Object: Object{ID: thePrim, Local: 77, Name: "workbench"}}}
	f.mu.Unlock()
	if got, err = w.ObjectByID(context.Background(), thePrim, 0); err != nil {
		t.Fatalf("ObjectByID: %v", err)
	}
	if got.Name != "workbench" {
		t.Errorf("ObjectByID = %+v", got)
	}
	if n := len(sentOf[*msg.RequestObjectPropertiesFamily](f)); n != 0 {
		t.Errorf("%d name requests went out for an object already named", n)
	}
}

// TestObjectByIdSaysWhenThereIsNothingToLookAt: an object beyond the
// draw distance is not merely unnamed, it is unknown -- nothing has been
// said about it at all -- and a caller told only "not found" would look
// for it in inventory.
func TestObjectByIdSaysWhenThereIsNothingToLookAt(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	_, err := w.ObjectByID(context.Background(), thePrim, time.Second)
	if err == nil || !strings.Contains(err.Error(), "draw distance") {
		t.Errorf("ObjectByID = %v, want it to say why nothing was found", err)
	}

	f.mu.Lock()
	f.objectsErr = errors.New("the cache is gone")
	f.mu.Unlock()
	if _, err := w.ObjectByID(context.Background(), thePrim, time.Second); err == nil {
		t.Error("ObjectByID answered from a region it could not read")
	}
}

// TestObjectByIdPassesOnAFailedLookup: the name request goes over the
// wire, and a circuit that has gone is not an object without a name.
func TestObjectByIdPassesOnAFailedLookup(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.objects = []*Seen{{Object: Object{ID: thePrim, Local: 77}}}
	f.mu.Unlock()
	f.FailSends(errors.New("the circuit is gone"))

	if _, err := w.ObjectByID(context.Background(), thePrim, time.Second); err == nil {
		t.Error("ObjectByID reported an object though it could not ask what it was")
	}
}

// TestAllObjectsNamesWhatNobodyHasAskedAbout: filling the names is the
// expensive half -- one request per object, which on a first run is all
// of them -- so they go out in batches rather than as a flood, and the
// answers are remembered so the second caller pays nothing.
func TestAllObjectsNamesWhatNobodyHasAskedAbout(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	// More than one batch, so the instalments and the pause between
	// them are what actually happens rather than a branch nothing takes.
	names := map[msg.UUID]string{}
	var objs []*Seen
	for i := range resolveBatch + 5 {
		id := msg.UUID{byte(i + 1), 0xaa}
		objs = append(objs, &Seen{Object: Object{ID: id, Local: uint32(i + 1)}})
		names[id] = fmt.Sprintf("prim %d", i)
	}
	answerFamily(t, f, names)
	f.mu.Lock()
	f.objects = objs
	f.mu.Unlock()

	wait := aside(t, func() ([]*Seen, error) {
		return w.AllObjects(context.Background(), 20*time.Second)
	})
	got, err := wait()
	if err != nil {
		t.Fatalf("AllObjects: %v", err)
	}
	if len(got) != len(objs) {
		t.Errorf("AllObjects returned %d of %d", len(got), len(objs))
	}
	// Sorted by local id, because the two backends build the list
	// differently and one of them iterates a map.
	for i := 1; i < len(got); i++ {
		if got[i-1].Local > got[i].Local {
			t.Fatalf("the answer is not in local id order: %d then %d",
				got[i-1].Local, got[i].Local)
		}
	}
	if n := len(sentOf[*msg.RequestObjectPropertiesFamily](f)); n != len(objs) {
		t.Errorf("%d name requests went out for %d objects", n, len(objs))
	}

	// Everything already named: nothing more is asked.
	f.Forget()
	f.mu.Lock()
	for _, o := range f.objects {
		o.Name = "prim"
	}
	f.mu.Unlock()
	if _, err := w.AllObjects(context.Background(), 0); err != nil {
		t.Fatalf("AllObjects: %v", err)
	}
	if n := len(sentOf[*msg.RequestObjectPropertiesFamily](f)); n != 0 {
		t.Errorf("%d name requests went out for a region already named", n)
	}
}

// TestAllObjectsGivesUpOnNamesThatNeverCome: an object can go away
// between being described and being asked about, so insisting on every
// answer would hang on every region -- but giving up at the first quiet
// second gives up before anything has arrived.
func TestAllObjectsGivesUpOnNamesThatNeverCome(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	// Nothing answers, so this is the several-quiet-rounds path.  The
	// timeout is what bounds it, and it is short here.
	f.mu.Lock()
	f.objects = []*Seen{{Object: Object{ID: thePrim, Local: 77}}}
	f.mu.Unlock()

	wait := aside(t, func() ([]*Seen, error) {
		return w.AllObjects(context.Background(), 1500*time.Millisecond)
	})
	got, err := wait()
	if err != nil {
		t.Fatalf("AllObjects: %v", err)
	}
	if len(got) != 1 || got[0].Name != "" {
		t.Errorf("AllObjects = %+v, want the object it has, still nameless", got)
	}
}

// TestAllObjectsReportsWhatDidNotHappen: the region read and the name
// requests fail differently, and an empty list from either reads as an
// empty region.
func TestAllObjectsReportsWhatDidNotHappen(t *testing.T) {
	t.Parallel()

	t.Run("the region could not be read", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.mu.Lock()
		f.objectsErr = errors.New("the cache is gone")
		f.mu.Unlock()
		if _, err := w.AllObjects(context.Background(), time.Second); err == nil {
			t.Error("AllObjects reported an empty region it could not read")
		}
		if _, err := w.ObjectsNamed(context.Background(), "workbench", time.Second); err == nil {
			t.Error("ObjectsNamed reported nothing found in a region it could not read")
		}
		if _, err := w.Known(context.Background()); err == nil {
			t.Error("Known counted a region it could not read")
		}
	})

	t.Run("the name requests never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.mu.Lock()
		f.objects = []*Seen{{Object: Object{ID: thePrim, Local: 77}}}
		f.mu.Unlock()
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.AllObjects(context.Background(), time.Second); err == nil {
			t.Error("AllObjects reported names for requests that never went out")
		}
	})
}

// TestObjectsNamedNamesTheRegionFirst: a name is only known by having
// asked, so looking for one by name costs the whole scan the first time
// and nothing afterwards.
func TestObjectsNamedNamesTheRegionFirst(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	answerFamily(t, f, map[msg.UUID]string{thePrim: "workbench"})
	f.mu.Lock()
	f.objects = []*Seen{
		{Object: Object{ID: thePrim, Local: 77, Name: "workbench"}},
		{Object: Object{ID: theOther, Local: 78, Name: "anvil"}},
	}
	f.mu.Unlock()

	got, err := w.ObjectsNamed(context.Background(), "workbench", 5*time.Second)
	if err != nil {
		t.Fatalf("ObjectsNamed: %v", err)
	}
	if len(got) != 1 || got[0].ID != thePrim {
		t.Errorf("ObjectsNamed = %+v", got)
	}

	n, err := w.Known(context.Background())
	if err != nil || n != 2 {
		t.Errorf("Known = %d, %v", n, err)
	}
}

// TestResolveAsksNothingWhenThereIsNothingToAsk: an empty list is the
// ordinary case on every call after the first, and a round trip for it
// would make a cached answer cost as much as an uncached one.
func TestResolveAsksNothingWhenThereIsNothingToAsk(t *testing.T) {
	w, f := newFakeSession(t)
	if err := w.resolve(context.Background(), nil, time.Second); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("resolving nothing sent %s", f.describe())
	}
}

// TestTheRegionIsWhatTheHandshakeSaid: the simulator says it once,
// before any client is listening, so a session that has not heard it has
// no way to ask -- and answering with an empty region would have a
// caller believing it was somewhere called "".
func TestTheRegionIsWhatTheHandshakeSaid(t *testing.T) {
	w, f := newFakeSession(t)

	r, err := w.Region(context.Background())
	if err != nil {
		t.Fatalf("Region: %v", err)
	}
	if r.Name != "Test Region" || r.ID != testRegionID {
		t.Errorf("Region = %+v", r)
	}

	f.mu.Lock()
	f.regionKnown = false
	f.mu.Unlock()
	if _, err := w.Region(context.Background()); err == nil {
		t.Error("Region described somewhere the handshake has not arrived from")
	}

	f.mu.Lock()
	f.regionErr = errors.New("no session")
	f.mu.Unlock()
	if _, err := w.Region(context.Background()); err == nil {
		t.Error("Region answered from a backend that refused")
	}
}

// TestFlushSaysHowMuchItThrewAway: the count is the only evidence the
// cache held anything, and a second flush answering zero is what says
// the first one worked.
func TestFlushSaysHowMuchItThrewAway(t *testing.T) {
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.objects = []*Seen{
		{Object: Object{ID: thePrim, Local: 77}},
		{Object: Object{ID: theOther, Local: 78}},
	}
	f.mu.Unlock()

	if n, err := w.Flush(context.Background()); err != nil || n != 2 {
		t.Errorf("Flush = %d, %v", n, err)
	}
	if n, err := w.Flush(context.Background()); err != nil || n != 0 {
		t.Errorf("the second Flush = %d, %v", n, err)
	}

	f.mu.Lock()
	f.flushErr = errors.New("no session")
	f.mu.Unlock()
	if _, err := w.Flush(context.Background()); err == nil {
		t.Error("Flush reported emptying a cache it could not reach")
	}
}

// TestAnUnreadableGroupIsNoGroup: the wire carries it as a string, and a
// zero uuid and an absent one mean the same thing here -- so anything
// unreadable has to become none rather than an error a caller must
// handle on every read.
func TestAnUnreadableGroupIsNoGroup(t *testing.T) {
	if got := parseUUIDOrZero(theOther.String()); got != theOther {
		t.Errorf("parseUUIDOrZero = %s", got)
	}
	for _, s := range []string{"", "not a uuid"} {
		if got := parseUUIDOrZero(s); !got.IsZero() {
			t.Errorf("parseUUIDOrZero(%q) = %s, want none", s, got)
		}
	}
}

// TestResolveGivesUpAfterSeveralQuietRoundsAndNotOne: an object can go
// away between being described and being asked about, so insisting on
// every answer would hang on every region -- but the first answer takes
// a moment, and stopping at the first second with nothing new in it
// stops before anything has arrived at all, which looks exactly like a
// region full of nameless objects.
func TestResolveGivesUpAfterSeveralQuietRoundsAndNotOne(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	// One of the two is answered and the other never is, so the quiet
	// rounds are what ends this rather than the deadline: the timeout
	// here is far longer than the wait turns out to be.
	answerFamily(t, f, map[msg.UUID]string{thePrim: "workbench"})
	f.mu.Lock()
	f.objects = []*Seen{
		{Object: Object{ID: thePrim, Local: 77}},
		{Object: Object{ID: theOther, Local: 78}},
	}
	f.mu.Unlock()

	start := time.Now()
	wait := aside(t, func() ([]*Seen, error) {
		return w.AllObjects(context.Background(), 90*time.Second)
	})
	if _, err := wait(); err != nil {
		t.Fatalf("AllObjects: %v", err)
	}
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("waiting for a name that never came took %s", took)
	}
}

// TestResolveStopsWhenTheCallerDoes: naming a region is the expensive
// half of a scan, and a caller that has changed its mind must not have
// to wait out a minute and a half of somebody else's timeout.
func TestResolveStopsWhenTheCallerDoes(t *testing.T) {
	t.Parallel()

	t.Run("while it waits for answers", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.mu.Lock()
		f.objects = []*Seen{{Object: Object{ID: thePrim, Local: 77}}}
		f.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		wait := aside(t, func() ([]*Seen, error) {
			return w.AllObjects(ctx, 90*time.Second)
		})
		if _, err := wait(); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("AllObjects = %v, want the context's reason", err)
		}
	})

	t.Run("between batches", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		// More than one batch, so there is a pause between instalments
		// for the caller to give up in.
		var objs []*Seen
		for i := range resolveBatch + 5 {
			objs = append(objs, &Seen{
				Object: Object{ID: msg.UUID{byte(i + 1), 0xbb}, Local: uint32(i + 1)},
			})
		}
		f.mu.Lock()
		f.objects = objs
		f.mu.Unlock()

		ctx, cancel := context.WithCancel(context.Background())
		// Cancelled the moment the first batch has gone out, which is
		// while the pause between them is running.
		f.mu.Lock()
		f.onSend = func(m msg.Message) {
			if len(sentOf[*msg.RequestObjectPropertiesFamily](f)) >= resolveBatch {
				cancel()
			}
		}
		f.mu.Unlock()
		defer cancel()

		wait := aside(t, func() ([]*Seen, error) {
			return w.AllObjects(ctx, 90*time.Second)
		})
		if _, err := wait(); !errors.Is(err, context.Canceled) {
			t.Errorf("AllObjects = %v, want the context's reason", err)
		}
	})
}

// TestTheClickActionsAreLindensPublishedValues: the names stand for the
// numbers in the published LSL list, in its order.
func TestTheClickActionsAreLindensPublishedValues(t *testing.T) {
	for i, c := range []uint8{ClickTouch, ClickSit, ClickBuy, ClickPay, ClickOpen,
		ClickPlay, ClickOpenMedia, ClickZoom, ClickDisabled, ClickIgnore} {
		if int(c) != i {
			t.Errorf("click action %d is %d", i, c)
		}
	}
}
