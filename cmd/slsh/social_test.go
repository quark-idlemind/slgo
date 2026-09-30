package main

// Talking, and everybody there is to talk to.
//
// Nearly all of this is about what somebody sees: a remark arriving
// while a command is being typed, an offer that has to be answered
// before it can be accepted, a conversation opening because somebody
// else started it.  None of it can be checked by reading the code, and
// the live version of the check is asking a friend to say something --
// so it went unchecked, and the parts that only happen when two people
// are talking at once went unchecked hardest.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Two more people, since most of what is here needs somebody to talk to
// and several of the refusals need two of them.
var (
	testFriend = msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000002")
	testOther  = msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000003")
)

// watching starts the printer the shell runs for as long as it is up,
// and stops it when the test ends.
//
// Everything heard arrives on it rather than as the answer to a
// command, so a test about arriving chat has to have it running -- and
// has to wait for what it prints, since it prints on its own goroutine.
func watching(t *testing.T, x *testShell) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); x.watch(ctx) }()

	// The subscriptions are made on the session's own goroutine, so
	// anything relayed before they exist is delivered to nobody and is
	// gone.  A line printed is the proof that they are all there: the
	// printer only reaches its loop once it has made every one of them.
	probe := &msg.ChatFromSimulator{}
	probe.ChatData.SourceID = testLamp
	probe.ChatData.FromName = append([]byte("a probe"), 0)
	probe.ChatData.Message = append([]byte("is anybody listening"), 0)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(x.out.String(), "is anybody listening") {
		if time.Now().After(deadline) {
			t.Fatal("the chat printer never subscribed to anything")
		}
		x.grid.Relay(t, probe)
		time.Sleep(time.Millisecond)
	}
	x.out.Reset()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the chat printer did not stop with its context")
		}
	})
}

// waits until the screen says something, and gives up rather than
// hanging when it never does.
func waits(t *testing.T, x *testShell, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := x.out.String()
		if strings.Contains(got, want) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %q; the screen says:\n%s", want, got)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// imFrom builds one instant message.
//
// Every kind is this message with a different dialog -- a remark, a
// friendship offer, an inventory offer, a typing notification -- which
// is why the shell has one place that sorts them out.
func imFrom(from msg.UUID, name string, dialog uint8, text string) *msg.ImprovedInstantMessage {
	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID = from
	m.MessageBlock.Dialog = dialog
	m.MessageBlock.FromAgentName = append([]byte(name), 0)
	m.MessageBlock.Message = append([]byte(text), 0)
	// The transaction, which is the only thing an answer can quote.  One
	// per sender is enough here and keeps two offers apart.
	m.MessageBlock.ID = from
	return m
}

// offering is an inventory offer, which carries what is being offered
// in its binary bucket rather than in its text.
func offering(from msg.UUID, name, what string, asset sl.AssetType, item msg.UUID) *msg.ImprovedInstantMessage {
	m := imFrom(from, name, sl.DialogInventoryOffered, what)
	m.MessageBlock.BinaryBucket = append([]byte{byte(asset)}, item[:]...)
	return m
}

// knows tells the session what somebody is called without anybody
// having said anything, which is what a name reply is.
func knows(t *testing.T, x *testShell, names map[msg.UUID]string) {
	t.Helper()
	r := &msg.UUIDNameReply{}
	for id, n := range names {
		first, last, _ := strings.Cut(n, " ")
		r.UUIDNameBlock = append(r.UUIDNameBlock, msg.UUIDNameReply_UUIDNameBlock{
			ID: id, FirstName: append([]byte(first), 0), LastName: append([]byte(last), 0),
		})
	}
	x.grid.Relay(t, r)
	// The reply is handled on the session's own goroutine, so the name
	// is not there the instant the relay returns.
	deadline := time.Now().Add(5 * time.Second)
	for id := range names {
		for x.s.Name(id) == "" {
			if time.Now().After(deadline) {
				t.Fatalf("the session never learnt who %s is", id)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// ------------------------------------------------------------ the list

// TestConversationsKeepLocalFirstAndComeBackToIt.
//
// Tab cycles, and the cycle has to be a cycle: somebody who has opened
// three conversations and wants to say something out loud presses tab
// until Local comes round again.
func TestConversationsKeepLocalFirstAndComeBackToIt(t *testing.T) {
	cs := NewConversations()
	if got := cs.Current(); !got.Local() || got.Label() != "Local" {
		t.Fatalf("a fresh list starts at %q", got.Label())
	}

	c, made := cs.Open(testSomebody, "Some Body")
	if !made {
		t.Error("the first conversation with somebody has to be made")
	}
	if again, made := cs.Open(testSomebody, "Some Body"); made || again != c {
		t.Error("opening the same conversation twice should find the first one")
	}
	// A name that has arrived since is taken: an id is all an IM
	// carries until somebody says who it was from.
	if got, _ := cs.Open(testSomebody, "Somebody Else"); got.Label() != "Somebody Else" {
		t.Errorf("a better name should be taken, got %q", got.Label())
	}

	other, _ := cs.Open(testFriend, "A Friend")
	cs.Switch(other)
	if cs.Current() != other {
		t.Error("switching should land on the one asked for")
	}
	// Switching to one that is not in the list leaves the current one
	// alone rather than picking something.
	cs.Switch(&Conversation{Target: testOther, Name: "Not Listed"})
	if cs.Current() != other {
		t.Error("a conversation that is not in the list is not somewhere to switch to")
	}

	if got := cs.Next(); !got.Local() {
		t.Errorf("the cycle should come back round to Local, got %q", got.Label())
	}
	list, cur := cs.All()
	if len(list) != 3 || cur != 0 {
		t.Errorf("the list is %d long at %d, want 3 at 0", len(list), cur)
	}
}

// ------------------------------------------------------------ listening

// TestWhatIsHeardIsPrintedWhateverModeIsInForce, since arriving chat
// has nothing to do with what the keyboard is for.
func TestWhatIsHeardIsPrintedWhateverModeIsInForce(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	m := &msg.ChatFromSimulator{}
	m.ChatData.SourceID = testSomebody
	m.ChatData.SourceType = sl.SourceAgent
	m.ChatData.FromName = append([]byte("Some Body"), 0)
	m.ChatData.Message = append([]byte("hello there"), 0)
	x.grid.Relay(t, m)

	if got := waits(t, x, "hello there"); !strings.Contains(got, "< [Local] Some Body: hello there") {
		t.Errorf("chat printed:\n%s", got)
	}

	// Our own words come back from the simulator and must not be shown
	// twice: send already printed them.
	x.out.Reset()
	mine := &msg.ChatFromSimulator{}
	mine.ChatData.SourceID = testMe
	mine.ChatData.FromName = append([]byte("Quark Idlemind"), 0)
	mine.ChatData.Message = append([]byte("an echo of what we said"), 0)
	x.grid.Relay(t, mine)
	x.grid.Relay(t, m)

	got := waits(t, x, "hello there")
	if strings.Contains(got, "an echo") {
		t.Errorf("our own words came back onto the screen:\n%s", got)
	}
}

// TestWhatIsHeardIsShownRatherThanObeyed.
//
// Chat and instant messages are a stranger's bytes, name and all, and
// the terminal obeys an escape sequence wherever it comes from: this
// one would clear the screen, set the window title and write to the
// clipboard.  What reaches the screen has to be the text of them, and
// the lines of a message of several lines still have to be lines.
func TestWhatIsHeardIsShownRatherThanObeyed(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	m := &msg.ChatFromSimulator{}
	m.ChatData.SourceID = testSomebody
	m.ChatData.SourceType = sl.SourceAgent
	m.ChatData.FromName = append([]byte("Some\x1b]0;a title\x07Body"), 0)
	m.ChatData.Message = append([]byte("hello\x1b[2J\x1b[H\x1b]52;c;aGk=\x07\r\nsecond line\rover it"), 0)
	x.grid.Relay(t, m)

	got := waits(t, x, "second line")
	if strings.ContainsAny(got, "\x1b\x07\r") {
		t.Errorf("a control character reached the terminal:\n%q", got)
	}
	for _, want := range []string{
		"< [Local] Some^[]0;a title^GBody: hello^[[2J^[[H^[]52;c;aGk=^G\n",
		"\nsecond line^Mover it\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the screen should say %q:\n%q", want, got)
		}
	}

	x.out.Reset()
	x.grid.Relay(t, imFrom(testSomebody, "Some\x1b[8mBody", sl.DialogMessage, "psst\x1b[6n\u009b6n"))
	got = waits(t, x, "psst")
	if strings.ContainsAny(got, "\x1b\u009b") {
		t.Errorf("a control character in an IM reached the terminal:\n%q", got)
	}
	if !strings.Contains(got, "< [IM Some^[[8mBody] psst^[[6nM-^[6n") {
		t.Errorf("the IM should be shown as text:\n%q", got)
	}
}

// TestAnInstantMessageOpensAConversationAndSaysSo.
//
// The notice is the point: an IM from somebody the shell has never
// heard from is a new place for typing to go, and tab now has one more
// stop on it.
func TestAnInstantMessageOpensAConversationAndSaysSo(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.Relay(t, imFrom(testSomebody, "Some Body", sl.DialogMessage, "are you there"))
	got := waits(t, x, "are you there")
	if !strings.Contains(got, "new conversation with Some Body") {
		t.Errorf("the first message should say a conversation was opened:\n%s", got)
	}
	if !strings.Contains(got, "< [IM Some Body] are you there") {
		t.Errorf("an IM printed:\n%s", got)
	}

	// The second one is not a new conversation.
	x.out.Reset()
	x.grid.Relay(t, imFrom(testSomebody, "Some Body", sl.DialogMessage, "still there"))
	if got := waits(t, x, "still there"); strings.Contains(got, "new conversation") {
		t.Errorf("the same person twice is one conversation:\n%s", got)
	}

	// A message that carries no name is from whoever the session knows
	// that id to be, and from the id itself when it knows nobody.
	x.out.Reset()
	x.grid.Relay(t, imFrom(testOther, "", sl.DialogMessage, "no name on this one"))
	if got := waits(t, x, "no name on this one"); !strings.Contains(got, "(cccccccc)") {
		t.Errorf("an unnamed sender should be printed as the id:\n%s", got)
	}
}

// TestAnObjectsMessageIsShownAsAnObjects.
//
// A script's message carries its owner's id and any name the object
// has.  Taken for conversation it would open one with the owner under
// a name that may be somebody else's, and what was typed there would
// go to the owner.
func TestAnObjectsMessageIsShownAsAnObjects(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	object := msg.MustParseUUID("6dc67e57-7e57-c0de-132d-c92f933df25c")
	m := imFrom(testSomebody, "A Friend", sl.DialogFromTask, "the door is open")
	m.MessageBlock.ID = object
	x.grid.Relay(t, m)

	got := waits(t, x, "the door is open")
	if !strings.Contains(got, "< [Object] A Friend: the door is open") {
		t.Errorf("an object's message printed:\n%s", got)
	}
	if strings.Contains(got, "[IM ") || strings.Contains(got, "new conversation") {
		t.Errorf("an object's message was taken for a conversation:\n%s", got)
	}
	if list, _ := x.talk.All(); len(list) != 1 {
		t.Errorf("%d conversations, want only Local", len(list))
	}

	// With no name on it, it is the object's key: never the owner's
	// name, which is somebody else.
	knows(t, x, map[msg.UUID]string{testSomebody: "Some Body"})
	x.out.Reset()
	m = imFrom(testSomebody, "", sl.DialogFromTask, "no name on this one")
	m.MessageBlock.ID = object
	x.grid.Relay(t, m)
	if got := waits(t, x, "no name on this one"); !strings.Contains(got, "< [Object] (6dc67e57): no name") {
		t.Errorf("an unnamed object's message printed:\n%s", got)
	}
}

// TestNothingButAPersonIsPrintedAsOne: every dialog an instant message
// can carry, sent by an object, a group or the grid under a person's
// name, is printed with the name labelled as what sent it.  A kind
// printed with the name bare fails here, whichever it is.
// Why: doc/im-senders.md#labelling-a-sender
func TestNothingButAPersonIsPrintedAsOne(t *testing.T) {
	x := newTestShell(t)
	const name = "A Friend"
	region := msg.MustParseUUID("36fa7e57-7e57-c0de-9e3a-f16236c34d10")
	labelled := 0
	for d := 0; d < 256; d++ {
		for _, how := range []struct {
			what string
			set  func(*sl.IM)
		}{
			{"as it came", func(*sl.IM) {}},
			{"as a group", func(m *sl.IM) { m.Group = true }},
			{"with no sender", func(m *sl.IM) { m.From = msg.UUID{} }},
		} {
			m := &sl.IM{
				From: testSomebody, FromName: name, Dialog: uint8(d), ID: testLamp,
				Region: region, Text: "a remark",
			}
			how.set(m)
			x.out.Reset()
			x.heard(m)
			k := m.Sender()
			if k == sl.SenderPerson {
				continue
			}
			for _, line := range strings.Split(x.out.String(), "\n") {
				if !strings.Contains(line, name) {
					continue
				}
				labelled++
				if !strings.Contains(line, k.Label(name)) {
					t.Errorf("dialog %d, %s: %q names %s without saying it is not a person",
						d, how.what, line, name)
				}
			}
		}
	}
	if labelled == 0 {
		t.Fatal("nothing labelled was printed, so this checked nothing")
	}

	// The ones seen on the grid, whose name is the object's.
	for d, want := range map[uint8]string{
		sl.DialogFromTask:             "< [Object] A Friend: a remark",
		sl.DialogTaskInventoryOffered: "* object inventory offer from [Object] A Friend: a remark",
		sl.DialogFromTaskAsAlert:      "* object alert from [Object] A Friend: a remark",
	} {
		x.out.Reset()
		x.heard(&sl.IM{From: testSomebody, FromName: name, Dialog: d, ID: testLamp, Region: region, Text: "a remark"})
		if got := x.out.String(); !strings.Contains(got, want) {
			t.Errorf("dialog %d printed %q, want %q", d, got, want)
		}
	}
}

// TestChatSaysWhatSpoke: an object in local chat can be called anything,
// a person's name included, and the simulator's own lines are neither.
func TestChatSaysWhatSpoke(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	for _, c := range []struct {
		kind uint8
		name string
		text string
		want string
	}{
		{sl.SourceAgent, "Some Body", "said by a person", "< [Local] Some Body: said by a person"},
		{sl.SourceObject, "Some Body", "said by an object", "< [Local] [Object] Some Body: said by an object"},
		{sl.SourceSystem, "Second Life", "said by the grid", "< [Local] [Grid] Second Life: said by the grid"},
	} {
		m := &msg.ChatFromSimulator{}
		m.ChatData.SourceID = testSomebody
		m.ChatData.SourceType = c.kind
		m.ChatData.FromName = append([]byte(c.name), 0)
		m.ChatData.Message = append([]byte(c.text), 0)
		x.grid.Relay(t, m)
		if got := waits(t, x, c.text); !strings.Contains(got, c.want) {
			t.Errorf("chat printed:\n%s\nwant %q", got, c.want)
		}
	}
}

// TestADoNotDisturbReplyIsShownAsOne: the far viewer sent it by itself,
// so it is said to be that rather than shown as the person talking.
func TestADoNotDisturbReplyIsShownAsOne(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.Relay(t, imFrom(testFriend, "A Friend", sl.DialogDoNotDisturbAutoResponse, "away until evening"))
	got := waits(t, x, "away until evening")
	if !strings.Contains(got, "* do not disturb auto response from A Friend: away until evening") {
		t.Errorf("an auto response printed:\n%s", got)
	}
	if strings.Contains(got, "new conversation") {
		t.Errorf("an auto response opened a conversation:\n%s", got)
	}
}

// TestAnOfferOfFriendshipSaysHowToAnswerIt, by the first name alone,
// since that is what accept and decline take.
func TestAnOfferOfFriendshipSaysHowToAnswerIt(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.Relay(t, imFrom(testSomebody, "Some Body", sl.DialogFriendshipOffered, ""))
	got := waits(t, x, "offers friendship")
	if !strings.Contains(got, "accept Some, or decline Some") {
		t.Errorf("the notice should say what to type:\n%s", got)
	}

	// Typing is a line per keystroke and is not worth showing.
	x.out.Reset()
	x.grid.Relay(t, imFrom(testSomebody, "Some Body", sl.DialogTypingStart, ""))
	x.grid.Relay(t, imFrom(testSomebody, "Some Body", sl.DialogTypingStop, ""))
	x.grid.Relay(t, imFrom(testSomebody, "Some Body", sl.DialogTeleportLure, "come and see"))
	got = waits(t, x, "come and see")
	if strings.Contains(got, "typing") {
		t.Errorf("a keystroke should not reach the screen:\n%s", got)
	}
	if !strings.Contains(got, "teleport lure from Some Body") {
		t.Errorf("anything else should be named and printed:\n%s", got)
	}
}

// TestAnArrivalNobodyAskedForIsPrinted.
//
// A region change reaches this shell whoever provoked it: a lure
// accepted from another client attached to the same daemon, a session
// re-established after the circuit was lost, and an avatar that walked
// over a border and was told about it.  None of those is a command's
// answer, so without this the shell would go on describing a region it
// had left and nothing on the screen would say why the objects had all
// changed.
func TestAnArrivalNobodyAskedForIsPrinted(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.RelayRegion(t, "Sandbox Goguen", goguenHandle)
	got := waits(t, x, "Sandbox Goguen")
	if !strings.Contains(got, "the avatar is now in Sandbox Goguen") {
		t.Errorf("the notice should name the region arrived in:\n%s", got)
	}
	// And say what it costs, since a script or a person holding an
	// object from a moment ago is holding something the new region has
	// never heard of.
	if !strings.Contains(got, "gone") {
		t.Errorf("the notice should say what the move threw away:\n%s", got)
	}
}

// TestAnArrivalSaysWhatSentTheAvatarThere: a move the shell did not ask
// for is told with its cause when the simulator gave one, and with
// nothing where it gave none.
func TestAnArrivalSaysWhatSentTheAvatarThere(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.RelayRegionBy(t, "Sandbox Goguen", goguenHandle, sl.TeleportViaHome|sl.TeleportForceRedirect)
	got := waits(t, x, "Sandbox Goguen")
	if !strings.Contains(got, "the avatar is now in Sandbox Goguen (sent home) -- ") {
		t.Errorf("the notice should say the avatar was sent home:\n%s", got)
	}

	x.out.Reset()
	x.grid.RelayRegionBy(t, "Sandbox Goguen", goguenHandle, sl.TeleportViaLure)
	got = waits(t, x, "Sandbox Goguen")
	if !strings.Contains(got, "(by a teleport offer)") {
		t.Errorf("the notice should say a lure moved the avatar:\n%s", got)
	}

	x.out.Reset()
	x.grid.RelayRegion(t, "Sandbox Goguen", goguenHandle)
	got = waits(t, x, "Sandbox Goguen")
	if strings.Contains(got, "(") {
		t.Errorf("a move with no cause should not name one:\n%s", got)
	}
}

// TestAScriptAskingForSomethingSaysHowToAnswerIt.
//
// A dialog and a permission request both wait for an answer that has
// to be typed, and neither says how on its own -- so the notice names
// the commands that answer it, and typing what it says answers it.
func TestAScriptAskingForSomethingSaysHowToAnswerIt(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	d := &msg.ScriptDialog{}
	d.Data.ObjectID = testLamp
	d.Data.ObjectName = append([]byte("a lamp"), 0)
	d.Data.Message = append([]byte("on or off?"), 0)
	d.Data.ChatChannel = -1379
	d.Buttons = []msg.ScriptDialog_Buttons{
		{ButtonLabel: append([]byte("on"), 0)},
		{ButtonLabel: append([]byte("off"), 0)},
	}
	x.grid.Relay(t, d)

	got := waits(t, x, "a lamp asks")
	if !strings.Contains(got, "* [Object] a lamp asks") {
		t.Errorf("the notice should say the lamp is an object:\n%s", got)
	}
	if !strings.Contains(got, "[on off]") {
		t.Errorf("the notice should list the buttons:\n%s", got)
	}
	// This asserted "accept-perms" for as long as the notice said it,
	// and no such command has ever existed.  A test that checks the
	// advice is spelled the same way it was written down does not check
	// that the advice works, so it is followed.
	followAdvice(t, x, got, "a lamp asks", "dialog", " on")
	var reply *msg.ScriptDialogReply
	for _, m := range x.grid.Sent() {
		if r, ok := m.(*msg.ScriptDialogReply); ok {
			reply = r
		}
	}
	if reply == nil {
		t.Fatal("answering the dialog as the notice said sent no reply")
	}
	if label := strings.TrimRight(string(reply.Data.ButtonLabel), "\x00"); reply.Data.ObjectID != testLamp ||
		reply.Data.ChatChannel != -1379 || label != "on" {
		t.Errorf("the reply pressed %q on channel %d of %s", label, reply.Data.ChatChannel, reply.Data.ObjectID)
	}

	x.grid.Relay(t, asking("a lamp", sl.PermissionTakeControls))

	got = waits(t, x, "a lamp wants")
	if !strings.Contains(got, "* [Object] a lamp wants") {
		t.Errorf("the request should say the lamp is an object:\n%s", got)
	}
	followAdvice(t, x, got, "a lamp wants", "permission", "")
	sent := scriptAnswers(x)
	if len(sent) != 1 {
		t.Fatalf("granting as the notice said sent %d answers, want one", len(sent))
	}
	if a := sent[0].Data; a.TaskID != testLamp || a.ItemID != testProbe ||
		sl.Perms(a.Questions) != sl.PermissionTakeControls {
		t.Errorf("the grant was %+v, want take controls for the lamp's script", a)
	}
}

// followAdvice does what the notice saying marker tells somebody to do:
// every command it names has to be one, the one that lists things has
// to list this kind, and "answer", given the number it was listed
// under and then what, has to take it.
func followAdvice(t *testing.T, x *testShell, screen, marker, kind, what string) {
	t.Helper()
	var notice string
	for _, line := range strings.Split(screen, "\n") {
		if strings.Contains(line, marker) {
			notice = line
		}
	}
	at := strings.LastIndex(notice, " -- ")
	if at < 0 {
		t.Fatalf("the notice does not say how to answer it: %q", notice)
	}
	named := map[string]bool{}
	for _, clause := range strings.Split(notice[at+len(" -- "):], ", ") {
		name := strings.Fields(clause)[0]
		named[name] = true
		if _, ok := commands[name]; !ok {
			t.Errorf("the notice names %q, which is not a command: %q", name, notice)
		}
	}
	if !named["waiting"] || !named["answer"] {
		t.Fatalf("the notice should name waiting and answer: %q", notice)
	}

	listing := x.do(t, "waiting")
	n := ""
	for _, line := range strings.Split(listing, "\n") {
		if f := strings.Fields(line); len(f) > 1 && f[1] == kind {
			n = f[0]
		}
	}
	if n == "" {
		t.Fatalf("waiting does not list the %s:\n%s", kind, listing)
	}
	if got := x.do(t, "answer "+n+what); strings.Contains(got, "slsh:") {
		t.Fatalf("answer %s%s, as the notice said, failed: %s", n, what, got)
	}
}

// TestTheChatPrinterStopsWhenTheSessionDoes.
//
// A shell whose connection has gone must not be left with a goroutine
// waiting on channels nobody will ever put anything on.  It waits on
// four of them, and any one closing is enough; the session closes them
// all.
func TestTheChatPrinterStopsWhenTheSessionDoes(t *testing.T) {
	x := newTestShell(t)
	done := make(chan struct{})
	go func() { defer close(done); x.watch(context.Background()) }()

	// Long enough for it to have subscribed: a printer that started
	// after the end finds the chat channel closed already, which is the
	// same ending by a different door.
	time.Sleep(time.Millisecond)
	x.grid.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the chat printer outlived the session")
	}
}

// TestTheIMSubscriptionEndsWithTheSession: whatever the shell is
// waiting on has to end when the session does.
//
// This one did not.  sl.Session.closeChat closed the chat and
// permission maps and left imSubs alone, so a subscriber waited for
// ever on a session that had gone.  The shell survived it only because
// it waited on all three at once and the other two closed -- anything
// waiting on IMs alone hung, and the arm of the printer's select that
// handles the closure was unreachable.
func TestTheIMSubscriptionEndsWithTheSession(t *testing.T) {
	x := newTestShell(t)
	ims := x.s.IMs(0)
	x.grid.Close()

	select {
	case _, ok := <-ims:
		if ok {
			t.Error("something arrived on a session that has ended")
		}
	case <-time.After(2 * time.Second):
		t.Error("the IM subscription was never closed")
	}
}

// ------------------------------------------------------------ speaking

// TestSendGoesToWhicheverConversationIsCurrent, which is the whole of
// what chat mode is: the prompt says where the line will go and the
// line goes there.
func TestSendGoesToWhicheverConversationIsCurrent(t *testing.T) {
	x := newTestShell(t)
	ctx := context.Background()

	x.send(ctx, "out loud")
	if got := x.out.String(); !strings.Contains(got, "> [Local] out loud") {
		t.Errorf("saying something printed %q", got)
	}
	if _, ok := x.grid.Sent()[0].(*msg.ChatFromViewer); !ok {
		t.Errorf("open chat went out as %T", x.grid.Sent()[0])
	}

	c, _ := x.talk.Open(testSomebody, "Some Body")
	x.talk.Switch(c)
	x.out.Reset()
	x.send(ctx, "privately")
	if got := x.out.String(); !strings.Contains(got, "> [IM Some Body] privately") {
		t.Errorf("an IM printed %q", got)
	}
	if _, ok := x.grid.Sent()[1].(*msg.ImprovedInstantMessage); !ok {
		t.Errorf("an IM went out as %T", x.grid.Sent()[1])
	}

	// A line the circuit would not take is not a line that was said.
	x.grid.sendErr = errors.New("the circuit is down")
	x.out.Reset()
	x.send(ctx, "into the void")
	if got := x.out.String(); !strings.Contains(got, "the circuit is down") {
		t.Errorf("a refused IM printed %q", got)
	}
	if strings.Contains(x.out.String(), "> [IM") {
		t.Error("a send that failed must not be printed as though it happened")
	}

	x.talk.Switch(x.talk.Next()) // back round to Local
	x.out.Reset()
	x.send(ctx, "out loud again")
	if got := x.out.String(); !strings.Contains(got, "the circuit is down") {
		t.Errorf("a refused remark printed %q", got)
	}
}

// TestTabMovesBetweenConversationsUnlessSomethingIsTyped.
//
// Tab in chat mode is the only way to change where a line goes, and it
// must not do it under a half-typed line: the line would arrive
// somewhere nobody meant to send it.
func TestTabMovesBetweenConversationsUnlessSomethingIsTyped(t *testing.T) {
	x := newTestShell(t)
	c, _ := x.talk.Open(testSomebody, "Some Body")

	x.term.SetLine("half a sentence")
	x.cycle()
	if got := x.talk.Current(); got == c {
		t.Error("tab moved the conversation under a half-typed line")
	}

	x.term.SetLine("")
	x.cycle()
	if got := x.talk.Current(); got != c {
		t.Errorf("tab should move to the next conversation, got %q", got.Label())
	}
}

// ------------------------------------------------------------ commands

// TestSayPrintsWhatWasSaidAndWhereItWent.
//
// The channel is worth printing because a negative one reaches scripts
// and nobody else: without it there is nothing at all to show for the
// command.
func TestSayPrintsWhatWasSaidAndWhereItWent(t *testing.T) {
	x := newTestShell(t)

	if got := x.do(t, "say hello there"); !strings.Contains(got, "> [Local] hello there") {
		t.Errorf("say printed %q", got)
	}
	if got := x.do(t, "say -c -1379 on"); !strings.Contains(got, "> [channel -1379] on") {
		t.Errorf("say on a channel printed %q", got)
	}
	if got := x.do(t, "say"); !strings.Contains(got, "usage: say") {
		t.Errorf("say with nothing to say printed %q", got)
	}
	if got := x.do(t, "say --help"); !strings.Contains(got, "-c") {
		t.Errorf("say --help printed %q", got)
	}

	x.grid.sendErr = errors.New("the circuit is down")
	got := x.do(t, "say into the void")
	if !strings.Contains(got, "the circuit is down") {
		t.Errorf("say should report the failure, got %q", got)
	}
	if strings.Contains(got, "> [Local]") {
		t.Errorf("a send that failed is not something that was said: %q", got)
	}
}

// TestChatModeCanBeEnteredOnSomebodyInParticular.
func TestChatModeCanBeEnteredOnSomebodyInParticular(t *testing.T) {
	x := newTestShell(t)
	knows(t, x, map[msg.UUID]string{testSomebody: "Some Body"})

	if got := x.do(t, "chat"); got != "" {
		t.Errorf("chat printed %q, and should print nothing", got)
	}
	if !x.chatting() {
		t.Error("chat should leave the shell in chat mode")
	}

	x.setMode(modeCommand)
	if got := x.do(t, "chat Some Body"); got != "" {
		t.Errorf("chat on somebody printed %q", got)
	}
	if got := x.talk.Current().Label(); got != "Some Body" {
		t.Errorf("chat on somebody should switch to them, got %q", got)
	}

	if got := x.do(t, "chat nobody-of-that-name"); !strings.Contains(got, "is known") {
		t.Errorf("chat on a stranger printed %q", got)
	}
	if got := x.do(t, "chat --help"); !strings.Contains(got, "[WHO]") {
		t.Errorf("chat --help printed %q", got)
	}
}

// TestIMOpensOrSendsDependingOnWhetherThereIsAnythingToSay.
func TestIMOpensOrSendsDependingOnWhetherThereIsAnythingToSay(t *testing.T) {
	x := newTestShell(t)
	x.setListed([]person{{ID: testSomebody, Name: "Some Body"}})

	// With text it sends and stays in command mode.
	got := x.do(t, "im 1 how are you")
	if !strings.Contains(got, "new conversation with Some Body") {
		t.Errorf("the first IM should say a conversation was opened: %q", got)
	}
	if !strings.Contains(got, "> [IM Some Body] how are you") {
		t.Errorf("im printed %q", got)
	}
	if x.chatting() {
		t.Error("im with text should not enter chat mode")
	}

	// The second one is not a new conversation.
	if got := x.do(t, "im 1 and again"); strings.Contains(got, "new conversation") {
		t.Errorf("the same person twice is one conversation: %q", got)
	}

	// Without text it opens the conversation and goes to chat mode.
	if got := x.do(t, "im 1"); got != "" {
		t.Errorf("im with nobody to say anything to printed %q", got)
	}
	if !x.chatting() {
		t.Error("im with no text should enter chat mode")
	}
	if got := x.talk.Current().Label(); got != "Some Body" {
		t.Errorf("im should switch to them, got %q", got)
	}

	if got := x.do(t, "im"); !strings.Contains(got, "usage: im WHO") {
		t.Errorf("im with nobody printed %q", got)
	}
	if got := x.do(t, "im 9 hello"); !strings.Contains(got, "no 9 in the last listing") {
		t.Errorf("im to a number nobody showed printed %q", got)
	}
	if got := x.do(t, "im --help"); !strings.Contains(got, "WHO") {
		t.Errorf("im --help printed %q", got)
	}

	x.grid.sendErr = errors.New("the circuit is down")
	if got := x.do(t, "im 1 hello"); !strings.Contains(got, "the circuit is down") {
		t.Errorf("im should report the failure, got %q", got)
	}
}

// TestFriendsAreTheOnesOnlineUnlessAllAreAsked, because a list of four
// hundred people, of whom two are logged in, answers a question nobody
// asked.
func TestFriendsAreTheOnesOnlineUnlessAllAreAsked(t *testing.T) {
	x := newTestShell(t)
	x.grid.AnswerNames(t, map[msg.UUID]string{
		testSomebody: "Some Body", testFriend: "A Friend",
	})

	if got, want := x.do(t, "friends"), "nobody\n"; got != want {
		t.Errorf("friends with nobody online printed %q, want %q", got, want)
	}

	x.grid.mu.Lock()
	x.grid.friends = []sl.Friend{
		{ID: testSomebody, Online: true},
		{ID: testFriend},
	}
	x.grid.mu.Unlock()

	got := x.do(t, "friends")
	if !strings.Contains(got, " 1  Some Body") || strings.Contains(got, "A Friend") {
		t.Errorf("friends should list whoever is online:\n%s", got)
	}
	// The number is what the other commands take.
	id, name, err := x.who(context.Background(), io.Discard, "1")
	if err != nil || id != testSomebody || name != "Some Body" {
		t.Errorf("who(1) = %v %q %v, want the first line", id, name, err)
	}

	if got := x.do(t, "friends -a"); !strings.Contains(got, "A Friend") {
		t.Errorf("friends -a should list everybody:\n%s", got)
	}
	if got := x.do(t, "friends --help"); !strings.Contains(got, "-a") {
		t.Errorf("friends --help printed %q", got)
	}

	x.grid.friendsErr = errors.New("the friend list never arrived")
	if got := x.do(t, "friends"); !strings.Contains(got, "the friend list never arrived") {
		t.Errorf("friends should report the failure, got %q", got)
	}
}

// TestLookupAsksTheGridAndShowsTheDisplayNameWhenItDiffers.
//
// Both names are needed and they are not the same: the legacy name is
// what every other command takes, and the display name is what the
// person calls themselves and what somebody searching will have typed.
func TestLookupAsksTheGridAndShowsTheDisplayNameWhenItDiffers(t *testing.T) {
	x := newTestShell(t)
	found := `<map><key>id</key><uuid>` + testSomebody.String() + `</uuid>` +
		`<key>legacy_first_name</key><string>Some</string>` +
		`<key>legacy_last_name</key><string>Body</string>` +
		`<key>display_name</key><string>Somebody Entirely</string>` +
		`<key>username</key><string>somebody</string></map>` +
		`<map><key>id</key><uuid>` + testFriend.String() + `</uuid>` +
		`<key>legacy_first_name</key><string>A</string>` +
		`<key>legacy_last_name</key><string>Friend</string>` +
		`<key>display_name</key><string>A Friend</string></map>` +
		// A row with no id at all, which the grid sends for a name it
		// will not say more about.
		`<map><key>display_name</key><string>Nobody</string></map>`
	x.grid.ServeCap(t, sl.PickerCap, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "names=") {
			http.Error(w, "no name to search for", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, `<?xml version="1.0" ?><llsd><map><key>agents</key><array>`+
			found+`</array></map></llsd>`)
	})

	got := x.do(t, "lookup body")
	if !strings.Contains(got, " 1  A Friend") {
		t.Errorf("lookup should number what it found, in name order:\n%s", got)
	}
	if !strings.Contains(got, "Some Body                         Somebody Entirely") {
		t.Errorf("a display name that differs should be shown:\n%s", got)
	}
	// A display name that is only the same name again is noise.
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if strings.Contains(line, "A Friend") && strings.Count(line, "A Friend") != 1 {
			t.Errorf("a display name the same as the name should not be repeated: %q", line)
		}
	}
	// And the listing is what a number afterwards means.
	if id, _, err := x.who(context.Background(), io.Discard, "2"); err != nil || id != testSomebody {
		t.Errorf("who(2) = %v %v, want the second line", id, err)
	}

	if got := x.do(t, "lookup"); !strings.Contains(got, "usage: lookup") {
		t.Errorf("lookup with nothing to look up printed %q", got)
	}
	if got := x.do(t, "lookup --help"); !strings.Contains(got, "TEXT") {
		t.Errorf("lookup --help printed %q", got)
	}

	// -l adds the key, which is the one handle that does not change,
	// and leaves the display name where it was.  Without it the key is
	// machinery: the listing hands it to whatever the number is typed
	// at, and a page of them would bury the names.
	plain := x.do(t, "lookup body")
	long := x.do(t, "lookup -l body")
	if strings.Contains(plain, testSomebody.String()) {
		t.Errorf("a bare lookup printed a key:\n%s", plain)
	}
	if !strings.Contains(long, testSomebody.String()) {
		t.Errorf("lookup -l did not print the key:\n%s", long)
	}
	if !strings.Contains(long, testSomebody.String()+"  Somebody Entirely") {
		t.Errorf("the key should come between the name and the display name:\n%s", long)
	}
	// The listing still means what a number typed afterwards means.
	if id, _, err := x.who(context.Background(), io.Discard, "2"); err != nil || id != testSomebody {
		t.Errorf("after lookup -l, who(2) = %v %v, want the second line", id, err)
	}
}

// TestLookupSaysSoWhenNobodyMatched rather than printing nothing, which
// is what a search that failed looks like.
func TestLookupSaysSoWhenNobodyMatched(t *testing.T) {
	x := newTestShell(t)
	x.grid.ServeCap(t, sl.PickerCap, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "gone") {
			http.Error(w, "the search is not answering", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, `<?xml version="1.0" ?><llsd><map><key>agents</key><array/></map></llsd>`)
	})

	if got, want := x.do(t, "lookup nobody"), "nobody found\n"; got != want {
		t.Errorf("lookup printed %q, want %q", got, want)
	}
	if got := x.do(t, "lookup gone"); !strings.Contains(got, "status 500") {
		t.Errorf("lookup should report a search that failed, got %q", got)
	}
}

// testGroupID is a group somebody lists in their profile, which is not
// one this avatar has joined: a profile's groups are somebody else's.
var testGroupID = msg.MustParseUUID("e2a17e57-7e57-c0de-b864-09116b98f7c6")

// TestProfileShowsWhatAProfileSays.
//
// Everything printed comes from a different one of the three replies, so
// a listing missing a line is a reply that went astray rather than a
// formatting slip: born and payment and the partner from the properties,
// the languages from the interests, and the group from the reply that
// arrives before either of them.
func TestProfileShowsWhatAProfileSays(t *testing.T) {
	x := newTestShell(t)
	x.grid.AnswerNames(t, map[msg.UUID]string{
		testSomebody: "Some Body", testFriend: "A Friend",
	})
	x.grid.AnswerProfile(t,
		avatarGroups(testSomebody, aGroup(testGroupID, "Lorn Rangers", "Officer")),
		avatarProperties(testSomebody, "5/21/2010", "I build things.\nMostly boats.",
			testFriend, 0x1d),
		avatarInterests(testSomebody, "", "", "English"))

	got := x.do(t, "profile "+testSomebody.String())
	for _, want := range []string{
		"Some Body\n",
		"  born      5/21/2010\n",
		// Both flags set, which is the 0x1d measured on Agni: on file
		// says one thing and used says another.
		"  payment   on file, and used\n",
		"  about     I build things.\n            Mostly boats.\n",
		"  speaks    English\n",
		"1 listed, which is not every group they are in",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("profile printed no %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, testFriend.String()) || !strings.Contains(got, "A Friend") {
		t.Errorf("the partner is named by neither key nor name:\n%s", got)
	}

	// The group row is a key, a title and the name last, since the name
	// is the only one of the three with no length worth relying on.
	var row string
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, testGroupID.String()) {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("the groups reply arrives first and nothing printed it:\n%s", got)
	}
	if i, j := strings.Index(row, "Officer"), strings.Index(row, "Lorn Rangers"); i < 0 || j < i {
		t.Errorf("the group row is %q, want the key, the title, and the name last", row)
	}
}

// TestProfileSaysWhatItDoesNotKnow.
//
// A blank beside "born" reads as a shell that lost the answer where "not
// said" reads as a profile that has none, and the two are worth telling
// apart on the screen because they are worth telling apart at all.  The
// interests are the exception and say nothing when there is nothing:
// hardly anybody has been able to set one for years, so a line about
// them on every profile would be noise.
func TestProfileSaysWhatItDoesNotKnow(t *testing.T) {
	x := newTestShell(t)
	x.grid.AnswerNames(t, map[msg.UUID]string{testSomebody: "Some Body"})
	x.grid.AnswerProfile(t,
		avatarGroups(testSomebody),
		avatarProperties(testSomebody, "", "", msg.UUID{}, 0),
		avatarInterests(testSomebody, "", "", ""))

	got := x.do(t, "profile "+testSomebody.String())
	for _, want := range []string{
		"  born      not said\n",
		"  payment   none on file\n",
		"  partner   nobody\n",
		"  about     nothing said\n",
		"  groups    none listed\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("profile printed no %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"wants", "skills", "speaks", "web", "account"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("profile printed an empty %q:\n%s", unwanted, got)
		}
	}
	// The empty row an avatar with no groups sends is not a group, and
	// printing it would put a line of zeros under the count that says
	// none.
	if strings.Contains(got, "00000000-0000-0000-0000-000000000000") {
		t.Errorf("the empty group row was printed as a group:\n%s", got)
	}
}

// TestAKeyTheGridHasNeverHeardOfIsASentenceAndNotAFailure.
//
// The grid answers a made-up key with the empty group row and never
// sends the profile, so the wait running out IS the answer.  Reported as
// an error it would read as slsh having broken, and the thing that
// actually happened -- there is nobody with that key -- would be the one
// fact missing from the screen.
func TestAKeyTheGridHasNeverHeardOfIsASentenceAndNotAFailure(t *testing.T) {
	x := newTestShell(t)
	nobody := msg.MustParseUUID("f2537e57-7e57-c0de-f6ea-ead72a026c9b")
	x.grid.AnswerNames(t, map[msg.UUID]string{})
	x.grid.AnswerProfile(t, avatarGroups(nobody))

	got := x.do(t, "profile -w 1 "+nobody.String())
	if !strings.Contains(got, "never heard of") || !strings.Contains(got, nobody.String()) {
		t.Errorf("a key nobody knows printed %q", got)
	}
	if strings.Contains(got, "slsh:") {
		t.Errorf("a key nobody knows was reported as a failure: %q", got)
	}
}

// searching makes the grid's name search answer with these people.
//
// It is how a shell that has just started turns the name of somebody
// who is not here into a key: the session's own cache holds whoever has
// been mentioned, and in "slsh -c" nothing has been mentioned yet.
func searching(t *testing.T, x *testShell, people ...sl.Found) {
	t.Helper()
	x.grid.ServeCap(t, sl.PickerCap, func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" ?><llsd><map><key>agents</key><array>`)
		for _, p := range people {
			first, last, _ := strings.Cut(p.Name, " ")
			fmt.Fprintf(&b, `<map><key>id</key><uuid>%s</uuid>`+
				`<key>legacy_first_name</key><string>%s</string>`+
				`<key>legacy_last_name</key><string>%s</string></map>`, p.ID, first, last)
		}
		b.WriteString(`</array></map></llsd>`)
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, b.String())
	})
}

// TestProfileFindsANameOnlyTheGridKnows.
//
// The session's name cache holds whoever has been mentioned and the
// region whoever is standing in it, so "slsh -c profile SOMEBODY",
// which is how most of this shell's examples are written, would refuse
// anybody who is not here.  The search is one call and answers it, so
// the command makes it rather than telling somebody to type lookup and
// try again.
func TestProfileFindsANameOnlyTheGridKnows(t *testing.T) {
	x := newTestShell(t)
	searching(t, x, sl.Found{ID: testSomebody, Name: "Perrick Hobb"})
	x.grid.AnswerProfile(t,
		avatarGroups(testSomebody),
		avatarProperties(testSomebody, "4/17/2011", "", msg.UUID{}, 0),
		avatarInterests(testSomebody, "", "", ""))

	got := x.do(t, "profile -w 2 Perrick Hobb")
	if !strings.Contains(got, "Perrick Hobb\n") || !strings.Contains(got, "4/17/2011") {
		t.Errorf("a name only the grid knows printed %q", got)
	}
	if strings.Contains(got, "is known") {
		t.Errorf("the name was refused rather than looked up: %q", got)
	}
}

// crowd is a search result of n people, all answering to "Some".
//
// Enough of them that the shape of the answer matters: two people fit in
// a sentence and ninety-five do not, and it was ninety-five that a
// single letter typed on Agni turned up.  The first is the one the rest
// of the test profiles.
func crowd(n int) []sl.Found {
	out := make([]sl.Found, 0, n)
	for i := range n {
		id := testSomebody
		if i > 0 {
			id = msg.MustParseUUID(fmt.Sprintf("d22b7e57-7e57-c0de-0e4e-%012d", 100+i))
		}
		out = append(out, sl.Found{ID: id, Name: fmt.Sprintf("Some Body%02d", i)})
	}
	return out
}

// TestProfileListsThePeopleANameCouldBe.
//
// Picking one of several would read a stranger's profile and say it was
// the person asked for, which is the one mistake a search can make that
// looks like success.  So they are listed instead -- as lookup's own
// numbered listing, one per line, because the sentence that follows
// promises that a number picks one and a promise about numbers is worth
// nothing beside a line of names joined by commas.
func TestProfileListsThePeopleANameCouldBe(t *testing.T) {
	x := newTestShell(t)
	searching(t, x, crowd(12)...)
	x.grid.AnswerProfile(t,
		avatarGroups(testSomebody),
		avatarProperties(testSomebody, "4/17/2011", "", msg.UUID{}, 0),
		avatarInterests(testSomebody, "", "", ""))

	got := x.do(t, "profile Some")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 13 {
		t.Fatalf("twelve people and one sentence printed %d lines:\n%s", len(lines), got)
	}
	// The same shape lookup prints, numbered from one and lined up.
	if !strings.HasPrefix(lines[0], " 1  Some Body00") {
		t.Errorf("the first line is %q, want lookup's numbered listing", lines[0])
	}
	if !strings.HasPrefix(lines[9], "10  Some Body09") {
		t.Errorf("the tenth line is %q, want the number in the same column", lines[9])
	}
	// And the sentence is short, now that the names are above it.
	if last := lines[12]; !strings.Contains(last, "12 people answer to") ||
		!strings.Contains(last, "a number picks one") || len(last) > 80 {
		t.Errorf("the sentence under the listing is %q", last)
	}

	// The number it invites is a number that works.
	if got := x.do(t, "profile -w 2 1"); !strings.Contains(got, "4/17/2011") {
		t.Errorf("the first of the people listed did not profile: %q", got)
	}

	// A number is never searched for: one that is not in the listing
	// means the listing and not somebody called "99".
	if got := x.do(t, "profile 99"); !strings.Contains(got, "no 99 in the last listing") {
		t.Errorf("a number nobody listed printed %q", got)
	}
}

// TestAFullPageOfSearchResultsSaysItIsOne.
//
// The search answers with at most sl.LookupLimit rows and says nothing
// about what it left out, so one letter comes back looking exactly like
// the whole of the grid.  Somebody scrolling a hundred names for one
// that is not among them would conclude their friend had gone, where
// what actually happened is that the search stopped counting.
func TestAFullPageOfSearchResultsSaysItIsOne(t *testing.T) {
	x := newTestShell(t)
	searching(t, x, crowd(sl.LookupLimit)...)

	const note = "as many as one search answers with"
	got := x.do(t, "profile Some")
	if !strings.Contains(got, note) {
		t.Errorf("a full page of results did not say it was a page:\n%s",
			strings.Join(strings.Split(got, "\n")[:3], "\n"))
	}
	// lookup shows the same rows and needs the same warning: it is the
	// same list, printed by the same code, read by the same person.
	if got := x.do(t, "lookup Some"); !strings.Contains(got, note) {
		t.Error("lookup printed a full page without saying so")
	}

	// One short of the page is the whole answer, and saying otherwise
	// would put a warning under every listing.
	x2 := newTestShell(t)
	searching(t, x2, crowd(sl.LookupLimit-1)...)
	if got := x2.do(t, "lookup Some"); strings.Contains(got, note) {
		t.Error("a listing that was not a full page was called one")
	}
}

// TestAFullyTypedNameIsNotAnAmbiguousOne.
//
// A search matches part of a name, so the whole of somebody's name can
// come back with the people whose names contain it.  Listing them would
// refuse the one argument that could not have been meant any other way,
// which is what chooseGroup decided about a group name for the same
// reason.
func TestAFullyTypedNameIsNotAnAmbiguousOne(t *testing.T) {
	x := newTestShell(t)
	searching(t, x,
		sl.Found{ID: testSomebody, Name: "Some Body"},
		sl.Found{ID: testFriend, Name: "Some Body Junior"})
	x.grid.AnswerProfile(t,
		avatarGroups(testSomebody),
		avatarProperties(testSomebody, "4/17/2011", "", msg.UUID{}, 0),
		avatarInterests(testSomebody, "", "", ""))

	got := x.do(t, "profile -w 2 Some Body")
	if !strings.Contains(got, "4/17/2011") {
		t.Errorf("a name typed in full printed %q", got)
	}
	if strings.Contains(got, "could be any of") {
		t.Errorf("a name typed in full was called ambiguous: %q", got)
	}
}

// TestProfileNeedsSomebodyToAskAbout, and answers for itself like every
// other command.
//
// A name the grid's own search cannot find either is still a refusal,
// and one that no longer says "try lookup": that is what has just been
// done.  What is left to try is less of the name, since the search
// matches part of one, or the key.
func TestProfileNeedsSomebodyToAskAbout(t *testing.T) {
	x := newTestShell(t)
	searching(t, x)

	if got := x.do(t, "profile"); !strings.Contains(got, "usage: profile [-w SECONDS] WHO") {
		t.Errorf("profile with nobody printed %q", got)
	}
	if got := x.do(t, "profile --help"); !strings.Contains(got, "WHO") ||
		!strings.Contains(got, "--wait") {
		t.Errorf("profile --help printed %q", got)
	}
	got := x.do(t, "profile nobody at all")
	if !strings.Contains(got, `nobody here or on the grid is called "nobody at all"`) {
		t.Errorf("profile of a name nobody knows printed %q", got)
	}
	if strings.Contains(got, "try who, friends or lookup") {
		t.Errorf("the refusal still points at the search it just made: %q", got)
	}
}

// TestOfferSaysWhoItOffered, since the offer itself produces nothing
// visible until the other side answers it.
func TestOfferSaysWhoItOffered(t *testing.T) {
	x := newTestShell(t)
	x.setListed([]person{{ID: testSomebody, Name: "Some Body"}})

	if got, want := x.do(t, "offer 1"), "offered friendship to Some Body\n"; got != want {
		t.Errorf("offer printed %q, want %q", got, want)
	}
	if got := x.do(t, "offer 1 be my friend"); !strings.Contains(got, "offered friendship") {
		t.Errorf("offer with a message printed %q", got)
	}
	if got := x.do(t, "offer"); !strings.Contains(got, "usage: offer WHO") {
		t.Errorf("offer with nobody printed %q", got)
	}
	if got := x.do(t, "offer 9"); !strings.Contains(got, "no 9 in the last listing") {
		t.Errorf("offer to a number nobody showed printed %q", got)
	}
	if got := x.do(t, "offer --help"); !strings.Contains(got, "WHO") {
		t.Errorf("offer --help printed %q", got)
	}

	x.grid.sendErr = errors.New("the circuit is down")
	if got := x.do(t, "offer 1"); !strings.Contains(got, "the circuit is down") {
		t.Errorf("offer should report the failure, got %q", got)
	}
}

// TestOffersListsBothKindsTogether.
//
// From where a person sits they are the same thing: something somebody
// offered that has not been answered.  An offer nobody answers stays
// pending for ever and the other side is told nothing, which is why
// they are worth listing at all.
func TestOffersListsBothKindsTogether(t *testing.T) {
	x := newTestShell(t)

	// Said with what it is an account of, which here -- a backend with
	// no daemon keeping a record -- is only what this shell saw.
	if got := x.do(t, "offers"); !strings.HasPrefix(got, "no offers waiting -- ") {
		t.Errorf("offers printed %q, want no offers waiting and what that means", got)
	}
	if got := x.do(t, "offers --help"); !strings.Contains(got, "offers") {
		t.Errorf("offers --help printed %q", got)
	}

	x.grid.Relay(t, imFrom(testSomebody, "Some Body", sl.DialogFriendshipOffered, ""))
	x.grid.Relay(t, offering(testFriend, "A Friend", "a lamp", sl.AssetObject, testLamp))
	waitForOffers(t, x, 1, 1)

	got := x.do(t, "offers")
	if !strings.Contains(got, "friendship  Some Body") {
		t.Errorf("a friendship offer should be listed:\n%s", got)
	}
	if !strings.Contains(got, "object      a lamp") || !strings.Contains(got, "from A Friend") {
		t.Errorf("an inventory offer should say what it is and who from:\n%s", got)
	}
}

// waitForOffers waits for relayed offers to have been recorded, since
// they are read off the wire on the session's own goroutine.
func waitForOffers(t *testing.T, x *testShell, friends, items int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, i := len(x.s.Offers()), len(x.s.InventoryOffers())
		if f >= friends && i >= items {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d friendship and %d inventory offers, have %d and %d",
				friends, items, f, i)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestAcceptTakesTheOnlyOfferOrTheOneNamed.
//
// Naming is how an ambiguity is settled, and there is no default:
// accepting the wrong one of two friendship offers cannot be undone
// from here.
func TestAcceptTakesTheOnlyOfferOrTheOneNamed(t *testing.T) {
	x := newTestShell(t)

	if got := x.do(t, "accept"); !strings.Contains(got, "no offers waiting") {
		t.Errorf("accept with nothing to accept printed %q", got)
	}
	if got := x.do(t, "accept --help"); !strings.Contains(got, "WHO") {
		t.Errorf("accept --help printed %q", got)
	}

	x.grid.Relay(t, imFrom(testSomebody, "Some Body", sl.DialogFriendshipOffered, ""))
	waitForOffers(t, x, 1, 0)
	if got, want := x.do(t, "accept"), "accepted Some Body\n"; got != want {
		t.Errorf("accept printed %q, want %q", got, want)
	}
	if _, ok := x.grid.Sent()[0].(*msg.AcceptFriendship); !ok {
		t.Errorf("accepting went out as %T", x.grid.Sent()[0])
	}

	// Two of them, and neither is what "accept" on its own means.
	x.grid.Relay(t, imFrom(testSomebody, "Some Body", sl.DialogFriendshipOffered, ""))
	x.grid.Relay(t, imFrom(testFriend, "A Friend", sl.DialogFriendshipOffered, ""))
	waitForOffers(t, x, 2, 0)

	if got := x.do(t, "accept"); !strings.Contains(got, "2 offers waiting; name one") {
		t.Errorf("accept with two offers printed %q", got)
	}
	if got := x.do(t, "accept nobody of that name"); !strings.Contains(got, "no offer from anybody matching") {
		t.Errorf("accept of a name nobody offered printed %q", got)
	}
	// A word that fits both is an ambiguity too, and is said as one.
	if got := x.do(t, "accept e"); !strings.Contains(got, "matches 2 offers") {
		t.Errorf("accept of an ambiguous name printed %q", got)
	}
	if got := x.do(t, "accept friend"); !strings.Contains(got, "accepted A Friend") {
		t.Errorf("accept of a named offer printed %q", got)
	}

	x.grid.sendErr = errors.New("the circuit is down")
	if got := x.do(t, "accept some"); !strings.Contains(got, "the circuit is down") {
		t.Errorf("accept should report the failure, got %q", got)
	}
}

// TestDeclineIsTheSameQuestionAnsweredTheOtherWay, and has to be sent:
// an offer left unanswered stays pending and the other side is told
// nothing either way.
func TestDeclineIsTheSameQuestionAnsweredTheOtherWay(t *testing.T) {
	x := newTestShell(t)

	if got := x.do(t, "decline"); !strings.Contains(got, "no offers waiting") {
		t.Errorf("decline with nothing to decline printed %q", got)
	}
	if got := x.do(t, "decline --help"); !strings.Contains(got, "WHO") {
		t.Errorf("decline --help printed %q", got)
	}

	x.grid.Relay(t, imFrom(testSomebody, "Some Body", sl.DialogFriendshipOffered, ""))
	waitForOffers(t, x, 1, 0)
	if got, want := x.do(t, "decline"), "declined Some Body\n"; got != want {
		t.Errorf("decline printed %q, want %q", got, want)
	}
	if _, ok := x.grid.Sent()[0].(*msg.DeclineFriendship); !ok {
		t.Errorf("declining went out as %T", x.grid.Sent()[0])
	}

	x.grid.Relay(t, imFrom(testFriend, "A Friend", sl.DialogFriendshipOffered, ""))
	waitForOffers(t, x, 1, 0)
	x.grid.sendErr = errors.New("the circuit is down")
	if got := x.do(t, "decline"); !strings.Contains(got, "the circuit is down") {
		t.Errorf("decline should report the failure, got %q", got)
	}
}

// TestAnInventoryOfferGoesIntoTheFolderTheShellIsIn.
//
// "cd Objects; accept" puts it where it was wanted, which is the whole
// reason the shell answers these at all: a viewer would put it wherever
// the grid chose.
func TestAnInventoryOfferGoesIntoTheFolderTheShellIsIn(t *testing.T) {
	x := newTestShell(t)
	x.do(t, "cd Objects")

	x.grid.Relay(t, offering(testFriend, "A Friend", "a lamp", sl.AssetObject, testLamp))
	waitForOffers(t, x, 0, 1)

	got := x.do(t, "accept")
	if want := "accepted \"a lamp\" from A Friend\n"; got != want {
		t.Errorf("accept printed %q, want %q", got, want)
	}
	answer, ok := x.grid.Sent()[0].(*msg.ImprovedInstantMessage)
	if !ok {
		t.Fatalf("the answer went out as %T", x.grid.Sent()[0])
	}
	if answer.MessageBlock.Dialog != sl.DialogInventoryAccepted {
		t.Errorf("the answer had dialog %d, want inventory accepted", answer.MessageBlock.Dialog)
	}
	if got := msg.UUID(answer.MessageBlock.BinaryBucket); got != testObjects {
		t.Errorf("it was accepted into %v, want the folder the shell is in", got)
	}

	// One declined is one that will not arrive.
	x.grid.Relay(t, offering(testFriend, "A Friend", "a lamp", sl.AssetObject, testLamp))
	waitForOffers(t, x, 0, 1)
	if got, want := x.do(t, "decline"), "declined \"a lamp\" from A Friend\n"; got != want {
		t.Errorf("decline printed %q, want %q", got, want)
	}
}

// TestAnInventoryOfferIsNotWhatAcceptMeansWhileAFriendshipOfferWaits.
//
// Both are answered by the same word, so with one of each waiting the
// bare command keeps its older meaning and the other has to be named.
func TestAnInventoryOfferIsNotWhatAcceptMeansWhileAFriendshipOfferWaits(t *testing.T) {
	x := newTestShell(t)
	x.grid.Relay(t, imFrom(testSomebody, "Some Body", sl.DialogFriendshipOffered, ""))
	x.grid.Relay(t, offering(testFriend, "A Friend", "a lamp", sl.AssetObject, testLamp))
	waitForOffers(t, x, 1, 1)

	if got := x.do(t, "accept"); !strings.Contains(got, "accepted Some Body") {
		t.Errorf("accept should still mean the friendship offer, got %q", got)
	}
	// Named, the inventory offer is reachable all along.
	if got := x.do(t, "accept a lamp"); !strings.Contains(got, `accepted "a lamp"`) {
		t.Errorf("accept of a named inventory offer printed %q", got)
	}

	// Two of them and nothing else waiting is an ambiguity as well, and
	// is refused as one -- see the test below, which is where that used
	// to come out as "no offers waiting".
	x.grid.Relay(t, offering(testFriend, "A Friend", "a lamp", sl.AssetObject, testLamp))
	x.grid.Relay(t, offering(testOther, "Some Other", "a notecard", sl.AssetNotecard, testNote))
	waitForOffers(t, x, 0, 2)
	if got := x.do(t, "accept"); !strings.Contains(got, "2 items offered") {
		t.Errorf("accept with two inventory offers printed %q", got)
	}
	if got := x.do(t, "decline a notecard"); !strings.Contains(got, `declined "a notecard"`) {
		t.Errorf("decline of a named inventory offer printed %q", got)
	}
}

// TestTwoItemsWaitingIsAnAmbiguityRatherThanAnEmptyList.
//
// Measured: with two inventory offers waiting, no friendship offer and
// nothing typed, both accept and decline said "no offers waiting"
// while "offers" listed both of them.  A bare command looked for an
// item, found two, declined to choose, and then fell through to the
// friendship offers -- of which there were none, and it was the
// friendship path that wrote the sentence.
//
// The rule the friendship offers already keep is the one to match: an
// ambiguity is refused with a count, and nothing is answered by
// accident.
func TestTwoItemsWaitingIsAnAmbiguityRatherThanAnEmptyList(t *testing.T) {
	x := newTestShell(t)
	x.grid.Relay(t, offering(testFriend, "A Friend", "a lamp", sl.AssetObject, testLamp))
	x.grid.Relay(t, offering(testOther, "Some Other", "a lamp stand", sl.AssetObject, testNote))
	waitForOffers(t, x, 0, 2)

	for _, cmd := range []string{"accept", "decline"} {
		got := x.do(t, cmd)
		if strings.Contains(got, "no offers waiting") {
			t.Errorf("%s with two items offered says there are none: %q", cmd, got)
		}
		if !strings.Contains(got, "2 items offered") {
			t.Errorf("%s should say how many are offered: %q", cmd, got)
		}
	}

	// A word that fits both is the same ambiguity reached by naming.
	// This answered the older of the two and said nothing whatever
	// about the newer.
	if got := x.do(t, "accept lamp"); !strings.Contains(got, "matches 2 offered items") {
		t.Errorf("an item name matching two offers printed %q", got)
	}
	if n := len(x.grid.Sent()); n != 0 {
		t.Fatalf("%d messages went out while nothing had been chosen", n)
	}

	// The whole of a name is not ambiguous even where it is the
	// beginning of another's, or an item could be named out of reach by
	// what somebody else sent.
	if got := x.do(t, "accept a lamp"); !strings.Contains(got, `accepted "a lamp"`) {
		t.Errorf("accept of the item named in full printed %q", got)
	}
	// And with one left, the bare command means it again.
	if got := x.do(t, "decline"); !strings.Contains(got, `declined "a lamp stand"`) {
		t.Errorf("decline of the one item left printed %q", got)
	}
}

// TestAnInventoryOfferNeedsSomewhereToPutIt: a shell standing in a
// folder that has been renamed or removed under it has nowhere to
// accept into, and must say so rather than let the grid choose.
func TestAnInventoryOfferNeedsSomewhereToPutIt(t *testing.T) {
	x := newTestShell(t)
	x.mu.Lock()
	x.cwd = []string{"a folder that went away"}
	x.mu.Unlock()

	x.grid.Relay(t, offering(testFriend, "A Friend", "a lamp", sl.AssetObject, testLamp))
	waitForOffers(t, x, 0, 1)

	if got := x.do(t, "accept"); !strings.Contains(got, "no folder") {
		t.Errorf("accept with nowhere to put it printed %q", got)
	}

	x.grid.sendErr = errors.New("the circuit is down")
	x.mu.Lock()
	x.cwd = nil
	x.mu.Unlock()
	if got := x.do(t, "accept"); !strings.Contains(got, "the circuit is down") {
		t.Errorf("accept should report the failure, got %q", got)
	}
	if got := x.do(t, "decline"); !strings.Contains(got, "the circuit is down") {
		t.Errorf("decline should report the failure, got %q", got)
	}
}

// TestTalkMarksWhereTypingWouldGo, which is the only way to find out
// short of pressing tab and watching the prompt.
func TestTalkMarksWhereTypingWouldGo(t *testing.T) {
	x := newTestShell(t)
	c, _ := x.talk.Open(testSomebody, "Some Body")

	if got, want := x.do(t, "talk"), "*  1  Local\n   2  Some Body\n"; got != want {
		t.Errorf("talk printed %q, want %q", got, want)
	}
	x.talk.Switch(c)
	if got := x.do(t, "talk"); !strings.Contains(got, "*  2  Some Body") {
		t.Errorf("talk should mark the current conversation:\n%s", got)
	}
	if got := x.do(t, "talk --help"); !strings.Contains(got, "talk") {
		t.Errorf("talk --help printed %q", got)
	}
}

// TestWhoTurnsWhatWasTypedIntoSomebody.
//
// Three forms, because all three are things a person has in front of
// them: the uuid out of a listing, the number beside a name that was
// just printed, and the name itself.
func TestWhoTurnsWhatWasTypedIntoSomebody(t *testing.T) {
	x := newTestShell(t)
	ctx := context.Background()
	x.grid.AnswerNames(t, map[msg.UUID]string{testSomebody: "Some Body"})

	id, name, err := x.who(ctx, io.Discard, testSomebody.String())
	if err != nil || id != testSomebody || name != "Some Body" {
		t.Errorf("who(uuid) = %v %q %v", id, name, err)
	}
	// A name the session has heard, in whole or in part.
	if id, _, err := x.who(ctx, io.Discard, "some body"); err != nil || id != testSomebody {
		t.Errorf("who(name) = %v %v", id, err)
	}
	if id, _, err := x.who(ctx, io.Discard, "some"); err != nil || id != testSomebody {
		t.Errorf("who(part of a name) = %v %v", id, err)
	}
	if _, _, err := x.who(ctx, io.Discard, "nobody of that name"); err == nil {
		t.Error("a name nobody has should not resolve to anybody")
	}

	// Two people whose names begin alike cannot be told apart, and are
	// not guessed between.
	knows(t, x, map[msg.UUID]string{testFriend: "Some Other"})
	_, _, err = x.who(ctx, io.Discard, "some")
	if err == nil || !strings.Contains(err.Error(), "could be any of") {
		t.Errorf("an ambiguous name should list them, got %v", err)
	}
	if !strings.Contains(err.Error(), "Some Body") || !strings.Contains(err.Error(), "Some Other") {
		t.Errorf("the refusal should name both, got %v", err)
	}
}

// TestWhoFindsSomebodyStandingInTheRegion.
//
// The second of the three steps, and the one a fresh shell needs: the
// daemon has known this avatar since before slsh started, and the
// session's cache has had nobody mentioned to it, so a name that "who"
// would list is a name every command should take.  Nothing here is a
// guess -- the person is thirty metres away and answers to that name.
func TestWhoFindsSomebodyStandingInTheRegion(t *testing.T) {
	x := newTestShell(t)
	ctx := context.Background()
	near := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000004")
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: near, Local: 3}, PCode: pcodeAvatar,
			Position: msg.Vector3{X: 130, Y: 128, Z: 25}},
	}
	x.grid.AnswerNames(t, map[msg.UUID]string{near: "Perrick Hobb"})

	// Nothing has been listed, nobody has spoken, and no command has been
	// typed: the cache is empty and the name resolves anyway.
	id, name, err := x.who(ctx, io.Discard, "Perrick Hobb")
	if err != nil || id != near || name != "Perrick Hobb" {
		t.Fatalf("who(a name only the region knows) = %v %q %v", id, name, err)
	}

	// And having been asked once, it is in the cache: a name the session
	// already knows must not cost another round trip to the region.
	asked := x.grid.AskedTheRegion()
	if asked == 0 {
		t.Fatal("the name resolved without the region being asked at all")
	}
	if id, _, err := x.who(ctx, io.Discard, "perrick"); err != nil || id != near {
		t.Errorf("who(part of a name already learnt) = %v %v", id, err)
	}
	if again := x.grid.AskedTheRegion(); again != asked {
		t.Errorf("the region was asked again for a name the session already knew: "+
			"%d asks became %d", asked, again)
	}
}

// TestTheRegionIsNotGuessedBetweenEither.
//
// Two people standing here whose names begin alike are two people, and
// the step that reaches them must refuse in the same words the cache
// does rather than picking the nearest -- which would send an instant
// message to whoever happened to be standing closer.
func TestTheRegionIsNotGuessedBetweenEither(t *testing.T) {
	x := newTestShell(t)
	ctx := context.Background()
	other := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000005")
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testSomebody, Local: 2}, PCode: pcodeAvatar,
			Position: msg.Vector3{X: 138, Y: 128, Z: 25}},
		{Object: sl.Object{ID: other, Local: 3}, PCode: pcodeAvatar,
			Position: msg.Vector3{X: 130, Y: 128, Z: 25}},
	}
	x.grid.AnswerNames(t, map[msg.UUID]string{
		testSomebody: "Some Body", other: "Some Other",
	})

	_, _, err := x.who(ctx, io.Discard, "some")
	if err == nil || !strings.Contains(err.Error(), "could be any of") {
		t.Fatalf("two people in the region answering to a name gave %v", err)
	}
	if !strings.Contains(err.Error(), "Some Body") || !strings.Contains(err.Error(), "Some Other") {
		t.Errorf("the refusal should name both, got %v", err)
	}
}

// TestARegionThatWillNotAnswerIsARegionWithNobodyInIt.
//
// The region is one more place to look and not the point of the call, so
// a daemon that cannot say who is here leaves the refusal exactly as it
// was: telling somebody who mistyped a name about an object query would
// explain the wrong thing.
func TestARegionThatWillNotAnswerIsARegionWithNobodyInIt(t *testing.T) {
	x := newTestShell(t)
	x.grid.objectsErr = errors.New("the circuit is down")

	_, _, err := x.who(context.Background(), io.Discard, "nobody of that name")
	if err == nil || !strings.Contains(err.Error(), "is known") {
		t.Errorf("who with the region unreachable = %v, want the ordinary refusal", err)
	}
	if strings.Contains(err.Error(), "circuit is down") {
		t.Errorf("a mistyped name was reported as a daemon failure: %v", err)
	}
}

// gridOf answers the name search the way the grid does: with everybody
// whose name has a word beginning with each word asked for, so that part
// of a name comes back as every person it could be part of and a word
// of a message stuck to a name comes back as nobody.  searching answers
// every question with the same rows, which is right for a command that
// asks once and wrong for one that asks run by run.
//
// What was asked is kept, in order, so that a test can say how many
// round trips a line cost.
func gridOf(t *testing.T, x *testShell, people ...sl.Found) *searches {
	t.Helper()
	s := &searches{}
	x.grid.ServeCap(t, sl.PickerCap, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("names")
		s.mu.Lock()
		s.asked = append(s.asked, q)
		s.mu.Unlock()

		var b strings.Builder
		b.WriteString(`<?xml version="1.0" ?><llsd><map><key>agents</key><array>`)
		for _, p := range people {
			if !namesLike(p.Name, q) {
				continue
			}
			// The username is the grid's: the first name alone for
			// somebody whose last name is Resident, and first.last for
			// anybody older, both in lower case.
			first, last, _ := strings.Cut(p.Name, " ")
			user := strings.ToLower(first + "." + last)
			if last == "Resident" {
				user = strings.ToLower(first)
			}
			fmt.Fprintf(&b, `<map><key>id</key><uuid>%s</uuid>`+
				`<key>username</key><string>%s</string>`+
				`<key>legacy_first_name</key><string>%s</string>`+
				`<key>legacy_last_name</key><string>%s</string></map>`, p.ID, user, first, last)
		}
		b.WriteString(`</array></map></llsd>`)
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, b.String())
	})
	return s
}

// searches is what gridOf was asked, which the http server's goroutine
// writes and the test reads.
type searches struct {
	mu    sync.Mutex
	asked []string
}

func (s *searches) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("%q", s.asked)
}

// namesLike is gridOf's matching: every word of the question begins some
// word of the name, ignoring case.
func namesLike(name, q string) bool {
	words := strings.Fields(strings.ToLower(name))
	for _, want := range strings.Fields(strings.ToLower(q)) {
		hit := false
		for _, w := range words {
			hit = hit || strings.HasPrefix(w, want)
		}
		if !hit {
			return false
		}
	}
	return true
}

// addressee is who a message that reaches a person went to, for the
// three shapes those messages come in.
func addressee(m msg.Message) (msg.UUID, bool) {
	switch m := m.(type) {
	case *msg.ImprovedInstantMessage:
		return m.MessageBlock.ToAgentID, true
	case *msg.StartLure:
		if len(m.TargetData) == 1 {
			return m.TargetData[0].TargetID, true
		}
	case *msg.InviteGroupRequest:
		if len(m.InviteData) == 1 {
			return m.InviteData[0].InviteeID, true
		}
	}
	return msg.UUID{}, false
}

// reached is everybody the messages sent so far went to, in order.
func reached(x *testShell) []msg.UUID {
	var out []msg.UUID
	for _, m := range x.grid.Sent() {
		if id, ok := addressee(m); ok {
			out = append(out, id)
		}
	}
	return out
}

// TestANameTypedInFullReachesSomebodyNobodyHereHasHeardOf.
//
// A name that was neither in the region, on the friend list nor in the
// last listing was refused with advice to run lookup and type the
// number it printed -- for invite most of all, where the person being
// invited is quite often not nearby, which is why they are being
// invited.  Seen on Agni between two avatars in different regions.
//
// The whole name is not a guess, so each command that reaches somebody
// takes it from the grid's search and delivers to that person, with the
// rest of the line left as what was said.  The search also turns up a
// second person whose name begins the same way, which is the everyday
// shape of a search result, and a full name has to win over that rather
// than be called ambiguous.
func TestANameTypedInFullReachesSomebodyNobodyHereHasHeardOf(t *testing.T) {
	for _, line := range []string{
		"im Perrick Hobb the dock is finished",
		"offer Perrick Hobb the dock is finished",
		"lure Perrick Hobb the dock is finished",
		"invite Perrick Hobb Example Builders",
		"im perrick hobb the dock is finished",
		"im perrick.hobb the dock is finished",
		"chat Perrick Hobb",
	} {
		x := newTestShell(t)
		joined(x, sl.Group{ID: testBuilders, Name: "Example Builders", Powers: testInvitePowers})
		gridOf(t, x,
			sl.Found{ID: testSomebody, Name: "Perrick Hobb"},
			sl.Found{ID: testOther, Name: "Perrick Hobbinson"})

		got := x.do(t, line)
		if strings.Contains(got, "slsh:") {
			t.Errorf("%q was refused: %q", line, got)
			continue
		}
		if strings.HasPrefix(line, "chat ") {
			if c := x.talk.Current(); c.Target != testSomebody {
				t.Errorf("%q switched to %q", line, c.Label())
			}
			continue
		}
		to := reached(x)
		if len(to) != 1 || to[0] != testSomebody {
			t.Errorf("%q reached %v, want only %s", line, to, testSomebody)
			continue
		}
		if strings.HasPrefix(line, "im ") {
			sent := x.grid.Sent()
			if got := said(t, sent[len(sent)-1]); got != "the dock is finished" {
				t.Errorf("%q said %q, want the message without the name", line, got)
			}
		}
	}
}

// TestPartOfANameOnTheGridIsListedNotGuessed.
//
// The search matches part of a name, so what comes back for a first
// name is everybody who has it.  Taking the only row, or the first,
// would hand a message to whoever the grid happened to put there, and
// nothing here would look amiss -- so the rows are listed, numbered,
// and the line refused, which is the rule the shell already keeps for a
// name that answers to two people nearby.  The number it offers has to
// work, since otherwise the listing is only a longer refusal.
//
// A first name on its own is refused even when it is, letter for
// letter, the whole of somebody's username: the last run a line is
// searched by is its first word, whatever that word was meant as.
//
// Each case has a shell of its own because a search teaches the session
// every name it turned up, and a name the session has heard is matched
// by the start of it like any other.
func TestPartOfANameOnTheGridIsListedNotGuessed(t *testing.T) {
	people := []sl.Found{
		{ID: testSomebody, Name: "Perrick Hobb"},
		{ID: testOther, Name: "Perrick Hobbinson"},
		{ID: testFriend, Name: "Lorn Resident"},
	}

	x := newTestShell(t)
	gridOf(t, x, people...)
	got := x.do(t, "im Perrick the dock is finished")
	if n := len(reached(x)); n != 0 {
		t.Fatalf("part of a name sent %d messages: %q", n, got)
	}
	if !strings.Contains(got, " 1  Perrick Hobb\n") || !strings.Contains(got, " 2  Perrick Hobbinson\n") {
		t.Errorf("the people it could be were not listed: %q", got)
	}
	if !strings.Contains(got, `nobody on the grid is called "Perrick"`) ||
		!strings.Contains(got, "a number picks one") {
		t.Errorf("the refusal under the listing is %q", got)
	}
	x.do(t, "im 2 the dock is finished")
	if to := reached(x); len(to) != 1 || to[0] != testOther {
		t.Errorf("the number offered reached %v, want %s", to, testOther)
	}

	// One row is the grid's best guess and no less a guess for being
	// alone.
	x = newTestShell(t)
	gridOf(t, x, people...)
	got = x.do(t, "offer Hobbin the dock is finished")
	if n := len(reached(x)); n != 0 {
		t.Fatalf("a single partial match was taken: %q", got)
	}
	if !strings.Contains(got, " 1  Perrick Hobbinson\n") ||
		!strings.Contains(got, "the one listed has a name like it") {
		t.Errorf("a single partial match printed %q", got)
	}

	// Letter for letter a Resident's username, and still only a word.
	x = newTestShell(t)
	gridOf(t, x, people...)
	got = x.do(t, "im lorn hello")
	if n := len(reached(x)); n != 0 {
		t.Fatalf("a first name alone was taken from the grid: %q", got)
	}
	if !strings.Contains(got, " 1  Lorn Resident\n") {
		t.Errorf("a first name alone printed %q", got)
	}
	if got := x.do(t, "im Lorn Resident hello"); strings.Contains(got, "slsh:") {
		t.Errorf("the whole name the listing showed was refused: %q", got)
	}

	// And a name like nobody's says the grid was asked, rather than
	// sending somebody to lookup to make the same search again.
	x = newTestShell(t)
	gridOf(t, x, people...)
	got = x.do(t, "im Ruvo hello")
	if !strings.Contains(got, `nobody here or on the grid is called "Ruvo"`) ||
		strings.Contains(got, "try who, friends or lookup") {
		t.Errorf("a name like nobody's printed %q", got)
	}
}

// TestASearchThatCouldNotBeMadeFindsNobody.
//
// The grid's search is the last place looked, and a failure there is
// nobody found -- the refusal the shell always gave, since that is what
// a line nothing here recognises was always going to get.  What changes
// is that it says the grid was not asked, which is the one part worth
// trying again; a refusal reading exactly as it did before would say the
// grid had been searched and had nobody.
//
// A session that was never given the capability is the same case, and
// is refused at once rather than sent the older whole-name message
// lookup falls back on, which has only a deadline to say nobody answered.
func TestASearchThatCouldNotBeMadeFindsNobody(t *testing.T) {
	x := newTestShell(t)
	gridOf(t, x, sl.Found{ID: testSomebody, Name: "Perrick Hobb"})
	x.grid.mu.Lock()
	x.grid.capErr = errors.New("the search timed out")
	x.grid.mu.Unlock()

	got := x.do(t, "im Perrick Hobb the dock is finished")
	if n := len(reached(x)); n != 0 {
		t.Fatalf("a failed search sent %d messages: %q", n, got)
	}
	if !strings.Contains(got, `nobody called "Perrick" is known here`) ||
		!strings.Contains(got, "search could not be made") ||
		!strings.Contains(got, "the search timed out") ||
		!strings.Contains(got, "try who, friends or lookup") {
		t.Errorf("a failed search printed %q", got)
	}

	bare := newTestShell(t)
	start := time.Now()
	got = bare.do(t, "chat Perrick Hobb")
	if !strings.Contains(got, "search could not be made") ||
		!strings.Contains(got, "not given "+sl.PickerCap) {
		t.Errorf("a session without the search printed %q", got)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("a session without the search took %v to refuse", d)
	}
}

// TestTheGridIsAskedOnlyForALineThatWouldHaveFailed.
//
// The search is a round trip, and a name the session or the last
// listing already answers must not pay for it -- nor may a number,
// which means the listing and never somebody called "3".  A line that
// does reach the grid asks it once for each run of words, longest
// first, and stops at the first that is somebody's whole name.
func TestTheGridIsAskedOnlyForALineThatWouldHaveFailed(t *testing.T) {
	x := newTestShell(t)
	asked := gridOf(t, x, sl.Found{ID: testSomebody, Name: "Perrick Hobb"})
	knows(t, x, map[msg.UUID]string{testOther: "Example Resident"})

	x.do(t, "im Example Resident hello")
	x.do(t, "im 3 hello")
	if got := asked.String(); got != `[]` {
		t.Errorf("a line the shell could answer searched the grid for %s", got)
	}
	x.do(t, "im Perrick Hobb hello")
	if got, want := asked.String(), `["Perrick Hobb"]`; got != want {
		t.Errorf("a full name searched for %s, want %s", got, want)
	}

	x = newTestShell(t)
	asked = gridOf(t, x, sl.Found{ID: testSomebody, Name: "Perrick Hobb"})
	x.do(t, "im Perrick hello")
	if got, want := asked.String(), `["Perrick hello" "Perrick"]`; got != want {
		t.Errorf("a first name searched for %s, want %s", got, want)
	}
}

// said is the text of an instant message that went out, which is where
// the difference between the right words and the wrong ones shows.
func said(t *testing.T, m msg.Message) string {
	t.Helper()
	im, ok := m.(*msg.ImprovedInstantMessage)
	if !ok {
		t.Fatalf("the message went out as %T, want an instant message", m)
	}
	return strings.TrimRight(string(im.MessageBlock.Message), "\x00")
}

// TestTheNameIsTheLongestRunOfWordsThatNamesSomebody.
//
// A name has two words in it and sh.who matches either half of one, so
// reading the first word as the name and the rest as the message put
// the last name at the front of what was said.  Measured live, with the
// names changed:
//
//	> [IM Example Resident] Resident hello from the guide
//
// It goes to the right person and says the wrong thing, and nothing on
// this side looks amiss, which is what makes it worth a test rather
// than a rule somebody is meant to remember.
func TestTheNameIsTheLongestRunOfWordsThatNamesSomebody(t *testing.T) {
	x := newTestShell(t)
	knows(t, x, map[msg.UUID]string{testSomebody: "Example Resident"})

	got := x.do(t, "im Example Resident hello from the guide")
	if !strings.Contains(got, "> [IM Example Resident] hello from the guide") {
		t.Errorf("im printed %q", got)
	}
	if got := said(t, x.grid.Sent()[0]); got != "hello from the guide" {
		t.Errorf("what went out was %q, want the message without the last name on it", got)
	}

	// The same rule where there is nothing left to say: the whole line
	// is the name, and the conversation opens rather than a message
	// going out saying "Resident".
	x.setMode(modeCommand)
	if got := x.do(t, "im Example Resident"); got != "" {
		t.Errorf("im with nothing to say printed %q", got)
	}
	if !x.chatting() {
		t.Error("im with no text should open the conversation and enter chat mode")
	}
	if n := len(x.grid.Sent()); n != 1 {
		t.Errorf("%d messages went out, want only the first one", n)
	}
}

// TestAWordOfTheMessageIsNotPartOfTheName, which is the other half of
// the rule: the run shortens until it names somebody, so a one-word
// name keeps the whole of what follows it.
func TestAWordOfTheMessageIsNotPartOfTheName(t *testing.T) {
	x := newTestShell(t)
	knows(t, x, map[msg.UUID]string{testSomebody: "Example Resident"})

	if got := x.do(t, "im Example hello there"); !strings.Contains(got, "] hello there") {
		t.Errorf("im printed %q", got)
	}
	if got := said(t, x.grid.Sent()[0]); got != "hello there" {
		t.Errorf("what went out was %q, want the whole message", got)
	}

	// A message whose first word could start a name is still a message.
	if got := x.do(t, "im Example Examples are hard"); !strings.Contains(got, "] Examples are hard") {
		t.Errorf("im printed %q", got)
	}
	if got := said(t, x.grid.Sent()[1]); got != "Examples are hard" {
		t.Errorf("what went out was %q, want the whole message", got)
	}
}

// TestAnAmbiguousNameStaysAmbiguousRatherThanShortening.
//
// Find matches on prefixes, so everybody a long run could be a short
// one could be too: falling back would answer a refusal that names two
// people with one that names more of them, and it would do it while
// looking like progress.
func TestAnAmbiguousNameStaysAmbiguousRatherThanShortening(t *testing.T) {
	x := newTestShell(t)
	knows(t, x, map[msg.UUID]string{
		testSomebody: "Example Resident", testFriend: "Example Builder",
	})

	got := x.do(t, "im Example hello there")
	if !strings.Contains(got, "could be any of") {
		t.Errorf("an ambiguous name printed %q", got)
	}
	if !strings.Contains(got, "Example Resident") || !strings.Contains(got, "Example Builder") {
		t.Errorf("the refusal should name both, got %q", got)
	}
	if n := len(x.grid.Sent()); n != 0 {
		t.Errorf("%d messages went out on a name nobody resolved", n)
	}

	// Typed in full it is one person again, and the rest is the message.
	if got := x.do(t, "im Example Builder hello there"); !strings.Contains(got, "] hello there") {
		t.Errorf("a name typed in full printed %q", got)
	}
}

// TestGiveAndOfferReadTheNameTheSameWay.
//
// One helper for every command that takes somebody and something else,
// because the mistake is worst here: an item offered under a
// mangled path is either not found at all or is a different item, and
// the person at the other end is the right one either way.
func TestGiveAndOfferReadTheNameTheSameWay(t *testing.T) {
	x := newTestShell(t)
	knows(t, x, map[msg.UUID]string{testSomebody: "Example Resident"})

	if got := x.do(t, "give Example Resident readme"); !strings.Contains(got, `offered "readme" to Example Resident`) {
		t.Errorf("give printed %q", got)
	}
	// The path is what was left after the name, so a one-word name
	// leaves a path of one word as well.
	if got := x.do(t, "give Example readme"); !strings.Contains(got, `offered "readme"`) {
		t.Errorf("give printed %q", got)
	}
	// A name with nothing after it is a give with no item, and says so
	// rather than offering something nobody named.
	if got := x.do(t, "give Example Resident"); !strings.Contains(got, "usage: give") {
		t.Errorf("give with no item printed %q", got)
	}

	if got := x.do(t, "offer Example Resident be my friend"); !strings.Contains(got, "offered friendship to Example Resident") {
		t.Errorf("offer printed %q", got)
	}
	if got := said(t, x.grid.Sent()[len(x.grid.Sent())-1]); got != "be my friend" {
		t.Errorf("the greeting was %q, want the message without the last name on it", got)
	}
}

// TestSomebodyWithNoNameIsTheirId.
//
// A uuid that nobody will name still names one person, so it resolves;
// the id stands in for the name, since a blank there would read as a
// conversation with nobody.
func TestSomebodyWithNoNameIsTheirId(t *testing.T) {
	x := newTestShell(t)

	// Nothing is going to answer, and the shell must not wait out the
	// timeout to find that out.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	id, name, err := x.who(ctx, io.Discard, testOther.String())
	if err != nil || id != testOther {
		t.Fatalf("who(uuid) = %v %q %v", id, name, err)
	}
	if name != testOther.String() {
		t.Errorf("somebody with no name should be their id, got %q", name)
	}
}

// TestFirstWordIsWhatAcceptTakes: the notice says "accept Some", so a
// name of one word has to survive being shortened.
func TestFirstWordIsWhatAcceptTakes(t *testing.T) {
	for in, want := range map[string]string{
		"Some Body": "Some",
		"Somebody":  "Somebody",
		"":          "",
		" Leading":  " Leading", // nothing before the space to take
	} {
		if got := firstWord(in); got != want {
			t.Errorf("firstWord(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestOrIDPrefersTheName, and falls back rather than printing nothing.
func TestOrIDPrefersTheName(t *testing.T) {
	if got := orID("Some Body", testSomebody); got != "Some Body" {
		t.Errorf("orID = %q", got)
	}
	if got := orID("", testSomebody); got != testSomebody.String() {
		t.Errorf("orID with no name = %q, want the id", got)
	}
}
