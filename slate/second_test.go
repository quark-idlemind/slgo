package slate

// A second avatar in a run: the avatar header, as NAME on the stimuli it
// can do, to NAME on what reaches it, and the sessions behind them.
// Why: doc/slate-language.md#a-second-avatar

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids, made with tools/new-id.
var (
	idSecond        = msg.MustParseUUID("01747e57-7e57-c0de-63d2-7b54a6999788")
	idSecondSession = msg.MustParseUUID("af457e57-7e57-c0de-695f-84382b54bcb7")
)

const secondHdr = hdr + "avatar visitor\n"

func TestASecondAvatarParses(t *testing.T) {
	s := mustCheck(t, secondHdr+`touch sign anywhere as visitor
touch sign face 2 at 0.25 0.75 as visitor
touch sign button text "Go" as visitor
drag sign face 1 from 0.1 0.1 to 0.9 0.9 over 200ms as visitor
say "hi" on 0 as visitor
choose "Red" on vendor as visitor
choose button 2 on vendor as visitor
answer "text" on vendor as visitor
expect dialog from vendor to visitor text "Pick" button "Red" only
expect textbox from vendor to visitor text "Name?"
expect give "Example Thank You" from vendor to visitor
expect say "hi" on public from avatar visitor
expect no dialog from vendor to visitor
`)
	if len(s.Avatars) != 1 || s.Avatars[0].Name.Text != "visitor" {
		t.Fatalf("avatars = %+v", s.Avatars)
	}
	st := s.Tests[0].Steps
	if st[0].Stimulus.Touch.AsAvatar.Text != "visitor" || st[1].Stimulus.Touch.AsAvatar.Text != "visitor" ||
		st[2].Stimulus.Touch.AsAvatar.Text != "visitor" || st[3].Stimulus.Drag.AsAvatar.Text != "visitor" {
		t.Error("a touch or drag lost its as")
	}
	if sp := st[4].Stimulus.Say.As; sp == nil || sp.Kind != SpeakSecond || sp.Name.Text != "visitor" {
		t.Errorf("say as = %+v", sp)
	}
	if st[5].Stimulus.Choose.AsAvatar.Text != "visitor" || st[7].Stimulus.Answer.AsAvatar.Text != "visitor" {
		t.Error("a choose or answer lost its as")
	}
	ex := st[7].Expect
	if d := ex[0].Dialog; d.To == nil || d.To.Text != "visitor" || d.Name.Text != "vendor" || !d.Only {
		t.Errorf("dialog = %+v", d)
	}
	if b := ex[1].TextBox; b.To == nil || b.To.Text != "visitor" {
		t.Errorf("textbox = %+v", b)
	}
	if g := ex[2].Give; g.To == nil || g.To.Text != "visitor" || g.From.Text != "vendor" {
		t.Errorf("give = %+v", g)
	}
	if sp := ex[3].Say.From; sp.Kind != SpeakSecond || sp.Name.Text != "visitor" {
		t.Errorf("say from = %+v", sp)
	}
	// Without to and as, everything is the tester's, as before.
	s = mustCheck(t, hdr+"touch sign anywhere\nexpect dialog from vendor\nexpect give \"x\" from vendor\n")
	if s.Tests[0].Steps[0].Stimulus.Touch.AsAvatar != nil || s.Tests[0].Steps[0].Expect[0].Dialog.To != nil {
		t.Error("the tester's forms gained an avatar")
	}
	// Any number may be declared, in any header order.
	s = mustCheck(t, "slate 1\navatar a\nobject sign is \"Example Sign\"\navatar b\ntouch sign anywhere as b\n")
	if len(s.Avatars) != 2 {
		t.Errorf("%d avatars", len(s.Avatars))
	}
}

func TestASecondAvatarGrammarErrors(t *testing.T) {
	parseErr(t, "slate 1\navatar\n", "expected a name")
	parseErr(t, "slate 1\navatar \"Name\"\nwait 100ms\n", "expected a name")
	parseErr(t, secondHdr+"touch sign anywhere as\n", "expected a name")
	parseErr(t, secondHdr+"touch sign anywhere as \"visitor\"\n", "expected a name")
	parseErr(t, secondHdr+"say \"x\" on 0 as avatar visitor\n", "as NAME, without avatar")
	parseErr(t, secondHdr+"expect dialog from vendor to\n", "expected a name")
	parseErr(t, secondHdr+"wait 100ms\navatar late\n", "headers go before the first step")
}

func TestASecondAvatarStaticErrors(t *testing.T) {
	const tester = "stays the tester's"
	for _, c := range []struct{ name, src, want string }{
		{"pay", "slate 1\nallow pay\nobject vendor is \"Example Tip Jar\"\navatar visitor\npay vendor L$5 as visitor\n", "pay " + tester},
		{"sit", secondHdr + "sit sign as visitor\n", "sit " + tester},
		{"stand", secondHdr + "stand as visitor\n", "stand " + tester},
		{"take off", wearHdr + "avatar visitor\nwear hat on \"Chest\" as h\ntake off h as visitor\n", "take off " + tester},
		{"drag on screen", wearHdr + "avatar visitor\nwear hat on \"HUD centre 2\" as hud\ndrag hud on screen from 1 1 to 5 5 as visitor\n", "drag on screen " + tester},
		{"wait", secondHdr + "wait 100ms as visitor\n", "wait " + tester},
		{"wear binds a name", wearHdr + "avatar visitor\nwear hat on \"Chest\" as visitor\n", "visitor is a second avatar, and a second avatar never wears"},
		{"wear then as", wearHdr + "avatar visitor\nwear hat on \"Chest\" as h as visitor\n", "wear " + tester},
		{"rez binds a name", "slate 1\navatar visitor\nobject sign is \"Example Sign\"\nitem hat is \"Example Hat\" in \"Objects\"\nrez hat at 1.0 2.0 3.0 as visitor\n", "never rezzes"},
		{"rez then as", "slate 1\nobject sign is \"Example Sign\"\navatar visitor\nitem hat is \"Example Hat\" in \"Objects\"\nrez hat at 1.0 2.0 3.0 as r as visitor\n", "rez " + tester},
		{"unknown on touch", hdr + "touch sign anywhere as ghost\n", "ghost is not an avatar"},
		{"unknown on say", hdr + "say \"x\" on 0 as ghost\n", "ghost is not an avatar"},
		{"unknown on choose", hdr + "choose \"x\" on vendor as ghost\n", "ghost is not an avatar"},
		{"unknown on answer", hdr + "answer \"x\" on vendor as ghost\n", "ghost is not an avatar"},
		{"unknown on drag", hdr + "drag sign face 1 from 0.1 0.1 to 0.9 0.9 as ghost\n", "ghost is not an avatar"},
		{"unknown on dialog", hdr + "expect dialog from vendor to ghost\n", "ghost is not an avatar"},
		{"unknown on textbox", hdr + "expect textbox from vendor to ghost text \"x\"\n", "ghost is not an avatar"},
		{"unknown on give", hdr + "expect give \"x\" from vendor to ghost\n", "ghost is not an avatar"},
		{"unknown on say from", hdr + "expect say \"x\" on public from avatar ghost\n", "ghost is not an avatar"},
		{"bound twice", secondHdr + "avatar visitor\ntouch sign anywhere\n", "visitor is already bound"},
		{"an object's name", hdr + "avatar sign\ntouch vendor anywhere\n", "sign is already bound"},
		{"an object after it", "slate 1\navatar sign\nobject sign is \"Example Sign\"\ntouch sign anywhere\n", "sign is already bound"},
		{"an item's name", "slate 1\nitem hat is \"Example Hat\" in \"Objects\"\navatar hat\nwait 100ms\n", "hat is already bound"},
		{"an object where an avatar is wanted", hdr + "touch sign anywhere as vendor\n", "vendor is an object, not an avatar"},
		{"an object in dialog to", hdr + "expect dialog from vendor to sign\n", "sign is an object, not an avatar"},
		{"an object in say from", hdr + "expect say \"x\" on public from avatar sign\n", "sign is an object, not an avatar"},
		{"an avatar where an object is wanted", secondHdr + "touch visitor anywhere\n", "visitor is an avatar, not an object"},
		{"an avatar in dialog from", secondHdr + "expect dialog from visitor\n", "visitor is an avatar, not an object"},
		{"an avatar in choose on", secondHdr + "choose \"x\" on visitor\n", "visitor is an avatar, not an object"},
		{"an avatar in give from", secondHdr + "expect give \"x\" from visitor to visitor\n", "visitor is an avatar, not an object"},
		{"an avatar in say object", secondHdr + "expect say \"x\" on public from object visitor\n", "visitor is an avatar, not an object"},
		{"an avatar in allow permission", secondHdr + "allow permission attach from visitor\nwait 100ms\n", "visitor is not an object or an item"},
		{"an avatar as a probe", secondHdr + "probe visitor\nwait 100ms\n", "visitor is not an object"},
		{"an avatar bound by an expectation's as", secondHdr + "expect rez name \"R\" from vendor as visitor within 1s\n", "visitor is already bound"},
	} {
		t.Run(c.name, func(t *testing.T) { checkErr(t, c.src, c.want) })
	}
}

// secondGrid is a second avatar's side of the same region: the same
// objects, its own body, id and session.
func (f *fakeGrid) secondGrid(t *testing.T) *fakeGrid {
	t.Helper()
	g := newGrid(t)
	g.info = &sl.Info{
		Name: "fake", AgentID: idSecond, SessionID: idSecondSession,
		AvatarName: "Example Resident", Region: "Test Region", InventoryRoot: testInvRoot,
	}
	g.objects = nil
	f.mu.Lock()
	for _, o := range f.objects {
		if o.ID == testMe {
			continue
		}
		c := *o
		g.objects = append(g.objects, &c)
	}
	f.mu.Unlock()
	g.objects = append(g.objects, at(&sl.Seen{Object: sl.Object{ID: idSecond, Local: 2, Name: "Example Resident"}, PCode: 47}, 129, 128, 22))
	return g
}

// playWithSecond runs src with the second avatar's session given as visitor.
func playWithSecond(t *testing.T, f, g *fakeGrid, src string) *Result {
	t.Helper()
	return playWith(t, f, src, Options{Avatars: map[string]*sl.Session{"visitor": g.session(t)}}, testCfg())
}

func TestASecondAvatarTouchIsSentOnItsOwnSession(t *testing.T) {
	f := newGrid(t)
	g := f.secondGrid(t)
	res := playWithSecond(t, f, g, secondHdr+"touch sign anywhere as visitor\n")
	wantExit(t, res, 0)
	if n := len(sentOf[*msg.ObjectGrab](g)); n != 1 {
		t.Errorf("%d grabs on the second avatar's session, want 1", n)
	}
	if n := len(sentOf[*msg.ObjectDeGrab](g)); n != 1 {
		t.Errorf("%d degrabs on the second avatar's session, want 1", n)
	}
	if n := len(sentOf[*msg.ObjectGrab](f)) + len(sentOf[*msg.ObjectDeGrab](f)); n != 0 {
		t.Errorf("%d grab messages on the tester's session, want none", n)
	}

	// The same step without as is the tester's.
	f = newGrid(t)
	g = f.secondGrid(t)
	wantExit(t, playWithSecond(t, f, g, secondHdr+"touch sign anywhere\n"), 0)
	if len(sentOf[*msg.ObjectGrab](f)) != 1 || len(sentOf[*msg.ObjectGrab](g)) != 0 {
		t.Error("a touch without as was not the tester's")
	}
}

func TestASecondAvatarFaceDragIsSentOnItsOwnSession(t *testing.T) {
	f := newGrid(t)
	g := f.secondGrid(t)
	res := playWithSecond(t, f, g, secondHdr+"drag sign face 1 from 0.1 0.1 to 0.9 0.9 over 100ms as visitor\n")
	wantExit(t, res, 0)
	if n := len(sentOf[*msg.ObjectGrab](g)); n != 1 {
		t.Errorf("%d grabs on the second avatar's session, want 1", n)
	}
	if n := len(sentOf[*msg.ObjectDeGrab](g)); n != 1 {
		t.Errorf("%d degrabs on the second avatar's session, want 1", n)
	}
	if len(sentOf[*msg.ObjectGrab](f)) != 0 || len(sentOf[*msg.ObjectDeGrab](f)) != 0 {
		t.Error("the tester's session sent a grab")
	}
}

func TestASecondAvatarSaysAndTheTesterHearsItBySourceId(t *testing.T) {
	f := newGrid(t)
	g := f.secondGrid(t)
	g.replyTo(func(m msg.Message) {
		if text, _, ok := says(m); ok {
			f.relay(chatMsg("Example Resident", idSecond, sl.ChatSay, text))
		}
	})
	res := playWithSecond(t, f, g, secondHdr+"say \"hi\" on 0 as visitor\nexpect say \"hi\" on public from avatar visitor within 1s\n")
	wantExit(t, res, 0)
	if got := g.said(); len(got) != 1 || got[0] != "hi" {
		t.Errorf("the second avatar said %q", got)
	}
	if got := f.said(); len(got) != 0 {
		t.Errorf("the tester said %q", got)
	}
	mustHave(t, res, `chat public from visitor: "hi"`)
	mustNotHave(t, res, "Example Resident")

	// Matched by id, not by name: the tester saying the same words, or
	// somebody with the second avatar's name, is not it.
	f = newGrid(t)
	g = f.secondGrid(t)
	f.replyTo(func(m msg.Message) {
		if text, _, ok := says(m); ok {
			f.relay(chatMsg("Example Resident", idStray, sl.ChatSay, text))
		}
	})
	res = playWithSecond(t, f, g, secondHdr+"say \"hi\" on 0\nexpect say \"hi\" on public from avatar visitor within 200ms\n")
	wantExit(t, res, 1)
}

func TestADialogToASecondAvatarIsHeldApartFromTheTesters(t *testing.T) {
	f := newGrid(t)
	g := f.secondGrid(t)
	g.replyTo(func(m msg.Message) {
		if _, ok := m.(*msg.ObjectGrab); ok {
			f.relay(dialogMsg(idVendor, "Example Tip Jar", "Hello tester", -111, "Go"))
			g.relay(dialogMsg(idVendor, "Example Tip Jar", "Pick one", -222, "Red", "Blue"))
		}
	})
	res := playWithSecond(t, f, g, secondHdr+`touch sign anywhere as visitor
expect dialog from vendor text "Hello tester" button "Go" within 1s
expect dialog from vendor to visitor text "Pick one" button "Red" button "Blue" only within 1s
choose "Go" on vendor
choose "Red" on vendor as visitor
`)
	wantExit(t, res, 0)
	mustHave(t, res,
		`dialog from vendor: "Hello tester" buttons "Go"`,
		`dialog to visitor from vendor: "Pick one" buttons "Red" "Blue"`)
	tr, sr := f.replies(), g.replies()
	if len(tr) != 1 || string(tr[0].Data.ButtonLabel) != "Go\x00" && string(tr[0].Data.ButtonLabel) != "Go" || tr[0].Data.ChatChannel != -111 {
		t.Errorf("the tester's replies = %+v", tr)
	}
	if len(sr) != 1 || !strings.HasPrefix(string(sr[0].Data.ButtonLabel), "Red") || sr[0].Data.ChatChannel != -222 {
		t.Errorf("the second avatar's replies = %+v", sr)
	}
}

func TestATextBoxToASecondAvatarIsAnsweredOnItsSession(t *testing.T) {
	f := newGrid(t)
	g := f.secondGrid(t)
	g.replyTo(func(m msg.Message) {
		if _, ok := m.(*msg.ObjectGrab); ok {
			g.relay(dialogMsg(idVendor, "Example Tip Jar", "Name?", -333, "!!llTextBox!!"))
		}
	})
	res := playWithSecond(t, f, g, secondHdr+`touch sign anywhere as visitor
expect textbox from vendor to visitor text "Name?" within 1s
answer "Pelham" on vendor as visitor
`)
	wantExit(t, res, 0)
	mustHave(t, res, `textbox to visitor from vendor: "Name?"`)
	sr := g.replies()
	if len(sr) != 1 || !strings.HasPrefix(string(sr[0].Data.ButtonLabel), "Pelham") || len(f.replies()) != 0 {
		t.Errorf("replies: second %+v, tester %d", sr, len(f.replies()))
	}
}

func TestADialogToTheTesterIsNotOneToTheSecondAvatarAndTheOtherWayRound(t *testing.T) {
	f := newGrid(t)
	g := f.secondGrid(t)
	f.on("menu", dialogMsg(idVendor, "Example Tip Jar", "Hi", -1, "A"))
	wantExit(t, playWithSecond(t, f, g, secondHdr+"say \"menu\" on 0\nexpect dialog from vendor to visitor within 200ms\n"), 1)

	f = newGrid(t)
	g = f.secondGrid(t)
	g.replyTo(func(m msg.Message) {
		if _, ok := m.(*msg.ObjectGrab); ok {
			g.relay(dialogMsg(idVendor, "Example Tip Jar", "Hi", -1, "A"))
		}
	})
	res := playWithSecond(t, f, g, secondHdr+"touch sign anywhere as visitor\nexpect dialog from vendor within 200ms\n")
	wantExit(t, res, 1)
	// No hold was made for the tester, and the unanswered dialog is named.
	res = playWithSecond(t, f, g, secondHdr+"choose \"A\" on vendor as visitor\n")
	wantExit(t, res, 1)
	mustHave(t, res, "no dialog is held for vendor to visitor")
}

func TestAnUnansweredDialogToASecondAvatarIsLeftAndNamedInAFailure(t *testing.T) {
	f := newGrid(t)
	g := f.secondGrid(t)
	g.replyTo(func(m msg.Message) {
		if _, ok := m.(*msg.ObjectGrab); ok {
			g.relay(dialogMsg(idVendor, "Example Tip Jar", "Pick one", -222, "Red"))
		}
	})
	res := playWithSecond(t, f, g, secondHdr+`touch sign anywhere as visitor
expect dialog from vendor to visitor text "Pick one" within 1s
expect say "never" on public from object sign within 100ms
`)
	wantExit(t, res, 1)
	mustHave(t, res, `dialog left unanswered: [Object] "Example Tip Jar" "Pick one" buttons "Red" (to visitor)`)
	if len(g.replies()) != 0 {
		t.Error("an unanswered dialog was answered")
	}
}

func TestAGiveToASecondAvatarIsSeenAndDeclinedNeverAccepted(t *testing.T) {
	f := newGrid(t)
	g := f.secondGrid(t)
	g.replyTo(func(m msg.Message) {
		if _, ok := m.(*msg.ObjectGrab); ok {
			g.relay(offerIM(idStranger, "Example Tip Jar", idOffer, "Example Thank You", 10))
		}
	})
	res := playWithSecond(t, f, g, secondHdr+"touch sign anywhere as visitor\nexpect give \"Example Thank You\" from vendor to visitor within 1s as $item\n")
	wantExit(t, res, 0)
	mustHave(t, res, `give to visitor from vendor: "Example Thank You"`, "give to visitor declined, transaction "+idOffer.String())
	var declined, accepted int
	for _, m := range sentOf[*msg.ImprovedInstantMessage](g) {
		switch m.MessageBlock.Dialog {
		case sl.DialogTaskInventoryDeclined:
			declined++
			if m.MessageBlock.ID != idOffer || m.MessageBlock.ToAgentID != idStranger {
				t.Errorf("decline = id %s to %s", m.MessageBlock.ID, m.MessageBlock.ToAgentID)
			}
		case sl.DialogTaskInventoryAccepted:
			accepted++
		}
	}
	if declined != 1 || accepted != 0 {
		t.Errorf("%d declines and %d accepts on the second avatar's session, want 1 and 0", declined, accepted)
	}
	if n := len(sentOf[*msg.ImprovedInstantMessage](f)); n != 0 {
		t.Errorf("the tester's session sent %d instant messages", n)
	}
	if len(g.accepts()) != 0 {
		t.Error("an accept was recorded")
	}
}

func TestAnOfferToASecondAvatarNoStepExpectedIsDeclinedAtOnce(t *testing.T) {
	f := newGrid(t)
	g := f.secondGrid(t)
	g.replyTo(func(m msg.Message) {
		if _, ok := m.(*msg.ObjectGrab); ok {
			g.relay(offerIM(idStranger, "Example Tip Jar", idOffer, "Example Thank You", 10))
		}
	})
	res := playWithSecond(t, f, g, secondHdr+"touch sign anywhere as visitor\nexpect no give \"Other\" from vendor to visitor within 300ms\n")
	wantExit(t, res, 0)
	var declined int
	for _, m := range sentOf[*msg.ImprovedInstantMessage](g) {
		if m.MessageBlock.Dialog == sl.DialogTaskInventoryDeclined {
			declined++
		}
	}
	if declined != 1 {
		t.Errorf("%d declines, want 1", declined)
	}
}

func TestAGiveToTheTesterIsNotOneToTheSecondAvatar(t *testing.T) {
	f := newGrid(t)
	g := f.secondGrid(t)
	f.withInventory(t)
	f.whenSaid("buy", func() {
		f.relay(offerIM(idStranger, "Example Tip Jar", idOffer, "Example Thank You", 10))
	})
	res := playWithSecond(t, f, g, secondHdr+"say \"buy\" on 0\nexpect give \"Example Thank You\" from vendor to visitor within 300ms\n")
	wantExit(t, res, 1)
	for _, m := range sentOf[*msg.ImprovedInstantMessage](g) {
		t.Errorf("the second avatar sent dialog %d", m.MessageBlock.Dialog)
	}
}

func TestAPermissionRequestToASecondAvatarIsDeniedWhateverTheFileAllows(t *testing.T) {
	f := newGrid(t)
	g := f.secondGrid(t)
	m := &msg.ScriptQuestion{}
	m.Data.TaskID, m.Data.ItemID = idSign, idStray
	m.Data.ObjectName, m.Data.ObjectOwner = []byte("Example Sign\x00"), []byte("Example Resident\x00")
	m.Data.Questions = int32(sl.PermissionAttach)
	g.quietly(t, 100*time.Millisecond, m)
	res := playWithSecond(t, f, g, secondHdr+"allow permission attach from sign\nwait 100ms\nexpect no say \"x\" on public from anyone within 400ms\n")
	wantExit(t, res, 0)
	wantAnswers(t, g, 0)
	if len(answers(f)) != 0 {
		t.Errorf("the tester answered %v", answers(f))
	}
	mustHave(t, res, "permission denied to visitor from sign: ")
	mustNotHave(t, res, "permission granted")
}

func TestASecondAvatarRunIsGivenExactlyTheAvatarsTheFileDeclares(t *testing.T) {
	f := newGrid(t)
	g := f.secondGrid(t)
	other := f.secondGrid(t)

	// Declared and not given.
	res, err := tryPlay(t, f, secondHdr+"wait 100ms\n", Options{}, testCfg())
	if err == nil {
		t.Fatal("no error")
	}
	wantExit(t, res, 3)
	mustHave(t, res, "slate: setup: visitor is declared but no --avatar visitor=... was given")

	// Given and not declared.
	res, err = tryPlay(t, f, hdr+"wait 100ms\n", Options{Avatars: map[string]*sl.Session{"visitor": g.session(t)}}, testCfg())
	if err == nil {
		t.Fatal("no error")
	}
	wantExit(t, res, 3)
	mustHave(t, res, "an --avatar was given for visitor, which the file does not declare")

	// The tester's own: the same session, or another one of its avatar.
	own := f.session(t)
	res, err = tryPlay(t, f, secondHdr+"wait 100ms\n", Options{Avatars: map[string]*sl.Session{"visitor": own}}, testCfg())
	if err == nil {
		t.Fatal("no error")
	}
	wantExit(t, res, 3)
	mustHave(t, res, "the profile given for visitor is the tester's own")
	twin := newGrid(t) // the tester's id again, on a session of its own
	res, err = tryPlay(t, f, secondHdr+"wait 100ms\n", Options{Avatars: map[string]*sl.Session{"visitor": twin.session(t)}}, testCfg())
	if err == nil {
		t.Fatal("no error")
	}
	wantExit(t, res, 3)
	mustHave(t, res, "the profile given for visitor is the tester's own")

	// Two names, one avatar.
	res, err = tryPlay(t, f, hdr+"avatar visitor\navatar guest\nwait 100ms\n", Options{Avatars: map[string]*sl.Session{
		"visitor": g.session(t), "guest": other.session(t)}}, testCfg())
	if err == nil {
		t.Fatal("no error")
	}
	wantExit(t, res, 3)
	mustHave(t, res, "the profiles given for visitor and guest are the same avatar")
}
