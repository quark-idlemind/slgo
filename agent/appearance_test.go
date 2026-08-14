package agent

import (
	"fmt"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

func appearanceOf(id msg.UUID, texture string) *msg.AvatarAppearance {
	m := &msg.AvatarAppearance{}
	m.Sender.ID = id
	m.ObjectData.TextureEntry = []byte(texture)
	m.VisualParam = []msg.AvatarAppearance_VisualParam{{ParamValue: 42}}
	return m
}

func avatarID(n int) msg.UUID {
	return msg.MustParseUUID(fmt.Sprintf("00157e57-7e57-c0de-028f-%012d", n))
}

// TestAppearanceIsKeptPerAvatar: this arrives once for each avatar in
// view, and a viewer attaching later has no way to ask for it.
func TestAppearanceIsKeptPerAvatar(t *testing.T) {
	var s Appearances
	s.note(appearanceOf(avatarID(1), "one"))
	s.note(appearanceOf(avatarID(2), "two"))

	if held, _ := s.Stats(); held != 2 {
		t.Fatalf("held %d appearances, want 2", held)
	}
	got := s.Get(avatarID(2))
	if got == nil || string(got.ObjectData.TextureEntry) != "two" {
		t.Errorf("second avatar = %v", got)
	}
}

// TestChangingClothesReplaces: the message is a complete description
// every time, so keeping both would leave a viewer to choose.
func TestChangingClothesReplaces(t *testing.T) {
	var s Appearances
	s.note(appearanceOf(avatarID(1), "before"))
	s.note(appearanceOf(avatarID(1), "after"))

	if held, _ := s.Stats(); held != 1 {
		t.Fatalf("held %d appearances for one avatar, want 1", held)
	}
	if got := string(s.Get(avatarID(1)).ObjectData.TextureEntry); got != "after" {
		t.Errorf("texture entry = %q, want the later one", got)
	}
}

// TestAppearanceIsCopied: every block here is or holds a slice pointing
// into the receive buffer, which is reused.
func TestAppearanceIsCopied(t *testing.T) {
	var s Appearances
	buf := []byte("original")
	m := appearanceOf(avatarID(1), "")
	m.ObjectData.TextureEntry = buf
	s.note(m)

	copy(buf, "OVERWRIT")
	m.VisualParam[0].ParamValue = 9

	kept := s.Get(avatarID(1))
	if got := string(kept.ObjectData.TextureEntry); got != "original" {
		t.Errorf("texture entry = %q; the receive buffer was kept rather than copied", got)
	}
	if got := kept.VisualParam[0].ParamValue; got != 42 {
		t.Errorf("visual param = %d; the block was shared rather than copied", got)
	}
}

// TestAppearancesAreBounded: nothing here is told when an avatar leaves,
// so a session somewhere busy would remember everybody who ever passed.
func TestAppearancesAreBounded(t *testing.T) {
	var s Appearances
	for i := 1; i <= AppearanceLimit*2; i++ {
		s.note(appearanceOf(avatarID(i), "x"))
	}
	held, dropped := s.Stats()
	if held > AppearanceLimit {
		t.Errorf("held %d appearances, over the %d limit", held, AppearanceLimit)
	}
	if dropped == 0 {
		t.Error("nothing was dropped, so the limit did nothing")
	}
	if s.Get(avatarID(AppearanceLimit * 2)) == nil {
		t.Error("the most recent avatar was dropped; eviction took the wrong end")
	}
}

// TestCrossingForgetsAppearances: the avatars of the region just left
// are not the ones here, and those that are describe themselves again.
func TestCrossingForgetsAppearances(t *testing.T) {
	var s Appearances
	s.note(appearanceOf(avatarID(1), "somewhere"))
	s.forget()
	if held, _ := s.Stats(); held != 0 {
		t.Errorf("%d appearances survived a crossing", held)
	}
}
