package client

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// The xfer protocol is how a file is pulled off a simulator over UDP.
// It is old and still load bearing: RequestTaskInventory answers with a
// filename rather than the contents, and the only way to read that file
// is this.
//
// A transfer is a series of SendXferPacket, each acknowledged with a
// ConfirmXferPacket.  The packet number counts from zero and its top
// bit marks the last one.  The first packet carries a four byte length
// prefix that is not part of the file.
//
// It runs in the client because it is nothing but UDP messages, which
// the server relays without understanding.

const (
	xferLastPacket = 0x80000000

	// FilePathTaskInventory is the path code RequestTaskInventory's
	// filenames live under.
	FilePathTaskInventory = 4
)

// ErrXferAborted is reported when the simulator gives up on a transfer.
var ErrXferAborted = errors.New("client: transfer aborted")

// Xfers reassembles files arriving over the xfer protocol.
//
// A client makes one, feeds it every SendXferPacket and AbortXfer it
// receives, and asks it for a file by id.
type Xfers struct {
	c Sender

	mu      sync.Mutex
	pending map[uint64]*xfer
	nextID  uint64
}

type xfer struct {
	got      map[uint32][]byte
	last     uint32
	haveLast bool
	done     chan []byte
	err      chan error
	once     sync.Once
}

// Sender is what a reassembler needs from a connection: somewhere to
// put a message.  An interface rather than *Conn so that a session
// holding its own grid connection, with no server in between, can use
// the same machinery.
type Sender interface {
	Send(ctx context.Context, m msg.Message, reliable bool) error
}

// NewXfers prepares a reassembler on anything that can send.
func NewXfers(c Sender) *Xfers {
	return &Xfers{
		c:       c,
		pending: map[uint64]*xfer{},
		// Ids only have to be unique within this session; the
		// simulator echoes whatever we choose.
		nextID: uint64(time.Now().UnixNano()) | 1,
	}
}

// Handle feeds one relayed message to the reassembler and reports
// whether it belonged to a transfer.
//
// SendXferPacket must be acknowledged or the simulator stops sending,
// so this replies as it goes.
func (x *Xfers) Handle(ctx context.Context, m *Message) bool {
	switch m.Name {
	case "SendXferPacket":
		v, err := m.Decode()
		if err != nil || v == nil {
			return false
		}
		x.packet(ctx, v.(*msg.SendXferPacket))
		return true
	case "AbortXfer":
		v, err := m.Decode()
		if err != nil || v == nil {
			return false
		}
		a := v.(*msg.AbortXfer)
		x.fail(a.XferID.ID, fmt.Errorf("%w: result %d", ErrXferAborted, a.XferID.Result))
		return true
	}
	return false
}

func (x *Xfers) packet(ctx context.Context, p *msg.SendXferPacket) {
	id := p.XferID.ID
	n := p.XferID.Packet

	// Acknowledge first: the simulator sends the next packet only
	// once this arrives, so anything else here is on the critical
	// path of the transfer.
	if x.c != nil {
		ack := &msg.ConfirmXferPacket{}
		ack.XferID.ID = id
		ack.XferID.Packet = n &^ xferLastPacket
		_ = x.c.Send(ctx, ack, false)
	}

	x.mu.Lock()
	f := x.pending[id]
	x.mu.Unlock()
	if f == nil {
		return // not ours, or already finished
	}

	data := p.DataPacket.Data
	seq := n &^ xferLastPacket
	if seq == 0 {
		// The first packet is prefixed with the file's length,
		// which is not part of the file.
		if len(data) >= 4 {
			data = data[4:]
		} else {
			data = nil
		}
	}
	buf := make([]byte, len(data))
	copy(buf, data)

	x.mu.Lock()
	f.got[seq] = buf
	if n&xferLastPacket != 0 {
		f.last, f.haveLast = seq, true
	}
	complete := f.haveLast && uint32(len(f.got)) == f.last+1
	var out []byte
	if complete {
		for i := uint32(0); i <= f.last; i++ {
			out = append(out, f.got[i]...)
		}
		delete(x.pending, id)
	}
	x.mu.Unlock()

	if complete {
		f.once.Do(func() { f.done <- out })
	}
}

func (x *Xfers) fail(id uint64, err error) {
	x.mu.Lock()
	f := x.pending[id]
	delete(x.pending, id)
	x.mu.Unlock()
	if f != nil {
		f.once.Do(func() { f.err <- err })
	}
}

// Fetch asks the simulator for a file and waits for it.
//
// The filename is the one a message such as ReplyTaskInventory handed
// back; path says which of the simulator's directories it lives in.
func (x *Xfers) Fetch(ctx context.Context, agentID, sessionID msg.UUID,
	filename string, path uint8, timeout time.Duration) ([]byte, error) {

	f := &xfer{
		got:  map[uint32][]byte{},
		done: make(chan []byte, 1),
		err:  make(chan error, 1),
	}

	x.mu.Lock()
	x.nextID += 2
	id := x.nextID
	x.pending[id] = f
	x.mu.Unlock()

	req := &msg.RequestXfer{}
	req.XferID.ID = id
	req.XferID.Filename = append([]byte(filename), 0)
	req.XferID.FilePath = path
	req.XferID.DeleteOnCompletion = true
	req.XferID.UseBigPackets = false
	if err := x.c.Send(ctx, req, true); err != nil {
		x.fail(id, err)
		return nil, err
	}

	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case b := <-f.done:
		return b, nil
	case err := <-f.err:
		return nil, err
	case <-ctx.Done():
		x.fail(id, ctx.Err())
		return nil, ctx.Err()
	case <-t.C:
		x.fail(id, nil)
		return nil, fmt.Errorf("client: transfer of %q timed out after %s", filename, timeout)
	}
}

// xferLength reads the length prefix on a transfer's first packet, for
// callers that want to check it.
func xferLength(first []byte) (uint32, bool) {
	if len(first) < 4 {
		return 0, false
	}
	return binary.LittleEndian.Uint32(first[:4]), true
}
