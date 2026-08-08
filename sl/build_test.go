package sl

// Making an object out of nothing.
//
// A build is a sequence of things that are not confirmed by replies.  A
// rez is a request the simulator may place where it likes, so the prim
// is put where it belongs afterwards rather than trusted to have landed
// there; a rename has no reply, so the name is read back; a description
// only arrives in the full properties, which have to be asked for by
// selecting the object; and a link shows up as the children naming the
// root as their parent in an update that would have arrived anyway.
//
// Each prim is finished before the next is started, and that is the part
// worth holding to.  Rezzing a batch and sorting out afterwards which is
// which cannot work: objects stream in the whole time, and two prims of
// the same size at the same place are indistinguishable once both exist.
//
// The calls wait for messages the test has to relay, so they run aside;
// see fake_test.go.

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// describedAs is an ObjectProperties reply carrying one description,
// which is the only place a description ever comes back.
func describedAs(id msg.UUID, desc string) *msg.ObjectProperties {
	m := propertiesOf(id)
	m.ObjectData[0].Description = append([]byte(desc), 0)
	return m
}

// confirmRez plays the simulator's side of a rez: the region describes
// something new, and answers the question of whose it is.
func confirmRez(t *testing.T, f *fakeBackend, id msg.UUID, local uint32, ask int) {
	t.Helper()
	waitSentN[*msg.ObjectAdd](t, f, ask)
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: id, ID: local}))
	q := waitSentN[*msg.RequestObjectPropertiesFamily](t, f, ask)
	if q.ObjectData.ObjectID != id {
		t.Errorf("asked whose %s is, want %s", q.ObjectData.ObjectID, id)
	}
	f.Relay(t, familyReply(id, testAgentID, "Object"))
}

// TestBuildFinishesEachPrimBeforeStartingTheNext: objects stream in the
// whole time, so the only way to know which new one is ours is to ask
// about each as it appears.  Rezzing a batch and matching up afterwards
// has nothing to match on.
func TestBuildFinishesEachPrimBeforeStartingTheNext(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	at := msg.Vector3{X: 129, Y: 128, Z: 25}
	size := msg.Vector3{X: 2, Y: 1, Z: 0.5}
	rot := msg.Quaternion{X: 0, Y: 0, Z: 0.7071}
	wait := aside(t, func() (*Built, error) {
		return w.Build(context.Background(), []Prim{{
			Name: "workbench", Description: "a thing",
			Position: at, Size: size, Rotation: rot,
		}})
	})

	confirmRez(t, f, thePrim, 77, 1)

	// The prim is told where it goes rather than trusted to have landed
	// there: a rez is a request, and the simulator places it near the
	// ray at a height of its own choosing.
	place := waitSent[*msg.MultipleObjectUpdate](t, f)
	if len(place.ObjectData) != 1 || place.ObjectData[0].ObjectLocalID != 77 {
		t.Fatalf("placed %+v", place.ObjectData)
	}
	d := place.ObjectData[0]
	if d.Type != updPosition|updRotation|updScale {
		t.Errorf("the update sets %#x, want position, rotation and scale", d.Type)
	}
	// Position, then rotation, then scale, twelve bytes each: a
	// quaternion travels as three floats with the fourth recovered by
	// normalising.
	if len(d.Data) != 36 {
		t.Fatalf("the payload is %d bytes, want 36", len(d.Data))
	}
	if got := readVector(d.Data[0:]); got != at {
		t.Errorf("placed at %v, want %v", got, at)
	}
	if got := readVector(d.Data[12:]); got != (msg.Vector3{Z: 0.7071}) {
		t.Errorf("rotated to %v", got)
	}
	if got := readVector(d.Data[24:]); got != size {
		t.Errorf("scaled to %v, want %v", got, size)
	}

	// The name, read back, since ObjectName has no reply.
	waitSent[*msg.ObjectName](t, f)
	waitSentN[*msg.RequestObjectPropertiesFamily](t, f, 2)
	f.Relay(t, familyReply(thePrim, testAgentID, "workbench"))

	// The description, which only comes back in the full properties.
	waitSent[*msg.ObjectDescription](t, f)
	waitSent[*msg.ObjectSelect](t, f)
	f.Relay(t, describedAs(thePrim, "a thing"))

	b, err := wait()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(b.Parts) != 1 || b.Root != b.Parts[0] {
		t.Fatalf("built %+v", b)
	}
	if b.Root.ID != thePrim || b.Root.Name != "workbench" {
		t.Errorf("built %s", b.Root)
	}
	// One prim has nothing to link, and a link message for it would be
	// the simulator being asked to link an object to itself.
	if got := sentOf[*msg.ObjectLink](f); len(got) != 0 {
		t.Errorf("a single prim was linked to %d things", len(got))
	}
}

// TestBuildLinksTheRestOntoTheFirst: a shape is described by saying
// where each piece goes and letting the link keep it there, so the root
// is the first prim and the rest arrive already in place.
func TestBuildLinksTheRestOntoTheFirst(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	wait := aside(t, func() (*Built, error) {
		return w.Build(context.Background(), []Prim{
			{Position: msg.Vector3{X: 128, Y: 128, Z: 25}},
			{Position: msg.Vector3{X: 129, Y: 128, Z: 25}},
		})
	})

	confirmRez(t, f, thePrim, 77, 1)
	waitSentN[*msg.MultipleObjectUpdate](t, f, 1)
	confirmRez(t, f, theChild, 78, 2)
	waitSentN[*msg.MultipleObjectUpdate](t, f, 2)

	// A link acts on the current selection, so the selection goes first
	// and the simulator is given a moment to take it in.
	sel := waitSent[*msg.ObjectSelect](t, f)
	if len(sel.ObjectData) != 2 {
		t.Errorf("selected %+v, want both prims", sel.ObjectData)
	}
	link := waitSent[*msg.ObjectLink](t, f)
	if len(link.ObjectData) != 2 || link.ObjectData[0].ObjectLocalID != 77 {
		t.Errorf("linked %+v, want the root first", link.ObjectData)
	}
	// Nothing replies to a link: it shows up as the child naming the
	// root as its parent.
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: theChild, ID: 78, ParentID: 77}))

	b, err := wait()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(b.Parts) != 2 || b.Root.ID != thePrim || b.Parts[1].ID != theChild {
		t.Errorf("built %+v", b)
	}
}

// TestBuildChecksEveryPositionBeforeRezzingAny: checking as it goes
// would leave the prims built so far standing where nothing can see
// them, which is a worse place to stop than not having started.
func TestBuildChecksEveryPositionBeforeRezzingAny(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.DrawDistance = 64
	f.mu.Unlock()

	_, err := w.Build(context.Background(), []Prim{
		{Name: "near", Position: msg.Vector3{X: 128, Y: 128, Z: 25}},
		{Name: "far", Position: msg.Vector3{X: 1000, Y: 1000, Z: 25}},
	})
	if !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("Build = %v, want it refused for range", err)
	}
	// Which prim, and how far out, since the caller wrote the list and
	// has to find the one it got wrong.
	if !strings.Contains(err.Error(), "prim 2 of 2") || !strings.Contains(err.Error(), `"far"`) {
		t.Errorf("Build = %v, want it to name the prim it refused", err)
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("the first prim was rezzed before the second was checked: %s", f.describe())
	}
}

// TestBuildReportsWhatDidNotHappen: a build is a dozen round trips and
// a caller told only that it failed cannot tell a refused rez from a
// link that never took.
func TestBuildReportsWhatDidNotHappen(t *testing.T) {
	t.Parallel()

	t.Run("nothing to build", func(t *testing.T) {
		w, _ := newFakeSession(t)
		if _, err := w.Build(context.Background(), nil); err == nil {
			t.Error("Build made an object out of no prims at all")
		}
	})

	t.Run("nothing knows where we are", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.mu.Lock()
		f.presenceErr = errors.New("no presence")
		f.mu.Unlock()
		if _, err := w.Build(context.Background(), []Prim{{}}); err == nil {
			t.Error("Build went ahead without knowing whether it was in range")
		}
	})

	t.Run("the rez was never confirmed", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		_, err := w.Build(context.Background(), []Prim{{Name: "workbench"}})
		if err == nil || !strings.Contains(err.Error(), "prim 1 of 1") {
			t.Errorf("Build = %v, want it to name the prim that failed", err)
		}
	})

	t.Run("the link never took", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		wait := aside(t, func() (*Built, error) {
			return w.Build(context.Background(), []Prim{
				{Position: msg.Vector3{X: 128, Y: 128, Z: 25}},
				{Position: msg.Vector3{X: 129, Y: 128, Z: 25}},
			})
		})
		confirmRez(t, f, thePrim, 77, 1)
		waitSentN[*msg.MultipleObjectUpdate](t, f, 1)
		confirmRez(t, f, theChild, 78, 2)
		waitSentN[*msg.MultipleObjectUpdate](t, f, 2)
		// The link never goes out -- and the prims that were built are
		// still handed back, since they exist and somebody has to be
		// able to clear them up.
		f.FailSends(errors.New("the circuit is gone"))

		b, err := wait()
		if err == nil || !strings.Contains(err.Error(), "linking") {
			t.Errorf("Build = %v, want it to say the link failed", err)
		}
		if b == nil || len(b.Parts) != 2 {
			t.Errorf("a failed link threw away the prims that were built: %+v", b)
		}
	})
}

// TestBuildOneRefusesAPrimItCouldNotFinish: the name and the description
// are separate round trips after the rez, and a prim that is standing
// there under the wrong name is worse than one that was never made.
func TestBuildOneRefusesAPrimItCouldNotFinish(t *testing.T) {
	t.Parallel()

	t.Run("the placement never went", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		// The rez is confirmed and everything after it fails, which is
		// the circuit going while the prim is half made.
		failSendsAfter[*msg.RequestObjectPropertiesFamily](f, errors.New("the circuit is gone"))
		wait := aside(t, func() (*Built, error) {
			return w.Build(context.Background(), []Prim{{Name: "workbench"}})
		})
		confirmRez(t, f, thePrim, 77, 1)
		if _, err := wait(); err == nil {
			t.Error("Build reported a prim it could not place")
		}
	})

	t.Run("the description never went", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		failSendsAfter[*msg.MultipleObjectUpdate](f, errors.New("the circuit is gone"))
		wait := aside(t, func() (*Built, error) {
			return w.Build(context.Background(), []Prim{{Description: "a thing"}})
		})
		confirmRez(t, f, thePrim, 77, 1)
		if _, err := wait(); err == nil {
			t.Error("Build reported a prim it could not describe")
		}
	})

	t.Run("the rename did not take", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		wait := aside(t, func() (*Built, error) {
			return w.Build(context.Background(), []Prim{{Name: "workbench"}})
		})
		confirmRez(t, f, thePrim, 77, 1)
		waitSent[*msg.MultipleObjectUpdate](t, f)
		waitSent[*msg.ObjectName](t, f)
		waitSentN[*msg.RequestObjectPropertiesFamily](t, f, 2)
		// Renamed to something else entirely, which is what a name the
		// simulator would not accept comes back as.
		f.Relay(t, familyReply(thePrim, testAgentID, "Object"))
		if _, err := wait(); err == nil {
			t.Error("Build reported a prim under a name it does not have")
		}
	})
}

// TestSetDescriptionReadsItBackBecauseNothingConfirmsIt:
// ObjectDescription has no reply and the description only arrives in the
// full properties, so writing one and reading it straight afterwards
// reported an empty description -- the read overtook the write.
func TestSetDescriptionReadsItBackBecauseNothingConfirmsIt(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	o := &Object{ID: thePrim, Local: 77}

	waitErr := asideErr(t, func() error {
		return w.SetDescription(context.Background(), o, "a thing")
	})

	m := waitSent[*msg.ObjectDescription](t, f)
	if len(m.ObjectData) != 1 || m.ObjectData[0].LocalID != 77 {
		t.Fatalf("described %+v", m.ObjectData)
	}
	if got := trimNul(m.ObjectData[0].Description); got != "a thing" {
		t.Errorf("described as %q", got)
	}

	// The value that has not landed yet, which is exactly what a read
	// overtaking the write sees, and then the one that has.
	waitSentN[*msg.ObjectSelect](t, f, 1)
	f.Relay(t, describedAs(thePrim, ""))
	waitSentN[*msg.ObjectSelect](t, f, 2)
	f.Relay(t, describedAs(thePrim, "a thing"))

	if err := waitErr(); err != nil {
		t.Fatalf("SetDescription: %v", err)
	}
}

// TestSetDescriptionReportsWhatDidNotHappen: the write goes over the
// wire and the read back is a selection and a reply, so a caller told
// nothing would believe the description had been set.
func TestSetDescriptionReportsWhatDidNotHappen(t *testing.T) {
	w, f := newFakeSession(t)
	f.FailSends(errors.New("the circuit is gone"))
	err := w.SetDescription(context.Background(), &Object{ID: thePrim, Local: 77}, "a thing")
	if err == nil {
		t.Error("SetDescription confirmed a description that never went out")
	}
}

// TestPlaceSaysAllThreeThingsInOneMessage: position, rotation and scale
// travel in one payload with a mask saying which are present, and a
// reader that got the order wrong would rotate a prim by its size.
func TestPlaceSaysAllThreeThingsInOneMessage(t *testing.T) {
	w, f := newFakeSession(t)
	at := msg.Vector3{X: 1, Y: 2, Z: 3}
	scale := msg.Vector3{X: 4, Y: 5, Z: 6}
	rot := msg.Quaternion{X: 0.5, Y: 0.5, Z: 0.5}

	err := w.Place(context.Background(), &Object{ID: thePrim, Local: 77}, at, rot, scale)
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	d := onlySent[*msg.MultipleObjectUpdate](t, f).ObjectData[0]
	if got := readVector(d.Data[0:]); got != at {
		t.Errorf("position = %v", got)
	}
	// The fourth component is not sent: it is recovered by normalising.
	if got := readVector(d.Data[12:]); got != (msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}) {
		t.Errorf("rotation = %v", got)
	}
	if got := readVector(d.Data[24:]); got != scale {
		t.Errorf("scale = %v", got)
	}
}

// readVector reads one of the three-float groups a placement payload is
// made of, which is what says the order was written the way the viewer
// writes it.
func readVector(b []byte) msg.Vector3 {
	f := func(i int) float32 {
		return math.Float32frombits(binary.LittleEndian.Uint32(b[i : i+4]))
	}
	return msg.Vector3{X: f(0), Y: f(4), Z: f(8)}
}

// TestInRangeOnlyRefusesWhatWouldNotBeDescribed: rezzing beyond the draw
// distance is not refused by the simulator -- the prim is made and then
// nothing is ever said about it -- but a draw distance of nothing means
// the session has not been told what it is, and refusing everything
// would be worse than the problem.
func TestInRangeOnlyRefusesWhatWouldNotBeDescribed(t *testing.T) {
	unknown := &Presence{Camera: msg.Vector3{X: 128, Y: 128, Z: 25}}
	if err := inRange(unknown, msg.Vector3{X: 9000}); err != nil {
		t.Errorf("inRange refused something with no draw distance to judge by: %v", err)
	}

	p := &Presence{Camera: msg.Vector3{X: 128, Y: 128, Z: 25}, DrawDistance: 64}
	if err := inRange(p, msg.Vector3{X: 150, Y: 128, Z: 25}); err != nil {
		t.Errorf("inRange refused a position 22 m away: %v", err)
	}
	err := inRange(p, msg.Vector3{X: 1000, Y: 128, Z: 25})
	if !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("inRange = %v", err)
	}
	// The distance and the draw distance both, since the caller has to
	// decide whether to move or to see further.
	if !strings.Contains(err.Error(), "872 m") || !strings.Contains(err.Error(), "64 m") {
		t.Errorf("inRange = %v, want it to say how far out it was", err)
	}
}

// TestFindingOursSkipsWhatWasAlreadyThere: a local id that is new to
// this session is not necessarily one we just made -- objects stream in
// the whole time -- and local id 0 is an attachment, which is never a
// fresh rez.
func TestFindingOursSkipsWhatWasAlreadyThere(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	// Two objects the session already knows about before anything is
	// rezzed: one worn, one somebody else's standing in the region.
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: theOther, ID: 0}))
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: theChild, ID: 12}))

	wait := aside(t, func() (*Object, error) {
		return w.Rez(context.Background(), RezOptions{At: msg.Vector3{X: 128, Y: 128, Z: 25}})
	})
	waitSent[*msg.ObjectAdd](t, f)
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: thePrim, ID: 77}))

	// Only the new one is asked about: the other two were there before.
	q := waitSent[*msg.RequestObjectPropertiesFamily](t, f)
	if q.ObjectData.ObjectID != thePrim {
		t.Fatalf("asked about %s, want the prim just rezzed", q.ObjectData.ObjectID)
	}
	f.Relay(t, familyReply(thePrim, testAgentID, "Object"))

	o, err := wait()
	if err != nil {
		t.Fatalf("Rez: %v", err)
	}
	if o.ID != thePrim {
		t.Errorf("rezzed %s", o)
	}
	if n := len(sentOf[*msg.RequestObjectPropertiesFamily](f)); n != 1 {
		t.Errorf("%d objects were asked about, want only the new one", n)
	}
}

// TestFindingOursWillNotClaimSomebodyElsesPrim: building on another
// avatar's prim by mistake fails later in ways that look like something
// else, so ownership is confirmed rather than assumed.
func TestFindingOursWillNotClaimSomebodyElsesPrim(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	wait := aside(t, func() (*Object, error) {
		return w.rezAt(context.Background(), msg.Vector3{X: 128, Y: 128, Z: 25},
			msg.Vector3{X: 1, Y: 1, Z: 1}, msg.Quaternion{})
	})
	waitSent[*msg.ObjectAdd](t, f)

	// Somebody else's prim arriving in the middle of our rez, which is
	// the ordinary state of a busy region.
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: theOther, ID: 90}))
	waitSent[*msg.RequestObjectPropertiesFamily](t, f)
	f.Relay(t, familyReply(theOther, theChild, "not ours"))

	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: thePrim, ID: 91}))
	waitSentN[*msg.RequestObjectPropertiesFamily](t, f, 2)
	f.Relay(t, familyReply(thePrim, testAgentID, "Object"))

	o, err := wait()
	if err != nil {
		t.Fatalf("rezAt: %v", err)
	}
	if o.ID != thePrim {
		t.Errorf("rezAt took %s, which belongs to somebody else", o)
	}
}
