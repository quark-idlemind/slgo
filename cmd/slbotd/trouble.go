package main

// What has gone wrong with an avatar, kept where somebody can ask it.
//
// The log file is the record and stays the record.  This is a tap on
// the same lines, held per avatar, for the one person who cannot read
// that file: whoever is standing in the virtual world holding a
// conversation with the thing.  An avatar that has been failing for
// fourteen hours looks exactly like one that has nothing to say, and
// from in-world there has been no way to tell the two apart.
//
// It is deliberately a method and not a filter over the log.  Deciding
// which lines are failures by matching their words is the mistake
// issues/006 already records, and chatf's own comment says what it
// costs: a filter for one shape of sentence silently swallowed another.
// So a failure is a call to errf, written as such at the site, and
// everything else is logf as before.
//
// In memory and bounded.  What it answers is "what has gone wrong with
// you lately", which is a question about the last few things rather
// than an audit; the log file has the rest and has it across restarts.

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// TroubleKeep is how many failures an avatar remembers.
//
// Enough to cover a bad spell without becoming something nobody reads.
// The oldest go first, because the newest are the ones still happening.
const TroubleKeep = 50

// Trouble is one thing that went wrong, and when.
type Trouble struct {
	At   time.Time
	Text string
}

// troubles is an avatar's recent failures.
//
// reported counts the ones an admin has already been told about, so
// that the same bad afternoon is not announced at the top of every
// conversation for the rest of the week.
type troubles struct {
	mu       sync.Mutex
	kept     []Trouble
	reported int
	dropped  int
}

// add records one.
func (t *troubles) add(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.kept = append(t.kept, Trouble{At: time.Now(), Text: text})
	for len(t.kept) > TroubleKeep {
		t.kept = t.kept[1:]
		t.dropped++
		if t.reported > 0 {
			t.reported--
		}
	}
}

// list is what is kept, oldest first.
func (t *troubles) list() []Trouble {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Trouble(nil), t.kept...)
}

// unreported is how many have turned up since anybody was last told.
func (t *troubles) unreported() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.kept) - t.reported
}

// noteReported marks everything kept as told-about, and says how many
// that was.
func (t *troubles) noteReported() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(t.kept) - t.reported
	t.reported = len(t.kept)
	return n
}

// clear forgets them.
func (t *troubles) clear() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(t.kept)
	t.kept, t.reported, t.dropped = nil, 0, 0
	return n
}

// errf logs a failure and keeps it.
//
// Everything errf writes goes to the log exactly as logf would, so the
// file still holds the whole story in order.  What it adds is that the
// line can be asked for from inside the world.
func (b *bot) errf(format string, args ...any) {
	b.trouble.add(fmt.Sprintf(format, args...))
	b.logf(format, args...)
}

// Troubles is what has gone wrong with this avatar lately.
func (b *bot) Troubles() []Trouble { return b.trouble.list() }

// troubleNotice is the one line an admin gets at the top of a fresh
// conversation, when there is something to say.
//
// One line and not the failures themselves, for three reasons.  An
// avatar is in character and a page of daemon diagnostics is not; the
// whole point of a backstory is undone by it.  When things are going
// badly -- which is exactly when this fires -- the list is long, and a
// conversation that opens with twenty lines is one nobody reads.  And
// the detail is one command away, so nothing is lost by making the
// asking deliberate.
//
// Empty when there is nothing to say, which is the ordinary case.
func troubleNotice(n int, prefix string) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return fmt.Sprintf("(something has gone wrong since we last spoke -- say %serrors)", prefix)
	default:
		return fmt.Sprintf("(%d things have gone wrong since we last spoke -- say %serrors)",
			n, prefix)
	}
}

// renderTroubles writes them out for somebody who asked.
func renderTroubles(list []Trouble, dropped int, now time.Time) string {
	if len(list) == 0 {
		return "nothing has gone wrong that I have kept."
	}
	var b strings.Builder
	if dropped > 0 {
		fmt.Fprintf(&b, "the last %d, and %d older ones dropped:\n", len(list), dropped)
	}
	for _, t := range list {
		fmt.Fprintf(&b, "%s ago: %s\n", ago(now.Sub(t.At)), t.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ago is a duration as somebody would say it.
//
// Coarse on purpose, and coarser the further back it goes: the useful
// question about a failure is whether it is happening now, happened
// this afternoon, or happened last week, and a figure to the second
// answers none of those any better than these words do.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < 2*time.Minute:
		return "a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 2*time.Hour:
		return "an hour"
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d < 48*time.Hour:
		return "a day"
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%d weeks", int(d.Hours()/(24*7)))
	}
}
