package world

import (
	"sync"
	"testing"
	"time"

	"slgo/msg"
)

// newTestWorld builds enough of a World to exercise the subscription
// machinery, and runs the part of the reader goroutine that owns it.
//
// The rest of a World needs a connection; none of this does.
func newTestWorld(t *testing.T) (*World, func()) {
	t.Helper()
	w := &World{
		chatSubs: map[<-chan Line]*chatSub{},
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
	w, stop := newTestWorld(t)
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
	w, stop := newTestWorld(t)
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
	w, stop := newTestWorld(t)
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
	w, stop := newTestWorld(t)
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
