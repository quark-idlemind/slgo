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
	"errors"
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

// ErrStillBusy is a wait for places that ran out before they came free.
// It is the same whether the daemon said so or this side stopped
// waiting first.
var ErrStillBusy = errors.New("client: the places were still busy when the wait ran out")

// granting is the state of what this connection has asked for.
type granting struct {
	mu      sync.Mutex
	next    uint64
	waiting []*asking // oldest first

	// gaveUp is the requests for places whose caller stopped waiting.
	// The daemon is not told and grants them anyway, and what it grants
	// is given straight back.
	gaveUp map[uint64]bool
}

// asking is one request waiting for its answer.
type asking struct {
	id    uint64
	reply chan *pb.SlotsGranted

	// places says it asked for places, rather than to renew a grant the
	// caller still holds: only the first may be given back unasked.
	places bool
}

// Slots asks for n places, all of them or none, for as long as timeout.
//
// It waits its turn.  A timeout of zero takes the daemon's own default,
// and asking for more than the daemon has anywhere comes back with Why
// set rather than waiting for objects that do not exist.
//
// agent asks for them on one avatar; empty takes them from wherever they
// are free, which may be more than one.
//
// A caller that gives up through ctx need do nothing more: the daemon is
// not told, and what it grants for the request later is given back.
func (c *Conn) Slots(ctx context.Context, n int, timeout time.Duration, agent string) (*Grant, error) {
	return c.askSlots(ctx, n, timeout, 0, agent, false)
}

// SlotsWithin is Slots giving up when the places have not come free
// within wait, with ErrStillBusy.  A wait of zero is Slots.
//
// The wait goes to the daemon in whole seconds, a fraction counted as a
// whole one, and this side stops waiting at the same deadline in case
// the daemon is older than the field and waits on.
func (c *Conn) SlotsWithin(ctx context.Context, n int, timeout, wait time.Duration, agent string) (*Grant, error) {
	return c.askSlots(ctx, n, timeout, wait, agent, false)
}

// TrySlots asks for n places and comes back at once either way.
func (c *Conn) TrySlots(ctx context.Context, n int, timeout time.Duration, agent string) (*Grant, error) {
	return c.askSlots(ctx, n, timeout, 0, agent, true)
}

func (c *Conn) askSlots(ctx context.Context, n int, timeout, wait time.Duration, agent string, try bool) (*Grant, error) {
	if n < 1 {
		return nil, fmt.Errorf("client: a request for %d objects", n)
	}
	stream := c.streamOrErr()
	if stream == nil {
		return nil, fmt.Errorf("client: not connected")
	}
	var secs uint32
	if wait > 0 && !try {
		secs = uint32((wait + time.Second - 1) / time.Second)
	}

	// Registered before asking, so an answer cannot arrive first.
	w := c.grants.await(true)
	return c.ask(ctx, stream, w, &pb.ClientPacket{Body: &pb.ClientPacket_Slots{
		Slots: &pb.Slots{
			Want: uint32(n), Seconds: uint32(timeout / time.Second),
			Try: try, Agent: agent, Request: w.id, WaitSeconds: secs,
		},
	}}, time.Duration(secs)*time.Second)
}

// Renew puts a grant's clock back, for work that cannot say in advance
// how long it will take.
func (c *Conn) RenewSlots(ctx context.Context, id string, timeout time.Duration) (*Grant, error) {
	stream := c.streamOrErr()
	if stream == nil {
		return nil, fmt.Errorf("client: not connected")
	}
	w := c.grants.await(false)
	return c.ask(ctx, stream, w, &pb.ClientPacket{Body: &pb.ClientPacket_RenewSlots{
		RenewSlots: &pb.RenewSlots{Grant: id, Seconds: uint32(timeout / time.Second), Request: w.id},
	}}, 0)
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
	return c.sendPacket(stream, &pb.ClientPacket{Body: &pb.ClientPacket_ReleaseSlots{
		ReleaseSlots: &pb.ReleaseSlots{Grant: id, Clean: clean},
	}})
}

// ask sends a request and waits for the answer to it, for no longer
// than within when that is not zero.
//
// A caller that stops waiting leaves a grant nobody will use, which
// holds the places until it runs out or this stream ends.  So one that
// arrives for it, or had just arrived, goes straight back.
func (c *Conn) ask(ctx context.Context, stream pb.Grid_StreamClient, w *asking, p *pb.ClientPacket, within time.Duration) (*Grant, error) {
	var (
		lapsed   <-chan time.Time
		deadline time.Time
	)
	if within > 0 {
		deadline = time.Now().Add(within)
		t := time.NewTimer(within)
		defer t.Stop()
		lapsed = t.C
	}
	err := c.sendPacket(stream, p)
	if err == nil {
		select {
		case got := <-w.reply:
			g := grantOf(got)
			// A refusal once the deadline has passed is the daemon's
			// word that the wait ran out: it started its clock after
			// this one, so it cannot say so any sooner.
			if within > 0 && !g.Held() && !time.Now().Before(deadline) {
				return nil, ErrStillBusy
			}
			return g, nil
		case <-lapsed:
			err = ErrStillBusy
		case <-c.Done():
			err = c.Err()
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if late := c.grants.giveUp(w); late != "" {
		c.giveBack(late)
	}
	return nil, err
}

func grantOf(got *pb.SlotsGranted) *Grant {
	g := &Grant{ID: got.GetGrant(), Why: got.GetWhy()}
	if at := got.GetExpires(); at != 0 {
		g.Expires = time.Unix(at, 0)
	}
	for _, h := range got.GetHeld() {
		g.Places = append(g.Places, Place{
			Agent: h.GetAgent(), Slot: int(h.GetSlot()), Dirty: h.GetDirty(),
		})
	}
	return g
}

// giveBack releases a grant nobody asked for any longer, NOT clean:
// nothing was done in the objects, and clean would wipe out whatever
// mark the last holder left on them.
func (c *Conn) giveBack(id string) {
	_ = c.ReleaseSlots(id, false)
}

// await registers a request and numbers it.  places says it asks for
// places rather than to renew a grant.
func (g *granting) await(places bool) *asking {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	w := &asking{id: g.next, reply: make(chan *pb.SlotsGranted, 1), places: places}
	g.waiting = append(g.waiting, w)
	return w
}

// giveUp stops waiting.  It returns the grant to give back when the
// answer had already arrived, and otherwise remembers the request so
// that deliver gives back what arrives for it.
func (g *granting) giveUp(w *asking) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, x := range g.waiting {
		if x == w {
			g.waiting = append(g.waiting[:i:i], g.waiting[i+1:]...)
			if w.places {
				if g.gaveUp == nil {
					g.gaveUp = map[uint64]bool{}
				}
				g.gaveUp[w.id] = true
			}
			return ""
		}
	}
	select {
	case got := <-w.reply:
		if w.places {
			return got.GetGrant()
		}
	default:
	}
	return ""
}

// deliver hands an answer to the request it names.  It returns a grant
// to give back when that request was given up on.
//
// An answer naming no request is from a daemon older than the numbers.
// It goes to whoever has waited longest, which is right as long as one
// request is out at a time.
// Why: doc/slots.md#asking-and-being-answered
func (g *granting) deliver(got *pb.SlotsGranted) (giveBack string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	i := -1
	if id := got.GetRequest(); id == 0 {
		if len(g.waiting) > 0 {
			i = 0
		}
	} else {
		for j, w := range g.waiting {
			if w.id == id {
				i = j
				break
			}
		}
		if i < 0 && g.gaveUp[id] {
			delete(g.gaveUp, id)
			return got.GetGrant()
		}
	}
	if i < 0 {
		return ""
	}
	w := g.waiting[i]
	g.waiting = append(g.waiting[:i:i], g.waiting[i+1:]...)
	select {
	case w.reply <- got:
	default:
	}
	return ""
}
