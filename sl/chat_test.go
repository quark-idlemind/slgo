package sl

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// newTestSession builds enough of a Session to exercise the subscription
// machinery, and runs the part of the reader goroutine that owns it.
//
// The rest of a Session needs a connection; none of this does.
func newTestSession(t *testing.T) (*Session, func()) {
	t.Helper()
	w := &Session{
		chatSubs: map[<-chan Line]*chatSub{},
		permSubs: map[<-chan *Permission]*permSub{},
		imSubs:   map[<-chan *IM]*imSub{},
		chatCtl:  make(chan chatCmd),
		readDone: make(chan struct{}),
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			close(w.readDone)
			w.closeChat()
		}()
		for {
			select {
			case c := <-w.chatCtl:
				w.applyChat(c)
			case <-stop:
				return
			}
		}
	}()
	return w, func() { close(stop); wg.Wait() }
}

func line(source msg.UUID, sourceType, chatType uint8, text string) Line {
	return Line{Source: source, SourceType: sourceType, Type: chatType, Text: text}
}

func TestChatFilter(t *testing.T) {
	a := msg.UUID{1}
	b := msg.UUID{2}

	cases := []struct {
		name string
		f    ChatFilter
		l    Line
		want bool
	}{
		{"zero hears everything", ChatFilter{},
			line(a, SourceObject, ChatSay, "x"), true},
		{"by source, match", ChatFilter{Source: a},
			line(a, SourceObject, ChatSay, "x"), true},
		{"by source, miss", ChatFilter{Source: a},
			line(b, SourceObject, ChatSay, "x"), false},
		{"by source type, match", ChatFilter{SourceTypes: []uint8{SourceObject}},
			line(a, SourceObject, ChatSay, "x"), true},
		{"by source type, miss", ChatFilter{SourceTypes: []uint8{SourceAgent}},
			line(a, SourceObject, ChatSay, "x"), false},
		{"debug only, match", ChatFilter{Types: []uint8{ChatDebug}},
			line(a, SourceObject, ChatDebug, "err"), true},
		{"debug only, miss", ChatFilter{Types: []uint8{ChatDebug}},
			line(a, SourceObject, ChatOwner, "hello"), false},
		{"source and type together", ChatFilter{Source: a, Types: []uint8{ChatDebug}},
			line(a, SourceObject, ChatSay, "x"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.f.match(c.l); got != c.want {
				t.Errorf("match = %v, want %v", got, c.want)
			}
		})
	}
}

// One line reaching several subscriptions is the case that makes this
// worth having: two callers watching for different things must not
// have to agree with each other about who consumes what.
func TestChatDeliversToEverySubscriber(t *testing.T) {
	w, stop := newTestSession(t)
	defer stop()

	obj := msg.UUID{9}
	all := w.Chat(ChatFilter{}, 4)
	debug := w.Chat(ChatFilter{Types: []uint8{ChatDebug}}, 4)
	other := w.Chat(ChatFilter{Source: msg.UUID{7}}, 4)

	w.deliver(line(obj, SourceObject, ChatDebug, "Math Error"))

	if got := (<-all).Text; got != "Math Error" {
		t.Errorf("the unfiltered subscription got %q", got)
	}
	if got := (<-debug).Text; got != "Math Error" {
		t.Errorf("the debug subscription got %q", got)
	}
	select {
	case l := <-other:
		t.Errorf("the subscription for a different source got %q", l.Text)
	default:
	}
}

// A full buffer must cost that subscriber lines and nobody else
// anything.  There is no way to ask the simulator to say it again, so
// the alternative to dropping is stalling the relay for everyone.
func TestChatDropsWhenFull(t *testing.T) {
	w, stop := newTestSession(t)
	defer stop()

	obj := msg.UUID{9}
	slow := w.Chat(ChatFilter{}, 2)
	fast := w.Chat(ChatFilter{}, 16)

	for i := range 10 {
		w.deliver(line(obj, SourceObject, ChatSay, string(rune('a'+i))))
	}

	if got := w.ChatDropped(slow); got != 8 {
		t.Errorf("dropped = %d, want 8", got)
	}
	if got := w.ChatDropped(fast); got != 0 {
		t.Errorf("the roomy subscription dropped %d, want 0", got)
	}
	if got := len(slow); got != 2 {
		t.Errorf("the slow buffer holds %d, want 2", got)
	}
	if got := len(fast); got != 10 {
		t.Errorf("the roomy buffer holds %d, want 10", got)
	}
}

// Stopping must close the channel, so a caller ranging over it stops
// ranging rather than blocking for ever.
func TestStopChatCloses(t *testing.T) {
	w, stop := newTestSession(t)
	defer stop()

	ch := w.Chat(ChatFilter{}, 4)
	w.deliver(line(msg.UUID{9}, SourceObject, ChatSay, "before"))
	w.StopChat(ch)

	var got []string
	for l := range ch {
		got = append(got, l.Text)
	}
	if len(got) != 1 || got[0] != "before" {
		t.Errorf("read %v, want [before]", got)
	}

	// Delivering after the stop must not panic on a closed channel.
	w.deliver(line(msg.UUID{9}, SourceObject, ChatSay, "after"))

	// Stopping twice is not an error.
	w.StopChat(ch)
}

// When the reader stops, every subscription closes: a caller waiting
// on one has to find out that nothing more is coming.
func TestChatClosesWhenReaderStops(t *testing.T) {
	w, stop := newTestSession(t)
	a := w.Chat(ChatFilter{}, 4)
	b := w.Chat(ChatFilter{}, 4)
	stop()

	for i, ch := range []<-chan Line{a, b} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("subscription %d had a line, want closed", i)
			}
		case <-time.After(time.Second):
			t.Errorf("subscription %d was not closed", i)
		}
	}

	// Subscribing after the reader has gone hands back something
	// already closed rather than something that stays empty.
	late := w.Chat(ChatFilter{}, 4)
	select {
	case _, ok := <-late:
		if ok {
			t.Error("a late subscription delivered a line")
		}
	case <-time.After(time.Second):
		t.Error("a late subscription was not closed")
	}
}

// TestSayRoutesNegativeChannels: a negative channel goes as a script
// dialog reply, because ChatFromViewer from this client does not carry
// one.  A positive channel still goes as chat.
func TestSayRoutesNegativeChannels(t *testing.T) {
	w, stop := newTestSession(t)
	defer stop()
	w.me = msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")

	sent := make(chan msg.Message, 4)
	w.sendFn = func(m msg.Message) error { sent <- m; return nil }

	if err := w.Say(nil, "on zero", 0); err != nil {
		t.Fatal(err)
	}
	if m, ok := (<-sent).(*msg.ChatFromViewer); !ok {
		t.Errorf("channel 0 went as %T", m)
	}
	if err := w.Say(nil, "on 42", 42); err != nil {
		t.Fatal(err)
	}
	if m, ok := (<-sent).(*msg.ChatFromViewer); !ok {
		t.Errorf("channel 42 went as %T", m)
	}

	if err := w.Say(nil, "on minus", -4242); err != nil {
		t.Fatal(err)
	}
	m, ok := (<-sent).(*msg.ScriptDialogReply)
	if !ok {
		t.Fatalf("a negative channel went as %T", m)
	}
	if m.Data.ChatChannel != -4242 {
		t.Errorf("channel = %d", m.Data.ChatChannel)
	}
	if trimNul(m.Data.ButtonLabel) != "on minus" {
		t.Errorf("text = %q", trimNul(m.Data.ButtonLabel))
	}
	// The id has to name something real, and this avatar certainly is.
	if m.Data.ObjectID != w.me {
		t.Errorf("object id = %s, want this avatar", m.Data.ObjectID)
	}
}

// TestSayNegativeLimits: the two things the dialog reply path cannot
// do, refused rather than quietly done wrong.
func TestSayNegativeLimits(t *testing.T) {
	w, stop := newTestSession(t)
	defer stop()
	w.sendFn = func(m msg.Message) error { return nil }

	// Volume cannot be carried.
	if err := w.SayAs(nil, "quietly", -1, ChatWhisper); err == nil {
		t.Error("whispering on a negative channel should be refused")
	}
	if err := w.SayAs(nil, "loudly", -1, ChatShout); err == nil {
		t.Error("shouting on a negative channel should be refused")
	}
	// But shouting on a positive one is fine.
	if err := w.SayAs(nil, "loudly", 1, ChatShout); err != nil {
		t.Errorf("shouting on a positive channel: %v", err)
	}

	// The label has a one byte length prefix, so there is a ceiling.
	long := strings.Repeat("x", maxDialogReply+1)
	err := w.SayAs(nil, long, -1, ChatSay)
	if err == nil {
		t.Fatal("a message past the limit should be refused")
	}
	if !strings.Contains(err.Error(), "254") {
		t.Errorf("the error should say the limit: %v", err)
	}
	if err := w.SayAs(nil, strings.Repeat("x", maxDialogReply), -1, ChatSay); err != nil {
		t.Errorf("a message at the limit should be sent: %v", err)
	}
}

// TestALineSaysWhoSpokeAndOnWhatChannel: a subscription may be hearing
// several sources at once and the channel is the only thing they arrive
// on, so everything that tells one utterance from another has to be on
// the line itself.
func TestALineSaysWhoSpokeAndOnWhatChannel(t *testing.T) {
	l := Line{From: "a prim", Text: "hello", SourceType: SourceObject, Type: ChatSay}
	if !l.FromObject() || l.FromAgent() || l.Debug() {
		t.Errorf("a line from an object reads as %+v", l)
	}
	if got := l.String(); got != "a prim: hello" {
		t.Errorf("String = %q", got)
	}

	// Open chat and the debug channel are the same message with a
	// different type, which is how a script's run-time errors arrive.
	l.SourceType, l.Type = SourceAgent, ChatDebug
	if l.FromObject() || !l.FromAgent() || !l.Debug() {
		t.Errorf("a debug line from an avatar reads as %+v", l)
	}
}

// TestEveryChatTypeHasAName: these are printed for a person to read, and
// a type nobody here has heard of still has to say something rather than
// come out blank.
func TestEveryChatTypeHasAName(t *testing.T) {
	for _, c := range []struct {
		t    uint8
		want string
	}{
		{ChatWhisper, "whisper"}, {ChatSay, "say"}, {ChatShout, "shout"},
		{ChatDebug, "debug"}, {ChatRegion, "region"}, {ChatOwner, "owner"},
		{ChatDirect, "direct"},
	} {
		if got := ChatTypeName(c.t); got != c.want {
			t.Errorf("ChatTypeName(%d) = %q, want %q", c.t, got, c.want)
		}
	}
	if got := ChatTypeName(0); got != "whisper" {
		t.Errorf("ChatTypeName(0) = %q", got)
	}
	// The two the switch does not know: one of them is zero, which the
	// digit loop would otherwise render as nothing at all.
	if got := ChatTypeName(42); got != "type 42" {
		t.Errorf("ChatTypeName(42) = %q", got)
	}
	if got := itoa(0); got != "0" {
		t.Errorf("itoa(0) = %q", got)
	}
}

// TestAFaultIsRecognisedFromItsHeaderAndNothingElse: the simulator
// reports a fatal run-time error as a header naming the script and then
// the reason on its own line.  Everything else on the debug channel --
// "Could not find texture" and its relatives -- is a complaint the
// script survives, and treating one as a fault would end a run that was
// still going.
func TestAFaultIsRecognisedFromItsHeaderAndNothingElse(t *testing.T) {
	name, ok := faultScript("Test HUD [script:slgo try divzero] Script run-time error")
	if !ok || name != "slgo try divzero" {
		t.Errorf("faultScript = %q, %v", name, ok)
	}
	for _, text := range []string{
		"Could not find texture",
		"Test HUD [script:unterminated Script run-time error",
		"Test HUD [script:a script] said something else",
	} {
		if name, ok := faultScript(text); ok {
			t.Errorf("faultScript(%q) took it for a fault by %q", text, name)
		}
	}
}

// TestAFaultPrintsWhatItKnows: the header arrives before the reason, so
// a fault has to be printable with only half of itself.
func TestAFaultPrintsWhatItKnows(t *testing.T) {
	f := &Fault{Script: "a script"}
	if got := f.String(); got != "a script: run-time error" {
		t.Errorf("String = %q", got)
	}
	f.Reason = "Stack-Heap Collision"
	if got := f.String(); got != "a script: Stack-Heap Collision" {
		t.Errorf("String = %q", got)
	}
}

// TestRunningOutOfMemoryIsToldApartFromEveryOtherFault: a benchmark
// searching for a size limit halves its script on this one and gives up
// on the rest, so a division by zero mistaken for it would halve for
// ever and a collision mistaken for a division by zero would end a
// benchmark that had just found its answer.
func TestRunningOutOfMemoryIsToldApartFromEveryOtherFault(t *testing.T) {
	if f := (&Fault{Script: "a script", Reason: "Stack-Heap Collision"}); !f.OutOfMemory() {
		t.Errorf("%v was not recognised as running out of memory", f)
	}
	for _, reason := range []string{"Math Error", "", "Heap Stack Collision"} {
		if f := (&Fault{Script: "a script", Reason: reason}); f.OutOfMemory() {
			t.Errorf("%q was taken for running out of memory", reason)
		}
	}
}

// TestACollectorHearsOnlyTheObjectItWasPointedAt: a region has other
// things talking in it, and a run whose transcript held them would
// report lines the script never said.
func TestACollectorHearsOnlyTheObjectItWasPointedAt(t *testing.T) {
	w, f := newFakeSession(t)
	col := &collector{source: thePrim, sentinel: "FINISHED", faultFor: "a script"}
	w.startCollector(col)

	f.Relay(t, objectSaid(theOther, ChatSay, "somebody else"))
	f.Relay(t, objectSaid(thePrim, ChatSay, "mine"))
	if got := col.collected(); len(got) != 1 || got[0].Text != "mine" {
		t.Errorf("the collector heard %v", got)
	}
	if col.faultSeen() != nil {
		t.Error("a collector that heard no fault reported one")
	}

	// The fault is two messages, and the reason is only taken from a
	// debug line that is not itself another header.
	f.Relay(t, objectSaid(thePrim, ChatDebug, "Test HUD [script:a script] Script run-time error"))
	<-col.faulted
	f.Relay(t, objectSaid(thePrim, ChatDebug, "Math Error"))
	<-col.reasoned
	if fault := col.faultSeen(); fault == nil || fault.Reason != "Math Error" {
		t.Errorf("fault = %+v", col.faultSeen())
	}

	f.Relay(t, objectSaid(thePrim, ChatSay, "FINISHED"))
	<-col.found

	// Stopped, it hears nothing more -- which is what makes a second run
	// in the same session a transcript of its own.
	w.stopCollector(col)
	before := len(col.collected())
	f.Relay(t, objectSaid(thePrim, ChatSay, "after the run"))
	if got := col.collected(); len(got) != before {
		t.Errorf("a stopped collector went on hearing: %v", got)
	}
}

// TestACollectorIgnoresAFaultInAnotherScript: an object may hold several
// scripts, and one of the others dying is not this run's business.
func TestACollectorIgnoresAFaultInAnotherScript(t *testing.T) {
	w, f := newFakeSession(t)
	col := &collector{source: thePrim, faultFor: "a script"}
	w.startCollector(col)
	defer w.stopCollector(col)

	f.Relay(t, objectSaid(thePrim, ChatDebug,
		"Test HUD [script:something else] Script run-time error"))
	if col.faultSeen() != nil {
		t.Error("a fault in another script was blamed on this one")
	}
	select {
	case <-col.faulted:
		t.Error("the run ended on another script's fault")
	default:
	}
}

// TestACollectorWithNoSentinelNeverFires: a run without one has nothing
// to wait for but the clock, and a collector that fired on any line at
// all would end it at the first thing said.
func TestACollectorWithNoSentinelNeverFires(t *testing.T) {
	w, f := newFakeSession(t)
	col := &collector{}
	w.startCollector(col)
	defer w.stopCollector(col)

	f.Relay(t, objectSaid(thePrim, ChatSay, "anything"))
	select {
	case <-col.found:
		t.Error("a collector with no sentinel said it had found one")
	default:
	}
	if got := col.collected(); len(got) != 1 {
		t.Errorf("a collector with a zero source heard %v, want everything", got)
	}
}

// TestChatCarriesEverythingTheMessageSaid: a line is built once, here,
// and anything dropped on the way is gone -- there is no asking the
// simulator what was said a moment ago.
func TestChatCarriesEverythingTheMessageSaid(t *testing.T) {
	w, f := newFakeSession(t)
	ch := w.Chat(ChatFilter{}, 4)

	m := objectSaid(thePrim, ChatShout, "over here")
	m.ChatData.Audible = 1
	m.ChatData.Position = msg.Vector3{X: 128, Y: 129, Z: 25}
	f.Relay(t, m)

	select {
	case l := <-ch:
		if l.Source != thePrim || l.Owner != testAgentID || l.From != "a prim" {
			t.Errorf("who said it came out as %+v", l)
		}
		if l.Type != ChatShout || l.SourceType != SourceObject || l.Audible != 1 {
			t.Errorf("how it was said came out as %+v", l)
		}
		if l.Position != (msg.Vector3{X: 128, Y: 129, Z: 25}) {
			t.Errorf("where it was said came out as %v", l.Position)
		}
		if l.At.IsZero() {
			t.Error("the line carries no arrival time")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was delivered to the subscription")
	}
}

// TestStoppingASubscriptionAfterTheReaderHasGoneStillCloses: the caller
// is owed the same effect either way, and one left ranging over a
// channel nobody will ever close is a hang rather than an error.
func TestStoppingASubscriptionAfterTheReaderHasGoneStillCloses(t *testing.T) {
	w, f := newFakeSession(t)
	ch := w.Chat(ChatFilter{}, 1)

	f.Close()
	select {
	case <-w.readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the reader did not stop")
	}

	// Already closed by the reader on its way out, so this is the path
	// where the work runs here instead of on a goroutine that is gone.
	w.StopChat(ch)
	if _, open := <-ch; open {
		t.Error("the subscription is still open after the session ended")
	}
	if n := w.ChatDropped(ch); n != 0 {
		t.Errorf("a subscription that no longer exists reported %d dropped", n)
	}
}

// TestChatDroppedIsZeroForSomethingNeverSubscribed: the answer is a
// count, so a channel nobody knows about has to read as none missed
// rather than as a failure the caller has to handle.
func TestChatDroppedIsZeroForSomethingNeverSubscribed(t *testing.T) {
	w, stop := newTestSession(t)
	defer stop()
	ch := make(chan Line)
	if n := w.ChatDropped((<-chan Line)(ch)); n != 0 {
		t.Errorf("ChatDropped = %d for a channel that was never subscribed", n)
	}
}
