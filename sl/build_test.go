package sl

// Making an object out of nothing.
//
// A build is a sequence of things that are not confirmed by replies.  A
// rez is answered by the prim turning up, its bottom rather than its
// centre on the point asked for, so it is recognised by where it stands
// and then put where it belongs; a rename has no reply, so the name is
// read back; a description
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// describedAs is an ObjectProperties reply carrying one description,
// which is the only place a description ever comes back.
func describedAs(id msg.UUID, desc string) *msg.ObjectProperties {
	m := propertiesOf(id)
	m.ObjectData[0].Description = append([]byte(desc), 0)
	return m
}

// landing is where the simulator puts the prim an ObjectAdd asks for,
// as measured on Agni: X and Y as sent, and Z raised by half the height,
// added in float32, so that the prim's bottom is on the point.  See
// doc/rez.md.
func landing(add *msg.ObjectAdd) msg.Vector3 {
	at := add.ObjectData.RayEnd
	at.Z += add.ObjectData.Scale.Z / 2
	return at
}

// appear puts a prim in the region the way a simulator does: the
// backend's list of the region gains it, and the session is relayed its
// update.
func appear(t *testing.T, f *fakeBackend, s *Seen) {
	t.Helper()
	if s.PCode == 0 {
		s.PCode = pcodePrim
	}
	f.mu.Lock()
	f.objects = append(f.objects, s)
	f.mu.Unlock()
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: s.ID, ID: s.Local, ParentID: s.Parent}))
}

// confirmRez plays the simulator's side of a rez: the prim appears
// where the simulator puts one, and the question of whose it is gets
// answered.
func confirmRez(t *testing.T, f *fakeBackend, id msg.UUID, local uint32, ask int) {
	t.Helper()
	add := waitSentN[*msg.ObjectAdd](t, f, ask)
	appear(t, f, &Seen{Object: Object{ID: id, Local: local},
		Position: landing(add), Scale: add.ObjectData.Scale})
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
	// there: the simulator puts its bottom on the point, not its centre.
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

// TestBuildHandsBackAPrimItCouldNotFinish: the name and the
// description are separate round trips after the rez, and a prim that
// is standing there under the wrong name is worse than one that was
// never made -- so the build fails.  But the prim exists, and the build
// hands it back with the error, since nothing else knows its ids and
// somebody has to be able to clear it away.
func TestBuildHandsBackAPrimItCouldNotFinish(t *testing.T) {
	t.Parallel()

	handedBack := func(t *testing.T, b *Built, err error) {
		t.Helper()
		if err == nil {
			t.Error("Build reported a prim it could not finish")
		}
		if b == nil || len(b.Parts) != 1 || b.Root != b.Parts[0] || b.Root.ID != thePrim || b.Root.Local != 77 {
			t.Errorf("Build = %+v, want the prim it made handed back", b)
		}
	}

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
		b, err := wait()
		handedBack(t, b, err)
	})

	t.Run("the description never went", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		failSendsAfter[*msg.MultipleObjectUpdate](f, errors.New("the circuit is gone"))
		wait := aside(t, func() (*Built, error) {
			return w.Build(context.Background(), []Prim{{Description: "a thing"}})
		})
		confirmRez(t, f, thePrim, 77, 1)
		b, err := wait()
		handedBack(t, b, err)
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
		b, err := wait()
		handedBack(t, b, err)
	})
}

// TestBuildHandsBackEveryPrimWhenALaterOneFails: the prims finished
// before the one that failed are standing in the region too, and
// dropping them with the error left them there with nobody holding
// their ids -- slsh clears away half a build only when it is given one.
func TestBuildHandsBackEveryPrimWhenALaterOneFails(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	wait := aside(t, func() (*Built, error) {
		return w.Build(context.Background(), []Prim{
			{Position: msg.Vector3{X: 128, Y: 128, Z: 25}},
			{Name: "top", Position: msg.Vector3{X: 128, Y: 128, Z: 26}},
		})
	})
	confirmRez(t, f, thePrim, 77, 1)
	waitSentN[*msg.MultipleObjectUpdate](t, f, 1)
	confirmRez(t, f, theChild, 78, 2)
	waitSentN[*msg.MultipleObjectUpdate](t, f, 2)
	waitSent[*msg.ObjectName](t, f)
	waitSentN[*msg.RequestObjectPropertiesFamily](t, f, 3)
	f.Relay(t, familyReply(theChild, testAgentID, "Object"))

	b, err := wait()
	if err == nil || !strings.Contains(err.Error(), "prim 2 of 2") {
		t.Errorf("Build = %v, want it to name the prim that failed", err)
	}
	if b == nil || len(b.Parts) != 2 || b.Root != b.Parts[0] ||
		b.Parts[0].ID != thePrim || b.Parts[1].ID != theChild {
		t.Fatalf("Build = %+v, want both prims handed back", b)
	}
	// Nothing is linked on the way out: the prims are loose, and what
	// becomes of them is the caller's to say.
	if got := sentOf[*msg.ObjectLink](f); len(got) != 0 {
		t.Errorf("a failed build went on to link %d times", len(got))
	}
}

// TestCreateHandsBackWhatTheBuildMade: Create is Build and more, and a
// build that failed part way has to reach the caller in one piece.
func TestCreateHandsBackWhatTheBuildMade(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	failSendsAfter[*msg.RequestObjectPropertiesFamily](f, errors.New("the circuit is gone"))
	wait := aside(t, func() (*Built, error) {
		return w.Create(context.Background(), ObjectJSON{Prims: []PrimJSON{{
			Type: "box", Name: "workbench", Pos: []float32{128, 128, 25},
		}}})
	})
	confirmRez(t, f, thePrim, 77, 1)
	b, err := wait()
	if err == nil {
		t.Error("Create reported an object it could not place")
	}
	if b == nil || len(b.Parts) != 1 || b.Root.ID != thePrim {
		t.Errorf("Create = %+v, want the prim that was made handed back", b)
	}
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

// More prims of ours, for the tests of telling a new one from the
// rest.
var (
	ourElsewhere = msg.MustParseUUID("61987e57-7e57-c0de-9478-9591867eb8c2")
	ourNearby    = msg.MustParseUUID("77047e57-7e57-c0de-79eb-36fdeebcb523")
)

// ours is a root prim of ours the region describes at a position.
func ours(id msg.UUID, local uint32, at msg.Vector3) *Seen {
	return &Seen{Object: Object{ID: id, Local: local}, PCode: pcodePrim,
		Owner: testAgentID, Position: at}
}

// TestFindingOursSkipsWhatWasAlreadyThere: what the region held before
// the rez is read from the backend, which heard all of it, and not from
// what this session was relayed since it attached.  An update about
// something already there arrives during a rez as a matter of course,
// and one of ours standing on the very spot -- one a take has just
// removed, before its kill arrives -- is not the new one.
func TestFindingOursSkipsWhatWasAlreadyThere(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	at := msg.Vector3{X: 128, Y: 128, Z: 25}
	spot := msg.Vector3{X: 128, Y: 128, Z: 25.25}

	// Nothing of this was relayed: the session attached after it was
	// said, and only the backend heard it.
	f.objects = []*Seen{ours(theOther, 12, spot)}

	wait := aside(t, func() (*Object, error) {
		return w.Rez(context.Background(), RezOptions{At: at})
	})
	waitSent[*msg.ObjectAdd](t, f)

	// Now the region says it again, and somebody has asked whose it is.
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: theOther, ID: 12}))
	f.Relay(t, familyReply(theOther, testAgentID, "Object"))
	// And the avatar, which is ours and new to the list when a build
	// follows a login closely, standing where the prim was asked for.
	appear(t, f, &Seen{Object: Object{ID: testAgentID, Local: 3}, PCode: pcodeAvatar,
		Owner: testAgentID, Position: spot})

	confirmRez(t, f, thePrim, 77, 1)

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

	at := msg.Vector3{X: 128, Y: 128, Z: 25}
	wait := aside(t, func() (*Object, error) {
		return w.rezAt(context.Background(), at, msg.Vector3{X: 1, Y: 1, Z: 1}, msg.Quaternion{})
	})
	waitSent[*msg.ObjectAdd](t, f)

	// Somebody else's prim arriving on the same spot in the middle of
	// our rez, which is the ordinary state of a busy sandbox.
	spot := msg.Vector3{X: 128, Y: 128, Z: 25.5}
	appear(t, f, &Seen{Object: Object{ID: theOther, Local: 90}, Position: spot})
	waitSent[*msg.RequestObjectPropertiesFamily](t, f)
	f.Relay(t, familyReply(theOther, theChild, "not ours"))

	appear(t, f, &Seen{Object: Object{ID: thePrim, Local: 91}, Position: spot})
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

// TestFindingOursTakesThePrimWhereItWasAskedFor: a prim of ours that is
// new is not necessarily the one just made -- a script of ours, or
// another client of this avatar, can make one at the same moment -- and
// taking the first new one of ours has handed back the wrong one.  The
// simulator gives X and Y back exactly as sent, so one anywhere else,
// even a centimetre off, is somebody else's rez.
func TestFindingOursTakesThePrimWhereItWasAskedFor(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	at := msg.Vector3{X: 128, Y: 128, Z: 25}
	wait := aside(t, func() (*Object, error) {
		return w.Rez(context.Background(), RezOptions{At: at})
	})
	waitSent[*msg.ObjectAdd](t, f)

	appear(t, f, ours(ourElsewhere, 80, msg.Vector3{X: 130, Y: 128, Z: 25.25}))
	appear(t, f, ours(ourNearby, 81, msg.Vector3{X: 128.01, Y: 128, Z: 25.25}))
	f.Relay(t, familyReply(ourElsewhere, testAgentID, "Object"))
	f.Relay(t, familyReply(ourNearby, testAgentID, "Object"))
	appear(t, f, ours(thePrim, 82, msg.Vector3{X: 128, Y: 128, Z: 25.25}))

	o, err := wait()
	if err != nil {
		t.Fatalf("Rez: %v", err)
	}
	if o.ID != thePrim {
		t.Errorf("rezzed %s, want the one where it was asked for", o)
	}
	if n := len(sentOf[*msg.RequestObjectPropertiesFamily](f)); n != 0 {
		t.Errorf("%d objects were asked about, and every owner was known", n)
	}
}

// TestFindingOursAcceptsAPrimRaisedToTheGround: a prim asked for under
// the ground comes up to rest on it, so Z is a floor and not a match.
// Nothing measured ever came back lower than it was asked for, so one
// below the point is not the rez.
func TestFindingOursAcceptsAPrimRaisedToTheGround(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	at := msg.Vector3{X: 128, Y: 128, Z: 20}
	f.objects = []*Seen{
		ours(ourElsewhere, 80, msg.Vector3{X: 128, Y: 128, Z: 19.5}),
		ours(thePrim, 81, msg.Vector3{X: 128, Y: 128, Z: 23.75}),
	}
	o, err := w.findOurs(context.Background(), nil, at, msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}, time.Second)
	if err != nil {
		t.Fatalf("findOurs: %v", err)
	}
	if o.ID != thePrim {
		t.Errorf("found %s, want the one raised to the ground", o)
	}
}

// TestFindingOursPrefersTheOneAtTheHeightItShouldBe: two of ours on the
// same spot are told apart by height -- the new one's centre is half
// its height above the point -- and one whose owner is still unknown is
// waited for rather than passed over for one further off.
func TestFindingOursPrefersTheOneAtTheHeightItShouldBe(t *testing.T) {
	t.Parallel()

	at := msg.Vector3{X: 128, Y: 128, Z: 25}
	scale := msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}

	t.Run("both known", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.objects = []*Seen{
			ours(ourElsewhere, 80, msg.Vector3{X: 128, Y: 128, Z: 28}),
			ours(thePrim, 81, msg.Vector3{X: 128, Y: 128, Z: 25.25}),
		}
		o, err := w.findOurs(context.Background(), nil, at, scale, time.Second)
		if err != nil {
			t.Fatalf("findOurs: %v", err)
		}
		if o.ID != thePrim {
			t.Errorf("found %s, want the one half its height above the point", o)
		}
	})

	t.Run("the nearer one unknown", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		nearer := ours(thePrim, 81, msg.Vector3{X: 128, Y: 128, Z: 25.25})
		nearer.Owner = msg.UUID{}
		f.objects = []*Seen{ours(ourElsewhere, 80, msg.Vector3{X: 128, Y: 128, Z: 28}), nearer}

		wait := aside(t, func() (*Object, error) {
			return w.findOurs(context.Background(), nil, at, scale, 20*time.Second)
		})
		q := waitSent[*msg.RequestObjectPropertiesFamily](t, f)
		if q.ObjectData.ObjectID != thePrim {
			t.Errorf("asked about %s, want the nearer one", q.ObjectData.ObjectID)
		}
		f.Relay(t, familyReply(thePrim, testAgentID, "Object"))

		o, err := wait()
		if err != nil {
			t.Fatalf("findOurs: %v", err)
		}
		if o.ID != thePrim {
			t.Errorf("found %s before the nearer one's owner was known", o)
		}
	})
}

// TestFindingOursGivesUpOnAPrimAnywhereElse: nothing new of ours where
// the rez was asked for is a rez not confirmed, whatever else of ours
// turned up, and the timeout says where it was looking.
func TestFindingOursGivesUpOnAPrimAnywhereElse(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	at := msg.Vector3{X: 128, Y: 128, Z: 25}
	f.objects = []*Seen{
		ours(ourElsewhere, 80, msg.Vector3{X: 128, Y: 128.01, Z: 25.25}),
		ours(ourNearby, 81, msg.Vector3{X: 127.99, Y: 128, Z: 25.25}),
	}
	_, err := w.findOurs(context.Background(), nil, at, msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}, 300*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("findOurs = %v, want a timeout", err)
	}
	if !strings.Contains(err.Error(), "128") {
		t.Errorf("findOurs = %v, want it to say where it looked", err)
	}
}

// TestFindingOursAsksWithoutHoldingTheLock: a family request is a send,
// and a send can wait on the daemon.  Holding the session's lock over it
// stops the reader goroutine handling anything until it returns.
func TestFindingOursAsksWithoutHoldingTheLock(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	var free atomic.Bool
	f.mu.Lock()
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.RequestObjectPropertiesFamily); !ok {
			return
		}
		// A few tries, since the reader takes the lock for a moment
		// now and then on its own account.
		for range 20 {
			if w.mu.TryLock() {
				w.mu.Unlock()
				free.Store(true)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	f.mu.Unlock()

	wait := aside(t, func() (*Object, error) {
		return w.Rez(context.Background(), RezOptions{At: msg.Vector3{X: 128, Y: 128, Z: 25}})
	})
	confirmRez(t, f, thePrim, 77, 1)
	if _, err := wait(); err != nil {
		t.Fatalf("Rez: %v", err)
	}
	if !free.Load() {
		t.Error("the owner was asked for with the session's lock held")
	}
}

// TestRezIsMadeInTheActiveGroup: a viewer rezzes in the avatar's
// active group.  A prim of no group on land that runs only its group's
// scripts holds a script the simulator reports running and that never
// runs.
func TestRezIsMadeInTheActiveGroup(t *testing.T) {
	t.Parallel()
	group := msg.MustParseUUID("80f37e57-7e57-c0de-682d-3aa3f92b7cc4")

	t.Run("the group is sent", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.mu.Lock()
		f.presence.ActiveGroup = group
		f.mu.Unlock()

		wait := aside(t, func() (*Built, error) {
			return w.Build(context.Background(), []Prim{{Position: msg.Vector3{X: 128, Y: 128, Z: 25}}})
		})
		confirmRez(t, f, thePrim, 77, 1)
		if _, err := wait(); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got := onlySent[*msg.ObjectAdd](t, f).AgentData.GroupID; got != group {
			t.Errorf("rezzed in group %s, want the active group %s", got, group)
		}
	})

	t.Run("the group cannot be learned", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.mu.Lock()
		f.presenceErr = errors.New("nobody knows where we are")
		f.mu.Unlock()
		_, err := w.rezAt(context.Background(), msg.Vector3{X: 128, Y: 128, Z: 25},
			msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}, msg.Quaternion{})
		if err == nil {
			t.Error("rezAt went ahead without knowing the group to rez in")
		}
		if got := sentOf[*msg.ObjectAdd](f); len(got) != 0 {
			t.Errorf("a prim was rezzed in no group: %s", f.describe())
		}
	})
}

// TestSetDescriptionStopsWhenTheCallerGivesUp: the same promise SetName
// makes, in the other twenty second loop in this package.
//
// This one had no pause of its own -- the only wait was inside
// Properties -- so an error that came back at once sent it round again
// at once.  A cancelled context did exactly that, and the loop span
// flat out for the full twenty seconds before reporting a timeout that
// was not the reason.
func TestSetDescriptionStopsWhenTheCallerGivesUp(t *testing.T) {
	t.Parallel()
	w, _ := newFakeSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := w.SetDescription(ctx, &Object{ID: thePrim, Local: 77}, "a thing")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("SetDescription = %v, want the context's reason", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("SetDescription took %s to notice the caller had gone", waited)
	}
}
