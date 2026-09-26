package sl

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// theLandGroup is the group the parcel under test is set to.
var theLandGroup = msg.MustParseUUID("41d97e57-7e57-c0de-29fb-119ddd42e51c")

// land is what the fake region says to scriptsBlocked: the ground under
// everything, the parcel, and who owns the prim and what group it is in.
type land struct {
	ground      float32
	flags       uint32
	parcelOwner msg.UUID
	owner       msg.UUID // the prim's, as its properties say
	group       msg.UUID
}

// asked counts what the land was asked, by message name.
type asked struct {
	mu sync.Mutex
	n  map[string]int
}

func (a *asked) of(name string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n[name]
}

// onLand plays the simulator for one parcel and one prim, and the
// daemon for the ground.  The prim is thePrim, local 77, a 0.5 m cube
// of this avatar's with its centre at z.
func onLand(t *testing.T, f *fakeBackend, l land, z float32) *asked {
	t.Helper()
	a := &asked{n: map[string]int{}}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects = []*Seen{{
		Object:   Object{ID: thePrim, Local: 77, Name: "a prim"},
		Owner:    testAgentID,
		Position: msg.Vector3{X: 100, Y: 60, Z: z},
		Scale:    msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5},
		PCode:    pcodePrim,
	}}
	f.ground = func(west, south, east, north float32) (float32, bool) {
		return l.ground, true
	}
	f.onSend = func(m msg.Message) {
		a.mu.Lock()
		a.n[m.MsgInfo().Name]++
		a.mu.Unlock()
		switch q := m.(type) {
		case *msg.ParcelPropertiesRequest:
			f.RelayEvent(t, "ParcelProperties", fmt.Sprintf(`<llsd><map>
			  <key>ParcelData</key><array><map>
			    <key>Name</key><string>Perlwick Row</string>
			    <key>LocalID</key><integer>9</integer>
			    <key>SequenceID</key><integer>%d</integer>
			    <key>ParcelFlags</key><integer>%d</integer>
			    <key>OwnerID</key><uuid>%s</uuid>
			    <key>GroupID</key><uuid>%s</uuid>
			  </map></array>
			</map></llsd>`, q.ParcelData.SequenceID, l.flags, l.parcelOwner, theLandGroup))
		case *msg.RequestObjectPropertiesFamily:
			r := &msg.ObjectPropertiesFamily{}
			r.ObjectData.ObjectID = q.ObjectData.ObjectID
			r.ObjectData.OwnerID = l.owner
			r.ObjectData.GroupID = l.group
			r.ObjectData.Name = append([]byte("a prim"), 0)
			f.Relay(t, r)
		case *msg.GetScriptRunning:
			f.Relay(t, scriptRunningReply(q.Script.ObjectID, q.Script.ItemID, false))
		}
	}
	return a
}

// groupOnly is a parcel that runs only its group's scripts, and its
// owner's.
const groupOnly = agent.ParcelAllowGroupScripts

// TestARunOnLandThatWillNotRunItSaysSoAtOnce: measured on Agni, a script
// in a prim of no group on a parcel that runs only group scripts, near
// the ground, never ran and was reported running.  Waiting for its
// sentinel waits out the whole timeout for nothing.
func TestARunOnLandThatWillNotRunItSaysSoAtOnce(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	up := serveUpload(t, f, "UpdateScriptTask", compiles)
	onLand(t, f, land{ground: 20, flags: groupOnly, parcelOwner: nemo, owner: testAgentID}, 21.25)

	wait := aside(t, func() (*Result, error) {
		return w.Run(context.Background(), Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
			Source: "default {}", Done: "FINISHED", Timeout: time.Minute,
		})
	})
	answerContents(t, f, thePrim, theContentsFile)
	<-up.body

	res, err := wait()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Blocked, "the land doesn't run this object's scripts here") ||
		!strings.Contains(res.Blocked, "Perlwick Row") || !strings.Contains(res.Blocked, "1.00 m") {
		t.Errorf("blocked = %q", res.Blocked)
	}
	if !res.Compiled || res.Finished || !res.Failed() {
		t.Errorf("Run = %+v; want compiled, not finished", res)
	}
	if res.Elapsed > 30*time.Second {
		t.Errorf("the run waited %s for a script the land does not run", res.Elapsed)
	}
	// Installed and started, as asked, and stopped again after.
	stopped := false
	for _, s := range f.Sent() {
		if m, ok := s.Msg.(*msg.SetScriptRunning); ok && !m.Script.Running {
			stopped = true
		}
	}
	if !stopped {
		t.Error("the script was left running")
	}
}

// TestARunFiftyMetresUpIsNotBlocked: the parcel's rules reach 50 m above
// the ground, and a script above that ran on the same parcel.  Nothing
// is asked about the parcel at all.
func TestARunFiftyMetresUpIsNotBlocked(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	up := serveUpload(t, f, "UpdateScriptTask", compiles)
	a := onLand(t, f, land{ground: 20, flags: groupOnly, parcelOwner: nemo, owner: testAgentID}, 70.25)

	wait := aside(t, func() (*Result, error) {
		return w.Run(context.Background(), Script{
			In: foundHere(w, &Object{ID: thePrim, Local: 77}), Name: "a script",
			Source: "default {}", Done: "FINISHED", Timeout: time.Minute,
		})
	})
	answerContents(t, f, thePrim, theContentsFile)
	<-up.body
	f.Relay(t, objectSaid(thePrim, ChatSay, "FINISHED"))

	res, err := wait()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Blocked != "" || !res.Finished {
		t.Errorf("Run = %+v; want finished and not blocked", res)
	}
	if n := a.of("ParcelPropertiesRequest"); n != 0 {
		t.Errorf("the parcel was asked about %d times for a prim above its reach", n)
	}
}

// TestTheLandRunsItsOwnersScriptsAndItsGroups: which objects a parcel
// with scripts off runs, from its flags (llparcelflags.h), and how close
// to the edge of its reach.
func TestTheLandRunsItsOwnersScriptsAndItsGroups(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		what    string
		l       land
		z       float32 // the prim's centre
		blocked bool
		family  bool // whether the object had to be asked about
	}{
		{"other scripts on", land{ground: 20, flags: agent.ParcelAllowOtherScripts, parcelOwner: nemo}, 21.25, false, false},
		{"the parcel owner's own", land{ground: 20, parcelOwner: testAgentID}, 21.25, false, false},
		{"in the parcel's group", land{ground: 20, flags: groupOnly, parcelOwner: nemo, owner: testAgentID, group: theLandGroup}, 21.25, false, true},
		{"in no group, group scripts on", land{ground: 20, flags: groupOnly, parcelOwner: nemo, owner: testAgentID}, 21.25, true, true},
		{"scripts off for all but the owner", land{ground: 20, parcelOwner: nemo}, 21.25, true, false},
		{"its bottom just under 50 m up", land{ground: 20, parcelOwner: nemo}, 70.2, true, false},
		{"its bottom at 50 m up", land{ground: 20, parcelOwner: nemo}, 70.25, false, false},
	} {
		t.Run(c.what, func(t *testing.T) {
			t.Parallel()
			w, f := newFakeSession(t)
			a := onLand(t, f, c.l, c.z)
			got := w.scriptsBlocked(context.Background(), &Object{ID: thePrim, Local: 77})
			if (got != "") != c.blocked {
				t.Errorf("blocked = %q, want blocked %v", got, c.blocked)
			}
			if n := a.of("RequestObjectPropertiesFamily"); (n > 0) != c.family {
				t.Errorf("the object was asked about %d times", n)
			}
		})
	}
}

// TestTheRegionCanStopEveryScript: the region's flags say so for every
// object, at any height, without asking about the parcel.
func TestTheRegionCanStopEveryScript(t *testing.T) {
	t.Parallel()
	for flag, want := range map[uint32]string{
		agent.RegionSkipScripts:       "this region is not running any scripts",
		agent.RegionEstateSkipScripts: "an administrator has temporarily stopped scripts in this region",
	} {
		w, f := newFakeSession(t)
		a := onLand(t, f, land{ground: 20, flags: agent.ParcelAllowOtherScripts}, 300)
		f.mu.Lock()
		f.region.Flags = flag
		f.mu.Unlock()
		if got := w.scriptsBlocked(context.Background(), &Object{ID: thePrim}); got != want {
			t.Errorf("flag %#x: blocked = %q, want %q", flag, got, want)
		}
		if n := a.of("ParcelPropertiesRequest"); n != 0 {
			t.Errorf("flag %#x: the parcel was asked about", flag)
		}
	}
}

// TestWhatCannotBeToldIsNotBlocked: a worn object, land that has not
// arrived, and an object that is not in the region are none of them a
// reason to say a script will not run, and none is worth asking the
// parcel about.
func TestWhatCannotBeToldIsNotBlocked(t *testing.T) {
	t.Parallel()
	nowhere := land{ground: 20, parcelOwner: nemo}
	for what, change := range map[string]func(f *fakeBackend){
		"worn": func(f *fakeBackend) { f.objects[0].AttachPoint = 2 },
		"a child of something worn": func(f *fakeBackend) {
			f.objects[0].Parent = 5
			f.objects = append(f.objects, &Seen{Object: Object{ID: theOther, Local: 5}, Parent: 1, AttachPoint: 2})
		},
		"no land yet":           func(f *fakeBackend) { f.ground = nil },
		"not in the region":     func(f *fakeBackend) { f.objects = nil },
		"its root not in sight": func(f *fakeBackend) { f.objects[0].Parent = 5 },
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			w, f := newFakeSession(t)
			a := onLand(t, f, nowhere, 21.25)
			f.mu.Lock()
			change(f)
			f.mu.Unlock()
			if got := w.scriptsBlocked(context.Background(), &Object{ID: thePrim}); got != "" {
				t.Errorf("blocked = %q", got)
			}
			if n := a.of("ParcelPropertiesRequest"); n != 0 {
				t.Error("the parcel was asked about")
			}
		})
	}
}

// TestAChildPrimIsPlacedInItsRootsFrame: a child's position is an offset
// from its root, turned by the root's rotation, and the ground asked
// about is under where the prim is in the region.
func TestAChildPrimIsPlacedInItsRootsFrame(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	onLand(t, f, land{ground: 20, flags: agent.ParcelAllowOtherScripts}, 0)
	var rect [4]float32
	s := float32(math.Sin(math.Pi / 4))
	f.mu.Lock()
	// The root, turned a quarter about z; the child 2 m along the
	// root's x, which is the region's y, and turned an eighth more.
	f.objects = []*Seen{
		{Object: Object{ID: theOther, Local: 5}, Position: msg.Vector3{X: 100, Y: 60, Z: 30},
			Rotation: msg.Quaternion{Z: s}, Scale: msg.Vector3{X: 1, Y: 1, Z: 1}},
		{Object: Object{ID: thePrim, Local: 77}, Parent: 5, Position: msg.Vector3{X: 2},
			Rotation: msg.Quaternion{Z: float32(math.Sin(math.Pi / 8))}, Scale: msg.Vector3{X: 1, Y: 1, Z: 2}},
	}
	f.ground = func(west, south, east, north float32) (float32, bool) {
		rect = [4]float32{west, south, east, north}
		return 20, true
	}
	f.mu.Unlock()

	w.scriptsBlocked(context.Background(), &Object{ID: thePrim})

	// Turned three eighths of a turn in all, a unit square spans the
	// square root of two each way, about its centre at 100, 62.
	h := float32(math.Sqrt2 / 2)
	want := [4]float32{100 - h, 62 - h, 100 + h, 62 + h}
	for i := range rect {
		if math.Abs(float64(rect[i]-want[i])) > 1e-4 {
			t.Fatalf("asked about the ground under %v, want %v", rect, want)
		}
	}
}
