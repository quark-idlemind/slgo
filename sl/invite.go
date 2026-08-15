package sl

// Group invitations.
//
// A parcel usually grants "create objects" to a group rather than to
// individuals, so an avatar that belongs to no group can be locked out
// of land its siblings build on.  A group whose enrolment is closed can
// only be joined by being invited, and an invitation is not a message
// of its own: it is an instant message of dialog 3
// (llinstantmessage.h:60, IM_GROUP_INVITATION).  Before this the
// package could see one arrive and had no way to answer it, so an
// avatar could be invited and still never join.
//
// Three things about the message are not what they look like.  All
// three were read out of the viewer first, because no avatar this
// daemon held had the power to invite another and so no invitation
// could be made to arrive; they have since been measured, by creating a
// group on Agni for the purpose and inviting two of these avatars into
// it.  Where a paragraph below says what was measured, that is what it
// means.
//
// The sender id IS the group, not the avatar who invited.  The viewer
// says so where it files one for its spam filter -- "haystack.mOwnerID
// = from_id; // group ID" (llimprocessing.cpp:822) -- and again where
// it builds the answer, taking the group from the sender when the
// message is marked as coming from a group and from the offline path's
// spare id otherwise (llimprocessing.cpp:1522).  That spare id is null
// on the circuit: it is a defaulted parameter (llimprocessing.h:55) and
// process_improved_im passes nothing for it (llviewermessage.cpp:2552).
// So for an invitation that arrives while logged in the sender is the
// group and nothing else is.  Whoever invited is in the agent-name
// field, which here holds a person who is not the sender.
//
// Nothing carries the group's NAME, which is why there is no field for
// one below.  The name a person recognises the group by, if it appears
// at all, is inside the text the simulator composed; the viewer puts
// that text into its dialog and shows nothing else beside it
// (args["MESSAGE"] = message, llimprocessing.cpp:1529, into a
// notification whose whole body is [MESSAGE],
// skins/default/xui/en/notifications.xml:8727-8731).  The name is not
// derivable here either: a group this avatar has not joined is not in
// AgentGroupDataUpdate, and this package has never asked a group
// profile for anything.
//
// The fee and the role id are in the binary bucket, as an S32 fee in
// network byte order followed by a role id -- twenty bytes, which the
// viewer requires exactly and drops the message otherwise
// (llimprocessing.cpp:1502-1518).  The fee is the part that matters,
// because accepting spends the person's real money; the viewer stops
// on it and asks (JoinGroupCanAfford, llviewermessage.cpp:696-707).
//
// The bucket is the one place worth doubting.  The comment beside the
// dialog in llinstantmessage.h:52-59 describes an entirely different
// shape -- a null terminated string of one byte for officer or member
// and then the cost in decimal -- and says "ID is the group id", which
// the code contradicts twice over.  The comment is taken to be stale:
// the parsing is what today's viewer runs, and a viewer that dropped
// every group invitation on Agni would not have gone unnoticed, so an
// invitation there carries twenty bytes.
//
// Measured, in the end: an invitation into a free group read as a
// stated fee of zero, and the same group with its signup fee set to
// L$50 read as fifty -- so the length is twenty and the four bytes are
// in network order, since little-endian would have made that fifty into
// 838860800.  The doubt below is kept anyway.  Two grids and one
// message do not settle a shape, and a bucket of any other length is
// still the case worth being careful about.
//
// So a bucket of any other length is not
// read as a fee of zero -- it is recorded as a fee the message did not
// state, which a caller can be told about, rather than a number that
// would be wrong in the direction of money.
//
// Accepting sends an ordinary instant message back to the group id with
// dialog 35, and declining with 36 (llinstantmessage.h:151-152),
// quoting the invitation's transaction id -- llviewermessage.cpp:
// 736-750, the same shape as DeclineLure answering a lure by its id.
// There are also AcceptGroupInvite and DeclineGroupInvite capabilities
// (llviewermessage.cpp:713-730).  They are not implemented here because
// the viewer uses them for one case only: an invitation that arrived
// while the avatar was logged out has no transaction id to quote
// (use_offline_cap is session_id.isNull() && offline == IM_OFFLINE,
// llimprocessing.cpp:1526), and this client asks for offline messages
// nowhere, so every invitation it can see arrived on the circuit with a
// transaction id of its own.  If one ever turns up with a null
// transaction it is that offline case, it cannot be answered this way,
// and the capabilities are where to look.

import (
	"context"
	"encoding/binary"
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
func (w *Session) noteInvitation(im *IM) {
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
	// To the group rather than to whoever invited: the group id is the
	// only address in the invitation, and it is where the viewer sends
	// its answer (llviewermessage.cpp:745).
	m := w.im(i.Group, dialog, "")
	// The invitation's transaction rather than a fresh one, which is
	// the only thing tying the answer to the invitation.
	m.MessageBlock.ID = i.Transaction
	if err := w.Send(ctx, m); err != nil {
		return err
	}
	w.ForgetInvitation(i)
	return nil
}
