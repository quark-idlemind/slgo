package agent

import (
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

func im(text string) *msg.ImprovedInstantMessage {
	m := &msg.ImprovedInstantMessage{}
	m.MessageBlock.FromAgentName = []byte("Kerra Yule\x00")
	m.MessageBlock.Message = append([]byte(text), 0)
	return m
}

// TestAnOfferSurvivesUntilAViewerCanShowIt: these are said once and
// addressed to the person, and a viewer is the only thing that can
// answer them.
func TestAnOfferSurvivesUntilAViewerCanShowIt(t *testing.T) {
	var o Offers
	at := time.Now()
	o.note(im("come and see the house"), at)

	got := o.Take()
	if len(got) != 1 {
		t.Fatalf("took %d offers, want 1", len(got))
	}
	m, ok := got[0].(*msg.ImprovedInstantMessage)
	if !ok {
		t.Fatalf("took a %T, want an instant message", got[0])
	}
	if string(m.MessageBlock.Message) != "come and see the house\x00" {
		t.Errorf("message = %q", m.MessageBlock.Message)
	}
	if string(m.MessageBlock.FromAgentName) != "Kerra Yule\x00" {
		t.Errorf("from = %q", m.MessageBlock.FromAgentName)
	}
}

// TestTakingEmpties: they are shown to a person, and the next viewer to
// attach should not be handed an invitation somebody already answered.
func TestTakingEmpties(t *testing.T) {
	var o Offers
	o.note(im("hello"), time.Now())
	o.Take()
	if got := o.Take(); len(got) != 0 {
		t.Errorf("took %d offers a second time, want none", len(got))
	}
}

// TestStaleOffersAreNotShown: a viewer started an hour later should not
// be handed a pile of invitations the simulator has already timed out.
func TestStaleOffersAreNotShown(t *testing.T) {
	var o Offers
	now := time.Now()
	o.note(im("stale"), now.Add(-offerHold-time.Minute))
	o.note(im("fresh"), now.Add(-time.Minute))

	got := o.take(now)
	if len(got) != 1 {
		t.Fatalf("took %d offers, want only the fresh one", len(got))
	}
	if s := string(got[0].(*msg.ImprovedInstantMessage).MessageBlock.Message); s != "fresh\x00" {
		t.Errorf("kept %q", s)
	}
}

// TestOffersAreBounded: a session can be talked at all day and nothing
// here is obliged to remember it.
func TestOffersAreBounded(t *testing.T) {
	var o Offers
	for i := 0; i < offerLimit*2; i++ {
		o.note(im("chatter"), time.Now())
	}
	held, dropped := o.Stats()
	if held > offerLimit {
		t.Errorf("held %d offers, over the %d limit", held, offerLimit)
	}
	if dropped == 0 {
		t.Error("nothing was dropped, so the limit did nothing")
	}
}

// TestOffersAreCopiedOutOfTheBuffer: the message arrives pointing into a
// receive buffer that is reused within milliseconds.
func TestOffersAreCopiedOutOfTheBuffer(t *testing.T) {
	var o Offers
	m := im("original")
	o.note(m, time.Now())
	copy(m.MessageBlock.Message, "OVERWRIT")

	got := o.Take()[0].(*msg.ImprovedInstantMessage)
	if s := string(got.MessageBlock.Message); s != "original\x00" {
		t.Errorf("message = %q; the buffer was kept rather than copied", s)
	}
}
