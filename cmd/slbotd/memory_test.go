package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

// Compaction, which is what an avatar's memory of a long conversation
// is made of.  The thing most worth testing is not that it remembers
// but that it cannot lose the backstory doing it.

// longConversation makes one that has outgrown its budget.
func longConversation(t *testing.T, d *daemon, turns, tokens int) *Conversation {
	t.Helper()
	conv := loaded(t, d.chat.Store(), "example", testSender, "Trusted Resident")
	now := time.Now()
	for i := 0; i < turns/2; i++ {
		conv.Add("user", "crate number "+strings.Repeat("x", 20), now)
		conv.Add("assistant", "salt fish and rope "+strings.Repeat("y", 20), now)
	}
	conv.Tokens = tokens
	if err := d.chat.Store().Save(conv); err != nil {
		t.Fatal(err)
	}
	return conv
}

// The one the whole feature has to get right: a conversation folded
// down to a note still knows what the avatar is.
func TestCompactingCannotLoseTheBackstory(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)

	path := t.TempDir() + "/hobb.txt"
	if err := writeFile(path, "You are Hobb, a weathered dockhand."); err != nil {
		t.Fatal(err)
	}
	d.cfg.Backstory["example"] = path
	d.cfg.ChatContext = 200
	d.chat.cfg = d.cfg

	longConversation(t, d, 20, 900)

	defer serving(t, b)()
	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "and the tide?"))
	waitIMs(t, grid, 1)

	// It compacted...
	if got := f.sawSummaries(); len(got) != 1 {
		t.Fatalf("%d summary requests, want one", len(got))
	}
	// ...and the reply still knows who it is.
	asks := f.sawAsks()
	if len(asks) != 1 {
		t.Fatalf("%d asks", len(asks))
	}
	msgs, _ := asks[0]["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	if !strings.Contains(first["content"].(string), "Hobb") {
		t.Errorf("the backstory did not survive compaction: %v", first["content"])
	}
}

// The summariser is never given the character.  A dockhand asked to
// summarise writes a dockhand's remark about it, and this is the one
// call that is not the avatar speaking.
func TestTheSummariserIsNotGivenTheBackstory(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)

	path := t.TempDir() + "/hobb.txt"
	if err := writeFile(path, "You are Hobb, a weathered dockhand."); err != nil {
		t.Fatal(err)
	}
	d.cfg.Backstory["example"] = path
	d.cfg.ChatContext = 200
	d.chat.cfg = d.cfg
	longConversation(t, d, 20, 900)

	defer serving(t, b)()
	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "and the tide?"))
	waitIMs(t, grid, 1)

	sums := f.sawSummaries()
	if len(sums) != 1 {
		t.Fatalf("%d summary requests", len(sums))
	}
	for _, m := range sums[0]["messages"].([]any) {
		msg, _ := m.(map[string]any)
		if strings.Contains(msg["content"].(string), "Hobb") {
			t.Errorf("the backstory reached the summariser: %v", msg["content"])
		}
	}
}

// The recent turns stay word for word and the older ones become the
// note, which is what keeps the thread of what is being said now.
func TestCompactingKeepsTheRecentTurnsAndFoldsTheRest(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	d.cfg.ChatContext = 200
	d.cfg.ChatKeep = 4
	d.chat.cfg = d.cfg
	longConversation(t, d, 20, 900)

	defer serving(t, b)()
	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "and the tide?"))
	waitIMs(t, grid, 1)

	convs := d.chat.Conversations("example")
	if len(convs) != 1 {
		t.Fatalf("%d conversations", len(convs))
	}
	c := convs[0]
	if c.Summary == "" {
		t.Fatal("nothing was remembered")
	}
	if c.Compacted != 16 {
		t.Errorf("folded %d turns, want the 20 minus the 4 kept", c.Compacted)
	}
	// Four kept, plus the exchange that has just happened.
	if len(c.Turns) != 6 {
		t.Errorf("%d turns kept, want the 4 plus this exchange", len(c.Turns))
	}
}

// What it remembers reaches the next prompt, introduced as memory
// rather than as a document.
func TestWhatIsRememberedReachesThePrompt(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	d.cfg.ChatContext = 200
	d.chat.cfg = d.cfg
	longConversation(t, d, 20, 900)

	defer serving(t, b)()
	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "and the tide?"))
	waitIMs(t, grid, 1)

	asks := f.sawAsks()
	msgs, _ := asks[0]["messages"].([]any)
	var whole strings.Builder
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		whole.WriteString(msg["content"].(string))
		whole.WriteString("\n")
	}
	if !strings.Contains(whole.String(), "Their name is Quark") {
		t.Errorf("the memory did not reach the prompt:\n%s", whole.String())
	}
	if !strings.Contains(whole.String(), Remembered) {
		t.Errorf("the memory was not introduced as memory:\n%s", whole.String())
	}
}

// A model that will not write a note is not a conversation that cannot
// happen: the oldest exchanges are dropped instead, which is what this
// replaced, and the reply still goes out.
func TestAFailedSummaryFallsBackToForgetting(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	f.summaryErr = true
	withChat(t, d, f, Anyone)
	d.cfg.ChatContext = 200
	d.chat.cfg = d.cfg
	longConversation(t, d, 20, 900)

	defer serving(t, b)()
	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "and the tide?"))

	ims := waitIMs(t, grid, 1)
	if got := string(ims[0].MessageBlock.Message); !strings.Contains(got, "Evening") {
		t.Errorf("the turn did not answer: %q", got)
	}
	c := d.chat.Conversations("example")[0]
	if c.Summary != "" {
		t.Error("a failed summary was remembered anyway")
	}
	if len(c.Turns) >= 22 {
		t.Errorf("%d turns: nothing was dropped when the summary failed", len(c.Turns))
	}
}

// Compacting twice folds the note in with what has been said since, so
// one note always stands for the whole conversation before the last few
// exchanges.
func TestCompactingIsCumulative(t *testing.T) {
	d, _, _ := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	d.cfg.ChatContext = 200
	d.chat.cfg = d.cfg

	conv := longConversation(t, d, 20, 900)
	conv.Summary = "An earlier note about the wharf."
	ctx := context.Background()
	slot, _, err := d.chat.slots.take(ctx, conv.Key())
	if err != nil {
		t.Fatal(err)
	}
	d.chat.compact(ctx, conv, slot)
	d.chat.slots.give(slot)

	sums := f.sawSummaries()
	if len(sums) != 1 {
		t.Fatalf("%d summary requests", len(sums))
	}
	msgs := sums[0]["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if !strings.Contains(last["content"].(string), "An earlier note about the wharf") {
		t.Errorf("the previous note was not folded in:\n%v", last["content"])
	}
}

// After folding, the count describes a conversation that no longer
// exists, so it must not be believed: the turn after must not compact
// again on a stale number.
func TestTheTokenCountIsForgottenAfterFolding(t *testing.T) {
	d, _, _ := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	d.cfg.ChatContext = 200
	d.chat.cfg = d.cfg

	conv := longConversation(t, d, 20, 900)
	ctx := context.Background()
	slot, _, _ := d.chat.slots.take(ctx, conv.Key())
	d.chat.compact(ctx, conv, slot)
	d.chat.slots.give(slot)

	if conv.Tokens != 0 {
		t.Errorf("Tokens = %d after folding, want it unknown", conv.Tokens)
	}
	if d.chat.shouldCompact(conv) {
		t.Error("it would compact again straight away")
	}
	if conv.State != "" || conv.By != "" {
		t.Errorf("the kept context still claims to describe the old history: %q %q",
			conv.State, conv.By)
	}
}

// A short conversation is left alone.
func TestAConversationInsideItsBudgetIsNotFolded(t *testing.T) {
	d, _, _ := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	d.chat.cfg = d.cfg

	conv := longConversation(t, d, 4, 100)
	if d.chat.shouldCompact(conv) {
		t.Error("a short conversation would be folded")
	}
}
