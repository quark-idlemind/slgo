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

	child := msg.MustParseUUID("d7987e57-7e57-c0de-dab1-160750dc77f2")
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

	somebody := msg.MustParseUUID("36d97e57-7e57-c0de-7134-ab99a0aedcbd")
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
