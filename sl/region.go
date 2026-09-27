package sl

// Being told the avatar is somewhere else.
//
// A teleport keeps the session and changes the region under it, and
// almost everything this package remembers is about the region rather
// than about the avatar.  Local ids are the region's own numbering and
// the next region starts its own from one, so a local id kept across a
// move is not stale in the harmless sense -- it names something, and
// something else.  That is why this is a clear and not a refresh: what
// is dropped is asked for again from the region we are in, and what is
// kept is what was never the region's to say.
//
// The backend does the finding out.  A hosted session is told by the
// daemon that followed the avatar, a direct one by the agent in this
// process, and Backend.RegionChanges is the same channel either way --
// see backend.go, which is where the reason for that lives.

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// refreshTimeout bounds the ask that follows a region change.  Long
// enough for a daemon that is busy reconnecting, or a region slow to
// give its capabilities, short enough that one which will never answer
// is not waited on for ever.
const refreshTimeout = 30 * time.Second

// DefaultRegionDepth is the buffer a region-change subscription gets
// when none is asked for.
//
// Small, where chat and instant messages get sixty four, because these
// arrive one to a teleport rather than one to a sentence: a subscriber
// eight teleports behind is not a slow reader, it is a reader that has
// stopped.
const DefaultRegionDepth = 8

// regionSub is a subscription to region changes.
type regionSub struct {
	ch      chan *RegionChange
	dropped atomic.Uint64
}

// RegionChanges returns a channel of the moments the avatar arrived in
// another region, and it is closed when StopRegionChanges is called or
// the session ends.
//
// Polling Where is not the same thing and does not substitute for it.
// Where answers which region the avatar is in, which two teleports out
// and back leave exactly as it was; anything that has to act at the
// moment -- drop what it holds, tell a person where they are now -- has
// to be told rather than to look.
//
// The session has already dropped what belonged to the region left
// behind by the time this is delivered, so a subscriber is looking at a
// session that agrees with the news.
func (w *Session) RegionChanges(depth int) <-chan *RegionChange {
	if depth <= 0 {
		depth = DefaultRegionDepth
	}
	sub := &regionSub{ch: make(chan *RegionChange, depth)}
	if !w.addSub(func() { w.regionSubs[sub.ch] = sub }) {
		close(sub.ch)
	}
	return sub.ch
}

// StopRegionChanges closes a subscription, and returns once it is
// closed.
func (w *Session) StopRegionChanges(ch <-chan *RegionChange) {
	w.onReader(func() {
		if s := w.regionSubs[ch]; s != nil {
			delete(w.regionSubs, ch)
			close(s.ch)
		}
	})
}

// RegionChangesDropped is how many a subscription missed because its
// buffer was full.
func (w *Session) RegionChangesDropped(ch <-chan *RegionChange) uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := w.regionSubs[ch]; s != nil {
		return s.dropped.Load()
	}
	return 0
}

// regionChanged is one arrival in another region: what the session
// forgets, and then whoever asked to be told.
//
// Called from the reader goroutine only, like deliver, so that the
// forgetting cannot land in the middle of a message being handled --
// an ObjectUpdate half written into maps that were emptied under it
// would leave exactly the mixture this is here to prevent.
func (w *Session) regionChanged(c *RegionChange) {
	// Who this session is may have changed with it: a daemon
	// re-establishing a dropped session announces itself as a region
	// change, and a re-established session has a new session id.
	// Asked for off this goroutine, because this one is the relay --
	// see refreshIdentity.
	go func() {
		// Bounded: a daemon that never answers must not leave a
		// goroutine here for the life of the process, and the next
		// region change asks again anyway.
		ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
		defer cancel()
		w.refreshIdentity(ctx)
	}()

	w.mu.Lock()
	w.dropRegionState()
	subs := make([]*regionSub, 0, len(w.regionSubs))
	for _, s := range w.regionSubs {
		subs = append(subs, s)
	}
	w.mu.Unlock()

	for _, s := range subs {
		select {
		case s.ch <- c:
		default:
			s.dropped.Add(1)
		}
	}
}

// dropRegionState forgets what belonged to the region the avatar has
// left.  It is called with mu held.
//
// The division is between what the REGION said and what the GRID said.
// A region describes its own objects in its own numbering, to whoever
// is standing in it; the grid knows who people are, who is a friend of
// whom, and what is in this avatar's inventory, and none of that is
// different a region away.  Field by field, and each of them is a
// decision rather than a sweep:
//
//   - at: the visit, which starts afresh with the region's uuid unknown.
//     Every Object found before now carries the old one, so its local id
//     is looked up again before it is sent; see Session.local.
//   - locals, parents, killed: local ids, which are the region's own
//     numbering and are handed out again by the next region.  A local
//     id kept across a move does not go stale, it goes WRONG: it names
//     an object here as confidently as it named one there.  killed is
//     the worst of the three, because a stale kill makes an object that
//     exists read as one that was destroyed.
//   - owners, groups, objectNames: keyed by object id, which is
//     grid-wide, but what they describe is a prim in the region left
//     behind.  They are dropped because they are answers about things
//     nothing here can see, act on or ask about any more, and because a
//     session that kept every region it ever visited would answer
//     questions about a region from what it heard in another one.
//   - attach: the attachments follow the avatar, and are still dropped.
//     The Attached that is kept holds the object id and the local id
//     the LAST region gave them, and the new simulator re-describes
//     every one of them with new ids within seconds of the arrival.  So
//     what is thrown away is about to be replaced by the truth, and
//     what would be kept is a claim that reads right and points at
//     nothing.
//   - taskInv, taskSeen: the filename in a ReplyTaskInventory is a
//     handle the SIMULATOR issued for a transfer, and the object it is
//     about is in the region left.  Asking the new region for it would
//     be asking a stranger about a receipt.
//   - asking: the ids a UUIDNameRequest is still out for.  It is the
//     one entry here that is neither about objects nor obviously the
//     region's, and it goes because it is a promise from a simulator we
//     no longer talk to: the reply will never arrive, and the mark says
//     "already asked" for the rest of the session, so a name never
//     asked for again is a name never learned.  Cleared, the next ask
//     goes to the region we are in.
//
// What is deliberately kept, and why, since a clear that took
// everything would look like this one working:
//
//   - names: who somebody is, which is the grid's answer and no
//     region's.  Some of them were never asked for -- an instant
//     message carries the sender's name -- so one dropped here may
//     never be offered again.
//   - offers, invOffers, lures, tpRequests, invites, asked, dialogs:
//     what people and their scripts have said to this avatar and is
//     still waiting for an answer.  A person who offered friendship, or
//     a teleport, or a group, did not withdraw it because the avatar
//     walked through a door -- and an offer thrown away here is one
//     nobody can accept afterwards.  The friend list itself is not here
//     to keep: it comes from login, and the backend answers for it.
//   - created, and the inventory generally: an item is the avatar's and
//     travels with it.  The inventory comes from login rather than from
//     a region, which is a good part of why a teleport is cheaper than
//     the relog it replaces.
//   - alerts and the collectors: what was said while we were listening.
//     alertsSince quotes the alerts by an index taken before a wait
//     began, so truncating them under a call that is waiting would make
//     a refusal silently unquotable, and a chat collector is the
//     caller's window rather than the region's.
//   - the subscriptions and the waiters: chatSubs, imSubs, permSubs,
//     regionSubs, the pickers, and the propsFns, mapFns, teleportFns,
//     profileFns, scriptFns, parcelFns and dwellFns beside them.  These
//     are not state at all, they are callers waiting, and the teleport
//     that caused this is very probably one of them.  Emptying them
//     would strand every one.
//   - syntax: the LSL a region implements, which looks per-region and
//     is not.  It is a property of the grid's server build, half a
//     megabyte over a capability, and it changes when Linden Lab
//     changes the language rather than when the avatar moves.
//   - xfers and transfers: file transfers in flight, whose ids are the
//     old simulator's.  Nothing is done about them here, because there
//     is no way to fail one and abandoning it would leave its caller
//     waiting for ever rather than timing out with a reason.
func (w *Session) dropRegionState() {
	w.at = newVisit()
	clear(w.locals)
	clear(w.owners)
	clear(w.groups)
	clear(w.objectNames)
	clear(w.parents)
	clear(w.attach)
	clear(w.killed)
	clear(w.taskInv)
	clear(w.taskSeen)
	clear(w.asking)
}

// A visit is one stay in one run of a region, which is what a local id
// belongs to.
//
// n is drawn afresh on every region change, and a re-established
// session arrives as one, so a region that restarted under the avatar
// is a new visit although its uuid is the same.  The numbers are drawn
// for the whole process rather than for each session, so that an Object
// from one Session never matches another's by coincidence.  Zero is no
// visit, which is what an Object built by hand has.
type visit struct {
	region msg.UUID // zero until the backend has said
	n      uint64
}

var visits atomic.Uint64

func newVisit() visit { return visit{n: visits.Add(1)} }

// here is the visit the avatar is on, asking the backend for the
// region's uuid the first time it is wanted in each.  An answer that
// arrives after the avatar has moved again is not kept, and a region
// that cannot be named leaves it zero.
func (w *Session) here(ctx context.Context) visit {
	w.mu.Lock()
	v := w.at
	w.mu.Unlock()
	if !v.region.IsZero() {
		return v
	}
	r, known, err := w.b.Region(ctx)
	if err != nil || !known || r == nil || r.ID.IsZero() {
		return v
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.at.n != v.n {
		return v
	}
	w.at.region = r.ID
	return w.at
}

// current is whether a local id handed out in v means the same prim
// now.  One from a region that was never named is not trusted.  With mu
// held.
func (w *Session) current(v visit) bool {
	return v.n != 0 && !v.region.IsZero() && v == w.at
}

// local is the local id to send for o, and every call that sends one
// takes it from here.
//
// It is o's own if o was found in the visit the avatar is on now.
// Otherwise o is looked up by its id in the region the avatar is in,
// and what that region calls it is written back into o and sent.  An
// answer from a visit that is already over -- the avatar moved during
// the lookup, or the region could not be named -- is looked up once
// more.  One the region has not described, or a second stale answer,
// is refused with ErrNotHere, saying which it was.
// Why: doc/local-ids.md
func (w *Session) local(ctx context.Context, o *Object) (uint32, error) {
	if o == nil {
		return 0, errors.New("sl: no object to send")
	}
	w.mu.Lock()
	now := o.Local != 0 && w.current(o.from)
	w.mu.Unlock()
	if now {
		return o.Local, nil
	}
	if o.ID.IsZero() {
		return 0, fmt.Errorf("sl: local id %d may belong to another region, "+
			"and there is no object id to look it up by", o.Local)
	}
	who := o.ID.String()
	if o.Name != "" {
		who = fmt.Sprintf("%q %s", o.Name, o.ID)
	}
	for try := 1; ; try++ {
		found, err := w.fetch(ctx, "", o.ID.String())
		if err != nil {
			return 0, err
		}
		var s *Seen
		for _, c := range found {
			if c.ID == o.ID && c.Local != 0 {
				s = c
				break
			}
		}
		if s == nil {
			return 0, fmt.Errorf("sl: %s is %w, or is beyond the draw distance", who, ErrNotHere)
		}
		w.mu.Lock()
		ok, moved := w.current(s.from), s.from.n != w.at.n
		w.mu.Unlock()
		if ok {
			o.Local, o.from = s.Local, s.from
			return o.Local, nil
		}
		if try == 2 {
			if moved {
				return 0, notHere("sl: no local id for " + who +
					": the avatar changed region while it was being looked up")
			}
			return 0, notHere("sl: no local id for " + who +
				": the backend has not said which region the avatar is in")
		}
	}
}

// notHere is ErrNotHere for a lookup that could not say which region
// it answered for, in words of its own: "not in this region" may be
// untrue of it.
type notHere string

func (e notHere) Error() string { return string(e) }
func (e notHere) Unwrap() error { return ErrNotHere }

// localNow is o's local id in the visit the avatar is on, as far as the
// session knows without asking: o's own if it is current, or else what
// the updates relayed since arriving said, or zero.  It is for reading
// what the session was told, never for sending.  With mu held.
func (w *Session) localNow(o *Object) uint32 {
	if o.Local != 0 && w.current(o.from) {
		return o.Local
	}
	return w.locals[o.ID]
}
