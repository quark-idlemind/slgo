package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The clock is the tell.  A model answers in a second or two whatever
// it was asked, and an avatar that replies before the question has
// finished arriving is not a person however well it writes.

func chatterWith(set func(*Config)) *Chatter {
	cfg := DefaultConfig()
	if set != nil {
		set(&cfg)
	}
	return &Chatter{cfg: cfg}
}

// The worked example from the specification: a 46 character remark read
// at 23 characters a second is two seconds, and a 32 character answer
// typed at 3.2 is ten more.
func TestThePaceIsTheArithmeticItSays(t *testing.T) {
	c := chatterWith(nil)
	p := c.pace("example", strings.Repeat("x", 46), strings.Repeat("y", 32))

	if got, want := p.Read, 2*time.Second; got != want {
		t.Errorf("reading 46 characters at 23.0 cps took %s, want %s", got, want)
	}
	if got, want := p.Type, 10*time.Second; got != want {
		t.Errorf("typing 32 characters at 3.2 cps took %s, want %s", got, want)
	}
	// The far end sees nothing for two seconds, then typing for ten.
	if p.Before() != 2*time.Second || p.Total() != 12*time.Second {
		t.Errorf("before=%s total=%s, want 2s and 12s", p.Before(), p.Total())
	}
}

func TestTheDefaultSpeedsAreTheOnesWrittenDown(t *testing.T) {
	c := DefaultConfig()
	if s := c.SpeedsFor("anybody"); s.Read != 23.0 || s.Type != 3.2 {
		t.Errorf("read=%v type=%v, want 23.0 and 3.2", s.Read, s.Type)
	}
}

// Avatars are not all the same person: one may type with two fingers
// and another answer the instant they have read it.
func TestAnAvatarCanHaveItsOwnSpeed(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AvatarRead["quick"] = 50
	cfg.AvatarType["quick"] = 8

	if s := cfg.SpeedsFor("quick"); s.Read != 50 || s.Type != 8 {
		t.Errorf("the avatar's own speeds were not used: %+v", s)
	}
	if s := cfg.SpeedsFor("somebody-else"); s.Read != 23.0 || s.Type != 3.2 {
		t.Errorf("another avatar did not get the default: %+v", s)
	}

	// One half overridden leaves the other at the default, or an
	// avatar given a typing speed would silently read at nothing.
	cfg.AvatarRead["half"] = 40
	if s := cfg.SpeedsFor("half"); s.Read != 40 || s.Type != 3.2 {
		t.Errorf("half an override took the other half with it: %+v", s)
	}
}

func TestSpeedsAreReadFromTheFile(t *testing.T) {
	c := parse(t, `
avatar  = example
avatar  = builder
trusted = A B
chat    = *
llm-url = http://127.0.0.1:8080

read-cps = 30
type-cps = 4.5
read-cps = builder 12
type-cps = builder 1.5
`)
	if c.ReadCPS != 30 || c.TypeCPS != 4.5 {
		t.Errorf("defaults read=%v type=%v", c.ReadCPS, c.TypeCPS)
	}
	if s := c.SpeedsFor("builder"); s.Read != 12 || s.Type != 1.5 {
		t.Errorf("builder got %+v", s)
	}
	if s := c.SpeedsFor("example"); s.Read != 30 || s.Type != 4.5 {
		t.Errorf("example got %+v, want the file's defaults", s)
	}
}

func TestABadSpeedLineIsRefused(t *testing.T) {
	for _, text := range []string{
		"read-cps = quickly\n",
		"type-cps = qi quickly\n",
		"read-cps = -5\n",
		"type-cps = a b c\n",
	} {
		if _, err := parseConfig(strings.NewReader(text)); err == nil {
			t.Errorf("accepted %q", strings.TrimSpace(text))
		}
	}
}

func TestALongerAnswerTakesLongerToWrite(t *testing.T) {
	c := chatterWith(nil)
	short := c.pace("example", "hi", "aye")
	long := c.pace("example", "hi", strings.Repeat("x", 200))
	if long.Type <= short.Type {
		t.Errorf("typing %s for a long answer and %s for a short one", long.Type, short.Type)
	}
	if long.Read != short.Read {
		t.Errorf("reading changed with the ANSWER's length: %s then %s", short.Read, long.Read)
	}
}

// Off by default, because the arithmetic is the point.  It is here for
// an operator who would rather not have an avatar typing for minutes.
func TestTheCeilingIsOffUntilItIsAskedFor(t *testing.T) {
	if DefaultConfig().PaceMax != 0 {
		t.Error("something is capping the wait by default")
	}
	c := chatterWith(func(cfg *Config) { cfg.PaceMax = 20 * time.Second })
	p := c.pace("example", "hi", strings.Repeat("x", 4000))
	if p.Total() > 20*time.Second {
		t.Errorf("total %s past a ceiling of 20s", p.Total())
	}
	if p.Read == 0 {
		t.Error("the reading was thrown away to make room, rather than the typing")
	}
}

func TestThePaceCanBeTurnedOff(t *testing.T) {
	c := chatterWith(func(cfg *Config) { cfg.ReadCPS, cfg.TypeCPS = 0, 0 })
	if p := c.pace("example", "a remark", "an answer"); p.Total() != 0 {
		t.Errorf("total %s with both speeds off", p.Total())
	}
}

// The model's own seconds count towards the wait: a reply that took
// longer than a person would have goes out at once.
func TestTheModelsOwnTimeCountsTowardsTheWait(t *testing.T) {
	_, b, _ := newTestDaemon(t)
	p := Pace{Read: 200 * time.Millisecond, Type: 200 * time.Millisecond}

	started := time.Now()
	b.wait(context.Background(), b.Session(), testSender, time.Now().Add(-time.Hour), p)
	if took := time.Since(started); took > 200*time.Millisecond {
		t.Errorf("waited %s for a reply that was already overdue", took)
	}
}

func TestAQuickAnswerIsHeldBackAndLooksLikeTyping(t *testing.T) {
	_, b, grid := newTestDaemon(t)
	p := Pace{Read: 150 * time.Millisecond, Type: 200 * time.Millisecond}

	started := time.Now()
	b.wait(context.Background(), b.Session(), testSender, time.Now(), p)
	if took := time.Since(started); took < 300*time.Millisecond {
		t.Errorf("waited only %s, want about %s", took, p.Total())
	}

	starts, stops := grid.typingSent()
	if starts != 1 || stops != 1 {
		t.Errorf("%d typing starts and %d stops, want one of each", starts, stops)
	}
}

// A daemon being shut down does not sit out the wait, and the far end
// is not left showing "typing..." for ever.
func TestShuttingDownCutsTheWaitShort(t *testing.T) {
	_, b, grid := newTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	p := Pace{Type: 30 * time.Second}

	started := time.Now()
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	b.wait(ctx, b.Session(), testSender, time.Now(), p)
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("waited %s after being cancelled", took)
	}
	if _, stops := grid.typingSent(); stops != 1 {
		t.Errorf("%d typing stops, want the far end told it had stopped anyway", stops)
	}
}
