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

// locking is the state of the locks this connection has asked for.
type locking struct {
	mu      sync.Mutex
	waiting map[string][]chan *pb.Locked
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
		// A waiting lock only answers when it has been given, so this
		// is not a case that should arise.
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
	reply := make(chan *pb.Locked, 1)
	c.locks.await(name, reply)
	defer c.locks.stopAwaiting(name, reply)

	if err := c.sendPacket(stream, &pb.ClientPacket{Body: &pb.ClientPacket_Lock{
		Lock: &pb.Lock{Name: name, Try: try},
	}}); err != nil {
		return false, "", err
	}

	select {
	case l := <-reply:
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

func (l *locking) await(name string, reply chan *pb.Locked) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.waiting == nil {
		l.waiting = map[string][]chan *pb.Locked{}
	}
	l.waiting[name] = append(l.waiting[name], reply)
}

func (l *locking) stopAwaiting(name string, reply chan *pb.Locked) {
	l.mu.Lock()
	defer l.mu.Unlock()
	q := l.waiting[name]
	for i, w := range q {
		if w == reply {
			l.waiting[name] = append(q[:i:i], q[i+1:]...)
			return
		}
	}
}

// deliver hands an answer to whoever asked for that name, oldest first.
func (l *locking) deliver(got *pb.Locked) {
	l.mu.Lock()
	defer l.mu.Unlock()
	q := l.waiting[got.GetName()]
	if len(q) == 0 {
		return
	}
	select {
	case q[0] <- got:
	default:
	}
	l.waiting[got.GetName()] = q[1:]
}
