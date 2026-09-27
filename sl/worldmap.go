package sl

// Finding a region on the grid's map, by name.
//
// # Why a name has to be asked about at all
//
// A person types a name and TeleportLocationRequest takes a handle, so
// something has to turn one into the other, and nothing local can:
// where a region stands is a fact about the grid and not about this
// session.  MapNameRequest is the question -- it is what the viewer
// sends when a name is typed into the world map
// (llworldmapmessage.cpp:79-95, sendNamedRegionRequest) -- and
// MapBlockReply is the answer, whose blocks carry the grid coordinates
// msg.RegionHandle packs.  Nothing here teleports; this is the lookup a
// teleport will need, and it answers "where is that" on its own.
//
// Everything below was measured on Agni, and each of the four is a way
// the reply is not the tidy list its field names suggest.
//
// # It is a case-insensitive PREFIX search
//
// "Sandbox" comes back with thirty-three regions and none of them is
// called Sandbox.  A name is matched from the start and without regard
// to case, so an exact name is one row among however many others begin
// with it -- which makes several matches the ordinary case rather than
// the exception.  So the list is handed over whole and nothing here
// picks from it: choosing the first row would be choosing on behalf of
// whoever typed the name.
//
// # The list ends with a block that is not a region
//
// Every reply ends with a block of X=0, Y=0, Access=255 and a zero map
// image, whose Name is the query lowercased with its last character
// taken off -- "pelm rea" for "Pelmar Reach".  It is the end of the list
// and not a place, and it is recognised here by that shape rather than
// by rebuilding the mangled name, which is a rule about the query where
// the shape is a rule about the block.  The viewer knows the same block
// by the same 255 (llworldmapmessage.cpp:266-274).
//
// A name that matches nothing comes back as that block ALONE.  That, and
// not an empty reply, is how the grid says there is no such region.
//
// # One answer can be several packets
//
// "Sandbox" came back as 26 blocks and then 8, with the end marker in
// the second.  So this accumulates until the marker arrives rather than
// until the first reply -- and still needs a deadline, because a
// question that goes unanswered has no marker either.  A reply that does
// come takes about 110 milliseconds.
//
// # Most of a block is empty
//
// Agents, RegionFlags and WaterHeight came back zero for every region on
// every run, so none of them is passed on: a region reported as having
// no water and nobody in it would be this package inventing facts out of
// fields the grid does not fill in.  What a block really carries is the
// position and the maturity rating.
//
// # Two clients asking at once
//
// A reply says nothing about which question provoked it.  Profile and
// ScriptRunning both sift their answers by an id the reply carries;
// MapBlockReply carries AgentData.AgentID, which is this avatar in every
// reply this session will ever see, and the blocks.  And the daemon
// relays by message number alone (Client.wants, in server/grpc.go), so
// blocks one attached client asked for reach every client subscribed to
// them.
//
// Half of that is fixable and is fixed here.  The search is by prefix,
// so a block whose name does not begin with the name THIS call asked
// about is not an answer to this question, and is dropped.  That is the
// search's own behaviour rather than a guess about it, and it keeps
// another query's regions out of this one's list entirely.
//
// The other half is not fixable and is worth knowing before trusting a
// listing.  The end marker is the only end-of-list signal there is, and
// its shape -- no coordinates, an access code of 255 -- is the whole of
// what identifies it.  The mangled name it carries would attribute it to
// a query, but that is a rule about what was asked rather than about the
// block, and nothing promises the mangling; so the shape is what is
// matched, and another lookup's marker arriving on this session while
// this one is waiting ends this list early.  What that costs is a
// listing shorter than the grid's answer rather than a wrong one, since
// every row in it matched the name asked about.  It takes two lookups
// running at once on one session to reach, which is why it is written
// down here rather than guarded against.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// The maturity ratings, which a map block's Access and a region
// handshake's SimAccess both carry (indra_constants.h:163-168).
const (
	AccessGeneral  uint8 = 13
	AccessModerate uint8 = 21
	AccessAdult    uint8 = 42
)

// AccessName is a maturity rating in words.
//
// Anything else keeps its number rather than being called something: the
// list above is what Second Life uses, and a rating this package has
// never seen is one it cannot name without guessing.
func AccessName(a uint8) string {
	switch a {
	case AccessGeneral:
		return "general"
	case AccessModerate:
		return "moderate"
	case AccessAdult:
		return "adult"
	}
	return fmt.Sprintf("access %d", a)
}

// mapListEnd is the Access carried by the block that ends a list of map
// blocks.  It is not a rating.  See the head of this file.
const mapListEnd uint8 = 255

// mapLayerFlag is what a map request puts in Flags: LAYER_FLAG, the only
// flag the viewer ever sends here (llworldmapmessage.cpp:41 and :90).
//
// Zero works as well and is the difference between a zero MapImageID and
// a real one -- the map tile, a texture this client can fetch and cannot
// show.  So the viewer's value is used: matching it costs nothing, and
// the one datum the other buys is one nothing here would look at.
const mapLayerFlag = 2

// MapRegion is one region as the map describes it.
//
// Not Region, which is what the simulator said about itself in the
// handshake.  That is the region this avatar is standing in, in thirty
// fields, none of which anything else on the grid will answer for; this
// is a row about somewhere the avatar has never been, in four.  One name
// for both would invite one to be passed where the other was meant.
type MapRegion struct {
	Name string

	// X and Y are the grid square and Handle is the same place as the
	// protocol names it.  Both, because the coordinates are what a
	// person reads and the handle is what a teleport takes.
	X, Y   uint32
	Handle uint64

	// Access is the maturity rating: see AccessName.  It is the only
	// thing besides the position that a block really carries.
	Access uint8
}

// FindRegions asks the grid's map which regions a name matches.
//
// The name is a prefix and the match ignores case, so this returns a
// list and not a region -- an exact name is one row of it.  The rows are
// in name order, which is settled here rather than left to the grid: the
// blocks arrive in whatever order the map walked and across however many
// packets it took, so two identical questions can otherwise answer with
// two different listings.
//
// A name nothing matches is an error naming it.  That is the grid's own
// answer rather than an absence to be reported as an empty list: it
// replies with the end of the list and nothing before it, which is as
// definite as the map ever gets.
//
// Only regions whose names begin with the name asked about are returned,
// because the relay this runs over carries another client's answers here
// as well as ours.  See the head of this file for what that catches and
// what it does not.
//
// The timeout is how long to wait for the whole answer.  Reaching it is
// a real timeout and not "no such region": the end of the list never
// arrived, so what did arrive is a fragment, and a fragment reported as
// the answer would be a shorter grid than the real one.
func (w *Session) FindRegions(ctx context.Context, name string, timeout time.Duration) ([]MapRegion, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("sl: FindRegions needs a name to look for")
	}
	if timeout == 0 {
		timeout = 15 * time.Second
	}

	var found []MapRegion
	var ended bool
	done := make(chan struct{}, 1)
	want := strings.ToLower(name)

	// Listening before asking, as Profile and ScriptRunning do.  The
	// reply is a message like any other and it comes back in about a
	// tenth of a second, so a listener opened after the send is one that
	// loses the race often enough to matter and silently when it does.
	stop := w.onMapBlocks(func(blocks []msg.MapBlockReply_Data) {
		for i := range blocks {
			b := &blocks[i]
			if b.X == 0 && b.Y == 0 && b.Access == mapListEnd {
				ended = true
				continue
			}
			// Somebody else's answer, on a relay that carries every
			// client's: this question was about a prefix, so a name that
			// does not begin with it is not an answer to it.
			region := trimNul(b.Name)
			if !strings.HasPrefix(strings.ToLower(region), want) {
				continue
			}
			x, y := uint32(b.X), uint32(b.Y)
			found = append(found, MapRegion{
				Name:   region,
				X:      x,
				Y:      y,
				Handle: msg.RegionHandle(x, y),
				Access: b.Access,
			})
		}
		select {
		case done <- struct{}{}:
		default:
		}
	})
	defer stop()

	m := &msg.MapNameRequest{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentData.Flags = mapLayerFlag
	// EstateID and Godlike are left as they are: the simulator fills
	// both in, and the viewer sends zero and false for that reason
	// (llworldmapmessage.cpp:92-93).
	m.NameData.Name = append([]byte(name), 0)
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(timeout)
	for {
		w.mu.Lock()
		regions, complete := append([]MapRegion(nil), found...), ended
		w.mu.Unlock()
		if complete {
			if len(regions) == 0 {
				return nil, fmt.Errorf("sl: no region on this grid is called %q "+
					"or begins with it", name)
			}
			sort.Slice(regions, func(i, j int) bool {
				if a, b := strings.ToLower(regions[i].Name), strings.ToLower(regions[j].Name); a != b {
					return a < b
				}
				// Two regions may share a name, and the order still has
				// to be an order.
				return regions[i].Handle < regions[j].Handle
			})
			return regions, nil
		}

		t := time.NewTimer(time.Until(deadline))
		select {
		case <-done:
			t.Stop()
		case <-t.C:
			// What had arrived is worth saying: nothing at all and most
			// of an answer are different things to do something about.
			return nil, fmt.Errorf("%w: the map's answer about %q; the end of the "+
				"list never came (matching regions so far: %d)",
				ErrTimeout, name, len(regions))
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
}

// onMapBlocks is the internal subscription FindRegions waits on.
//
// Delivered rather than stored.  A map reply is what the grid said about
// a name at the moment it was asked, and the interesting part of it --
// where the list ends -- belongs to the question that provoked it and to
// nothing else.
func (w *Session) onMapBlocks(fn func(blocks []msg.MapBlockReply_Data)) (stop func()) {
	w.mu.Lock()
	w.mapFns = append(w.mapFns, fn)
	i := len(w.mapFns) - 1
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		if i < len(w.mapFns) {
			w.mapFns[i] = nil
		}
		w.mu.Unlock()
	}
}

// mapBlocks hands one reply's blocks to whoever asked for them.
//
// The subscriptions are called with the lock HELD, which is what
// avatarReplyTo does and is deliberate for the same reason: an answer is
// assembled from several packets arriving on the reader goroutine while
// the caller waits on another, so the lock is what makes the assembling
// safe to read.  Nothing registered here does anything but append to a
// slice.
func (w *Session) mapBlocks(blocks []msg.MapBlockReply_Data) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, fn := range w.mapFns {
		if fn != nil {
			fn(blocks)
		}
	}
}
