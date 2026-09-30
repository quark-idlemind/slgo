package sl

import (
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	friendOne    = msg.MustParseUUID("454d7e57-7e57-c0de-a589-88bc45f804fb")
	friendTwo    = msg.MustParseUUID("52a27e57-7e57-c0de-ec24-3894b06d3a69")
	friendNobody = msg.MustParseUUID("6fa27e57-7e57-c0de-b607-ae8fd2f2917b")
)

func onlineOf(ids ...msg.UUID) *msg.OnlineNotification {
	m := &msg.OnlineNotification{}
	for _, id := range ids {
		m.AgentBlock = append(m.AgentBlock, msg.OnlineNotification_AgentBlock{AgentID: id})
	}
	return m
}

func offlineOf(ids ...msg.UUID) *msg.OfflineNotification {
	m := &msg.OfflineNotification{}
	for _, id := range ids {
		m.AgentBlock = append(m.AgentBlock, msg.OfflineNotification_AgentBlock{AgentID: id})
	}
	return m
}

func nextFriend(t *testing.T, ch <-chan *FriendChange) *FriendChange {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatal("the subscription closed")
		}
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no friend change arrived")
	}
	return nil
}

// Each id in a notification is one event, in order, online or offline,
// and an id that is nobody's friend is one too, as the viewer announces it.
func TestAFriendNotificationIsOneEventPerId(t *testing.T) {
	w, f := newFakeSession(t)
	ch := w.FriendChanges(0)

	f.Relay(t, onlineOf(friendOne, friendNobody, friendTwo))
	f.Relay(t, offlineOf(friendTwo))

	want := []FriendChange{
		{ID: friendOne, Online: true},
		{ID: friendNobody, Online: true},
		{ID: friendTwo, Online: true},
		{ID: friendTwo, Online: false},
	}
	for i, w := range want {
		got := nextFriend(t, ch)
		if got.ID != w.ID || got.Online != w.Online || got.At.IsZero() {
			t.Errorf("event %d: %+v, want %v online=%v", i, got, w.ID, w.Online)
		}
	}
}

// Ending the session closes the subscription, as it does every other.
func TestFriendChangesEndWithTheSession(t *testing.T) {
	w, f := newFakeSession(t)
	ch := w.FriendChanges(0)
	f.Relay(t, onlineOf(friendOne))
	nextFriend(t, ch)
	w.StopFriendChanges(ch)
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("an event after the stop")
		}
	case <-time.After(5 * time.Second):
		t.Error("the stopped subscription was not closed")
	}
}
