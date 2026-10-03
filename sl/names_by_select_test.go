package sl

// Naming what the family request will not name.
//
// RequestObjectPropertiesFamily is answered for a root prim and not for
// a child of a linkset.  Measured against a live region: a child prim
// stayed nameless through a whole resolve, and one ObjectSelect named
// it at once.  That was the difference between what "objects" could
// show and what "dump" could -- dump selects.

import (
	"context"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// answerSelectOnly names objects when they are selected, and never
// answers a family request -- which is how a simulator treats a child
// prim.
func answerSelectOnly(t *testing.T, f *fakeBackend, names map[uint32]string, byLocal map[uint32]msg.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		s, ok := m.(*msg.ObjectSelect)
		if !ok {
			return
		}
		for _, d := range s.ObjectData {
			if _, known := names[d.ObjectLocalID]; !known {
				continue
			}
			p := propertiesOf(byLocal[d.ObjectLocalID])
			p.ObjectData[0].Name = append([]byte(names[d.ObjectLocalID]), 0)
			f.Relay(t, p)
		}
	}
}

// TestNamesComeFromASelectWhenTheFamilyRequestIsNotAnswered.
func TestNamesComeFromASelectWhenTheFamilyRequestIsNotAnswered(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	child := msg.MustParseUUID("d7987e57-7e57-c0de-07cd-7b1f6da667d0")
	f.mu.Lock()
	f.objects = []*Seen{{Object: Object{ID: child, Local: 288331}, Parent: 288335}}
	f.mu.Unlock()
	answerSelectOnly(t, f,
		map[uint32]string{288331: "HearthEmbers"},
		map[uint32]msg.UUID{288331: child})

	wait := aside(t, func() ([]*Seen, error) {
		return w.AllObjects(context.Background(), 8*time.Second)
	})
	if _, err := wait(); err != nil {
		t.Fatalf("AllObjects: %v", err)
	}

	if n := len(sentOf[*msg.ObjectSelect](f)); n == 0 {
		t.Fatal("nothing was selected, so a child prim stays nameless")
	}
	// And the selection is given back: the simulator believes a
	// selected object is being edited, and one avatar's selection is
	// another's object that will not move.
	if n := len(sentOf[*msg.ObjectDeselect](f)); n == 0 {
		t.Error("the objects were left selected")
	}
}

// TestSelectingForNamesLeavesAvatarsAlone: an avatar is named by a name
// lookup rather than by asking what the object is called, and selecting
// one is a strange thing to do to somebody.
func TestSelectingForNamesLeavesAvatarsAlone(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	somebody := msg.MustParseUUID("36d97e57-7e57-c0de-513d-7625ec032f53")
	f.mu.Lock()
	f.objects = []*Seen{{Object: Object{ID: somebody, Local: 42}, PCode: pcodeAvatar}}
	f.mu.Unlock()

	wait := aside(t, func() ([]*Seen, error) {
		return w.AllObjects(context.Background(), 3*time.Second)
	})
	if _, err := wait(); err != nil {
		t.Fatalf("AllObjects: %v", err)
	}
	for _, s := range sentOf[*msg.ObjectSelect](f) {
		for _, d := range s.ObjectData {
			if d.ObjectLocalID == 42 {
				t.Error("an avatar was selected to ask what it is called")
			}
		}
	}
}

// A simulator that answers the family request only for a root in the
// world and names whatever is selected.  Why: doc/objects.md#naming-an-object
func answerAsASimulatorDoes(t *testing.T, f *fakeBackend, names map[msg.UUID]string, local map[uint32]msg.UUID, roots map[msg.UUID]bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		switch m := m.(type) {
		case *msg.RequestObjectPropertiesFamily:
			if id := m.ObjectData.ObjectID; roots[id] {
				f.Relay(t, familyReply(id, testAgentID, names[id]))
			}
		case *msg.ObjectSelect:
			for _, d := range m.ObjectData {
				id := local[d.ObjectLocalID]
				p := propertiesOf(id)
				p.ObjectData[0].Name = append([]byte(names[id]), 0)
				f.Relay(t, p)
			}
		}
	}
}

// everySelectIsGivenBack fails unless each ObjectSelect has an
// ObjectDeselect for the same local ids.
func everySelectIsGivenBack(t *testing.T, f *fakeBackend) {
	t.Helper()
	held := map[uint32]int{}
	for _, s := range f.Sent() {
		switch m := s.Msg.(type) {
		case *msg.ObjectSelect:
			for _, d := range m.ObjectData {
				held[d.ObjectLocalID]++
			}
		case *msg.ObjectDeselect:
			for _, d := range m.ObjectData {
				held[d.ObjectLocalID]--
			}
		}
	}
	for l, n := range held {
		if n != 0 {
			t.Errorf("object %d was selected %d more times than it was deselected", l, n)
		}
	}
}

func familyAsked(f *fakeBackend) map[msg.UUID]bool {
	out := map[msg.UUID]bool{}
	for _, q := range sentOf[*msg.RequestObjectPropertiesFamily](f) {
		out[q.ObjectData.ObjectID] = true
	}
	return out
}

func selectedLocals(f *fakeBackend) map[uint32]bool {
	out := map[uint32]bool{}
	for _, s := range sentOf[*msg.ObjectSelect](f) {
		for _, d := range s.ObjectData {
			out[d.ObjectLocalID] = true
		}
	}
	return out
}

// quickly is far under the four quiet seconds the old path spent
// before it selected, and well over what a select and its settle take
// (about 0.5 s); the timeout rule gives a bound of about 3x that.
const quickly = 2 * time.Second

// TestAChildIsNamedBySelectingItAtOnce: no family request is sent for a
// child of a linkset, and the name comes without the quiet wait.
func TestAChildIsNamedBySelectingItAtOnce(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	ids := []msg.UUID{msg.MustParseUUID("1a057e57-7e57-c0de-883e-ab08b3bb0f1e")}
	f.mu.Lock()
	f.objects = []*Seen{{Object: Object{ID: ids[0], Local: 501}, Parent: 500}}
	f.mu.Unlock()
	answerAsASimulatorDoes(t, f, map[msg.UUID]string{ids[0]: "a lamp"},
		map[uint32]msg.UUID{501: ids[0]}, nil)

	start := time.Now()
	wait := aside(t, func() (*Seen, error) {
		return w.ObjectByID(context.Background(), ids[0], 20*time.Second)
	})
	got, err := wait()
	if err != nil {
		t.Fatalf("ObjectByID: %v", err)
	}
	if took := time.Since(start); took > quickly {
		t.Errorf("took %v, want under %v", took, quickly)
	}
	if n := w.nameHeld(got.ID); n != "a lamp" {
		t.Errorf("name = %q", n)
	}
	if n := len(familyAsked(f)); n != 0 {
		t.Errorf("%d family requests were sent for a child", n)
	}
	everySelectIsGivenBack(t, f)
}

// TestAnAttachmentIsNamedBySelectingItAtOnce: the root prim of a worn
// object hangs off the avatar, so it is no root in the world.
func TestAnAttachmentIsNamedBySelectingItAtOnce(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	me := msg.MustParseUUID("5eb47e57-7e57-c0de-ec65-5b9e11b13749")
	worn := msg.MustParseUUID("62887e57-7e57-c0de-bc32-9ff327bb9b7e")
	f.mu.Lock()
	f.objects = []*Seen{
		{Object: Object{ID: me, Local: 40}, PCode: pcodeAvatar},
		{Object: Object{ID: worn, Local: 601}, Parent: 40, AttachPoint: 2},
	}
	f.mu.Unlock()
	answerAsASimulatorDoes(t, f, map[msg.UUID]string{worn: "a hat"},
		map[uint32]msg.UUID{601: worn}, nil)

	start := time.Now()
	wait := aside(t, func() (*Seen, error) {
		return w.ObjectByID(context.Background(), worn, 20*time.Second)
	})
	got, err := wait()
	if err != nil {
		t.Fatalf("ObjectByID: %v", err)
	}
	if took := time.Since(start); took > quickly {
		t.Errorf("took %v, want under %v", took, quickly)
	}
	if n := w.nameHeld(got.ID); n != "a hat" {
		t.Errorf("name = %q", n)
	}
	if n := len(familyAsked(f)); n != 0 {
		t.Errorf("%d family requests were sent for an attachment", n)
	}
	everySelectIsGivenBack(t, f)
}

// TestARootIsAskedByTheFamilyRequestAndNotSelected.
func TestARootIsAskedByTheFamilyRequestAndNotSelected(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	root := msg.MustParseUUID("77577e57-7e57-c0de-8229-d903ee75dcd9")
	f.mu.Lock()
	f.objects = []*Seen{{Object: Object{ID: root, Local: 701}}}
	f.mu.Unlock()
	answerAsASimulatorDoes(t, f, map[msg.UUID]string{root: "a bench"},
		map[uint32]msg.UUID{701: root}, map[msg.UUID]bool{root: true})

	wait := aside(t, func() (*Seen, error) {
		return w.ObjectByID(context.Background(), root, 20*time.Second)
	})
	got, err := wait()
	if err != nil {
		t.Fatalf("ObjectByID: %v", err)
	}
	if n := w.nameHeld(got.ID); n != "a bench" {
		t.Errorf("name = %q", n)
	}
	if !familyAsked(f)[root] {
		t.Error("no family request was sent for a root")
	}
	if n := len(sentOf[*msg.ObjectSelect](f)); n != 0 {
		t.Errorf("%d selects were sent for a root that answered", n)
	}
}

// TestAMixedScanAsksRootsAndSelectsChildren.
func TestAMixedScanAsksRootsAndSelectsChildren(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	root := msg.MustParseUUID("90967e57-7e57-c0de-a250-a5d87c79fd24")
	kid := msg.MustParseUUID("9d057e57-7e57-c0de-8640-b1e25c4fd6ce")
	av := msg.MustParseUUID("36d97e57-7e57-c0de-513d-7625ec032f53")
	f.mu.Lock()
	f.objects = []*Seen{
		{Object: Object{ID: root, Local: 801}},
		{Object: Object{ID: kid, Local: 802}, Parent: 801},
		{Object: Object{ID: av, Local: 803}, PCode: pcodeAvatar},
	}
	f.mu.Unlock()
	answerAsASimulatorDoes(t, f,
		map[msg.UUID]string{root: "a bench", kid: "a box"},
		map[uint32]msg.UUID{801: root, 802: kid},
		map[msg.UUID]bool{root: true})
	// The avatar is named by the lookup that comes with it.
	f.mu.Lock()
	f.objects[2].Name = "Example Resident"
	f.mu.Unlock()

	wait := aside(t, func() ([]*Seen, error) {
		return w.AllObjects(context.Background(), 20*time.Second)
	})
	if _, err := wait(); err != nil {
		t.Fatalf("AllObjects: %v", err)
	}
	asked := familyAsked(f)
	if !asked[root] || asked[kid] || asked[av] {
		t.Errorf("family requests went to %v, want the root only", asked)
	}
	sel := selectedLocals(f)
	if sel[801] || !sel[802] || sel[803] {
		t.Errorf("selected %v, want the child only", sel)
	}
	everySelectIsGivenBack(t, f)
}

// nameHeld is what the session has been told an object is called; the
// fake store does not fold that into what it returns.
func (w *Session) nameHeld(id msg.UUID) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.objectNames[id]
}
