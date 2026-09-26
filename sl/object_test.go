package sl

// What the session knows about an object, and how it finds out.
//
// Almost nothing here is answered by a reply to the thing that asked.
// Renaming an object has no reply, so the name is asked for again and
// read back; a link is confirmed by the children mentioning the root as
// their parent, in an update that would have arrived anyway; a take is
// confirmed by an item appearing in a folder over a different protocol
// some seconds later.  Each of those is a loop with a deadline, and
// each of them can be made to hang, to give up, or to declare success
// on a stale answer -- which is what the bookkeeping in object.go is
// for, and what these tests hold it to.
//
// The calls that wait cannot be the goroutine that relays what they are
// waiting for, so they are run aside; see fake_test.go.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	thePrim  = msg.MustParseUUID("89ad7e57-7e57-c0de-08a1-04b25f97cc85")
	theChild = msg.MustParseUUID("909e7e57-7e57-c0de-177e-107fc4869811")
	theOther = msg.MustParseUUID("97c27e57-7e57-c0de-c041-be2c2f8cb586")
)

// anUpdate is one ObjectUpdate, which is how the region says an object
// exists at all: an id, a local id, and no name.
func anUpdate(d msg.ObjectUpdate_ObjectData) *msg.ObjectUpdate {
	return &msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{d}}
}

// familyReply answers RequestObjectPropertiesFamily, which is the only
// thing that ever says what an object is called.
func familyReply(id, owner msg.UUID, name string) *msg.ObjectPropertiesFamily {
	m := &msg.ObjectPropertiesFamily{}
	m.ObjectData.ObjectID = id
	m.ObjectData.OwnerID = owner
	m.ObjectData.Name = append([]byte(name), 0)
	return m
}

// TestAnObjectNamesBothOfItsIdentifiers: a message quoting an object
// has to say which one it means, since the local id is the one most of
// the older messages take and it is meaningless in another region.
func TestAnObjectNamesBothOfItsIdentifiers(t *testing.T) {
	o := Object{ID: thePrim, Local: 77}
	if got := o.String(); !strings.Contains(got, thePrim.String()) || !strings.Contains(got, "77") {
		t.Errorf("String = %q", got)
	}
	o.Name = "workbench"
	got := o.String()
	if !strings.Contains(got, `"workbench"`) || !strings.Contains(got, "77") {
		t.Errorf("String = %q", got)
	}
}

// TestRezMakesOnePrimAndConfirmsWeOwnIt: a prim that is new to the
// region is not necessarily one we just made -- objects stream in the
// whole time -- so the rez is not finished until something says the new
// prim is ours.
func TestRezMakesOnePrimAndConfirmsWeOwnIt(t *testing.T) {
	w, f := newFakeSession(t)

	at := msg.Vector3{X: 129, Y: 128, Z: 25}
	wait := aside(t, func() (*Object, error) {
		return w.Rez(context.Background(), RezOptions{At: at})
	})

	add := waitSent[*msg.ObjectAdd](t, f)
	if add.ObjectData.RayStart != at || add.ObjectData.RayEnd != at {
		t.Errorf("rezzed at %+v, want %+v", add.ObjectData.RayStart, at)
	}
	// No scale asked for means half a metre cubed, not a prim of no
	// size at all.
	if got := add.ObjectData.Scale; got != (msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}) {
		t.Errorf("scale = %+v", got)
	}

	// The region describes something new where the rez lands, which is
	// not yet known to be ours, so the session asks whose it is.
	appear(t, f, &Seen{Object: Object{ID: thePrim, Local: 77}, Position: landing(add)})
	q := waitSent[*msg.RequestObjectPropertiesFamily](t, f)
	if q.ObjectData.ObjectID != thePrim {
		t.Errorf("asked about %s, want the new prim", q.ObjectData.ObjectID)
	}
	f.Relay(t, familyReply(thePrim, testAgentID, "Object"))

	o, err := wait()
	if err != nil {
		t.Fatalf("Rez: %v", err)
	}
	if o.ID != thePrim || o.Local != 77 {
		t.Errorf("rezzed %s", o)
	}
}

// TestRezRefusesWhatItCouldNotConfirm: rezzing beyond the draw distance
// is not refused by the simulator.  The prim is made, nothing is ever
// said about it, and the caller waits fifteen seconds to be told
// nothing happened -- so this refuses at once and says why.
func TestRezRefusesWhatItCouldNotConfirm(t *testing.T) {
	t.Run("out where nothing would be described", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.presence.DrawDistance = 64

		_, err := w.Rez(context.Background(), RezOptions{
			At:    msg.Vector3{X: 1000, Y: 1000, Z: 25},
			Scale: msg.Vector3{X: 1, Y: 1, Z: 1},
		})
		if !errors.Is(err, ErrOutOfRange) {
			t.Errorf("Rez out of range = %v", err)
		}
		if got := f.Sent(); len(got) != 0 {
			t.Errorf("a prim nobody would describe was rezzed anyway: %s", f.describe())
		}
	})

	t.Run("nothing knows where we are", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.presenceErr = errors.New("no presence")
		if _, err := w.Rez(context.Background(), RezOptions{}); err == nil {
			t.Error("Rez went ahead without knowing whether it was in range")
		}
	})

	t.Run("the request never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.Rez(context.Background(), RezOptions{}); err == nil {
			t.Error("Rez reported a prim though nothing was sent")
		}
	})
}

// TestSetNameReadsTheNameBack: ObjectName has no reply, so the only way
// to know a rename took is to ask what the object is called now.  The
// name we think it has is dropped first, or the answer read back is the
// one from before the rename -- which is why renaming a fresh prim used
// to report that it was still called "Object".
func TestSetNameReadsTheNameBack(t *testing.T) {
	w, f := newFakeSession(t)
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	// What it was called before, which must not be read back as the
	// new name.
	f.Relay(t, familyReply(thePrim, testAgentID, "Object"))

	wait := asideErr(t, func() error { return w.SetName(context.Background(), o, "workbench") })

	nm := waitSent[*msg.ObjectName](t, f)
	if len(nm.ObjectData) != 1 || nm.ObjectData[0].LocalID != 77 {
		t.Errorf("renamed %+v", nm.ObjectData)
	}
	if got := trimNul(nm.ObjectData[0].Name); got != "workbench" {
		t.Errorf("renamed to %q", got)
	}
	waitSent[*msg.RequestObjectPropertiesFamily](t, f)
	f.Relay(t, familyReply(thePrim, testAgentID, "workbench"))

	if err := wait(); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if o.Name != "workbench" {
		t.Errorf("the object is called %q", o.Name)
	}
}

// TestSetNameSaysWhenTheNameDidNotTake: a simulator that refuses a
// rename says nothing about it, and the object keeps the name it had.
// Reporting success there would leave every later lookup by name
// looking for something that does not exist.
func TestSetNameSaysWhenTheNameDidNotTake(t *testing.T) {
	w, f := newFakeSession(t)
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	wait := asideErr(t, func() error { return w.SetName(context.Background(), o, "workbench") })
	waitSent[*msg.RequestObjectPropertiesFamily](t, f)
	f.Relay(t, familyReply(thePrim, testAgentID, "Object"))

	err := wait()
	if err == nil || !strings.Contains(err.Error(), "Object") {
		t.Fatalf("SetName = %v, want it to say what the object is called now", err)
	}
	if o.Name == "workbench" {
		t.Error("the object was recorded under a name it never took")
	}
}

// TestSetNameNeedsBothMessagesToGoOut: the rename and the question that
// confirms it are separate messages, and a rename that reported success
// because the confirming question was never sent would be worse than
// one that failed.
func TestSetNameNeedsBothMessagesToGoOut(t *testing.T) {
	t.Run("the rename never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		err := w.SetName(context.Background(), foundHere(w, &Object{ID: thePrim, Local: 77}), "workbench")
		if err == nil {
			t.Error("SetName reported success though nothing was sent")
		}
	})

	t.Run("the question that confirms it never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		// Everything after the rename itself fails, which is the case
		// where the circuit dies mid call.
		f.mu.Lock()
		f.onSend = func(m msg.Message) {
			if _, ok := m.(*msg.ObjectName); ok {
				f.FailSends(errors.New("the circuit is gone"))
			}
		}
		f.mu.Unlock()

		err := w.SetName(context.Background(), foundHere(w, &Object{ID: thePrim, Local: 77}), "workbench")
		if err == nil {
			t.Error("SetName reported success though it never asked what the name was")
		}
	})
}

// TestSetNameStopsWhenTheCallerGivesUp: a caller that has cancelled its
// context wants the call to end, and SetName is a twenty second loop --
// the longest wait in this file -- so it is the one call where being
// held past a cancellation is most obvious.
//
// It used not to stop.  The context reached await, which honours it,
// but the error await came back with was discarded: the loop saw only
// that the name had not arrived and went round again.  With a
// cancelled context await returns at once, so the loop spun flat out
// for the full twenty seconds, asking the simulator every time round,
// and then reported a timeout rather than the cancellation.
func TestSetNameStopsWhenTheCallerGivesUp(t *testing.T) {
	w, _ := newFakeSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := w.SetName(ctx, foundHere(w, &Object{ID: thePrim, Local: 77}), "workbench")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("SetName = %v, want the context's reason", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("SetName took %s to notice the caller had gone", waited)
	}
}

// propertiesOf is the full ObjectProperties reply, which is where the
// permission masks live.
func propertiesOf(ids ...msg.UUID) *msg.ObjectProperties {
	m := &msg.ObjectProperties{}
	for _, id := range ids {
		m.ObjectData = append(m.ObjectData, msg.ObjectProperties_ObjectData{
			ObjectID:        id,
			CreatorID:       testAgentID,
			OwnerID:         testAgentID,
			GroupID:         msg.UUID{2},
			CreationDate:    1_600_000_000_000_000, // microseconds
			BaseMask:        0x7fffffff,
			OwnerMask:       0x7ffffff0,
			GroupMask:       0x00040000,
			EveryoneMask:    0x00020000,
			NextOwnerMask:   0x0008e000,
			SaleType:        1,
			SalePrice:       10,
			InventorySerial: 3,
			Name:            append([]byte("workbench"), 0),
			Description:     append([]byte("a thing"), 0),
		})
	}
	return m
}

// TestPropertiesSelectsAndWaits: the masks are only sent for a selected
// object, so asking means selecting, and the reply is not addressed to
// the request -- it has to be picked out by which object it is about.
func TestPropertiesSelectsAndWaits(t *testing.T) {
	w, f := newFakeSession(t)
	o := foundHere(w, &Object{ID: thePrim, Local: 77})

	// A timeout of zero asks for the default, which is not reached
	// here.
	wait := aside(t, func() (*Properties, error) {
		return w.Properties(context.Background(), o, 0)
	})

	sel := waitSent[*msg.ObjectSelect](t, f)
	if len(sel.ObjectData) != 1 || sel.ObjectData[0].ObjectLocalID != 77 {
		t.Errorf("selected %+v", sel.ObjectData)
	}

	// Somebody else's properties first, so that picking the reply out
	// by object is what has to happen; then two for ours, which is the
	// answer arriving twice before it has been read.
	f.Relay(t, propertiesOf(theOther, thePrim, thePrim))

	p, err := wait()
	if err != nil {
		t.Fatalf("Properties: %v", err)
	}
	if p.Object != thePrim || p.Name != "workbench" || p.Description != "a thing" {
		t.Errorf("properties = %+v", p)
	}
	if p.Owner != testAgentID || p.Creator != testAgentID {
		t.Errorf("owned by %s, made by %s", p.Owner, p.Creator)
	}
	// CreationDate is microseconds.  Read as seconds it dates a fresh
	// prim to the year 56 million.
	if got := p.Created.Year(); got != 2020 {
		t.Errorf("created in %d, want 2020", got)
	}
	if p.NextOwnerMask != 0x0008e000 || p.BaseMask != 0x7fffffff {
		t.Errorf("masks = %+v", p)
	}
	if p.SaleType != 1 || p.SalePrice != 10 || p.InventorySerial != 3 {
		t.Errorf("sale = %+v", p)
	}
}

// TestPropertiesGivesUpRatherThanHangs: an object that will not answer
// is the ordinary case for anything worn, so the wait has to end -- and
// end saying it timed out rather than that the object has no
// permissions.
func TestPropertiesGivesUpRatherThanHangs(t *testing.T) {
	oAt := func(w *Session) *Object { return foundHere(w, &Object{ID: thePrim, Local: 77}) }

	t.Run("nothing answered", func(t *testing.T) {
		w, _ := newFakeSession(t)
		_, err := w.Properties(context.Background(), oAt(w), 50*time.Millisecond)
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("Properties = %v, want a timeout", err)
		}
	})

	t.Run("the caller gave up", func(t *testing.T) {
		w, _ := newFakeSession(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := w.Properties(ctx, oAt(w), 30*time.Second); !errors.Is(err, context.Canceled) {
			t.Errorf("Properties = %v, want the context's reason", err)
		}
	})

	t.Run("the selection never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.Properties(context.Background(), oAt(w), 30*time.Second); err == nil {
			t.Error("Properties waited for a reply to a selection that was not sent")
		}
	})
}

// TestSelectNamesEveryObjectAtOnce: some operations act on the current
// selection rather than on what they name, so a selection that dropped
// one of them would link or take the wrong things.
func TestSelectNamesEveryObjectAtOnce(t *testing.T) {
	w, f := newFakeSession(t)
	err := w.Select(context.Background(),
		foundHere(w, &Object{ID: thePrim, Local: 1}), foundHere(w, &Object{ID: theChild, Local: 2}))
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	m := onlySent[*msg.ObjectSelect](t, f)
	if len(m.ObjectData) != 2 ||
		m.ObjectData[0].ObjectLocalID != 1 || m.ObjectData[1].ObjectLocalID != 2 {
		t.Errorf("selected %+v", m.ObjectData)
	}
	if m.AgentData.AgentID != testAgentID || m.AgentData.SessionID != testSessionID {
		t.Errorf("the selection came from %+v", m.AgentData)
	}
}

// TestLinkWaitsForTheChildrenToNameTheRoot: nothing replies to a link.
// It shows up as the children saying who their parent is in an update
// that would have arrived anyway, so that is what is waited for -- and
// the selection has to be in first, since the link acts on it.
func TestLinkWaitsForTheChildrenToNameTheRoot(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	root := foundHere(w, &Object{ID: thePrim, Local: 1})
	child := foundHere(w, &Object{ID: theChild, Local: 2})
	wait := asideErr(t, func() error { return w.Link(context.Background(), root, child) })

	sel := waitSent[*msg.ObjectSelect](t, f)
	if len(sel.ObjectData) != 2 {
		t.Errorf("linking selected %+v, want the root and the child", sel.ObjectData)
	}
	lnk := waitSent[*msg.ObjectLink](t, f)
	if len(lnk.ObjectData) != 2 || lnk.ObjectData[0].ObjectLocalID != 1 {
		t.Errorf("linked %+v, want the root first", lnk.ObjectData)
	}

	// Nothing has said the child is under the root, so nothing is
	// finished yet.
	if _, ok := w.Parent(child); ok {
		t.Error("the child had a parent before anything said so")
	}
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: theChild, ID: 2, ParentID: 1}))

	if err := wait(); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if p, ok := w.Parent(child); !ok || p != 1 {
		t.Errorf("Parent = %d, %v", p, ok)
	}
}

// TestLinkRefusesWhatItCannotDo: a link that reported success without
// the simulator having agreed would leave a build that looks linked
// here and is not linked there.
func TestLinkRefusesWhatItCannotDo(t *testing.T) {
	rootAt := func(w *Session) *Object { return foundHere(w, &Object{ID: thePrim, Local: 1}) }
	childAt := func(w *Session) *Object { return foundHere(w, &Object{ID: theChild, Local: 2}) }

	t.Run("nothing to link", func(t *testing.T) {
		w, f := newFakeSession(t)
		if err := w.Link(context.Background(), rootAt(w)); err == nil {
			t.Error("Link joined an object to nothing")
		}
		if got := f.Sent(); len(got) != 0 {
			t.Errorf("a link with nothing to link sent %s", f.describe())
		}
	})

	t.Run("the selection never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if err := w.Link(context.Background(), rootAt(w), childAt(w)); err == nil {
			t.Error("Link went ahead without a selection")
		}
	})

	t.Run("the caller gave up while the selection settled", func(t *testing.T) {
		w, f := newFakeSession(t)
		ctx, cancel := context.WithCancel(context.Background())
		wait := asideErr(t, func() error { return w.Link(ctx, rootAt(w), childAt(w)) })
		waitSent[*msg.ObjectSelect](t, f)
		cancel()
		if err := wait(); !errors.Is(err, context.Canceled) {
			t.Errorf("Link = %v, want the context's reason", err)
		}
		if got := sentOf[*msg.ObjectLink](f); len(got) != 0 {
			t.Error("the link went out after the caller gave up")
		}
	})

	t.Run("the link never went", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.mu.Lock()
		f.onSend = func(m msg.Message) {
			if _, ok := m.(*msg.ObjectSelect); ok {
				f.FailSends(errors.New("the circuit is gone"))
			}
		}
		f.mu.Unlock()

		if err := w.Link(context.Background(), rootAt(w), childAt(w)); err == nil {
			t.Error("Link waited for a link that was not sent")
		}
	})
}

// TestUnlinkSendsThePrimsBeingFreedAndWaitsForThemToSaySo.
//
// A delink names the prims that are LEAVING rather than the root they
// are leaving, which is the opposite of a link and is what lets one
// prim out of a set.  Nothing replies to it, so the confirmation is the
// freed prims saying they have no parent -- and "no parent" has to mean
// something the region said, not the absence of anything said at all: a
// map of parents answers zero for a prim it has never heard of, which
// would pass for success before the request had gone anywhere.
func TestUnlinkSendsThePrimsBeingFreedAndWaitsForThemToSaySo(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	child := foundHere(w, &Object{ID: theChild, Local: 2})
	other := foundHere(w, &Object{ID: theOther, Local: 3})

	// Not asideErr: what this has to watch is the call NOT returning,
	// and a wait for the answer cannot say that.
	done := make(chan error, 1)
	go func() { done <- w.Unlink(context.Background(), child, other) }()

	sel := waitSent[*msg.ObjectSelect](t, f)
	if len(sel.ObjectData) != 2 {
		t.Errorf("unlinking selected %+v, want both prims", sel.ObjectData)
	}
	del := waitSent[*msg.ObjectDelink](t, f)
	if len(del.ObjectData) != 2 ||
		del.ObjectData[0].ObjectLocalID != 2 || del.ObjectData[1].ObjectLocalID != 3 {
		t.Errorf("delinked %+v, want the prims being freed", del.ObjectData)
	}
	if del.AgentData.AgentID != testAgentID || del.AgentData.SessionID != testSessionID {
		t.Errorf("the delink came from %+v", del.AgentData)
	}

	select {
	case err := <-done:
		t.Fatalf("Unlink finished before the region said anything at all: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	// One of the two is not both of them.
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: theChild, ID: 2, ParentID: 0}))
	select {
	case err := <-done:
		t.Fatalf("Unlink finished with the second prim still linked: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: theOther, ID: 3, ParentID: 0}))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Unlink: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Unlink never finished, though both prims had said they were loose")
	}
	if p, ok := w.Parent(child); !ok || p != 0 {
		t.Errorf("Parent = %d, %v", p, ok)
	}
}

// TestUnlinkRefusesWhatItCannotDo: an unlink that reported success
// without the simulator having agreed would have somebody take a
// linkset apart, be told it came apart, and find it whole.
func TestUnlinkRefusesWhatItCannotDo(t *testing.T) {
	childAt := func(w *Session) *Object { return foundHere(w, &Object{ID: theChild, Local: 2}) }

	t.Run("nothing to take apart", func(t *testing.T) {
		w, f := newFakeSession(t)
		if err := w.Unlink(context.Background()); err == nil {
			t.Error("Unlink took nothing apart and reported it")
		}
		if got := f.Sent(); len(got) != 0 {
			t.Errorf("a delink with nothing to free sent %s", f.describe())
		}
	})

	t.Run("the selection never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if err := w.Unlink(context.Background(), childAt(w)); err == nil {
			t.Error("Unlink went ahead without a selection")
		}
		if got := sentOf[*msg.ObjectDelink](f); len(got) != 0 {
			t.Error("the delink went out after the selection failed")
		}
	})

	t.Run("the caller gave up waiting", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		ctx, cancel := context.WithCancel(context.Background())
		wait := asideErr(t, func() error { return w.Unlink(ctx, childAt(w)) })
		waitSent[*msg.ObjectDelink](t, f)
		cancel()
		if err := wait(); !errors.Is(err, context.Canceled) {
			t.Errorf("Unlink = %v, want the context's reason", err)
		}
	})
}

// aFolder is where things are taken to.
var aFolder = msg.MustParseUUID("41857e57-7e57-c0de-7570-3c776df7b8ca")

// anItem is one thing in it, as AIS describes it.
func anItem(id msg.UUID, name string) *Item {
	return &Item{ID: id, ParentID: aFolder, Name: name, Type: 6, InvType: 6}
}

// TestTakeWaitsForTheItemToTurnUpInInventory: nothing answers a take
// over UDP.  The item appears in the folder over AIS some seconds
// later, so the only confirmation is something in the folder that was
// not there before.
func TestTakeWaitsForTheItemToTurnUpInInventory(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	// The folder is not empty to begin with, which is the case that
	// matters: an item that was already there is not the one we took.
	old := anItem(theOther, "workbench")
	taken := anItem(theChild, "workbench")

	var mu sync.Mutex
	arrived := false
	f.ServeInventory(t, func(folder msg.UUID) []*Item {
		mu.Lock()
		defer mu.Unlock()
		if !arrived {
			return []*Item{old}
		}
		return []*Item{old, taken}
	})

	o := foundHere(w, &Object{ID: thePrim, Local: 77, Name: "workbench"})
	wait := aside(t, func() (*Item, error) {
		return w.Take(context.Background(), o, aFolder, 0)
	})

	d := waitSent[*msg.DeRezObject](t, f)
	if d.AgentBlock.Destination != derezTakeIntoInventory {
		t.Errorf("took it to destination %d", d.AgentBlock.Destination)
	}
	if d.AgentBlock.DestinationID != aFolder {
		t.Errorf("took it into %s", d.AgentBlock.DestinationID)
	}
	if len(d.ObjectData) != 1 || d.ObjectData[0].ObjectLocalID != 77 {
		t.Errorf("took %+v", d.ObjectData)
	}
	if d.AgentBlock.PacketCount != 1 || d.AgentBlock.TransactionID.IsZero() {
		t.Errorf("agent block = %+v", d.AgentBlock)
	}
	// It has to have been selected first: a derez acts on the
	// selection.
	if got := sentOf[*msg.ObjectSelect](f); len(got) != 1 {
		t.Errorf("%d selections went out before the take", len(got))
	}

	mu.Lock()
	arrived = true
	mu.Unlock()

	it, err := wait()
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if it.ID != theChild {
		t.Errorf("took %s, want the item that was not there before", it.ID)
	}
}

// TestTakeGivesUpWhenNothingArrives: the folder read can fail on its
// own -- it is a different protocol and a different server -- and a
// take that never lands must end saying so rather than looping for
// ever.
func TestTakeGivesUpWhenNothingArrives(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.ServeInventory(t, func(msg.UUID) []*Item { return nil })

	o := foundHere(w, &Object{ID: thePrim, Local: 77, Name: "workbench"})
	wait := aside(t, func() (*Item, error) {
		return w.Take(context.Background(), o, aFolder, 3*time.Second)
	})
	waitSent[*msg.DeRezObject](t, f)

	// A folder that cannot be read is not an empty folder, so the take
	// keeps looking rather than reporting a failure it might recover
	// from.
	f.mu.Lock()
	f.capErr = errors.New("the inventory server is not answering")
	f.mu.Unlock()

	if _, err := wait(); !errors.Is(err, ErrTimeout) {
		t.Errorf("Take = %v, want a timeout", err)
	}
}

// TestTakeWithNoNameAsksWhatTheObjectIsCalled: every fresh prim is
// called "Object", so taking the first new item in the folder takes
// whatever else turned up there first.  The object is asked its name,
// and the item is the new one of that name.
func TestTakeWithNoNameAsksWhatTheObjectIsCalled(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	old := anItem(theOther, "workbench")
	unrelated := anItem(ourElsewhere, "Object")
	taken := anItem(theChild, "probe")

	var mu sync.Mutex
	stage, reads := 0, 0
	f.ServeInventory(t, func(folder msg.UUID) []*Item {
		mu.Lock()
		defer mu.Unlock()
		reads++
		switch stage {
		case 0:
			return []*Item{old}
		case 1:
			// Something else new of ours, landing first.
			return []*Item{old, unrelated}
		}
		return []*Item{old, unrelated, taken}
	})

	o := foundHere(w, &Object{ID: thePrim, Local: 77})
	wait := aside(t, func() (*Item, error) {
		return w.Take(context.Background(), o, aFolder, 0)
	})

	// The selection a take makes is answered with the object's
	// properties, which is where its name comes from.
	waitSent[*msg.ObjectSelect](t, f)
	props := propertiesOf(thePrim)
	props.ObjectData[0].Name = []byte("probe\x00")
	f.Relay(t, props)

	waitSent[*msg.DeRezObject](t, f)
	mu.Lock()
	stage, reads = 1, 0
	mu.Unlock()
	waitFor(t, "the folder to be read with only the unrelated item new", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return reads > 0
	})
	mu.Lock()
	stage = 2
	mu.Unlock()

	it, err := wait()
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if it.ID != theChild {
		t.Errorf("took %s %q, want the item named for the object", it.ID, it.Name)
	}
	if got := sentOf[*msg.ObjectSelect](f); len(got) != 1 {
		t.Errorf("%d selections went out, want the one the take needs", len(got))
	}
}

// TestTakeWithNoNameRefusesWhenTheNameCannotBeLearned: without the name
// there is nothing to know the item by, and a take that cannot tell its
// item from another is not made at all.
func TestTakeWithNoNameRefusesWhenTheNameCannotBeLearned(t *testing.T) {
	t.Parallel()

	t.Run("a take", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return nil })

		_, err := w.Take(context.Background(), foundHere(w, &Object{ID: thePrim, Local: 77}), aFolder, time.Second)
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("Take = %v, want the name's timeout", err)
		}
		if got := sentOf[*msg.DeRezObject](f); len(got) != 0 {
			t.Errorf("an object was taken with no name to know its item by: %s", f.describe())
		}
	})

	// A copy that times out on an object still standing is reported as
	// refused for its permissions.  One that never went out was not
	// refused by anybody.
	t.Run("a copy", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return nil })
		f.objects = []*Seen{{Object: Object{ID: thePrim, Local: 77, Name: "probe"}, PCode: pcodePrim}}

		_, err := w.TakeCopy(context.Background(), foundHere(w, &Object{ID: thePrim, Local: 77}), aFolder, time.Second)
		if err == nil || strings.Contains(err.Error(), "not copied") {
			t.Errorf("TakeCopy = %v, want it to say the name could not be learned", err)
		}
		if got := sentOf[*msg.DeRezObject](f); len(got) != 0 {
			t.Errorf("an object was copied with no name to know its item by: %s", f.describe())
		}
	})
}

// TestTakeReportsWhatDidNotHappen: a take is several seconds of waiting
// either side of the derez, and every step of it can fail on its own --
// a caller that has given up must not be held through both waits, and a
// message that never went must not be waited on.
func TestTakeReportsWhatDidNotHappen(t *testing.T) {
	t.Parallel()

	t.Run("the derez never went", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return nil })
		f.mu.Lock()
		f.onSend = func(m msg.Message) {
			if _, ok := m.(*msg.ObjectSelect); ok {
				f.FailSends(errors.New("the circuit is gone"))
			}
		}
		f.mu.Unlock()

		_, err := w.Take(context.Background(), foundHere(w, &Object{Local: 77, Name: "workbench"}), aFolder, 30*time.Second)
		if err == nil {
			t.Error("Take waited for an item to arrive from a derez that was not sent")
		}
	})

	t.Run("the folder cannot be read", func(t *testing.T) {
		t.Parallel()
		w, _ := newFakeSession(t)
		// No inventory capability at all, which is what a session
		// attached without one looks like.
		_, err := w.Take(context.Background(), foundHere(w, &Object{ID: thePrim, Local: 77}), aFolder, time.Second)
		if err == nil {
			t.Error("Take read a folder that is not there")
		}
	})

	t.Run("the selection never went", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return nil })
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.Take(context.Background(), foundHere(w, &Object{Local: 77, Name: "workbench"}), aFolder, time.Second); err == nil {
			t.Error("Take went ahead without a selection")
		}
	})

	t.Run("gave up before the derez", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return nil })
		ctx, cancel := context.WithCancel(context.Background())
		wait := aside(t, func() (*Item, error) {
			return w.Take(ctx, foundHere(w, &Object{Local: 77, Name: "workbench"}), aFolder, 30*time.Second)
		})
		waitSent[*msg.ObjectSelect](t, f)
		cancel()
		if _, err := wait(); !errors.Is(err, context.Canceled) {
			t.Errorf("Take = %v, want the context's reason", err)
		}
	})

	t.Run("gave up after the derez", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return nil })
		ctx, cancel := context.WithCancel(context.Background())
		wait := aside(t, func() (*Item, error) {
			return w.Take(ctx, foundHere(w, &Object{Local: 77, Name: "workbench"}), aFolder, 30*time.Second)
		})
		waitSent[*msg.DeRezObject](t, f)
		cancel()
		if _, err := wait(); !errors.Is(err, context.Canceled) {
			t.Errorf("Take = %v, want the context's reason", err)
		}
	})
}

// TestDeleteSendsItToTheTrash: deleting is a derez to a different
// destination, and the selection has to settle first for the same
// reason a link does.
func TestDeleteSendsItToTheTrash(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	trash := msg.MustParseUUID("1ad37e57-7e57-c0de-4b44-9217348fe328")
	o := foundHere(w, &Object{ID: thePrim, Local: 77})
	if err := w.Delete(context.Background(), o, trash); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	d := onlySent[*msg.DeRezObject](t, f)
	if d.AgentBlock.Destination != derezTrash || d.AgentBlock.DestinationID != trash {
		t.Errorf("deleted to %d %s", d.AgentBlock.Destination, d.AgentBlock.DestinationID)
	}
	if len(d.ObjectData) != 1 || d.ObjectData[0].ObjectLocalID != 77 {
		t.Errorf("deleted %+v", d.ObjectData)
	}
	if len(sentOf[*msg.ObjectSelect](f)) != 1 {
		t.Error("nothing was selected before the delete")
	}
}

// TestDeleteReportsWhatDidNotHappen: the same three ways as a take, and
// the same reason -- a delete reported as done that was not leaves the
// caller believing the region is tidier than it is.
func TestDeleteReportsWhatDidNotHappen(t *testing.T) {
	oAt := func(w *Session) *Object { return foundHere(w, &Object{ID: thePrim, Local: 77}) }

	t.Run("the selection never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if err := w.Delete(context.Background(), oAt(w), msg.UUID{1}); err == nil {
			t.Error("Delete reported success though nothing was sent")
		}
	})

	t.Run("the caller gave up while the selection settled", func(t *testing.T) {
		w, f := newFakeSession(t)
		ctx, cancel := context.WithCancel(context.Background())
		wait := asideErr(t, func() error { return w.Delete(ctx, oAt(w), msg.UUID{1}) })
		waitSent[*msg.ObjectSelect](t, f)
		cancel()
		if err := wait(); !errors.Is(err, context.Canceled) {
			t.Errorf("Delete = %v, want the context's reason", err)
		}
	})

	t.Run("the derez never went", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.mu.Lock()
		f.onSend = func(m msg.Message) {
			if _, ok := m.(*msg.ObjectSelect); ok {
				f.FailSends(errors.New("the circuit is gone"))
			}
		}
		f.mu.Unlock()
		if err := w.Delete(context.Background(), oAt(w), msg.UUID{1}); err == nil {
			t.Error("Delete reported success though the derez was not sent")
		}
	})
}

// TestATransactionIdIsNeverTheSameTwice: a derez quotes one, and two
// takes sharing an id is a take the simulator may treat as a repeat of
// the first.
func TestATransactionIdIsNeverTheSameTwice(t *testing.T) {
	a, b := randomUUID(), randomUUID()
	if a == b || a.IsZero() {
		t.Errorf("two transaction ids came back %s and %s", a, b)
	}

	// The fallback is reached only if crypto/rand fails, which it does
	// not in practice; what matters is that it fills something in
	// rather than leaving the zero uuid, which means "nothing" to the
	// simulator.
	var u msg.UUID
	binaryFallback(&u)
	if u.IsZero() {
		t.Error("the fallback left the transaction id zero")
	}
}
