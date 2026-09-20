package main

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// A remark arriving at an avatar and an answer going back: the whole
// path, with a model that is not there and a grid that is not there.

// withChat gives a test daemon a model to talk to and a list of who it
// will talk to.
func withChat(t *testing.T, d *daemon, f *fakeLLM, chat ...string) {
	t.Helper()
	d.cfg.LLMURL = f.URL
	d.cfg.Chat = chat
	d.cfg.ChatDir = t.TempDir()
	// No pacing unless a test asks for it.  Holding every reply back by
	// the seconds a person would have taken is the point of pace.go and
	// pure cost everywhere else; pace_test.go sets its own.
	d.cfg.ReadCPS, d.cfg.TypeCPS = 0, 0
	c, err := NewChatter(d.cfg, func(string, ...any) {})
	if err != nil {
		t.Fatalf("NewChatter: %v", err)
	}
	d.chat = c
	d.audience = listAudience(d.cfg)
}

func TestARemarkIsAnswered(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	f.reply = "Evening. The tide is out."
	withChat(t, d, f, "Trusted Resident")
	defer serving(t, b)()

	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "what is the tide doing?"))

	ims := waitIMs(t, grid, 1)
	if got := string(ims[0].MessageBlock.Message); !strings.Contains(got, "The tide is out") {
		t.Errorf("answered %q", got)
	}
	if ims[0].MessageBlock.ToAgentID != testSender {
		t.Errorf("answered %s", ims[0].MessageBlock.ToAgentID)
	}

	// What was sent to the model is the conversation, and the backstory
	// slot is filled even with no backstory configured.
	asks := f.sawAsks()
	if len(asks) != 1 {
		t.Fatalf("%d asks", len(asks))
	}
	msgs, _ := asks[0]["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("%d messages sent to the model, want a system message and the remark", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("the first message is %v", first["role"])
	}
}

// Somebody not on the list gets silence, and the model is not asked at
// all -- the decision is taken before anything is spent on it.
func TestSomebodyNotOnTheListIsNotAnswered(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, "Trusted Resident")
	defer serving(t, b)()

	grid.deliver(t, incoming(testStranger, "Some Body", sl.DialogMessage, "hello there"))
	quiet(grid)

	if ims := grid.IMsSent(); len(ims) != 0 {
		t.Errorf("a stranger was answered: %q", string(ims[0].MessageBlock.Message))
	}
	if asks := f.sawAsks(); len(asks) != 0 {
		t.Errorf("the model was asked %d times about somebody who is not answered", len(asks))
	}
}

// The star means anyone, including somebody the daemon has never heard
// of and does not take commands from.
func TestTheStarAnswersAStranger(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	defer serving(t, b)()

	grid.deliver(t, incoming(testStranger, "Some Body", sl.DialogMessage, "hello there"))
	ims := waitIMs(t, grid, 1)
	if got := string(ims[0].MessageBlock.Message); !strings.Contains(got, "Evening") {
		t.Errorf("answered %q", got)
	}
}

// A command is still a command when chat is on.  The prefix decides,
// and it decides first: an avatar that answered ":where" with small
// talk would be one nobody could drive.
func TestACommandIsStillACommandWhenChatIsOn(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	defer serving(t, b)()

	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, ":where"))
	ims := waitIMs(t, grid, 1)
	if got := string(ims[0].MessageBlock.Message); !strings.Contains(got, "Nowhere at 128") {
		t.Errorf("answered %q", got)
	}
	if asks := f.sawAsks(); len(asks) != 0 {
		t.Error("a command was sent to the model")
	}
}

// The conversation is written down and the kv cache kept, which is what
// makes the next remark cheap.
func TestTheConversationIsKept(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	defer serving(t, b)()

	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "hello"))
	waitIMs(t, grid, 1)

	convs := d.chat.Conversations("example")
	if len(convs) != 1 {
		t.Fatalf("%d conversations kept", len(convs))
	}
	c := convs[0]
	if len(c.Turns) != 2 || c.Turns[0].Text != "hello" || c.Turns[1].Role != "assistant" {
		t.Errorf("kept %+v", c.Turns)
	}
	if c.Tokens != 120 {
		t.Errorf("Tokens = %d, want what the server counted", c.Tokens)
	}
	if c.State == "" || c.By == "" {
		t.Errorf("the kv cache was not recorded: state=%q by=%q", c.State, c.By)
	}
	if saved := f.sawSaved(); len(saved) != 1 || saved[0] != c.State {
		t.Errorf("saved %v, want %q", saved, c.State)
	}
}

// The second remark in one conversation finds its slot still holding
// it, so nothing is restored.  That is the affinity earning its keep.
func TestASecondRemarkNeedsNoRestore(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	defer serving(t, b)()

	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "hello"))
	waitIMs(t, grid, 1)
	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "and the tide?"))
	waitIMs(t, grid, 2)

	if got := f.sawRestored(); len(got) != 0 {
		t.Errorf("restored %v, want nothing: the slot still held the conversation", got)
	}
	// And the second prompt carries the first exchange.
	asks := f.sawAsks()
	if len(asks) != 2 {
		t.Fatalf("%d asks", len(asks))
	}
	msgs, _ := asks[1]["messages"].([]any)
	if len(msgs) != 4 {
		t.Errorf("%d messages on the second ask, want system, two turns and the remark", len(msgs))
	}
}

// A state file that will not load is not a conversation that cannot
// happen.  The text is the record; the cache is only ever an
// accelerator, and losing it costs prefill and nothing else.
func TestAnUnloadableContextStillAnswers(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	f.restoreErr = true
	withChat(t, d, f, Anyone)

	// A conversation that already has a state file recorded, as one
	// would after a restart.
	store := d.chat.Store()
	conv := store.Load("example", testSender, "Trusted Resident")
	conv.Add("user", "hello", time.Now())
	conv.Add("assistant", "evening", time.Now())
	props, err := d.chat.Model(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conv.State, conv.By = conv.stateName(), props.Fingerprint("")
	if err := store.Save(conv); err != nil {
		t.Fatal(err)
	}

	defer serving(t, b)()
	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "still there?"))

	ims := waitIMs(t, grid, 1)
	if got := string(ims[0].MessageBlock.Message); !strings.Contains(got, "Evening") {
		t.Errorf("answered %q", got)
	}
}

// A conversation whose fingerprint has moved is not restored at all --
// the server would load it happily and answer out of another model's
// tokens.
func TestAStaleFingerprintIsNotRestored(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)

	store := d.chat.Store()
	conv := store.Load("example", testSender, "Trusted Resident")
	conv.Add("user", "hello", time.Now())
	conv.Add("assistant", "evening", time.Now())
	conv.State, conv.By = conv.stateName(), "a fingerprint from another model"
	if err := store.Save(conv); err != nil {
		t.Fatal(err)
	}

	defer serving(t, b)()
	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "still there?"))
	waitIMs(t, grid, 1)

	if got := f.sawRestored(); len(got) != 0 {
		t.Errorf("restored %v under a fingerprint that had moved", got)
	}
}

// Forgetting is not something to do by halves: the text is what a
// conversation is made of.
func TestForgettingEndsTheConversation(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	defer serving(t, b)()

	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "hello"))
	waitIMs(t, grid, 1)
	if len(d.chat.Conversations("example")) != 1 {
		t.Fatal("nothing was kept to forget")
	}

	got := send(t, d, b, "forget "+testSender.String())
	if !strings.Contains(got, "forgot the conversation") {
		t.Errorf("got %q", got)
	}
	if n := len(d.chat.Conversations("example")); n != 0 {
		t.Errorf("%d conversations left", n)
	}
}

func TestChatSaysWhatIsConfigured(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	if got := send(t, d, b, "chat"); !strings.Contains(got, "no model is configured") {
		t.Errorf("got %q", got)
	}

	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	got := send(t, d, b, "chat")
	for _, want := range []string{"model at", "small", "2 slots", "no backstory", "will talk to"} {
		if !strings.Contains(got, want) {
			t.Errorf("chat did not say %q:\n%s", want, got)
		}
	}
}

// The backstory is read every time, so working on a character is an
// edit rather than a restart.
func TestTheBackstoryIsReadWhenItIsUsed(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)

	path := t.TempDir() + "/hobb.txt"
	if err := writeFile(path, "You are Hobb, a dockhand."); err != nil {
		t.Fatal(err)
	}
	d.cfg.Backstory["example"] = path
	d.chat.cfg = d.cfg

	defer serving(t, b)()
	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "who are you?"))
	waitIMs(t, grid, 1)

	asks := f.sawAsks()
	msgs, _ := asks[0]["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	if !strings.Contains(first["content"].(string), "Hobb") {
		t.Errorf("the backstory did not reach the model: %v", first["content"])
	}

	// Rewrite it and the next conversation gets the new words, with no
	// restart anywhere.
	if err := writeFile(path, "You are Perrick, a bargee."); err != nil {
		t.Fatal(err)
	}
	other := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000007")
	grid.deliver(t, incoming(other, "Another Person", sl.DialogMessage, "who are you?"))
	waitIMs(t, grid, 2)

	asks = f.sawAsks()
	msgs, _ = asks[len(asks)-1]["messages"].([]any)
	first, _ = msgs[0].(map[string]any)
	if !strings.Contains(first["content"].(string), "Perrick") {
		t.Errorf("the edited backstory did not reach the model: %v", first["content"])
	}
}
