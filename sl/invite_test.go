package sl

// Group invitations, which are an instant message wearing a disguise.
//
// Two of the disguises are what these tests are for.  The sender id is
// the GROUP and not the avatar who invited, so an invitation filed
// under the wrong key answers the wrong thing or nothing at all; and
// the fee is in the binary bucket, where reading it wrong is money.
// Neither can be checked against a live grid from here -- no avatar
// this daemon holds has the power to invite another, so no real
// invitation can be made to arrive -- which is exactly why the shape
// the viewer's source describes is pinned down here instead.

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	testGroupID = msg.MustParseUUID("cc4b7e57-7e57-c0de-0d0f-49dc46c716e1")
	testRoleID  = msg.MustParseUUID("d45a7e57-7e57-c0de-029f-bc28b0fb6f92")
)

// invitingIM is an invitation as the simulator sends one: from the
// group, naming whoever invited in the agent-name field, with the fee
// and the role in the bucket.
func invitingIM(group msg.UUID, by string, txn msg.UUID, text string, fee int32, role msg.UUID) *msg.ImprovedInstantMessage {
	m := arrivingIM(group, by, DialogGroupInvitation, txn, text)
	m.MessageBlock.FromGroup = true
	m.MessageBlock.BinaryBucket = inviteBucketBytes(fee, role)
	return m
}

// inviteBucketBytes builds the bucket llimprocessing.cpp:1502-1518
// reads: an S32 fee in network byte order, then the role id.
func inviteBucketBytes(fee int32, role msg.UUID) []byte {
	b := binary.BigEndian.AppendUint32(make([]byte, 0, inviteBucketLen), uint32(fee))
	return append(b, role[:]...)
}

// TestAnInvitationIsFiledUnderTheGroupThatSentIt.
//
// The sender of a group invitation is the group itself, which is the
// one fact the whole of invite.go rests on: it is where the answer
// goes.  Filing it under the inviter -- the obvious reading, and what
// every other kind of instant message means by its sender -- would
// send the answer to a person who is not listening for one, and the
// invitation would sit open for ever.
func TestAnInvitationIsFiledUnderTheGroupThatSentIt(t *testing.T) {
	w, f := newFakeSession(t)

	txn := msg.MustParseUUID("cff37e57-7e57-c0de-1b1a-1bd6b87b0dbd")
	f.Relay(t, invitingIM(testGroupID, "quark.idlemind", txn, "join us", 0, testRoleID))

	is := w.Invitations()
	if len(is) != 1 {
		t.Fatalf("kept %d invitations, want 1", len(is))
	}
	i := is[0]
	if i.Group != testGroupID {
		t.Errorf("the invitation is from %s, want the group %s", i.Group, testGroupID)
	}
	if i.By != "quark.idlemind" || i.Text != "join us" || i.Transaction != txn {
		t.Errorf("invitation = %+v", i)
	}
	if i.Role != testRoleID {
		t.Errorf("role = %s, want %s", i.Role, testRoleID)
	}
	if !i.Stated || i.Fee != 0 {
		t.Errorf("fee = %d, stated %v; want a stated 0", i.Fee, i.Stated)
	}

	if err := w.AcceptInvitation(context.Background(), i); err != nil {
		t.Fatalf("AcceptInvitation: %v", err)
	}
	m := onlySent[*msg.ImprovedInstantMessage](t, f)
	if m.MessageBlock.ToAgentID != testGroupID {
		t.Errorf("the answer went to %s, want the group %s", m.MessageBlock.ToAgentID, testGroupID)
	}
	if m.MessageBlock.Dialog != DialogGroupInvitationAccept {
		t.Errorf("the answer is dialog %d, want %d", m.MessageBlock.Dialog, DialogGroupInvitationAccept)
	}
	if m.MessageBlock.ID != txn {
		t.Errorf("the answer quoted %s, want the invitation's %s", m.MessageBlock.ID, txn)
	}
	if m.AgentData.AgentID != testAgentID || m.AgentData.SessionID != testSessionID {
		t.Errorf("the answer came from %+v", m.AgentData)
	}
	if len(w.Invitations()) != 0 {
		t.Error("the invitation is still waiting after being accepted")
	}
}

// TestDecliningAnInvitationTellsTheGroup: an invitation left
// unanswered stays open on the other side, and the group's officers
// are shown a list of people who have not replied.
func TestDecliningAnInvitationTellsTheGroup(t *testing.T) {
	w, f := newFakeSession(t)

	txn := msg.MustParseUUID("cff37e57-7e57-c0de-1b1a-1bd6b87b0dbd")
	f.Relay(t, invitingIM(testGroupID, "quark.idlemind", txn, "", 0, testRoleID))

	is := w.Invitations()
	if len(is) != 1 {
		t.Fatalf("kept %d invitations, want 1", len(is))
	}
	if err := w.DeclineInvitation(context.Background(), is[0]); err != nil {
		t.Fatalf("DeclineInvitation: %v", err)
	}
	m := onlySent[*msg.ImprovedInstantMessage](t, f)
	if m.MessageBlock.Dialog != DialogGroupInvitationDecline {
		t.Errorf("the refusal is dialog %d, want %d", m.MessageBlock.Dialog, DialogGroupInvitationDecline)
	}
	if m.MessageBlock.ToAgentID != testGroupID || m.MessageBlock.ID != txn {
		t.Errorf("the refusal went to %s quoting %s", m.MessageBlock.ToAgentID, m.MessageBlock.ID)
	}
	if len(w.Invitations()) != 0 {
		t.Error("the invitation is still waiting after being declined")
	}
}

// TestTheFeeAndTheRoleComeOutOfTheBucket, in the shape the viewer
// parses (llimprocessing.cpp:1502-1518) and in network byte order,
// which is the one field in the message that is not little-endian.
// Reading the fee off the wrong end of the number turns L$50 into
// L$838860800, and reading it out of a bucket of the wrong shape turns
// anything at all into whatever the first four bytes happen to say.
func TestTheFeeAndTheRoleComeOutOfTheBucket(t *testing.T) {
	w, f := newFakeSession(t)

	txn := msg.MustParseUUID("cff37e57-7e57-c0de-1b1a-1bd6b87b0dbd")
	f.Relay(t, invitingIM(testGroupID, "quark.idlemind", txn, "join us", 50, testRoleID))

	is := w.Invitations()
	if len(is) != 1 {
		t.Fatalf("kept %d invitations, want 1", len(is))
	}
	i := is[0]
	if !i.Stated || i.Fee != 50 {
		t.Errorf("fee = %d, stated %v; want a stated 50", i.Fee, i.Stated)
	}
	if i.Role != testRoleID {
		t.Errorf("role = %s, want %s", i.Role, testRoleID)
	}
	if got := i.String(); !strings.Contains(got, "L$50") {
		t.Errorf("String = %q, and should say what it costs", got)
	}
}

// TestABucketOfTheWrongShapeIsAFeeNobodySaid.
//
// The viewer drops an invitation whose bucket is not exactly twenty
// bytes, and the comment beside the dialog in llinstantmessage.h:52-59
// describes a different shape again -- so a bucket this does not
// recognise is not hypothetical, it is what an older simulator would
// send.  Treating it as a fee of zero would be the one wrong guess
// that costs money, and dropping the invitation would leave a person
// invited with nothing to answer, so it is kept with the fee marked as
// never stated.
func TestABucketOfTheWrongShapeIsAFeeNobodySaid(t *testing.T) {
	for _, bucket := range [][]byte{
		// The shape llinstantmessage.h describes: "M" for member, then
		// the cost in decimal, null terminated.
		[]byte("M0\x00"),
		// Nothing at all, which is what a simulator that does not send
		// the bucket would give.
		nil,
		// Longer than the viewer accepts.  A length checked with "at
		// least" rather than "exactly" would read the first four bytes
		// of this as a fee of nothing and join for free, which is the
		// wrong guess in the direction that costs money.
		make([]byte, inviteBucketLen+4),
	} {
		w, f := newFakeSession(t)

		m := arrivingIM(testGroupID, "quark.idlemind", DialogGroupInvitation, msg.UUID{7}, "join us")
		m.MessageBlock.FromGroup = true
		m.MessageBlock.BinaryBucket = bucket
		f.Relay(t, m)

		is := w.Invitations()
		if len(is) != 1 {
			t.Fatalf("%d bytes of bucket kept %d invitations, want 1", len(bucket), len(is))
		}
		i := is[0]
		if i.Stated {
			t.Errorf("%d bytes of bucket was read as a fee anyway", len(bucket))
		}
		if i.Fee != 0 || !i.Role.IsZero() {
			t.Errorf("%d bytes of bucket gave fee %d, role %s", len(bucket), i.Fee, i.Role)
		}
		if got := i.String(); !strings.Contains(got, "did not say") {
			t.Errorf("String = %q, and should say the cost is unknown", got)
		}
	}
}

// TestASecondInvitationFromTheSameGroupReplacesTheFirst: the second
// one carries a transaction of its own, and an answer quoting the
// stale one answers nothing.  Two lines for one group would also have
// a person answering the one that cannot work.
func TestASecondInvitationFromTheSameGroupReplacesTheFirst(t *testing.T) {
	w, f := newFakeSession(t)

	first := msg.MustParseUUID("16fe7e57-7e57-c0de-9c05-cd001fefa0d8")
	second := msg.MustParseUUID("225b7e57-7e57-c0de-fb4e-9d7ea8050e7a")
	f.Relay(t, invitingIM(testGroupID, "quark.idlemind", first, "join us", 0, testRoleID))
	f.Relay(t, invitingIM(testGroupID, "hobb.idlemind", second, "really, join us", 0, testRoleID))

	is := w.Invitations()
	if len(is) != 1 {
		t.Fatalf("kept %d invitations for one group, want 1", len(is))
	}
	if is[0].Transaction != second {
		t.Errorf("kept the transaction %s, want the newer %s", is[0].Transaction, second)
	}

	// A different group is a different invitation, and both wait.
	other := msg.MustParseUUID("eb847e57-7e57-c0de-dabb-98143de2e974")
	f.Relay(t, invitingIM(other, "quark.idlemind", msg.UUID{3}, "and us", 0, testRoleID))
	if is := w.Invitations(); len(is) != 2 {
		t.Fatalf("two groups gave %d invitations, want 2", len(is))
	}
	// Oldest first, which is the order every other waiting thing comes
	// back in and what the shell numbers by.
	if is := w.Invitations(); is[0].Group != testGroupID || is[1].Group != other {
		t.Errorf("out of order: %s then %s", is[0].Group, is[1].Group)
	}
}

// TestAnInvitationAnsweredIntoTheVoidIsKept: if the answer never went,
// the invitation has not been answered, and forgetting it would throw
// away the only transaction that can ever answer it.
func TestAnInvitationAnsweredIntoTheVoidIsKept(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, invitingIM(testGroupID, "quark.idlemind", msg.UUID{9}, "join us", 0, testRoleID))
	f.FailSends(errors.New("the circuit is gone"))

	is := w.Invitations()
	if len(is) != 1 {
		t.Fatalf("kept %d invitations, want 1", len(is))
	}
	if err := w.AcceptInvitation(context.Background(), is[0]); err == nil {
		t.Error("AcceptInvitation reported success though nothing was sent")
	}
	if len(w.Invitations()) != 1 {
		t.Error("an invitation whose acceptance never went out was forgotten")
	}
	if err := w.DeclineInvitation(context.Background(), is[0]); err == nil {
		t.Error("DeclineInvitation reported success though nothing was sent")
	}
	if len(w.Invitations()) != 1 {
		t.Error("an invitation whose refusal never went out was forgotten")
	}
}

// TestForgettingAnInvitationSendsNothing, which is what a person does
// by closing the dialog: the group is told nothing and the invitation
// stops being offered here.
func TestForgettingAnInvitationSendsNothing(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, invitingIM(testGroupID, "quark.idlemind", msg.UUID{9}, "join us", 0, testRoleID))

	is := w.Invitations()
	if len(is) != 1 {
		t.Fatalf("kept %d invitations, want 1", len(is))
	}
	w.ForgetInvitation(is[0])
	w.ForgetInvitation(nil)
	if len(w.Invitations()) != 0 {
		t.Error("a forgotten invitation is still waiting")
	}
	if len(f.Sent()) != 0 {
		t.Errorf("forgetting sent %d messages", len(f.Sent()))
	}
	if err := w.AcceptInvitation(context.Background(), nil); err == nil {
		t.Error("accepting nothing reported success")
	}
	if err := w.DeclineInvitation(context.Background(), nil); err == nil {
		t.Error("declining nothing reported success")
	}
}

// TestAnInvitationFromNobodyIsNotKept: the sender is the group and the
// group is where the answer goes, so an invitation with no sender is
// one nobody could ever answer.  Keeping it would offer a person a
// choice that does nothing.
func TestAnInvitationFromNobodyIsNotKept(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, invitingIM(msg.UUID{}, "quark.idlemind", msg.UUID{9}, "join us", 0, testRoleID))
	if is := w.Invitations(); len(is) != 0 {
		t.Errorf("kept %d unanswerable invitations", len(is))
	}
}

// TestAnInvitationDoesNotTeachTheGroupAPersonsName.
//
// Every other instant message carries a person in its sender and that
// person's name beside it, and the session learns the pair as it goes.
// An invitation carries a group in the sender and the INVITER's name
// beside it, so learning that pair files a person's name under a
// group's id -- after which anything asking what that group is called
// is told the name of whoever happened to invite.
func TestAnInvitationDoesNotTeachTheGroupAPersonsName(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, invitingIM(testGroupID, "quark.idlemind", msg.UUID{9}, "join us", 0, testRoleID))

	if len(w.Invitations()) != 1 {
		t.Fatal("the invitation was not kept")
	}
	if got := w.Name(testGroupID); got != "" {
		t.Errorf("the group is now called %q, which is whoever invited", got)
	}
}

// Sending one, which nothing answers.
//
// The measurements behind these are from Agni on 2026-09-02, one
// avatar inviting another into a group made for the purpose: the
// invitation arrived at the far end as the instant message the rest of
// this file parses, and nothing at all came back to the sender.  So
// what is checked here is what went onto the wire and what was refused
// before anything did.

// testInvitee is somebody to invite, and is not a group.
var testInvitee = msg.MustParseUUID("8f947e57-7e57-c0de-8cf0-54e10ee32eb6")

// invitingSession is a session whose avatar belongs to one group with
// the power to invite and one without, which is the arrangement every
// refusal below is about.  Measured on Agni: an owner's membership
// carries every bit and an ordinary member's carries 0x0000080018010000,
// which does not include this one.
func invitingSession(t *testing.T, powers uint64) (*Session, *fakeBackend) {
	t.Helper()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.Groups = []Group{{ID: testGroupID, Name: "Example Builders", Powers: powers}}
	f.mu.Unlock()
	return w, f
}

// TestInviteToGroupSendsTheGroupTheRoleAndThePeople: the message is
// what the far end's invitation is made out of, so every field of it is
// somebody else's experience of this command.
func TestInviteToGroupSendsTheGroupTheRoleAndThePeople(t *testing.T) {
	t.Parallel()
	w, f := invitingSession(t, GroupPowerInvite)

	if err := w.InviteToGroup(context.Background(), testGroupID, RoleEveryone, testInvitee); err != nil {
		t.Fatalf("InviteToGroup: %v", err)
	}
	m := onlySent[*msg.InviteGroupRequest](t, f)
	if m.GroupData.GroupID != testGroupID {
		t.Errorf("the invitation is into group %v", m.GroupData.GroupID)
	}
	if len(m.InviteData) != 1 {
		t.Fatalf("%d people were invited, want 1", len(m.InviteData))
	}
	if m.InviteData[0].InviteeID != testInvitee {
		t.Errorf("it invites %v", m.InviteData[0].InviteeID)
	}
	if !m.InviteData[0].RoleID.IsZero() {
		t.Errorf("the everyone role went out as %v, and it is the null id",
			m.InviteData[0].RoleID)
	}
	if m.AgentData.AgentID != testAgentID || m.AgentData.SessionID != testSessionID {
		t.Error("the request did not carry this session's ids")
	}
}

// TestInviteToGroupTakesSeveralInOneMessage: the block is a list on the
// wire, and inviting three people is one message rather than three.
func TestInviteToGroupTakesSeveralInOneMessage(t *testing.T) {
	t.Parallel()
	w, f := invitingSession(t, GroupPowerInvite)

	who := []msg.UUID{testInvitee, msg.UUID{7}, msg.UUID{8}}
	if err := w.InviteToGroup(context.Background(), testGroupID, RoleEveryone, who...); err != nil {
		t.Fatalf("InviteToGroup: %v", err)
	}
	if m := onlySent[*msg.InviteGroupRequest](t, f); len(m.InviteData) != len(who) {
		t.Errorf("%d of the three went out", len(m.InviteData))
	}
}

// TestInviteToGroupRefusesWhatTheGridWouldAnswerWithSilence.
//
// Nothing replies to this message, so an invitation that was never
// allowed looks exactly like one that went.  Both refusals are
// therefore made here, out of the membership list the session already
// holds, and neither of them puts anything on the wire.
func TestInviteToGroupRefusesWhatTheGridWouldAnswerWithSilence(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		why    string
		powers uint64
		group  msg.UUID
		says   string
	}{
		{"a member whose role cannot invite", 0x0000080018010000, testGroupID,
			"does not carry the power"},
		{"not a member of it at all", GroupPowerInvite, msg.UUID{4},
			"not a member"},
	} {
		w, f := invitingSession(t, c.powers)
		err := w.InviteToGroup(context.Background(), c.group, RoleEveryone, testInvitee)
		if !errors.Is(err, ErrCannotInvite) {
			t.Errorf("%s: InviteToGroup = %v, want a refusal", c.why, err)
		}
		if err != nil && !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: the refusal %q does not say which of the two it is", c.why, err)
		}
		if got := sentOf[*msg.InviteGroupRequest](f); len(got) != 0 {
			t.Errorf("%s: the invitation went out anyway: %s", c.why, f.describe())
		}
	}
}

// TestInviteToGroupTriesWhenTheListHasNotArrived.
//
// Nothing asks for the membership list: it arrives unasked shortly
// after login, so an empty one is "not told yet" exactly as much as it
// is "belongs to none".  Refusing on it would make this command
// useless in the seconds after a session comes up, and the grid's own
// answer to an invitation nobody was allowed to make is silence, which
// is no worse than the refusal would have been.
func TestInviteToGroupTriesWhenTheListHasNotArrived(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	if err := w.InviteToGroup(context.Background(), testGroupID, RoleEveryone, testInvitee); err != nil {
		t.Fatalf("InviteToGroup: %v", err)
	}
	if got := sentOf[*msg.InviteGroupRequest](f); len(got) != 1 {
		t.Errorf("%d invitations went out, want the one", len(got))
	}
}

// TestInviteToGroupRefusesTheHalvesItCannotSupply: a null group and a
// null invitee are both what a missing field looks like, and neither is
// something the grid would report back.
func TestInviteToGroupRefusesTheHalvesItCannotSupply(t *testing.T) {
	t.Parallel()
	w, f := invitingSession(t, GroupPowerInvite)
	ctx := context.Background()

	if err := w.InviteToGroup(ctx, msg.UUID{}, RoleEveryone, testInvitee); err == nil {
		t.Error("an invitation into no group at all was sent")
	}
	if err := w.InviteToGroup(ctx, testGroupID, RoleEveryone); err == nil {
		t.Error("an invitation naming nobody was sent")
	}
	if err := w.InviteToGroup(ctx, testGroupID, RoleEveryone, msg.UUID{}); err == nil {
		t.Error("an invitation of the null id was sent")
	}
	if got := sentOf[*msg.InviteGroupRequest](f); len(got) != 0 {
		t.Errorf("%d of the three went out: %s", len(got), f.describe())
	}
}
