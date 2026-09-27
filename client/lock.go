package client

// Asking slgod for exclusive use of something named.
//
// The lock lives on the stream rather than in an RPC of its own, and
// that is the whole point: a lock is only worth having if it is given
// back when its holder dies, and the stream is what slgod already
// watches for exactly that.  Nothing here has to say it is still alive.

import (
	"context"
	"fmt"
	"sync"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// locking is the requests for a lock waiting for their answers.
type locking struct {
	mu      sync.Mutex
	next    uint64
	waiting []*lockAsk // oldest first
}

// lockAsk is one request waiting for its answer.
type lockAsk struct {
	id    uint64
	name  string
	reply chan *pb.Locked
}

// Lock takes a named lock, waiting for it.
//
// It returns when the lock is this client's, or when ctx is done, or
// when the connection ends.  Releasing it is Unlock, and so is going
// away: slgod gives back everything a client holds when its stream
// ends, however it ended.
func (c *Conn) Lock(ctx context.Context, name string) error {
	held, _, err := c.lock(ctx, name, false)
	if err != nil {
		return err
	}
	if !held {
		// A waiting lock only answers when it has been given.  A
		// daemon older than request numbers is matched by name,
		// oldest waiter first, so from one of those a TryLock of the
		// same name on this connection meanwhile can have its "not
		// held" handed to this wait.
		return fmt.Errorf("client: waiting for the %q lock came back without it", name)
	}
	return nil
}

// TryLock takes a named lock if it is free, and otherwise says who has
// it rather than waiting.
func (c *Conn) TryLock(ctx context.Context, name string) (bool, string, error) {
	return c.lock(ctx, name, true)
}

func (c *Conn) lock(ctx context.Context, name string, try bool) (bool, string, error) {
	if name == "" {
		return false, "", fmt.Errorf("client: a lock needs a name")
	}
	stream := c.streamOrErr()
	if stream == nil {
		return false, "", fmt.Errorf("client: not connected")
	}

	// Registered before asking, so an answer cannot arrive first.
	w := c.locks.await(name)
	defer c.locks.stopAwaiting(w)

	if err := c.sendPacket(stream, &pb.ClientPacket{Body: &pb.ClientPacket_Lock{
		Lock: &pb.Lock{Name: name, Try: try, Request: w.id},
	}}); err != nil {
		return false, "", err
	}

	select {
	case l := <-w.reply:
		return l.GetHeld(), l.GetHolder(), nil
	case <-c.Done():
		return false, "", c.Err()
	case <-ctx.Done():
		// Giving up on a wait has to be said, or slgod hands the lock
		// to somebody who is no longer listening for it.
		_ = c.sendPacket(stream, &pb.ClientPacket{Body: &pb.ClientPacket_Unlock{
			Unlock: &pb.Unlock{Name: name},
		}})
		return false, "", ctx.Err()
	}
}

// Unlock gives a lock back.
func (c *Conn) Unlock(name string) error {
	stream := c.streamOrErr()
	if stream == nil {
		return fmt.Errorf("client: not connected")
	}
	return c.sendPacket(stream, &pb.ClientPacket{Body: &pb.ClientPacket_Unlock{
		Unlock: &pb.Unlock{Name: name},
	}})
}

func (c *Conn) streamOrErr() pb.Grid_StreamClient {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stream
}

// await registers a request for name and numbers it.
func (l *locking) await(name string) *lockAsk {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	w := &lockAsk{id: l.next, name: name, reply: make(chan *pb.Locked, 1)}
	l.waiting = append(l.waiting, w)
	return w
}

func (l *locking) stopAwaiting(w *lockAsk) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, x := range l.waiting {
		if x == w {
			l.waiting = append(l.waiting[:i:i], l.waiting[i+1:]...)
			return
		}
	}
}

// deliver hands an answer to the request it names.
//
// An answer naming no request is from a daemon older than the numbers,
// and goes to whoever has waited longest for that name, which is right
// as long as one request for a name is out at a time.
// Why: doc/slots.md#asking-and-being-answered
func (l *locking) deliver(got *pb.Locked) {
	l.mu.Lock()
	defer l.mu.Unlock()
	id := got.GetRequest()
	for i, w := range l.waiting {
		if id != 0 && w.id == id || id == 0 && w.name == got.GetName() {
			l.waiting = append(l.waiting[:i:i], l.waiting[i+1:]...)
			select {
			case w.reply <- got:
			default:
			}
			return
		}
	}
}
