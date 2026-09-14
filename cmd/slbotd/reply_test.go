package main

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/quark-idlemind/slgo/sl"
)

// An instant message holds about a kilobyte and what goes over that is
// cut off by the simulator without a word.  So the cutting is done
// here, where it can be done at a sensible place and where what did not
// fit can be reported.

func TestAShortAnswerIsOneMessage(t *testing.T) {
	got := splitReply("Nowhere at 128, 64, 25", IMBytes)
	if len(got) != 1 || got[0] != "Nowhere at 128, 64, 25" {
		t.Errorf("got %q", got)
	}
}

func TestNothingToSayIsNoMessages(t *testing.T) {
	for _, text := range []string{"", "   ", "\n\n", " \t\n"} {
		if got := splitReply(text, IMBytes); len(got) != 0 {
			t.Errorf("splitReply(%q) = %q, want nothing", text, got)
		}
	}
}

// A listing is cut at a line ending, because a listing cut in the
// middle of a line is a line somebody will read as a whole one.
func TestALongAnswerIsCutAtLineEndings(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("a line of about thirty bytes\n")
	}
	parts := splitReply(b.String(), 100)
	if len(parts) < 8 {
		t.Fatalf("%d parts, wanted several", len(parts))
	}
	for i, p := range parts {
		if len(p) > 100 {
			t.Errorf("part %d is %d bytes", i, len(p))
		}
		for _, line := range strings.Split(p, "\n") {
			if line != "a line of about thirty bytes" {
				t.Fatalf("part %d holds a broken line %q", i, line)
			}
		}
	}
}

// One long line with no line endings in it -- a listing of ids, or a
// script -- is cut at a space instead.
func TestALongLineIsCutAtASpace(t *testing.T) {
	text := strings.TrimSpace(strings.Repeat("word ", 100))
	parts := splitReply(text, 40)
	for i, p := range parts {
		if len(p) > 40 {
			t.Errorf("part %d is %d bytes", i, len(p))
		}
		if strings.HasPrefix(p, " ") || strings.HasSuffix(p, " ") {
			t.Errorf("part %d has loose whitespace: %q", i, p)
		}
	}
	if joined := strings.Join(parts, " "); joined != text {
		t.Errorf("the pieces do not add back up:\n%q", joined)
	}
}

// The worst case is a run of bytes with nowhere sensible to cut, and
// the one thing that must not happen there is a cut inside a rune: half
// a rune arrives in somebody's chat window as a replacement character.
func TestACutIsNeverInsideARune(t *testing.T) {
	text := strings.Repeat("日本語", 200) // three bytes to the rune
	parts := splitReply(text, 100)
	if len(parts) < 10 {
		t.Fatalf("%d parts, wanted several", len(parts))
	}
	for i, p := range parts {
		if len(p) > 100 {
			t.Errorf("part %d is %d bytes", i, len(p))
		}
		if !utf8.ValidString(p) {
			t.Errorf("part %d is not valid UTF-8: %q", i, p)
		}
	}
	if strings.Join(parts, "") != text {
		t.Error("the pieces do not add back up")
	}
}

// A limit smaller than one rune would otherwise be a loop that sends
// nothing for ever.
func TestARuneLongerThanTheLimitIsStillSent(t *testing.T) {
	parts := splitReply("日本", 2)
	if len(parts) != 2 || parts[0] != "日" || parts[1] != "本" {
		t.Errorf("got %q", parts)
	}
}

// What is cut off is said.  An answer that simply stopped would be
// indistinguishable from an answer that had finished.
func TestWhatDidNotFitIsSaid(t *testing.T) {
	d, b, f := newTestDaemon(t)
	d.cfg.ReplyLimit = 2
	s := b.Session()

	var long strings.Builder
	for i := 0; i < 200; i++ {
		long.WriteString("a line of about thirty bytes\n")
	}
	if err := sendReply(context.Background(), s, testSender, long.String(), d.cfg.ReplyLimit); err != nil {
		t.Fatalf("sendReply: %v", err)
	}

	ims := f.IMsSent()
	if len(ims) != 3 {
		t.Fatalf("%d messages sent, want the limit of 2 and one saying so", len(ims))
	}
	last := string(ims[2].MessageBlock.Message)
	if !strings.Contains(last, "there was more") {
		t.Errorf("the last message does not say what was lost: %q", last)
	}
	for i, im := range ims {
		if n := len(im.MessageBlock.Message); n > IMBytes+1 {
			t.Errorf("message %d is %d bytes", i, n)
		}
	}
}

// The session works out the conversation id the way a viewer does, so
// that both ends agree without being told.  A reply that invented one
// would land in a conversation the far end thinks is new.
func TestAReplyGoesToWhoAsked(t *testing.T) {
	_, b, f := newTestDaemon(t)
	s := b.Session()
	if err := sendReply(context.Background(), s, testSender, "ok", 4); err != nil {
		t.Fatal(err)
	}
	ims := f.IMsSent()
	if len(ims) != 1 {
		t.Fatalf("%d messages sent", len(ims))
	}
	if ims[0].MessageBlock.ToAgentID != testSender {
		t.Errorf("sent to %s, want %s", ims[0].MessageBlock.ToAgentID, testSender)
	}
	if ims[0].MessageBlock.Dialog != sl.DialogMessage {
		t.Errorf("dialog = %d, want a plain message", ims[0].MessageBlock.Dialog)
	}
}

// A listing is indented, and a piece that began at a line but had its
// indent trimmed away starts with one row out of line with the rest.
// This is what a help listing running to three messages looked like
// before the trimming was narrowed to line endings.
func TestTheIndentSurvivesACut(t *testing.T) {
	var b strings.Builder
	b.WriteString("looking\n")
	for i := 0; i < 20; i++ {
		b.WriteString("  where      the region and position this avatar is at\n")
	}
	parts := splitReply(b.String(), 200)
	if len(parts) < 3 {
		t.Fatalf("%d parts, wanted several", len(parts))
	}
	for i, p := range parts[1:] {
		if !strings.HasPrefix(p, "  where") {
			t.Errorf("part %d lost its indent: %q", i+1, firstLine(p))
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
