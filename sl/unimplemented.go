package sl

import (
	"context"
	"errors"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// ErrNotSupported is what an UnimplementedBackend answers to a method
// its embedder did not override: this backend does not do that.
var ErrNotSupported = errors.New("sl: the backend does not support this")

// UnimplementedBackend implements every method of Backend as "not
// supported", to be embedded by a Backend written outside this module so
// that a method added to Backend in a minor release does not stop it
// compiling:
//
//	type myBackend struct{ sl.UnimplementedBackend }
//
// and then override what it does support.
//
// A method that returns an error returns ErrNotSupported, with a nil or
// zero value beside it.  Messages, Events and RegionChanges return
// channels that are already closed and Done one that is closed, so a
// reader ends at once instead of waiting for ever; Err then says
// ErrNotSupported.  HasCap is false and Close is nil.  Info is an empty
// Info, and New refuses a backend whose Info has no AgentID or no
// SessionID, so an embedder overrides Info; it also overrides Messages
// and Done unless the session may end as soon as it starts.
type UnimplementedBackend struct{}

var _ Backend = UnimplementedBackend{}

// The channels every UnimplementedBackend hands out.  Closed once, here,
// and only ever received from.
var (
	closedMessages      = make(chan *Message)
	closedEvents        = make(chan *QueueEvent)
	closedRegionChanges = make(chan *RegionChange)
	closedDone          = make(chan struct{})
)

func init() {
	close(closedMessages)
	close(closedEvents)
	close(closedRegionChanges)
	close(closedDone)
}

func (UnimplementedBackend) Info() *Info { return &Info{} }

func (UnimplementedBackend) Refresh(context.Context) (*Info, error) {
	return nil, ErrNotSupported
}

func (UnimplementedBackend) Send(context.Context, msg.Message, bool) error {
	return ErrNotSupported
}

func (UnimplementedBackend) Control(context.Context, uint32) error { return ErrNotSupported }

func (UnimplementedBackend) Messages() <-chan *Message           { return closedMessages }
func (UnimplementedBackend) Events() <-chan *QueueEvent          { return closedEvents }
func (UnimplementedBackend) RegionChanges() <-chan *RegionChange { return closedRegionChanges }
func (UnimplementedBackend) Done() <-chan struct{}               { return closedDone }
func (UnimplementedBackend) Err() error                          { return ErrNotSupported }

func (UnimplementedBackend) Presence(context.Context, float32) (*Presence, error) {
	return nil, ErrNotSupported
}

func (UnimplementedBackend) Objects(context.Context, string, string) ([]*Seen, error) {
	return nil, ErrNotSupported
}

func (UnimplementedBackend) SimAttachments(context.Context, msg.UUID) (*SimAttachments, error) {
	return nil, ErrNotSupported
}

func (UnimplementedBackend) Region(context.Context) (*Region, bool, error) {
	return nil, false, ErrNotSupported
}

func (UnimplementedBackend) SimStats(context.Context) (*SimStats, error) {
	return nil, ErrNotSupported
}

func (UnimplementedBackend) LastRegionDetails(context.Context) (*RegionDetails, uint64, error) {
	return nil, 0, ErrNotSupported
}

func (UnimplementedBackend) Land(context.Context) (*Land, error) { return nil, ErrNotSupported }

func (UnimplementedBackend) Ground(context.Context, float32, float32, float32, float32) (float32, bool, error) {
	return 0, false, ErrNotSupported
}

func (UnimplementedBackend) Neighbours(context.Context, *bool) (*Neighbours, error) {
	return nil, ErrNotSupported
}

func (UnimplementedBackend) Lock(context.Context, string) error { return ErrNotSupported }
func (UnimplementedBackend) Unlock(string) error                { return ErrNotSupported }

func (UnimplementedBackend) TryLock(context.Context, string) (bool, string, error) {
	return false, "", ErrNotSupported
}

func (UnimplementedBackend) Flush(context.Context) (int, error) { return 0, ErrNotSupported }

func (UnimplementedBackend) ConfirmLinkOrder(context.Context, msg.UUID, []msg.UUID) (*LinkConfirmation, error) {
	return nil, ErrNotSupported
}

func (UnimplementedBackend) Friends(context.Context) ([]Friend, error) {
	return nil, ErrNotSupported
}

func (UnimplementedBackend) NoteFriend(context.Context, msg.UUID, bool) error {
	return ErrNotSupported
}

func (UnimplementedBackend) HasCap(string) bool { return false }

func (UnimplementedBackend) DoCap(context.Context, agent.CapRequest) (*agent.CapResponse, error) {
	return nil, ErrNotSupported
}

func (UnimplementedBackend) Close() error { return nil }
