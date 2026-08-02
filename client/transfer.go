package client

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"slgo/msg"
)

// Asset transfer is how the bytes behind an inventory item are read.
//
// It is not the same thing as xfer, which moves a file the simulator
// names, and it is not the ViewerAsset capability, which serves the
// content delivery network -- textures, meshes, sounds -- and answers
// 403 for a notecard or a script.  Those have to come this way.
//
// A TransferRequest names the item and the asset behind it; the
// simulator answers with a TransferInfo carrying the size, then a run
// of TransferPacket.  Nothing is acknowledged: the packets simply
// arrive, possibly out of order, and the last one is marked in its
// status rather than its number.

// Transfer channels and sources, from the viewer's
// lltransfermanager.h.
const (
	channelAsset = 2

	sourceAsset      = 2 // an asset by id alone
	sourceSimInvItem = 3 // an inventory item, which is permission checked

	statusOK   = 0
	statusDone = 1
)

// Asset types worth naming here; the full list is in llassettype.h.
const (
	AssetNotecard = 7
	AssetLSLText  = 10
)

// ErrTransferDenied is reported when the simulator refuses.
var ErrTransferDenied = errors.New("client: transfer refused")

// Transfers reassembles assets arriving over the transfer protocol.
type Transfers struct {
	c *Conn

	mu      sync.Mutex
	pending map[msg.UUID]*transfer
}

type transfer struct {
	got      map[int32][]byte
	last     int32
	haveLast bool
	size     int32
	done     chan []byte
	err      chan error
	once     sync.Once
}

// NewTransfers prepares a reassembler on a connection.
func NewTransfers(c *Conn) *Transfers {
	return &Transfers{c: c, pending: map[msg.UUID]*transfer{}}
}

// Handle feeds one relayed message in and reports whether it belonged
// to a transfer.
func (x *Transfers) Handle(m *Message) bool {
	switch m.Name {
	case "TransferInfo":
		v, err := m.Decode()
		if err != nil || v == nil {
			return false
		}
		x.info(v.(*msg.TransferInfo))
		return true
	case "TransferPacket":
		v, err := m.Decode()
		if err != nil || v == nil {
			return false
		}
		x.packet(v.(*msg.TransferPacket))
		return true
	}
	return false
}

func (x *Transfers) info(m *msg.TransferInfo) {
	id := m.TransferInfo.TransferID
	x.mu.Lock()
	t := x.pending[id]
	if t != nil {
		t.size = m.TransferInfo.Size
	}
	x.mu.Unlock()
	if t == nil {
		return
	}

	// A negative status is a refusal, and no packets will follow.
	// -3 is insufficient permissions, which is worth saying plainly
	// rather than reporting as a timeout.
	if s := m.TransferInfo.Status; s < 0 {
		why := fmt.Sprintf("status %d", s)
		if s == -3 {
			why = "insufficient permissions"
		}
		x.fail(id, fmt.Errorf("%w: %s", ErrTransferDenied, why))
		return
	}
	// A size of zero with an ok status means there is nothing to
	// send, which completes the transfer immediately.
	if m.TransferInfo.Size == 0 {
		x.finish(id, nil)
	}
}

func (x *Transfers) packet(m *msg.TransferPacket) {
	d := &m.TransferData
	id := d.TransferID

	x.mu.Lock()
	t := x.pending[id]
	if t == nil {
		x.mu.Unlock()
		return
	}
	buf := make([]byte, len(d.Data))
	copy(buf, d.Data)
	t.got[d.Packet] = buf

	// The last packet is marked by its status, not its number.
	if d.Status == statusDone {
		t.last, t.haveLast = d.Packet, true
	}

	var out []byte
	complete := t.haveLast && int32(len(t.got)) == t.last+1
	if complete {
		for i := int32(0); i <= t.last; i++ {
			out = append(out, t.got[i]...)
		}
		delete(x.pending, id)
	}
	x.mu.Unlock()

	if complete {
		t.once.Do(func() { t.done <- out })
	}
}

func (x *Transfers) fail(id msg.UUID, err error) {
	x.mu.Lock()
	t := x.pending[id]
	delete(x.pending, id)
	x.mu.Unlock()
	if t != nil {
		t.once.Do(func() { t.err <- err })
	}
}

func (x *Transfers) finish(id msg.UUID, b []byte) {
	x.mu.Lock()
	t := x.pending[id]
	delete(x.pending, id)
	x.mu.Unlock()
	if t != nil {
		t.once.Do(func() { t.done <- b })
	}
}

// AssetRef says which asset to read and how to prove we may.
//
// Owner, Task and Item are what the simulator checks the permissions
// against.  Task is zero for something in agent inventory.
type AssetRef struct {
	Owner msg.UUID
	Task  msg.UUID
	Item  msg.UUID
	Asset msg.UUID
	Type  int32
}

// Fetch reads an asset's bytes.
func (x *Transfers) Fetch(ctx context.Context, agentID, sessionID msg.UUID,
	ref AssetRef, timeout time.Duration) ([]byte, error) {

	id := newTransferID()
	t := &transfer{
		got:  map[int32][]byte{},
		done: make(chan []byte, 1),
		err:  make(chan error, 1),
	}
	x.mu.Lock()
	x.pending[id] = t
	x.mu.Unlock()

	// The parameters for an inventory item are the identities the
	// simulator checks, then the asset and its type.
	params := make([]byte, 0, 6*16+4)
	for _, u := range []msg.UUID{agentID, sessionID, ref.Owner, ref.Task, ref.Item, ref.Asset} {
		params = append(params, u[:]...)
	}
	params = binary.LittleEndian.AppendUint32(params, uint32(ref.Type))

	req := &msg.TransferRequest{}
	req.TransferInfo.TransferID = id
	req.TransferInfo.ChannelType = channelAsset
	req.TransferInfo.SourceType = sourceSimInvItem
	req.TransferInfo.Priority = 100
	req.TransferInfo.Params = params
	if x.c == nil {
		x.fail(id, nil)
		return nil, errors.New("client: not connected")
	}
	if err := x.c.Send(ctx, req, true); err != nil {
		x.fail(id, err)
		return nil, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case b := <-t.done:
		return b, nil
	case err := <-t.err:
		return nil, err
	case <-ctx.Done():
		x.abort(ctx, id)
		return nil, ctx.Err()
	case <-timer.C:
		x.abort(ctx, id)
		return nil, fmt.Errorf("client: asset %s timed out after %s", ref.Asset, timeout)
	}
}

// abort tells the simulator to stop, and forgets the transfer.
func (x *Transfers) abort(ctx context.Context, id msg.UUID) {
	x.fail(id, nil)
	if x.c == nil {
		return
	}
	m := &msg.TransferAbort{}
	m.TransferInfo.TransferID = id
	m.TransferInfo.ChannelType = channelAsset
	_ = x.c.Send(ctx, m, false)
}

// transferCounter keeps two transfers started in the same nanosecond
// apart.
var transferCounter atomic.Uint64

func newTransferID() msg.UUID {
	var u msg.UUID
	binary.BigEndian.PutUint64(u[0:8], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint64(u[8:16], transferCounter.Add(1))
	return u
}
