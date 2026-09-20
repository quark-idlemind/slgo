package main

// What this avatar said through another client of the same session.
//
// The dangerous half of this feature is not the display, it is that an
// avatar with a model behind it now hears its own voice.  Answering
// that is not a cosmetic fault: with "chat = *" the avatar is in its
// own audience, so a reply becomes a remark becomes a reply, and it
// does not stop.

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
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
	conv := store.Load("example", testSender, "Trusted Resident")
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
	conv := store.Load("example", testSender, "Trusted Resident")
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
	conv := store.Load("example", testSender, "Trusted Resident")
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
	conv := store.Load("example", testSender, "Trusted Resident")
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
