package client

// Asking slgod for a number of the shared objects at once.
//
// Like a lock, this lives on the stream and for the same reason: what a
// client holds has to be given back when it dies, and the stream is what
// slgod already watches for that.  Nothing here says it is still alive.
//
// Unlike a lock, the answer is for a COUNT rather than a name, and it
// may be answered out of more than one avatar.  What comes back says
// which avatar each place belongs to; turning a place into an object is
// the caller's business, because the caller is the one that knows what
// the objects are and can make one that is missing.

import (
	"context"
	"fmt"
	"sync"
	"time"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// A Place is one object to run in: which avatar's, and which of its
// places.
type Place struct {
	Agent string
	Slot  int

	// Dirty says the last holder did not leave it fit to use, so
	// whatever it was running may still be running.  Clear it before
	// listening to it: chat carries the object a line came from and
	// never the script's name, so a script left running is a line this
	// caller would read as its own.
	Dirty bool
}

// A Grant is a number of places, held until it is released or runs out.
type Grant struct {
	// ID names this grant to slgod.  Empty when nothing was granted.
	ID string

	// Places is where to run, one per object.
	Places []Place

	// Expires is when the grant runs out.  Renew before then if there is
	// still work to do.
	Expires time.Time

	// Why is what the daemon said when it granted nothing.
	Why string
}

// Held is whether anything was granted.
func (g *Grant) Held() bool { return g != nil && g.ID != "" }

// granting is the state of what this connection has asked for.
type granting struct {
	mu      sync.Mutex
	waiting []chan *pb.SlotsGranted
}

// Slots asks for n places, all of them or none, for as long as timeout.
//
// It waits its turn.  A timeout of zero takes the daemon's own default,
// and asking for more than the daemon has anywhere comes back with Why
// set rather than waiting for objects that do not exist.
//
// agent asks for them on one avatar; empty takes them from wherever they
// are free, which may be more than one.
func (c *Conn) Slots(ctx context.Context, n int, timeout time.Duration, agent string) (*Grant, error) {
	return c.askSlots(ctx, n, timeout, agent, false)
}

// TrySlots asks for n places and comes back at once either way.
func (c *Conn) TrySlots(ctx context.Context, n int, timeout time.Duration, agent string) (*Grant, error) {
	return c.askSlots(ctx, n, timeout, agent, true)
}

func (c *Conn) askSlots(ctx context.Context, n int, timeout time.Duration, agent string, try bool) (*Grant, error) {
	if n < 1 {
		return nil, fmt.Errorf("client: a request for %d objects", n)
	}
	stream := c.streamOrErr()
	if stream == nil {
		return nil, fmt.Errorf("client: not connected")
	}

	// Registered before asking, so an answer cannot arrive first.
	reply := make(chan *pb.SlotsGranted, 1)
	c.grants.await(reply)
	defer c.grants.stopAwaiting(reply)

	if err := stream.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Slots{
		Slots: &pb.Slots{
			Want: uint32(n), Seconds: uint32(timeout / time.Second),
			Try: try, Agent: agent,
		},
	}}); err != nil {
		return nil, err
	}
	return c.awaitGrant(ctx, reply)
}

// Renew puts a grant's clock back, for work that cannot say in advance
// how long it will take.
func (c *Conn) RenewSlots(ctx context.Context, id string, timeout time.Duration) (*Grant, error) {
	stream := c.streamOrErr()
	if stream == nil {
		return nil, fmt.Errorf("client: not connected")
	}
	reply := make(chan *pb.SlotsGranted, 1)
	c.grants.await(reply)
	defer c.grants.stopAwaiting(reply)

	if err := stream.Send(&pb.ClientPacket{Body: &pb.ClientPacket_RenewSlots{
		RenewSlots: &pb.RenewSlots{Grant: id, Seconds: uint32(timeout / time.Second)},
	}}); err != nil {
		return nil, err
	}
	return c.awaitGrant(ctx, reply)
}

// ReleaseSlots gives a grant back.
//
// clean says the objects were left fit for the next caller -- nothing of
// this caller's still running in them.  Saying so when it is not true is
// how the next caller comes to read somebody else's output as its own,
// so it is worth being sure.
func (c *Conn) ReleaseSlots(id string, clean bool) error {
	if id == "" {
		return nil
	}
	stream := c.streamOrErr()
	if stream == nil {
		return fmt.Errorf("client: not connected")
	}
	return stream.Send(&pb.ClientPacket{Body: &pb.ClientPacket_ReleaseSlots{
		ReleaseSlots: &pb.ReleaseSlots{Grant: id, Clean: clean},
	}})
}

func (c *Conn) awaitGrant(ctx context.Context, reply chan *pb.SlotsGranted) (*Grant, error) {
	select {
	case got := <-reply:
		g := &Grant{ID: got.GetGrant(), Why: got.GetWhy()}
		if at := got.GetExpires(); at != 0 {
			g.Expires = time.Unix(at, 0)
		}
		for _, h := range got.GetHeld() {
			g.Places = append(g.Places, Place{
				Agent: h.GetAgent(), Slot: int(h.GetSlot()), Dirty: h.GetDirty(),
			})
		}
		return g, nil
	case <-c.Done():
		return nil, c.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *granting) await(reply chan *pb.SlotsGranted) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.waiting = append(g.waiting, reply)
}

func (g *granting) stopAwaiting(reply chan *pb.SlotsGranted) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, w := range g.waiting {
		if w == reply {
			g.waiting = append(g.waiting[:i:i], g.waiting[i+1:]...)
			return
		}
	}
}

// deliver hands an answer to whoever asked, oldest first.
//
// Oldest first because there is nothing in the answer to match it to a
// request: a grant is named by the daemon, so the name arrives WITH the
// answer rather than being something the asker chose.  One connection
// asking for two lots at once would have to tell them apart by order,
// which is what this does.
func (g *granting) deliver(got *pb.SlotsGranted) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.waiting) == 0 {
		return
	}
	select {
	case g.waiting[0] <- got:
	default:
	}
	g.waiting = g.waiting[1:]
}
