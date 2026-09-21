package main

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// The ring keeps the newest and drops the oldest, and says how many it
// dropped rather than pretending they were never there.
func TestTroublesKeepTheNewestAndSayWhatWentOver(t *testing.T) {
	var tr troubles
	for i := 0; i < TroubleKeep+5; i++ {
		tr.add("problem")
	}
	if got := len(tr.list()); got != TroubleKeep {
		t.Errorf("kept %d, want %d", got, TroubleKeep)
	}
	if tr.dropped != 5 {
		t.Errorf("dropped %d, want 5", tr.dropped)
	}
}

// Telling somebody marks what they were told, so the same bad
// afternoon is not announced for the rest of the week.
func TestTroublesAreOnlyReportedOnce(t *testing.T) {
	var tr troubles
	tr.add("one")
	tr.add("two")
	if got := tr.unreported(); got != 2 {
		t.Fatalf("unreported %d, want 2", got)
	}
	if got := tr.noteReported(); got != 2 {
		t.Errorf("reported %d, want 2", got)
	}
	if got := tr.unreported(); got != 0 {
		t.Errorf("unreported %d after reporting, want 0", got)
	}

	// And something new after that is new again.
	tr.add("three")
	if got := tr.unreported(); got != 1 {
		t.Errorf("unreported %d after a new one, want 1", got)
	}
	if n := tr.clear(); n != 3 {
		t.Errorf("cleared %d, want 3", n)
	}
	if got := tr.unreported(); got != 0 {
		t.Errorf("unreported %d after clearing, want 0", got)
	}
}

// The notice is one line and names the command, whatever the prefix
// that daemon was given.
func TestTheNoticeIsOneLineAndNamesTheCommand(t *testing.T) {
	if got := troubleNotice(0, ":"); got != "" {
		t.Errorf("a notice with nothing to say: %q", got)
	}
	one := troubleNotice(1, ":")
	if !strings.Contains(one, "something has gone wrong") || !strings.Contains(one, ":errors") {
		t.Errorf("one: %q", one)
	}
	many := troubleNotice(4, "!")
	if !strings.Contains(many, "4 things") || !strings.Contains(many, "!errors") {
		t.Errorf("many: %q", many)
	}
	for _, s := range []string{one, many} {
		if strings.Contains(s, "\n") {
			t.Errorf("the notice is more than one line: %q", s)
		}
	}
}

func TestAgoIsCoarserTheFurtherBack(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{90 * time.Second, "a minute"},
		{20 * time.Minute, "20 minutes"},
		{90 * time.Minute, "an hour"},
		{5 * time.Hour, "5 hours"},
		{30 * time.Hour, "a day"},
		{72 * time.Hour, "3 days"},
		{21 * 24 * time.Hour, "3 weeks"},
	} {
		if got := ago(c.d); got != c.want {
			t.Errorf("ago(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

// A trusted person opening a conversation is told, in one line, and is
// not told again the next time they speak.
func TestAnAdminIsToldOnceWhatHasGoneWrong(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	d.cfg.ErrorGap = time.Hour

	b.errf("could not send the answer to somebody: the sky fell in")
	b.errf("could not keep the context: no room")

	defer serving(t, b)()
	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "hello there"))
	sent := waitIMs(t, grid, 1)

	if !strings.Contains(string(sent[0].MessageBlock.Message), "2 things have gone wrong") {
		t.Errorf("the notice did not go: %q", string(sent[0].MessageBlock.Message))
	}

	// The next remark, straight away, is not a fresh approach.
	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "and again"))
	quiet(grid)
	for _, m := range grid.IMsSent()[1:] {
		if strings.Contains(string(m.MessageBlock.Message), "gone wrong") {
			t.Errorf("told twice in one conversation: %q", string(m.MessageBlock.Message))
		}
	}
}

// Somebody who is not trusted is told nothing: it is a report about
// the daemon, and an avatar reciting its faults to a stranger is out
// of character as well as none of their business.
func TestAStrangerIsNotToldWhatHasGoneWrong(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	d.cfg.ErrorGap = time.Hour
	b.errf("could not do the thing")

	defer serving(t, b)()
	stranger := msg.MustParseUUID("f8247e57-7e57-c0de-9845-94dc3b58a2ac")
	grid.deliver(t, incoming(stranger, "Passing Stranger", sl.DialogMessage, "hello"))
	quiet(grid)
	for _, m := range grid.IMsSent() {
		if strings.Contains(string(m.MessageBlock.Message), "gone wrong") {
			t.Errorf("a stranger was told: %q", string(m.MessageBlock.Message))
		}
	}
	if b.trouble.unreported() != 1 {
		t.Error("a stranger's message marked the trouble reported")
	}
}

// errf both logs and keeps, so the file still has the whole story in
// order and the avatar can still be asked.
func TestErrfLogsAndKeeps(t *testing.T) {
	_, b, _ := newTestDaemon(t)
	b.errf("could not %s", "cope")

	list := b.Troubles()
	if len(list) != 1 || !strings.Contains(list[0].Text, "could not cope") {
		t.Fatalf("not kept: %v", list)
	}
	if list[0].At.IsZero() {
		t.Error("kept without a time")
	}
}

// Nothing to report reads as an answer rather than as an empty list.
func TestRenderingNothingSaysSo(t *testing.T) {
	if got := renderTroubles(nil, 0, time.Now()); !strings.Contains(got, "nothing has gone wrong") {
		t.Errorf("got %q", got)
	}
	now := time.Now()
	got := renderTroubles([]Trouble{{At: now.Add(-3 * time.Minute), Text: "the sky fell in"}}, 2, now)
	for _, want := range []string{"3 minutes ago", "the sky fell in", "2 older ones dropped"} {
		if !strings.Contains(got, want) {
			t.Errorf("got\n%s\nwant something with %q in it", got, want)
		}
	}
}
