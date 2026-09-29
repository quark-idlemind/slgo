package sl

// Group invitations.
//
// A group whose enrolment is closed can only be joined by being
// invited, and an invitation is an instant message of dialog 3
// (llinstantmessage.h:60, IM_GROUP_INVITATION).  Three things about it
// are not what they look like, read out of the viewer and since
// measured on Agni:
//
//   - The sender id IS the group, not the avatar who invited, who is
//     named in the agent-name field and by no id at all.
//   - Nothing carries the group's NAME, except as the simulator may have
//     written it into the text, so there is no field for one below.
//   - The fee and the role id are in the binary bucket: an S32 fee in
//     network byte order, then a role id, twenty bytes, which the viewer
//     requires exactly (llimprocessing.cpp:1502-1518).  A bucket of any
//     other length is recorded as a fee the message did not state rather
//     than read as a fee of zero, since accepting spends real money.
//
// Accepting sends an instant message back to the group id with dialog
// 35, and declining with 36, quoting the invitation's transaction id.
// The AcceptGroupInvite and DeclineGroupInvite capabilities are for an
// invitation that arrived while logged out, with no transaction to
// quote, and this client asks for offline messages nowhere, so they are
// not used.
// Why: doc/group-invitations.md

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// The instant messages that answer an invitation
// (llinstantmessage.h:151-152).
const (
	DialogGroupInvitationAccept  = 35
	DialogGroupInvitationDecline = 36
)

// inviteBucketLen is the bucket a group invitation carries: an S32 fee
// and a role id, which is what sizeof(invite_bucket_t) comes to
// (llimprocessing.cpp:1502-1509).
const inviteBucketLen = 4 + 16

// Invitation is somebody asking this avatar into a group.
type Invitation struct {
	At time.Time

	// Group is which group, and is also who the message came from and
	// where the answer goes.
	Group msg.UUID

	// By is whoever invited, as the message names them, which the
	// simulator sends as a username rather than a display name
	// (llimprocessing.cpp:1478).  It is not an id: the inviter's id is
	// not in the message at all.
	By string

	// Text is what the simulator wrote, which is the only place the
	// group's name can appear.
	Text string

	// Transaction is the invitation's id, and the only thing that can
	// answer it: an answer with a fresh id matches no invitation.
	Transaction msg.UUID

	// Role is the role the invitation is into.  The viewer reads it out
	// of the bucket and then never uses it, so nothing here can say what
	// happens if it is sent back or left out; it is kept because it is
	// the one piece of the invitation a person might want to see.
	Role msg.UUID

	// Fee is what joining costs, in L$, and Stated says whether the
	// message actually said so.  A fee of zero that was stated and a fee
	// nobody stated are different situations: the first is the ordinary
	// free group, the second is a bucket of a shape this does not know,
	// where accepting could spend anything.  A caller that spends money
	// on somebody's behalf has to tell them apart.
	Fee    int32
	Stated bool

	// Recorded says it arrived before this session attached, and is
	// known only because slgod kept it.  See offers.go.
	Recorded bool

	// key is slgod's name for it; see Offer.
	key string
}

func (i *Invitation) String() string {
	who := i.By
	if who == "" {
		who = "somebody"
	}
	s := fmt.Sprintf("%s invites you into group %s", who, i.Group)
	switch {
	case !i.Stated:
		return s + ", and did not say what joining costs"
	case i.Fee > 0:
		return fmt.Sprintf("%s, L$%d to join", s, i.Fee)
	}
	return s
}

// inviteFee reads the fee and the role out of a bucket, and says
// whether it was the shape the viewer expects.  A bucket of any other
// length leaves both zero rather than guessing at an offset, since a
// fee guessed wrong is money.
func inviteFee(b []byte) (fee int32, role msg.UUID, ok bool) {
	if len(b) != inviteBucketLen {
		return 0, msg.UUID{}, false
	}
	// ntohl at llimprocessing.cpp:1518, so network byte order rather
	// than the little-endian the rest of the message is packed in.
	fee = int32(binary.BigEndian.Uint32(b[:4]))
	copy(role[:], b[4:])
	return fee, role, true
}

// noteInvitation keeps one, replacing any earlier invitation into the
// same group: a second supersedes the first, and answering the stale
// transaction answers nothing at all.
//
// An invitation with no sender is dropped rather than kept, because the
// sender is the group: there is nowhere to send the answer, so there is
// nothing a person could do with it but be puzzled.
func (w *Session) noteInvitation(im *IM, key string, recorded bool) {
	if im.From.IsZero() {
		return
	}
	fee, role, stated := inviteFee(im.Bucket)
	w.mu.Lock()
	if w.invites == nil {
		w.invites = map[msg.UUID]*Invitation{}
	}
	w.invites[im.From] = &Invitation{
		At: im.At, Group: im.From, By: im.FromName, Text: im.Text,
		Transaction: im.ID, Role: role, Fee: fee, Stated: stated,
		Recorded: recorded, key: key,
	}
	w.mu.Unlock()
}

// Invitations returns the group invitations waiting for an answer,
// oldest first.
func (w *Session) Invitations() []*Invitation {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*Invitation, 0, len(w.invites))
	for _, i := range w.invites {
		out = append(out, i)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].At.Before(out[j-1].At); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ForgetInvitation drops one without answering it, which is what a
// person does by closing the dialog.
func (w *Session) ForgetInvitation(i *Invitation) {
	if i == nil {
		return
	}
	w.mu.Lock()
	delete(w.invites, i.Group)
	w.mu.Unlock()
}

// AcceptInvitation joins the group.
//
// It is worth knowing what this costs before calling it: if the group
// charges to join, this is the call that spends it.  Nothing here can
// cap the amount -- the answer carries no figure, and the simulator
// charges whatever the group's fee is at the moment it arrives -- so
// Invitation.Fee is what the invitation said and not a promise, and a
// caller acting for a person should have that person say the number
// first.
//
// The profile's rules check it as a purchase, free or not: refused
// unless pay = on, and held to pay_max, pay_daily and pay_to with the
// group as the payee.  A session held here returns a *PayRefused; one
// held through slgod is told by a MoneyBalanceReply signed
// pay.RefusedBy, which this does not wait for.
// Why: doc/money.md#how-each-is-checked
//
// Nothing replies to say the join worked.  What arrives afterwards is
// an AgentGroupDataUpdate carrying the whole membership list again,
// which is where a caller sees the new group appear.
func (w *Session) AcceptInvitation(ctx context.Context, i *Invitation) error {
	if i == nil {
		return fmt.Errorf("sl: no group invitation to accept")
	}
	return w.answerInvitation(ctx, i, DialogGroupInvitationAccept)
}

// DeclineInvitation says no.  Saying so matters: an invitation left
// unanswered stays open, and whoever sent it is told nothing.
func (w *Session) DeclineInvitation(ctx context.Context, i *Invitation) error {
	if i == nil {
		return fmt.Errorf("sl: no group invitation to decline")
	}
	return w.answerInvitation(ctx, i, DialogGroupInvitationDecline)
}

// answerInvitation sends either answer, since they differ only in the
// dialog number.
//
// The viewer sends the literal strings "name" and "message" in the two
// text fields (llviewermessage.cpp:746-747), which is as good as saying
// the simulator reads neither; this sends the avatar's own name, the
// way every other instant message from here does, and nothing for the
// text.
func (w *Session) answerInvitation(ctx context.Context, i *Invitation, dialog uint8) error {
	// Asked first, before anything a join costs: another client of the
	// same avatar that has already answered this one means nothing
	// goes, and no fee is paid twice.  See offers.go.
	how := "accepted"
	if dialog == DialogGroupInvitationDecline {
		how = "declined"
	}
	undo, err := w.answering(ctx, i.key, how)
	if err != nil {
		return err
	}
	// To the group rather than to whoever invited: the group id is the
	// only address in the invitation, and it is where the viewer sends
	// its answer (llviewermessage.cpp:745).
	m := w.im(i.Group, dialog, "")
	// The invitation's transaction rather than a fresh one, which is
	// the only thing tying the answer to the invitation.
	m.MessageBlock.ID = i.Transaction
	if err := w.Send(ctx, m); err != nil {
		undo()
		return err
	}
	w.ForgetInvitation(i)
	return nil
}

// Sending one, which is the other direction and a different message.
//
// InviteGroupRequest carries the group, and a list of invitee-and-role
// pairs: the group is the one being invited into, and the role is which
// role inside it the invitation is for.
//
// Nothing answers it.  InviteGroupResponse exists and is TRUSTED --
// simulator to simulator -- so a client never sees one, and there is no
// reply on the circuit and no field anywhere saying an invitation went.
// What the sender can observe is an alert, if the simulator objects,
// and nothing at all otherwise.  So this checks what it can before
// sending and says plainly that it cannot check the rest.
//
// The invitation the far end gets is the instant message the top of
// this file describes, which is why both directions live here: what is
// sent is not the shape of what arrives, and reading one without the
// other is how somebody comes to look for the reply that does not
// exist.

// RoleEveryone is the role every group has and the one an invitation
// with no role chosen is into.
//
// It is the null id, which is the same value a missing field has -- so
// it is written down rather than left as a zero somebody has to
// recognise.  Linden Lab's viewer offers the group's roles in a menu
// and starts on this one; a group that has never defined a role has
// only this.
var RoleEveryone = msg.UUID{}

// GroupPowerInvite is the power to invite somebody into a group, out of
// the mask AgentGroupDataUpdate carries for each membership
// (Group.Powers).
//
// From Linden Lab's roles_constants.h, where it is GP_MEMBER_INVITE.
// It is the second bit and not the first: bit zero is unassigned in
// that file, so a mask read as "any power at all" would let a member
// with none through.
const GroupPowerInvite = 1 << 1

// ErrCannotInvite is this avatar not being able to invite into that
// group: not a member of it, or a member whose role does not carry the
// power.
//
// It is refused here rather than sent, because a refusal by the grid is
// not something a client can see -- see the head of this section -- so
// an invitation nobody was allowed to make would look exactly like one
// that went.
var ErrCannotInvite = errors.New("sl: this avatar cannot invite into that group")

// InviteToGroup asks the grid to invite people into a group.
//
// The role is which role they are invited into; RoleEveryone is the one
// every group has and is what a caller with nothing else in mind wants.
//
// Nothing comes back.  There is no reply to this message, so what this
// returns is that the request was sent and not that anybody was
// invited -- the invitation itself is an instant message the far end
// receives, and the only sure way to know one arrived is to be the
// avatar it arrived at.  A caller reporting to a person should say
// "invited" in the sense of "asked", which is all anybody here knows.
//
// What can be checked is checked first.  The membership list this
// session already holds says whether this avatar is in the group at all
// and whether its role carries the power to invite, and both of those
// are refused here with ErrCannotInvite rather than sent to be ignored
// in silence.  An empty list is not read as "belongs to none": nothing
// asks for it and it arrives unasked, so an avatar that has not been
// told yet is allowed to try -- see the Groups field for that argument.
func (w *Session) InviteToGroup(ctx context.Context, group, role msg.UUID, who ...msg.UUID) error {
	if group.IsZero() {
		return fmt.Errorf("sl: no group to invite into")
	}
	if len(who) == 0 {
		return fmt.Errorf("sl: nobody to invite into group %s", group)
	}
	for _, id := range who {
		if id.IsZero() {
			return fmt.Errorf("sl: the null id is not somebody to invite")
		}
	}
	if err := w.mayInvite(ctx, group); err != nil {
		return err
	}

	m := &msg.InviteGroupRequest{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.GroupData.GroupID = group
	for _, id := range who {
		m.InviteData = append(m.InviteData, msg.InviteGroupRequest_InviteData{
			InviteeID: id, RoleID: role,
		})
	}
	return w.Send(ctx, m)
}

// mayInvite is what this session can tell about whether the invitation
// is allowed, which is the membership list and nothing else.
//
// The powers are the role's, as the simulator sent them, and they are
// tested rather than displayed: a mask is not something to hand a
// caller and ask them to interpret.
func (w *Session) mayInvite(ctx context.Context, group msg.UUID) error {
	p, err := w.Where(ctx)
	if err != nil {
		return err
	}
	// Not told yet is not the same as belongs to none, so it is allowed
	// through rather than refused on a list that may be empty because
	// nothing has arrived.
	if len(p.Groups) == 0 {
		return nil
	}
	for _, g := range p.Groups {
		if g.ID != group {
			continue
		}
		if g.Powers&GroupPowerInvite == 0 {
			return fmt.Errorf("%w: this avatar is in %s and its role there does not "+
				"carry the power to invite", ErrCannotInvite, groupCalled(g))
		}
		return nil
	}
	return fmt.Errorf("%w: this avatar is not a member of %s, and only a member "+
		"invites", ErrCannotInvite, group)
}

// groupCalled names a group the way an error should: by its name where
// there is one, and by its key either way, since the key is what a
// caller can act on.
func groupCalled(g Group) string {
	if g.Name == "" {
		return g.ID.String()
	}
	return fmt.Sprintf("%q (%s)", g.Name, g.ID)
}
