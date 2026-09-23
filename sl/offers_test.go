package sl

// Offers slgod kept, and what a session does about them.
//
// The fake here is fakeBackend with the four calls a daemon keeping a
// record adds, so that the logic -- load what was kept, ask before
// answering, drop what somebody else answered -- is tested against the
// same reader goroutine a real session runs.  hosted_test.go's daemon
// is used once, for the translation off the wire.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// keeperBackend is a fakeBackend with a daemon's record of offers.
type keeperBackend struct {
	*fakeBackend

	record *OfferRecord
	kept   []*Message

	mu     sync.Mutex
	asked  []string // "key how", in order
	undone []string

	// answer is what Handled says; nil agrees to everything.
	answer func(key string) (bool, *Handled)

	notices chan *Handled
}

var _ OfferKeeper = (*keeperBackend)(nil)

func (k *keeperBackend) KeptOffers() (*OfferRecord, []*Message, bool) {
	return k.record, k.kept, k.record != nil
}

func (k *keeperBackend) Handled(_ context.Context, key, how string) (bool, *Handled, error) {
	k.mu.Lock()
	k.asked = append(k.asked, key+" "+how)
	answer := k.answer
	k.mu.Unlock()
	if answer == nil {
		return true, nil, nil
	}
	claimed, earlier := answer(key)
	return claimed, earlier, nil
}

func (k *keeperBackend) Unhandled(_ context.Context, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.undone = append(k.undone, key)
	return nil
}

func (k *keeperBackend) HandledOffers() <-chan *Handled { return k.notices }

func (k *keeperBackend) Asked() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.asked...)
}

// Tell is the daemon saying an offer was dealt with, and returns once
// the reader has finished with it; see Relay for the barrier.
func (k *keeperBackend) Tell(t *testing.T, h *Handled) {
	t.Helper()
	select {
	case k.notices <- h:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing read the notices")
	}
	k.put(t, &Message{ID: barrierID, Name: "slgo relay barrier", At: time.Now()})
}

// newKeptSession is a session over a daemon that had kept these.
func newKeptSession(t *testing.T, record *OfferRecord, kept ...*Message) (*Session, *keeperBackend) {
	t.Helper()
	k := &keeperBackend{fakeBackend: newFake(t), record: record, kept: kept,
		notices: make(chan *Handled)}
	w, err := New(k)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return w, k
}

// Invented people and ids.
var (
	someLurer  = msg.MustParseUUID("41497e57-7e57-c0de-8e40-535e4340746b")
	someGiver  = msg.MustParseUUID("9bf57e57-7e57-c0de-acac-5c71d05f9c65")
	someGroup  = msg.MustParseUUID("e86b7e57-7e57-c0de-7369-7fbb4c3ea183")
	someFriend = msg.MustParseUUID("d45c7e57-7e57-c0de-8063-28db797b6d18")
	someLure   = msg.MustParseUUID("0d817e57-7e57-c0de-7df5-71b55084a53f")
	someTxn    = msg.MustParseUUID("77c67e57-7e57-c0de-0877-a368e2c13bb1")
)

// keptIM is an offer as the daemon hands it over: the message it came
// in, the daemon's name for it, and when it came.
func keptIM(t *testing.T, key string, at time.Time, from msg.UUID, name string, dialog uint8, id msg.UUID, text string, bucket []byte) *Message {
	t.Helper()
	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID = from
	m.MessageBlock.Dialog = dialog
	m.MessageBlock.ID = id
	m.MessageBlock.FromAgentName = append([]byte(name), 0)
	m.MessageBlock.Message = append([]byte(text), 0)
	m.MessageBlock.BinaryBucket = bucket
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return &Message{ID: msg.IDOf(m), Name: "ImprovedInstantMessage", Body: body, At: at,
		Offer: key, Recorded: true}
}

func itemBucket(item msg.UUID) []byte { return append([]byte{byte(AssetObject)}, item[:]...) }

// TestWhatSlgodKeptIsWaitingTheMomentTheSessionIs is the fault from the
// client's side: offers made while no client was attached were real and
// waiting on the grid, and a session attaching afterwards listed none of
// them.  They have to be in the lists as soon as New returns -- a shell
// running "waiting" as its only command asks at once -- marked as kept,
// and dated when they arrived rather than when the session read them.
func TestWhatSlgodKeptIsWaitingTheMomentTheSessionIs(t *testing.T) {
	t.Parallel()
	since := time.Now().Add(-3 * time.Hour)
	then := time.Now().Add(-2 * time.Hour).Truncate(time.Microsecond)
	w, k := newKeptSession(t, &OfferRecord{Since: since, Kept: 5, Limit: 100},
		keptIM(t, "lure:1", then, someLurer, "Example Lurer", DialogTeleportLure, someLure, "come over", nil),
		keptIM(t, "tprequest:1", then, someLurer, "Example Lurer", DialogTeleportRequest, msg.UUID{}, "may I", nil),
		keptIM(t, "inventory:1", then, someGiver, "Example Giver", DialogInventoryOffered, someTxn, "a lantern", itemBucket(someGiver)),
		keptIM(t, "friendship:1", then, someFriend, "Example Friend", DialogFriendshipOffered, someTxn, "", nil),
		keptIM(t, "group:1", then, someGroup, "Example Inviter", DialogGroupInvitation, someTxn, "join us", make([]byte, 20)),
	)
	defer k.Close()

	ls := w.Lures()
	if len(ls) != 1 || !ls[0].Recorded || !ls[0].At.Equal(then) {
		t.Fatalf("lures = %+v, want the kept one, marked and dated when it arrived", ls)
	}
	if rs := w.TeleportRequests(); len(rs) != 1 || !rs[0].Recorded {
		t.Errorf("teleport requests = %+v", rs)
	}
	if is := w.InventoryOffers(); len(is) != 1 || !is[0].Recorded || is[0].Name != "a lantern" {
		t.Errorf("inventory offers = %+v", is)
	}
	if fs := w.Offers(); len(fs) != 1 || !fs[0].Recorded {
		t.Errorf("friendship offers = %+v", fs)
	}
	if gs := w.Invitations(); len(gs) != 1 || !gs[0].Recorded {
		t.Errorf("invitations = %+v", gs)
	}
	rec, ok := w.OfferRecord()
	if !ok || !rec.Since.Equal(since) || rec.Kept != 5 {
		t.Errorf("record = %+v, %v", rec, ok)
	}
}

// liveOffer relays one as the daemon does when a session is attached:
// off the wire, named.
func liveOffer(t *testing.T, k *keeperBackend, key string, from msg.UUID, name string, dialog uint8, id msg.UUID, text string, bucket []byte) {
	t.Helper()
	m := keptIM(t, key, time.Now(), from, name, dialog, id, text, bucket)
	m.Recorded = false
	k.RelayRaw(t, m)
}

// sentIMs is the instant messages the session put on the wire.
func sentIMs(k *keeperBackend) []*msg.ImprovedInstantMessage {
	var out []*msg.ImprovedInstantMessage
	for _, s := range k.fakeBackend.Sent() {
		if im, ok := s.Msg.(*msg.ImprovedInstantMessage); ok {
			out = append(out, im)
		}
	}
	return out
}

// TestAnOfferAnotherClientAnsweredIsNotAnsweredAgain: two clients on one
// avatar, one offer.  Whichever asks the daemon second is told who got
// there first, and sends nothing -- a group joined twice is a fee paid
// twice, and an item accepted twice is a second answer to a transaction
// already spent.  The offer leaves this session's lists as well, since
// it is no longer waiting for anybody.
func TestAnOfferAnotherClientAnsweredIsNotAnsweredAgain(t *testing.T) {
	t.Parallel()
	w, k := newKeptSession(t, &OfferRecord{})
	defer k.Close()
	at := time.Now()
	k.answer = func(key string) (bool, *Handled) {
		return false, &Handled{Key: key, How: "accepted", By: "slbotd", At: at}
	}

	liveOffer(t, k, "inventory:9", someGiver, "Example Giver", DialogInventoryOffered, someTxn, "a lantern",
		itemBucket(someGiver))
	o := w.InventoryOffers()[0]
	err := o.Accept(context.Background(), msg.UUID{})
	var already *AnsweredError
	if !errors.As(err, &already) {
		t.Fatalf("accept answered %v, want to be told another client got there first", err)
	}
	if !strings.Contains(err.Error(), "slbotd") || !strings.Contains(err.Error(), "a lantern") ||
		!strings.Contains(err.Error(), "nothing was sent") {
		t.Errorf("the refusal says %q, want what, who, and that nothing went", err)
	}
	if n := len(sentIMs(k)); n != 0 {
		t.Errorf("%d answers went out for an offer already answered", n)
	}
	if n := len(w.InventoryOffers()); n != 0 {
		t.Errorf("%d offers still listed after the daemon said it was answered", n)
	}
}

// TestEveryAnswerAsksTheDaemonFirst: each way of dealing with a kept
// offer says so before anything goes, under the name the daemon gave it
// -- including refusing a teleport request, which sends nothing to the
// grid and is otherwise invisible to every other client.
func TestEveryAnswerAsksTheDaemonFirst(t *testing.T) {
	t.Parallel()
	w, k := newKeptSession(t, &OfferRecord{})
	defer k.Close()
	ctx := context.Background()

	liveOffer(t, k, "friendship:1", someFriend, "Example Friend", DialogFriendshipOffered, someTxn, "", nil)
	liveOffer(t, k, "inventory:1", someGiver, "Example Giver", DialogInventoryOffered, someTxn, "a lantern", itemBucket(someGiver))
	liveOffer(t, k, "lure:1", someLurer, "Example Lurer", DialogTeleportLure, someLure, "", nil)
	liveOffer(t, k, "group:1", someGroup, "Example Inviter", DialogGroupInvitation, someTxn, "", make([]byte, 20))
	liveOffer(t, k, "tprequest:1", someLurer, "Example Lurer", DialogTeleportRequest, msg.UUID{}, "", nil)

	if err := w.Offers()[0].Decline(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.InventoryOffers()[0].Decline(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.DeclineLure(ctx, w.Lures()[0]); err != nil {
		t.Fatal(err)
	}
	if err := w.DeclineInvitation(ctx, w.Invitations()[0]); err != nil {
		t.Fatal(err)
	}
	if err := w.RefuseTeleportRequest(ctx, w.TeleportRequests()[0]); err != nil {
		t.Fatal(err)
	}

	want := []string{"friendship:1 declined", "inventory:1 declined", "lure:1 declined",
		"group:1 declined", "tprequest:1 refused"}
	got := k.Asked()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the daemon was asked\n%q\nwant\n%q", got, want)
	}
}

// TestAnAnswerThatCouldNotGoIsPutBack: having told the daemon it was
// answering, a session whose answer then fails to go out has to say so,
// or the daemon goes on telling every other client that an offer still
// waiting on the grid has been dealt with.
func TestAnAnswerThatCouldNotGoIsPutBack(t *testing.T) {
	t.Parallel()
	w, k := newKeptSession(t, &OfferRecord{})
	defer k.Close()
	liveOffer(t, k, "group:2", someGroup, "Example Inviter", DialogGroupInvitation, someTxn, "", make([]byte, 20))

	k.fakeBackend.mu.Lock()
	k.fakeBackend.sendErr = errors.New("the circuit is down")
	k.fakeBackend.mu.Unlock()
	if err := w.AcceptInvitation(context.Background(), w.Invitations()[0]); err == nil {
		t.Fatal("an answer that could not be sent was reported as sent")
	}
	k.mu.Lock()
	undone := append([]string(nil), k.undone...)
	k.mu.Unlock()
	if len(undone) != 1 || undone[0] != "group:2" {
		t.Errorf("put back %q, want the invitation", undone)
	}
	if n := len(w.Invitations()); n != 1 {
		t.Errorf("%d invitations listed after a failed answer, want it still waiting", n)
	}
}

// TestAnOfferAnsweredElsewhereLeavesTheListsAndIsSaid: the daemon's word
// that another client dealt with an offer takes it out of this session's
// lists -- so that a person here is not left holding a number for
// something already answered -- and whoever is listening is told what it
// was, how and by whom.  An offer this session is answering itself is not
// reported back to it as somebody else's doing.
func TestAnOfferAnsweredElsewhereLeavesTheListsAndIsSaid(t *testing.T) {
	t.Parallel()
	w, k := newKeptSession(t, &OfferRecord{})
	defer k.Close()
	var told []Handled
	var mu sync.Mutex
	w.mu.Lock()
	w.OnHandled = func(h Handled) {
		mu.Lock()
		told = append(told, h)
		mu.Unlock()
	}
	w.mu.Unlock()

	liveOffer(t, k, "lure:3", someLurer, "Example Lurer", DialogTeleportLure, someLure, "", nil)
	k.Tell(t, &Handled{Key: "lure:3", How: "declined", By: "slsh", At: time.Now()})
	if n := len(w.Lures()); n != 0 {
		t.Errorf("%d lures still listed after another client declined it", n)
	}
	mu.Lock()
	if len(told) != 1 || !strings.Contains(told[0].String(), "the teleport Example Lurer offered was declined by slsh") {
		t.Errorf("told %+v", told)
	}
	told = nil
	mu.Unlock()

	// This session's own answer, whose notice comes back to it too.
	liveOffer(t, k, "group:3", someGroup, "Example Inviter", DialogGroupInvitation, someTxn, "", make([]byte, 20))
	if err := w.DeclineInvitation(context.Background(), w.Invitations()[0]); err != nil {
		t.Fatal(err)
	}
	k.Tell(t, &Handled{Key: "group:3", How: "declined", By: "slsh", At: time.Now()})
	mu.Lock()
	if len(told) != 0 {
		t.Errorf("this session's own answer was reported to it as another's: %+v", told)
	}
	mu.Unlock()
}

// TestAnOfferReadAfterItsNoticeIsNotKept: the offer and the notice that
// it has been dealt with come on different relays, and the reader takes
// whichever is ready.  An offer read after its own notice must not be
// listed as waiting -- but one put back out of the record, after an
// answer that failed, must be, and without being handed to a subscriber
// as though it had just arrived: that is an offer announced twice, and
// to a program that acts on offers, an offer acted on twice.
func TestAnOfferReadAfterItsNoticeIsNotKept(t *testing.T) {
	t.Parallel()
	w, k := newKeptSession(t, &OfferRecord{})
	defer k.Close()

	k.Tell(t, &Handled{Key: "lure:4", How: "accepted", By: "slbotd", At: time.Now()})
	liveOffer(t, k, "lure:4", someLurer, "Example Lurer", DialogTeleportLure, someLure, "", nil)
	if n := len(w.Lures()); n != 0 {
		t.Errorf("an offer read after the notice that it was answered is listed: %d", n)
	}

	ims := w.IMs(8)
	back := keptIM(t, "lure:4", time.Now(), someLurer, "Example Lurer", DialogTeleportLure, someLure, "", nil)
	k.RelayRaw(t, back)
	if n := len(w.Lures()); n != 1 {
		t.Errorf("an offer put back is not listed: %d", n)
	}
	select {
	case im := <-ims:
		t.Errorf("an offer out of the record was delivered as arriving now: %+v", im)
	default:
	}
}

// TestTheKeptOffersCrossTheWire: what the daemon hands over at attach,
// and what it answers Handled with, turn into this package's own types
// field by field -- the name the daemon gave each offer above all, since
// it is the only thing the answer can quote.
func TestTheKeptOffersCrossTheWire(t *testing.T) {
	t.Parallel()
	d, conn := dialFakeDaemon(t)
	then := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	im := keptIM(t, "lure:5", then, someLurer, "Example Lurer", DialogTeleportLure, someLure, "", nil)
	d.offers = &pb.OfferRecord{
		Since: then.Add(-time.Hour).UnixMicro(), Evicted: 2, Limit: 100,
		Messages: []*pb.InboundMessage{{
			Id: uint32(im.ID), Name: im.Name, Body: im.Body, Sequence: 9,
			ReceivedAt: then.UnixMicro(), Offer: "lure:5", Recorded: true,
		}},
	}
	d.handle = func(r *pb.HandledRequest) *pb.HandledResponse {
		return &pb.HandledResponse{Earlier: &pb.OfferHandled{
			Offer: r.GetOffer(), How: "accepted", By: "slbotd", At: then.UnixMicro()}}
	}
	h, err := AttachConn(context.Background(), conn, "quark")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	rec, msgs, ok := h.KeptOffers()
	if !ok || rec.Evicted != 2 || rec.Limit != 100 || rec.Kept != 1 || !rec.Since.Equal(then.Add(-time.Hour)) {
		t.Fatalf("record = %+v, %v", rec, ok)
	}
	if len(msgs) != 1 || msgs[0].Offer != "lure:5" || !msgs[0].Recorded || !msgs[0].At.Equal(then) ||
		msgs[0].Sequence != 9 {
		t.Errorf("messages = %+v", msgs)
	}

	claimed, earlier, err := h.Handled(context.Background(), "lure:5", "declined")
	if err != nil || claimed || earlier == nil || earlier.By != "slbotd" || !earlier.At.Equal(then) {
		t.Errorf("Handled = %v %+v %v", claimed, earlier, err)
	}
	if r := <-d.handled; r.GetOffer() != "lure:5" || r.GetHow() != "declined" || r.GetUndo() {
		t.Errorf("the daemon was asked %v", r)
	}
	if err := h.Unhandled(context.Background(), "lure:5"); err != nil {
		t.Fatal(err)
	}
	if r := <-d.handled; !r.GetUndo() {
		t.Errorf("an undo reached the daemon as %v", r)
	}

	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Handled{Handled: &pb.OfferHandled{
		Offer: "lure:5", How: "declined", By: "slsh", At: then.UnixMicro()}}}
	select {
	case n := <-h.HandledOffers():
		if n.Key != "lure:5" || n.How != "declined" || n.By != "slsh" || !n.At.Equal(then) {
			t.Errorf("notice = %+v", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the notice never came through")
	}
}
