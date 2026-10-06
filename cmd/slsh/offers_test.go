package main

// What slgod kept, as a shell shows it.
//
// keptGrid is the fake grid with a daemon's record of offers behind it:
// what was waiting when the shell attached, what the daemon answers when
// the shell says it is dealing with one, and its word that another
// client dealt with one.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

type keptGrid struct {
	*fakeGrid

	record *sl.OfferRecord
	kept   []*sl.Message

	mu     sync.Mutex
	asked  []string
	answer func(key string) (bool, *sl.Handled)

	notices chan *sl.Handled
}

var _ sl.OfferKeeper = (*keptGrid)(nil)

func (k *keptGrid) KeptOffers() (*sl.OfferRecord, []*sl.Message, bool) {
	return k.record, k.kept, k.record != nil
}

func (k *keptGrid) Handled(_ context.Context, key, how string) (bool, *sl.Handled, error) {
	k.mu.Lock()
	k.asked = append(k.asked, key+" "+how)
	answer := k.answer
	k.mu.Unlock()
	if answer == nil {
		return true, nil, nil
	}
	claimed, earlier := answer(key)
	return claimed, earlier, nil
}

func (k *keptGrid) Unhandled(context.Context, string) error { return nil }

func (k *keptGrid) HandledOffers() <-chan *sl.Handled { return k.notices }

func (k *keptGrid) Asked() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.asked...)
}

// newKeptShell is a shell attached to a daemon that had kept these.
func newKeptShell(t *testing.T, record *sl.OfferRecord, kept ...*sl.Message) (*testShell, *keptGrid) {
	t.Helper()
	k := &keptGrid{fakeGrid: newFakeGrid(t), record: record, kept: kept, notices: make(chan *sl.Handled)}
	x := newTestShellOn(t, k, Config{Addr: "fake:7807", Prefix: 27})
	x.grid = k.fakeGrid
	return x, k
}

// kept is an offer as the daemon names it: out of its record when
// recorded, off the wire otherwise.
func kept(t *testing.T, key string, at time.Time, recorded bool, m *msg.ImprovedInstantMessage) *sl.Message {
	t.Helper()
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return &sl.Message{ID: msg.IDOf(m), Name: "ImprovedInstantMessage", Body: body, At: at,
		Offer: key, Recorded: recorded}
}

// relayKept hands the session a live offer the daemon has named, and
// waits for it to be listed.
func relayKept(t *testing.T, x *testShell, k *keptGrid, m *sl.Message) {
	t.Helper()
	before := len(x.waiters())
	select {
	case k.msgs <- m:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing read the relay")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(x.waiters()) <= before {
		if time.Now().After(deadline) {
			t.Fatal("the offer was never listed")
		}
		time.Sleep(time.Millisecond)
	}
}

var testLurer = msg.MustParseUUID("41497e57-7e57-c0de-8e40-535e4340746b")

func luring(txn msg.UUID) *msg.ImprovedInstantMessage {
	m := imFrom(testLurer, "Example Lurer", sl.DialogTeleportLure, "come over")
	m.MessageBlock.ID = txn
	return m
}

// TestWaitingListsWhatSlgodKeptFromBeforeTheShell is the failure as a
// person met it: two group invitations sent while no client was
// attached, and a shell started afterwards that said "nothing waiting".
// What the daemon kept is listed, said to be from before this shell --
// it was not seen arriving, and should not be taken for news -- and the
// banner says how many there are before anybody asks.
func TestWaitingListsWhatSlgodKeptFromBeforeTheShell(t *testing.T) {
	then := time.Now().Add(-time.Hour)
	x, _ := newKeptShell(t, &sl.OfferRecord{Since: then.Add(-time.Hour), Kept: 2, Limit: 100},
		kept(t, "group:1", then, true, inviting("Example Inviter", "join us", 0, testRole)),
		kept(t, "lure:1", then, true, luring(testLurer)),
	)

	got := x.do(t, "waiting")
	for _, want := range []string{"group", "teleport"} {
		line := entryLine(t, got, want)
		if !strings.Contains(line, "(from before this shell)") {
			t.Errorf("the %s kept by slgod is not marked as from before this shell: %q", want, line)
		}
	}

	x.out.Reset()
	x.banner()
	if b := x.out.String(); !strings.Contains(b, "2 things are waiting from before this shell attached") {
		t.Errorf("the banner said %q", b)
	}
}

// TestNothingWaitingSaysWhatItIsAnAccountOf: "nothing waiting" on its
// own reads as an answer, and it is only as good as what there was to
// hear.  Each of the four situations says which it is.
func TestNothingWaitingSaysWhatItIsAnAccountOf(t *testing.T) {
	since := time.Now().Add(-2 * time.Hour)

	x, _ := newKeptShell(t, &sl.OfferRecord{Since: since, Limit: 100})
	got := x.do(t, "waiting")
	if !strings.Contains(got, "nothing waiting -- slgod has kept every offer") ||
		!strings.Contains(got, since.Local().Format("15:04")) ||
		!strings.Contains(got, "permission request") {
		t.Errorf("with a record, waiting said %q", got)
	}
	if got := x.do(t, "offers"); !strings.Contains(got, "no offers waiting -- slgod has kept") ||
		strings.Contains(got, "dialog") {
		t.Errorf("with a record, offers said %q", got)
	}

	full, _ := newKeptShell(t, &sl.OfferRecord{Since: since, Evicted: 3, Limit: 100})
	if got := full.do(t, "waiting"); !strings.Contains(got, "dropped 3 older ones") {
		t.Errorf("with a record that had overflowed, waiting said %q", got)
	}

	none := newTestShell(t)
	if got := none.do(t, "waiting"); !strings.Contains(got, "keeps no record of offers") {
		t.Errorf("with no record, waiting said %q", got)
	}

	direct := newTestShellOn(t, newFakeGrid(t), Config{Direct: true, Prefix: 27})
	if got := direct.do(t, "waiting"); !strings.Contains(got, "since it logged in") {
		t.Errorf("logged in directly, waiting said %q", got)
	}
}

// TestAnOfferAnsweredInAnotherClientIsNotAnsweredHere: slbotd and a
// shell attached to one avatar, one offer.  The daemon's word that the
// other client dealt with it takes it out of the listing and is said at
// once; and a shell that asks to answer one the other client has
// already answered is told who, and sends nothing.
func TestAnOfferAnsweredInAnotherClientIsNotAnsweredHere(t *testing.T) {
	x, k := newKeptShell(t, &sl.OfferRecord{Since: time.Now(), Limit: 100})
	watching(t, x)

	relayKept(t, x, k, kept(t, "lure:7", time.Now(), false, luring(testLurer)))
	select {
	case k.notices <- &sl.Handled{Key: "lure:7", How: "accepted", By: "slbotd", At: time.Now()}:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing read the notices")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(x.out.String(), "was accepted by slbotd -- it is no longer waiting") {
		if time.Now().After(deadline) {
			t.Fatalf("the shell never said the offer was answered elsewhere: %q", x.out.String())
		}
		time.Sleep(time.Millisecond)
	}
	if got := x.do(t, "waiting"); !strings.Contains(got, "nothing waiting") {
		t.Errorf("an offer answered elsewhere is still listed: %q", got)
	}

	k.mu.Lock()
	k.answer = func(key string) (bool, *sl.Handled) {
		return false, &sl.Handled{Key: key, How: "accepted", By: "slbotd", At: time.Now()}
	}
	k.mu.Unlock()
	relayKept(t, x, k, kept(t, "inventory:7", time.Now(), false,
		offering(testFriend, "A Friend", "a lamp", sl.AssetObject, testLamp)))
	got := x.do(t, "answer 1")
	if !strings.Contains(got, "was already accepted by slbotd") || !strings.Contains(got, "nothing was sent") {
		t.Errorf("answering an offer already answered said %q", got)
	}
	if n := len(inventoryAnswers(x)); n != 0 {
		t.Errorf("%d answers went out for an offer already answered", n)
	}
}

// TestNoToATeleportRequestSettlesItForEveryClient: refusing a request
// sends nothing to the grid, which made it invisible to every other
// client of the avatar -- each went on listing a question somebody had
// already answered.  It is told to the daemon instead.
func TestNoToATeleportRequestSettlesItForEveryClient(t *testing.T) {
	x, k := newKeptShell(t, &sl.OfferRecord{Since: time.Now(), Limit: 100})
	relayKept(t, x, k, kept(t, "tprequest:8", time.Now(), false,
		imFrom(testLurer, "Example Lurer", sl.DialogTeleportRequest, "may I")))

	if got := x.do(t, "no 1"); !strings.Contains(got, "left Example Lurer unanswered") {
		t.Fatalf("no said %q", got)
	}
	if got := k.Asked(); len(got) != 1 || got[0] != "tprequest:8 refused" {
		t.Errorf("the daemon was told %q", got)
	}
}

// TestThePromptCountFollowsWhatArrivesAndWhatGoes: the count at the
// prompt is what tells somebody mid-conversation that something wants
// an answer, and it was worked out only after a command.  A dialog, a
// permission request and an offer each put it up as they are announced,
// on whichever goroutine announces them, and an offer answered in
// another client takes it down again.
func TestThePromptCountFollowsWhatArrivesAndWhatGoes(t *testing.T) {
	x, k := newKeptShell(t, &sl.OfferRecord{Since: time.Now(), Limit: 100})
	watching(t, x)
	x.prompt()

	becomes := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			x.term.mu.Lock()
			got := x.term.prompt
			x.term.mu.Unlock()
			if got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the prompt is %q, want %q", got, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	becomes("/$ ")

	x.grid.Relay(t, dialogFrom("a lamp", "pick one", -4242, "Yes", "No"))
	becomes("(1) /$ ")
	x.grid.Relay(t, asking("a lamp", sl.PermissionAttach))
	becomes("(2) /$ ")
	select {
	case k.msgs <- kept(t, "lure:9", time.Now(), false, luring(testLurer)):
	case <-time.After(5 * time.Second):
		t.Fatal("nothing read the relay")
	}
	becomes("(3) /$ ")

	select {
	case k.notices <- &sl.Handled{Key: "lure:9", How: "accepted", By: "slbotd", At: time.Now()}:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing read the notices")
	}
	becomes("(2) /$ ")
}

// keptDialogMsg is a script dialog as the daemon names it: off the wire,
// under a key.
func keptDialogMsg(t *testing.T, key string, channel int32, buttons ...string) *sl.Message {
	t.Helper()
	m := dialogFrom("a lamp", "on or off?", channel, buttons...)
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return &sl.Message{ID: msg.IDOf(m), Name: "ScriptDialog", Body: body, At: time.Now(), Offer: key}
}

func dialogReplies(x *testShell) int {
	n := 0
	for _, m := range x.grid.Sent() {
		if _, ok := m.(*msg.ScriptDialogReply); ok {
			n++
		}
	}
	return n
}

// TestNoToADialogIgnoresItForEveryClient: the viewer's Ignore sends
// nothing, so the dialog stayed in slgod's list for every other client
// for an hour.  It is told to the daemon instead, and nothing is sent.
func TestNoToADialogIgnoresItForEveryClient(t *testing.T) {
	x, k := newKeptShell(t, &sl.OfferRecord{Since: time.Now(), Limit: 100})
	relayKept(t, x, k, keptDialogMsg(t, "dialog:3", -4300, "on", "off"))

	got := x.do(t, "no 1")
	for _, want := range []string{"ignored the dialog from [Object] a lamp", "nothing is sent", "slgod stops listing it for every program"} {
		if !strings.Contains(got, want) {
			t.Errorf("no said %q, want %q in it", got, want)
		}
	}
	if a := k.Asked(); len(a) != 1 || a[0] != "dialog:3 ignored" {
		t.Errorf("the daemon was told %q", a)
	}
	if n := dialogReplies(x); n != 0 {
		t.Errorf("%d replies went to the grid", n)
	}
	if got := x.do(t, "waiting"); !strings.Contains(got, "nothing waiting") {
		t.Errorf("the dialog is still listed: %q", got)
	}
}

// TestNoToATextBoxIgnoresItToo is the same for llTextBox.
func TestNoToATextBoxIgnoresItToo(t *testing.T) {
	x, k := newKeptShell(t, &sl.OfferRecord{Since: time.Now(), Limit: 100})
	relayKept(t, x, k, keptDialogMsg(t, "dialog:4", -4301, "!!llTextBox!!"))

	if got := x.do(t, "no 1"); !strings.Contains(got, "ignored the text box from [Object] a lamp") {
		t.Errorf("no said %q", got)
	}
	if a := k.Asked(); len(a) != 1 || a[0] != "dialog:4 ignored" {
		t.Errorf("the daemon was told %q", a)
	}
	if n := dialogReplies(x); n != 0 {
		t.Errorf("%d replies went to the grid", n)
	}
}

// TestIgnoreOnADialogStaysThisShellsOwn: ignore does not reach the daemon.
func TestIgnoreOnADialogStaysThisShellsOwn(t *testing.T) {
	x, k := newKeptShell(t, &sl.OfferRecord{Since: time.Now(), Limit: 100})
	relayKept(t, x, k, keptDialogMsg(t, "dialog:5", -4302, "on", "off"))

	if got := x.do(t, "ignore 1"); !strings.Contains(got, "still waiting") {
		t.Errorf("ignore said %q", got)
	}
	if a := k.Asked(); len(a) != 0 {
		t.Errorf("the daemon was told %q", a)
	}
	if got := x.do(t, "waiting -a"); !strings.Contains(got, "dialog") {
		t.Errorf("an ignored dialog is not listed with -a: %q", got)
	}
}
