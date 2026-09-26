package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var (
	exampleGroupID = msg.MustParseUUID("41467e57-7e57-c0de-cfe9-0b291eec06eb")
	otherGroupID   = msg.MustParseUUID("a6947e57-7e57-c0de-c79d-c445397c313c")
	noticePoster   = msg.MustParseUUID("666e7e57-7e57-c0de-696d-85d008d2bb50")
)

// fakeClock is a time a test moves by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// noticeShell is a shell in Example Group, on a clock of its own.
func noticeShell(t *testing.T) (*testShell, *fakeClock) {
	t.Helper()
	x := newTestShell(t)
	x.grid.presence.Groups = []sl.Group{{ID: exampleGroupID, Name: "Example Group"}}
	clock := &fakeClock{t: time.Date(2026, 3, 4, 12, 3, 4, 0, time.Local)}
	x.notices.now = clock.Now
	return x, clock
}

// groupNotice is a notice as sl delivers it.
func groupNotice(group msg.UUID, text, item string) *sl.IM {
	b := []byte{0, 0}
	if item != "" {
		b = []byte{1, byte(sl.AssetNotecard)}
	}
	b = append(b, group[:]...)
	b = append(append(b, item...), 0)
	return &sl.IM{
		From: noticePoster, FromName: "Example Resident",
		Dialog: sl.DialogGroupNotice, Text: text, Bucket: b,
	}
}

// heardNow is what reached the terminal while one message was heard.
func (x *testShell) heardNow(m *sl.IM) string {
	x.out.Reset()
	x.heard(m)
	return x.out.String()
}

func TestANoticeArrivesAsOneNumberedLine(t *testing.T) {
	x, _ := noticeShell(t)
	got := x.heardNow(groupNotice(exampleGroupID, "Meeting moved|Friday at eight.\nBring a chair.", ""))
	want := "* notice 1 from Example Resident in Example Group: Meeting moved"
	if !strings.Contains(got, want) {
		t.Errorf("arrival printed %q, want %q", got, want)
	}
	if strings.Count(strings.TrimRight(got, "\n"), "\n") != 0 {
		t.Errorf("arrival printed more than one line: %q", got)
	}
	if strings.Contains(got, "Friday") {
		t.Errorf("the body was printed on arrival: %q", got)
	}
}

func TestANoticeSubjectIsKeptToOneLine(t *testing.T) {
	x, _ := noticeShell(t)
	subject := "First line\nsecond line\t" + strings.Repeat("x", 200)
	got := x.heardNow(groupNotice(exampleGroupID, subject+"|body", ""))
	if !strings.Contains(got, "First line second line xxx") || !strings.Contains(got, "...") {
		t.Errorf("the subject was not flattened and cut: %q", got)
	}
	if strings.Count(strings.TrimRight(got, "\n"), "\n") != 0 {
		t.Errorf("arrival printed more than one line: %q", got)
	}
}

func TestAGroupNotInTheListIsNamedByItsKey(t *testing.T) {
	x, _ := noticeShell(t)
	got := x.heardNow(groupNotice(otherGroupID, "S|B", ""))
	if !strings.Contains(got, "in group a6947e57: S") {
		t.Errorf("arrival printed %q", got)
	}
	full := x.do(t, "notice 1")
	if !strings.Contains(full, "group    "+otherGroupID.String()) {
		t.Errorf("notice 1 does not give the whole key:\n%s", full)
	}
}

func TestADuplicateNoticeIsNeitherPrintedNorNumbered(t *testing.T) {
	x, _ := noticeShell(t)
	x.heardNow(groupNotice(exampleGroupID, "Meeting moved|Friday.", ""))
	if got := x.heardNow(groupNotice(exampleGroupID, "Meeting moved|Friday.", "")); got != "" {
		t.Errorf("a duplicate printed %q", got)
	}
	// Another body, or another group, is another notice.
	if got := x.heardNow(groupNotice(exampleGroupID, "Meeting moved|Saturday.", "")); !strings.Contains(got, "notice 2 ") {
		t.Errorf("a different body printed %q", got)
	}
	if got := x.heardNow(groupNotice(otherGroupID, "Meeting moved|Friday.", "")); !strings.Contains(got, "notice 3 ") {
		t.Errorf("a different group printed %q", got)
	}
	if got := x.do(t, "notice"); strings.Count(got, "notice ") != 3 {
		t.Errorf("listing:\n%s", got)
	}
}

func TestANoticeIsForgottenAfterNoticeKeep(t *testing.T) {
	x, clock := noticeShell(t)
	x.cfg.NoticeKeep = 10 * time.Minute
	x.heardNow(groupNotice(exampleGroupID, "Old|one", ""))
	clock.Add(6 * time.Minute)
	x.heardNow(groupNotice(exampleGroupID, "New|one", ""))
	clock.Add(4 * time.Minute)

	got := x.do(t, "notice")
	if strings.Contains(got, "notice 1 ") || !strings.Contains(got, "notice 2 from Example Resident in Example Group: New") {
		t.Errorf("after ten minutes the listing is:\n%s", got)
	}
	if got := x.do(t, "notice 1"); !strings.Contains(got, "notice 1 has been forgotten: a notice is kept for 10m") {
		t.Errorf("notice 1 said %q", got)
	}

	// Forgotten, the same notice is news again, and takes a new number.
	if got := x.heardNow(groupNotice(exampleGroupID, "Old|one", "")); !strings.Contains(got, "notice 3 ") {
		t.Errorf("the notice arriving again printed %q", got)
	}

	clock.Add(time.Hour)
	if got := x.do(t, "notice"); !strings.Contains(got, "no group notices kept; each is kept for 10m") {
		t.Errorf("an empty listing said %q", got)
	}
}

func TestNoticeNPrintsItInFull(t *testing.T) {
	x, _ := noticeShell(t)
	x.heardNow(groupNotice(exampleGroupID, "Rules|Read the card.\nThen come in.", "Example Rules"))
	got := x.do(t, "notice 1")
	for _, want := range []string{
		"notice   1\n",
		"time     12:03:04\n",
		"from     Example Resident\n",
		"group    Example Group\n",
		"subject  Rules\n",
		"attached Example Rules, notecard\n",
		"\nRead the card.\nThen come in.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("notice 1 is missing %q:\n%s", want, got)
		}
	}
}

func TestNoticeRefusesWhatItDoesNotHave(t *testing.T) {
	x, _ := noticeShell(t)
	if got := x.do(t, "notice 1"); !strings.Contains(got, "there is no notice 1; none has arrived") {
		t.Errorf("before any: %q", got)
	}
	x.heardNow(groupNotice(exampleGroupID, "S|B", ""))
	if got := x.do(t, "notice 5"); !strings.Contains(got, "there is no notice 5; the last to arrive was 1") {
		t.Errorf("past the last: %q", got)
	}
	if got := x.do(t, "notice 0"); !strings.Contains(got, "there is no notice 0") {
		t.Errorf("zero: %q", got)
	}
	if got := x.do(t, "notice first"); !strings.Contains(got, `"first" is not a number`) {
		t.Errorf("a word: %q", got)
	}
}

func TestNoticeNumbersAreNeverReused(t *testing.T) {
	x, clock := noticeShell(t)
	for i, text := range []string{"A|1", "B|2", "C|3"} {
		x.heardNow(groupNotice(exampleGroupID, text, ""))
		clock.Add(time.Hour) // each forgotten before the next
		if got := x.do(t, "notice"); !strings.Contains(got, "no group notices kept") {
			t.Fatalf("after %d: %q", i+1, got)
		}
	}
	if got := x.heardNow(groupNotice(exampleGroupID, "D|4", "")); !strings.Contains(got, "notice 4 ") {
		t.Errorf("after three forgotten the next is %q", got)
	}
	if got := x.do(t, "notice 2"); !strings.Contains(got, "notice 2 has been forgotten") {
		t.Errorf("notice 2 said %q", got)
	}
}

func TestNoticeKeepIsASetting(t *testing.T) {
	c := DefaultConfig()
	if c.NoticeKeep != 15*time.Minute {
		t.Errorf("default notice_keep is %s", c.NoticeKeep)
	}
	s, ok := findSetting("notice_keep")
	if !ok {
		t.Fatal("no notice_keep setting")
	}
	if got := s.show(&c); got != "15m" {
		t.Errorf("shown as %q", got)
	}
	if err := s.parse(&c, "1h"); err != nil || c.NoticeKeep != time.Hour {
		t.Errorf("1h: %v, %s", err, c.NoticeKeep)
	}
	for _, bad := range []string{"soon", "0", "-5m"} {
		if err := s.parse(&c, bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}

// TestNoticesArriveWhileTheyAreRead: heard runs on the watch goroutine
// and notice on the command one.  Run with -race.
func TestNoticesArriveWhileTheyAreRead(t *testing.T) {
	t.Setenv("SLSH_CONFIG_DIR", t.TempDir())
	x, _ := noticeShell(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			x.heard(groupNotice(exampleGroupID, "S|"+strings.Repeat("b", i), ""))
		}
	}()
	for i := 0; i < 50; i++ {
		x.Do(t.Context(), "notice")
		x.Do(t.Context(), "set notice_keep 20m")
	}
	<-done
	if got := x.do(t, "notice 50"); !strings.Contains(got, "notice   50") {
		t.Errorf("notice 50 said %q", got)
	}
}
