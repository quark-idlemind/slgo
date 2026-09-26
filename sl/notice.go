package sl

// Group notices, read out of the instant message that delivers them.
// Why the bucket is read this way: doc/group-notices.md

import (
	"bytes"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// GroupNotice is a notice posted to a group this avatar is in.
type GroupNotice struct {
	At time.Time

	// From is who posted it, or the group itself: some simulators put
	// the group's id there.  FromName is always the poster's name.
	From     msg.UUID
	FromName string

	// Group is zero when the bucket was too short to say.
	Group msg.UUID

	Subject string
	Body    string

	// HasAttachment says an item came with it, called Attachment, of
	// kind Asset.  Transaction is what accepting it would quote.
	HasAttachment bool
	Attachment    string
	Asset         AssetType
	Transaction   msg.UUID
}

// noticeHeader is the bucket's has_inventory, asset_type and group_id,
// ahead of the attachment's name.
const noticeHeader = 1 + 1 + 16

// GroupNoticeFrom reads a notice out of an instant message, and says
// whether it was one.  A bucket too short for its header leaves Group
// zero and no attachment rather than refusing the notice.
func GroupNoticeFrom(im *IM) (*GroupNotice, bool) {
	if im.Dialog != DialogGroupNotice {
		return nil, false
	}
	n := &GroupNotice{
		At:          im.At,
		From:        im.From,
		FromName:    im.FromName,
		Transaction: im.ID,
	}
	// The first '|' ends the subject; a body may hold more of them.
	n.Subject, n.Body, _ = strings.Cut(im.Text, "|")

	b := im.Bucket
	if len(b) < noticeHeader {
		return n, true
	}
	copy(n.Group[:], b[2:noticeHeader])
	if b[0] != 0 {
		n.HasAttachment = true
		n.Asset = AssetType(int8(b[1]))
		name := b[noticeHeader:]
		if i := bytes.IndexByte(name, 0); i >= 0 {
			name = name[:i]
		}
		n.Attachment = string(name)
	}
	return n, true
}
