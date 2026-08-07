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

// Info is what the session knows about itself before anything is
// asked.
type Info struct {
	// Name is what the session is filed under: the profile name in
	// slgod, or "direct" when this process holds it.
	Name string

	AgentID   msg.UUID
	SessionID msg.UUID

	AvatarName    string
	Region        string
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

	// Messages is the relay: every message this session subscribed
	// to, undecoded.  Closed when the session ends.
	Messages() <-chan *Message

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
