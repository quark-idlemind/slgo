package sl

// A friend logging in or out, as it is heard.
//
// The simulator says so in OnlineNotification and OfflineNotification,
// each a list of ids, and at login it sends one naming every friend
// already on.  The agent keeps who is online (agent/friends.go); this is
// the other half, for a program that wants to be told at the moment.
// Why: doc/friend-notices.md

import (
	"sync/atomic"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// FriendChange is one friend coming online or going offline.  A message
// naming several is several of these, in its order.  Nothing here
// checks the id against the friend list: the viewer announces an id it
// has no friend for too.
type FriendChange struct {
	At     time.Time
	ID     msg.UUID
	Online bool
}

// DefaultFriendDepth holds a login burst from a long friend list.
//
// The value is not promised and may change in any release: refer to it by name.
const DefaultFriendDepth = 256

// friendSub is a subscription to friends coming and going.
type friendSub struct {
	ch      chan *FriendChange
	dropped atomic.Uint64
}

// FriendChanges returns a channel of friends logging in and out, the
// login burst included, closed when StopFriendChanges is called or the
// session ends.  A subscription made after the burst has missed it; see
// OnlineFriends for who is on now.
func (w *Session) FriendChanges(depth int) <-chan *FriendChange {
	if depth <= 0 {
		depth = DefaultFriendDepth
	}
	sub := &friendSub{ch: make(chan *FriendChange, depth)}
	if !w.addSub(func() { w.friendSubs[sub.ch] = sub }) {
		close(sub.ch)
	}
	return sub.ch
}

// StopFriendChanges closes a subscription, and returns once it is
// closed.
func (w *Session) StopFriendChanges(ch <-chan *FriendChange) {
	w.onReader(func() {
		if s := w.friendSubs[ch]; s != nil {
			delete(w.friendSubs, ch)
			close(s.ch)
		}
	})
}

// FriendChangesDropped is how many a subscription missed because its
// buffer was full.
func (w *Session) FriendChangesDropped(ch <-chan *FriendChange) uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := w.friendSubs[ch]; s != nil {
		return s.dropped.Load()
	}
	return 0
}

// friendsChanged delivers one notification, an event per id.  Called
// from the reader goroutine only.
func (w *Session) friendsChanged(ids []msg.UUID, online bool) {
	w.mu.Lock()
	subs := make([]*friendSub, 0, len(w.friendSubs))
	for _, s := range w.friendSubs {
		subs = append(subs, s)
	}
	w.mu.Unlock()

	now := time.Now()
	for _, id := range ids {
		c := &FriendChange{At: now, ID: id, Online: online}
		for _, s := range subs {
			select {
			case s.ch <- c:
			default:
				s.dropped.Add(1)
			}
		}
	}
}
