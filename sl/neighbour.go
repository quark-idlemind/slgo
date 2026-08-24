package sl

// The regions around the one the avatar is in.
//
// A simulator will not hand an avatar over a border to a client that
// holds no circuit to the region on the other side, so walking out of a
// region is something a session either can do or cannot, and which it
// is can be changed while the session is up.  It is asked for and set
// here; everything it takes is in agent/neighbour.go and the reasoning
// is in doc/history/neighbours.md.
//
// This package holds none of it.  A circuit belongs to whoever owns the
// socket -- slgod for a hosted session, this process for a direct one
// -- and what crosses is a description of what is held.

import "context"

// Neighbours is what a session holds of the regions around it.
type Neighbours struct {
	// On is whether the session takes up the offers a simulator makes
	// of the regions beside it.
	//
	// On with nothing held is ordinary rather than broken, and it
	// usually means "not yet" rather than "nothing near".  A simulator
	// offers of its own accord and repeats until the offer is taken:
	// measured on 2026-08-23 the first circuit took between twenty and
	// sixty seconds, and the set went on growing for a minute after
	// that.  Being in the middle of a region does not empty it -- the
	// middle of a 256-metre one is offered all four edges.
	On bool

	// Held is one entry per circuit, in grid handle order.
	Held []Neighbour
}

// A Neighbour is one region beside this one and what the session has of
// it.
type Neighbour struct {
	// Handle is the region's place on the grid.  msg.GridCoords turns
	// it into the square, which is what a person reads.
	Handle uint64

	// Addr is where the offer said the simulator was.
	Addr string

	// Name is what the region called itself in its handshake, and is
	// empty for a circuit that has not been answered.
	Name string

	// Handshook says the simulator answered.  An address that never
	// does is an offer that came to nothing.
	Handshook bool

	// Heard is how many packets have arrived on the circuit.  A
	// neighbour describes itself fully and unprompted -- 59
	// ObjectUpdate and 51 LayerData in thirty seconds, measured -- so
	// a circuit that is alive counts up without being asked.
	Heard uint64
}

// Neighbours asks what the session holds, and changes nothing.
func (w *Session) Neighbours(ctx context.Context) (*Neighbours, error) {
	return w.b.Neighbours(ctx, nil)
}

// SetNeighbours turns the circuits on or off and returns what the
// session holds afterwards.
//
// Two calls rather than one with a flag in it, for the reason Where and
// SetDrawDistance are two: reading is the common thing and asking for a
// change is a decision, and a caller that only wanted to look should not
// have to name a value it is not setting.
//
// Turning them on holds nothing straight away and there is nothing here
// to wait for: the simulator repeats an offer for as long as it goes
// untaken -- 57 times in a 200 second run -- so the answer to a second
// call moments later has what the first was too early for.  Turning them
// off drops what was held, so the answer to that one is empty.
func (w *Session) SetNeighbours(ctx context.Context, on bool) (*Neighbours, error) {
	return w.b.Neighbours(ctx, &on)
}
