package sl

// The two ways to be connected, behind one interface.
//
// Everything above this line is the same either way: a program says
// Build or Say or ListInventory and never learns which it got.  What
// differs is only where the session lives -- in slgod, which outlives
// the program, or in this process, which does not.
//
// The interface is deliberately in this package's own types rather than
// in the protobuf ones.  If it spoke protobuf, the direct backend would
// have to build protobuf messages for a wire it is not using, and the
// conversions in the server would be mirrored here.  Instead each side
// converts once, in its own direction: hosted turns protobuf into
// these, direct turns the agent's own state into these.

import (
	"context"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
)

// Message is one message relayed from the grid, still undecoded.
//
// An alias rather than a new type: the transfer machinery in client
// takes these, and a session that had to convert on the way past would
// be copying every packet to rename it.
type Message = client.Message

// QueueEvent is one entry from the grid's event queue, still LLSD
// encoded.
//
// The queue carries what the circuit no longer does.  A message the
// template marks UDPDeprecated is not gone: the simulator sends it here
// instead, under its own name and with its blocks as LLSD, and it may
// carry fields the template never had.  ScriptRunningReply is the one
// this package reads; see Session.event.
//
// Not "Event", which this package already uses for one of the handlers
// an LSL state declares (see Syntax).  The two have nothing to do with
// each other and the shorter name was there first.
type QueueEvent = client.Event

// RegionChange is the avatar being somewhere else: the name and handle
// of the region it is in now.
//
// An alias for the same reason Message is one: the hosted backend is
// handed these by the client package and would otherwise copy each one
// to rename it.
type RegionChange = client.RegionChange

// Info is what the session knows about itself before anything is
// asked.
type Info struct {
	// Name is what the session is filed under: the profile name in
	// slgod, or "direct" when this process holds it.
	Name string

	AgentID   msg.UUID
	SessionID msg.UUID

	AvatarName string

	// Region is where the avatar was when this session attached, and
	// it is not revised afterwards.
	//
	// That is the contract above working as written rather than a
	// fault to fix: this whole struct is what was known at attach time,
	// and a field that quietly changed under a caller would be worse
	// than one that plainly does not.  An avatar that teleports is
	// somewhere else and this still says where it started, so ask
	// Session.Where, or subscribe to Session.RegionChanges to be told.
	Region string

	InventoryRoot msg.UUID

	// Channel is the client name and version the login server was
	// given, and Caps are the capabilities the simulator offered.
	Channel string
	Caps    []string
}

// HasCap reports whether the simulator offered a capability.
func (i *Info) HasCap(name string) bool {
	for _, c := range i.Caps {
		if c == name {
			return true
		}
	}
	return false
}

// Friend is somebody on the friend list, and whether they are logged
// in.
//
// The rights are bit masks: 1 may see me online, 2 may see me on the
// map, 4 may edit my objects.  Given is what this avatar granted, Has
// what was granted back.  Both are zero for a friendship formed during
// the session, which arrives as a notification rather than in the login
// response.
type Friend struct {
	ID          msg.UUID
	Online      bool
	RightsGiven int32
	RightsHas   int32
}

// Backend is a way to reach the grid.
//
// Hosted talks to slgod; Direct holds the connection here.  Nothing
// above this interface may care which, so anything that only one of
// them can do -- listing the sessions a daemon holds, logging the
// avatar out -- is a method on that one and not part of this.
type Backend interface {
	// Info is what was known at attach time and does not change.
	Info() *Info

	// Send puts a message on the circuit.
	Send(ctx context.Context, m msg.Message, reliable bool) error

	// Control sends one AgentUpdate carrying these control flags and
	// then forgets them.
	//
	// It is here rather than being built by the caller and handed to
	// Send because an AgentUpdate carries the camera, its axes and the
	// draw distance as well as the flags, and the simulator scopes its
	// interest list by them.  Only the side that owns the camera can
	// send one that is right about everything except the bit being
	// asked for; a caller has none of it and would be inventing a
	// camera the simulator would then believe.  See agent.Control.
	Control(ctx context.Context, flags uint32) error

	// Messages is the relay: every message this session subscribed
	// to, undecoded.  Closed when the session ends.
	Messages() <-chan *Message

	// Events is the other relay: what arrived on the grid's event
	// queue rather than on the circuit.
	//
	// Both are needed, and neither replaces the other.  A message
	// marked UDPDeprecated in the template stops arriving on the
	// circuit and starts arriving here, so a session reading only
	// Messages waits for ever on confirmations the simulator has
	// already sent -- which is exactly what ScriptRunning did before
	// this existed.
	//
	// A nil channel is a valid answer from a backend that has no
	// queue to offer, and blocks rather than ending the session.
	Events() <-chan *QueueEvent

	// RegionChanges is the third relay: the avatar has been moved to
	// another region, and everything keyed on the one it was in is
	// stale.
	//
	// A relay rather than a question, because there is no question to
	// ask.  Where says which region the avatar is in now and cannot
	// tell one teleport from two, so a caller that has to act at the
	// moment it happens -- to drop what it holds, or to say something
	// to a person -- must be told rather than look.
	//
	// A nil channel is a valid answer, as for Events, and is what a
	// backend with no way of hearing about one gives.
	RegionChanges() <-chan *RegionChange

	// Done closes when the session ends, and Err says why.
	Done() <-chan struct{}
	Err() error

	// Presence is where the avatar is and how far it is being asked
	// to see.  A draw distance above zero sets it.
	Presence(ctx context.Context, drawDistance float32) (*Presence, error)

	// Objects is what the region has said about itself, filtered by
	// name or id when either is given.
	//
	// This is the clearest case for the rule in the package doc: a
	// region describes itself once, when the avatar arrives, so a
	// session that attached later never heard any of it and has to
	// ask whoever did.
	Objects(ctx context.Context, named, id string) ([]*Seen, error)

	// Region is what the simulator said in the handshake, and
	// whether it has arrived at all.
	Region(ctx context.Context) (*Region, bool, error)

	// Land is what the session was told about the ground under the
	// avatar: the parcel it was pushed when it arrived, and the
	// region's parcel overlay.
	//
	// Here for the reason Region and Objects are: it arrives unasked,
	// once, before a client is listening.  The overlay especially --
	// four packets on arrival and none afterwards, so a session that
	// has been up for hours is the only thing that still has it.
	Land(ctx context.Context) (*Land, error)

	// Neighbours is the circuits held to the regions AROUND that one,
	// and whether the session is holding any at all.  A non-nil set
	// turns them on or off first, and the answer describes what is
	// held after the change.
	//
	// A pointer rather than a bool, because there are three requests
	// and not two: leave it alone, turn it on, turn it off.  Presence
	// spells the first as a zero draw distance and gets away with it
	// only because nobody wants a draw distance of zero.
	Neighbours(ctx context.Context, set *bool) (*Neighbours, error)

	// Lock takes exclusive use of something named, waiting for it,
	// and Unlock gives it back.  Going away gives it back too.
	//
	// The name means nothing to the backend; it is whatever the
	// programs sharing the thing agree to call it.  What it is FOR is
	// a resource in the world that two programs cannot share -- the
	// object automate and autobench run scripts in, whose linkset data
	// belongs to the object and not to the script.
	//
	// A direct session holds it trivially: one avatar cannot be logged
	// in twice, so a process that logged in itself has no one to
	// contend with.
	Lock(ctx context.Context, name string) error
	Unlock(name string) error

	// TryLock takes one only if it is free, and says whether it got
	// it and who holds it otherwise.  This is what lets a caller pick
	// among several interchangeable things -- one of a pool of
	// objects, one of several avatars -- without queueing on the
	// first and without the deadlock that asking for several at once
	// invites.
	TryLock(ctx context.Context, name string) (bool, string, error)

	// Flush empties the object cache, and says how much it held.
	Flush(ctx context.Context) (int, error)

	// Friends is the friend list and who is logged in.
	Friends(ctx context.Context) ([]Friend, error)

	// NoteFriend records a friendship formed while connected, which
	// is the one thing the grid never reports: whoever accepts an
	// offer is told nothing at all.
	NoteFriend(ctx context.Context, id msg.UUID, online bool) error

	// HasCap and DoCap reach the simulator's http capabilities.
	HasCap(name string) bool
	DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error)

	// Close hangs up.  For a hosted session that leaves the avatar
	// logged in; for a direct one it does not, since there is nobody
	// else holding it.
	Close() error
}

// A Watcher is a backend that filters what it relays and can be told to
// filter differently while it runs.
//
// Only a hosted session is one.  A direct session relays everything the
// circuit carries -- there is nothing between the socket and the reader
// to filter with -- so it does not implement this, and a caller that
// finds no Watcher should conclude that everything is already arriving
// rather than that nothing can be asked for.
//
// What it is for is a message that is needed for the length of one
// command and too expensive to keep: AvatarAnimation, which is the whole
// of the evidence that a ground sit happened and which arrives for every
// avatar in range, in full, about every three seconds.  See sit.go.
type Watcher interface {
	Watch(names ...string) error
	Unwatch(names ...string) error
}
