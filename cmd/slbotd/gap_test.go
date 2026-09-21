package main

import (
	"strings"
	"testing"
	"time"
)

// A pause is a pause only above the floor, and a conversation nobody
// has said anything in has not paused -- it has not started.
func TestPausedIsAFloorAndNotAMemory(t *testing.T) {
	now := time.Now()
	c := &Conversation{}
	if _, ok := c.Paused(time.Hour, now); ok {
		t.Error("an empty conversation reported a pause")
	}

	c.Add("user", "hello", now.Add(-30*time.Minute))
	if _, ok := c.Paused(time.Hour, now); ok {
		t.Error("half an hour reported as a pause with an hour floor")
	}

	c.Spoke = now.Add(-3 * 24 * time.Hour)
	d, ok := c.Paused(time.Hour, now)
	if !ok {
		t.Fatal("three days was not a pause")
	}
	if d < 71*time.Hour {
		t.Errorf("pause of %s, want about three days", d)
	}

	// Nothing about the record moved.
	if len(c.Turns) != 1 {
		t.Errorf("Paused changed the turns: %d", len(c.Turns))
	}

	// A floor of zero is the feature switched off.
	if _, ok := c.Paused(0, now); ok {
		t.Error("a zero floor still reported a pause")
	}
}

// The wording has to survive both shapes ago() produces: "an hour" and
// "3 days" cannot share a verb.
func TestTheGapReadsAsASentence(t *testing.T) {
	for _, d := range []time.Duration{
		90 * time.Minute, 5 * time.Hour, 30 * time.Hour, 72 * time.Hour, 21 * 24 * time.Hour,
	} {
		got := Gap(d)
		if !strings.HasPrefix(got, "(It has been ") || !strings.HasSuffix(got, ".)") {
			t.Errorf("Gap(%s) = %q", d, got)
		}
	}
	if got := Gap(72 * time.Hour); got != "(It has been 3 days since they last wrote to you.)" {
		t.Errorf("got %q", got)
	}
}

// Medium carries the instruction, because the hint does nothing
// without it -- measured, see the constant.
func TestMediumTellsItToRemarkOnAPause(t *testing.T) {
	if !strings.Contains(Medium, "remark on it") {
		t.Errorf("Medium does not ask for the pause to be noticed: %q", Medium)
	}
}

// The hint is SENT and not STORED: the model sees it on the remark,
// the record holds what the person actually wrote, and the summariser
// is given the record.
func TestThePauseIsSentButNotRemembered(t *testing.T) {
	d, _, _ := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	d.cfg.ChatGap = time.Hour
	d.chat.cfg = d.cfg

	conv := d.chat.Store().Load("example", someone, someoneName)
	conv.Add("user", "Is the north berth free?", time.Now().Add(-3*24*time.Hour))
	conv.Add("assistant", "It is, for now.", time.Now().Add(-3*24*time.Hour))
	if err := d.chat.Store().Save(conv); err != nil {
		t.Fatal(err)
	}

	if _, err := d.chat.Reply(t.Context(), "example", someone, someoneName, "Hello again."); err != nil {
		t.Fatal(err)
	}

	asks := f.sawAsks()
	msgs, _ := asks[len(asks)-1]["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	sent, _ := last["content"].(string)
	if !strings.Contains(sent, "It has been 3 days") {
		t.Errorf("the pause was not sent: %q", sent)
	}
	if !strings.Contains(sent, "Hello again.") {
		t.Errorf("the remark did not survive the hint: %q", sent)
	}

	// And the record is clean.
	again := d.chat.Store().Load("example", someone, someoneName)
	for _, turn := range again.Turns {
		if strings.Contains(turn.Text, "It has been") {
			t.Errorf("the hint was stored as something said: %q", turn.Text)
		}
	}
	if last := again.Turns[len(again.Turns)-2]; last.Text != "Hello again." {
		t.Errorf("the stored remark is %q", last.Text)
	}
}

// Under the floor, nothing is added at all.
func TestAShortPauseSaysNothing(t *testing.T) {
	d, _, _ := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	d.cfg.ChatGap = time.Hour
	d.chat.cfg = d.cfg

	conv := d.chat.Store().Load("example", someone, someoneName)
	conv.Add("user", "still here?", time.Now().Add(-2*time.Minute))
	conv.Add("assistant", "aye", time.Now().Add(-2*time.Minute))
	if err := d.chat.Store().Save(conv); err != nil {
		t.Fatal(err)
	}

	if _, err := d.chat.Reply(t.Context(), "example", someone, someoneName, "Hello again."); err != nil {
		t.Fatal(err)
	}
	asks := f.sawAsks()
	msgs, _ := asks[len(asks)-1]["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if sent, _ := last["content"].(string); sent != "Hello again." {
		t.Errorf("a short pause added something: %q", sent)
	}
}
