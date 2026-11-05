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
		t.Errorf("objects = %+v, want the body on avatar center", got.Objects)
	}

	// An appearance with no attachment list at all is known and empty.
	if got := simAttachmentsFrom(&msg.AvatarAppearance{}, at); got == nil || len(got.Objects) != 0 {
		t.Errorf("an empty list read as %+v", got)
	}
}

// The shape's sex is one byte of the visual parameters: 0 is female and
// 255 male, and an appearance too short to have it, or none at all, says
// nothing.
func TestSexIsTheShapesMaleSetting(t *testing.T) {
	params := func(male byte) []byte {
		p := make([]byte, 253)
		p[MaleParam] = male
		return p
	}
	for _, c := range []struct {
		a    *SimAttachments
		want Sex
	}{
		{nil, SexUnknown},
		{&SimAttachments{}, SexUnknown},
		{&SimAttachments{VisualParams: params(0)[:MaleParam]}, SexUnknown},
		{&SimAttachments{VisualParams: params(0)}, SexFemale},
		{&SimAttachments{VisualParams: params(255)}, SexMale},
	} {
		if got := c.a.Sex(); got != c.want {
			t.Errorf("%+v: %v, want %v", c.a, got, c.want)
		}
	}

	m := &msg.AvatarAppearance{VisualParam: make([]msg.AvatarAppearance_VisualParam, 253)}
	m.VisualParam[MaleParam].ParamValue = 255
	if got := simAttachmentsFrom(m, time.Now()).Sex(); got != SexMale {
		t.Errorf("read from an appearance as %v, want male", got)
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
