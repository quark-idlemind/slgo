package sl

// Instant messages, which are a dozen different things wearing one
// message.
//
// The routing is what these tests are about.  ImprovedInstantMessage
// carries somebody talking, a group notice, an inventory offer, a
// friendship offer and a teleport lure, told apart only by a number,
// and two of those have to be kept rather than merely delivered: the
// transaction id an offer arrives with is the only thing that can
// answer it and cannot be recovered afterwards.  An offer that is
// dropped, or filed under the wrong key, is an offer nobody can ever
// accept -- which is the failure all the bookkeeping here exists to
// prevent, and which no amount of live testing would show up quickly.
//
// Everything below runs against the fake backend in fake_test.go, so
// the whole of it works with no grid and no network.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	somebody     = msg.MustParseUUID("a72c7e57-7e57-c0de-c01f-3c2f792e3dc6")
	somebodyElse = msg.MustParseUUID("bca27e57-7e57-c0de-96d3-7101f4e3c87a")
)

// arrivingIM is one instant message as the simulator sends it.
func arrivingIM(from msg.UUID, name string, dialog uint8, id msg.UUID, text string) *msg.ImprovedInstantMessage {
	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID = from
	m.MessageBlock.FromAgentName = append([]byte(name), 0)
	m.MessageBlock.Message = append([]byte(text), 0)
	m.MessageBlock.Dialog = dialog
	m.MessageBlock.ID = id
	return m
}

// offerBucket is the binary bucket an inventory offer carries: one
// byte saying what kind of thing it is, then the item's id.
func offerBucket(a AssetType, item msg.UUID) []byte {
	return append([]byte{byte(a)}, item[:]...)
}

// TestConversationIsOnlySomebodyTalking: the two dialogs a person
// can be behind, and nothing else.  A caller writing a chat program
// asks this rather than learning the numbers, so a wrong answer here
// puts group notices and typing notifications in somebody's log.
func TestConversationIsOnlySomebodyTalking(t *testing.T) {
	cases := []struct {
		name string
		im   IM
		want bool
	}{
		{"a private message", IM{Dialog: DialogMessage, From: somebody}, true},
		{"a message box", IM{Dialog: DialogMessageBox, From: somebody}, true},
		{"a script's message", IM{Dialog: DialogFromTask, From: somebody}, false},
		{"a do not disturb auto response", IM{Dialog: DialogDoNotDisturbAutoResponse, From: somebody}, false},
		{"a group message", IM{Dialog: DialogSessionSend, From: somebody}, false},
		{"a teleport lure", IM{Dialog: DialogTeleportLure, From: somebody}, false},
		{"an inventory offer", IM{Dialog: DialogInventoryOffered, From: somebody}, false},
		{"typing", IM{Dialog: DialogTypingStart, From: somebody}, false},
		// A group saying something is the group, not a person, even
		// though it arrives with the same number.
		{"from a group", IM{Dialog: DialogMessage, From: somebody, Group: true}, false},
		// Nobody to answer is nobody talking.
		{"from nobody", IM{Dialog: DialogMessage}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.im.Conversation(); got != c.want {
				t.Errorf("Conversation = %v, want %v", got, c.want)
			}
		})
	}
}

// TestAScriptAndAnAutoResponseAreNotSpoken: dialog 19 is a script's
// llInstantMessage, which carries its owner's id and any name the
// object was given, and 20 is a viewer answering by itself.  Either
// taken for conversation is answered, or obeyed, as a person.
func TestAScriptAndAnAutoResponseAreNotSpoken(t *testing.T) {
	for _, d := range []uint8{DialogFromTask, DialogDoNotDisturbAutoResponse} {
		for _, mine := range []bool{false, true} {
			im := IM{Dialog: d, From: somebody, FromName: "Quark Idlemind", Text: ":where", Mine: mine}
			if im.Spoken() {
				t.Errorf("a %s (mine %v) is Spoken", DialogName(d), mine)
			}
			if im.Conversation() {
				t.Errorf("a %s (mine %v) is Conversation", DialogName(d), mine)
			}
		}
	}
}

// TestAScriptDoesNotTeachItsOwnerItsName: a script's message arrives
// with the owner's id and the object's name, the shape measured in
// doc/im-senders.md.  It is delivered as it came, and the object's
// name is not filed under the owner.
func TestAScriptDoesNotTeachItsOwnerItsName(t *testing.T) {
	w, f := newFakeSession(t)
	ims := w.IMs(4)

	object := msg.MustParseUUID("57327e57-7e57-c0de-3d85-fc737241ec86")
	m := arrivingIM(somebody, "Doorbell", DialogFromTask, object, "somebody is at the door")
	m.MessageBlock.ToAgentID = somebody
	m.MessageBlock.BinaryBucket = []byte("Pelmar Reach/128/64/22")
	f.Relay(t, m)

	select {
	case im := <-ims:
		if im.From != somebody || im.ID != object || im.FromName != "Doorbell" {
			t.Errorf("from %s, object %s, name %q", im.From, im.ID, im.FromName)
		}
		if string(im.Bucket) != "Pelmar Reach/128/64/22" {
			t.Errorf("bucket = %q", im.Bucket)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the message never reached the subscription")
	}
	if got := w.Name(somebody); got != "" {
		t.Errorf("the owner is now called %q, which is the object", got)
	}
}

// TestAnObjectsOfferOrAlertDoesNotRenameItsOwner: dialogs 9 and 31 are
// built as 19 is, by the viewer's reading, with the owner's id and the
// object's name.  Neither changes what the session calls the owner.
func TestAnObjectsOfferOrAlertDoesNotRenameItsOwner(t *testing.T) {
	for _, d := range []uint8{DialogTaskInventoryOffered, DialogFromTaskAsAlert} {
		t.Run(DialogName(d), func(t *testing.T) {
			w, f := newFakeSession(t)
			ims := w.IMs(4)
			w.learn(somebody, "Quark Idlemind")

			id := msg.MustParseUUID("57327e57-7e57-c0de-3d85-fc737241ec86")
			m := arrivingIM(somebody, "Doorbell", d, id, "somebody is at the door")
			m.MessageBlock.ToAgentID = somebody
			if d == DialogTaskInventoryOffered {
				m.MessageBlock.BinaryBucket = []byte{byte(AssetNotecard)}
			}
			f.Relay(t, m)

			select {
			case im := <-ims:
				if im.Dialog != d || im.From != somebody || im.FromName != "Doorbell" {
					t.Errorf("dialog %d from %s, name %q", im.Dialog, im.From, im.FromName)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the message never reached the subscription")
			}
			if got := w.Name(somebody); got != "Quark Idlemind" {
				t.Errorf("the owner is now called %q, which is the object", got)
			}
		})
	}
}

// TestDialogNames: the numbers are how the protocol says what arrived,
// and this is the only place that turns them back into words.
func TestDialogNames(t *testing.T) {
	cases := map[uint8]string{
		DialogMessage:                  "message",
		DialogMessageBox:               "message box",
		DialogGroupInvitation:          "group invitation",
		DialogInventoryOffered:         "inventory offer",
		DialogInventoryAccepted:        "inventory accepted",
		DialogInventoryDeclined:        "inventory declined",
		DialogTaskInventoryOffered:     "object inventory offer",
		DialogSessionSend:              "group message",
		DialogFromTask:                 "object message",
		DialogDoNotDisturbAutoResponse: "do not disturb auto response",
		DialogTeleportLure:             "teleport lure",
		DialogFromTaskAsAlert:          "object alert",
		DialogGroupNotice:              "group notice",
		DialogFriendshipOffered:        "friendship offer",
		DialogFriendshipAccepted:       "friendship accepted",
		DialogFriendshipDeclined:       "friendship declined",
		DialogTypingStart:              "typing",
		DialogTypingStop:               "stopped typing",
	}
	for d, want := range cases {
		if got := DialogName(d); got != want {
			t.Errorf("DialogName(%d) = %q, want %q", d, got, want)
		}
	}
	// One this package has never heard of still says something, since
	// the alternative is a log line that names nothing at all.
	if got := DialogName(200); got != "dialog 200" {
		t.Errorf("an unknown dialog = %q", got)
	}
}

// TestAnInstantMessageArrivesWhole: every field a caller can act on
// comes out of the message, and the sender's name is learned on the
// way past -- an instant message is one of the few things that carries
// a name, and not keeping it means asking for it later.
func TestAnInstantMessageArrivesWhole(t *testing.T) {
	w, f := newFakeSession(t)
	ims := w.IMs(4)

	conv := msg.MustParseUUID("17037e57-7e57-c0de-b12a-24470e312844")
	m := arrivingIM(somebody, "Quark Idlemind", DialogMessage, conv, "hello there")
	m.MessageBlock.FromGroup = true
	m.MessageBlock.BinaryBucket = []byte{1, 2, 3}
	f.Relay(t, m)

	select {
	case im := <-ims:
		if im.From != somebody || im.FromName != "Quark Idlemind" {
			t.Errorf("from %s %q", im.From, im.FromName)
		}
		if im.Text != "hello there" {
			t.Errorf("text = %q", im.Text)
		}
		if im.Dialog != DialogMessage || im.ID != conv || !im.Group {
			t.Errorf("im = %+v", im)
		}
		if string(im.Bucket) != "\x01\x02\x03" {
			t.Errorf("bucket = %v", im.Bucket)
		}
		if time.Since(im.At) > time.Minute {
			t.Errorf("arrived at %s", im.At)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the message never reached the subscription")
	}

	if got := w.Name(somebody); got != "Quark Idlemind" {
		t.Errorf("the name that came with the message was not learned: %q", got)
	}
}

// TestIMSubscriptionsHearEveryKind: deciding which kinds matter is the
// caller's business, so the session must not filter, and two callers
// watching for different things must not have to agree about who
// consumes what.
func TestIMSubscriptionsHearEveryKind(t *testing.T) {
	w, f := newFakeSession(t)

	a := w.IMs(8)
	b := w.IMs(0) // a depth of zero asks for the default
	if cap(b) != DefaultIMDepth {
		t.Errorf("the default depth is %d, want %d", cap(b), DefaultIMDepth)
	}

	for _, d := range []uint8{DialogMessage, DialogTypingStart, DialogTeleportLure} {
		f.Relay(t, arrivingIM(somebody, "Quark Idlemind", d, msg.UUID{}, "x"))
	}
	for i, ch := range []<-chan *IM{a, b} {
		for _, want := range []uint8{DialogMessage, DialogTypingStart, DialogTeleportLure} {
			select {
			case im := <-ch:
				if im.Dialog != want {
					t.Errorf("subscription %d saw %s, want %s",
						i, DialogName(im.Dialog), DialogName(want))
				}
			default:
				t.Errorf("subscription %d never saw %s", i, DialogName(want))
			}
		}
	}

	// Stopping one closes it and leaves the other alone.
	w.StopIMs(a)
	if _, open := <-a; open {
		t.Error("the channel should be closed after StopIMs")
	}
	// Stopping something already stopped, or never started, is not an
	// error: a caller cleaning up should not have to remember which.
	w.StopIMs(a)
	w.StopIMs(make(chan *IM))

	f.Relay(t, arrivingIM(somebody, "Quark Idlemind", DialogMessage, msg.UUID{}, "still here"))
	select {
	case im := <-b:
		if im.Text != "still here" {
			t.Errorf("the surviving subscription got %q", im.Text)
		}
	default:
		t.Error("stopping one subscription stopped the other")
	}
}

// TestIMsDropRatherThanBlock: there is no way to ask the simulator to
// say it again, so a full buffer must cost that subscriber messages
// and everybody else nothing.  Stalling the relay would stop the
// session reading anything at all.
func TestIMsDropRatherThanBlock(t *testing.T) {
	w, f := newFakeSession(t)

	slow := w.IMs(2)
	fast := w.IMs(16)
	for i := range 6 {
		f.Relay(t, arrivingIM(somebody, "Quark", DialogMessage, msg.UUID{}, string(rune('a'+i))))
	}

	if got := w.IMsDropped(slow); got != 4 {
		t.Errorf("the small subscription dropped %d, want 4", got)
	}
	if got := w.IMsDropped(fast); got != 0 {
		t.Errorf("the roomy subscription dropped %d, want 0", got)
	}
	// A channel that is not a subscription has dropped nothing, rather
	// than being a reason to panic.
	if got := w.IMsDropped(make(chan *IM)); got != 0 {
		t.Errorf("an unknown channel reported %d dropped", got)
	}
}

// TestAFriendshipOfferIsKeptUntilItIsAnswered: the offer carries a
// transaction id that nothing else will ever repeat, and accepting
// means quoting it back.  Only delivering the offer to a subscription
// would leave it unanswerable the moment nobody was listening.
func TestAFriendshipOfferIsKeptUntilItIsAnswered(t *testing.T) {
	w, f := newFakeSession(t)

	txn := msg.MustParseUUID("cff37e57-7e57-c0de-8e11-705cd0510fca")
	f.Relay(t, arrivingIM(somebody, "Quark Idlemind", DialogFriendshipOffered, txn, "be my friend?"))

	offers := w.Offers()
	if len(offers) != 1 {
		t.Fatalf("kept %d offers, want 1", len(offers))
	}
	o := offers[0]
	if o.From != somebody || o.Name != "Quark Idlemind" || o.Transaction != txn {
		t.Errorf("offer = %+v", o)
	}
	if got, ok := w.OfferFrom(somebody); !ok || got != o {
		t.Errorf("OfferFrom = %v, %v", got, ok)
	}
	if _, ok := w.OfferFrom(somebodyElse); ok {
		t.Error("somebody who never offered has an offer waiting")
	}
	if got := o.String(); got != "Quark Idlemind offers friendship" {
		t.Errorf("String = %q", got)
	}

	if err := o.Accept(context.Background()); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	m := onlySent[*msg.AcceptFriendship](t, f)
	if m.TransactionBlock.TransactionID != txn {
		t.Errorf("accepted transaction %s, want the offer's %s",
			m.TransactionBlock.TransactionID, txn)
	}
	if m.AgentData.AgentID != testAgentID || m.AgentData.SessionID != testSessionID {
		t.Errorf("the answer came from %+v", m.AgentData)
	}
	// The folder block has to be there even though the calling card is
	// the part that does not happen.
	if len(m.FolderData) != 1 {
		t.Errorf("FolderData has %d blocks, want 1", len(m.FolderData))
	}

	// Accepting tells nobody on this side, so the session records the
	// friendship itself or the friend list stays wrong until the next
	// login.
	noted := f.Noted()
	if len(noted) != 1 || noted[0].ID != somebody || !noted[0].Online {
		t.Errorf("NoteFriend saw %+v", noted)
	}
	if len(w.Offers()) != 0 {
		t.Error("the offer is still waiting after being accepted")
	}
}

// TestDecliningAFriendshipOffer: the simulator has to be told, or the
// offer stays pending on the other side for ever.
func TestDecliningAFriendshipOffer(t *testing.T) {
	w, f := newFakeSession(t)

	txn := msg.MustParseUUID("cff37e57-7e57-c0de-8e11-705cd0510fca")
	f.Relay(t, arrivingIM(somebody, "Quark Idlemind", DialogFriendshipOffered, txn, ""))

	o, ok := w.OfferFrom(somebody)
	if !ok {
		t.Fatal("the offer was not kept")
	}
	if err := o.Decline(context.Background()); err != nil {
		t.Fatalf("Decline: %v", err)
	}
	if m := onlySent[*msg.DeclineFriendship](t, f); m.TransactionBlock.TransactionID != txn {
		t.Errorf("declined transaction %s, want %s", m.TransactionBlock.TransactionID, txn)
	}
	if len(w.Offers()) != 0 {
		t.Error("the offer is still waiting after being declined")
	}
	if len(f.Noted()) != 0 {
		t.Error("declining recorded a friendship")
	}
}

// TestAnOfferAnsweredIntoTheVoidIsKept: if the answer did not go, the
// offer has not been answered, and forgetting it would throw away the
// only id that can ever answer it.
func TestAnOfferAnsweredIntoTheVoidIsKept(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, arrivingIM(somebody, "Quark Idlemind", DialogFriendshipOffered, msg.UUID{9}, ""))
	f.FailSends(errors.New("the circuit is gone"))

	o, _ := w.OfferFrom(somebody)
	if err := o.Accept(context.Background()); err == nil {
		t.Error("Accept reported success though nothing was sent")
	}
	if len(w.Offers()) != 1 {
		t.Error("an offer whose acceptance never went out was forgotten")
	}
	if err := o.Decline(context.Background()); err == nil {
		t.Error("Decline reported success though nothing was sent")
	}
	if len(w.Offers()) != 1 {
		t.Error("an offer whose refusal never went out was forgotten")
	}
	if len(f.Noted()) != 0 {
		t.Error("a friendship was recorded although the acceptance failed")
	}
}

// TestAnOfferFromSomebodyUnnamedTakesTheNameWeHave: a friendship offer
// does not always carry a name, and an offer listed as "" is one a
// person cannot pick out of the list to answer.
func TestAnOfferFromSomebodyUnnamedTakesTheNameWeHave(t *testing.T) {
	w, f := newFakeSession(t)

	// Nothing known about them yet: the shortened id stands in, which
	// at least names somebody.
	f.Relay(t, arrivingIM(somebodyElse, "", DialogFriendshipOffered, msg.UUID{1}, ""))
	o, ok := w.OfferFrom(somebodyElse)
	if !ok {
		t.Fatal("the offer was not kept")
	}
	if !strings.HasPrefix(o.Name, "(") {
		t.Errorf("an unnamed offer is filed as %q, want the id standing in", o.Name)
	}

	// Once the name has been heard, an unnamed offer uses it.
	f.Relay(t, arrivingIM(somebody, "Quark Idlemind", DialogMessage, msg.UUID{}, "hi"))
	f.Relay(t, arrivingIM(somebody, "", DialogFriendshipOffered, msg.UUID{2}, ""))
	if o, _ := w.OfferFrom(somebody); o.Name != "Quark Idlemind" {
		t.Errorf("offer name = %q, want the name learned earlier", o.Name)
	}

	// An inventory offer is filled in the same way, and matters more:
	// InventoryOffersFor looks the giver up by name, so an offer with
	// no giver's name on it cannot be answered that way at all.
	inv := arrivingIM(somebody, "", DialogInventoryOffered, msg.UUID{3}, "a box")
	inv.MessageBlock.BinaryBucket = offerBucket(AssetObject, msg.UUID{4})
	f.Relay(t, inv)
	if hits := w.InventoryOffersFor("Quark Idlemind"); len(hits) != 1 {
		t.Errorf("an unnamed inventory offer was found by its giver %d times", len(hits))
	} else if hits[0].FromName != "Quark Idlemind" {
		t.Errorf("the offer says it is from %q", hits[0].FromName)
	}
}

// TestTheFirstOfferBuildsTheMapItGoesIn: the maps holding offers are
// made on the way past rather than at setup -- New builds one of them
// and not the other -- so the first offer of either kind must not be
// dropped into a nil map.
func TestTheFirstOfferBuildsTheMapItGoesIn(t *testing.T) {
	// A session that has been through none of New's setup, which is
	// the worst case: both maps are nil.
	w := &Session{}

	w.instantMessage(nil, arrivingIM(somebody, "Quark Idlemind", DialogFriendshipOffered, msg.UUID{1}, ""))
	if len(w.Offers()) != 1 {
		t.Errorf("kept %d friendship offers, want 1", len(w.Offers()))
	}

	inv := arrivingIM(somebody, "Quark Idlemind", DialogInventoryOffered, msg.UUID{2}, "a box")
	inv.MessageBlock.BinaryBucket = offerBucket(AssetObject, msg.UUID{3})
	w.instantMessage(nil, inv)
	if len(w.InventoryOffers()) != 1 {
		t.Errorf("kept %d inventory offers, want 1", len(w.InventoryOffers()))
	}
}

// TestInventoryOfferFrom: the offer's substance is in the binary
// bucket, which is not self describing -- one byte of asset type and
// sixteen of id -- so a bucket that is too short has to be refused
// rather than read past the end of.
func TestInventoryOfferFrom(t *testing.T) {
	item := msg.MustParseUUID("106f7e57-7e57-c0de-3904-0bf24c089c63")

	cases := []struct {
		name   string
		im     IM
		want   bool
		asset  AssetType
		wantID msg.UUID
	}{
		{
			name: "an ordinary item",
			im:   IM{Dialog: DialogInventoryOffered, Bucket: offerBucket(AssetLSLText, item)},
			want: true, asset: AssetLSLText, wantID: item,
		}, {
			// A folder is offered as a category, and then the id is
			// the folder rather than an item in it.
			name: "a folder",
			im:   IM{Dialog: DialogInventoryOffered, Bucket: offerBucket(AssetCategory, item)},
			want: true, asset: AssetCategory, wantID: item,
		}, {
			// The type is signed, and -1 is the grid's way of saying
			// it does not know: read as a byte it would be 255.
			name: "a kind nobody named",
			im:   IM{Dialog: DialogInventoryOffered, Bucket: offerBucket(-1, item)},
			want: true, asset: -1, wantID: item,
		}, {
			name: "trailing rubbish after the id",
			im: IM{Dialog: DialogInventoryOffered,
				Bucket: append(offerBucket(AssetTexture, item), 'j', 'u', 'n', 'k')},
			want: true, asset: AssetTexture, wantID: item,
		}, {
			name: "a bucket one byte short",
			im:   IM{Dialog: DialogInventoryOffered, Bucket: offerBucket(AssetTexture, item)[:16]},
			want: false,
		}, {
			name: "no bucket at all",
			im:   IM{Dialog: DialogInventoryOffered},
			want: false,
		}, {
			name: "somebody merely talking",
			im:   IM{Dialog: DialogMessage, Bucket: offerBucket(AssetTexture, item)},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.im.At = time.Now()
			c.im.From = somebody
			c.im.FromName = "Quark Idlemind"
			c.im.Text = "a thing"
			c.im.ID = msg.UUID{7}

			o, ok := InventoryOfferFrom(&c.im)
			if ok != c.want {
				t.Fatalf("InventoryOfferFrom = %v, want %v", ok, c.want)
			}
			if !ok {
				return
			}
			if o.Asset != c.asset || o.Item != c.wantID {
				t.Errorf("offer is %s %s, want %s %s", o.Asset, o.Item, c.asset, c.wantID)
			}
			// The name is the message text: nothing else in the offer
			// says what is being given.
			if o.Name != "a thing" || o.FromName != "Quark Idlemind" || o.From != somebody {
				t.Errorf("offer = %+v", o)
			}
			if o.Transaction != (msg.UUID{7}) {
				t.Errorf("transaction = %s, want the message id", o.Transaction)
			}
		})
	}
}

// relayInventoryOffer relays one and returns what the session kept.
func relayInventoryOffer(t *testing.T, w *Session, f *fakeBackend,
	from msg.UUID, fromName, name string, txn msg.UUID) *InventoryOffer {
	t.Helper()
	m := arrivingIM(from, fromName, DialogInventoryOffered, txn, name)
	m.MessageBlock.BinaryBucket = offerBucket(AssetObject, msg.UUID{0xaa})
	f.Relay(t, m)

	hits := w.InventoryOffersFor(name)
	if len(hits) != 1 {
		t.Fatalf("the offer of %q was kept %d times, want once", name, len(hits))
	}
	return hits[0]
}

// TestAnInventoryOfferIsKeptUntilItIsAnswered: nothing arrives in
// inventory until the offer is accepted, and the acceptance is another
// instant message quoting the transaction the offer came with.  An
// answer with a fresh id is ignored and the offer stays open for ever.
func TestAnInventoryOfferIsKeptUntilItIsAnswered(t *testing.T) {
	w, f := newFakeSession(t)

	txn := msg.MustParseUUID("19cc7e57-7e57-c0de-4169-ae942a366a74")
	o := relayInventoryOffer(t, w, f, somebody, "Quark Idlemind", "a box of parts", txn)
	if o.Asset != AssetObject || o.Item != (msg.UUID{0xaa}) {
		t.Errorf("offer = %+v", o)
	}
	if got := o.String(); got != `Quark Idlemind offers "a box of parts"` {
		t.Errorf("String = %q", got)
	}

	if err := o.Accept(context.Background(), msg.UUID{}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	m := onlySent[*msg.ImprovedInstantMessage](t, f)
	if m.MessageBlock.Dialog != DialogInventoryAccepted {
		t.Errorf("answered with %s", DialogName(m.MessageBlock.Dialog))
	}
	if m.MessageBlock.ID != txn {
		t.Errorf("answer quotes %s, want the offer's %s", m.MessageBlock.ID, txn)
	}
	if m.MessageBlock.ToAgentID != somebody {
		t.Errorf("the answer went to %s", m.MessageBlock.ToAgentID)
	}
	// A zero folder means the default one for that kind of thing,
	// which is said by sending no bucket at all.
	if len(m.MessageBlock.BinaryBucket) != 0 {
		t.Errorf("a default folder sent a bucket of %d bytes", len(m.MessageBlock.BinaryBucket))
	}
	if len(w.InventoryOffers()) != 0 {
		t.Error("the offer is still waiting after being accepted")
	}
}

// TestAcceptingIntoAFolderSaysWhichOne: the folder rides in the binary
// bucket, which is the only way to say anything but "wherever this
// kind of thing goes".
func TestAcceptingIntoAFolderSaysWhichOne(t *testing.T) {
	w, f := newFakeSession(t)
	o := relayInventoryOffer(t, w, f, somebody, "Quark Idlemind", "a script", msg.UUID{4})

	into := msg.MustParseUUID("b70e7e57-7e57-c0de-ba76-bea374553205")
	if err := o.Accept(context.Background(), into); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	m := onlySent[*msg.ImprovedInstantMessage](t, f)
	if string(m.MessageBlock.BinaryBucket) != string(into[:]) {
		t.Errorf("bucket = %x, want the folder %s", m.MessageBlock.BinaryBucket, into)
	}
}

// TestDecliningAnInventoryOffer: saying so matters.  An offer left
// unanswered stays pending, and the giver is told nothing either way.
func TestDecliningAnInventoryOffer(t *testing.T) {
	w, f := newFakeSession(t)
	txn := msg.UUID{5}
	o := relayInventoryOffer(t, w, f, somebody, "Quark Idlemind", "a landmark", txn)

	if err := o.Decline(context.Background()); err != nil {
		t.Fatalf("Decline: %v", err)
	}
	m := onlySent[*msg.ImprovedInstantMessage](t, f)
	if m.MessageBlock.Dialog != DialogInventoryDeclined {
		t.Errorf("refused with %s", DialogName(m.MessageBlock.Dialog))
	}
	if m.MessageBlock.ID != txn {
		t.Errorf("refusal quotes %s, want %s", m.MessageBlock.ID, txn)
	}
	if len(w.InventoryOffers()) != 0 {
		t.Error("the offer is still waiting after being declined")
	}
}

// TestAnInventoryOfferAnsweredIntoTheVoidIsKept: the same rule as for
// friendship.  An answer that did not go leaves the offer unanswered,
// and the transaction id is not recoverable.
func TestAnInventoryOfferAnsweredIntoTheVoidIsKept(t *testing.T) {
	w, f := newFakeSession(t)
	o := relayInventoryOffer(t, w, f, somebody, "Quark Idlemind", "a notecard", msg.UUID{6})
	f.FailSends(errors.New("the circuit is gone"))

	if err := o.Accept(context.Background(), msg.UUID{}); err == nil {
		t.Error("Accept reported success though nothing was sent")
	}
	if len(w.InventoryOffers()) != 1 {
		t.Error("an offer whose acceptance never went out was forgotten")
	}
	if err := o.Decline(context.Background()); err == nil {
		t.Error("Decline reported success though nothing was sent")
	}
	if len(w.InventoryOffers()) != 1 {
		t.Error("an offer whose refusal never went out was forgotten")
	}
}

// waiting builds a session holding inventory offers, without a
// backend: what is being tested is the bookkeeping, and the arrival
// times have to be chosen rather than observed.
func waiting(offers ...*InventoryOffer) *Session {
	w := &Session{invOffers: map[msg.UUID]*InventoryOffer{}}
	for i, o := range offers {
		o.Transaction = msg.UUID{byte(i + 1)}
		w.invOffers[o.Transaction] = o
	}
	return w
}

// The order both kinds of offer must come back in.  They are held in
// a map, which has none, and a person answering "the offer" means the
// one that has been waiting longest.
const oldestFirst = "first,second,third,fourth"

// asking is how many times each of these looks.  A map hands its
// contents back in a different order every time it is ranged over, so
// asking once is a test that passes whether or not anything sorts
// them: the one arrangement that needs no sorting comes up often
// enough to be a green run.
const asking = 20

// TestInventoryOffersComeBackOldestFirst pins the order down for
// inventory offers.
func TestInventoryOffersComeBackOldestFirst(t *testing.T) {
	base := time.Now()
	w := waiting(
		&InventoryOffer{Name: "third", At: base.Add(2 * time.Second)},
		&InventoryOffer{Name: "first", At: base},
		&InventoryOffer{Name: "fourth", At: base.Add(3 * time.Second)},
		&InventoryOffer{Name: "second", At: base.Add(time.Second)},
	)
	for range asking {
		var got []string
		for _, o := range w.InventoryOffers() {
			got = append(got, o.Name)
		}
		if strings.Join(got, ",") != oldestFirst {
			t.Fatalf("offers came back %v", got)
		}
	}
}

// TestFriendshipOffersComeBackOldestFirst is the same for friendship
// offers, which are kept in their own map and sorted by their own
// copy of the same loop.
func TestFriendshipOffersComeBackOldestFirst(t *testing.T) {
	base := time.Now()
	w := &Session{offers: map[msg.UUID]*Offer{}}
	for i, o := range []*Offer{
		{Name: "third", At: base.Add(2 * time.Second)},
		{Name: "first", At: base},
		{Name: "fourth", At: base.Add(3 * time.Second)},
		{Name: "second", At: base.Add(time.Second)},
	} {
		o.From = msg.UUID{byte(i + 1)}
		w.offers[o.From] = o
	}
	for range asking {
		var got []string
		for _, o := range w.Offers() {
			got = append(got, o.Name)
		}
		if strings.Join(got, ",") != oldestFirst {
			t.Fatalf("offers came back %v", got)
		}
	}
}

// TestInventoryOffersForNarrowHowAPersonWould: a caller answering an
// offer names it the way it was said to them -- part of what it is
// called, or who sent it -- and every offer that could be meant comes
// back, because deciding what two of them mean is the caller's job and
// not this one's.
func TestInventoryOffersForNarrowHowAPersonWould(t *testing.T) {
	// The arrival times are set because a match of two comes back
	// oldest first, and offers are held in a map: two with the same
	// time would come back in whichever order the map felt like.
	base := time.Now()
	box := &InventoryOffer{Name: "A Big Box", FromName: "Quark Idlemind", At: base}
	script := &InventoryOffer{Name: "hovertext.lsl", FromName: "Someone Else",
		At: base.Add(time.Second)}
	// One whose name contains the other's whole name, which is what
	// makes part-of-a-name and the-whole-of-a-name two different
	// questions rather than one.
	lamp := &InventoryOffer{Name: "a lamp", FromName: "Someone Else", At: base}
	stand := &InventoryOffer{Name: "a lamp stand", FromName: "Quark Idlemind",
		At: base.Add(time.Second)}

	cases := []struct {
		name   string
		asking string
		have   []*InventoryOffer
		want   []string // the offers' names, oldest first
	}{
		{"the only one waiting", "", []*InventoryOffer{box}, []string{"A Big Box"}},
		{"which of two is not decided here", "", []*InventoryOffer{box, script},
			[]string{"A Big Box", "hovertext.lsl"}},
		{"none at all", "", nil, nil},
		{"by name", "A Big Box", []*InventoryOffer{box, script}, []string{"A Big Box"}},
		// An item's name is spelt as it is: the whole of it in another
		// case is another name, and is not found as part of this one.
		{"not by the whole name in another case", "a big BOX", []*InventoryOffer{box, script}, nil},
		{"by part of the name", "box", []*InventoryOffer{box, script}, []string{"A Big Box"}},
		{"by part of the name, any case", "BIG", []*InventoryOffer{box, script}, []string{"A Big Box"}},
		{"by who sent it", "Someone Else", []*InventoryOffer{box, script}, []string{"hovertext.lsl"}},
		{"by who sent it, any case", "someone else", []*InventoryOffer{box, script}, []string{"hovertext.lsl"}},
		{"by something nobody said", "trousers", []*InventoryOffer{box, script}, nil},
		// The whole of a name beats part of another, or an item called
		// the beginning of something else could never be asked for.
		{"the whole of a name that starts another", "a lamp",
			[]*InventoryOffer{lamp, stand}, []string{"a lamp"}},
		{"part of a name two of them share", "lamp",
			[]*InventoryOffer{lamp, stand}, []string{"a lamp", "a lamp stand"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := waiting(c.have...)
			var got []string
			for _, o := range w.InventoryOffersFor(c.asking) {
				got = append(got, o.Name)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("found %v, want %v", got, c.want)
			}
		})
	}
}

// TestSendIMComputesTheSessionBothEndsAgreeOn: the viewer derives the
// conversation id from the two agent ids rather than inventing one, so
// that both ends arrive at the same one without being told.  Getting
// it wrong does not stop the message but lands it in a conversation
// the other end thinks is new.
func TestSendIMComputesTheSessionBothEndsAgreeOn(t *testing.T) {
	w, f := newFakeSession(t)

	if err := w.SendIM(context.Background(), somebody, "hello"); err != nil {
		t.Fatalf("SendIM: %v", err)
	}
	m := onlySent[*msg.ImprovedInstantMessage](t, f)
	if m.MessageBlock.Dialog != DialogMessage {
		t.Errorf("sent as %s", DialogName(m.MessageBlock.Dialog))
	}
	if trimNul(m.MessageBlock.Message) != "hello" {
		t.Errorf("message = %q", trimNul(m.MessageBlock.Message))
	}
	if trimNul(m.MessageBlock.FromAgentName) != "Quark Idlemind" {
		t.Errorf("from = %q", trimNul(m.MessageBlock.FromAgentName))
	}
	if m.MessageBlock.ToAgentID != somebody {
		t.Errorf("to = %s", m.MessageBlock.ToAgentID)
	}
	if got := m.MessageBlock.ID; got != imSessionID(testAgentID, somebody) {
		t.Errorf("session id = %s, want the two ids exclusive ored", got)
	}

	// It has to be the same from either side, which is the whole point
	// of deriving it, and it must not be the zero id -- which a message
	// to oneself would get from the exclusive or, and which the viewer
	// replaces with the agent's own id (llimview.cpp:2551-2557).
	if imSessionID(testAgentID, somebody) != imSessionID(somebody, testAgentID) {
		t.Error("the two ends would compute different conversation ids")
	}
	if got := imSessionID(somebody, somebody); got != somebody {
		t.Errorf("a conversation with oneself is %s, want the agent's own id", got)
	}

	// Where it was sent from rides along, so the far end can offer a
	// teleport back to it.
	if m.MessageBlock.Position.X != 128 {
		t.Errorf("position = %+v, want the avatar's", m.MessageBlock.Position)
	}
	if m.MessageBlock.RegionID != testRegionID {
		t.Errorf("region = %s, want %s", m.MessageBlock.RegionID, testRegionID)
	}
	// Reliably, because there is no second chance at an instant
	// message and nothing tells the sender it was lost.
	if sent := f.Sent(); !sent[0].Reliable {
		t.Error("the instant message went unreliably")
	}

	// And one to oneself goes under the agent's own id.
	f.Forget()
	if err := w.SendIM(context.Background(), testAgentID, "a note to self"); err != nil {
		t.Fatalf("SendIM to oneself: %v", err)
	}
	if got := onlySent[*msg.ImprovedInstantMessage](t, f).MessageBlock.ID; got != testAgentID {
		t.Errorf("a message to oneself went as session %s, want %s", got, testAgentID)
	}
}

// TestAnIMFromNowhereStillGoesOut: neither the position nor the region
// is required, and neither is worth failing a message over, so a
// session that cannot find out where it is sends zeros rather than an
// error.
func TestAnIMFromNowhereStillGoesOut(t *testing.T) {
	t.Run("nothing knows where we are", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.presenceErr = errors.New("no presence")
		f.regionErr = errors.New("no region")

		if err := w.SendIM(context.Background(), somebody, "hello"); err != nil {
			t.Fatalf("SendIM: %v", err)
		}
		m := onlySent[*msg.ImprovedInstantMessage](t, f)
		if m.MessageBlock.Position != (msg.Vector3{}) {
			t.Errorf("position = %+v, want zeros", m.MessageBlock.Position)
		}
		if !m.MessageBlock.RegionID.IsZero() {
			t.Errorf("region = %s, want zero", m.MessageBlock.RegionID)
		}
	})

	t.Run("the handshake has not arrived", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.regionKnown = false

		if err := w.SendIM(context.Background(), somebody, "hello"); err != nil {
			t.Fatalf("SendIM: %v", err)
		}
		m := onlySent[*msg.ImprovedInstantMessage](t, f)
		if !m.MessageBlock.RegionID.IsZero() {
			t.Errorf("region = %s, want zero until the handshake", m.MessageBlock.RegionID)
		}
	})
}

// TestOfferFriendshipHasWordsWhenGivenNone: the message is what the
// other end sees in the dialog, and an empty one is a dialog that does
// not say who is asking or why.
func TestOfferFriendshipHasWordsWhenGivenNone(t *testing.T) {
	w, f := newFakeSession(t)

	if err := w.OfferFriendship(context.Background(), somebody, ""); err != nil {
		t.Fatalf("OfferFriendship: %v", err)
	}
	m := onlySent[*msg.ImprovedInstantMessage](t, f)
	if m.MessageBlock.Dialog != DialogFriendshipOffered {
		t.Errorf("sent as %s", DialogName(m.MessageBlock.Dialog))
	}
	if got := trimNul(m.MessageBlock.Message); got == "" {
		t.Error("an offer with no words went out")
	}

	f.Forget()
	if err := w.OfferFriendship(context.Background(), somebody, "we have met"); err != nil {
		t.Fatalf("OfferFriendship: %v", err)
	}
	m = onlySent[*msg.ImprovedInstantMessage](t, f)
	if got := trimNul(m.MessageBlock.Message); got != "we have met" {
		t.Errorf("message = %q", got)
	}
}

// TestBeingAcceptedIsRecorded: the side that offered is told by
// instant message and nothing else in time to be useful, so the
// session takes that as the friendship having formed.
func TestBeingAcceptedIsRecorded(t *testing.T) {
	_, f := newFakeSession(t)
	f.Relay(t, arrivingIM(somebody, "Quark Idlemind", DialogFriendshipAccepted, msg.UUID{}, ""))

	noted := f.Noted()
	if len(noted) != 1 || noted[0].ID != somebody || !noted[0].Online {
		t.Fatalf("NoteFriend saw %+v", noted)
	}
	// Being refused is not, since there is nothing to record.
	f.Relay(t, arrivingIM(somebodyElse, "Someone Else", DialogFriendshipDeclined, msg.UUID{}, ""))
	if len(f.Noted()) != 1 {
		t.Errorf("a refusal was recorded as a friendship: %+v", f.Noted())
	}
}

// TestTheFriendListIsNamed: the grid's friend list is uuids, which is
// not a list anybody can read, so the names are asked for and filled
// in -- and one the grid will not name still appears, shortened,
// rather than the whole list failing.
func TestTheFriendListIsNamed(t *testing.T) {
	w, f := newFakeSession(t)
	nameless := msg.MustParseUUID("03717e57-7e57-c0de-f677-52aa7cece51e")
	f.friends = []Friend{
		{ID: somebody, Online: true},
		{ID: somebodyElse, Online: false},
		{ID: nameless, Online: true},
	}
	f.AnswerNames(t, map[msg.UUID]string{
		somebody:     "Quark Idlemind",
		somebodyElse: "Someone Else",
	})

	// A friend the grid never names holds the call for its whole name
	// timeout, three seconds, so the wait is cut short here the way a
	// caller in a hurry would cut it short: by the context.  What is
	// being tested is that the list still arrives, not how long it is
	// willing to wait for a name.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	all, err := w.FriendList(ctx)
	if err != nil {
		t.Fatalf("FriendList: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("the list has %d, want 3", len(all))
	}
	if all[0].Name != "Quark Idlemind" || all[1].Name != "Someone Else" {
		t.Errorf("names = %+v", all)
	}
	if !strings.HasPrefix(all[2].Name, "(") {
		t.Errorf("the one nobody named is %q, want the id standing in", all[2].Name)
	}

	// Online is a question about the moment, not about the list, so
	// the offline friend is not in the answer at all.
	online, err := w.OnlineFriends(ctx)
	if err != nil {
		t.Fatalf("OnlineFriends: %v", err)
	}
	if len(online) != 2 {
		t.Fatalf("online has %d, want 2: %+v", len(online), online)
	}
	for _, p := range online {
		if p.ID == somebodyElse {
			t.Error("somebody logged out is in the online list")
		}
	}
}

// TestTheIdsOfTheFriendListCostNoNames: a caller that only has to ask
// whether an id it already holds is a friend must not pay for the
// names it will never print.
//
// The names are worth up to three seconds of waiting for a listing
// somebody is going to read, and worth nothing at all to slsh's "map",
// which colours its friends in on every picture.  So this asks the
// wire: no name was requested, and the friend the grid would never
// have named is in the answer like everybody else.
func TestTheIdsOfTheFriendListCostNoNames(t *testing.T) {
	w, f := newFakeSession(t)
	nameless := msg.MustParseUUID("03717e57-7e57-c0de-f677-52aa7cece51e")
	f.friends = []Friend{
		{ID: somebody, Online: true},
		{ID: nameless, Online: false},
	}

	ids, err := w.FriendIDs(context.Background())
	if err != nil {
		t.Fatalf("FriendIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != somebody || ids[1] != nameless {
		t.Errorf("the ids are %v, want %v and %v", ids, somebody, nameless)
	}
	for _, s := range f.Sent() {
		if q, ok := s.Msg.(*msg.UUIDNameRequest); ok {
			t.Errorf("a name was asked for: %+v", q)
		}
	}
}

// TestTheFriendListSaysWhenItCannotBeHad: the list comes from whoever
// holds the session, so it can fail, and a failure must not look like
// an avatar with no friends.
func TestTheFriendListSaysWhenItCannotBeHad(t *testing.T) {
	w, f := newFakeSession(t)
	f.friendsErr = errors.New("the daemon is not answering")

	if _, err := w.FriendList(context.Background()); err == nil {
		t.Error("FriendList reported no friends rather than a failure")
	}
	if _, err := w.FriendIDs(context.Background()); err == nil {
		t.Error("FriendIDs reported no friends rather than a failure")
	}
	if _, err := w.OnlineFriends(context.Background()); err == nil {
		t.Error("OnlineFriends reported nobody rather than a failure")
	}
}

// TestIMSubscriptionsCloseWhenTheSessionEnds: when the session ends
// every subscription must close, so a caller ranging over one stops
// ranging rather than waiting for ever, and a subscription taken out
// after the end comes back already closed rather than silent.
//
// Chat and permission subscriptions always did -- see
// TestChatClosesWhenReaderStops -- and IM ones did not: closeChat
// closed chatSubs and permSubs and walked past imSubs, and IMs had
// none of the "the reader has already stopped" guard Chat had.  So an
// IM subscription outlived the session it belonged to and blocked
// whoever was ranging over it, for ever.
func TestIMSubscriptionsCloseWhenTheSessionEnds(t *testing.T) {
	w, f := newFakeSession(t)
	ims := w.IMs(4)
	f.Close()

	select {
	case _, open := <-ims:
		if open {
			t.Error("the subscription delivered a message, want closed")
		}
	case <-time.After(2 * time.Second):
		t.Error("the session ended and the IM subscription was not closed")
	}

	late := w.IMs(4)
	select {
	case _, open := <-late:
		if open {
			t.Error("a late subscription delivered a message")
		}
	case <-time.After(2 * time.Second):
		t.Error("a subscription taken out after the session ended was not closed")
	}
}
