package slate

import (
	"fmt"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// probeHdr declares the vendor with a probe and a listen on 7.
const probeHdr = hdr + "probe vendor\nlisten 7\n"

// world is the grid of hdr with the vendor made a three-prim linkset, its
// root link 1 and its children, Example Lid and Example Coin Slot, links
// 2 and 3, and the simulated bridge and probes.
func world(t *testing.T) (*fakeGrid, *fakeOps) {
	t.Helper()
	f := newGrid(t)
	f.objects = append(f.objects,
		child(prim(idLid, 201, "Example Lid", idStranger), f.objects[1]),
		child(prim(idSlot, 202, "Example Coin Slot", idStranger), f.objects[1]))
	return f, newOps(f)
}

func (o *fakeOps) play(t *testing.T, src string) *Result {
	t.Helper()
	return playWith(t, o.f, src, Options{}, o.cfg())
}

func (o *fakeOps) try(t *testing.T, src string) *Result {
	t.Helper()
	res, _ := tryPlay(t, o.f, src, Options{}, o.cfg())
	return res
}

// inOrder fails unless each of want is in calls, one after another.
func inOrder(t *testing.T, calls []string, want ...string) {
	t.Helper()
	at := 0
	for _, w := range want {
		i := slices.Index(calls[at:], w)
		if i < 0 {
			t.Errorf("%q is not in the calls after %d: %q", w, at, calls)
			return
		}
		at += i + 1
	}
}

func countOf(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

const sendHi = "send on vendor from link 1 to link 2 num 7 text \"hi\"\nexpect link on vendor from link 1 num 7 text \"hi\" within 1s\n"

func TestTheBringUpRunsInOrderAndTheMapIsPrinted(t *testing.T) {
	f, o := world(t)
	res := o.play(t, probeHdr+sendHi)
	wantExit(t, res, 0)
	inOrder(t, o.calls(),
		"blocked? Example Tip Jar", "blocked? Example Lid", "blocked? Example Coin Slot",
		"find slate bridge", fmt.Sprintf("wear %d", sl.HUDCenter2|sl.AttachAdd), "install slate bridge in slate bridge",
		"install slate probe in Example Tip Jar", "install slate probe in Example Lid", "install slate probe in Example Coin Slot",
		"remove probe from Example Tip Jar", "remove probe from Example Lid", "remove probe from Example Coin Slot",
		"remove bridge from slate bridge", "take off")
	mustHave(t, res,
		"slate: setup: installing the probe in 3 prims",
		"slate: probe vendor link 1 "+idVendor.String(),
		"slate: probe vendor link 2 "+idLid.String(),
		"slate: probe vendor link 3 "+idSlot.String(),
		"slate: cleanup: removed the probe from 3 prims, settling 6s after each",
		`probe link vendor heard-by 2 from 1 num 7 "hi"`, "slate: passed 1 tests")
	// The installing line is before the first hello line, and the cleanup
	// line after the last test.
	var order []string
	for _, l := range lines(res) {
		for _, k := range []string{"installing the probe", "slate: probe vendor link 1", "slate: test", "slate: passed", "slate: cleanup: removed"} {
			if strings.Contains(l, k) {
				order = append(order, k)
			}
		}
	}
	if want := "installing the probe,slate: probe vendor link 1,slate: test,slate: cleanup: removed,slate: passed"; strings.Join(order, ",") != want {
		t.Errorf("order %q", order)
	}
	// Each probe is acked once, to its own link, through the bridge.
	var acks []string
	for _, s := range f.said() {
		if strings.Contains(s, " ack ") {
			acks = append(acks, s[strings.LastIndex(s, " ")+1:])
		}
	}
	if strings.Join(acks, "") != "123" {
		t.Errorf("acks to links %q", acks)
	}
	if n := countOf(o.calls(), "install slate bridge"); n != 1 {
		t.Errorf("%d bridge installs", n)
	}
}

func TestAFileWithNoProbeAndNoListenWearsNothing(t *testing.T) {
	_, o := world(t)
	wantExit(t, o.play(t, hdr+"say \"go\" on 0\nexpect no say \"x\" on public from anyone within 100ms\n"), 0)
	if c := o.calls(); len(c) != 0 {
		t.Errorf("calls %q", c)
	}
}

func TestAMissingBridgeItemFailsSetupNamingMakeBridge(t *testing.T) {
	_, o := world(t)
	o.items = 0
	res := o.try(t, probeHdr+sendHi)
	wantExit(t, res, 3)
	mustHave(t, res, `slate: setup: no "slate bridge" in inventory; run slate -make-bridge once where the tester may build`)
	if c := o.calls(); countOf(c, "wear") != 0 || countOf(c, "install") != 0 {
		t.Errorf("calls %q", c)
	}
	// Two of the name are PickNamed's refusal.
	_, o = world(t)
	o.items = 2
	res = o.try(t, probeHdr+sendHi)
	wantExit(t, res, 3)
	mustHave(t, res, `slate: setup: sl: 2 items in folder`, idBridgeItem.String(), idBridgeItem2.String())
	mustNotHave(t, res, "-make-bridge")
}

func TestScriptsBlockedFailsSetupBeforeAnythingIsWorn(t *testing.T) {
	_, o := world(t)
	o.blocked[idLid] = "this region is not running any scripts"
	res := o.try(t, probeHdr+sendHi)
	wantExit(t, res, 3)
	mustHave(t, res, "slate: setup: this region is not running any scripts")
	if c := o.calls(); countOf(c, "wear") != 0 || countOf(c, "find") != 0 {
		t.Errorf("calls %q", c)
	}
	mustNotHave(t, res, "slate: cleanup")
}

func TestABridgeAlreadyWornIsReusedAndStillTakenOff(t *testing.T) {
	_, o := world(t)
	o.wornAlready = true
	wantExit(t, o.play(t, probeHdr+sendHi), 0)
	c := o.calls()
	if countOf(c, "wear") != 0 {
		t.Errorf("worn again: %q", c)
	}
	inOrder(t, c, "install slate bridge in slate bridge", "remove bridge from slate bridge", "take off")
}

func TestABridgeThatNeverSaysReadyFailsSetupAndIsStillCleanedUp(t *testing.T) {
	_, o := world(t)
	o.silentBridge = true
	res := o.try(t, probeHdr+sendHi)
	wantExit(t, res, 3)
	mustHave(t, res, "slate: setup: bridge did not say ready within 300ms")
	c := o.calls()
	if countOf(c, "install slate probe") != 0 {
		t.Errorf("a probe went in before ready: %q", c)
	}
	inOrder(t, c, "remove bridge from slate bridge", "take off")
	mustNotHave(t, res, "removed the probe")
}

func TestAProbeThatDoesNotCompileOrInstallFailsSetupWithThePrimNamed(t *testing.T) {
	_, o := world(t)
	o.uncompiled[idLid] = true
	res := o.try(t, probeHdr+sendHi)
	wantExit(t, res, 3)
	mustHave(t, res, `slate: setup: cannot install the probe in "Example Lid": the script did not compile: (4, 10) : ERROR : Syntax error`)
	// The probes already in are removed, with the one that did not compile.
	c := o.calls()
	inOrder(t, c, "remove probe from Example Tip Jar", "remove probe from Example Lid", "remove bridge from slate bridge", "take off")
	if countOf(c, "remove probe") != 2 {
		t.Errorf("calls %q", c)
	}
	mustHave(t, res, "slate: cleanup: removed the probe from 2 prims, settling 6s after each")

	_, o = world(t)
	o.installErr[idSlot] = errNoSuch
	res = o.try(t, probeHdr+sendHi)
	wantExit(t, res, 3)
	mustHave(t, res, `slate: setup: cannot install the probe in "Example Coin Slot": the grid said no`)
}

func TestAProbeThatNeverHellosFailsSetupNamingItAndDoesNotClaimTheParcel(t *testing.T) {
	_, o := world(t)
	o.mute[idSlot] = true
	res := o.try(t, probeHdr+sendHi)
	wantExit(t, res, 3)
	mustHave(t, res, fmt.Sprintf(`slate: setup: probe in "Example Coin Slot" did not hello from %s within 300ms; ScriptsBlocked was empty and that is not proof the parcel will run the script (doc/ground.md)`, idSlot))
	mustNotHave(t, res, "slate: probe vendor link")
	inOrder(t, o.calls(), "remove probe from Example Coin Slot", "remove bridge from slate bridge", "take off")
}

func TestTwoPrimsThatHelloAsOneLinkFailSetup(t *testing.T) {
	_, o := world(t)
	o.linkOf[idSlot] = 2
	res := o.try(t, probeHdr+sendHi)
	wantExit(t, res, 3)
	mustHave(t, res, fmt.Sprintf(`slate: setup: two prims of "vendor" hello as link 2: %s and %s`, idLid, idSlot))
}

func TestAHelloThatNamesAnotherPrimIsIgnoredWithAWarning(t *testing.T) {
	// A forged or confused hello is not the prim's own, so it is not in the
	// map: the prim then fails setup as one that never helloed.
	_, o := world(t)
	o.helloKey[idLid] = idForeign
	res := o.try(t, probeHdr+sendHi)
	wantExit(t, res, 3)
	mustHave(t, res, fmt.Sprintf("slate: warning: ignored a hello from %s that names %s", idLid, idForeign),
		`slate: setup: probe in "Example Lid" did not hello`)
}

func TestAHelloFromAPrimNoProbeWasInstalledInIsIgnored(t *testing.T) {
	f, o := world(t)
	o.onInstall = func(name string) {
		if name == bridgeScript {
			o.emit(chatMsg("Example Stranger Box", idForeign, sl.ChatDirect,
				fmt.Sprintf("%s %s hello 9 %s", wireVersion, o.bridge.nonce, idForeign)))
		}
	}
	res := o.play(t, probeHdr+sendHi)
	wantExit(t, res, 0)
	mustNotHave(t, res, "link 9")
	mustHave(t, res, "slate: probe vendor link 3 "+idSlot.String())
	_ = f
}

// reports is a send, and the links that must hear it and must not.
func TestSendReachesOnlyTheScriptsTheTargetAddresses(t *testing.T) {
	for _, c := range []struct {
		from   int
		to     string
		heard  []int
		silent []int
	}{
		{1, "link 2", []int{2}, []int{1, 3}},
		{1, "link 3", []int{3}, []int{1, 2}},
		{1, "link children", []int{2, 3}, []int{1}},
		{1, "link others", []int{2, 3}, []int{1}},
		{2, "link others", []int{1, 3}, []int{2}},
		{2, "link this", []int{2}, []int{1, 3}},
		{3, "link root", []int{1}, []int{2, 3}},
		{2, "link all", []int{1, 2, 3}, nil},
	} {
		_, o := world(t)
		step := fmt.Sprintf("send on vendor from link %d to %s num 7 text \"x\"\n", c.from, c.to)
		for _, h := range c.heard {
			step += fmt.Sprintf("expect link on vendor from link %d num 7 text \"x\" heard by %d within 1s\n", c.from, h)
		}
		for _, h := range c.silent {
			step += fmt.Sprintf("expect no link on vendor from link %d num 7 text \"x\" heard by %d within 150ms\n", c.from, h)
		}
		res := o.play(t, probeHdr+step)
		if res.Exit != 0 {
			t.Errorf("%d to %s: exit %d\n%s", c.from, c.to, res.Exit, res.Transcript)
		}
	}
}

func TestAReportWithoutHeardByPassesOnTheFirstPrimToReportIt(t *testing.T) {
	_, o := world(t)
	// Three prims report an all; one expectation takes one, and the others
	// are not failures.
	res := o.play(t, probeHdr+"send on vendor from link 1 to link all num 7 text \"x\"\nexpect link on vendor from link 1 num 7 text \"x\" within 1s\n")
	wantExit(t, res, 0)
	// The root's own report does not pass an expectation of heard by 2.
	_, o = world(t)
	res = o.play(t, probeHdr+"send on vendor from link 1 to link 1 num 7 text \"x\"\nexpect link on vendor from link 1 num 7 text \"x\" heard by 2 within 200ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched link on vendor from link 1 num 7 text \"x\" heard by 2 within 200ms")
	// A different sender, num, key or text does not match.
	for _, exp := range []string{
		"from link 2 num 7 text \"x\"", "from link 1 num 8 text \"x\"", "from link 1 num 7 text \"y\"",
		"from link 1 num 7 text \"x\" key " + idKeyed.String(),
	} {
		_, o = world(t)
		wantExit(t, o.play(t, probeHdr+"send on vendor from link 1 to link all num 7 text \"x\"\nexpect link on vendor "+exp+" within 200ms\n"), 1)
	}
	// The key rides along: sent with one, reported with it, expected by it.
	_, o = world(t)
	wantExit(t, o.play(t, probeHdr+"send on vendor from link 1 to link 2 num 7 text \"x\" key "+idKeyed.String()+"\nexpect link on vendor from link 1 num 7 text \"x\" key "+idKeyed.String()+" within 1s\n"), 0)
}

func TestLinkTextMatchesAPatternAndSurvivesTabAndNewline(t *testing.T) {
	_, o := world(t)
	wantExit(t, o.play(t, probeHdr+"send on vendor from link 1 to link 2 num 7 text \"hello 42\"\nexpect link on vendor from link 1 num 7 text matching \"^hello [0-9]+$\" within 1s\n"), 0)
	_, o = world(t)
	wantExit(t, o.play(t, probeHdr+"send on vendor from link 1 to link 2 num 7 text \"hello 42\"\nexpect link on vendor from link 1 num 7 text matching \"^bye\" within 200ms\n"), 1)
	// A pattern is tested against the unquoted text, so a tab is a tab.
	_, o = world(t)
	wantExit(t, o.play(t, probeHdr+"send on vendor from link 1 to link 2 num 7 text \"a\\tb\\nc\"\nexpect link on vendor from link 1 num 7 text matching \"a\\tb\\nc\" within 1s\n"), 0)
	_, o = world(t)
	wantExit(t, o.play(t, probeHdr+"send on vendor from link 1 to link 2 num 7 text \"a\\tb\\nc\"\nexpect link on vendor from link 1 num 7 text \"a\\tb\\nc\" within 1s\n"), 0)
	// A line over the wire carries the escapes and no raw tab or newline.
	f, o := world(t)
	o.play(t, probeHdr+"send on vendor from link 1 to link 2 num 7 text \"a\\tb\\nc\\\\d\\\"e\"\n")
	for _, s := range f.said() {
		if strings.Contains(s, " send ") && (strings.ContainsAny(s, "\t\n") || !strings.HasSuffix(s, `"a\tb\nc\\d\"e"`)) {
			t.Errorf("the line is %q", s)
		}
	}
}

func TestABadReportAfterASendFailsTheStepWithTheSentence(t *testing.T) {
	_, o := world(t)
	o.rejectAll = true
	res := o.play(t, probeHdr+"send on vendor from link 2 to link 1 num 7 text \"x\"\nexpect link on vendor from link 2 num 7 text \"x\" within 1s\n")
	wantExit(t, res, 1)
	mustHave(t, res, "  failed: the probe rejected the command", "probe bad vendor link 2")
}

func TestASendFromALinkThatIsNotInTheMapFailsBeforeAnythingIsSaid(t *testing.T) {
	f, o := world(t)
	res := o.play(t, probeHdr+"send on vendor from link 5 to link 1 num 7 text \"x\"\nexpect link on vendor from link 5 num 7 text \"x\" within 200ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "not sent: link 5 is not one of the probed links of vendor (1 2 3)")
	for _, s := range f.said() {
		if strings.Contains(s, " send ") {
			t.Errorf("a command was spoken: %q", s)
		}
	}
}

func TestTouchAndDragOnALinkReachThatPrim(t *testing.T) {
	f, o := world(t)
	res := o.play(t, probeHdr+"touch vendor link 3\n")
	wantExit(t, res, 0)
	if g := sentOf[*msg.ObjectGrab](f); len(g) != 1 || g[0].ObjectData.LocalID != 202 {
		t.Errorf("grabs %+v", g)
	}

	f, o = world(t)
	wantExit(t, o.play(t, probeHdr+"touch vendor link 2 face 1 at 0.25 0.75\n"), 0)
	g := sentOf[*msg.ObjectGrab](f)
	if len(g) != 1 || g[0].ObjectData.LocalID != 201 || g[0].SurfaceInfo[0].FaceIndex != 1 {
		t.Errorf("grabs %+v", g)
	}

	f, o = world(t)
	wantExit(t, o.play(t, probeHdr+"touch vendor link 1\n"), 0)
	if g := sentOf[*msg.ObjectGrab](f); len(g) != 1 || g[0].ObjectData.LocalID != 102 {
		t.Errorf("grabs %+v", g)
	}

	f, o = world(t)
	wantExit(t, o.play(t, probeHdr+"drag vendor link 2 face 0 from 0.1 0.5 to 0.9 0.5 over 100ms\n"), 0)
	g = sentOf[*msg.ObjectGrab](f)
	if len(g) != 1 || g[0].ObjectData.LocalID != 201 || len(sentOf[*msg.ObjectDeGrab](f)) != 1 {
		t.Errorf("grabs %+v", g)
	}

	// A link the probe never reported fails before a touch is sent.
	f, o = world(t)
	res = o.play(t, probeHdr+"touch vendor link 4\n")
	wantExit(t, res, 1)
	mustHave(t, res, "not sent: link 4 is not one of the probed links of vendor (1 2 3)")
	if g := sentOf[*msg.ObjectGrab](f); len(g) != 0 {
		t.Errorf("grabs %+v", g)
	}
	f, o = world(t)
	wantExit(t, o.play(t, probeHdr+"drag vendor link 9 face 0 from 0.1 0.5 to 0.9 0.5\n"), 1)
	if g := sentOf[*msg.ObjectGrab](f); len(g) != 0 {
		t.Errorf("grabs %+v", g)
	}
}

func TestAButtonOnALinkIsSearchedAndClickedOnThatPrim(t *testing.T) {
	f, o := world(t)
	f.paint(t, f.objects[3], paintSpec{pics: map[int]image.Image{0: oneBox()}})
	res := o.play(t, probeHdr+"touch vendor link 2 button box\n")
	wantExit(t, res, 0)
	if g := sentOf[*msg.ObjectGrab](f); len(g) != 1 || g[0].ObjectData.LocalID != 201 {
		t.Errorf("grabs %+v", g)
	}
}

// pingPong has the product answer a say on 7 through the bridge.
func (o *fakeOps) answers(ch int32, speaker msg.UUID, name, text string) {
	o.extraSay = func(m msg.Message) {
		if s, _, ok := says(m); ok && s == "ping" {
			o.productSays(ch, speaker, name, text)
		}
	}
}

func TestAListenChannelIsMatchedFromTheRawTail(t *testing.T) {
	for _, c := range []struct {
		name, text string
		exp        string
		pass       bool
	}{
		{"Example Tip Jar", "pong", `expect say "pong" on 7 from object vendor`, true},
		{"Example Tip Jar", "pong", `expect say matching "^po" on 7 from object vendor`, true},
		{"Example Tip Jar", "pong", `expect say "ping" on 7 from object vendor`, false},
		// Not unescaped: the backslash and the n are two characters.
		{"Example Tip Jar", `a\nb "q"`, `expect say "a\\nb \"q\"" on 7 from object vendor`, true},
		// A name with a space and a quote is the speaker's, and the quote in
		// the tail is the product's.
		{`Example "East`, `say "hi"`, `expect say "say \"hi\"" on 7 from avatar "Example \"East"`, true},
		{`Example "East`, `say "hi"`, `expect say "say \"hi\"" on 7 from anyone`, true},
		// A line is never public chat for a speaker who is not the prim.
		{"Example Tip Jar", "pong", `expect say "pong" on 7 from object sign`, false},
		{"Example Tip Jar", "pong", `expect say "pong" on 7 from object vendor link 1`, true},
		{"Example Tip Jar", "pong", `expect say "pong" on 7 from object vendor link 2`, false},
		{"Example Tip Jar", "pong", `expect say "pong" on 7 from tester`, false},
		{"Example Tip Jar", "pong", `expect say "pong" on public from object vendor`, false},
	} {
		_, o := world(t)
		speaker := idVendor
		o.answers(7, speaker, c.name, c.text)
		res := o.play(t, probeHdr+"say \"ping\" on 7\n"+c.exp+" within 250ms\n")
		if got := res.Exit == 0; got != c.pass {
			t.Errorf("%s (%q from %q): passed = %v\n%s", c.exp, c.text, c.name, got, res.Transcript)
		}
	}
	// The line is printed as a forward, with the name as it came.
	_, o := world(t)
	o.answers(7, idSpeaker, `Example "East`, `say "hi"`)
	res := o.play(t, probeHdr+"say \"ping\" on 7\nexpect say \"say \\\"hi\\\"\" on 7 from anyone within 250ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, `chat channel 7 from [Object] Example "East: "say \"hi\""`)
	// The speaker is the child's key for a link.
	_, o = world(t)
	o.answers(7, idLid, "Example Lid", "pong")
	wantExit(t, o.play(t, probeHdr+"say \"ping\" on 7\nexpect say \"pong\" on 7 from object vendor link 2 within 250ms\n"), 0)
	// A channel the bridge does not listen on is not forwarded.
	_, o = world(t)
	o.answers(8, idVendor, "Example Tip Jar", "pong")
	wantExit(t, o.play(t, probeHdr+"say \"ping\" on 7\nexpect no say \"pong\" on 7 from anyone within 150ms\n"), 0)
}

func TestFromObjectLinkMatchesOnlyThatPrimsPublicChat(t *testing.T) {
	f, o := world(t)
	f.on("go", chatMsg("Example Lid", idLid, sl.ChatSay, "hi"))
	o.extraSay = nil
	// f.on replaced the far end's handler; the sends here are not needed.
	res := playWith(t, f, probeHdr+"say \"go\" on 0\nexpect say \"hi\" on public from object vendor link 2 within 300ms\n", Options{}, o.cfg())
	wantExit(t, res, 0)
}

func TestADialogOrTextBoxFromALinkIsThatPrimsOnly(t *testing.T) {
	for _, c := range []struct {
		from msg.UUID
		name string
		exp  string
		pass bool
	}{
		{idLid, "Example Lid", `dialog from vendor link 2 text "pick" button "A"`, true},
		{idSlot, "Example Coin Slot", `dialog from vendor link 2 text "pick" button "A"`, false},
		{idSlot, "Example Coin Slot", `dialog from vendor text "pick" button "A"`, true},
		{idSlot, "Example Coin Slot", `dialog from vendor link 3 text "pick" button "A"`, true},
		// The prim's name is not enough: the object must be that prim.
		{idForeign, "Example Lid", `dialog from vendor link 2 text "pick" button "A"`, false},
	} {
		f, o := world(t)
		o.extraSay = func(m msg.Message) {
			if s, _, ok := says(m); ok && s == "go" {
				f.relay(dialogMsg(c.from, c.name, "pick", -5, "A"))
			}
		}
		res := o.play(t, probeHdr+"say \"go\" on 0\nexpect "+c.exp+" within 250ms\n")
		if got := res.Exit == 0; got != c.pass {
			t.Errorf("%s from %s: passed = %v\n%s", c.exp, c.name, got, res.Transcript)
		}
	}
	f, o := world(t)
	o.extraSay = func(m msg.Message) {
		if s, _, ok := says(m); ok && s == "go" {
			f.relay(dialogMsg(idLid, "Example Lid", "name?", -5, "!!llTextBox!!"))
		}
	}
	wantExit(t, o.play(t, probeHdr+"say \"go\" on 0\nexpect textbox from vendor link 2 text \"name?\" within 250ms\n"), 0)
	// A link that is not in the map fails before the step is sent.
	f, o = world(t)
	res := o.play(t, probeHdr+"say \"go\" on 0\nexpect dialog from vendor link 8 text \"pick\" button \"A\" within 100ms\n")
	wantExit(t, res, 1)
	if len(f.said()) != 3 { // the three acks only
		t.Errorf("said %q", f.said())
	}
}

func TestAStateExpectationOnALinkReadsThatPrim(t *testing.T) {
	f, o := world(t)
	o.extraSay = func(m msg.Message) {
		if g, ok := m.(*msg.ObjectGrab); ok && g.ObjectData.LocalID == 201 {
			f.change(201, withTexture(0, idTexA))
		}
	}
	res := o.play(t, probeHdr+"touch vendor link 2\nexpect texture vendor link 2 face 0 is "+idTexA.String()+" within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "texture vendor link 2 face 0 "+idTexA.String())
	// Link 3 did not change.
	f, o = world(t)
	o.extraSay = func(m msg.Message) {
		if g, ok := m.(*msg.ObjectGrab); ok && g.ObjectData.LocalID == 201 {
			f.change(201, withTexture(0, idTexA))
		}
	}
	res = o.play(t, probeHdr+"touch vendor link 2\nexpect texture vendor link 3 face 0 is "+idTexA.String()+" within 300ms\n")
	wantExit(t, res, 1)
	// And a negative one on a link that did not change passes.
	f, o = world(t)
	o.extraSay = func(m msg.Message) {
		if g, ok := m.(*msg.ObjectGrab); ok && g.ObjectData.LocalID == 201 {
			f.change(201, withTexture(0, idTexA))
		}
	}
	wantExit(t, o.play(t, probeHdr+"touch vendor link 2\nexpect no texture vendor link 3 face 0 changes within 200ms\nexpect texture vendor link 2 face 0 becomes "+idTexA.String()+" within 1s\n"), 0)
	// A link that is not in the map is refused when the step is made.
	f, o = world(t)
	res = o.play(t, probeHdr+"touch vendor link 1\nexpect texture vendor link 6 face 0 changes within 100ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "link 6 is not one of the probed links of vendor (1 2 3)")
}

func TestProtocolLinesAreNeverChat(t *testing.T) {
	// The probe's reports are ChatDirect and the bridge's lines ChatOwner.
	// None satisfies expect say on those channels, and none trips a negative.
	_, o := world(t)
	o.answers(7, idVendor, "Example Tip Jar", "pong")
	res := o.play(t, probeHdr+`send on vendor from link 1 to link 2 num 7 text "hi"
expect link on vendor from link 1 num 7 text "hi" within 1s
expect no say matching ".*" on direct from anyone within 200ms
`)
	wantExit(t, res, 0)
	mustNotHave(t, res, "chat direct")
	mustHave(t, res, `probe link vendor heard-by 2 from 1 num 7 "hi"`)

	// The bridge's forward is ChatOwner too, and is no owner chat.
	_, o = world(t)
	o.answers(7, idVendor, "Example Tip Jar", "pong")
	res = o.play(t, probeHdr+`say "ping" on 7
expect say "pong" on 7 from anyone within 1s
expect no say matching "." on owner from anyone within 200ms
`)
	wantExit(t, res, 0)
	mustNotHave(t, res, "chat owner")

	// And one that is expected positively is not found.
	_, o = world(t)
	res = o.play(t, probeHdr+"send on vendor from link 1 to link 2 num 7 text \"hi\"\nexpect say matching \"slprobe\" on direct from anyone within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, `probe link vendor heard-by 2`)

	// A chat line from a prim the runner did not install in, that only
	// looks like a report, is chat.
	_, o = world(t)
	o.extraSay = func(m msg.Message) {
		if s, _, ok := says(m); ok && s == "go" {
			o.emit(chatMsg("Example Stray", idForeign, sl.ChatDirect, fmt.Sprintf("%s %s bad", wireVersion, o.bridge.nonce)))
		}
	}
	wantExit(t, o.play(t, probeHdr+"say \"go\" on 0\nexpect say matching \"bad$\" on direct from anyone within 300ms\n"), 0)
}

func TestAMalformedProtocolLineIsWarnedAboutAndNeitherChatNorAReport(t *testing.T) {
	_, o := world(t)
	o.extraSay = func(m msg.Message) {
		if s, _, ok := says(m); ok && s == "go" {
			o.emit(chatMsg("Example Tip Jar", idVendor, sl.ChatDirect, fmt.Sprintf("%s %s link x", wireVersion, o.bridge.nonce)))
		}
	}
	res := o.play(t, probeHdr+"say \"go\" on 0\nexpect no say matching \"link\" on direct from anyone within 200ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, "slate: warning: ignored a protocol line from "+idVendor.String())
	mustNotHave(t, res, "chat direct")
}

func TestASitOnAProbedLinksetSaysWhichLinksAreNotProbed(t *testing.T) {
	f, o := world(t)
	o.extraSay = func(m msg.Message) {
		if _, ok := m.(*msg.AgentRequestSit); ok {
			f.relay(&msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{FullID: testMe, ID: 1, ParentID: 102}}})
		}
	}
	res := o.play(t, probeHdr+"sit vendor\n")
	wantExit(t, res, 0)
	mustHave(t, res, "slate: step 1: seated on vendor: link numbers at and above the seated avatar's are not probed")
	// An object with no probe has no such note.
	f, o = world(t)
	o.extraSay = func(m msg.Message) {
		if _, ok := m.(*msg.AgentRequestSit); ok {
			f.relay(&msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{FullID: testMe, ID: 1, ParentID: 101}}})
		}
	}
	res = o.play(t, probeHdr+"sit sign\n")
	mustNotHave(t, res, "not probed")
}

func TestCleanupRunsAfterAFailedTestAndAfterTheLast(t *testing.T) {
	_, o := world(t)
	res := o.play(t, probeHdr+`
test "one" {
  send on vendor from link 1 to link 2 num 7 text "x"
  expect link on vendor from link 1 num 7 text "nope" within 150ms
}
test "two" {
  send on vendor from link 1 to link 2 num 7 text "x"
  expect link on vendor from link 1 num 7 text "x" within 1s
}
`)
	wantExit(t, res, 1)
	c := o.calls()
	inOrder(t, c, "remove probe from Example Tip Jar", "remove probe from Example Lid", "remove probe from Example Coin Slot", "remove bridge from slate bridge", "take off")
	// Once, after both tests.
	if countOf(c, "take off") != 1 || countOf(c, "remove bridge") != 1 {
		t.Errorf("calls %q", c)
	}
	var cut, last int
	for i, l := range lines(res) {
		if strings.HasPrefix(l, "slate: cleanup: removed") {
			cut = i
		}
		if strings.HasPrefix(l, "slate: failed 1 of 2 tests") {
			last = i
		}
	}
	// Cleanup comes before the verdict, which is the last line.
	if cut == 0 || last == 0 || cut > last {
		t.Errorf("the cleanup line is at %d and the result at %d:\n%s", cut, last, res.Transcript)
	}
}

func TestACleanupErrorIsAWarningAndChangesNothingElse(t *testing.T) {
	_, o := world(t)
	o.removeErr = errNoSuch
	o.takeOffErr = errNoSuch
	res := o.play(t, probeHdr+sendHi)
	wantExit(t, res, 0)
	mustHave(t, res,
		`slate: cleanup: warning: the probe in "Example Tip Jar" was not removed: the grid said no`,
		"slate: cleanup: warning: the bridge script was not removed: the grid said no",
		"slate: cleanup: warning: the bridge was not taken off: the grid said no")
	// Every removal is tried, whatever the first said.
	if n := countOf(o.calls(), "remove probe"); n != 3 {
		t.Errorf("%d probe removals", n)
	}
	// An earlier failure stays what it was.
	_, o = world(t)
	o.removeErr = errNoSuch
	res = o.play(t, probeHdr+"send on vendor from link 1 to link 2 num 7 text \"x\"\nexpect link on vendor from link 1 num 7 text \"y\" within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "slate: failed 1 of 1 tests", "slate: cleanup: warning")
	// And a setup failure stays exit 3.
	_, o = world(t)
	o.silentBridge, o.takeOffErr = true, errNoSuch
	wantExit(t, o.try(t, probeHdr+sendHi), 3)
}

func TestTheBringUpWaitsAreShortInTheTestsAndTheDefaultsAreThirtySeconds(t *testing.T) {
	var c runCfg
	if c.readyFor() != 30*time.Second || c.helloFor() != 30*time.Second || c.wearFor() != 40*time.Second {
		t.Errorf("defaults %s %s %s", c.readyFor(), c.helloFor(), c.wearFor())
	}
}

func TestAListenWithNoProbeWearsTheBridgeAndInstallsNoProbe(t *testing.T) {
	_, o := world(t)
	o.answers(7, idVendor, "Example Tip Jar", "pong")
	res := o.play(t, hdr+"listen 7\nsay \"ping\" on 7\nexpect say \"pong\" on 7 from object vendor within 1s\n")
	wantExit(t, res, 0)
	c := o.calls()
	if countOf(c, "blocked?") != 0 || countOf(c, "install slate probe") != 0 {
		t.Errorf("calls %q", c)
	}
	inOrder(t, c, "install slate bridge in slate bridge", "remove bridge from slate bridge", "take off")
	mustNotHave(t, res, "installing the probe")
	mustNotHave(t, res, "removed the probe")
}

func TestTheVerdictIsTheLastLineAndOnePrimIsSingular(t *testing.T) {
	if got := prims(1); got != "1 prim" {
		t.Errorf("prims(1) = %q", got)
	}
	_, o := world(t)
	res := o.play(t, probeHdr+"touch vendor link 2\n")
	lines := strings.Split(strings.TrimRight(res.Transcript, "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "slate: passed ") {
		t.Errorf("the last line is %q, not the verdict", last)
	}
}

func TestAClickOnALinkIsDescribedAtSetup(t *testing.T) {
	// The child's click byte is unknown until it is described; setup
	// describes it because a click expectation reads link 2.
	f, o := world(t)
	f.change(201, withClick(0))
	res := o.play(t, probeHdr+"touch vendor link 2\nexpect click vendor link 2 is touch within 1s\n")
	wantExit(t, res, 0)
	// Unknown when its describe ends: setup fails naming the child.
	_, o = world(t)
	res = o.try(t, probeHdr+"touch vendor link 2\nexpect click vendor link 2 is touch within 1s\n")
	wantExit(t, res, 3)
	mustHave(t, res, `click action was not on any update of "Example Lid"`)
}
