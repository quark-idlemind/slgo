package main

import (
	"context"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// Who an avatar will talk to is the one decision that lets a stranger
// reach the model at all, so what the list means is worth pinning down
// exactly.

func audienceFor(t *testing.T, lines ...string) Audience {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Chat = lines
	return listAudience(cfg)
}

func TestTheChatListNamesPeople(t *testing.T) {
	known := msg.MustParseUUID("3ac37e57-7e57-c0de-5607-527da8fa08de")
	other := msg.MustParseUUID("19d17e57-7e57-c0de-11be-a4325a5080a2")
	a := audienceFor(t, "Quark Idlemind", known.String())

	for _, tc := range []struct {
		id   msg.UUID
		name string
		want bool
	}{
		{other, "Quark Idlemind", true},
		{other, "quark idlemind", true},
		{known, "", true},
		{known, "Somebody Else", true},
		{other, "Somebody Else", false},
		{msg.UUID{}, "", false},
		{other, "Quark", false},
	} {
		got := a(context.Background(), &Approach{From: tc.id, Name: tc.name})
		if got.Talk != tc.want {
			t.Errorf("(%s, %q) = %v (%s), want %v", tc.id, tc.name, got.Talk, got.Why, tc.want)
		}
	}
}

// The star is the one entry that means everybody, and it is a word
// somebody wrote rather than a list that happened to be missing.
func TestTheStarMeansAnyone(t *testing.T) {
	a := audienceFor(t, Anyone)
	got := a(context.Background(), &Approach{
		From: msg.MustParseUUID("19d17e57-7e57-c0de-11be-a4325a5080a2"),
		Name: "Somebody Nobody Listed",
	})
	if !got.Talk {
		t.Errorf("the star refused somebody: %s", got.Why)
	}
}

// An empty list is silence, not "anyone".  Getting that the wrong way
// round would have an avatar answering the whole grid because a line
// was left out of a file.
func TestAnEmptyListTalksToNobody(t *testing.T) {
	a := audienceFor(t)
	got := a(context.Background(), &Approach{Name: "Anybody At All"})
	if got.Talk {
		t.Error("an empty chat list answered somebody")
	}
}

// Being trusted to drive an avatar is not the same as being somebody it
// will chat with.  The two lists are separate on purpose and neither
// implies the other.
func TestTrustDoesNotImplyConversation(t *testing.T) {
	a := audienceFor(t, "Somebody Else")
	got := a(context.Background(), &Approach{Name: "Quark Idlemind", Trusted: true})
	if got.Talk {
		t.Errorf("a trusted avatar was answered without being in the chat list: %s", got.Why)
	}
}

// An avatar that declines to speak does so silently, so the line in the
// log is the only evidence the decision happened.  A verdict with no
// reason is the hardest kind of fault to look into.
func TestEveryVerdictSaysWhy(t *testing.T) {
	for _, a := range []Audience{
		audienceFor(t, "Quark Idlemind"),
		audienceFor(t, Anyone),
		audienceFor(t),
		silentAudience(),
	} {
		for _, name := range []string{"Quark Idlemind", "Somebody Else"} {
			if got := a(context.Background(), &Approach{Name: name}); strings.TrimSpace(got.Why) == "" {
				t.Errorf("a verdict for %q came back with no reason", name)
			}
		}
	}
}

// A daemon with no model should not be deciding who it would talk to if
// it had one.
func TestNoModelMeansNoConversation(t *testing.T) {
	got := silentAudience()(context.Background(), &Approach{Name: "Quark Idlemind"})
	if got.Talk || !strings.Contains(got.Why, "model") {
		t.Errorf("got %v (%s)", got.Talk, got.Why)
	}
}

// A name written with "!" in front of it is the one thing in the list
// somebody went out of their way to say, so it beats every permission
// however the permission was granted.
func TestARefusalBeatsAPermission(t *testing.T) {
	quark := msg.MustParseUUID("3ac37e57-7e57-c0de-5607-527da8fa08de")
	for _, tc := range []struct {
		name  string
		lines []string
		who   string
		id    msg.UUID
		want  bool
	}{
		{"everyone but one", []string{Anyone, "!Quark Idlemind"}, "Quark Idlemind", msg.UUID{}, false},
		{"everyone but one, others still", []string{Anyone, "!Quark Idlemind"}, "Somebody Else", msg.UUID{}, true},
		{"order does not matter", []string{"!Quark Idlemind", Anyone}, "Quark Idlemind", msg.UUID{}, false},
		{"refused by id", []string{Anyone, "!" + quark.String()}, "", quark, false},
		{"named and refused", []string{"Quark Idlemind", "!Quark Idlemind"}, "Quark Idlemind", msg.UUID{}, false},
		{"refused by id, allowed by name", []string{"Quark Idlemind", "!" + quark.String()}, "Quark Idlemind", quark, false},
		{"case does not matter", []string{Anyone, "!quark idlemind"}, "Quark Idlemind", msg.UUID{}, false},
		{"a bare bang says nothing", []string{Anyone, "!"}, "Quark Idlemind", msg.UUID{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := audienceFor(t, tc.lines...)
			got := a(context.Background(), &Approach{From: tc.id, Name: tc.who})
			if got.Talk != tc.want {
				t.Errorf("Talk = %v (%s), want %v", got.Talk, got.Why, tc.want)
			}
		})
	}
}
