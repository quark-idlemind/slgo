package slate

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

const sayFar = hdr + "say \"go\" on 0\nexpect say \"hi\" on public from object sign within 300ms\n"

// moveSign puts the sign x metres east of the standing tester.
func (f *fakeGrid) moveSign(x float32) {
	at(f.objects[0], 128+x, 128, 22)
}

func TestASayFailsBeforeItIsSentWhenABindingIsOver20Metres(t *testing.T) {
	f := newGrid(t)
	f.moveSign(25)
	f.on("go", signSays("hi"))
	res := play(t, f, sayFar)
	wantExit(t, res, 1)
	const want = `slate: step 1: "Example Sign" is 25 m from the tester; a say is ordinary chat, not as far as the region lookup can see (say radius 20 m: heard at 19.5 m and not at 20.5 m)`
	n := 0
	for _, l := range lines(res) {
		if strings.TrimSpace(l) == want {
			n++
		}
	}
	if n != 2 {
		t.Errorf("the sentence is on %d lines, want 2 (the transcript and the stimulus detail):\n%s", n, res.Transcript)
	}
	if got := f.said(); len(got) != 0 {
		t.Errorf("said %q", got)
	}
	if got := sentOf[*msg.ChatFromViewer](f); len(got) != 0 {
		t.Errorf("%d chat sends", len(got))
	}
}

func TestASayWithinTheRadiusIsSentAndOnlyNamedBindingsAreMeasured(t *testing.T) {
	f := newGrid(t)
	f.moveSign(15)
	f.on("go", signSays("hi"))
	wantExit(t, play(t, f, sayFar), 0)

	// The sign is far, but this step names only the vendor.
	f = newGrid(t)
	f.moveSign(60)
	f.on("go", chatMsg("Example Tip Jar", idVendor, sl.ChatSay, "hi"))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect say \"hi\" on public from object vendor within 300ms\n"), 0)

	// The binding it speaks as is named.
	f = newGrid(t)
	f.moveSign(60)
	res := play(t, f, hdr+"say \"go\" on 0 as owner of sign\n")
	wantExit(t, res, 1)
	mustHave(t, res, `"Example Sign" is 60 m from the tester`)
}

func TestADistanceIsRoundedUpSoItNeverReadsAsAllowed(t *testing.T) {
	f := newGrid(t)
	f.moveSign(20.2)
	res := play(t, f, sayFar)
	wantExit(t, res, 1)
	mustHave(t, res, `"Example Sign" is 21 m from the tester`)
}

func TestTheTesterPositionMustBeExact(t *testing.T) {
	// Seated with the chain in the store: Where is used. The avatar's own
	// position is an offset from the seat, so using it would be far.
	f := newGrid(t)
	seat := at(prim(idChair, 401, "Example Chair", testMe), 130, 128, 25)
	f.objects = append(f.objects, seat)
	f.objects[len(f.objects)-2].Parent = 401 // the avatar
	at(f.objects[len(f.objects)-2], 0, 0, 0.4)
	f.on("go", signSays("hi"))
	wantExit(t, play(t, f, sayFar), 0)

	// Seated with an ancestor missing: not exact.
	f = newGrid(t)
	f.objects[len(f.objects)-1].Parent = 999
	res := play(t, f, sayFar)
	wantExit(t, res, 1)
	n := 0
	for _, l := range lines(res) {
		if strings.TrimSpace(l) == "slate: step 1: tester position is not exact; the say was not sent" {
			n++
		}
	}
	if n != 2 || len(f.said()) != 0 {
		t.Errorf("%d lines, said %q:\n%s", n, f.said(), res.Transcript)
	}

	// Not in the list at all.
	f = newGrid(t)
	f.objects = f.objects[:len(f.objects)-1]
	res = play(t, f, sayFar)
	wantExit(t, res, 1)
	mustHave(t, res, "tester position is not exact")
}

func TestTouchSendsTheRightSurface(t *testing.T) {
	for _, c := range []struct {
		step string
		face int32
		st   msg.Vector3
	}{
		{"touch sign anywhere", 0, msg.Vector3{X: 0.5, Y: 0.5}},
		{"touch sign face 2", 2, msg.Vector3{X: 0.5, Y: 0.5}},
		{"touch sign face 3 at 0.25 0.75", 3, msg.Vector3{X: 0.25, Y: 0.75}},
	} {
		f := newGrid(t)
		res := play(t, f, hdr+c.step+"\n")
		wantExit(t, res, 0)
		grabs, degrabs := sentOf[*msg.ObjectGrab](f), sentOf[*msg.ObjectDeGrab](f)
		if len(grabs) != 1 || len(degrabs) != 1 {
			t.Fatalf("%s: %d grabs, %d degrabs", c.step, len(grabs), len(degrabs))
		}
		g := grabs[0]
		if g.ObjectData.LocalID != 101 || len(g.SurfaceInfo) != 1 {
			t.Fatalf("%s: grab %+v", c.step, g)
		}
		if si := g.SurfaceInfo[0]; si.FaceIndex != c.face || si.STCoord != c.st {
			t.Errorf("%s: face %d st %v, want %d %v", c.step, si.FaceIndex, si.STCoord, c.face, c.st)
		}
	}
}

func TestDragSendsTwoPointsAndTheReleaseAndBlocks(t *testing.T) {
	step := hdr + "drag sign face 1 from 0.1 0.5 to 0.9 0.5 over 150ms\nexpect say \"hi\" on public from object sign within 250ms\n"

	// The drag takes 150 ms; the line comes 330 ms in, after the 250 ms
	// the expectation names would have run out, and inside the extension.
	f := newGrid(t)
	f.quietly(t, 330*time.Millisecond, signSays("hi"))
	t0 := time.Now()
	res := play(t, f, step)
	wantExit(t, res, 0)
	if took := time.Since(t0); took < 330*time.Millisecond {
		t.Errorf("the step took %s, want at least 330ms", took)
	}
	grabs, ups, downs := sentOf[*msg.ObjectGrab](f), sentOf[*msg.ObjectGrabUpdate](f), sentOf[*msg.ObjectDeGrab](f)
	if len(grabs) != 1 || len(downs) != 1 || len(ups) == 0 {
		t.Fatalf("%d grabs, %d updates, %d degrabs", len(grabs), len(ups), len(downs))
	}
	if si := grabs[0].SurfaceInfo[0]; si.FaceIndex != 1 || si.STCoord != (msg.Vector3{X: 0.1, Y: 0.5}) {
		t.Errorf("pressed at %+v", si)
	}
	if si := downs[0].SurfaceInfo[0]; si.FaceIndex != 1 || si.STCoord != (msg.Vector3{X: 0.9, Y: 0.5}) {
		t.Errorf("released at %+v", si)
	}

	// Nothing said: it fails at the extended deadline, not before it.
	f = newGrid(t)
	t0 = time.Now()
	wantExit(t, play(t, f, step), 1)
	if took := time.Since(t0); took < 350*time.Millisecond {
		t.Errorf("failed after %s, want the drag's 150ms and the 250ms after it", took)
	}
}

// TestADragHoldsItsPressAndDwell: press holds still at the start before
// moving and dwell at the end before letting go, so a script that reads
// the drag from its touch events sees both ends held.
func TestADragHoldsItsPressAndDwell(t *testing.T) {
	step := hdr + "drag sign face 1 from 0.1 0.5 to 0.9 0.5 over 100ms press 200ms dwell 200ms\n"
	f := newGrid(t)
	t0 := time.Now()
	wantExit(t, play(t, f, step), 0)
	if took := time.Since(t0); took < 400*time.Millisecond {
		t.Errorf("the drag took %s, want at least the 400ms of press and dwell", took)
	}
	ups := sentOf[*msg.ObjectGrabUpdate](f)
	if len(ups) < 3 {
		t.Fatalf("%d updates", len(ups))
	}
	var atStart, atEnd int
	for _, u := range ups {
		switch u.SurfaceInfo[0].STCoord {
		case msg.Vector3{X: 0.1, Y: 0.5}:
			atStart++
		case msg.Vector3{X: 0.9, Y: 0.5}:
			atEnd++
		}
	}
	// At the touch rate, 200 ms is several updates at each end.
	if atStart < 3 || atEnd < 3 {
		t.Errorf("%d updates held at the start and %d at the end, want several of each", atStart, atEnd)
	}
}

// sits has the simulator seat the avatar on the sign when asked.
func (f *fakeGrid) sits() {
	f.replyTo(func(m msg.Message) {
		if _, ok := m.(*msg.AgentRequestSit); ok {
			f.relay(&msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{FullID: testMe, ID: 1, ParentID: 101}}})
		}
	})
	f.onControl = func(flags uint32) {
		if flags&agent.ControlStandUp != 0 {
			f.relay(&msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{FullID: testMe, ID: 1}}})
		}
	}
}

func TestASitLeavesTheAvatarSeatedAndTheFailureBlockSaysSo(t *testing.T) {
	f := newGrid(t)
	f.sits()
	res := play(t, f, hdr+"sit sign\nsay \"go\" on 0\nexpect say \"never\" on public from object sign within 100ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, `seated: sign "Example Sign"`, "slate: pass step 1")
	if got := sentOf[*msg.AgentRequestSit](f); len(got) != 1 || got[0].TargetObject.TargetID != idSign {
		t.Errorf("sits %+v", got)
	}
	// Standing clears it, and standing while standing is no error.
	f = newGrid(t)
	f.sits()
	res = play(t, f, hdr+"sit sign\nstand\nstand\nsay \"go\" on 0\nexpect say \"never\" on public from object sign within 100ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "  seated: no", "slate: pass step 2", "slate: pass step 3")
	mustNotHave(t, res, `seated: sign`)
	if len(f.controls) != 2 {
		t.Errorf("%d stand flags, want 2", len(f.controls))
	}
}

func TestASitRefusedOrUnansweredFailsWithSlsOwnText(t *testing.T) {
	f := newGrid(t)
	f.replyTo(func(m msg.Message) {
		if _, ok := m.(*msg.AgentRequestSit); ok {
			a := &msg.AlertMessage{}
			a.AlertData.Message = []byte("You cannot sit there.\x00")
			f.relay(a)
		}
	})
	res := play(t, f, hdr+"sit sign\n")
	wantExit(t, res, 1)
	mustHave(t, res, "the simulator refused the sit", `"You cannot sit there."`, "seated: no")

	f = newGrid(t)
	res = play(t, f, hdr+"sit sign\nexpect no say \"x\" on public from anyone within 120ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "neither seated this avatar nor refused", "the sit may yet have taken", "seated: no")
}

// bank makes the far end answer a payment's questions.
func (f *fakeGrid) bank(balance int, onPay func(*msg.MoneyTransferRequest)) {
	reply := func(tx msg.UUID, ok bool, bal int, said string, typ int, from, to msg.UUID, amount int, echo string) *msg.MoneyBalanceReply {
		m := &msg.MoneyBalanceReply{}
		m.MoneyData.TransactionID = tx
		m.MoneyData.TransactionSuccess = ok
		m.MoneyData.MoneyBalance = int32(bal)
		m.MoneyData.Description = append([]byte(said), 0)
		m.TransactionInfo.TransactionType = int32(typ)
		m.TransactionInfo.SourceID = from
		m.TransactionInfo.DestID = to
		m.TransactionInfo.Amount = int32(amount)
		m.TransactionInfo.ItemDescription = append([]byte(echo), 0)
		return m
	}
	f.replyTo(func(m msg.Message) {
		switch x := m.(type) {
		case *msg.MoneyBalanceRequest:
			f.relay(reply(msg.UUID{}, true, balance, "", 0, msg.UUID{}, msg.UUID{}, -1, ""))
		case *msg.RequestObjectPropertiesFamily:
			p := &msg.ObjectPropertiesFamily{}
			p.ObjectData.ObjectID = x.ObjectData.ObjectID
			p.ObjectData.OwnerID = idStranger
			p.ObjectData.Name = append([]byte("Example Tip Jar"), 0)
			f.relay(p)
		case *msg.MoneyTransferRequest:
			if onPay != nil {
				onPay(x)
			}
		}
	})
	f.reply = reply
}

const payHdr = "slate 1\nallow pay\nobject vendor is \"Example Tip Jar\"\n"

var idPayTx = msg.MustParseUUID("e4317e57-7e57-c0de-fee1-3a55e8e34d2c")

func TestPayingIsOffWithoutTheFlag(t *testing.T) {
	f := newGrid(t)
	f.bank(100, nil)
	res := play(t, f, payHdr+"pay vendor L$5 reason \"tip\"\n")
	wantExit(t, res, 1)
	mustHave(t, res, "not sent: paying is off", "payment: none")
	mustNotHave(t, res, "pay L$")
	if got := sentOf[*msg.MoneyTransferRequest](f); len(got) != 0 {
		t.Errorf("%d transfers sent", len(got))
	}
}

func TestAPaymentIsPrintedAndSentOnceAndMoneyTimeoutIsTheBudget(t *testing.T) {
	f := newGrid(t)
	var during time.Duration
	f.bank(100, func(x *msg.MoneyTransferRequest) {
		f.mu.Lock()
		sess := f.sess
		f.mu.Unlock()
		during = sess.Options().MoneyTimeout
		f.relay(f.reply(idPayTx, true, 95, "", sl.TransactionPayObject, testMe, idVendor, 5, "tip"))
	})
	res := playWith(t, f, payHdr+"pay vendor L$5 reason \"tip\"\nexpect say \"x\" on public from object vendor within 250ms\n", Options{Pay: true}, testCfg())
	wantExit(t, res, 1) // the expectation, not the payment
	mustHave(t, res,
		`pay L$5 to "Example Tip Jar" `+idVendor.String()+` reason "tip"`,
		"payment: L$5 to \"Example Tip Jar\" transaction "+idPayTx.String(),
		`unmatched say "x"`)
	if got := sentOf[*msg.MoneyTransferRequest](f); len(got) != 1 || got[0].MoneyData.Amount != 5 {
		t.Errorf("transfers %+v", got)
	}
	if during != 250*time.Millisecond {
		t.Errorf("MoneyTimeout during the call = %s, want the 250ms budget", during)
	}
	f.mu.Lock()
	sess := f.sess
	f.mu.Unlock()
	if got := sess.Options(); got != (sl.Options{}) {
		t.Errorf("options afterwards = %+v, want the zero Options back", got)
	}
}

func TestAnOmittedReasonPaysWithTheObjectsName(t *testing.T) {
	f := newGrid(t)
	f.bank(100, func(x *msg.MoneyTransferRequest) {
		f.relay(f.reply(idPayTx, true, 95, "", sl.TransactionPayObject, testMe, idVendor, 5, "Example Tip Jar"))
	})
	res := playWith(t, f, payHdr+"pay vendor L$5\n", Options{Pay: true}, testCfg())
	wantExit(t, res, 0)
	mustHave(t, res, `reason "Example Tip Jar"`)
}

func TestARefusedPaymentFailsTheStepAndIsNotRetried(t *testing.T) {
	f := newGrid(t)
	f.bank(100, func(x *msg.MoneyTransferRequest) {
		f.relay(f.reply(msg.UUID{}, false, 100, "Not allowed", sl.TransactionPayObject, testMe, idVendor, 5, "tip"))
	})
	res := playWith(t, f, payHdr+"pay vendor L$5 reason \"tip\"\nexpect say \"x\" on public from object vendor within 250ms\n", Options{Pay: true}, testCfg())
	wantExit(t, res, 1)
	mustHave(t, res, "refused it", "Not allowed", "payment: L$5", `pay L$5 to "Example Tip Jar"`)
	mustHave(t, res, "waited: 0s") // the expectations did not run
	if got := sentOf[*msg.MoneyTransferRequest](f); len(got) != 1 {
		t.Errorf("%d transfers sent", len(got))
	}
}

func TestAnUnconfirmedPaymentFailsWithTheBalanceText(t *testing.T) {
	f := newGrid(t)
	f.bank(100, nil) // the transfer is never answered
	t0 := time.Now()
	res := playWith(t, f, payHdr+"pay vendor L$5 reason \"tip\"\nexpect say \"x\" on public from object vendor within 150ms\n", Options{Pay: true}, testCfg())
	wantExit(t, res, 1)
	mustHave(t, res, "the grid did not answer the payment of L$5", "it was not paid", "payment: L$5")
	if got := sentOf[*msg.MoneyTransferRequest](f); len(got) != 1 {
		t.Errorf("%d transfers sent, want one", len(got))
	}
	if took := time.Since(t0); took > 2*time.Second {
		t.Errorf("took %s", took)
	}
}

func TestAPendingPermissionRequestIsDeniedAndPrinted(t *testing.T) {
	ask := func() msg.Message {
		q := &msg.ScriptQuestion{}
		q.Data.TaskID = idSign
		q.Data.ItemID = idStray
		q.Data.ObjectName = []byte("Example Sign\x00")
		q.Data.ObjectOwner = []byte("Example Resident\x00")
		q.Data.Questions = int32(sl.PermissionTriggerAnimation)
		return q
	}
	check := func(t *testing.T, f *fakeGrid, res *Result) {
		t.Helper()
		mustHave(t, res, "permission denied from sign: trigger animation")
		got := sentOf[*msg.ScriptAnswerYes](f)
		if len(got) != 1 || got[0].Data.TaskID != idSign || got[0].Data.Questions != 0 {
			t.Errorf("answers %+v", got)
		}
	}

	// During a step that is still waiting.
	f := newGrid(t)
	f.on("go", ask())
	res := play(t, f, hdr+"say \"go\" on 0\nexpect no say \"zzz\" on public from object sign within 150ms\n")
	wantExit(t, res, 0)
	check(t, f, res)

	// Still pending when the test ends: the step passed on its chat.
	f = newGrid(t)
	f.on("go", signSays("hi"), ask())
	res = play(t, f, sayFar)
	wantExit(t, res, 0)
	check(t, f, res)
}

// TestAWaitWaitsAndSendsNothing: wait D blocks for D and is the whole
// step; nothing goes to the grid.
func TestAWaitWaitsAndSendsNothing(t *testing.T) {
	f := newGrid(t)
	t0 := time.Now()
	wantExit(t, play(t, f, hdr+"wait 300ms\n"), 0)
	if took := time.Since(t0); took < 300*time.Millisecond {
		t.Errorf("the wait took %s, want at least 300ms", took)
	}
	if n := len(sentOf[*msg.ObjectGrab](f)); n != 0 {
		t.Errorf("a wait sent %d grabs", n)
	}
}
