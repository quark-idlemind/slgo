package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// dialogFrom builds a script dialog with these buttons.
func dialogFrom(name, message string, channel int32, buttons ...string) *msg.ScriptDialog {
	d := &msg.ScriptDialog{}
	d.Data.ObjectID = testLamp
	d.Data.ObjectName = append([]byte(name), 0)
	d.Data.Message = append([]byte(message), 0)
	d.Data.ChatChannel = channel
	for _, b := range buttons {
		d.Buttons = append(d.Buttons, msg.ScriptDialog_Buttons{
			ButtonLabel: append([]byte(b), 0),
		})
	}
	return d
}

// testGroup is a group that invites, and testRole the role it invites
// into.  Neither is an avatar: the sender of a group invitation is the
// group itself, which is the fact the shell's handling turns on.
var (
	testGroup = msg.MustParseUUID("e2a17e57-7e57-c0de-b864-09116b98f7c6")
	testRole  = msg.MustParseUUID("dc047e57-7e57-c0de-1117-911169260e8b")
)

// inviting builds a group invitation: from the group, naming whoever
// invited, with the fee and the role in the binary bucket.
func inviting(by, text string, fee int32, txn msg.UUID) *msg.ImprovedInstantMessage {
	m := imFrom(testGroup, by, sl.DialogGroupInvitation, text)
	m.MessageBlock.FromGroup = true
	m.MessageBlock.ID = txn
	b := binary.BigEndian.AppendUint32(nil, uint32(fee))
	m.MessageBlock.BinaryBucket = append(b, testRole[:]...)
	return m
}

// TestANumberMeansTheSameThingAfterAnAnswer is the bug the first live
// run of this found.
//
// Numbering by position looks right until something is answered: with
// two waiting, answering the first made the second become 1, so the
// "answer 2" already typed out was an error -- and would have been a
// wrong answer to the wrong thing if the list had been longer.
func TestANumberMeansTheSameThingAfterAnAnswer(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.Relay(t, dialogFrom("a lamp", "on or off?", -101, "on", "off"))
	waits(t, x, "a lamp asks")
	x.grid.Relay(t, dialogFrom("a door", "open?", -102, "yes", "no"))
	waits(t, x, "a door asks")

	got := x.do(t, "waiting")
	if !strings.Contains(got, "1  dialog") || !strings.Contains(got, "2  dialog") {
		t.Fatalf("two dialogs should be 1 and 2:\n%s", got)
	}

	if got := x.do(t, "answer 1 on"); !strings.Contains(got, "pressed") {
		t.Fatalf("answering the first: %s", got)
	}

	// The door was 2 before and is 2 now.
	got = x.do(t, "waiting")
	if !strings.Contains(got, "2  dialog") {
		t.Errorf("the door should still be 2:\n%s", got)
	}
	if strings.Contains(got, "1  dialog") {
		t.Errorf("nothing should have taken the answered one's number:\n%s", got)
	}
	if got := x.do(t, "answer 2 yes"); !strings.Contains(got, "pressed") {
		t.Errorf("answering by the number on the screen: %s", got)
	}
	// And with nothing left, the numbering starts again rather than
	// climbing for the rest of the session.
	x.grid.Relay(t, dialogFrom("a gate", "through?", -103, "yes"))
	waits(t, x, "a gate asks")
	if got := x.do(t, "waiting"); !strings.Contains(got, "1  dialog") {
		t.Errorf("an empty list should start again at 1:\n%s", got)
	}
}

// TestAnObjectAskingIsListedAsAnObject: a dialog and a permission
// request come from an object, which can be called anything, so the
// listing and what answering says label it as one.
func TestAnObjectAskingIsListedAsAnObject(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.Relay(t, dialogFrom("Some Body", "on or off?", -101, "on", "off"))
	waits(t, x, "Some Body asks")
	q := &msg.ScriptQuestion{}
	q.Data.TaskID = testLamp
	q.Data.ItemID = testProbe
	q.Data.ObjectName = append([]byte("Some Body"), 0)
	q.Data.ObjectOwner = append([]byte("Quark Idlemind"), 0)
	q.Data.Questions = 2
	x.grid.Relay(t, q)
	waits(t, x, "Some Body wants")

	got := x.do(t, "waiting")
	for _, want := range []string{"dialog      [Object] Some Body ", "permission  [Object] Some Body "} {
		if !strings.Contains(got, want) {
			t.Errorf("the listing should say %q:\n%s", want, got)
		}
	}
	if got := x.do(t, "answer 1 on"); !strings.Contains(got, `pressed "on" on [Object] Some Body`) {
		t.Errorf("answering the dialog said:\n%s", got)
	}
}

// TestATextBoxIsNotAButton: llTextBox arrives as an ordinary dialog
// carrying a sentinel where its buttons would be, and a person should
// be shown a request for text rather than the plumbing.
func TestATextBoxIsNotAButton(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.Relay(t, dialogFrom("a sign", "your name?", -104, "!!llTextBox!!"))
	notice := waits(t, x, "a sign asks")
	if strings.Contains(notice, "llTextBox") {
		t.Errorf("the sentinel should not be shown to a person:\n%s", notice)
	}

	got := x.do(t, "waiting")
	if !strings.Contains(got, "text box") || !strings.Contains(got, "type an answer") {
		t.Errorf("a text box should say what it wants:\n%s", got)
	}
	if got := x.do(t, "answer 1"); !strings.Contains(got, "^D ends it") {
		t.Errorf("answering a text box with no text should start collecting: %s", got)
	}
	x.abandon()
	if got := x.do(t, "answer 1 Quark"); !strings.Contains(got, "told") {
		t.Errorf("answering with text: %s", got)
	}
}

// TestATextBoxTakesMoreThanOneLine.
//
// The viewer's text box is a text editor rather than a field, so an
// answer there can have newlines in it, and a shell that reads one line
// at a time cannot type one.  Naming a file is the way in.
//
// Measured on Agni: three lines went out and the script received them
// with the newlines intact -- 33 characters, three lines when parsed on
// "\n" -- and 254 bytes with five newlines arrived whole.
func TestATextBoxTakesMoreThanOneLine(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	path := filepath.Join(t.TempDir(), "answer.txt")
	if err := os.WriteFile(path, []byte("first line\nsecond line\nthird"), 0o600); err != nil {
		t.Fatal(err)
	}

	x.grid.Relay(t, dialogFrom("a sign", "your name?", -106, "!!llTextBox!!"))
	waits(t, x, "a sign asks")

	if got := x.do(t, "answer --file "+path+" 1"); !strings.Contains(got, "told") {
		t.Fatalf("answering from a file: %s", got)
	}
	var sent string
	for _, m := range x.grid.Sent() {
		if r, ok := m.(*msg.ScriptDialogReply); ok {
			sent = strings.TrimRight(string(r.Data.ButtonLabel), "\x00")
		}
	}
	if want := "first line\nsecond line\nthird"; sent != want {
		t.Errorf("the reply carried %q, want %q", sent, want)
	}
}

// TestATextBoxHasALimit: the label the answer travels in is one byte of
// length and 254 of text, and a person is better told before it goes
// than left wondering which half arrived.
func TestATextBoxHasALimit(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	path := filepath.Join(t.TempDir(), "long.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("y", 255)), 0o600); err != nil {
		t.Fatal(err)
	}

	x.grid.Relay(t, dialogFrom("a sign", "your name?", -107, "!!llTextBox!!"))
	waits(t, x, "a sign asks")

	got := x.do(t, "answer --file "+path+" 1")
	if !strings.Contains(got, "too long") || !strings.Contains(got, "254") {
		t.Errorf("an over-long answer should say so and give the limit: %s", got)
	}
	for _, m := range x.grid.Sent() {
		if _, ok := m.(*msg.ScriptDialogReply); ok {
			t.Error("it was sent anyway")
		}
	}
}

// TestIgnoringKeepsItWaiting: the point of ignoring is to stop being
// counted, not to answer or to lose it.
func TestIgnoringKeepsItWaiting(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.Relay(t, dialogFrom("a lamp", "on or off?", -105, "on", "off"))
	waits(t, x, "a lamp asks")

	if got := x.do(t, "ignore 1"); !strings.Contains(got, "still waiting") {
		t.Fatalf("ignore said: %s", got)
	}
	if n := x.waitingCount(); n != 0 {
		t.Errorf("an ignored thing is still counted: %d", n)
	}
	if got := x.do(t, "waiting"); !strings.Contains(got, "nothing waiting") {
		t.Errorf("an ignored thing should be out of the plain listing:\n%s", got)
	}
	got := x.do(t, "waiting -a")
	if !strings.Contains(got, "a lamp") || !strings.Contains(got, "(ignored)") {
		t.Errorf("-a should show it, marked:\n%s", got)
	}
	// And it can still be answered by the number it kept.
	if got := x.do(t, "answer 1 off"); !strings.Contains(got, "pressed") {
		t.Errorf("answering an ignored thing: %s", got)
	}
}

// TestNothingWaitingSaysSo rather than printing nothing, which reads
// like a command that failed quietly.
func TestNothingWaitingSaysSo(t *testing.T) {
	x := newTestShell(t)
	if got := x.do(t, "waiting"); !strings.Contains(got, "nothing waiting") {
		t.Errorf("waiting printed %q", got)
	}
	if got := x.do(t, "answer 1"); !strings.Contains(got, "nothing is waiting") {
		t.Errorf("answering nothing printed %q", got)
	}
}

// answersSent is every reply to a group invitation that reached the
// wire, which is what "no money moved" has to be checked against: the
// accept and the refusal are the only two messages this shell sends at
// an invitation.
func answersSent(x *testShell) []*msg.ImprovedInstantMessage {
	var out []*msg.ImprovedInstantMessage
	for _, m := range x.grid.Sent() {
		im, ok := m.(*msg.ImprovedInstantMessage)
		if !ok {
			continue
		}
		switch im.MessageBlock.Dialog {
		case sl.DialogGroupInvitationAccept, sl.DialogGroupInvitationDecline:
			out = append(out, im)
		}
	}
	return out
}

// entryLine is the line of a listing an entry is on, so that a test
// can say which of the two lines waiting prints for each thing it
// means.
func entryLine(t *testing.T, listing, starts string) string {
	t.Helper()
	for _, l := range strings.Split(listing, "\n") {
		if strings.Contains(l, starts) {
			return l
		}
	}
	t.Fatalf("no line of the listing has %q:\n%s", starts, listing)
	return ""
}

// TestAGroupInvitationIsOneMoreThingWaiting.
//
// It is the reason all of this exists: a group with enrolment closed
// can only be joined by being invited, the invitation arrives as an
// instant message, and before this the shell could list it at best.  A
// free group is the ordinary case and "answer N" takes it.
func TestAGroupInvitationIsOneMoreThingWaiting(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	txn := msg.MustParseUUID("f0367e57-7e57-c0de-e242-539cb34f0bd2")
	x.grid.Relay(t, inviting("quark.idlemind", "Quark invites you to Lorn Family", 0, txn))
	waits(t, x, "invites you into a group")

	got := x.do(t, "waiting")
	if !strings.Contains(got, "1  group") {
		t.Fatalf("an invitation should be one more thing waiting:\n%s", got)
	}
	if !strings.Contains(got, "quark.idlemind") || !strings.Contains(got, "Lorn Family") {
		t.Errorf("the listing should name who invited and what they said:\n%s", got)
	}
	if !strings.Contains(got, "(no fee)") {
		t.Errorf("the listing should say what joining costs:\n%s", got)
	}

	if got := x.do(t, "answer 1"); !strings.Contains(got, "accepted") {
		t.Fatalf("answering a free invitation: %s", got)
	}
	sent := answersSent(x)
	if len(sent) != 1 {
		t.Fatalf("%d answers went out, want 1", len(sent))
	}
	if d := sent[0].MessageBlock.Dialog; d != sl.DialogGroupInvitationAccept {
		t.Errorf("the answer is dialog %d, want %d", d, sl.DialogGroupInvitationAccept)
	}
	if to := sent[0].MessageBlock.ToAgentID; to != testGroup {
		t.Errorf("the answer went to %s, want the group %s", to, testGroup)
	}
	if id := sent[0].MessageBlock.ID; id != txn {
		t.Errorf("the answer quoted %s, want the invitation's %s", id, txn)
	}
	if got := x.do(t, "waiting"); !strings.Contains(got, "nothing waiting") {
		t.Errorf("an answered invitation is still waiting:\n%s", got)
	}
}

// TestABareAnswerWillNotSpendMoney.
//
// A group may charge to join, and accepting is what pays it: the
// simulator takes the fee and nothing in the answer names an amount.
// So "answer N" -- which for every other kind of waiting thing means
// yes -- must not be enough on its own, and the person has to type the
// figure back before anything goes.  This is the test that stands
// between a fee and somebody's balance.
func TestABareAnswerWillNotSpendMoney(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	txn := msg.MustParseUUID("f3597e57-7e57-c0de-5e4f-feef856d5803")
	x.grid.Relay(t, inviting("quark.idlemind", "Quark invites you to Lorn Family", 50, txn))
	waits(t, x, "invites you into a group")

	// On the line the invitation is on, not only in the line under it
	// that says what may be typed: what it costs is part of what is
	// waiting, and a person skimming reads the first line.
	if got := entryLine(t, x.do(t, "waiting"), "1  group"); !strings.Contains(got, "L$50") {
		t.Fatalf("the listing should say what it costs: %s", got)
	}

	got := x.do(t, "answer 1")
	if !strings.Contains(got, "L$50") || !strings.Contains(got, "answer 1 L$50") {
		t.Errorf("the refusal should say the fee and exactly what to type: %s", got)
	}
	if n := len(answersSent(x)); n != 0 {
		t.Fatalf("a bare answer sent %d messages at a group that charges L$50", n)
	}

	// A figure that is not the fee is not an agreement to the fee.
	if got := x.do(t, "answer 1 L$40"); !strings.Contains(got, "not L$40") {
		t.Errorf("the wrong amount should be refused, naming the right one: %s", got)
	}
	if got := x.do(t, "answer 1 fifty"); !strings.Contains(got, "not an amount") {
		t.Errorf("something that is not a number should be refused: %s", got)
	}
	if n := len(answersSent(x)); n != 0 {
		t.Fatalf("%d messages went out before the fee was agreed", n)
	}

	if got := x.do(t, "answer 1 L$50"); !strings.Contains(got, "at L$50") {
		t.Fatalf("the fee typed back should join, and say what it cost: %s", got)
	}
	sent := answersSent(x)
	if len(sent) != 1 || sent[0].MessageBlock.Dialog != sl.DialogGroupInvitationAccept {
		t.Fatalf("%d answers went out: %+v", len(sent), sent)
	}
}

// TestAFeeNobodyStatedIsNotAFeeOfZero.
//
// The bucket a fee arrives in is the one part of an invitation nothing
// here has seen on the wire, and the viewer's own header describes a
// different shape from the one its code parses.  A bucket this does
// not recognise therefore means "the invitation did not say", and the
// safe-looking guess -- reading it as free and joining -- is the one
// that spends money.  It still has to be answerable, or an invitation
// from a simulator with an older idea of the bucket could never be
// taken up at all.
func TestAFeeNobodyStatedIsNotAFeeOfZero(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	m := inviting("quark.idlemind", "Quark invites you to Lorn Family", 0, msg.UUID{4})
	// The shape llinstantmessage.h:52-59 describes: a letter for the
	// status and then the cost, null terminated.
	m.MessageBlock.BinaryBucket = []byte("M0\x00")
	x.grid.Relay(t, m)
	waits(t, x, "invites you into a group")

	if got := entryLine(t, x.do(t, "waiting"), "1  group"); !strings.Contains(got, "did not say what joining costs") {
		t.Fatalf("the listing should admit it does not know the fee: %s", got)
	}
	got := x.do(t, "answer 1")
	if !strings.Contains(got, "answer 1 L$0") {
		t.Errorf("the refusal should say exactly what to type: %s", got)
	}
	if n := len(answersSent(x)); n != 0 {
		t.Fatalf("a bare answer sent %d messages at an invitation of unknown cost", n)
	}

	got = x.do(t, "answer 1 L$0")
	if !strings.Contains(got, "did not say what joining costs") {
		t.Errorf("joining anyway should say the figure was the person's own: %s", got)
	}
	if !strings.Contains(got, "accepted") {
		t.Fatalf("naming an amount should join: %s", got)
	}
	if n := len(answersSent(x)); n != 1 {
		t.Fatalf("%d answers went out, want 1", n)
	}
}

// TestNoDeclinesAGroupInvitation: an invitation left unanswered stays
// open on the other side, so declining has to be a thing that sends
// something rather than a thing that forgets.
func TestNoDeclinesAGroupInvitation(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	txn := msg.MustParseUUID("f16c7e57-7e57-c0de-f489-4d4aee88fd0b")
	x.grid.Relay(t, inviting("quark.idlemind", "Quark invites you to Lorn Family", 50, txn))
	waits(t, x, "invites you into a group")

	// Declining costs nothing, so it needs no figure typed at it even
	// though joining does.
	if got := x.do(t, "no 1"); !strings.Contains(got, "declined the group invitation") {
		t.Fatalf("declining: %s", got)
	}
	sent := answersSent(x)
	if len(sent) != 1 || sent[0].MessageBlock.Dialog != sl.DialogGroupInvitationDecline {
		t.Fatalf("%d answers went out: %+v", len(sent), sent)
	}
	if sent[0].MessageBlock.ToAgentID != testGroup || sent[0].MessageBlock.ID != txn {
		t.Errorf("the refusal went to %s quoting %s", sent[0].MessageBlock.ToAgentID, sent[0].MessageBlock.ID)
	}
	if got := x.do(t, "waiting"); !strings.Contains(got, "nothing waiting") {
		t.Errorf("a declined invitation is still waiting:\n%s", got)
	}
}

// TestAnsweringALureFollowsTheTeleportRatherThanFiringItOff.
//
// A lure is the one teleport whose destination nobody knows in advance:
// the offer says who made it and whatever they typed with it, and the
// region is not a field of the message at all.  So the arrival is read
// back afterwards, and the line before it is said before the request
// goes -- accepting waits for the avatar to be there, which is half a
// second at best and has been measured at five.
func TestAnsweringALureFollowsTheTeleportRatherThanFiringItOff(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)
	x.grid.AnswerTeleport(t, "Sandbox Goguen", goguenHandle)

	x.grid.Relay(t, imFrom(testSomebody, "Some Body", sl.DialogTeleportLure, "come and see"))
	waits(t, x, "come and see")

	// The listing says what accepting will do, since a teleport is the
	// one offer here that moves the avatar.
	if got := x.do(t, "waiting"); !strings.Contains(got, "waits for the arrival") {
		t.Errorf("the listing should say what answering a lure does:\n%s", got)
	}

	got := x.do(t, "answer 1")
	accepting := strings.Index(got, "accepting a teleport from Some Body")
	arrived := strings.Index(got, "arrived in Sandbox Goguen at")
	if accepting < 0 || arrived < 0 || accepting > arrived {
		t.Fatalf("answering a lure should say what it is doing and then where it arrived:\n%s", got)
	}

	var asked *msg.TeleportLureRequest
	for _, m := range x.grid.Sent() {
		if r, ok := m.(*msg.TeleportLureRequest); ok {
			asked = r
		}
	}
	if asked == nil {
		t.Fatal("nothing accepted the lure")
	}
	if asked.Info.LureID != testSomebody {
		t.Errorf("the lure answered was %s", asked.Info.LureID)
	}
	if got := x.do(t, "waiting"); !strings.Contains(got, "nothing waiting") {
		t.Errorf("an accepted lure is still waiting:\n%s", got)
	}
}

// asking builds a script's request for permission, which arrives on
// its own message rather than as an instant message.
func asking(object string, wants sl.Perms) *msg.ScriptQuestion {
	q := &msg.ScriptQuestion{}
	q.Data.TaskID = testLamp
	q.Data.ItemID = testProbe
	q.Data.ObjectName = append([]byte(object), 0)
	q.Data.ObjectOwner = append([]byte("Quark Idlemind"), 0)
	q.Data.Questions = int32(wants)
	return q
}

// waitForAsked waits for a relayed request to have been recorded,
// since it is read off the wire on the session's own goroutine.
func waitForAsked(t *testing.T, x *testShell, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(x.s.Asked()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d permission requests, have %d", n, len(x.s.Asked()))
		}
		time.Sleep(time.Millisecond)
	}
}

// inventoryAnswers is every acceptance or refusal of an inventory
// offer that reached the wire, which is what "answered once" has to be
// counted against.
func inventoryAnswers(x *testShell) []*msg.ImprovedInstantMessage {
	var out []*msg.ImprovedInstantMessage
	for _, m := range x.grid.Sent() {
		im, ok := m.(*msg.ImprovedInstantMessage)
		if !ok {
			continue
		}
		switch im.MessageBlock.Dialog {
		case sl.DialogInventoryAccepted, sl.DialogInventoryDeclined:
			out = append(out, im)
		}
	}
	return out
}

// scriptAnswers is every answer to a script's request for permission
// that reached the wire.  There is no ScriptAnswerNo: a refusal is this
// message with no bits set.
func scriptAnswers(x *testShell) []*msg.ScriptAnswerYes {
	var out []*msg.ScriptAnswerYes
	for _, m := range x.grid.Sent() {
		if a, ok := m.(*msg.ScriptAnswerYes); ok {
			out = append(out, a)
		}
	}
	return out
}

// TestAnAnsweredInventoryOfferStopsWaiting.
//
// answer and no called into the session directly, where accept and
// decline call the offer's own Accept and Decline -- and only those
// two tell the session that the offer is spent.  So an offer answered
// by its number stayed in the listing under that number, stayed
// counted at the prompt, and could be answered a second time: another
// acceptance of the same offer, sent to somebody who had already had
// one.
func TestAnAnsweredInventoryOfferStopsWaiting(t *testing.T) {
	x := newTestShell(t)

	x.grid.Relay(t, offering(testFriend, "A Friend", "a lamp", sl.AssetObject, testLamp))
	waitForOffers(t, x, 0, 1)
	if got := x.do(t, "waiting"); !strings.Contains(got, "1  inventory") {
		t.Fatalf("an offer should be one thing waiting:\n%s", got)
	}

	if got := x.do(t, "answer 1"); !strings.Contains(got, `took "a lamp"`) {
		t.Fatalf("answering an inventory offer: %s", got)
	}
	if got := x.do(t, "waiting"); !strings.Contains(got, "nothing waiting") {
		t.Errorf("an accepted offer is still waiting:\n%s", got)
	}
	if n := x.waitingCount(); n != 0 {
		t.Errorf("an accepted offer is still counted at the prompt: %d", n)
	}
	if got := x.do(t, "answer 1"); !strings.Contains(got, "nothing is waiting") {
		t.Errorf("an accepted offer could be accepted again: %s", got)
	}
	if n := len(inventoryAnswers(x)); n != 1 {
		t.Errorf("%d answers reached the grid, want the one", n)
	}
}

// TestARefusedInventoryOfferStopsWaiting is the same hole in "no",
// where it has a second edge: declining also stops a thing being
// ignored, so an offer that had been set aside came back into the
// count at the prompt by being refused, which is the opposite of what
// was wanted.
func TestARefusedInventoryOfferStopsWaiting(t *testing.T) {
	x := newTestShell(t)

	x.grid.Relay(t, offering(testFriend, "A Friend", "a lamp", sl.AssetObject, testLamp))
	waitForOffers(t, x, 0, 1)
	x.do(t, "ignore 1")

	if got := x.do(t, "no 1"); !strings.Contains(got, `declined "a lamp"`) {
		t.Fatalf("declining an inventory offer: %s", got)
	}
	if n := x.waitingCount(); n != 0 {
		t.Errorf("a refused offer is counted at the prompt again: %d", n)
	}
	if got := x.do(t, "waiting -a"); !strings.Contains(got, "nothing waiting") {
		t.Errorf("a refused offer is still waiting:\n%s", got)
	}
	if got := x.do(t, "no 1"); !strings.Contains(got, "nothing is waiting") {
		t.Errorf("a refused offer could be refused again: %s", got)
	}
	if n := len(inventoryAnswers(x)); n != 1 {
		t.Errorf("%d refusals reached the grid, want the one", n)
	}
}

// TestAnAnsweredPermissionStopsWaiting.
//
// This one was never forgotten anywhere: the session appended each
// request to a list nothing ever removed from, so every script that
// had ever asked stayed listed, counted and answerable for the life of
// the shell -- and granting twice grants twice.
func TestAnAnsweredPermissionStopsWaiting(t *testing.T) {
	x := newTestShell(t)

	x.grid.Relay(t, asking("a lamp", sl.PermissionTakeControls))
	waitForAsked(t, x, 1)
	if got := x.do(t, "waiting"); !strings.Contains(got, "1  permission") {
		t.Fatalf("a request should be one thing waiting:\n%s", got)
	}

	if got := x.do(t, "answer 1"); !strings.Contains(got, "granted take controls") {
		t.Fatalf("granting a permission: %s", got)
	}
	if got := x.do(t, "waiting"); !strings.Contains(got, "nothing waiting") {
		t.Errorf("a granted permission is still waiting:\n%s", got)
	}
	if n := x.waitingCount(); n != 0 {
		t.Errorf("a granted permission is still counted at the prompt: %d", n)
	}
	if got := x.do(t, "answer 1"); !strings.Contains(got, "nothing is waiting") {
		t.Errorf("a granted permission could be granted again: %s", got)
	}

	// And a refusal is an answer too, which is worth its own half: it
	// goes out as the same message with no bits set, so a listing that
	// kept it would offer to refuse it again.
	x.grid.Relay(t, asking("a door", sl.PermissionDebit))
	waitForAsked(t, x, 1)
	if got := x.do(t, "no 1"); !strings.Contains(got, "refused debit") {
		t.Fatalf("refusing a permission: %s", got)
	}
	if got := x.do(t, "waiting"); !strings.Contains(got, "nothing waiting") {
		t.Errorf("a refused permission is still waiting:\n%s", got)
	}

	sent := scriptAnswers(x)
	if len(sent) != 2 {
		t.Fatalf("%d answers reached the grid, want one each", len(sent))
	}
	if got := sl.Perms(sent[0].Data.Questions); got != sl.PermissionTakeControls {
		t.Errorf("the grant sent %s", got)
	}
	if got := sl.Perms(sent[1].Data.Questions); got != 0 {
		t.Errorf("the refusal sent %s, want nothing", got)
	}
}
