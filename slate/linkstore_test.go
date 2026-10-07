package slate

// A link N on a binding with no probe is the prim the object store
// numbers N, found when it is used.
// Why: doc/slate-runner.md#linksets-and-region-positions

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

const (
	unknownOrder = `slate: step 1: the link order of "Example Tip Jar" is not known; bind the prim by its own name instead of link N, give the object a probe (the tester must own it), or take it and rez or wear it again`
	noLink9      = `slate: step 1: "Example Tip Jar" has no link 9; it has 3 prims`
)

// storeWorld is the grid of hdr with the vendor a root of three prims, as
// world makes it, and no probe or bridge: the store is the only map.
func storeWorld(t *testing.T) (f *fakeGrid, vendor, lid, slot *sl.Seen) {
	t.Helper()
	f = newGrid(t)
	vendor = f.objects[1]
	lid = child(prim(idLid, 201, "Example Lid", idStranger), vendor)
	slot = child(prim(idSlot, 202, "Example Coin Slot", idStranger), vendor)
	f.objects = append(f.objects, lid, slot)
	return f, vendor, lid, slot
}

// onGrab runs fn when the prim with a local id is touched.
func (f *fakeGrid) onGrab(local uint32, fn func()) {
	f.replyTo(func(m msg.Message) {
		if g, ok := m.(*msg.ObjectGrab); ok && g.ObjectData.LocalID == local {
			fn()
		}
	})
}

func grabbed(f *fakeGrid) []uint32 {
	var out []uint32
	for _, g := range sentOf[*msg.ObjectGrab](f) {
		out = append(out, g.ObjectData.LocalID)
	}
	return out
}

func TestTouchOnALinkWithNoProbeIsResolvedFromTheStore(t *testing.T) {
	for _, c := range []struct {
		step  string
		local uint32
	}{
		{"touch vendor link 1", 102},
		{"touch vendor link 2 face 1", 201},
		{"touch vendor link 3", 202},
		{"drag vendor link 3 face 0 from 0.1 0.5 to 0.9 0.5 over 100ms", 202},
	} {
		f, _, _, _ := storeWorld(t)
		res := play(t, f, hdr+c.step+"\n")
		wantExit(t, res, 0)
		if g := grabbed(f); len(g) != 1 || g[0] != c.local {
			t.Errorf("%s: grabs %v, want %d", c.step, g, c.local)
		}
	}
	// A set of one is link 0.
	f := newGrid(t)
	wantExit(t, play(t, f, hdr+"touch sign link 0\n"), 0)
	if g := grabbed(f); len(g) != 1 || g[0] != 101 {
		t.Errorf("grabs %v", g)
	}
}

func TestATextureReadingOnALinkWithNoProbeIsResolvedFromTheStore(t *testing.T) {
	f, _, _, _ := storeWorld(t)
	f.onGrab(201, func() { f.change(201, withTexture(0, idTexA)) })
	res := play(t, f, hdr+"touch vendor link 2\nexpect texture vendor link 2 face 0 is "+idTexA.String()+" within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "texture vendor link 2 face 0 "+idTexA.String())
	// Link 3 did not change.
	f, _, _, _ = storeWorld(t)
	f.onGrab(201, func() { f.change(201, withTexture(0, idTexA)) })
	wantExit(t, play(t, f, hdr+"touch vendor link 2\nexpect texture vendor link 3 face 0 is "+idTexA.String()+" within 300ms\n"), 1)
}

func TestASpeakerOnALinkWithNoProbeIsResolvedFromTheStore(t *testing.T) {
	for _, c := range []struct {
		link string
		pass bool
	}{{"link 2", true}, {"link 3", false}, {"link 1", false}} {
		f, _, _, _ := storeWorld(t)
		f.on("ping", chatMsg("Example Lid", idLid, sl.ChatSay, "pong"))
		res := play(t, f, hdr+"say \"ping\" on 0\nexpect say \"pong\" on public from object vendor "+c.link+" within 300ms\n")
		if got := res.Exit == 0; got != c.pass {
			t.Errorf("%s: passed = %v\n%s", c.link, got, res.Transcript)
		}
	}
}

func TestADialogOnALinkWithNoProbeIsResolvedFromTheStore(t *testing.T) {
	for _, c := range []struct {
		link string
		pass bool
	}{{"link 2", true}, {"link 3", false}} {
		f, _, _, _ := storeWorld(t)
		f.on("go", dialogMsg(idLid, "Example Lid", "pick", -5, "A", "B"))
		res := play(t, f, hdr+"say \"go\" on 0\nexpect dialog from vendor "+c.link+" text \"pick\" button \"A\" within 300ms\n")
		if got := res.Exit == 0; got != c.pass {
			t.Errorf("%s: passed = %v\n%s", c.link, got, res.Transcript)
		}
	}
}

func TestAWornLinksetWithNoProbeIsResolvedFromTheStore(t *testing.T) {
	f := newGrid(t)
	f.withHud()
	res := play(t, f, hudHdr+"touch hud link 3\n")
	wantExit(t, res, 0)
	if g := grabbed(f); len(g) != 1 || g[0] != 203 {
		t.Errorf("grabs %v, want 203", g)
	}
	f = newGrid(t)
	f.withHud()
	f.unknownLinks(f.objects[3], true)
	res = play(t, f, hudHdr+"touch hud link 3\n")
	wantExit(t, res, 1)
	mustHave(t, res, `slate: step 1: the link order of "Example HUD" is not known; bind the prim by its own name instead of link N, give the object a probe (the tester must own it), or take it and rez or wear it again`)
}

func TestASetWhoseOrderIsUnknownFailsEachLinkWithTheSentence(t *testing.T) {
	for _, c := range []struct{ name, src string }{
		{"touch", "touch vendor link 2\n"},
		{"drag", "drag vendor link 2 face 0 from 0.1 0.5 to 0.9 0.5\n"},
		{"button", "touch vendor link 2 button box\n"},
	} {
		f, vendor, _, _ := storeWorld(t)
		f.unknownLinks(vendor, true)
		res := play(t, f, hdr+c.src)
		wantExit(t, res, 1)
		mustHave(t, res, unknownOrder)
		if g := grabbed(f); len(g) != 0 {
			t.Errorf("%s: sent grabs %v", c.name, g)
		}
	}
	for _, c := range []struct{ name, src, trigger string }{
		{"texture", "expect texture vendor link 2 face 0 is " + idTexA.String() + " within 300ms\n", ""},
		{"button", "expect button vendor link 2 text \"x\" is shown within 300ms\n", ""},
		{"speaker", "say \"ping\" on 0\nexpect say \"pong\" on public from object vendor link 2 within 300ms\n", "ping"},
		{"dialog", "say \"ping\" on 0\nexpect dialog from vendor link 2 text \"pick\" within 300ms\n", "ping"},
	} {
		f, vendor, _, _ := storeWorld(t)
		f.unknownLinks(vendor, true)
		if c.trigger != "" {
			f.on("ping", chatMsg("Example Lid", idLid, sl.ChatSay, "pong"), dialogMsg(idLid, "Example Lid", "pick", -5, "A"))
		}
		res := play(t, f, hdr+c.src)
		wantExit(t, res, 1)
		// The sentence follows the unmatched line, as a button reading's reason does.
		found := false
		for _, l := range lines(res) {
			if strings.HasPrefix(l, "    unmatched ") && strings.HasSuffix(l, "; "+unknownOrder) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no unmatched line ends with the sentence\n%s", c.name, res.Transcript)
		}
	}
}

func TestALinkTheSetDoesNotHaveIsSaid(t *testing.T) {
	f, _, _, _ := storeWorld(t)
	res := play(t, f, hdr+"touch vendor link 9\n")
	wantExit(t, res, 1)
	mustHave(t, res, noLink9)
	f, _, _, _ = storeWorld(t)
	res = play(t, f, hdr+"expect texture vendor link 9 face 0 is "+idTexA.String()+" within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "; "+noLink9)
	// Link 0 is the root of a set of one only.
	f, _, _, _ = storeWorld(t)
	mustHave(t, play(t, f, hdr+"touch vendor link 0\n"), `"Example Tip Jar" has no link 0; it has 3 prims`)
}

func TestAProbeOverridesTheStore(t *testing.T) {
	// The store numbers the slot link 2 and the lid link 3. The probes in
	// the prims say the other way round, and a probe is the script's own
	// truth.
	f, o := world(t)
	f.setLinkOrder(f.objects[1], f.objects[4], f.objects[3])
	res := o.play(t, probeHdr+"touch vendor link 2\n")
	wantExit(t, res, 0)
	if g := grabbed(f); len(g) != 1 || g[0] != 201 {
		t.Errorf("grabs %v, want 201 (the probe's link 2)", g)
	}
	// And without a probe the same grid answers the store's way.
	f, _, lid, slot := storeWorld(t)
	f.setLinkOrder(f.objects[1], slot, lid)
	wantExit(t, play(t, f, hdr+"touch vendor link 2\n"), 0)
	if g := grabbed(f); len(g) != 1 || g[0] != 202 {
		t.Errorf("grabs %v, want 202 (the store's link 2)", g)
	}
	// A set the store cannot order is no matter when there is a probe.
	f, o = world(t)
	f.unknownLinks(f.objects[1], true)
	wantExit(t, o.play(t, probeHdr+"touch vendor link 3\n"), 0)
	if g := grabbed(f); len(g) != 1 || g[0] != 202 {
		t.Errorf("grabs %v, want 202", g)
	}
}

func TestARelinkDuringATestIsFollowed(t *testing.T) {
	f, vendor, lid, slot := storeWorld(t)
	// The slot, not the lid, is link 2 from 300 ms in, and it is the slot
	// that shows the texture.
	timer := time.AfterFunc(300*time.Millisecond, func() {
		f.setLinkOrder(vendor, slot, lid)
		f.change(202, withTexture(0, idTexA))
	})
	t.Cleanup(func() { timer.Stop() })
	res := play(t, f, hdr+"expect texture vendor link 2 face 0 is "+idTexA.String()+" within 2s\n")
	wantExit(t, res, 0)
}

func TestARelinkBetweenStepsMovesATouch(t *testing.T) {
	f, vendor, lid, slot := storeWorld(t)
	f.onGrab(201, func() { f.setLinkOrder(vendor, slot, lid) })
	wantExit(t, play(t, f, hdr+"touch vendor link 2\ntouch vendor link 2\n"), 0)
	if g := grabbed(f); len(g) != 2 || g[0] != 201 || g[1] != 202 {
		t.Errorf("grabs %v, want 201 then 202", g)
	}
}
