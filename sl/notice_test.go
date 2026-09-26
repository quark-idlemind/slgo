package sl

import (
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	exampleGroup  = msg.MustParseUUID("605c7e57-7e57-c0de-04c7-b5849f67c55b")
	exampleNotice = msg.MustParseUUID("7fc17e57-7e57-c0de-25b8-25f112ce9e28")
)

// noticeBucket is a notice's binary bucket: has_inventory, asset_type,
// the group, then the item's name and a NUL.
func noticeBucket(has bool, a AssetType, group msg.UUID, name string) []byte {
	b := []byte{0, byte(a)}
	if has {
		b[0] = 1
	}
	b = append(b, group[:]...)
	return append(append(b, name...), 0)
}

func TestAGroupNoticeIsReadOutOfItsMessage(t *testing.T) {
	cases := []struct {
		name          string
		text          string
		bucket        []byte
		subject, body string
		group         msg.UUID
		has           bool
		attachment    string
		asset         AssetType
	}{
		{
			name: "no attachment", text: "Meeting moved|Friday at eight, not seven.",
			bucket:  noticeBucket(false, 0, exampleGroup, ""),
			subject: "Meeting moved", body: "Friday at eight, not seven.", group: exampleGroup,
		},
		{
			name: "a notecard", text: "Rules|Read the card.",
			bucket:  noticeBucket(true, AssetNotecard, exampleGroup, "Example Rules"),
			subject: "Rules", body: "Read the card.", group: exampleGroup,
			has: true, attachment: "Example Rules", asset: AssetNotecard,
		},
		{
			name: "a bar in the body", text: "Poll|yes|no",
			bucket:  noticeBucket(false, 0, exampleGroup, ""),
			subject: "Poll", body: "yes|no", group: exampleGroup,
		},
		{
			name: "no bar at all", text: "Just a subject",
			bucket:  noticeBucket(false, 0, exampleGroup, ""),
			subject: "Just a subject", group: exampleGroup,
		},
		{name: "an empty bucket", text: "S|B", subject: "S", body: "B"},
		{name: "a bucket short of its header", text: "S|B", bucket: []byte{1, 7, 0x5e, 0x0a}, subject: "S", body: "B"},
		{
			name: "a name with no NUL", text: "S|B",
			bucket:  noticeBucket(true, AssetObject, exampleGroup, "Example Box")[:noticeHeader+len("Example Box")],
			subject: "S", body: "B", group: exampleGroup,
			has: true, attachment: "Example Box", asset: AssetObject,
		},
		{
			name: "an attachment with no name", text: "S|B",
			bucket:  noticeBucket(true, AssetLandmark, exampleGroup, "")[:noticeHeader],
			subject: "S", body: "B", group: exampleGroup, has: true, asset: AssetLandmark,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			im := &IM{
				Dialog: DialogGroupNotice, From: somebody, FromName: "Example Resident",
				Text: c.text, Bucket: c.bucket, ID: exampleNotice,
			}
			n, ok := GroupNoticeFrom(im)
			if !ok {
				t.Fatal("not read as a notice")
			}
			if n.Subject != c.subject || n.Body != c.body {
				t.Errorf("subject %q body %q", n.Subject, n.Body)
			}
			if n.Group != c.group {
				t.Errorf("group %s, want %s", n.Group, c.group)
			}
			if n.HasAttachment != c.has || n.Attachment != c.attachment || n.Asset != c.asset {
				t.Errorf("attachment %v %q %s", n.HasAttachment, n.Attachment, n.Asset)
			}
			if n.From != somebody || n.FromName != "Example Resident" || n.Transaction != exampleNotice {
				t.Errorf("from %s %q transaction %s", n.From, n.FromName, n.Transaction)
			}
		})
	}
}

func TestOnlyANoticeIsReadAsOne(t *testing.T) {
	im := &IM{Dialog: DialogMessage, Text: "S|B", Bucket: noticeBucket(false, 0, exampleGroup, "")}
	if _, ok := GroupNoticeFrom(im); ok {
		t.Error("a private message was read as a notice")
	}
}

// TestANoticeFromItsGroupTeachesNoName: a notice may carry the group's
// id with the poster's name, and that name is not the group's.
func TestANoticeFromItsGroupTeachesNoName(t *testing.T) {
	w, f := newFakeSession(t)
	ims := w.IMs(4)

	m := arrivingIM(exampleGroup, "Example Resident", DialogGroupNotice, exampleNotice, "S|B")
	m.MessageBlock.BinaryBucket = noticeBucket(false, 0, exampleGroup, "")
	f.Relay(t, m)

	select {
	case im := <-ims:
		if n, ok := GroupNoticeFrom(im); !ok || n.FromName != "Example Resident" {
			t.Errorf("read as %+v", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the notice never reached the subscription")
	}
	if got := w.Name(exampleGroup); got != "" {
		t.Errorf("the group is now called %q, which is the poster", got)
	}
}

// TestANoticeFromItsPosterTeachesTheirName: the other shape, where From
// is the member who posted it.
func TestANoticeFromItsPosterTeachesTheirName(t *testing.T) {
	w, f := newFakeSession(t)
	ims := w.IMs(4)

	m := arrivingIM(somebody, "Example Resident", DialogGroupNotice, exampleNotice, "S|B")
	m.MessageBlock.BinaryBucket = noticeBucket(false, 0, exampleGroup, "")
	f.Relay(t, m)
	select {
	case <-ims:
	case <-time.After(2 * time.Second):
		t.Fatal("the notice never reached the subscription")
	}
	if got := w.Name(somebody); got != "Example Resident" {
		t.Errorf("the poster's name was not learned: %q", got)
	}
}
