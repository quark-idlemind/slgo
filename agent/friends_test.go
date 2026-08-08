package agent

// The friend list arrives once, in two halves, from two places that
// never repeat themselves.
//
// Who the friends are is in the login response and on no message the
// simulator ever sends; which of them are logged in arrives seconds
// after the handshake as one burst of OnlineNotification naming everyone
// already there.  A client that attaches a minute later hears neither,
// and there is nothing it can ask.  So a buddy dropped while decoding
// the login response, or an OnlineNotification that goes nowhere, is a
// friend the session can never know about again -- which is what these
// tests are for.

import (
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	oneFriend   = msg.MustParseUUID("a9727e57-7e57-c0de-1b26-1aab3a8a0694")
	otherFriend = msg.MustParseUUID("c7577e57-7e57-c0de-5164-e6bbbaf8cb50")
	newFriend   = msg.MustParseUUID("d1fb7e57-7e57-c0de-91c3-7cf246efd48b")
)

// TestBuddiesFromTheLoginResponse: the block is an array of structs and
// the login server has been known to send its numbers as strings, so
// both forms have to come out as the same rights.  An entry that makes
// no sense is skipped rather than taken as a reason to fail the login.
func TestBuddiesFromTheLoginResponse(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		in   map[string]any
		want []Buddy
	}{
		{
			// An avatar with no friends and a login that never asked
			// for the block look exactly the same here, which is why
			// the option is always asked for.
			name: "no buddy-list at all",
			in:   map[string]any{},
		},
		{
			name: "a buddy-list that is not an array",
			in:   map[string]any{"buddy-list": "none"},
		},
		{
			name: "rights as the integers they should be",
			in: map[string]any{"buddy-list": []any{
				map[string]any{
					"buddy_id":            oneFriend.String(),
					"buddy_rights_given":  int64(RightOnlineStatus | RightMapLocation),
					"buddy_rights_has":    int64(RightModifyObject),
					"buddy_id_but_unused": "ignored",
				},
			}},
			want: []Buddy{{ID: oneFriend, RightsGiven: 3, RightsHas: 4}},
		},
		{
			// Older login servers sent them as <string>, and a viewer
			// that only understood <int> would have read no rights at
			// all rather than failing visibly.
			name: "rights as the strings an older server sent",
			in: map[string]any{"buddy-list": []any{
				map[string]any{
					"buddy_id":           oneFriend.String(),
					"buddy_rights_given": "1",
					"buddy_rights_has":   "2",
				},
			}},
			want: []Buddy{{ID: oneFriend, RightsGiven: 1, RightsHas: 2}},
		},
		{
			name: "entries that make no sense are stepped over",
			in: map[string]any{"buddy-list": []any{
				"not a struct at all",
				map[string]any{"buddy_id": "not a uuid"},
				map[string]any{"buddy_id": otherFriend.String()},
			}},
			want: []Buddy{{ID: otherFriend}},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := buddiesFrom(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %d buddies, want %d: %+v", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("buddy %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestOnlineStatusArrivesAfterTheList: the login response says who the
// friends are and nothing about who is there.  That comes over the
// circuit, and the two have to meet in the same list.
func TestOnlineStatusArrivesAfterTheList(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	a.seedFriends([]Buddy{
		{ID: oneFriend, RightsGiven: RightOnlineStatus},
		{ID: otherFriend, RightsGiven: RightMapLocation},
	})

	on := &msg.OnlineNotification{}
	on.AgentBlock = []msg.OnlineNotification_AgentBlock{
		{AgentID: oneFriend},
		// A friendship formed during the session shows up here first:
		// this one was not in the login list because they were not a
		// friend when it was written, and adding them is the only way it
		// ever becomes visible without logging in again.
		{AgentID: newFriend},
	}
	feed(t, a, on)

	byID := map[msg.UUID]Friend{}
	for _, f := range a.Friends() {
		byID[f.ID] = f
	}
	if len(byID) != 3 {
		t.Fatalf("Friends = %+v, want three", a.Friends())
	}
	if !byID[oneFriend].Online || byID[oneFriend].RightsGiven != RightOnlineStatus {
		t.Errorf("the login list's rights were lost: %+v", byID[oneFriend])
	}
	if byID[otherFriend].Online {
		t.Error("a friend nobody mentioned was marked online")
	}
	if !byID[newFriend].Online {
		t.Error("a friend made during the session was not added")
	}

	off := &msg.OfflineNotification{}
	off.AgentBlock = []msg.OfflineNotification_AgentBlock{{AgentID: oneFriend}}
	feed(t, a, off)

	for _, f := range a.Friends() {
		if f.ID == oneFriend && f.Online {
			t.Error("a friend who logged out is still shown as online")
		}
	}
}

// TestFriendsComeBackInAStableOrder: a map is walked in whatever order
// it likes, and a list that reshuffles itself between calls is one no
// caller can display.
func TestFriendsComeBackInAStableOrder(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	a.seedFriends([]Buddy{{ID: newFriend}, {ID: oneFriend}, {ID: otherFriend}})

	want := []msg.UUID{oneFriend, otherFriend, newFriend}
	for range 5 {
		got := a.Friends()
		for i := range want {
			if got[i].ID != want[i] {
				t.Fatalf("friend %d = %v, want %v", i, got[i].ID, want[i])
			}
		}
	}
}

// TestNoteFriendIsHowTheAcceptingSideEverFindsOut: the grid tells the
// avatar that accepts an offer nothing at all, so a friendship formed
// that way is invisible until somebody says so.
func TestNoteFriendIsHowTheAcceptingSideEverFindsOut(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	a.seedFriends([]Buddy{{ID: oneFriend, RightsGiven: RightModifyObject}})

	a.NoteFriend(newFriend, true)
	// A friend already known learns nothing here beyond being online,
	// so the rights must survive being noted again.
	a.NoteFriend(oneFriend, false)
	// The zero id is not a friend, and recording it would put an entry
	// in the list that no lookup could ever match.
	a.NoteFriend(msg.UUID{}, true)

	got := a.Friends()
	if len(got) != 2 {
		t.Fatalf("Friends = %+v, want two", got)
	}
	for _, f := range got {
		switch f.ID {
		case oneFriend:
			if f.Online || f.RightsGiven != RightModifyObject {
				t.Errorf("noting a known friend lost their rights: %+v", f)
			}
		case newFriend:
			if !f.Online {
				t.Errorf("the new friend is not online: %+v", f)
			}
		default:
			t.Errorf("unexpected friend %v", f.ID)
		}
	}
}
