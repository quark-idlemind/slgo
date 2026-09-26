package server

// The offers kept for a client that was not there.
//
// Each test here is one way the record could fail somebody starting a
// client after a while away: not keeping what arrived, keeping what was
// already answered, letting two clients answer one offer, or losing an
// offer whose answer never went.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// Invented, like everybody in this tree who is not Quark.
var (
	aLurer   = msg.MustParseUUID("41497e57-7e57-c0de-8e40-535e4340746b")
	aGiver   = msg.MustParseUUID("9bf57e57-7e57-c0de-acac-5c71d05f9c65")
	aGroup   = msg.MustParseUUID("e86b7e57-7e57-c0de-7369-7fbb4c3ea183")
	aLure    = msg.MustParseUUID("0d817e57-7e57-c0de-3eb0-2dd03720cbb5")
	aTxn     = msg.MustParseUUID("77c67e57-7e57-c0de-0877-a368e2c13bb1")
	aGroupTx = msg.MustParseUUID("366d7e57-7e57-c0de-65d1-3e37922ac4d3")
)

// instantMessage is one the way the simulator sends it.
func instantMessage(from msg.UUID, name string, dialog uint8, id msg.UUID, text string, bucket []byte) *msg.ImprovedInstantMessage {
	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID = from
	m.MessageBlock.Dialog = dialog
	m.MessageBlock.ID = id
	m.MessageBlock.FromAgentName = append([]byte(name), 0)
	m.MessageBlock.Message = append([]byte(text), 0)
	m.MessageBlock.BinaryBucket = bucket
	return m
}

// itemBucket is what an item offer carries: the asset type and the item.
func itemBucket(item msg.UUID) []byte { return append([]byte{6}, item[:]...) }

// arrived is a packet as the relay is handed one.
func arrived(m msg.Message, seq uint32) *msg.Packet {
	body, _ := m.Encode()
	return &msg.Packet{ID: m.MsgInfo().ID, Body: body, Header: msg.Header{Sequence: seq}, At: time.Now()}
}

// imClient is a client attached by hand, asking for instant messages.
func imClient(h *Hosted) (*Client, *pb.OfferRecord) {
	c := &Client{out: make(chan *pb.ServerPacket, 16), subs: map[msg.ID]bool{}, names: map[string]bool{}}
	c.setSubs(&pb.Subscribe{Set: []string{"ImprovedInstantMessage"}})
	return c, h.attach(c)
}

// TestAnOfferMadeWithNobodyAttachedIsHandedToTheNextClient is the fault
// itself.  Two group invitations were sent to an avatar with no client
// attached; the daemon acknowledged them and dropped them, and a shell
// started afterwards said "nothing waiting".  An offer that arrives to
// nobody has to be in the frame the next attach begins with -- and chat
// that arrives the same way must not be, or the record is a log.
func TestAnOfferMadeWithNobodyAttachedIsHandedToTheNextClient(t *testing.T) {
	r := newRig(t, nil)
	h, _ := r.srv.Agent("example")

	r.sim.send(instantMessage(aGroup, "Example Inviter", imGroupInvitation, aGroupTx,
		"Example Inviter has invited you to join a group.", make([]byte, 20)), msg.FlagReliable)
	r.sim.send(instantMessage(aLurer, "Example Lurer", 0, msg.UUID{}, "just talking", nil), msg.FlagReliable)
	waitFor(t, 5*time.Second, "the invitation to be kept", func() bool {
		return len(h.offerLog().snapshot().GetMessages()) > 0
	})
	// Long enough for the remark, sent second, to have been read too.
	time.Sleep(100 * time.Millisecond)

	c := r.dial(t, "ImprovedInstantMessage")
	defer c.Close()
	rec := c.Offers()
	if rec == nil {
		t.Fatal("the attach carried no record of offers at all")
	}
	if n := len(rec.GetMessages()); n != 1 {
		t.Fatalf("the record held %d messages, want the one invitation and not the remark", n)
	}
	got := rec.GetMessages()[0]
	if got.GetOffer() != offerKey(offerGroup, aGroupTx) || !got.GetRecorded() {
		t.Errorf("kept %q recorded=%v, want it named for the invitation and marked recorded",
			got.GetOffer(), got.GetRecorded())
	}
	var m msg.ImprovedInstantMessage
	if err := m.Decode(got.GetBody()); err != nil || m.MessageBlock.ID != aGroupTx {
		t.Errorf("the kept body does not read back as the invitation: %v", err)
	}
	if rec.GetSince() == 0 || rec.GetLimit() != offerLimit {
		t.Errorf("since=%d limit=%d, want both said", rec.GetSince(), rec.GetLimit())
	}

	// A client that asked for no instant messages has no offers to be
	// told about, and is not handed a record it did not ask for.
	quiet := r.dial(t, "ChatFromSimulator")
	defer quiet.Close()
	if quiet.Offers() != nil {
		t.Error("an attach without instant messages was handed the offers anyway")
	}
}

// TestALiveOfferIsNamedSoThatItCanBeAnswered: a client attached when an
// offer arrives needs the same name for it that a later client gets out
// of the record, or it has nothing to tell the daemon when it answers.
func TestALiveOfferIsNamedSoThatItCanBeAnswered(t *testing.T) {
	r := newRig(t, nil)
	c := r.dial(t, "ImprovedInstantMessage")
	defer c.Close()

	r.sim.send(instantMessage(aLurer, "Example Lurer", imLureUser, aLure, "come over", nil), msg.FlagReliable)
	m := waitMsg(t, c, "ImprovedInstantMessage", 5*time.Second)
	if m.Offer != offerKey(offerLure, aLure) {
		t.Errorf("relayed with offer %q, want %q", m.Offer, offerKey(offerLure, aLure))
	}
	if m.Recorded {
		t.Error("a live offer was marked as coming out of the record")
	}
}

// TestTheFirstClientToAnswerIsTheOnlyOne: two clients on one avatar see
// the same offer.  The first to say it is dealing with it takes it out
// of the record and every client is told; the second to ask is told who
// did and how, which is what lets it send nothing rather than answer an
// offer twice.  A client attaching afterwards is not handed it at all.
func TestTheFirstClientToAnswerIsTheOnlyOne(t *testing.T) {
	r := newRig(t, nil)
	h, _ := r.srv.Agent("example")
	a := r.dial(t, "ImprovedInstantMessage")
	defer a.Close()
	b := r.dial(t, "ImprovedInstantMessage")
	defer b.Close()

	r.sim.send(instantMessage(aGiver, "Example Giver", imInventoryOffered, aTxn, "a lantern", itemBucket(aGiver)),
		msg.FlagReliable)
	key := waitMsg(t, a, "ImprovedInstantMessage", 5*time.Second).Offer
	waitMsg(t, b, "ImprovedInstantMessage", 5*time.Second)
	if key == "" {
		t.Fatal("the offer was relayed without a name")
	}

	ctx := context.Background()
	first, err := a.Handled(ctx, key, "accepted", false)
	if err != nil {
		t.Fatal(err)
	}
	if !first.GetClaimed() {
		t.Fatalf("the first to ask was refused: %v", first)
	}

	// The other client is told, which is what takes it out of the
	// listing a person there is looking at.
	select {
	case n := <-b.HandledOffers():
		if n.GetOffer() != key || n.GetHow() != "accepted" || n.GetAt() == 0 {
			t.Errorf("b was told %v", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the other client was never told the offer had been dealt with")
	}

	second, err := b.Handled(ctx, key, "declined", false)
	if err != nil {
		t.Fatal(err)
	}
	if second.GetClaimed() {
		t.Error("the second client to ask was told to go ahead as well")
	}
	if e := second.GetEarlier(); e == nil || e.GetHow() != "accepted" {
		t.Errorf("the second client was told %v, want who got there first and how", e)
	}

	later := r.dial(t, "ImprovedInstantMessage")
	defer later.Close()
	if n := len(later.Offers().GetMessages()); n != 0 {
		t.Errorf("a client attaching after the answer was handed %d offers", n)
	}
	if n := len(h.offerLog().snapshot().GetMessages()); n != 0 {
		t.Errorf("the record still holds %d", n)
	}

	// And an offer nobody ever kept is not refused, because not having
	// heard of it says nothing about whether it was answered.
	unknown, err := a.Handled(ctx, offerKey(offerLure, aLurer), "accepted", false)
	if err != nil {
		t.Fatal(err)
	}
	if unknown.GetClaimed() || unknown.GetEarlier() != nil {
		t.Errorf("an offer the daemon never kept was answered with %v", unknown)
	}
}

// TestAnAnswerThatNeverWentPutsTheOfferBack: a client says it is
// answering, and then its answer cannot be sent.  The offer is still
// waiting on the grid, so it has to be waiting in the record too, and
// every client that dropped it on the word of the first has to be given
// it back -- quietly, since it is not news.
func TestAnAnswerThatNeverWentPutsTheOfferBack(t *testing.T) {
	t.Parallel()
	h := &Hosted{Name: "example", clients: map[*Client]bool{}}
	other, _ := imClient(h)

	h.relay(arrived(instantMessage(aLurer, "Example Lurer", imLureUser, aLure, "come over", nil), 7))
	<-other.out
	key := offerKey(offerLure, aLure)

	srv := New()
	srv.agents["example"] = h
	ctx := context.Background()
	if r, err := srv.Handled(ctx, &pb.HandledRequest{Offer: key, How: "accepted"}); err != nil || !r.GetClaimed() {
		t.Fatalf("claim: %v %v", r, err)
	}
	<-other.out // told it had gone

	r, err := srv.Handled(ctx, &pb.HandledRequest{Offer: key, Undo: true})
	if err != nil {
		t.Fatal(err)
	}
	if !r.GetRestored() {
		t.Fatal("the undo did not find the offer")
	}
	select {
	case p := <-other.out:
		in := p.GetMessage()
		if in.GetOffer() != key || !in.GetRecorded() {
			t.Errorf("given back %v, want the offer, marked recorded", in)
		}
	default:
		t.Error("the other client was not given the offer back")
	}
	if n := len(h.offerLog().snapshot().GetMessages()); n != 1 {
		t.Errorf("the record holds %d after the undo, want the offer back", n)
	}
}

// TestAnAnswerNobodyAnnouncedStillTakesTheOfferOut: a client built
// before Handled, or a viewer on the login endpoint, answers an offer
// without saying so first.  The answer goes out through this session's
// circuit all the same, and the record must not go on offering every
// later client something already answered.
func TestAnAnswerNobodyAnnouncedStillTakesTheOfferOut(t *testing.T) {
	t.Parallel()
	h := &Hosted{Name: "example", clients: map[*Client]bool{}}
	c, _ := imClient(h)

	h.relay(arrived(instantMessage(aGiver, "Example Giver", imInventoryOffered, aTxn, "a lantern", itemBucket(aGiver)), 3))
	h.relay(arrived(instantMessage(aLurer, "Example Lurer", imLureUser, aLure, "", nil), 4))
	h.relay(arrived(instantMessage(aLurer, "Example Lurer", imTeleportRequest, msg.UUID{}, "may I", nil), 5))
	for range 3 {
		<-c.out
	}

	// The item taken, the lure taken, and the request answered the only
	// way one can be: with an offer going back.
	take := instantMessage(aGiver, "", imInventoryAccepted, aTxn, "", nil)
	h.noteSent(arrived(take, 0))
	lure := &msg.TeleportLureRequest{}
	lure.Info.LureID = aLure
	h.noteSent(arrived(lure, 0))
	back := &msg.StartLure{TargetData: []msg.StartLure_TargetData{{TargetID: aLurer}}}
	h.noteSent(arrived(back, 0))

	if n := len(h.offerLog().snapshot().GetMessages()); n != 0 {
		t.Errorf("the record still holds %d offers that were answered on the wire", n)
	}
	for range 3 {
		select {
		case p := <-c.out:
			if n := p.GetHandled(); n == nil || n.GetBy() != "" {
				t.Errorf("told %v, want a notice naming nobody", p)
			}
		default:
			t.Fatal("the client was not told of every answer that went out")
		}
	}
}

// TestTheRecordKeepsOneOfEachAndNoMore: what makes an offer stop being
// worth keeping, other than an answer.
func TestTheRecordKeepsOneOfEachAndNoMore(t *testing.T) {
	t.Parallel()
	h := &Hosted{Name: "example", clients: map[*Client]bool{}}

	// A retransmission is the same offer, not a second one: the relay
	// runs ahead of duplicate suppression and sees both.
	first := instantMessage(aLurer, "Example Lurer", imLureUser, aLure, "come over", nil)
	h.noteOffer(arrived(first, 10))
	h.noteOffer(arrived(first, 10))
	// A second lure from the same person replaces the first, the way the
	// client keeps them: answering the stale id would send the avatar
	// somewhere its sender has stopped waiting.
	newer := msg.MustParseUUID("4fc57e57-7e57-c0de-7323-c067aef21fcb")
	h.noteOffer(arrived(instantMessage(aLurer, "Example Lurer", imLureUser, newer, "over here now", nil), 11))

	got := h.offerLog().snapshot().GetMessages()
	if len(got) != 1 || got[0].GetOffer() != offerKey(offerLure, newer) {
		t.Fatalf("kept %v, want only the newer lure", got)
	}

	// An answered offer arriving again under its old sequence number is
	// a retransmission, and is not waiting again.
	srv := New()
	srv.agents["example"] = h
	ctx := context.Background()
	if _, err := srv.Handled(ctx, &pb.HandledRequest{Offer: offerKey(offerLure, newer), How: "declined"}); err != nil {
		t.Fatal(err)
	}
	h.noteOffer(arrived(instantMessage(aLurer, "Example Lurer", imLureUser, newer, "over here now", nil), 11))
	if n := len(h.offerLog().snapshot().GetMessages()); n != 0 {
		t.Errorf("a retransmission of an answered offer put %d back", n)
	}

	// Items are the exception: two things handed over are two offers.
	for i := range offerLimit + 3 {
		id := msg.UUID{0x9c, 0x07, byte(i >> 8), byte(i)}
		h.noteOffer(arrived(instantMessage(aGiver, "Example Giver", imInventoryOffered, id, "a lantern", itemBucket(id)),
			uint32(100+i)))
	}
	rec := h.offerLog().snapshot()
	if n := len(rec.GetMessages()); n != offerLimit {
		t.Errorf("kept %d, want the limit of %d", n, offerLimit)
	}
	if rec.GetEvicted() != 3 {
		t.Errorf("evicted = %d, want the 3 oldest said to have gone", rec.GetEvicted())
	}
	var m msg.ImprovedInstantMessage
	m.Decode(rec.GetMessages()[0].GetBody())
	if want := (msg.UUID{0x9c, 0x07, 0, 3}); m.MessageBlock.ID != want {
		t.Errorf("the oldest kept is %v, want %v: the oldest go first", m.MessageBlock.ID, want)
	}
}

// TestStartAgentWatchesWhatTheAvatarSends: the backstop is only a
// backstop if it is wired into the session, and the caller's own send
// hook -- slgod's trace -- must survive it being wired in.
func TestStartAgentWatchesWhatTheAvatarSends(t *testing.T) {
	sim := newSim(t)
	defer sim.close()
	var logins atomic.Int64
	hs := loginServer(t, sim, &logins, nil)

	var mine atomic.Int64
	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{
			Timeout: 10 * time.Second, SkipCaps: true, Idle: -1,
			// Only the lure: the session sends other things of its own
			// meanwhile, and counting those made this fail under load.
			SendTap: func(p *msg.Packet) {
				if p.ID == msg.IDOf(&msg.TeleportLureRequest{}) {
					mine.Add(1)
				}
			},
		})
	if err != nil {
		t.Fatal(err)
	}
	h.noteOffer(arrived(instantMessage(aLurer, "Example Lurer", imLureUser, aLure, "", nil), 1))

	lure := &msg.TeleportLureRequest{}
	lure.Info.LureID = aLure
	before := mine.Load()
	h.opts.SendTap(arrived(lure, 0))
	if mine.Load()-before != 1 {
		t.Error("the caller's send hook did not see the message")
	}
	if n := len(h.offerLog().snapshot().GetMessages()); n != 0 {
		t.Errorf("the record still holds %d after the avatar took the lure", n)
	}
}
