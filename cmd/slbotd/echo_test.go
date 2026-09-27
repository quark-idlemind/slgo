package main

// What this avatar said through another client of the same session.
//
// The dangerous half of this feature is not the display, it is that an
// avatar with a model behind it now hears its own voice.  Answering
// that is not a cosmetic fault: with "chat = *" the avatar is in its
// own audience, so a reply becomes a remark becomes a reply, and it
// does not stop.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
	"github.com/quark-idlemind/slgo/sl"
)

// echoed builds the message slgod relays when another client of this
// session sends one.
func echoed(to msg.UUID, text string) *sl.IM {
	return &sl.IM{
		At:     time.Now(),
		From:   testMe,
		To:     to,
		Text:   text,
		Dialog: sl.DialogMessage,
		Mine:   true,
		Via:    "another client",
	}
}

// The test this whole branch exists for.
func TestAnAvatarDoesNotAnswerItself(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone) // in its own audience, which is the trap
	defer serving(t, b)()

	// A conversation already under way, so nothing is skipped for want
	// of one.
	store := d.chat.Store()
	conv := loaded(t, store, "example", testSender, "Trusted Resident")
	conv.Add("user", "evening", time.Now())
	conv.Add("assistant", "evening yourself", time.Now())
	if err := store.Save(conv); err != nil {
		t.Fatal(err)
	}

	b.arrived(t.Context(), b.Session(), echoed(testSender, "three coils, Quark"), waitGroup())
	quiet(grid)

	if ims := grid.IMsSent(); len(ims) != 0 {
		t.Fatalf("the avatar answered its own remark: %q",
			string(ims[0].MessageBlock.Message))
	}
	if asks := f.sawAsks(); len(asks) != 0 {
		t.Errorf("the model was asked about the avatar's own remark %d times", len(asks))
	}
}

// sl.IM.Conversation is the rule that protects every caller, including
// ones written before any of this existed, so it is asserted here as
// well as in sl.
func TestAnEchoIsNotSomebodyTalkingToUs(t *testing.T) {
	m := echoed(testSender, "three coils")
	if m.Conversation() {
		t.Error("an echo reads as somebody talking to this avatar")
	}
	if !m.Spoken() {
		t.Error("an echo does not read as conversation at all, so nothing would show it")
	}
}

// One avatar, one memory: a person answering through slsh is that
// avatar speaking, and the note should fold in what they said.
func TestWhatWasSaidElsewhereIsRemembered(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	defer serving(t, b)()

	store := d.chat.Store()
	conv := loaded(t, store, "example", testSender, "Trusted Resident")
	conv.Add("user", "can you hold three coils?", time.Now())
	conv.Add("assistant", "aye", time.Now())
	if err := store.Save(conv); err != nil {
		t.Fatal(err)
	}

	b.arrived(t.Context(), b.Session(), echoed(testSender, "until Thursday, mind"), waitGroup())

	deadline := time.Now().Add(5 * time.Second)
	for {
		got := d.chat.Conversations("example")
		if len(got) == 1 && len(got[0].Turns) == 3 {
			last := got[0].Turns[2]
			if last.Role != "assistant" || last.Text != "until Thursday, mind" {
				t.Errorf("recorded %+v", last)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("what was said elsewhere was never recorded: %v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A conversation that does not exist is not started by one line the
// avatar said through something else.  A store full of one-line files
// nobody replied to is not worth the disk.
func TestSpeakingToAStrangerElsewhereStartsNoConversation(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	defer serving(t, b)()

	b.arrived(t.Context(), b.Session(), echoed(testStranger, "hello there"), waitGroup())
	time.Sleep(300 * time.Millisecond)

	if got := d.chat.Conversations("example"); len(got) != 0 {
		t.Errorf("%d conversations started by a line said elsewhere", len(got))
	}
}

// Only conversation is kept.  An offer answered or a lure taken through
// another client is not something anybody said, and a memory of it
// would be noise in the note.
func TestOnlyConversationIsRememberedFromElsewhere(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	defer serving(t, b)()

	store := d.chat.Store()
	conv := loaded(t, store, "example", testSender, "Trusted Resident")
	conv.Add("user", "evening", time.Now())
	conv.Add("assistant", "evening", time.Now())
	if err := store.Save(conv); err != nil {
		t.Fatal(err)
	}

	answered := echoed(testSender, "")
	answered.Dialog = sl.DialogInventoryAccepted
	b.arrived(t.Context(), b.Session(), answered, waitGroup())
	time.Sleep(300 * time.Millisecond)

	got := d.chat.Conversations("example")
	if len(got) != 1 || len(got[0].Turns) != 2 {
		t.Errorf("an answered offer was recorded as something said: %v", got[0].Turns)
	}
}

// Two writers on one conversation used to be one: a reply being
// composed, and a line said elsewhere.  Both read, add and write back,
// so without a lock the later write drops whatever the earlier added --
// and what it drops is somebody's remark.
func TestARemarkFromElsewhereIsNotLostToAReplyInFlight(t *testing.T) {
	d, _, _ := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)

	store := d.chat.Store()
	conv := loaded(t, store, "example", testSender, "Trusted Resident")
	conv.Add("user", "evening", time.Now())
	conv.Add("assistant", "evening", time.Now())
	if err := store.Save(conv); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{}, 2)
	go func() {
		d.chat.Remember("example", testSender, "Trusted Resident", "said elsewhere")
		done <- struct{}{}
	}()
	go func() {
		d.chat.Reply(t.Context(), "example", testSender, "Trusted Resident", "and now?")
		done <- struct{}{}
	}()
	<-done
	<-done

	got := d.chat.Conversations("example")
	if len(got) != 1 {
		t.Fatalf("%d conversations", len(got))
	}
	var texts []string
	for _, turn := range got[0].Turns {
		texts = append(texts, turn.Text)
	}
	whole := strings.Join(texts, "|")
	for _, want := range []string{"said elsewhere", "and now?"} {
		if !strings.Contains(whole, want) {
			t.Errorf("%q was lost: the conversation is %q", want, whole)
		}
	}
}

// Two avatars attended by ONE daemon, each with the other in its
// audience, answer each other for ever.  Neither is answering itself,
// so the rule that stops an echo does not touch this: from each side it
// is an ordinary conversation with somebody who keeps replying.
//
// Measured before it was fixed: one message typed by hand from one of
// them to another ran to 26 exchanges in ninety seconds on the live
// grid, and was still going when it was stopped by hand.
func TestOneDaemonsAvatarsStopTalkingToEachOther(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone) // the setting that makes it possible
	d.cfg.ChatOwn = 4
	d.chat.cfg = d.cfg
	d.audience = ownAvatarsBounded(d, d.cfg.ChatOwn, listAudience(d.cfg))
	defer serving(t, b)()

	// A second avatar, attended by this same daemon, speaking to the
	// first.  testMe is what the fake grid calls every session, so the
	// other bot's id is this one's.
	other := newBot(d, "builder")
	other.setSession(b.Session())
	other.setState(stateAttached, "Builder Resident")
	d.mu.Lock()
	d.bots["builder"] = other
	d.order = append(d.order, "builder")
	d.mu.Unlock()

	// It answers while the conversation is short: two of them talking
	// is not a malfunction.
	grid.deliver(t, incoming(testMe, "Builder Resident", sl.DialogMessage, "evening"))
	waitIMs(t, grid, 1)
	grid.deliver(t, incoming(testMe, "Builder Resident", sl.DialogMessage, "and the wharf?"))
	waitIMs(t, grid, 2)

	// And then one of them stops, which is the only way it can end:
	// the far side is as tireless as this one.
	before := len(grid.IMsSent())
	grid.deliver(t, incoming(testMe, "Builder Resident", sl.DialogMessage, "and the tide?"))
	quiet(grid)
	if got := len(grid.IMsSent()); got != before {
		t.Fatalf("it answered %d more times past the bound of %d turns",
			got-before, d.cfg.ChatOwn)
	}
}

// Zero is never, for an operator who would rather they did not start.
func TestZeroMeansOurAvatarsNeverTalk(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	d.cfg.ChatOwn = 0
	d.audience = ownAvatarsBounded(d, 0, listAudience(d.cfg))
	defer serving(t, b)()

	grid.deliver(t, incoming(testMe, "Example Resident", sl.DialogMessage, "evening"))
	quiet(grid)
	if ims := grid.IMsSent(); len(ims) != 0 {
		t.Fatalf("answered another of this daemon's avatars: %q",
			string(ims[0].MessageBlock.Message))
	}
}

// A person is still a person.  The rule is about avatars this daemon is
// driving and must not quietly refuse everybody.
func TestAPersonIsStillAnsweredWithTheRuleOn(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	d.audience = ownAvatarsBounded(d, d.cfg.ChatOwn, listAudience(d.cfg))
	defer serving(t, b)()

	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "evening"))
	ims := waitIMs(t, grid, 1)
	if got := string(ims[0].MessageBlock.Message); !strings.Contains(got, "Evening") {
		t.Errorf("answered %q", got)
	}
}

// An attendant with no session is not driving that avatar just now, so
// whoever is using it is a person and is answered like one.
func TestADetachedAvatarIsNotTreatedAsOurOwn(t *testing.T) {
	d, _, _ := newTestDaemon(t)
	other := newBot(d, "builder")
	d.mu.Lock()
	d.bots["builder"] = other
	d.order = append(d.order, "builder")
	d.mu.Unlock()

	if who, ok := d.AvatarFor(testMe); !ok || who != "example" {
		t.Errorf("AvatarFor(attached) = %q, %v, want the attached one", who, ok)
	}
	if _, ok := d.AvatarFor(testStranger); ok {
		t.Error("a stranger was taken for one of this daemon's avatars")
	}
	if _, ok := d.AvatarFor(msg.UUID{}); ok {
		t.Error("a zero id was taken for one of this daemon's avatars")
	}
}

// slgod ends the stream of a session that is not coming back -- logged
// out on purpose, thrown off, or no longer hosted -- and keeps it open
// across one that dropped and is being re-established.  So the stream
// ending is how an attendant learns its session has gone for good, and
// a notice is something to log: nothing is asked of slgod on the
// strength of one.
//
// It used to ask.  The stream stayed open under a session that was
// never coming back, so the attendant asked slgod after every
// disconnection whether the avatar had been stopped; before it did
// that, it sat holding a session that no longer existed until the
// daemon was restarted.
func TestTheStreamEndingIsWhatLetsASessionGo(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	var asked atomic.Int64
	d.agents = func(ctx context.Context) ([]*pb.AgentInfo, error) {
		asked.Add(1)
		return []*pb.AgentInfo{{Name: "example", State: pb.AgentInfo_STOPPED}}, nil
	}

	s := b.Session()
	ims := s.IMs(IMDepth)
	done := make(chan struct{})
	go func() { defer close(done); b.read(context.Background(), s, ims, grid.notices) }()

	// While the stream is open the session is slgod's to bring back,
	// whatever slgod would say if asked.
	grid.notices <- &pb.AgentEvent{
		Kind:   pb.AgentEvent_DISCONNECTED,
		Detail: "logged out on request; it will not come back until asked for by name",
	}
	select {
	case <-done:
		t.Fatal("let go of a session on the strength of a notice, with its stream still open")
	case <-time.After(500 * time.Millisecond):
	}

	grid.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the attendant held on to a session whose stream had ended")
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("slgod was asked %d time(s) whether the session had stopped", n)
	}
}

// An avatar another program's model drives, named with chat-bot, is
// bounded as this daemon's own are -- and only while the two have not
// rested.  A person, or a bot after a rest, is answered.
func TestAnOutsideBotIsBoundedUntilTheyRest(t *testing.T) {
	d, _, _ := newTestDaemon(t)
	d.cfg.chatBotNames = map[string]string{"stranger bot": "Stranger Bot"}
	d.cfg.ChatOwnRest = 30 * time.Minute
	yes := func(context.Context, *Approach) Verdict { return Verdict{true, "yes"} }
	bound := ownAvatarsBounded(d, 4, yes)
	ctx := context.Background()

	if v := bound(ctx, &Approach{From: testStranger, Name: "Stranger Bot", Recent: 3, Turns: 40}); !v.Talk {
		t.Errorf("below the bound since the last rest: %s", v.Why)
	}
	v := bound(ctx, &Approach{From: testStranger, Name: "stranger bot", Recent: 4})
	if v.Talk {
		t.Error("an outside bot was answered past the bound")
	}
	if !strings.Contains(v.Why, "another program's model") || !strings.Contains(v.Why, "30m0s") {
		t.Errorf("the reason does not say why or until when: %s", v.Why)
	}
	if v := bound(ctx, &Approach{From: testSender, Name: "Trusted Resident", Recent: 40}); !v.Talk {
		t.Errorf("a person was bounded: %s", v.Why)
	}

	d.cfg.chatBotIDs = map[msg.UUID]bool{testSender: true}
	if v := bound(ctx, &Approach{From: testSender, Name: "Somebody Else", Recent: 4}); v.Talk {
		t.Error("a bot named by id was answered past the bound")
	}
}
