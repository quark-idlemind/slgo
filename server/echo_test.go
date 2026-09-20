package server

// What one client says reaching the others.
//
// The grid does not echo what an avatar says -- a viewer shows your own
// remarks because it composed them -- so two clients on one session saw
// everything the grid sent and nothing the other said.  Somebody
// watching a conversation through slsh while a daemon answered for the
// same avatar saw every reply and none of the questions.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
)

// saying puts a remark on the circuit as a client would.
func saying(text string) *msg.ChatFromViewer {
	m := &msg.ChatFromViewer{}
	m.ChatData.Message = append([]byte(text), 0)
	m.ChatData.Type = 1
	return m
}

func TestWhatOneClientSaysReachesTheOthers(t *testing.T) {
	r := newRig(t, nil)
	speaker := r.dial(t, "ChatFromViewer")
	defer speaker.Close()
	watcher := r.dial(t, "ChatFromViewer")
	defer watcher.Close()
	time.Sleep(100 * time.Millisecond) // let both attachments settle

	if err := speaker.Send(context.Background(), saying("three coils, Quark"), true); err != nil {
		t.Fatal(err)
	}

	m := waitMsg(t, watcher, "ChatFromViewer", 5*time.Second)
	if m.FromClient == "" {
		t.Error("the echo does not say which client sent it")
	}
	got, err := m.Decode()
	if err != nil {
		t.Fatal(err)
	}
	chat, ok := got.(*msg.ChatFromViewer)
	if !ok {
		t.Fatalf("decoded as %T", got)
	}
	if text := trimNulTest(chat.ChatData.Message); text != "three coils, Quark" {
		t.Errorf("said %q", text)
	}
}

// Never back to the sender.  It composed the message and has already
// shown it; sending it again would have every client that writes its
// own remarks print each of them twice.
func TestASpeakerIsNotToldWhatItSaid(t *testing.T) {
	r := newRig(t, nil)
	speaker := r.dial(t, "ChatFromViewer")
	defer speaker.Close()
	other := r.dial(t, "ChatFromViewer")
	defer other.Close()
	time.Sleep(100 * time.Millisecond)

	if err := speaker.Send(context.Background(), saying("hello"), true); err != nil {
		t.Fatal(err)
	}
	// The other one gets it, which is how we know the echo happened at
	// all rather than the test simply waiting out a timeout.
	waitMsg(t, other, "ChatFromViewer", 5*time.Second)

	select {
	case m, ok := <-speaker.Messages():
		if ok {
			t.Fatalf("the sender was told what it said: %s", m.Name)
		}
	case <-time.After(300 * time.Millisecond):
	}
}

// The same subscriptions as the grid's own traffic, and no other rule.
// A client that asked for nothing gets nothing, whoever said it.
func TestAnEchoObeysSubscriptions(t *testing.T) {
	r := newRig(t, nil)
	speaker := r.dial(t, "ChatFromViewer")
	defer speaker.Close()
	deaf := r.dial(t) // subscribed to nothing
	defer deaf.Close()
	listening := r.dial(t, "ChatFromViewer")
	defer listening.Close()
	time.Sleep(100 * time.Millisecond)

	if err := speaker.Send(context.Background(), saying("hello"), true); err != nil {
		t.Fatal(err)
	}
	waitMsg(t, listening, "ChatFromViewer", 5*time.Second)

	select {
	case m, ok := <-deaf.Messages():
		if ok {
			t.Fatalf("relayed %s to a client that asked for nothing", m.Name)
		}
	case <-time.After(300 * time.Millisecond):
	}
}

// An echo is not a datagram and does not pretend to be one.  A made-up
// sequence number would be a client's only way of telling these apart
// quietly going wrong.
func TestAnEchoCarriesNoSequence(t *testing.T) {
	r := newRig(t, nil)
	speaker := r.dial(t, "ChatFromViewer")
	defer speaker.Close()
	watcher := r.dial(t, "ChatFromViewer")
	defer watcher.Close()
	time.Sleep(100 * time.Millisecond)

	if err := speaker.Send(context.Background(), saying("hello"), true); err != nil {
		t.Fatal(err)
	}
	m := waitMsg(t, watcher, "ChatFromViewer", 5*time.Second)
	if m.Sequence != 0 || m.Flags != 0 {
		t.Errorf("sequence %d flags %d, want neither claimed", m.Sequence, m.Flags)
	}
	if m.At.IsZero() {
		t.Error("an echo has no time on it")
	}
}

// trimNulTest is the test's own, so that this file does not reach into
// what the package trims strings with.
func trimNulTest(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// The refusal says which program, which copy of it, and where it is --
// so somebody reading it knows where to go and look.
func TestARefusalNamesTheClientThatIsInTheWay(t *testing.T) {
	r := authRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// No dial options, so this is the real handshake -- which is what
	// carries the name and the pid.
	c, err := client.Dial(ctx, r.ln.Addr().String())
	if err != nil {
		t.Fatalf("dialling an authenticating daemon: %v", err)
	}
	defer c.Close()
	if _, err := c.Attach(ctx, "example"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	h, _ := r.srv.Agent("example")
	waitFor(t, 5*time.Second, "the client to attach", func() bool {
		return h.ClientCount() == 1
	})

	who := h.clientNames()
	if len(who) != 1 {
		t.Fatalf("clientNames = %v", who)
	}
	name := who[0]
	if !strings.Contains(name, "[") || !strings.Contains(name, "]@") {
		t.Errorf("client is named %q, want name[pid]@address", name)
	}
	if !strings.Contains(name, "127.0.0.1") {
		t.Errorf("client is named %q, want the address it dialled from", name)
	}
}

// Each piece is left out when it is not known, so an old client that
// sends no pid still reads as a sentence rather than as a gap.
func TestAClientIsDescribedByWhateverIsKnown(t *testing.T) {
	for _, tc := range []struct {
		name         string
		pid          int32
		remote, want string
	}{
		{"slsh", 1234, "198.51.100.7", "slsh[1234]@198.51.100.7"},
		{"slsh", 0, "198.51.100.7", "slsh@198.51.100.7"},
		{"slsh", 1234, "", "slsh[1234]"},
		{"slsh", 0, "", "slsh"},
		{"", 0, "198.51.100.7", "an unnamed client at 198.51.100.7"},
		{"", 0, "", "an unnamed client"},
	} {
		if got := describeClient(tc.name, tc.pid, tc.remote); got != tc.want {
			t.Errorf("describeClient(%q, %d, %q) = %q, want %q",
				tc.name, tc.pid, tc.remote, got, tc.want)
		}
	}
}
