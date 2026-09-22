package sl

// A message this avatar sent, arriving back because another client of
// the same session sent it.
//
// The grid never sends these: slgod relays what one client puts on the
// circuit to the others, because two clients on one session otherwise
// each see everything the grid said and nothing the other did.

import (
	"testing"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
)

func TestAnEchoedIMSaysWhoItWasTo(t *testing.T) {
	w := &Session{me: msg.UUID{9}}
	them := msg.UUID{7}

	ims := make(chan *IM, 4)
	w.imSubs = map[<-chan *IM]*imSub{}
	sub := &imSub{ch: ims}
	w.imSubs[ims] = sub

	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID = w.me
	m.MessageBlock.ToAgentID = them
	m.MessageBlock.Dialog = DialogMessage
	m.MessageBlock.Message = []byte("three coils, Quark\x00")
	w.instantMessage(&client.Message{FromClient: "slbotd"}, m)

	select {
	case got := <-ims:
		if !got.Mine || got.Via != "slbotd" {
			t.Errorf("Mine=%v Via=%q", got.Mine, got.Via)
		}
		if got.To != them {
			t.Errorf("To = %s, want the other person", got.To)
		}
		if got.Text != "three coils, Quark" {
			t.Errorf("Text = %q", got.Text)
		}
		// The rule that protects everything written before any of this
		// existed: an echo is not somebody talking to us.
		if got.Conversation() {
			t.Error("an echo reads as somebody talking to this avatar")
		}
		if !got.Spoken() {
			t.Error("an echo does not read as conversation, so nothing would show it")
		}
	default:
		t.Fatal("the echo was not delivered")
	}
}

// Everything instantMessage does besides delivering is about a message
// that ARRIVED.  Learning from an echo would file our own name under
// whoever we wrote to.
func TestAnEchoTeachesNothingAboutWhoIsWho(t *testing.T) {
	w := &Session{me: msg.UUID{9}, names: map[msg.UUID]string{}}
	them := msg.UUID{7}

	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID = w.me
	m.MessageBlock.ToAgentID = them
	m.MessageBlock.Dialog = DialogMessage
	m.MessageBlock.FromAgentName = []byte("Example Resident\x00")
	m.MessageBlock.Message = []byte("hello\x00")
	w.instantMessage(&client.Message{FromClient: "slbotd"}, m)

	if got := w.Name(them); got != "" {
		t.Errorf("the echo taught the session that %s is called %q", them, got)
	}
	if got := w.Name(w.me); got != "" {
		t.Errorf("the echo learnt a name for this avatar: %q", got)
	}
}

// An offer this avatar MADE must not become an offer it is holding, or
// a session could accept a gift it sent somebody else.
func TestAnEchoedOfferIsNotAnOfferToAnswer(t *testing.T) {
	w := &Session{me: msg.UUID{9}}
	them := msg.UUID{7}

	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID = w.me
	m.MessageBlock.ToAgentID = them
	m.MessageBlock.Dialog = DialogInventoryOffered
	m.MessageBlock.ID = msg.UUID{4}
	m.MessageBlock.Message = []byte("a lamp\x00")
	m.MessageBlock.BinaryBucket = offerBucket(AssetObject, msg.UUID{3})
	w.instantMessage(&client.Message{FromClient: "slbotd"}, m)

	if n := len(w.InventoryOffers()); n != 0 {
		t.Errorf("kept %d offers this avatar made itself", n)
	}

	friend := &msg.ImprovedInstantMessage{}
	friend.AgentData.AgentID = w.me
	friend.MessageBlock.ToAgentID = them
	friend.MessageBlock.Dialog = DialogFriendshipOffered
	friend.MessageBlock.ID = msg.UUID{5}
	w.instantMessage(&client.Message{FromClient: "slbotd"}, friend)

	if n := len(w.Offers()); n != 0 {
		t.Errorf("kept %d friendship offers this avatar made itself", n)
	}
}

// A message with no FromClient came from the grid, which is everything
// that existed before any of this.
func TestAMessageFromTheGridIsNotAnEcho(t *testing.T) {
	w := &Session{me: msg.UUID{9}, names: map[msg.UUID]string{}}
	them := msg.UUID{7}

	ims := make(chan *IM, 4)
	w.imSubs = map[<-chan *IM]*imSub{ims: {ch: ims}}

	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID = them
	m.MessageBlock.ToAgentID = w.me
	m.MessageBlock.Dialog = DialogMessage
	m.MessageBlock.FromAgentName = []byte("Somebody Else\x00")
	m.MessageBlock.Message = []byte("evening\x00")
	w.instantMessage(&client.Message{}, m)

	got := <-ims
	if got.Mine || got.Via != "" {
		t.Errorf("Mine=%v Via=%q for a message from the grid", got.Mine, got.Via)
	}
	if !got.Conversation() {
		t.Error("a message from somebody else does not read as conversation")
	}
	if got.To != w.me {
		t.Errorf("To = %s, want this avatar", got.To)
	}
}

// ChatFromViewer is an OUTBOUND message, so one arriving is always an
// echo.  It is delivered as an ordinary line so that anything showing
// chat shows the whole of it.
func TestEchoedChatArrivesAsALine(t *testing.T) {
	w := &Session{
		me:       msg.UUID{9},
		chatSubs: map[<-chan Line]*chatSub{},
	}
	w.ident.Store(&Info{AvatarName: "Example Resident"})
	lines := make(chan Line, 4)
	w.chatSubs[lines] = &chatSub{ch: lines}

	m := &msg.ChatFromViewer{}
	m.ChatData.Message = []byte("evening all\x00")
	m.ChatData.Channel = 0
	m.ChatData.Type = ChatSay
	w.saidElsewhere(&client.Message{FromClient: "slbotd"}, m)

	select {
	case got := <-lines:
		if !got.Mine || got.Via != "slbotd" {
			t.Errorf("Mine=%v Via=%q", got.Mine, got.Via)
		}
		if got.Source != w.me || got.From != "Example Resident" {
			t.Errorf("said by %s (%q), want this avatar", got.Source, got.From)
		}
		if got.Text != "evening all" {
			t.Errorf("Text = %q", got.Text)
		}
	default:
		t.Fatal("the echoed line was not delivered")
	}
}

// One that did not come from a client did not come from anywhere the
// grid could have sent it, so it is dropped rather than guessed at.
func TestChatFromViewerWithNoSenderIsDropped(t *testing.T) {
	w := &Session{
		me:       msg.UUID{9},
		chatSubs: map[<-chan Line]*chatSub{},
	}
	w.ident.Store(&Info{AvatarName: "Example Resident"})
	lines := make(chan Line, 4)
	w.chatSubs[lines] = &chatSub{ch: lines}

	m := &msg.ChatFromViewer{}
	m.ChatData.Message = []byte("evening\x00")
	w.saidElsewhere(&client.Message{}, m)

	select {
	case got := <-lines:
		t.Fatalf("delivered %q for a message that came from nowhere", got.Text)
	default:
	}
}
