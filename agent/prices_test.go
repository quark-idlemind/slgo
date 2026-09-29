package agent

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	feeGroup = msg.MustParseUUID("39fd7e57-7e57-c0de-badc-3904de59fac6")
	aTxn     = msg.MustParseUUID("97037e57-7e57-c0de-40d5-3d3c41520645")
	anObject = msg.MustParseUUID("2a6d7e57-7e57-c0de-5086-897db8087c01")
)

// inviteIM is a group invitation as the simulator sends one: the group
// is the sender, and the bucket holds the fee and a role.
func inviteIM(group, txn msg.UUID, fee int32, bucket []byte) *msg.ImprovedInstantMessage {
	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID = group
	m.MessageBlock.Dialog = dialogGroupInvitation
	m.MessageBlock.ID = txn
	m.MessageBlock.FromAgentName = []byte("example.inviter\x00")
	m.MessageBlock.Message = []byte("join us\x00")
	if bucket == nil {
		bucket = make([]byte, inviteBucketLen)
		binary.BigEndian.PutUint32(bucket, uint32(fee))
	}
	m.MessageBlock.BinaryBucket = bucket
	return m
}

func TestAnObjectsSaleIsKnownWhenTheRegionHasSaidWhoOwnsIt(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	if _, _, ok := a.ObjectSale(55); ok {
		t.Fatal("an object nobody described has a sale")
	}
	o := a.Objects()
	o.mu.Lock()
	o.seen(anObject).Local = 55
	o.mu.Unlock()
	if _, _, ok := a.ObjectSale(55); ok {
		t.Error("an object with no owner known has a sale")
	}

	f := &msg.ObjectPropertiesFamily{}
	f.ObjectData.ObjectID = anObject
	f.ObjectData.OwnerID = somebody
	f.ObjectData.Name = []byte("a lamp\x00")
	f.ObjectData.Description = []byte("\x00")
	f.ObjectData.SalePrice = 12
	feed(t, a, f)
	if owner, price, ok := a.ObjectSale(55); !ok || owner != somebody || price != 12 {
		t.Errorf("owner %v, price %d, %v", owner, price, ok)
	}
}

func TestAnObjectsPriceIsMinusOneUntilTheRegionSaysIt(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	o := a.Objects()
	o.mu.Lock()
	v := o.seen(anObject)
	v.Local, v.Owner = 55, somebody // as a compressed update carries it
	o.mu.Unlock()
	if owner, price, ok := a.ObjectSale(55); !ok || owner != somebody || price != -1 {
		t.Errorf("owner %v, price %d, %v; want the owner and no price", owner, price, ok)
	}
}

func TestAParcelSaleIsKnownOnlyForTheParcelUnderfoot(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	if _, _, _, ok := a.ParcelSale(7); ok {
		t.Fatal("a parcel nobody described has a sale")
	}
	a.parcels.note(&Parcel{LocalID: 7, Owner: somebody, SalePrice: 40, PassPrice: 3})
	if owner, sale, pass, ok := a.ParcelSale(7); !ok || owner != somebody || sale != 40 || pass != 3 {
		t.Errorf("owner %v, sale %d, pass %d, %v", owner, sale, pass, ok)
	}
	if _, _, _, ok := a.ParcelSale(8); ok {
		t.Error("another parcel of the region has a sale")
	}
}

func TestAGroupsFeeIsKnownOnceItsProfileIsHeard(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	if _, ok := a.GroupFee(feeGroup); ok {
		t.Fatal("a fee is known for a group nobody described")
	}
	p := &msg.GroupProfileReply{}
	p.GroupData.GroupID = feeGroup
	p.GroupData.MembershipFee = 25
	feed(t, a, p)
	if fee, ok := a.GroupFee(feeGroup); !ok || fee != 25 {
		t.Errorf("fee %d, %v", fee, ok)
	}
}

func TestAnInvitationsFeeIsKnownByItsTransaction(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	other := msg.MustParseUUID("97be7e57-7e57-c0de-074b-c52ef17daa27")
	feed(t, a, inviteIM(feeGroup, aTxn, 50, nil))
	feed(t, a, inviteIM(feeGroup, other, 5, nil))

	if fee, ok := a.InvitationFee(feeGroup, aTxn); !ok || fee != 50 {
		t.Errorf("the first invitation: %d, %v", fee, ok)
	}
	if fee, ok := a.InvitationFee(feeGroup, other); !ok || fee != 5 {
		t.Errorf("the second: %d, %v", fee, ok)
	}
	if fee, ok := a.InvitationFee(feeGroup, msg.UUID{}); !ok || fee != 5 {
		t.Errorf("the latest into the group: %d, %v", fee, ok)
	}
	if _, ok := a.InvitationFee(feeGroup, msg.MustParseUUID("12b57e57-7e57-c0de-efe3-b327af5dfe62")); ok {
		t.Error("an invitation nobody heard has a fee")
	}
	if _, ok := a.InvitationFee(somebody, msg.UUID{}); ok {
		t.Error("a group nobody invited from has a fee")
	}
}

// A bucket of any other shape says nothing: a fee guessed at is money.
func TestABucketOfTheWrongShapeIsNoFee(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	feed(t, a, inviteIM(feeGroup, aTxn, 0, make([]byte, 7)))
	if fee, ok := a.InvitationFee(feeGroup, aTxn); ok {
		t.Errorf("a malformed bucket gave a fee of %d", fee)
	}
	// Nor is another kind of message taken for one.
	m := inviteIM(feeGroup, aTxn, 9, nil)
	m.MessageBlock.Dialog = 0
	feed(t, a, m)
	if _, ok := a.InvitationFee(feeGroup, aTxn); ok {
		t.Error("an instant message that is no invitation gave a fee")
	}
}

func TestOnlyTheLatestInvitationsAreKept(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	for i := 0; i < keptInvitations+5; i++ {
		txn := aTxn
		txn[15] = byte(i)
		feed(t, a, inviteIM(feeGroup, txn, int32(i), nil))
	}
	first := aTxn
	first[15] = 0
	if _, ok := a.InvitationFee(feeGroup, first); ok {
		t.Error("the oldest invitation was kept past the bound")
	}
	last := aTxn
	last[15] = byte(keptInvitations + 4)
	if fee, ok := a.InvitationFee(feeGroup, last); !ok || fee != keptInvitations+4 {
		t.Errorf("the newest: %d, %v", fee, ok)
	}
}

func nameReply(id msg.UUID, first, last string) *msg.UUIDNameReply {
	r := &msg.UUIDNameReply{}
	r.UUIDNameBlock = []msg.UUIDNameReply_UUIDNameBlock{{ID: id, FirstName: []byte(first + "\x00"), LastName: []byte(last + "\x00")}}
	return r
}

func TestANameHeardIsNotAskedForAgain(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	feed(t, a, nameReply(somebody, "Example", "Payee"))
	if got := a.NameOf(context.Background(), somebody); got != "Example Payee" {
		t.Errorf("name %q", got)
	}
	for _, m := range w.messages(t) {
		if _, ok := m.(*msg.UUIDNameRequest); ok {
			t.Error("a name already heard was asked for")
		}
	}
}

func TestANameIsAskedForAndTheAnswerFound(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	got := make(chan string, 1)
	go func() { got <- a.NameOf(context.Background(), somebody) }()

	deadline := time.Now().Add(2 * time.Second)
	for asked := false; !asked; {
		for _, m := range w.messages(t) {
			if q, ok := m.(*msg.UUIDNameRequest); ok && len(q.UUIDNameBlock) == 1 && q.UUIDNameBlock[0].ID == somebody {
				asked = true
			}
		}
		if !asked {
			if time.Now().After(deadline) {
				t.Fatal("no UUIDNameRequest went out")
			}
			time.Sleep(time.Millisecond)
		}
	}
	feed(t, a, nameReply(somebody, "Example", "Payee"))
	select {
	case n := <-got:
		if n != "Example Payee" {
			t.Errorf("name %q", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the answer was not found")
	}
}

func TestANameNobodyAnswersIsNoNameAndIsAskedForAgain(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if got := a.NameOf(ctx, somebody); got != "" {
		t.Errorf("name %q from nobody", got)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	a.NameOf(ctx2, somebody)
	n := 0
	for _, m := range w.messages(t) {
		if _, ok := m.(*msg.UUIDNameRequest); ok {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d requests for two questions", n)
	}
}

func TestACapabilityRequestedByItsURLIsFoundByName(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	caps := Caps{"AcceptGroupInvite": "https://sim.invalid/cap/abc", "Other": "https://sim.invalid/cap/def"}
	a.caps.Store(&caps)
	for _, c := range []struct {
		r    CapRequest
		want string
	}{
		{CapRequest{Cap: "Other"}, "Other"},
		{CapRequest{URL: "https://sim.invalid/cap/abc"}, "AcceptGroupInvite"},
		{CapRequest{URL: "https://sim.invalid/cap/abc/x"}, "AcceptGroupInvite"},
		{CapRequest{URL: "https://elsewhere.invalid/"}, ""},
		{CapRequest{}, ""},
	} {
		if got := a.CapOf(c.r); got != c.want {
			t.Errorf("%+v is %q, want %q", c.r, got, c.want)
		}
	}
}
