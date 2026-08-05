package agent

// Who this avatar's friends are, and which of them are online.
//
// Both halves arrive once and never again, which is why they are kept
// here rather than left to whoever asks.  The list itself comes in the
// login response, in the buddy-list block, and is on no message the
// simulator ever sends.  Online status arrives seconds after the
// handshake as a burst of OnlineNotification, one message naming
// everyone already logged in, and after that only as people come and
// go.
//
// A client that attaches a minute later hears none of it.  That is the
// same reason the region's objects are held here: the grid describes
// something once, to whoever was listening at the time, and the session
// was the thing listening.

import (
	"sort"

	"github.com/quark-idlemind/slgo/msg"
)

// Buddy is one entry of the login response's friend list.
//
// The rights are bit masks: 1 is may see me online, 2 is may see me on
// the map, 4 is may edit my objects.  Given is what this avatar granted
// the friend, Has is what the friend granted back.
type Buddy struct {
	ID          msg.UUID
	RightsGiven int32
	RightsHas   int32
}

// Rights bits, from the viewer's LLRelationship.
const (
	RightOnlineStatus = 1 << 0
	RightMapLocation  = 1 << 1
	RightModifyObject = 1 << 2
)

// Friend is a friend and whether they are logged in.
type Friend struct {
	Buddy
	Online bool
}

// buddiesFrom reads the buddy-list block of a login response.
//
// It is an array of structs; a login that did not ask for it, or an
// avatar with no friends, has none, and those two are indistinguishable
// here -- which is why the option is always asked for.
func buddiesFrom(m map[string]any) []Buddy {
	v, ok := lookup(m, "buddy-list")
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []Buddy
	for _, e := range arr {
		b, ok := e.(map[string]any)
		if !ok {
			continue
		}
		id, err := msg.ParseUUID(getString(b, "buddy_id"))
		if err != nil {
			continue
		}
		given, _ := getInt(b, "buddy_rights_given")
		has, _ := getInt(b, "buddy_rights_has")
		out = append(out, Buddy{ID: id, RightsGiven: int32(given), RightsHas: int32(has)})
	}
	return out
}

// seedFriends installs the login response's list, before any
// notification can arrive.
func (a *Agent) seedFriends(bs []Buddy) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.friends == nil {
		a.friends = map[msg.UUID]*Friend{}
	}
	for _, b := range bs {
		a.friends[b.ID] = &Friend{Buddy: b}
	}
}

// setOnline records that a friend came or went.
//
// A friendship formed during the session shows up here first: the
// simulator sends an OnlineNotification for the new friend, who was not
// in the login list because they were not a friend when it was written.
// Adding them is the only way that ever becomes visible without logging
// in again.
func (a *Agent) setOnline(ids []msg.UUID, online bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.friends == nil {
		a.friends = map[msg.UUID]*Friend{}
	}
	for _, id := range ids {
		f, ok := a.friends[id]
		if !ok {
			f = &Friend{Buddy: Buddy{ID: id}}
			a.friends[id] = f
		}
		f.Online = online
	}
}

// Friends returns the friend list, sorted by id so the order is stable.
func (a *Agent) Friends() []Friend {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]Friend, 0, len(a.friends))
	for _, f := range a.friends {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID.String() < out[j].ID.String()
	})
	return out
}

// NoteFriend records a friendship formed while the session was up.
//
// The grid tells the side that accepts an offer nothing at all -- see
// the NoteFriend rpc -- so this is how that side ever finds out.  The
// rights stay as they were if the friend is already known, since
// nothing here has learned anything about them.
func (a *Agent) NoteFriend(id msg.UUID, online bool) {
	if id.IsZero() {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.friends == nil {
		a.friends = map[msg.UUID]*Friend{}
	}
	f, ok := a.friends[id]
	if !ok {
		f = &Friend{Buddy: Buddy{ID: id}}
		a.friends[id] = f
	}
	f.Online = online
}
