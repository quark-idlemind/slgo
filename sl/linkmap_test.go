package sl

// LinkMap: the region's own numbering of an object's links, from a script
// dropped into its root.  The far end is played by mapWorld: it answers
// the object's properties, takes the drop, says the script's lines as the
// object, and keeps what the object holds.
// Why: doc/scripts.md#the-links-of-an-object-from-its-own-script

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// Invented ids of an object of six prims, and one that is not theirs.
var (
	lmRoot  = msg.MustParseUUID("0a947e57-7e57-c0de-4e60-22b1233c4fd6")
	lmLid   = msg.MustParseUUID("20b67e57-7e57-c0de-f988-c27578222432")
	lmHinge = msg.MustParseUUID("26767e57-7e57-c0de-614e-ef72a239e849")
	lmLatch = msg.MustParseUUID("44907e57-7e57-c0de-1d57-dc247246409f")
	lmFoot  = msg.MustParseUUID("46397e57-7e57-c0de-d1a7-579820af3b65")
	lmKnob  = msg.MustParseUUID("599b7e57-7e57-c0de-234e-67a5bb3bdef4")
	lmOther = msg.MustParseUUID("69717e57-7e57-c0de-28f5-b9b7b174b1d0")
	lmSeat  = msg.MustParseUUID("91e57e57-7e57-c0de-0248-d6125a418f84")
	lmItem  = msg.MustParseUUID("bbe87e57-7e57-c0de-a1fd-8bafb6589d2b")
	lmAsset = msg.MustParseUUID("de5f7e57-7e57-c0de-256d-f8a0d907c7e7")
	lmCopy  = msg.MustParseUUID("e26e7e57-7e57-c0de-87ee-fe9453ea5fc3")
	lmDir   = msg.MustParseUUID("e3ca7e57-7e57-c0de-764d-be3d54bd7e1a")
)

// sixLines is what the script says of a six-prim object, by link number.
func sixLines() []string {
	return []string{
		"LINKMAP 1 " + lmRoot.String() + " crate",
		"LINKMAP 2 " + lmLid.String() + " lid",
		"LINKMAP 3 " + lmHinge.String() + " left hinge",
		"LINKMAP 4 " + lmLatch.String() + " latch",
		"LINKMAP 5 " + lmFoot.String() + " foot",
		"LINKMAP 6 " + lmKnob.String() + " ",
		"LINKMAP done 6 0",
	}
}

// mapWorld is the object, the avatar's inventory and the region around a
// LinkMap.
type mapWorld struct {
	t *testing.T
	w *Session
	f *fakeBackend
	o *Object

	mu sync.Mutex

	// The object's properties.
	owner     msg.UUID
	ownerMask uint32

	// say is what the script says when it is dropped, and from is who
	// says it; leaves keeps the script in the object after it has spoken,
	// and stuck keeps it there whatever is asked.
	say    []string
	from   msg.UUID
	leaves bool
	stuck  bool
	noise  []*msg.ChatFromSimulator // said first: what is not the script's

	// late is said lateAfter after the rest: a line that comes behind.
	late      []string
	lateAfter time.Duration

	holds   []string // what the object holds, by name
	drops   []*msg.RezScript
	removes int
	reads   int

	// item is the script in the avatar's inventory, nil for none, and
	// patched says its description was changed over AIS.
	item    *Item
	patched bool
	ais     *ais
}

// newMapWorld makes the world with an object of six prims that the avatar
// owns and may modify, the script in inventory at this version, and the
// script saying sixLines.
func newMapWorld(t *testing.T) *mapWorld {
	t.Helper()
	w, f := newFakeSession(t)
	m := &mapWorld{
		t: t, w: w, f: f,
		owner: testAgentID, ownerMask: 0x7fffffff,
		say: sixLines(), from: lmRoot,
		item: &Item{ID: lmItem, AssetID: lmAsset, Name: linkMapName, Desc: linkMapVersion,
			Type: int(AssetLSLText), InvType: int(AssetLSLText)},
	}
	m.o = foundHere(w, &Object{ID: lmRoot, Local: 41})
	f.mu.Lock()
	f.objects = []*Seen{
		{Object: Object{ID: lmRoot, Local: 41, Name: "crate"}, PCode: 9},
		{Object: Object{ID: lmLid, Local: 42}, PCode: 9, Parent: 41},
		{Object: Object{ID: lmHinge, Local: 43}, PCode: 9, Parent: 41},
		{Object: Object{ID: lmLatch, Local: 44}, PCode: 9, Parent: 41},
	}
	f.onSend = m.onSend
	f.mu.Unlock()
	m.serveInventory()
	f.ServeCap(t, "UpdateScriptAgent", func(rw http.ResponseWriter, r *http.Request) {})
	return m
}

// serveInventory serves the Scripts folder and what is in it, and takes
// the change of a description as the grid does.
func (m *mapWorld) serveInventory() {
	m.ais = serveAIS(m.t, m.f, func(id msg.UUID) ([]*Folder, []*Item) {
		m.mu.Lock()
		defer m.mu.Unlock()
		scripts := &Folder{ID: lmDir, ParentID: testInvRoot, Name: "Scripts", Type: 10}
		if id == testInvRoot {
			return []*Folder{scripts}, nil
		}
		if id != lmDir || m.item == nil {
			return nil, nil
		}
		it := *m.item
		if len(m.ais.changes()) > 0 {
			it.Desc = linkMapVersion
		}
		return nil, []*Item{&it}
	})
}

func (m *mapWorld) onSend(x msg.Message) {
	switch q := x.(type) {
	case *msg.ObjectSelect:
		p := &msg.ObjectProperties{}
		m.mu.Lock()
		p.ObjectData = append(p.ObjectData, msg.ObjectProperties_ObjectData{
			ObjectID: lmRoot, CreatorID: m.owner, OwnerID: m.owner, GroupID: msg.UUID{2},
			BaseMask: 0x7fffffff, OwnerMask: m.ownerMask, GroupMask: 0, EveryoneMask: 0,
			Name: append([]byte("crate"), 0), Description: append([]byte(""), 0),
		})
		m.mu.Unlock()
		m.f.Relay(m.t, p)
	case *msg.RezScript:
		m.mu.Lock()
		m.drops = append(m.drops, q)
		name := strings.TrimRight(string(q.InventoryBlock.Name), "\x00")
		m.holds = append(m.holds, name)
		lines, from, leaves, noise := m.say, m.from, m.leaves, m.noise
		late, lateAfter := m.late, m.lateAfter
		if !leaves && !m.stuck {
			m.holds = m.holds[:len(m.holds)-1]
		}
		m.mu.Unlock()
		for _, n := range noise {
			m.f.Relay(m.t, n)
		}
		for _, l := range lines {
			m.f.Relay(m.t, objectSaid(from, ChatOwner, l))
		}
		if len(late) > 0 {
			time.AfterFunc(lateAfter, func() {
				for _, l := range late {
					m.f.Relay(m.t, objectSaid(from, ChatOwner, l))
				}
			})
		}
	case *msg.RequestTaskInventory:
		m.mu.Lock()
		m.reads++
		n := len(m.holds)
		m.mu.Unlock()
		if n == 0 {
			m.f.Relay(m.t, replyTaskInventory(lmRoot, ""))
			return
		}
		m.f.Relay(m.t, replyTaskInventory(lmRoot, "inventory_5b1d.tmp"))
	case *msg.RequestXfer:
		m.mu.Lock()
		file := m.taskFile()
		m.mu.Unlock()
		m.f.Relay(m.t, xferPacket(q.XferID.ID, 0, true, []byte(file)))
	case *msg.RemoveTaskInventory:
		m.mu.Lock()
		m.removes++
		if !m.stuck {
			m.holds = nil
		}
		m.mu.Unlock()
	}
}

// taskFile is the object's contents as the simulator writes them.
func (m *mapWorld) taskFile() string {
	var b strings.Builder
	b.WriteString("\tinv_object\t0\n\t{\n\t\tobj_id\t00000000-0000-0000-0000-000000000000\n\t\ttype\tcategory\n\t\tname\tContents|\n\t}\n")
	for i, name := range m.holds {
		fmt.Fprintf(&b, "\tinv_item\t0\n\t{\n\t\titem_id\t%s\n\t\tparent_id\t%s\n", itemID(i), lmRoot)
		b.WriteString("\t\tpermissions 0\n\t\t{\n\t\t\tbase_mask\t7fffffff\n\t\t\towner_mask\t7fffffff\n\t\t}\n")
		fmt.Fprintf(&b, "\t\tasset_id\t%s\n\t\ttype\tlsltext\n\t\tinv_type\tlsltext\n\t\tname\t%s|\n\t}\n", lmAsset, name)
	}
	return b.String()
}

func itemID(i int) msg.UUID {
	u := lmCopy
	u[15] = byte(i)
	return u
}

func (m *mapWorld) sentOfRez() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.drops)
}

// shortLinkMapWaits shortens the waits that would otherwise take their defaults.
func shortLinkMapWaits(t *testing.T) {
	t.Helper()
	was := linkMapGone
	linkMapGone = 600 * time.Millisecond
	t.Cleanup(func() { linkMapGone = was })
}

func TestLinkMapDropsTheScriptAndReadsEveryLink(t *testing.T) {
	m := newMapWorld(t)
	// Another object talking on owner chat, and the root saying something
	// in open chat, are not the script.
	m.noise = []*msg.ChatFromSimulator{
		objectSaid(lmOther, ChatOwner, "LINKMAP 1 "+lmOther.String()+" not ours"),
		objectSaid(lmRoot, ChatSay, "LINKMAP 1 "+lmOther.String()+" not on owner chat"),
		objectSaid(lmRoot, ChatOwner, "hello from the crate"),
	}

	got, err := m.w.LinkMap(context.Background(), m.o)
	if err != nil {
		t.Fatalf("LinkMap: %v", err)
	}
	if got.Root != lmRoot || len(got.Links) != 6 || got.Seated != 0 {
		t.Fatalf("map %+v", got)
	}
	wantKeys := []msg.UUID{lmRoot, lmLid, lmHinge, lmLatch, lmFoot, lmKnob}
	for i, k := range wantKeys {
		if e := got.Links[i]; e.Number != i+1 || e.Key != k {
			t.Errorf("entry %d: %+v", i, e)
		}
	}
	if got.Links[2].Name != "left hinge" || got.Links[5].Name != "" {
		t.Errorf("names %q and %q", got.Links[2].Name, got.Links[5].Name)
	}
	// The store knew four of the six, and its local ids are given for those.
	if n, ok := got.NumberOf(lmHinge); !ok || n != 3 {
		t.Errorf("hinge is %d, %v", n, ok)
	}
	if n, ok := got.NumberOfLocal(44); !ok || n != 4 {
		t.Errorf("local 44 is %d, %v", n, ok)
	}
	if got.Links[4].Local != 0 || got.Links[3].Local != 44 {
		t.Errorf("locals %d and %d", got.Links[3].Local, got.Links[4].Local)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("warnings %v", got.Warnings)
	}

	// One drop, into the root, running, of the item the avatar keeps.
	if len(m.drops) != 1 {
		t.Fatalf("%d drops", len(m.drops))
	}
	d := m.drops[0]
	if d.UpdateBlock.ObjectLocalID != 41 || !d.UpdateBlock.Enabled || d.InventoryBlock.ItemID != lmItem {
		t.Errorf("the drop was %+v", d)
	}
	// It was selected only to read its permissions, and let go.
	if n := len(sentOf[*msg.ObjectDeselect](m.f)); n != 1 {
		t.Errorf("%d deselects", n)
	}
	// Nothing was uploaded or written: the script was already here.
	if len(m.ais.changes()) != 0 || len(sentOf[*msg.RemoveTaskInventory](m.f)) != 0 {
		t.Errorf("something was changed: %v", m.ais.changes())
	}
}

// A map is whole when the reader holds the number of link lines the end
// says were sent: not after a time, and not in an order.
func TestLinkMapHoldsOnForExactlyTheLinesTheEndSaysWereSent(t *testing.T) {
	l := sixLines()
	t.Run("the end first, then every line", func(t *testing.T) {
		m := newMapWorld(t)
		m.say = append([]string{l[6]}, l[:6]...)
		got, err := m.w.LinkMap(context.Background(), m.o)
		if err != nil || len(got.Links) != 6 || got.Links[5].Key != lmKnob {
			t.Fatalf("LinkMap = %+v, %v", got, err)
		}
	})
	t.Run("the lines in a shuffled order, the end among them", func(t *testing.T) {
		m := newMapWorld(t)
		m.say = []string{l[4], l[1], l[6], l[5], l[0], l[3], l[2]}
		got, err := m.w.LinkMap(context.Background(), m.o)
		if err != nil || len(got.Links) != 6 {
			t.Fatalf("LinkMap = %+v, %v", got, err)
		}
		for i, e := range got.Links {
			if e.Number != i+1 {
				t.Errorf("entry %d is link %d", i, e.Number)
			}
		}
		if got.Links[0].Key != lmRoot || got.Links[5].Key != lmKnob {
			t.Errorf("map %+v", got)
		}
	})
	t.Run("a lone prim whose only line follows its end", func(t *testing.T) {
		m := newMapWorld(t)
		m.say = []string{"LINKMAP done 1 0", "LINKMAP 0 " + lmRoot.String() + " crate"}
		got, err := m.w.LinkMap(context.Background(), m.o)
		if err != nil || len(got.Links) != 1 || got.Links[0].Number != 0 {
			t.Fatalf("LinkMap = %+v, %v", got, err)
		}
	})
	t.Run("a line that comes long after, within the timeout", func(t *testing.T) {
		m := newMapWorld(t)
		m.w.SetOptions(Options{LinkMapTimeout: 20 * time.Second})
		// Far longer than any grace that was ever given, and the end
		// and five lines were heard well before it.
		m.say, m.late, m.lateAfter = l[:6], l[6:], 0
		m.say, m.late = append([]string{l[6]}, l[:5]...), l[5:6]
		m.lateAfter = 1500 * time.Millisecond
		start := time.Now()
		got, err := m.w.LinkMap(context.Background(), m.o)
		if err != nil || len(got.Links) != 6 || got.Links[5].Key != lmKnob {
			t.Fatalf("LinkMap = %+v, %v", got, err)
		}
		if d := time.Since(start); d < m.lateAfter {
			t.Errorf("finished after %s, before the last line was said", d)
		}
	})
	t.Run("a line that never comes is an error saying how many of how many", func(t *testing.T) {
		m := newMapWorld(t)
		shortLinkMapWaits(t)
		m.w.SetOptions(Options{LinkMapTimeout: 700 * time.Millisecond})
		m.say = append([]string{l[6]}, l[:5]...)
		start := time.Now()
		_, err := m.w.LinkMap(context.Background(), m.o)
		if !errors.Is(err, ErrTimeout) || !strings.Contains(err.Error(), "sent 6 link lines and 5 were heard") {
			t.Fatalf("LinkMap = %v", err)
		}
		if d := time.Since(start); d < 700*time.Millisecond {
			t.Errorf("gave up after %s, before the timeout", d)
		}
	})
	t.Run("more lines than the end said is not a map", func(t *testing.T) {
		m := newMapWorld(t)
		m.say = []string{"LINKMAP 1 " + lmRoot.String() + " crate", "LINKMAP 2 " + lmLid.String() + " lid",
			"LINKMAP 3 " + lmHinge.String() + " h", "LINKMAP done 2 0"}
		if _, err := m.w.LinkMap(context.Background(), m.o); err == nil || errors.Is(err, ErrTimeout) {
			t.Errorf("LinkMap = %v, want an error naming the fault", err)
		}
	})
}

func TestLinkMapOfALonePrimIsLinkZero(t *testing.T) {
	m := newMapWorld(t)
	m.say = []string{"LINKMAP 0 " + lmRoot.String() + " crate", "LINKMAP done 1 0"}
	got, err := m.w.LinkMap(context.Background(), m.o)
	if err != nil {
		t.Fatalf("LinkMap: %v", err)
	}
	if len(got.Links) != 1 || got.Links[0].Number != 0 || got.Links[0].Key != lmRoot {
		t.Errorf("map %+v", got)
	}
	if k := got.PrimKeys(); len(k) != 1 || k[0] != lmRoot {
		t.Errorf("keys %v", k)
	}
}

func TestLinkMapLeavesSeatedAvatarsOutAndCountsThem(t *testing.T) {
	m := newMapWorld(t)
	m.say = []string{
		"LINKMAP 1 " + lmRoot.String() + " crate",
		"LINKMAP 2 " + lmLid.String() + " lid",
		"LINKMAP done 2 1",
	}
	got, err := m.w.LinkMap(context.Background(), m.o)
	if err != nil {
		t.Fatalf("LinkMap: %v", err)
	}
	if len(got.Links) != 2 || got.Seated != 1 {
		t.Errorf("map %+v", got)
	}
}

func TestLinkMapOfAPrimWithOnlyASitterIsLinkOne(t *testing.T) {
	m := newMapWorld(t)
	m.say = []string{"LINKMAP 1 " + lmRoot.String() + " crate", "LINKMAP done 1 1"}
	got, err := m.w.LinkMap(context.Background(), m.o)
	if err != nil || len(got.Links) != 1 || got.Links[0].Number != 1 || got.Seated != 1 {
		t.Errorf("LinkMap = %+v, %v", got, err)
	}
}

func TestLinkMapIsRefusedBeforeAnythingIsDroppedWhereTheAvatarMayNotModify(t *testing.T) {
	for name, set := range map[string]func(*mapWorld){
		"an object it owns without the right": func(m *mapWorld) { m.ownerMask = 0x7fffbfff },
		"an object somebody else owns":        func(m *mapWorld) { m.owner = lmOther },
	} {
		t.Run(name, func(t *testing.T) {
			m := newMapWorld(t)
			set(m)
			_, err := m.w.LinkMap(context.Background(), m.o)
			if !errors.Is(err, ErrCannotModify) {
				t.Fatalf("LinkMap = %v, want ErrCannotModify", err)
			}
			if m.sentOfRez() != 0 || len(sentOf[*msg.RequestTaskInventory](m.f)) != 0 {
				t.Errorf("something was sent: %s", m.f.describe())
			}
			// The inventory was not asked either: no script was made or
			// looked for on behalf of an object that cannot take it.
			if len(m.ais.calls) != 0 {
				t.Errorf("inventory asked: %v", m.ais.calls)
			}
		})
	}
}

func TestLinkMapIsRefusedWhereTheRegionRunsNoScripts(t *testing.T) {
	m := newMapWorld(t)
	m.f.mu.Lock()
	m.f.region = &Region{Flags: agent.RegionSkipScripts}
	m.f.regionKnown = true
	m.f.mu.Unlock()
	_, err := m.w.LinkMap(context.Background(), m.o)
	if !errors.Is(err, ErrScriptsStopped) {
		t.Fatalf("LinkMap = %v, want ErrScriptsStopped", err)
	}
	if m.sentOfRez() != 0 {
		t.Error("the script was dropped into a region that runs none")
	}
}

func TestLinkMapGivesUpWhenTheScriptSaysNothing(t *testing.T) {
	m := newMapWorld(t)
	shortLinkMapWaits(t)
	m.w.SetOptions(Options{LinkMapTimeout: 300 * time.Millisecond})
	m.say = nil
	_, err := m.w.LinkMap(context.Background(), m.o)
	if !errors.Is(err, ErrTimeout) || !strings.Contains(err.Error(), "no line from the script") {
		t.Errorf("LinkMap = %v", err)
	}
}

func TestLinkMapSaysHowManyLinksWereHeardWhenTheEndNeverCame(t *testing.T) {
	m := newMapWorld(t)
	shortLinkMapWaits(t)
	m.w.SetOptions(Options{LinkMapTimeout: 300 * time.Millisecond})
	m.say = sixLines()[:3]
	_, err := m.w.LinkMap(context.Background(), m.o)
	if !errors.Is(err, ErrTimeout) || !strings.Contains(err.Error(), "3 links") {
		t.Errorf("LinkMap = %v", err)
	}
}

func TestLinkMapHearsTheRootOnOwnerChatOnly(t *testing.T) {
	for name, set := range map[string]func(*mapWorld){
		"another object's lines": func(m *mapWorld) { m.from = lmOther },
	} {
		t.Run(name, func(t *testing.T) {
			m := newMapWorld(t)
			shortLinkMapWaits(t)
			m.w.SetOptions(Options{LinkMapTimeout: 300 * time.Millisecond})
			set(m)
			_, err := m.w.LinkMap(context.Background(), m.o)
			if !errors.Is(err, ErrTimeout) {
				t.Errorf("LinkMap = %v, want a timeout: nothing from the root was heard", err)
			}
		})
	}
	// The root's own lines in open chat are not owner say either.
	m := newMapWorld(t)
	shortLinkMapWaits(t)
	m.w.SetOptions(Options{LinkMapTimeout: 300 * time.Millisecond})
	m.say = nil
	wait := aside(t, func() (LinkMap, error) { return m.w.LinkMap(context.Background(), m.o) })
	waitFor(t, "the drop", func() bool { return m.sentOfRez() == 1 })
	for _, l := range sixLines() {
		m.f.Relay(t, objectSaid(lmRoot, ChatSay, l))
	}
	if _, err := wait(); !errors.Is(err, ErrTimeout) {
		t.Errorf("LinkMap = %v, want a timeout: the lines were in open chat", err)
	}
}

func TestLinkMapRefusesAMapThatDoesNotAddUp(t *testing.T) {
	for name, say := range map[string][]string{
		"a link twice with two keys": append([]string{"LINKMAP 2 " + lmOther.String() + " x"},
			sixLines()...),
		"the root not first": append([]string{"LINKMAP 1 " + lmLid.String() + " lid"},
			"LINKMAP 2 "+lmRoot.String()+" crate", "LINKMAP 3 "+lmHinge.String()+" h",
			"LINKMAP done 3 0"),
	} {
		t.Run(name, func(t *testing.T) {
			m := newMapWorld(t)
			m.say = say
			if _, err := m.w.LinkMap(context.Background(), m.o); err == nil || errors.Is(err, ErrTimeout) {
				t.Errorf("LinkMap = %v, want an error naming the fault", err)
			}
		})
	}
}

func TestLinkMapTakesOutAScriptThatDidNotRemoveItself(t *testing.T) {
	m := newMapWorld(t)
	shortLinkMapWaits(t)
	m.leaves = true
	got, err := m.w.LinkMap(context.Background(), m.o)
	if err != nil {
		t.Fatalf("LinkMap: %v", err)
	}
	if m.removes != 1 {
		t.Errorf("%d removals", m.removes)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "did not remove itself") {
		t.Errorf("warnings %v", got.Warnings)
	}
	if len(got.Links) != 6 {
		t.Errorf("the map was lost with the clean-up: %+v", got)
	}
}

func TestLinkMapSaysWhenTheScriptCannotBeGotRidOf(t *testing.T) {
	m := newMapWorld(t)
	shortLinkMapWaits(t)
	m.stuck = true
	got, err := m.w.LinkMap(context.Background(), m.o)
	if err != nil {
		t.Fatalf("LinkMap: %v", err)
	}
	if len(got.Warnings) == 0 || !strings.Contains(strings.Join(got.Warnings, " "), "may still be in") {
		t.Errorf("warnings %v", got.Warnings)
	}
}

func TestLinkMapCleansUpAfterTheCallerGivesUp(t *testing.T) {
	m := newMapWorld(t)
	shortLinkMapWaits(t)
	m.leaves = true
	m.say = nil
	ctx, cancel := context.WithCancel(context.Background())
	wait := aside(t, func() (LinkMap, error) { return m.w.LinkMap(ctx, m.o) })
	waitFor(t, "the drop", func() bool { return m.sentOfRez() == 1 })
	cancel()
	if _, err := wait(); !errors.Is(err, context.Canceled) {
		t.Errorf("LinkMap = %v", err)
	}
	if m.removes != 1 {
		t.Errorf("the script was left in the object: %d removals", m.removes)
	}
}

func TestACopyOfTheScriptTheObjectNumberedIsOneOfOurs(t *testing.T) {
	for name, want := range map[string]bool{
		linkMapName: true, linkMapName + " 1": true, linkMapName + " 12": true,
		linkMapName + " x": false, linkMapName + "s": false, "other": false, "": false,
		linkMapName + " ": false,
	} {
		if got := isLinkMapCopy(name); got != want {
			t.Errorf("isLinkMapCopy(%q) = %v", name, got)
		}
	}
}

func TestLinkMapSavesOverACopyOfAnotherVersion(t *testing.T) {
	m := newMapWorld(t)
	m.item.Desc = "slgo linkmap v0"
	up := serveUpload(t, m.f, "UpdateScriptAgent", compiles)
	got, err := m.w.LinkMap(context.Background(), m.o)
	if err != nil {
		t.Fatalf("LinkMap: %v", err)
	}
	if len(got.Links) != 6 {
		t.Errorf("map %+v", got)
	}
	<-up.asked
	if src := string(<-up.body); src != linkMapSource {
		t.Errorf("the item was saved with %q", src)
	}
	ch := m.ais.changes()
	if len(ch) != 1 || !strings.Contains(string(ch[0].Body), linkMapVersion) || ch[0].Path != "/item/"+lmItem.String() {
		t.Errorf("the description was changed by %+v", ch)
	}
	if len(sentOf[*msg.CreateInventoryItem](m.f)) != 0 {
		t.Error("a second item was made")
	}
	if m.drops[0].InventoryBlock.ItemID != lmItem {
		t.Error("the drop was not of the item that was saved over")
	}
}

func TestLinkMapKeepsAScriptItHasSavedAndDoesNotSaveItAgain(t *testing.T) {
	m := newMapWorld(t)
	up := serveUpload(t, m.f, "UpdateScriptAgent", compiles)
	if _, err := m.w.LinkMap(context.Background(), m.o); err != nil {
		t.Fatal(err)
	}
	if _, err := m.w.LinkMap(context.Background(), m.o); err != nil {
		t.Fatal(err)
	}
	select {
	case <-up.asked:
		t.Error("a current script was saved again")
	default:
	}
	if m.sentOfRez() != 2 {
		t.Errorf("%d drops, want 2", m.sentOfRez())
	}
}

func TestLinkMapMakesTheScriptOnFirstUse(t *testing.T) {
	m := newMapWorld(t)
	m.mu.Lock()
	m.item = nil
	m.mu.Unlock()
	up := serveUpload(t, m.f, "UpdateScriptAgent", compiles)
	prev := m.f.onSend
	m.f.mu.Lock()
	m.f.onSend = func(x msg.Message) {
		if c, ok := x.(*msg.CreateInventoryItem); ok {
			m.mu.Lock()
			m.item = &Item{ID: lmItem, AssetID: lmAsset, Name: linkMapName, Desc: strings.TrimRight(string(c.InventoryBlock.Description), "\x00"),
				Type: int(AssetLSLText), InvType: int(AssetLSLText)}
			m.mu.Unlock()
			r := &msg.UpdateCreateInventoryItem{}
			r.InventoryData = append(r.InventoryData, msg.UpdateCreateInventoryItem_InventoryData{
				ItemID: lmItem, FolderID: lmDir, CallbackID: c.InventoryBlock.CallbackID,
				Type: c.InventoryBlock.Type, InvType: c.InventoryBlock.InvType,
				Name: c.InventoryBlock.Name, Description: c.InventoryBlock.Description,
			})
			m.f.Relay(t, r)
			return
		}
		prev(x)
	}
	m.f.mu.Unlock()

	got, err := m.w.LinkMap(context.Background(), m.o)
	if err != nil {
		t.Fatalf("LinkMap: %v", err)
	}
	if len(got.Links) != 6 {
		t.Errorf("map %+v", got)
	}
	mk := sentOf[*msg.CreateInventoryItem](m.f)
	if len(mk) != 1 || strings.TrimRight(string(mk[0].InventoryBlock.Name), "\x00") != linkMapName {
		t.Fatalf("created %+v", mk)
	}
	<-up.asked
	if src := string(<-up.body); src != linkMapSource {
		t.Errorf("saved %q", src)
	}
	if ch := m.ais.changes(); len(ch) != 1 || !strings.Contains(string(ch[0].Body), linkMapVersion) {
		t.Errorf("description set by %+v", ch)
	}
}

func TestLinkMapRefusesAScriptThatDoesNotCompile(t *testing.T) {
	m := newMapWorld(t)
	m.item.Desc = "slgo linkmap v0"
	serveUpload(t, m.f, "UpdateScriptAgent", func() (int, string) {
		return 200, `<llsd><map><key>state</key><string>complete</string>` +
			`<key>compiled</key><boolean>0</boolean>` +
			`<key>errors</key><array><string>(1,1) : ERROR : Syntax error</string></array></map></llsd>`
	})
	_, err := m.w.LinkMap(context.Background(), m.o)
	if err == nil || !strings.Contains(err.Error(), "did not compile") {
		t.Errorf("LinkMap = %v", err)
	}
	if m.sentOfRez() != 0 {
		t.Error("a script that did not compile was dropped")
	}
	if len(m.ais.changes()) != 0 {
		t.Error("the item was marked as this version though it did not compile")
	}
}

func TestLinkMapRefusesTwoItemsOfTheName(t *testing.T) {
	m := newMapWorld(t)
	dup := *m.item
	dup.ID = lmCopy
	m.ais.contents = func(id msg.UUID) ([]*Folder, []*Item) {
		if id == testInvRoot {
			return []*Folder{{ID: lmDir, ParentID: testInvRoot, Name: "Scripts", Type: 10}}, nil
		}
		return nil, []*Item{m.item, &dup}
	}
	_, err := m.w.LinkMap(context.Background(), m.o)
	if err == nil || !strings.Contains(err.Error(), "keep one") {
		t.Errorf("LinkMap = %v", err)
	}
	if m.sentOfRez() != 0 {
		t.Error("dropped one of two")
	}
}

func TestMayModifyIsTheViewersRule(t *testing.T) {
	me, grp, other := testAgentID, lmDir, lmOther
	for name, tc := range map[string]struct {
		p    Properties
		want bool
	}{
		"mine, owner may modify":         {Properties{Owner: me, OwnerMask: PermModify}, true},
		"mine, owner may not":            {Properties{Owner: me, OwnerMask: PermCopy | PermMove}, false},
		"mine, the owner's mask decides": {Properties{Owner: me, OwnerMask: PermCopy, EveryoneMask: PermModify}, false},
		"another's, group may, my group": {Properties{Owner: other, Group: grp, GroupMask: PermModify}, true},
		"another's, group may, not mine": {Properties{Owner: other, Group: other, GroupMask: PermModify}, false},
		"another's, everyone may":        {Properties{Owner: other, EveryoneMask: PermModify}, true},
		"another's, nobody may":          {Properties{Owner: other, OwnerMask: PermAll}, false},
	} {
		ok, why := mayModify(&tc.p, me, grp)
		if ok != tc.want || (!ok && why == "") {
			t.Errorf("%s: %v, %q", name, ok, why)
		}
	}
}

func TestParseLinkMapLine(t *testing.T) {
	k := lmLid.String()
	for _, tc := range []struct {
		text         string
		kind         linkMapLineKind
		number       int
		name         string
		total, seats int
		bad          bool
	}{
		{text: "LINKMAP 2 " + k + " left hinge", kind: lineLink, number: 2, name: "left hinge"},
		{text: "LINKMAP 0 " + k + " ", kind: lineLink, number: 0},
		{text: "LINKMAP 3 " + k, kind: lineLink, number: 3},
		{text: "LINKMAP done 6 0", kind: lineDone, total: 6},
		{text: "LINKMAP done 4 1", kind: lineDone, total: 4, seats: 1},
		{text: "hello", kind: lineOther},
		{text: "linkmap 2 " + k + " x", kind: lineOther},
		{text: "LINKMAP x " + k + " y", kind: lineOther, bad: true},
		{text: "LINKMAP 2 notakey y", kind: lineOther, bad: true},
		{text: "LINKMAP -1 " + k + " y", kind: lineOther, bad: true},
		{text: "LINKMAP done 2", kind: lineOther, bad: true},
		{text: "LINKMAP done 2 2", kind: lineDone, total: 2, seats: 2},
		{text: "LINKMAP done 0 0", kind: lineOther, bad: true},
		{text: "LINKMAP done 2 -1", kind: lineOther, bad: true},
	} {
		kind, e, total, seats, err := parseLinkMapLine(tc.text)
		if kind != tc.kind || (err != nil) != tc.bad || e.Number != tc.number || e.Name != tc.name || total != tc.total || seats != tc.seats {
			t.Errorf("%q: %v %+v %d %d, %v", tc.text, kind, e, total, seats, err)
		}
	}
}

func TestTheScriptSaysNothingButItsLinesAndRemovesItself(t *testing.T) {
	// What the script is made of is what the parser and the drop depend
	// on: owner say only, the lines' words, and the removal.
	for _, want := range []string{`llOwnerSay("LINKMAP "`, `llOwnerSay("LINKMAP done "`,
		"llRemoveInventory(llGetScriptName())", "llGetLinkKey(i)", "llGetLinkName(i)", "llGetAgentSize(k)", "++sent", `(string)sent + " " + (string)seated`} {
		if !strings.Contains(linkMapSource, want) {
			t.Errorf("the script lacks %s", want)
		}
	}
	for _, not := range []string{"llSay(", "llShout(", "llWhisper(", "llRegionSay", "llInstantMessage", "llSetText"} {
		if strings.Contains(linkMapSource, not) {
			t.Errorf("the script uses %s", not)
		}
	}
	if !strings.HasPrefix(linkMapSource, "// "+linkMapVersion[:len("slgo linkmap")]) || !strings.Contains(linkMapSource, strings.TrimPrefix(linkMapVersion, "slgo linkmap ")) {
		t.Error("the source does not carry its version")
	}
}

func TestConfirmLinkOrderGivesTheStoreTheMapsKeys(t *testing.T) {
	m := newMapWorld(t)
	m.f.confirmWith = &LinkConfirmation{Corrected: true, Moved: []int{3, 4}}
	got, err := m.w.LinkMap(context.Background(), m.o)
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.w.ConfirmLinkOrder(context.Background(), m.o, got)
	if err != nil || !c.Corrected || len(c.Moved) != 2 {
		t.Fatalf("ConfirmLinkOrder = %+v, %v", c, err)
	}
	if len(m.f.confirms) != 1 || m.f.confirms[0].Root != lmRoot || len(m.f.confirms[0].Keys) != 6 || m.f.confirms[0].Keys[2] != lmHinge {
		t.Errorf("the store was given %+v", m.f.confirms)
	}
	// A map of another object is not applied to this one.
	other := foundHere(m.w, &Object{ID: lmOther, Local: 9})
	if _, err := m.w.ConfirmLinkOrder(context.Background(), other, got); err == nil {
		t.Error("a map was applied to another object")
	}
	if len(m.f.confirms) != 1 {
		t.Error("the other object's call reached the store")
	}
	// What the store refuses comes back as it says it.
	m.f.confirmErr = ErrLinkSetDiffers
	if _, err := m.w.ConfirmLinkOrder(context.Background(), m.o, got); !errors.Is(err, ErrLinkSetDiffers) {
		t.Errorf("ConfirmLinkOrder = %v", err)
	}
}
