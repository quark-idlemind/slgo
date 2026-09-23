package sl

// What the simulator says an avatar is wearing.
//
// Two things here say that an attachment is on, and they are not the
// same kind of thing.  One is the region describing the attached object,
// which is how WornObjects knows: the description carries the inventory
// item it came from, so it can be matched against the outfit.  The
// other is the list at the end of AvatarAppearance, which is the
// simulator's own account -- object ids and points, nothing else.
//
// The first is detailed and unreliable.  A description can be late, and
// a lost one is not sent again.  The second is sparse and authoritative:
// a viewer keeps an avatar drawn as a cloud until the attachments it has
// been shown number what this list says (LLVOAvatar::updateIsFullyLoaded),
// and that is the only way to tell "on, and not described yet" from
// "not on".
//
// Measured on Agni, one avatar wearing ten attachments: the list held
// six, one per body attachment, and their ids were the attached objects'
// -- the same ids WornObjects reports -- not the inventory items'.  The
// four HUDs were not in it, not even in the copy the avatar was sent
// about itself.  So the list can confirm a body attachment and count
// what is missing a description, and it can say nothing about HUDs.
//
// When it is sent was measured on Agni, and differs by who is being
// told.  The avatars around one that arrives hear its list at once.
// The avatar itself does not: one logging in was sent its own list not
// at all in the twenty seconds before anything asked for a bake, and
// was sent it within a second of asking.  One that teleported was sent
// it about twelve seconds after arriving, unasked.  So the way to have
// a list is to ask for a bake, which is what a viewer does as soon as
// the outfit folder has loaded, and again after every change to it,
// attachments included.  The folder version it was baked from travels
// with it, and a list whose version is not the folder's version now
// describes an outfit that has since changed.

import (
	"context"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// SimAttachment is one entry in the simulator's list: the attached
// object and its point.  The object id is new each time a thing is
// worn, so it names this wearing of it and no other.
type SimAttachment struct {
	Object msg.UUID
	Point  int
}

// SimAttachments is the simulator's attachment list for one avatar.
type SimAttachments struct {
	// CofVersion is the Current Outfit folder version the list was
	// baked from, and Heard when it arrived.
	CofVersion int
	Heard      time.Time

	// Objects is every attachment the simulator named an object for.
	// Pending counts the entries it listed on a point without one,
	// which a viewer discards and so does everything here.
	Objects []SimAttachment
	Pending int
}

// simAttachmentsFrom reads the list out of an appearance.  Nil for no
// appearance, which is "not told" and not "wearing nothing".
func simAttachmentsFrom(m *msg.AvatarAppearance, at time.Time) *SimAttachments {
	if m == nil {
		return nil
	}
	out := &SimAttachments{Heard: at}
	if len(m.AppearanceData) > 0 {
		out.CofVersion = int(m.AppearanceData[0].CofVersion)
	}
	for _, b := range m.AttachmentBlock {
		if b.ID.IsZero() {
			out.Pending++
			continue
		}
		out.Objects = append(out.Objects, SimAttachment{Object: b.ID, Point: int(b.AttachmentPoint)})
	}
	return out
}

// SimAttachments is what the simulator last said an avatar is wearing;
// the zero id is this one.  Nil, with no error, means nothing has been
// heard since the session arrived in the region.
func (w *Session) SimAttachments(ctx context.Context, avatar msg.UUID) (*SimAttachments, error) {
	return w.b.SimAttachments(ctx, avatar)
}

// IsHUDPoint says whether a point is on the screen rather than the
// body.  HUD attachments are never in the simulator's list.
func IsHUDPoint(p int) bool { return p >= HUDCenter2 && p <= HUDBottomRight }

// AttachmentAccount sets the simulator's list for this avatar against
// what the region has described of it.
type AttachmentAccount struct {
	// Known is whether the simulator's list has been heard at all, and
	// Current whether it was baked from the outfit as it is now.  A
	// list that is not current is still the simulator's word, about an
	// outfit that has since changed.
	Known   bool
	Current bool

	// Listed is how many attachments the simulator named objects for,
	// and Pending how many it listed without one.
	Listed  int
	Pending int

	// Undescribed is what the simulator lists and the region has not
	// described: on, by the simulator's account, and not matched to
	// anything in the outfit because nothing here knows which item it
	// came from.
	Undescribed []SimAttachment
}

// Waiting says whether the simulator knows of attachments on this
// avatar that the region has not yet described.  It is what a viewer
// draws as a cloud, and it is the reason not to put anything on yet:
// something named in the outfit and not described may be one of them.
func (a *AttachmentAccount) Waiting() bool {
	return a != nil && a.Known && a.Current && (len(a.Undescribed) > 0 || a.Pending > 0)
}

// AccountForAttachments compares the simulator's list for this avatar
// with the attachments the region has described, which the caller has
// usually just read and passes in.
func (w *Session) AccountForAttachments(ctx context.Context, described []*Attached) (*AttachmentAccount, error) {
	list, err := w.SimAttachments(ctx, msg.UUID{})
	if err != nil || list == nil {
		return &AttachmentAccount{}, err
	}
	acct := &AttachmentAccount{Known: true, Listed: len(list.Objects), Pending: list.Pending}

	cof, err := w.CurrentOutfit(ctx)
	if err != nil {
		return nil, err
	}
	version, err := w.folderVersion(ctx, cof)
	if err != nil {
		return nil, err
	}
	acct.Current = version == list.CofVersion

	seen := make(map[msg.UUID]bool, len(described))
	for _, d := range described {
		seen[d.Object.ID] = true
	}
	for _, o := range list.Objects {
		if !seen[o.Object] {
			acct.Undescribed = append(acct.Undescribed, o)
		}
	}
	return acct, nil
}
