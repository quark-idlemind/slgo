package slate

import (
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// declinesOf is the declines of an object's give that a grid was sent.
func declinesOf(g *fakeGrid) []*msg.ImprovedInstantMessage {
	var out []*msg.ImprovedInstantMessage
	for _, m := range sentOf[*msg.ImprovedInstantMessage](g) {
		if m.MessageBlock.Dialog == sl.DialogTaskInventoryDeclined {
			out = append(out, m)
		}
	}
	return out
}

func TestAnOfferNoStepAcceptedIsDeclinedAtTheEndOfTheTest(t *testing.T) {
	f := newGrid(t)
	f.whenSaid("buy", func() {
		f.relay(offerIM(idStranger, "Example Tip Jar", idOffer, "Example Thank You", 10))
	})
	res := play(t, f, hdr+"say \"buy\" on 0\nwait 300ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, `slate: declined offer "Example Thank You" from vendor`)
	d := declinesOf(f)
	if len(d) != 1 || d[0].MessageBlock.ID != idOffer || d[0].MessageBlock.ToAgentID != idStranger {
		t.Fatalf("declines = %+v", d)
	}
	if len(f.accepts()) != 0 {
		t.Error("an accept was sent")
	}
}

// TestAPersonsOfferIsLeftWaitingAndSaid: the tester is somebody's
// avatar, and a friend's give arriving during a run is theirs to answer,
// not the run's.  It is left waiting, with a line saying so, and nothing
// is sent to the person.
func TestAPersonsOfferIsLeftWaitingAndSaid(t *testing.T) {
	f := newGrid(t)
	f.whenSaid("buy", func() {
		// A person's give: the text is the item's name, and the bucket is
		// the asset type and the item's id.
		m := offerIM(idStranger, "Kerra Yule", idOffer, "Example Thank You", 10)
		m.MessageBlock.Dialog = sl.DialogInventoryOffered
		m.MessageBlock.Message = []byte("Example Thank You\x00")
		m.MessageBlock.BinaryBucket = append([]byte{10}, idNewCopy[:]...)
		f.relay(m)
	})
	res := play(t, f, hdr+"say \"buy\" on 0\nwait 300ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, `slate: left offer "Example Thank You" from Kerra Yule waiting; a person's offer is not the run's to answer`)
	if d := declinesOf(f); len(d) != 0 {
		t.Fatalf("a person's offer was declined: %+v", d)
	}
	if len(f.accepts()) != 0 {
		t.Error("an accept was sent")
	}
}

func TestAnOfferAnExpectGiveAcceptedIsNotDeclined(t *testing.T) {
	f := newGrid(t)
	offering(t, f, idNewCopy)
	res := play(t, f, giveSrc)
	wantExit(t, res, 0)
	if len(f.accepts()) != 1 {
		t.Fatalf("%d accepts, want 1\n%s", len(f.accepts()), res.Transcript)
	}
	if d := declinesOf(f); len(d) != 0 {
		t.Errorf("an accepted offer was declined: %+v", d)
	}
	mustNotHave(t, res, "declined offer")
}

func TestAnOfferToASecondAvatarIsStillDeclinedOnItsOwnSessionAndNotTwice(t *testing.T) {
	f := newGrid(t)
	g := f.secondGrid(t)
	g.replyTo(func(m msg.Message) {
		if _, ok := m.(*msg.ObjectGrab); ok {
			g.relay(offerIM(idStranger, "Example Tip Jar", idOffer, "Example Thank You", 10))
		}
	})
	res := playWithSecond(t, f, g, secondHdr+"touch sign anywhere as visitor\nexpect give \"Example Thank You\" from vendor to visitor within 1s\n")
	wantExit(t, res, 0)
	if d := declinesOf(g); len(d) != 1 {
		t.Errorf("%d declines on the second avatar, want 1", len(d))
	}
	if d := declinesOf(f); len(d) != 0 {
		t.Errorf("the tester declined %d offers it never had", len(d))
	}
	mustHave(t, res, "give to visitor declined, transaction "+idOffer.String())
	mustNotHave(t, res, "slate: declined offer")
}

func TestADialogNoStepAnsweredIsLeftAndSaidAtTheEndOfTheTest(t *testing.T) {
	f := newGrid(t)
	f.on("menu", dialogMsg(idVendor, "Example Tip Jar", "Pick one", -222, "Red", "Blue"))
	sess := f.session(t)
	s := mustCheck(t, hdr+"say \"menu\" on 0\nwait 300ms\n")
	res, err := run(t.Context(), sess, s, Options{}, testCfg())
	if err != nil {
		t.Fatal(err)
	}
	wantExit(t, res, 0)
	mustHave(t, res, `slate: left dialog from vendor "Pick one" unanswered`)
	if len(sess.Dialogs()) != 0 {
		t.Error("the dialog is still listed")
	}
	if len(f.replies()) != 0 {
		t.Error("an ignored dialog was answered")
	}
}

func TestAHeldDialogIsLeftAndSaidToo(t *testing.T) {
	f := newGrid(t)
	f.on("menu", dialogMsg(idVendor, "Example Tip Jar", "Pick one", -222, "Red"))
	sess := f.session(t)
	s := mustCheck(t, hdr+"say \"menu\" on 0\nexpect dialog from vendor text \"Pick one\" within 1s\n")
	res, err := run(t.Context(), sess, s, Options{}, testCfg())
	if err != nil {
		t.Fatal(err)
	}
	wantExit(t, res, 0)
	mustHave(t, res, `slate: left dialog from vendor "Pick one" unanswered`)
	if len(sess.Dialogs()) != 0 || len(f.replies()) != 0 {
		t.Error("the held dialog was kept or answered")
	}
}

func TestADialogAChooseAnsweredIsNotSaidToBeLeft(t *testing.T) {
	f := newGrid(t)
	f.on("menu", dialogMsg(idVendor, "Example Tip Jar", "Pick one", -222, "Red"))
	res := play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor text \"Pick one\" within 1s\nchoose \"Red\" on vendor\n")
	wantExit(t, res, 0)
	mustNotHave(t, res, "left dialog")
}
