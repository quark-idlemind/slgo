package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// The conversation itself: what goes to the model, what is dropped when
// there is too much of it, and what survives being written down.

func TestThePromptIsBackstoryThenEverythingSaid(t *testing.T) {
	c := &Conversation{Avatar: "example"}
	now := time.Now()
	c.Add("user", "hello", now)
	c.Add("assistant", "evening", now)

	got := c.Prompt("You are Hobb, a dockhand.", "what is the tide doing?", true)
	if len(got) != 4 {
		t.Fatalf("%d messages, want system, two turns and the new remark", len(got))
	}
	if got[0].Role != "system" {
		t.Errorf("the first message is %q", got[0].Role)
	}
	if !strings.Contains(got[0].Content, "Hobb") {
		t.Errorf("the backstory is not in the system message: %q", got[0].Content)
	}
	// The medium is appended rather than written into the character, so
	// that somebody writing a backstory does not have to explain the
	// plumbing to it.
	if !strings.Contains(got[0].Content, "instant messages") {
		t.Errorf("the medium is not in the system message: %q", got[0].Content)
	}
	if got[3].Role != "user" || got[3].Content != "what is the tide doing?" {
		t.Errorf("the new remark came out as %+v", got[3])
	}
}

// An avatar with no backstory still gets told where it is talking,
// rather than getting an empty system message.
func TestNoBackstoryStillSaysWhereItIs(t *testing.T) {
	got := (&Conversation{}).Prompt("", "hello", true)
	if got[0].Role != "system" || !strings.Contains(got[0].Content, "instant messages") {
		t.Errorf("got %+v", got[0])
	}
}

// Turns go in pairs, oldest first: half an exchange left at the front
// is a question nobody answered.
func TestTrimmingDropsWholeExchanges(t *testing.T) {
	c := &Conversation{Avatar: "example"}
	now := time.Now()
	for i := 0; i < 10; i++ {
		c.Add("user", strings.Repeat("a", 100), now)
		c.Add("assistant", strings.Repeat("b", 100), now)
	}
	c.Tokens = 500 // as the server counted it

	if !c.Trim(200) {
		t.Fatal("a conversation over its budget was not trimmed")
	}
	if len(c.Turns)%2 != 0 {
		t.Errorf("%d turns left, which is half an exchange", len(c.Turns))
	}
	if len(c.Turns) == 20 {
		t.Error("nothing was dropped")
	}
	if c.Turns[0].Role != "user" {
		t.Errorf("the conversation now starts with %q", c.Turns[0].Role)
	}
	// It aims below the budget rather than at it, so that it does not
	// trim one pair on every turn for the rest of its life.
	if c.Tokens > 200 {
		t.Errorf("still %d tokens after trimming to 200", c.Tokens)
	}
}

func TestAConversationInsideItsBudgetIsLeftAlone(t *testing.T) {
	c := &Conversation{Avatar: "example", Tokens: 100}
	c.Add("user", "hello", time.Now())
	c.Add("assistant", "evening", time.Now())
	if c.Trim(1536) {
		t.Error("a short conversation was trimmed")
	}
	if len(c.Turns) != 2 {
		t.Errorf("%d turns left", len(c.Turns))
	}
}

// ------------------------------------------------------------- the store

func TestAConversationSurvivesBeingWrittenDown(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	who := msg.MustParseUUID("19d17e57-7e57-c0de-11be-a4325a5080a2")

	c := store.Load("example", who, "Somebody Else")
	if len(c.Turns) != 0 {
		t.Fatal("a conversation that never happened came back with turns in it")
	}
	c.Add("user", "hello", time.Now())
	c.Add("assistant", "evening", time.Now())
	c.Tokens, c.State, c.By = 42, "example-"+who.String()+".bin", "fingerprint"
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}

	back := store.Load("example", who, "")
	if len(back.Turns) != 2 || back.Turns[1].Text != "evening" {
		t.Errorf("came back as %+v", back.Turns)
	}
	if back.Tokens != 42 || back.State == "" || back.By != "fingerprint" {
		t.Errorf("the bookkeeping was lost: %+v", back)
	}
	// The name is refreshed from what the message carried, since people
	// rename themselves and the record should not go stale.
	if again := store.Load("example", who, "New Name"); again.WithName != "New Name" {
		t.Errorf("WithName = %q", again.WithName)
	}
}

// One unreadable conversation must not take away the answer to "who
// have you been talking to".
func TestAnUnreadableConversationIsSkipped(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	good := msg.MustParseUUID("19d17e57-7e57-c0de-11be-a4325a5080a2")
	c := store.Load("example", good, "Somebody Else")
	c.Add("user", "hello", time.Now())
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(dir+"/example/garbage.json", "not json at all"); err != nil {
		t.Fatal(err)
	}

	got := store.List("example")
	if len(got) != 1 || got[0].With != good.String() {
		t.Errorf("listed %d conversations, want the one that parses", len(got))
	}
}

func TestForgettingRemovesTheConversation(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)
	who := msg.MustParseUUID("19d17e57-7e57-c0de-11be-a4325a5080a2")
	c := store.Load("example", who, "Somebody Else")
	c.Add("user", "hello", time.Now())
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}
	if err := store.Forget("example", who.String(), ""); err != nil {
		t.Fatal(err)
	}
	if got := store.List("example"); len(got) != 0 {
		t.Errorf("%d conversations left", len(got))
	}
	if err := store.Forget("example", who.String(), ""); err == nil {
		t.Error("forgetting nothing was not reported")
	}
}

// ------------------------------------------------------------- the slots

// A conversation that still holds the slot it spoke in finds its kv
// cache already there and pays nothing.  That is the whole point of the
// affinity, so it is worth a test of its own.
func TestASlotStaysWithItsConversation(t *testing.T) {
	p := newSlotPool(2)
	ctx := context.Background()

	a, mine, err := p.take(ctx, "one")
	if err != nil || mine {
		t.Fatalf("first take = %d, mine=%v, %v", a, mine, err)
	}
	p.give(a)

	again, mine, err := p.take(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if again != a || !mine {
		t.Errorf("took slot %d (mine=%v), want %d and mine", again, mine, a)
	}
	p.give(again)
}

// With more conversations than slots, the one that spoke longest ago
// gives its slot up.
func TestTheLeastRecentConversationGivesUpItsSlot(t *testing.T) {
	p := newSlotPool(2)
	ctx := context.Background()

	first, _, _ := p.take(ctx, "one")
	p.give(first)
	second, _, _ := p.take(ctx, "two")
	p.give(second)
	if first == second {
		t.Fatal("two conversations were given the same slot while one was free")
	}

	// "one" spoke longest ago, so a third conversation takes its slot.
	third, mine, _ := p.take(ctx, "three")
	if mine {
		t.Error("a conversation that has never spoken was told the slot was already its own")
	}
	if third != first {
		t.Errorf("took slot %d, want %d, which spoke longest ago", third, first)
	}
	p.give(third)

	// And "one" now has to be restored rather than found in place.
	back, mine, _ := p.take(ctx, "one")
	if mine {
		t.Errorf("slot %d claimed to still hold a conversation it gave up", back)
	}
	p.give(back)
}

// Every slot busy means waiting, and waiting must be something the
// daemon can stop doing.
func TestWaitingForASlotEndsWhenTheDaemonDoes(t *testing.T) {
	p := newSlotPool(1)
	held, _, err := p.take(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := p.take(ctx, "two"); done <- err }()

	select {
	case err := <-done:
		t.Fatalf("took a slot that was busy: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("waiting ended without an error when it was cancelled")
		}
	case <-time.After(5 * time.Second):
		t.Error("cancelling did not stop the wait")
	}
	p.give(held)
}

// A slot handed back wakes whoever is waiting for one.
func TestGivingASlotBackWakesAWaiter(t *testing.T) {
	p := newSlotPool(1)
	held, _, _ := p.take(context.Background(), "one")

	var wg sync.WaitGroup
	wg.Add(1)
	got := make(chan int, 1)
	go func() {
		defer wg.Done()
		s, _, err := p.take(context.Background(), "two")
		if err == nil {
			got <- s
			p.give(s)
		}
	}()

	time.Sleep(50 * time.Millisecond)
	p.give(held)
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Error("a waiter was not woken when the slot was given back")
	}
	wg.Wait()
}

// ---------------------------------------------------------- the template

// A memory dropped by a chat template is the worst failure available
// here: the avatar answers fluently, having forgotten everything, and
// nothing says so.  So it is asked rather than assumed.
func TestTheTemplateIsAskedWhereMemoryCanGo(t *testing.T) {
	d, _, _ := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)

	if !d.chat.separateMemory(context.Background()) {
		t.Error("a template that keeps both system messages was not believed")
	}
}

func TestATemplateThatDropsSystemMessagesIsNoticed(t *testing.T) {
	d, _, _ := newTestDaemon(t)
	f := newFakeLLM(t)
	f.dropsSystem = true
	withChat(t, d, f, Anyone)

	if d.chat.separateMemory(context.Background()) {
		t.Error("a template that drops the second system message was used anyway")
	}

	// And the memory still gets through, joined onto the backstory.
	c := &Conversation{Summary: "They are called Quark."}
	got := c.Prompt("You are Hobb.", "hello", false)
	if len(got) != 2 {
		t.Fatalf("%d messages, want one system message and the remark", len(got))
	}
	if !strings.Contains(got[0].Content, "Hobb") || !strings.Contains(got[0].Content, "Quark") {
		t.Errorf("the character and the memory are not both there: %q", got[0].Content)
	}
}

// With two messages the backstory is byte for byte the same one every
// turn, which is what keeps it a prefix the cache can match across a
// compaction.
func TestTheBackstoryIsItsOwnMessageWhenItCanBe(t *testing.T) {
	with := (&Conversation{Summary: "They are called Quark."}).Prompt("You are Hobb.", "hello", true)
	without := (&Conversation{}).Prompt("You are Hobb.", "hello", true)
	if with[0].Content != without[0].Content {
		t.Errorf("the backstory message changed when a memory appeared:\n%q\n%q",
			without[0].Content, with[0].Content)
	}
	if len(with) != 3 || with[1].Role != "system" {
		t.Fatalf("got %d messages, want character, memory and the remark", len(with))
	}
}
