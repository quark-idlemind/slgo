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
	"io"
	"net/http"
	"strings"
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

// TestAScriptAskingForSomethingSaysHowToAnswerIt.
//
// A dialog and a permission request both wait for an answer that has
// to be typed, and neither says how on its own -- so the notice
// carries the command, channel and all.
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
	if !strings.Contains(got, "say -c -1379 BUTTON") {
		t.Errorf("the notice should say how to answer:\n%s", got)
	}
	if !strings.Contains(got, "[on off]") {
		t.Errorf("the notice should list the buttons:\n%s", got)
	}

	q := &msg.ScriptQuestion{}
	q.Data.TaskID = testLamp
	q.Data.ItemID = testProbe
	q.Data.ObjectName = append([]byte("a lamp"), 0)
	q.Data.ObjectOwner = append([]byte("Quark Idlemind"), 0)
	q.Data.Questions = 2 // take controls
	x.grid.Relay(t, q)

	if got := waits(t, x, "a lamp wants"); !strings.Contains(got, "accept-perms") {
		t.Errorf("a permission request should say how to grant it:\n%s", got)
	}
}

// TestTheChatPrinterStopsWhenTheSessionDoes.
//
// A shell whose connection has gone must not be left with a goroutine
// waiting on channels nobody will ever put anything on.  It waits on
// three of them and any one closing is enough, which is as well: only
// the chat one ever gets there.  A goroutine parked in a select is
// woken by whichever channel closes FIRST rather than choosing between
// them afterwards, and the session closes the chat subscriptions before
// the permission ones -- so the permission arm never wins the race, and
// the instant message arm is never closed at all.
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
// it waits on all three at once and the other two closed -- anything
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
	id, name, err := x.who(context.Background(), "1")
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
	if id, _, err := x.who(context.Background(), "2"); err != nil || id != testSomebody {
		t.Errorf("who(2) = %v %v, want the second line", id, err)
	}

	if got := x.do(t, "lookup"); !strings.Contains(got, "usage: lookup") {
		t.Errorf("lookup with nothing to look up printed %q", got)
	}
	if got := x.do(t, "lookup --help"); !strings.Contains(got, "TEXT") {
		t.Errorf("lookup --help printed %q", got)
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

	if got, want := x.do(t, "offers"), "no offers waiting\n"; got != want {
		t.Errorf("offers printed %q, want %q", got, want)
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

	// Two of them and nothing else waiting is an ambiguity as well.
	x.grid.Relay(t, offering(testFriend, "A Friend", "a lamp", sl.AssetObject, testLamp))
	x.grid.Relay(t, offering(testOther, "Some Other", "a notecard", sl.AssetNotecard, testNote))
	waitForOffers(t, x, 0, 2)
	if got := x.do(t, "accept"); !strings.Contains(got, "no offers waiting") {
		t.Errorf("accept with two inventory offers printed %q", got)
	}
	if got := x.do(t, "decline a notecard"); !strings.Contains(got, `declined "a notecard"`) {
		t.Errorf("decline of a named inventory offer printed %q", got)
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

	id, name, err := x.who(ctx, testSomebody.String())
	if err != nil || id != testSomebody || name != "Some Body" {
		t.Errorf("who(uuid) = %v %q %v", id, name, err)
	}
	// A name the session has heard, in whole or in part.
	if id, _, err := x.who(ctx, "some body"); err != nil || id != testSomebody {
		t.Errorf("who(name) = %v %v", id, err)
	}
	if id, _, err := x.who(ctx, "some"); err != nil || id != testSomebody {
		t.Errorf("who(part of a name) = %v %v", id, err)
	}
	if _, _, err := x.who(ctx, "nobody of that name"); err == nil {
		t.Error("a name nobody has should not resolve to anybody")
	}

	// Two people whose names begin alike cannot be told apart, and are
	// not guessed between.
	knows(t, x, map[msg.UUID]string{testFriend: "Some Other"})
	_, _, err = x.who(ctx, "some")
	if err == nil || !strings.Contains(err.Error(), "could be any of") {
		t.Errorf("an ambiguous name should list them, got %v", err)
	}
	if !strings.Contains(err.Error(), "Some Body") || !strings.Contains(err.Error(), "Some Other") {
		t.Errorf("the refusal should name both, got %v", err)
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

	id, name, err := x.who(ctx, testOther.String())
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
