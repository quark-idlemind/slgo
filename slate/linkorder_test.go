package slate

// A link N on an object a linkmap header names is numbered by the object's
// own script at setup, and the transcript says what became of the store's
// reading.  Without the header nothing is sent to the object.
// Why: doc/slate-runner.md#link-order-from-the-objects-own-script

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// mapHdr is hdr with the vendor's link numbers asked of its own script.
const mapHdr = hdr + "linkmap vendor\n"

const (
	confirmedLine = "slate: link order of vendor confirmed by its own script (3 links)"
	storeLine     = "slate: link order of vendor is the store's best reading from the packets (no linkmap header for it)"
)

// linkMapStore is storeWorld with the tester's script in inventory and the
// drop answered: the vendor, a root of three prims that the tester owns.
func linkMapStore(t *testing.T, mayModify bool) (*fakeGrid, *linkMapWorld, *sl.Seen, *sl.Seen, *sl.Seen) {
	t.Helper()
	f, vendor, lid, slot := storeWorld(t)
	w := f.withDropping(t).withLinkMaps()
	w.own(vendor, mayModify)
	return f, w, vendor, lid, slot
}

func linkOrderLines(res *Result) []string {
	var out []string
	for _, l := range lines(res) {
		if strings.HasPrefix(l, "slate: link order of ") {
			out = append(out, l)
		}
	}
	return out
}

func TestAScriptThatAgreesConfirmsTheStoresOrderAtSetup(t *testing.T) {
	f, w, _, _, _ := linkMapStore(t, true)
	res := play(t, f, mapHdr+"touch vendor link 2\n")
	wantExit(t, res, 0)
	if got := linkOrderLines(res); !reflect.DeepEqual(got, []string{confirmedLine}) {
		t.Errorf("lines %q\n%s", got, res.Transcript)
	}
	if g := grabbed(f); len(g) != 1 || g[0] != 201 {
		t.Errorf("grabs %v, want the lid, 201", g)
	}
	// One drop, into the root, and nothing left in it.
	if d := w.dropped(); len(d) != 1 || d[0] != 102 {
		t.Errorf("drops into %v, want the vendor's root, 102", d)
	}
	if c := w.g.contents(102); len(c) != 0 {
		t.Errorf("the vendor holds %q", c)
	}
	if k := f.confirmedKeys(); len(k) != 1 || len(k[0]) != 3 || k[0][0] != idVendor {
		t.Errorf("the store was told %v", k)
	}
	// And before the steps: the line comes with the setup, not a test.
	if i, j := strings.Index(res.Transcript, confirmedLine), strings.Index(res.Transcript, "step 1"); i < 0 || j < 0 || i > j {
		t.Errorf("the line is not before the first step:\n%s", res.Transcript)
	}
}

func TestAScriptThatDisagreesCorrectsTheStoreAndSaysSo(t *testing.T) {
	f, w, vendor, lid, slot := linkMapStore(t, true)
	// The packets said the lid is link 2; the object says the slot is.
	w.numbered(vendor, slot, lid)
	res := play(t, f, mapHdr+"touch vendor link 2\n")
	wantExit(t, res, 0)
	want := "slate: link order of vendor corrected by its own script: the store had other prims at links 2 and 3 (3 links)"
	if got := linkOrderLines(res); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("lines %q\n%s", got, res.Transcript)
	}
	if g := grabbed(f); len(g) != 1 || g[0] != 202 {
		t.Errorf("grabs %v, want the slot, 202: the object's own link 2", g)
	}
}

func TestAScriptMakesASetTheStoreCouldNotNumberUsable(t *testing.T) {
	f, w, vendor, lid, slot := linkMapStore(t, true)
	f.unknownLinks(vendor, true)
	w.numbered(vendor, lid, slot)
	res := play(t, f, mapHdr+"touch vendor link 3\n")
	wantExit(t, res, 0)
	mustHave(t, res, confirmedLine)
	if g := grabbed(f); len(g) != 1 || g[0] != 202 {
		t.Errorf("grabs %v, want 202", g)
	}
}

func TestAnObjectTheTesterMayNotModifyThatALinkmapNamesIsASetupErrorAndNothingIsDropped(t *testing.T) {
	f, w, _, _, _ := linkMapStore(t, false)
	res, _ := tryPlay(t, f, mapHdr+"touch vendor link 2\n", Options{}, testCfg())
	wantExit(t, res, 3)
	mustHave(t, res, "slate: setup: linkmap vendor: the tester may not modify it, so no script can be dropped in it to say its links")
	if d := w.dropped(); len(d) != 0 {
		t.Errorf("a script was dropped into %v", d)
	}
	if n := len(sentOf[*msg.RezScript](f)); n != 0 {
		t.Errorf("%d RezScripts", n)
	}
	if k := f.confirmedKeys(); len(k) != 0 {
		t.Errorf("the store was told %v", k)
	}
}

func TestWithoutALinkmapNothingIsSentToTheObjectAndTheStoresReadingIsSaidOnce(t *testing.T) {
	f, w, _, _, _ := linkMapStore(t, true)
	res := play(t, f, hdr+"touch vendor link 2\ntouch vendor link 3\ntouch sign anywhere\n")
	wantExit(t, res, 0)
	if got := linkOrderLines(res); !reflect.DeepEqual(got, []string{storeLine}) {
		t.Errorf("lines %q\n%s", got, res.Transcript)
	}
	if d := w.dropped(); len(d) != 0 {
		t.Errorf("a script was dropped into %v", d)
	}
	if n := len(sentOf[*msg.RezScript](f)); n != 0 {
		t.Errorf("%d RezScripts", n)
	}
	// Not asked about its permissions either: that selects the object.
	if n := len(sentOf[*msg.ObjectSelect](f)); n != 0 {
		t.Errorf("%d ObjectSelects", n)
	}
	if k := f.confirmedKeys(); len(k) != 0 {
		t.Errorf("the store was told %v", k)
	}
	// A name the store cannot order stays refused, as the sentence says.
	f, _, vendor, _, _ := linkMapStore(t, true)
	f.unknownLinks(vendor, true)
	res = play(t, f, hdr+"touch vendor link 2\n")
	wantExit(t, res, 1)
	mustHave(t, res, storeLine, unknownOrder)
}

func TestAnObjectNobodyHasToldThePropertiesOfSaysItsScriptCouldNotBeHad(t *testing.T) {
	f, vendor := newGrid(t), (*sl.Seen)(nil)
	vendor = f.objects[1]
	f.objects = append(f.objects,
		child(prim(idLid, 201, "Example Lid", idStranger), vendor),
		child(prim(idSlot, 202, "Example Coin Slot", idStranger), vendor))
	res := play(t, f, mapHdr+"touch vendor link 2\n")
	wantExit(t, res, 0)
	got := linkOrderLines(res)
	if len(got) != 1 || !strings.HasPrefix(got[0], "slate: link order of vendor is the store's best reading from the packets (its own script could not be had: ") {
		t.Errorf("lines %q", got)
	}
}

func TestALonePrimIsLinkZeroAndSaysOneLink(t *testing.T) {
	f := newGrid(t)
	w := f.withDropping(t).withLinkMaps()
	w.own(f.objects[0], true)
	res := play(t, f, hdr+"linkmap sign\ntouch sign link 0\n")
	wantExit(t, res, 0)
	mustHave(t, res, "slate: link order of sign confirmed by its own script (1 link)")
	if k := f.confirmedKeys(); len(k) != 1 || len(k[0]) != 1 || k[0][0] != idSign {
		t.Errorf("the store was told %v", k)
	}
}

func TestOnlyObjectsAddressedWithLinkNAreAskedAndEachIsAskedOnce(t *testing.T) {
	f, w, _, _, _ := linkMapStore(t, true)
	w.own(f.objects[0], true) // the sign, which no step gives a link
	res := play(t, f, mapHdr+"touch sign anywhere\ntouch vendor link 2\ntouch vendor link 3\n")
	wantExit(t, res, 0)
	if got := linkOrderLines(res); !reflect.DeepEqual(got, []string{confirmedLine}) {
		t.Errorf("lines %q", got)
	}
	if d := w.dropped(); len(d) != 1 {
		t.Errorf("drops %v, want one", d)
	}
	// A file with no link N asks nobody.
	f, w, _, _, _ = linkMapStore(t, true)
	res = play(t, f, hdr+"touch vendor anywhere\n")
	wantExit(t, res, 0)
	if got := linkOrderLines(res); len(got) != 0 || len(w.dropped()) != 0 {
		t.Errorf("lines %q, drops %v", got, w.dropped())
	}
}

func TestAnObjectWithAProbeIsNotAskedItsProbeIsTheCount(t *testing.T) {
	f, o := world(t)
	w := f.withDropping(t).withLinkMaps()
	w.own(f.objects[1], true)
	res := o.play(t, probeHdr+"touch vendor link 2\n")
	wantExit(t, res, 0)
	if got := linkOrderLines(res); len(got) != 0 {
		t.Errorf("lines %q", got)
	}
	if d := w.dropped(); len(d) != 0 {
		t.Errorf("a script was dropped into %v beside the probe", d)
	}
}

func TestAScriptThatSaysNothingLeavesTheStoresReadingAndSaysWhy(t *testing.T) {
	f, w, _, _, _ := linkMapStore(t, true)
	w.silent = true
	res := play(t, f, mapHdr+"touch vendor link 2\n")
	wantExit(t, res, 0)
	got := linkOrderLines(res)
	if len(got) != 1 || !strings.Contains(got[0], "(its own script could not be had: ") || !strings.Contains(got[0], "no line from the script") {
		t.Errorf("lines %q", got)
	}
	if k := f.confirmedKeys(); len(k) != 0 {
		t.Errorf("the store was told %v", k)
	}
}

func TestAStoreThatHasNotSeenEveryPrimIsAskedAgainUntilItHas(t *testing.T) {
	f, _, _, _, _ := linkMapStore(t, true)
	f.confirmRefuse = 2
	res := play(t, f, mapHdr+"touch vendor link 2\n")
	wantExit(t, res, 0)
	mustHave(t, res, confirmedLine)
	if k := f.confirmedKeys(); len(k) != 3 {
		t.Errorf("the store was asked %d times, want 3", len(k))
	}

	// And one that never has is a line, not a failed run.
	f, _, _, _, _ = linkMapStore(t, true)
	f.confirmRefuse = 1 << 20
	cfg := testCfg()
	cfg.linkConfirm = 100 * time.Millisecond
	res = playWith(t, f, mapHdr+"touch vendor link 2\n", Options{}, cfg)
	wantExit(t, res, 0)
	got := linkOrderLines(res)
	if len(got) != 1 || !strings.Contains(got[0], "its own script gave an order the store could not take: ") {
		t.Errorf("lines %q", got)
	}
}

func TestAWornObjectAddressedWithLinkNIsNotAskedAndTheStoresReadingIsSaid(t *testing.T) {
	f := newGrid(t)
	d := f.withDropping(t).withLinkMaps()
	prevDrop := f.onSend
	f.withWearing(t)
	prevWear := f.onSend
	f.replyTo(func(m msg.Message) { prevDrop(m); prevWear(m) })
	root := idWornRoot
	root[15] = 1
	f.setProps(root, "Example Hat", "", testMe)
	f.setOwnerMaskOf(root, permAll)

	res := play(t, f, wearHdr+"wear hat on \"HUD centre 2\" as hud\ntouch hud link 2\n")
	wantExit(t, res, 0)
	// Nothing can name a binding a step makes in a linkmap header, so
	// nothing is dropped into a worn object, and the line says once why.
	if got := linkOrderLines(res); !reflect.DeepEqual(got, []string{"slate: link order of hud is the store's best reading from the packets (no linkmap header for it)"}) {
		t.Errorf("lines %q\n%s", got, res.Transcript)
	}
	if dr := d.dropped(); len(dr) != 0 {
		t.Errorf("drops %v, want none", dr)
	}
	if g := grabbed(f); len(g) != 1 || g[0] != 311 {
		t.Errorf("grabs %v, want the band, 311", g)
	}
}

func TestAWornObjectNoStepGivesALinkIsNotAsked(t *testing.T) {
	f := newGrid(t)
	d := f.withDropping(t).withLinkMaps()
	prevDrop := f.onSend
	f.withWearing(t)
	prevWear := f.onSend
	f.replyTo(func(m msg.Message) { prevDrop(m); prevWear(m) })
	res := play(t, f, wearHdr+"wear hat on \"HUD centre 2\" as hud\ntouch hud anywhere\n")
	wantExit(t, res, 0)
	if got := linkOrderLines(res); len(got) != 0 || len(d.dropped()) != 0 {
		t.Errorf("lines %q, drops %v", got, d.dropped())
	}
}

func TestLinkUsersAreTheNamesAStepAddressesWithLinkN(t *testing.T) {
	s := mustCheck(t, hdr+`
before each { touch sign link 1 }
test "t" {
  touch vendor link 2 face 1
  drag sign link 3 face 0 from 0.1 0.5 to 0.9 0.5
  say "ping" on 0
  expect say "pong" on public from object vendor link 4 within 300ms
  expect texture vendor link 2 face 0 is `+idTexA.String()+` within 300ms
  touch vendor anywhere
}
`)
	got := linkUsers(s)
	if !reflect.DeepEqual(got, map[string]bool{"sign": true, "vendor": true}) {
		t.Errorf("users %v", got)
	}
	s = mustCheck(t, hdr+"touch sign anywhere\ntouch vendor face 1\n")
	if got := linkUsers(s); len(got) != 0 {
		t.Errorf("users %v, want none", got)
	}
}

func TestALinkmapHeaderIsCheckedStatically(t *testing.T) {
	use := "touch vendor link 2\n"
	// Good: the object is a header object, a step addresses it with link
	// N, and it has no probe, whichever side of the use the header is on.
	mustCheck(t, hdr+"linkmap vendor\n"+use)
	mustCheck(t, "slate 1\nlinkmap vendor\nobject sign is \"Example Sign\"\nobject vendor is \"Example Tip Jar\"\n"+use)
	// An object used inside a test, a before and an after, counts.
	mustCheck(t, hdr+"linkmap vendor\ntest \"t\" { touch sign anywhere }\nbefore each { touch vendor link 1 }\n")

	checkErr(t, hdr+"linkmap nobody\n"+use, "nobody is not an object; linkmap names a header object")
	checkErr(t, hdr+"item hat is \"Example Hat\" in \"Objects\"\nlinkmap hat\n"+use, "hat is not an object")
	checkErr(t, hdr+"linkmap vendor\nlinkmap vendor\n"+use, "vendor already has a linkmap")
	checkErr(t, hdr+"linkmap vendor\n"+"touch sign anywhere\n", "no step addresses vendor with link N")
	// A name a step binds is not a header object, so a linkmap cannot name
	// it: no drop into a worn or rezzed object.
	checkErr(t, wearHdr+"linkmap hud\nwear hat on \"HUD centre 2\" as hud\ntouch hud link 2\n", "hud is not an object")
}

func TestALinkmapAndAProbeOnOneObjectIsRefusedAsRedundant(t *testing.T) {
	want := "vendor has a probe, whose hello already gives every link number"
	checkErr(t, hdr+"probe vendor\nlinkmap vendor\ntouch vendor link 2\n", want)
	checkErr(t, hdr+"linkmap vendor\nprobe vendor\ntouch vendor link 2\n", want)
	// Another object's probe is no reason.
	mustCheck(t, hdr+"probe sign\nlinkmap vendor\ntouch vendor link 2\n")
}

func TestLinkmapIsAWordOnlyWhereAHeaderIsExpected(t *testing.T) {
	s := mustCheck(t, "slate 1\nobject linkmap is \"Example Tip Jar\"\nlinkmap linkmap\ntouch linkmap link 2\n")
	if len(s.LinkMaps) != 1 || s.LinkMaps[0].Name.Text != "linkmap" || len(s.Objects) != 1 {
		t.Errorf("parsed %+v", s.LinkMaps)
	}
}
