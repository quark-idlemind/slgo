package sl

import (
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// TestSimAttachmentsReadWhatTheAppearanceSaid: nil for no appearance,
// which is "not told" and not "wearing nothing"; an entry with no object
// is pending, counted and left out, as a viewer leaves it out.
func TestSimAttachmentsReadWhatTheAppearanceSaid(t *testing.T) {
	if got := simAttachmentsFrom(nil, time.Now()); got != nil {
		t.Errorf("no appearance read as %+v, want nil", got)
	}

	body := msg.MustParseUUID("0d677e57-7e57-c0de-6555-1438aa79d2bd")
	m := &msg.AvatarAppearance{}
	m.AppearanceData = []msg.AvatarAppearance_AppearanceData{{CofVersion: 42}}
	m.AttachmentBlock = []msg.AvatarAppearance_AttachmentBlock{
		{ID: body, AttachmentPoint: 40},
		{AttachmentPoint: 1},
	}
	at := time.Unix(1700000000, 0)
	got := simAttachmentsFrom(m, at)
	if got.CofVersion != 42 || !got.Heard.Equal(at) || got.Pending != 1 {
		t.Errorf("read as %+v", got)
	}
	if len(got.Objects) != 1 || got.Objects[0] != (SimAttachment{Object: body, Point: 40}) {
		t.Errorf("objects = %+v, want the body on avatar centre", got.Objects)
	}

	// An appearance with no attachment list at all is known and empty.
	if got := simAttachmentsFrom(&msg.AvatarAppearance{}, at); got == nil || len(got.Objects) != 0 {
		t.Errorf("an empty list read as %+v", got)
	}
}

func TestOnlyTheEightHUDPointsAreHUDPoints(t *testing.T) {
	for p := 0; p <= 60; p++ {
		want := p >= 31 && p <= 38
		if IsHUDPoint(p) != want {
			t.Errorf("IsHUDPoint(%d) = %v", p, !want)
		}
	}
}
