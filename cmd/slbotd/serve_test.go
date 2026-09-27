package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// What arrives at an avatar and what is done about it.  This is the
// gate: everything else in this package is reached through it, and a
// fault here is a daemon obeying somebody it was never told to obey.

// incoming builds the message the grid sends when somebody says
// something to this avatar.
func incoming(from msg.UUID, name string, dialog uint8, text string) *msg.ImprovedInstantMessage {
	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID = from
	m.MessageBlock.ToAgentID = testMe
	m.MessageBlock.Dialog = dialog
	m.MessageBlock.FromAgentName = append([]byte(name), 0)
	m.MessageBlock.Message = append([]byte(text), 0)
	m.MessageBlock.ID = msg.MustParseUUID("43de7e57-7e57-c0de-69ba-51c767497346")
	return m
}

// offering builds an inventory offer, which is the same message with a
// binary bucket naming what is on offer.
func offering(from msg.UUID, name, what string, transaction msg.UUID) *msg.ImprovedInstantMessage {
	m := incoming(from, name, sl.DialogInventoryOffered, what)
	m.MessageBlock.ID = transaction
	bucket := make([]byte, 17)
	bucket[0] = byte(sl.AssetNotecard)
	copy(bucket[1:], testLamp[:])
	m.MessageBlock.BinaryBucket = bucket
	return m
}

// serving runs the listening loop until the returned function is
// called.
func serving(t *testing.T, b *bot) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	// Subscribed here rather than inside the goroutine, so that a
	// message delivered by the test after this returns is one the loop
	// is certain to see.  serve does the same thing for the same
	// reason; see its comment.
	s := b.Session()
	ims := s.IMs(IMDepth)
	go func() {
		defer close(done)
		defer s.StopIMs(ims)
		b.read(ctx, s, ims, nil)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the listening loop would not stop")
		}
	}
}

// waitIMs waits for at least n instant messages to have been sent.
func waitIMs(t *testing.T, f *fakeGrid, n int) []*msg.ImprovedInstantMessage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ims := f.IMsSent()
		if len(ims) >= n {
			return ims
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d messages and %d were sent", n, len(ims))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// quiet gives anything that was going to happen time to happen, for the
// tests whose point is that nothing does.
func quiet(f *fakeGrid) { time.Sleep(300 * time.Millisecond) }

func TestATrustedAvatarIsObeyed(t *testing.T) {
	_, b, f := newTestDaemon(t)
	defer serving(t, b)()

	f.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, ":where"))

	ims := waitIMs(t, f, 1)
	if got := string(ims[0].MessageBlock.Message); !strings.Contains(got, "Nowhere at 128") {
		t.Errorf("answered %q", got)
	}
	if ims[0].MessageBlock.ToAgentID != testSender {
		t.Errorf("answered %s", ims[0].MessageBlock.ToAgentID)
	}
}

// The name in the message is enough, because that is how a
// configuration written in names has to work.
func TestTrustByNameIsObeyed(t *testing.T) {
	_, b, f := newTestDaemon(t)
	defer serving(t, b)()

	// A different id, and the trusted name.
	f.deliver(t, incoming(testStranger, "Trusted Resident", sl.DialogMessage, ":where"))
	ims := waitIMs(t, f, 1)
	if got := string(ims[0].MessageBlock.Message); !strings.Contains(got, "Nowhere") {
		t.Errorf("answered %q", got)
	}
}

// A script's message carries its owner's id and whatever name the
// object was given, so an object named after somebody trusted, or one
// a trusted person owns, passes Trusts by name or by id.  It is
// ignored entirely: no command, no model, no report of trouble.
func TestAnObjectIsNotObeyed(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	f := newFakeLLM(t)
	withChat(t, d, f, Anyone)
	logged := &logBook{}
	d.logf = logged.logf
	d.cfg.ErrorGap = time.Hour
	b.errf("could not do the thing")
	defer serving(t, b)()

	object := msg.MustParseUUID("6dc67e57-7e57-c0de-132d-c92f933df25c")
	for _, owner := range []msg.UUID{testSender, testStranger} {
		for _, text := range []string{":where", "hello there"} {
			m := incoming(owner, "Trusted Resident", sl.DialogFromTask, text)
			m.MessageBlock.ID = object
			grid.deliver(t, m)
		}
	}
	quiet(grid)

	if ims := grid.IMsSent(); len(ims) != 0 {
		t.Errorf("an object was answered: %q", string(ims[0].MessageBlock.Message))
	}
	if asks := f.sawAsks(); len(asks) != 0 {
		t.Errorf("the model was asked %d times about an object", len(asks))
	}
	if b.trouble.unreported() != 1 {
		t.Error("an object's message marked the trouble reported")
	}
	if n := logged.count(`ignored an instant message from [Object] Trusted Resident, owned by (`); n != 4 {
		t.Errorf("logged %d of 4 objects' messages as ignored:\n%s", n, logged)
	}

	// The person, on the loop that ignored the object: told about the
	// trouble, since the object was not them speaking, and obeyed.
	grid.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, ":where"))
	ims := waitIMs(t, grid, 2)
	var told, answered bool
	for _, m := range ims {
		told = told || strings.Contains(string(m.MessageBlock.Message), "gone wrong")
		answered = answered || strings.Contains(string(m.MessageBlock.Message), "Nowhere at 128")
	}
	if !told || !answered {
		t.Errorf("told %v, answered %v", told, answered)
	}
}

// TestNothingButAPersonIsLoggedAsOne: whatever dialog an instant message
// carries, one sent by an object, a group or the grid under a person's
// name is logged with the name labelled as what sent it.
// Why: doc/im-senders.md#labelling-a-sender
func TestNothingButAPersonIsLoggedAsOne(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	logged := &logBook{}
	d.logf = logged.logf
	s := b.Session()
	const name = "Trusted Resident"

	var jobs sync.WaitGroup
	defer jobs.Wait()
	checked := 0
	for dialog := 0; dialog < 256; dialog++ {
		for _, how := range []struct {
			what string
			set  func(*sl.IM)
		}{
			{"as it came", func(*sl.IM) {}},
			{"as a group", func(m *sl.IM) { m.Group = true }},
			{"with no sender", func(m *sl.IM) { m.From = msg.UUID{} }},
		} {
			m := &sl.IM{From: testSender, FromName: name, Dialog: uint8(dialog), Text: "a remark"}
			how.set(m)
			k := m.Sender()
			if k == sl.SenderPerson {
				continue
			}
			logged.mu.Lock()
			from := len(logged.lines)
			logged.mu.Unlock()
			b.arrived(context.Background(), s, m, &jobs)
			logged.mu.Lock()
			lines := append([]string(nil), logged.lines[from:]...)
			logged.mu.Unlock()
			for _, line := range lines {
				if !strings.Contains(line, name) {
					continue
				}
				checked++
				if !strings.Contains(line, k.Label(name)) {
					t.Errorf("dialog %d, %s: logged %q, which does not say it is not a person",
						dialog, how.what, line)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("nothing labelled was logged, so this checked nothing")
	}
	for _, want := range []string{
		"object inventory offer from [Object] Trusted Resident: a remark",
		"object alert from [Object] Trusted Resident: a remark",
		"ignored an instant message from [Object] Trusted Resident, owned by (",
		"ignored an instant message from [Object] Trusted Resident, owned by [Group] (",
	} {
		if logged.count(want) == 0 {
			t.Errorf("nothing logged %q:\n%s", want, logged)
		}
	}
}

// logBook keeps what a daemon logs, for a test about what it said.
type logBook struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBook) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logBook) count(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, s) {
			n++
		}
	}
	return n
}

func (l *logBook) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// Somebody who is not trusted is not answered at all by default.  An
// answer would tell a stranger that there is something here to try
// commands against, and there is nothing useful for them to be told.
func TestAStrangerIsNotAnswered(t *testing.T) {
	_, b, f := newTestDaemon(t)
	defer serving(t, b)()

	f.deliver(t, incoming(testStranger, "Some Body", sl.DialogMessage, ":where"))
	quiet(f)
	if ims := f.IMsSent(); len(ims) != 0 {
		t.Errorf("a stranger was answered: %q", string(ims[0].MessageBlock.Message))
	}
}

func TestAStrangerIsAnsweredWhenTheFileSaysSo(t *testing.T) {
	d, b, f := newTestDaemon(t)
	d.cfg.AnswerStrangers = true
	defer serving(t, b)()

	f.deliver(t, incoming(testStranger, "Some Body", sl.DialogMessage, ":where"))
	ims := waitIMs(t, f, 1)
	if got := string(ims[0].MessageBlock.Message); !strings.Contains(got, "trust") {
		t.Errorf("answered %q", got)
	}
	if strings.Contains(string(ims[0].MessageBlock.Message), "Nowhere") {
		t.Error("a stranger was told where the avatar is")
	}
}

// A remark is a remark.  A daemon with no model that answered one would
// be holding a conversation it cannot hold.
func TestARemarkIsNotACommand(t *testing.T) {
	_, b, f := newTestDaemon(t)
	defer serving(t, b)()

	f.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogMessage, "where are you?"))
	quiet(f)
	if ims := f.IMsSent(); len(ims) != 0 {
		t.Errorf("a remark was answered: %q", string(ims[0].MessageBlock.Message))
	}
}

// Two of these arrive for every remark anybody types, and answering one
// would be a message sent for every keystroke in the region.
func TestTypingIsIgnored(t *testing.T) {
	_, b, f := newTestDaemon(t)
	defer serving(t, b)()

	f.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogTypingStart, ""))
	f.deliver(t, incoming(testSender, "Trusted Resident", sl.DialogTypingStop, ""))
	quiet(f)
	if ims := f.IMsSent(); len(ims) != 0 {
		t.Errorf("typing was answered: %q", string(ims[0].MessageBlock.Message))
	}
}

// An offer from a trusted avatar is taken without anybody being at a
// keyboard, which is the point of the setting.  The acceptance has to
// quote the offer's own transaction: the simulator matches on nothing
// else, and an answer with a fresh id leaves the offer open for ever.
func TestAnOfferFromATrustedAvatarIsAccepted(t *testing.T) {
	_, b, f := newTestDaemon(t)
	defer serving(t, b)()

	transaction := msg.MustParseUUID("9fdc7e57-7e57-c0de-37aa-b7364eeb890d")
	f.deliver(t, offering(testSender, "Trusted Resident", "a notecard", transaction))

	ims := waitIMs(t, f, 1)
	if ims[0].MessageBlock.Dialog != sl.DialogInventoryAccepted {
		t.Fatalf("dialog = %d, want an acceptance", ims[0].MessageBlock.Dialog)
	}
	if ims[0].MessageBlock.ID != transaction {
		t.Errorf("quoted %s, want the offer's own %s", ims[0].MessageBlock.ID, transaction)
	}
	if ims[0].MessageBlock.ToAgentID != testSender {
		t.Errorf("answered %s", ims[0].MessageBlock.ToAgentID)
	}
}

// An offer from somebody else is left exactly where it is.  Declining
// is a statement, and this daemon has nothing to say to a stranger
// about a gift it was not watching for; leaving it waiting means a
// person can still answer it from a viewer.
func TestAnOfferFromAStrangerIsLeftWaiting(t *testing.T) {
	_, b, f := newTestDaemon(t)
	defer serving(t, b)()

	transaction := msg.MustParseUUID("a0c97e57-7e57-c0de-9306-44ca640a431b")
	f.deliver(t, offering(testStranger, "Some Body", "a notecard", transaction))
	quiet(f)

	if ims := f.IMsSent(); len(ims) != 0 {
		t.Fatalf("a stranger's offer was answered: dialog %d", ims[0].MessageBlock.Dialog)
	}
	if got := len(b.Session().InventoryOffers()); got != 1 {
		t.Errorf("%d offers waiting, want the one nobody answered", got)
	}
}

func TestAnyoneCanOfferWhenTheFileSaysSo(t *testing.T) {
	d, b, f := newTestDaemon(t)
	d.cfg.AcceptInventory = AcceptAnyone
	defer serving(t, b)()

	transaction := msg.MustParseUUID("a14d7e57-7e57-c0de-24b0-36f087ef8a3b")
	f.deliver(t, offering(testStranger, "Some Body", "a notecard", transaction))
	ims := waitIMs(t, f, 1)
	if ims[0].MessageBlock.Dialog != sl.DialogInventoryAccepted {
		t.Errorf("dialog = %d, want an acceptance", ims[0].MessageBlock.Dialog)
	}
}

func TestNobodyCanOfferWhenTheFileSaysSo(t *testing.T) {
	d, b, f := newTestDaemon(t)
	d.cfg.AcceptInventory = AcceptNobody
	defer serving(t, b)()

	transaction := msg.MustParseUUID("a1577e57-7e57-c0de-f80d-6766245e67a0")
	f.deliver(t, offering(testSender, "Trusted Resident", "a notecard", transaction))
	quiet(f)
	if ims := f.IMsSent(); len(ims) != 0 {
		t.Errorf("an offer was answered when nobody's are taken: dialog %d", ims[0].MessageBlock.Dialog)
	}
}

// A sender who has filled this avatar is told so at once rather than
// queued silently behind a benchmark nobody remembers starting.
func TestAFullAvatarSaysSoRatherThanQueueing(t *testing.T) {
	_, b, _ := newTestDaemon(t)
	f := b.Session().Backend().(*fakeGrid)

	// Take every slot, as running commands would.
	for i := 0; i < cap(b.jobs); i++ {
		b.jobs <- struct{}{}
	}
	var jobs sync.WaitGroup
	b.arrived(context.Background(), b.Session(), &sl.IM{
		From: testSender, FromName: "Trusted Resident",
		Dialog: sl.DialogMessage, Text: ":where",
	}, &jobs)
	jobs.Wait()

	ims := waitIMs(t, f, 1)
	if got := string(ims[0].MessageBlock.Message); !strings.Contains(got, "already running") {
		t.Errorf("answered %q", got)
	}
}

// A command that succeeds and prints nothing still has to answer.
// Silence from a daemon is indistinguishable from a daemon that never
// heard.
func TestSilenceIsAnsweredWithOk(t *testing.T) {
	d, b, f := newTestDaemon(t)
	commands["nothing"] = &command{
		flags: func() any { return new(helpOnly) },
		brief: "print nothing at all",
		group: groupDaemon,
		run: func(ctx context.Context, r *req, out io.Writer, args []string) error {
			return nil
		},
	}
	t.Cleanup(func() { delete(commands, "nothing") })

	_ = d
	b.obey(context.Background(), b.Session(), &sl.IM{
		From: testSender, FromName: "Trusted Resident",
		Dialog: sl.DialogMessage, Text: ":nothing",
	}, "nothing")

	ims := waitIMs(t, f, 1)
	if got := string(ims[0].MessageBlock.Message); !strings.HasPrefix(got, "ok") {
		t.Errorf("answered %q", got)
	}
}
