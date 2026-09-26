package main

// Group notices.  Each is announced in one line as it arrives, kept for
// notice_keep, and read in full with "notice N".
//
//	notice      the notices kept, one line each
//	notice N    one of them in full
//
// A notice repeating one still kept -- the same group, subject and body
// -- is dropped without a word.  Numbers climb for the whole session and
// are never handed out twice, so a number on the screen names that
// notice or nothing.  The group is named from a cache of this avatar's
// group list, when shown rather than when heard.  The bucket layout,
// and why the cache: doc/group-notices.md

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var noticeCommands = map[string]*command{
	"notice": {
		params:   "[N]",
		flags:    func() any { return new(helpOnly) },
		brief:    "the group notices heard lately; with N, one of them in full",
		keywords: "group notice notices announcement announcements subject body attachment read recent heard",
		man:      "notice",
		run:      cmdNotice,
	},
}

// noticeDefaultKeep is notice_keep when nothing sets it.
const noticeDefaultKeep = 15 * time.Minute

// noticeSubjectMax is how many characters of a subject the one-line
// form shows.
const noticeSubjectMax = 72

// keptNotice is one notice with its number and when it arrived here.
// The group is kept as its key and named when shown, so a notice
// announced by key lists by name once the name is known.
type keptNotice struct {
	n    int
	at   time.Time
	from string
	*sl.GroupNotice
}

// noticeBoard is the notices kept, in arrival order.  heard adds on the
// watch goroutine and commands read, so everything is under mu.
type noticeBoard struct {
	mu   sync.Mutex
	now  func() time.Time // nil is time.Now
	last int              // the last number handed out
	kept []*keptNotice
}

func (b *noticeBoard) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// pruneLocked forgets whatever has been kept for keep or longer.
func (b *noticeBoard) pruneLocked(keep time.Duration) {
	now := b.clock()
	left := b.kept[:0]
	for _, k := range b.kept {
		if now.Sub(k.at) < keep {
			left = append(left, k)
		}
	}
	clear(b.kept[len(left):])
	b.kept = left
}

// add keeps a notice and numbers it, unless it repeats one still kept,
// when it returns false and nothing changes.
func (b *noticeBoard) add(n *sl.GroupNotice, from string, keep time.Duration) (*keptNotice, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(keep)
	for _, k := range b.kept {
		if k.Group == n.Group && k.Subject == n.Subject && k.Body == n.Body {
			return nil, false
		}
	}
	b.last++
	k := &keptNotice{n: b.last, at: b.clock(), from: from, GroupNotice: n}
	b.kept = append(b.kept, k)
	return k, true
}

// list is what is kept now, oldest first.
func (b *noticeBoard) list(keep time.Duration) []*keptNotice {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(keep)
	return append([]*keptNotice(nil), b.kept...)
}

// get is notice n, or why there is none: never handed out, or forgotten.
func (b *noticeBoard) get(n int, keep time.Duration) (*keptNotice, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(keep)
	for _, k := range b.kept {
		if k.n == n {
			return k, nil
		}
	}
	switch {
	case n >= 1 && n <= b.last:
		return nil, fmt.Errorf("notice %d has been forgotten: a notice is kept for %s", n, durationWord(keep))
	case b.last == 0:
		return nil, fmt.Errorf("there is no notice %d; none has arrived", n)
	}
	return nil, fmt.Errorf("there is no notice %d; the last to arrive was %d", n, b.last)
}

// groupWord is the group as a line names it, given its name or "".
func (k *keptNotice) groupWord(name string) string {
	switch {
	case name != "":
		return name
	case k.Group.IsZero():
		return "a group it did not name"
	}
	return "group " + k.Group.String()[:8]
}

// line is the notice in one line, without the time.
func (k *keptNotice) line(group string) string {
	return fmt.Sprintf("notice %d from %s in %s: %s", k.n, k.from, k.groupWord(group), oneLine(k.Subject, noticeSubjectMax))
}

// oneLine is s with its line breaks and tabs made spaces, cut to max
// characters.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max-3]) + "..."
	}
	return s
}

// noticeKeep is notice_keep, read under mu since heard runs off the
// command goroutine and set writes cfg.
func (sh *Shell) noticeKeep() time.Duration {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.cfg.NoticeKeep <= 0 {
		return noticeDefaultKeep
	}
	return sh.cfg.NoticeKeep
}

// heardNotice keeps a notice that has arrived and announces it, or
// drops it if it repeats one still kept.
//
// It runs on the goroutine that delivers every IM, so it names the
// group only from what is cached, and a miss asks for the list in the
// background rather than waiting for it.
// Why: doc/group-notices.md#naming-the-group
func (sh *Shell) heardNotice(m *sl.IM) {
	n, ok := sl.GroupNoticeFrom(m)
	if !ok {
		return
	}
	k, fresh := sh.notices.add(n, sh.noticeFrom(n), sh.noticeKeep())
	if !fresh {
		return
	}
	name, known := sh.groups.name(n.Group)
	if !known {
		sh.groups.kick()
	}
	sh.noticef("%s", k.line(name))
}

// noticeFrom is who posted it.  From is not asked for a name when it
// is the group's id.
func (sh *Shell) noticeFrom(n *sl.GroupNotice) string {
	if n.FromName != "" {
		return n.FromName
	}
	if !n.From.IsZero() && n.From != n.Group {
		if name := sh.s.NameOr(n.From); name != "" {
			return name
		}
	}
	return "somebody"
}

// groupLookupTimeout is how long one ask for the group list may take.
const groupLookupTimeout = 2 * time.Second

// groupRefreshEvery is the least time between two refreshes a notice
// arriving starts.  A command's own refresh is not counted.
const groupRefreshEvery = 30 * time.Second

// groupNameCache is what this avatar's own list of groups calls each group
// it is in, as last asked, so that heard can name a notice's group
// without waiting on the network.  Everything is under mu.
type groupNameCache struct {
	mu     sync.Mutex
	names  map[msg.UUID]string // nil until a refresh has succeeded
	now    func() time.Time    // nil is time.Now
	fetch  func(context.Context) ([]sl.Group, error)
	flight chan struct{} // closed when the refresh in flight ends; nil if none
	kicked time.Time     // when kick last started one
}

// name is what the list calls id, and whether it has id at all.  The
// zero key is no group, and never a miss.
func (g *groupNameCache) name(id msg.UUID) (string, bool) {
	if id.IsZero() {
		return "", true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	name, ok := g.names[id]
	return name, ok
}

// kick starts a refresh in the background, unless one is in flight or
// kick started one less than groupRefreshEvery ago.  It never waits.
func (g *groupNameCache) kick() {
	g.mu.Lock()
	now := time.Now()
	if g.now != nil {
		now = g.now()
	}
	if g.flight != nil || (!g.kicked.IsZero() && now.Sub(g.kicked) < groupRefreshEvery) {
		g.mu.Unlock()
		return
	}
	g.kicked = now
	done := make(chan struct{})
	g.flight = done
	g.mu.Unlock()
	go g.run(context.Background(), done)
}

// refresh asks for the list and waits for the answer, joining a
// refresh already in flight rather than starting a second.
func (g *groupNameCache) refresh(ctx context.Context) {
	g.mu.Lock()
	done := g.flight
	if done == nil {
		done = make(chan struct{})
		g.flight = done
		g.mu.Unlock()
		g.run(ctx, done)
		return
	}
	g.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// run is one refresh, which done belongs to.  A list that could not be
// had leaves the cache as it was.
func (g *groupNameCache) run(ctx context.Context, done chan struct{}) {
	ctx, cancel := context.WithTimeout(ctx, groupLookupTimeout)
	defer cancel()
	groups, err := g.fetch(ctx)
	g.mu.Lock()
	if err == nil {
		g.names = make(map[msg.UUID]string, len(groups))
		for _, gr := range groups {
			g.names[gr.ID] = gr.Name
		}
	}
	g.flight = nil
	g.mu.Unlock()
	close(done)
}

// groupsOf is the group list as a session gives it.
func groupsOf(s *sl.Session) func(context.Context) ([]sl.Group, error) {
	return func(ctx context.Context) ([]sl.Group, error) {
		p, err := s.Where(ctx)
		if err != nil {
			return nil, err
		}
		return p.Groups, nil
	}
}

// namesFor names the groups of kept, refreshing the cache first if any
// of them is missing from it.  It may wait, so commands only.
func (sh *Shell) namesFor(ctx context.Context, kept ...*keptNotice) map[msg.UUID]string {
	lookup := func() (map[msg.UUID]string, bool) {
		names := map[msg.UUID]string{}
		all := true
		for _, k := range kept {
			name, ok := sh.groups.name(k.Group)
			names[k.Group] = name
			all = all && ok
		}
		return names, all
	}
	names, all := lookup()
	if all {
		return names
	}
	sh.groups.refresh(ctx)
	names, _ = lookup()
	return names
}

func cmdNotice(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("notice", &o, out, args)
	if err != nil || done {
		return err
	}
	keep := sh.noticeKeep()
	switch len(args) {
	case 0:
		kept := sh.notices.list(keep)
		if len(kept) == 0 {
			fmt.Fprintf(out, "no group notices kept; each is kept for %s after it arrives\n", durationWord(keep))
		}
		names := sh.namesFor(ctx, kept...)
		for _, k := range kept {
			fmt.Fprintf(out, "%s  %s\n", k.at.Local().Format("15:04:05"), k.line(names[k.Group]))
		}
		return nil
	case 1:
	default:
		return usageError("notice")
	}

	n, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("%q is not a number; notice lists them", args[0])
	}
	k, err := sh.notices.get(n, keep)
	if err != nil {
		return err
	}
	name := sh.namesFor(ctx, k)[k.Group]
	group := k.groupWord(name)
	if name == "" && !k.Group.IsZero() {
		group = k.Group.String()
	}
	fmt.Fprintf(out, "notice   %d\n", k.n)
	fmt.Fprintf(out, "time     %s\n", k.at.Local().Format("15:04:05"))
	fmt.Fprintf(out, "from     %s\n", k.from)
	fmt.Fprintf(out, "group    %s\n", group)
	fmt.Fprintf(out, "subject  %s\n", k.Subject)
	if k.HasAttachment {
		name := k.Attachment
		if name == "" {
			name = "(no name)"
		}
		fmt.Fprintf(out, "attached %s, %s\n", name, k.Asset)
	}
	if k.Body != "" {
		fmt.Fprintf(out, "\n%s\n", strings.TrimRight(k.Body, "\n"))
	}
	return nil
}

// durationWord is a duration without its zero tail: "15m", not "15m0s".
func durationWord(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
