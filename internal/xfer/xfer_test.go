package xfer

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
)

// sendXfer builds the message a simulator would send for one packet.
func sendXfer(id uint64, seq uint32, data []byte, last bool) *client.Message {
	m := &msg.SendXferPacket{}
	m.XferID.ID = id
	m.XferID.Packet = seq
	if last {
		m.XferID.Packet |= xferLastPacket
	}
	m.DataPacket.Data = data
	body, _ := m.Encode()
	return &client.Message{ID: msg.IDOf(m), Name: "SendXferPacket", Body: body}
}

func TestXferReassembles(t *testing.T) {
	x := NewXfers(nil)

	// A transfer the caller is waiting for.
	f := &xfer{got: map[uint32][]byte{}, done: make(chan []byte, 1), err: make(chan error, 1)}
	x.mu.Lock()
	x.pending[42] = f
	x.mu.Unlock()

	// The first packet carries a four byte length that is not part
	// of the file.
	first := make([]byte, 4)
	binary.LittleEndian.PutUint32(first, 11)
	first = append(first, []byte("hello ")...)

	x.packet(context.Background(), decodeXfer(t, sendXfer(42, 0, first, false)))
	x.packet(context.Background(), decodeXfer(t, sendXfer(42, 1, []byte("world"), true)))

	select {
	case b := <-f.done:
		if string(b) != "hello world" {
			t.Errorf("got %q", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the transfer never completed")
	}
}

// TestXferOutOfOrder: UDP reorders, and the packet number is what says
// where a piece goes.
func TestXferOutOfOrder(t *testing.T) {
	x := NewXfers(nil)
	f := &xfer{got: map[uint32][]byte{}, done: make(chan []byte, 1), err: make(chan error, 1)}
	x.mu.Lock()
	x.pending[7] = f
	x.mu.Unlock()

	first := make([]byte, 4)
	binary.LittleEndian.PutUint32(first, 9)
	first = append(first, []byte("aaa")...)

	x.packet(context.Background(), decodeXfer(t, sendXfer(7, 2, []byte("ccc"), true)))
	x.packet(context.Background(), decodeXfer(t, sendXfer(7, 1, []byte("bbb"), false)))
	select {
	case <-f.done:
		t.Fatal("completed before the first packet arrived")
	default:
	}
	x.packet(context.Background(), decodeXfer(t, sendXfer(7, 0, first, false)))

	select {
	case b := <-f.done:
		if string(b) != "aaabbbccc" {
			t.Errorf("got %q", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the transfer never completed")
	}
}

func TestXferAbort(t *testing.T) {
	x := NewXfers(nil)
	f := &xfer{got: map[uint32][]byte{}, done: make(chan []byte, 1), err: make(chan error, 1)}
	x.mu.Lock()
	x.pending[9] = f
	x.mu.Unlock()

	x.fail(9, client.ErrXferAborted)
	select {
	case err := <-f.err:
		if err != client.ErrXferAborted {
			t.Errorf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("abort was not reported")
	}
}

// TestXferIgnoresStrangers: a packet for a transfer we are not waiting
// for must not panic or be kept.
func TestXferIgnoresStrangers(t *testing.T) {
	x := NewXfers(nil)
	x.packet(context.Background(), decodeXfer(t, sendXfer(1234, 0, []byte("nobody asked"), true)))
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.pending) != 0 {
		t.Errorf("kept %d transfers", len(x.pending))
	}
}

func TestXferLengthPrefix(t *testing.T) {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, 4242)
	if n, ok := xferLength(append(b, 'x')); !ok || n != 4242 {
		t.Errorf("length = %d %v", n, ok)
	}
	if _, ok := xferLength([]byte{1, 2}); ok {
		t.Error("a short packet has no length prefix")
	}
}

func decodeXfer(t *testing.T, m *client.Message) *msg.SendXferPacket {
	t.Helper()
	v, err := m.Decode()
	if err != nil {
		t.Fatal(err)
	}
	return v.(*msg.SendXferPacket)
}

// ------------------------------------------------ a simulator to talk to

// recordingSender is somewhere for a reassembler to put a message.
//
// A reassembler is half a conversation: it acknowledges every packet as
// it goes, and the simulator sends the next only when that arrives -- so
// what goes out is as much a part of the protocol as what comes in, and
// a test that only fed packets in would never notice an acknowledgement
// that stopped being sent.
type recordingSender struct {
	mu   sync.Mutex
	sent []msg.Message
	err  error

	// on is called with each message after it is recorded and without
	// the lock, so that a test can have the simulator answer.
	on func(msg.Message)
}

func (s *recordingSender) Send(ctx context.Context, m msg.Message, reliable bool) error {
	s.mu.Lock()
	err, on := s.err, s.on
	if err == nil {
		s.sent = append(s.sent, m)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if on != nil {
		on(m)
	}
	return nil
}

// Sent is everything that went out, in order.
func (s *recordingSender) Sent() []msg.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]msg.Message(nil), s.sent...)
}

// TestEveryXferPacketIsAcknowledgedBeforeAnythingElse: the simulator
// sends the next packet only when the acknowledgement arrives, so
// anything done before replying is on the critical path of the whole
// transfer -- and an acknowledgement that never went out is a transfer
// that simply stops.
func TestEveryXferPacketIsAcknowledgedBeforeAnythingElse(t *testing.T) {
	t.Parallel()
	s := &recordingSender{}
	x := NewXfers(s)

	// Not a transfer of ours, and it is still acknowledged: the reply
	// is what keeps the simulator from resending it forever.
	if !x.Handle(context.Background(), sendXfer(1234, 0, []byte("nobody asked"), true)) {
		t.Error("a packet for an unknown transfer was not claimed")
	}
	sent := s.Sent()
	if len(sent) != 1 {
		t.Fatalf("%d messages went out, want the acknowledgement", len(sent))
	}
	ack, ok := sent[0].(*msg.ConfirmXferPacket)
	if !ok {
		t.Fatalf("what went out was %T", sent[0])
	}
	// The last-packet bit is part of the numbering and not of the
	// number, so it comes off before the packet is acknowledged.
	if ack.XferID.ID != 1234 || ack.XferID.Packet != 0 {
		t.Errorf("acknowledged %+v", ack.XferID)
	}
}

// TestAnAbortedXferIsReportedRatherThanWaitedOut: the simulator giving
// up is the answer, and treating it as silence leaves whoever asked
// waiting out the whole timeout for a file that is not coming.
func TestAnAbortedXferIsReportedRatherThanWaitedOut(t *testing.T) {
	t.Parallel()
	s := &recordingSender{}
	x := NewXfers(s)

	f := &xfer{got: map[uint32][]byte{}, done: make(chan []byte, 1), err: make(chan error, 1)}
	x.mu.Lock()
	x.pending[9] = f
	x.mu.Unlock()

	abort := &msg.AbortXfer{}
	abort.XferID.ID = 9
	abort.XferID.Result = -39
	body, err := abort.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !x.Handle(context.Background(), &client.Message{ID: msg.IDOf(abort), Name: "AbortXfer", Body: body}) {
		t.Error("an abort was not claimed")
	}
	select {
	case err := <-f.err:
		if !errors.Is(err, client.ErrXferAborted) {
			t.Errorf("err = %v", err)
		}
		if !strings.Contains(err.Error(), "-39") {
			t.Errorf("err should carry the simulator's result: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the abort was not reported")
	}
}

// TestXferHandleLeavesAloneWhatIsNotItsOwn: a client feeds this every
// message it receives, so anything it claims is a message the client
// will not see -- and a body that will not decode is not a transfer
// packet whatever the name on it says.
func TestXferHandleLeavesAloneWhatIsNotItsOwn(t *testing.T) {
	t.Parallel()
	x := NewXfers(&recordingSender{})

	if x.Handle(context.Background(), &client.Message{Name: "ChatFromSimulator"}) {
		t.Error("a message that is not a transfer was claimed")
	}
	if x.Handle(context.Background(), &client.Message{
		ID: msg.IDOf(&msg.SendXferPacket{}), Name: "SendXferPacket", Body: []byte{1},
	}) {
		t.Error("a packet whose bytes will not decode was claimed")
	}
	if x.Handle(context.Background(), &client.Message{
		ID: msg.IDOf(&msg.AbortXfer{}), Name: "AbortXfer", Body: []byte{1},
	}) {
		t.Error("an abort whose bytes will not decode was claimed")
	}
}

// TestAFirstXferPacketTooShortToHoldALengthIsNotFileContents: the four
// byte prefix is not part of the file, and a first packet shorter than
// the prefix carries no file at all -- reading it as content would put
// the length itself at the front of what the caller gets back.
func TestAFirstXferPacketTooShortToHoldALengthIsNotFileContents(t *testing.T) {
	t.Parallel()
	x := NewXfers(&recordingSender{})
	f := &xfer{got: map[uint32][]byte{}, done: make(chan []byte, 1), err: make(chan error, 1)}
	x.mu.Lock()
	x.pending[3] = f
	x.mu.Unlock()

	x.packet(context.Background(), decodeXfer(t, sendXfer(3, 0, []byte{1, 2}, true)))
	select {
	case b := <-f.done:
		if len(b) != 0 {
			t.Errorf("got %q, want nothing at all", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the transfer never completed")
	}
}

// TestFetchingAFileAsksForItAndWaits: the request names the file the
// simulator handed back and which of its directories to look in, and
// nothing arrives until it has gone out.
func TestFetchingAFileAsksForItAndWaits(t *testing.T) {
	t.Parallel()
	s := &recordingSender{}
	x := NewXfers(s)

	// The simulator answers the request with the file, from inside the
	// Send, which is as close to what one does as this gets.
	s.on = func(m msg.Message) {
		req, ok := m.(*msg.RequestXfer)
		if !ok {
			return
		}
		first := make([]byte, 4)
		binary.LittleEndian.PutUint32(first, 5)
		go x.packet(context.Background(),
			decodeXfer(t, sendXfer(req.XferID.ID, 0, append(first, []byte("hello")...), true)))
	}

	got, err := x.Fetch(context.Background(), msg.UUID{}, msg.UUID{},
		"inventory.tmp", FilePathTaskInventory, 10*time.Second)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("Fetch = %q", got)
	}
	req, ok := s.Sent()[0].(*msg.RequestXfer)
	if !ok {
		t.Fatalf("what went out was %T", s.Sent()[0])
	}
	if string(req.XferID.Filename) != "inventory.tmp\x00" || req.XferID.FilePath != FilePathTaskInventory {
		t.Errorf("asked for %+v", req.XferID)
	}
	if !req.XferID.DeleteOnCompletion {
		t.Error("the simulator was not told to throw the file away afterwards")
	}
}

// TestAFetchThatCannotBeAskedForIsNotWaitedFor: the request going out is
// the whole of what starts a transfer, so one that never left would
// otherwise be a caller waiting for a file nobody was asked for.
func TestAFetchThatCannotBeAskedForIsNotWaitedFor(t *testing.T) {
	t.Parallel()
	s := &recordingSender{err: errors.New("the circuit is gone")}
	x := NewXfers(s)

	_, err := x.Fetch(context.Background(), msg.UUID{}, msg.UUID{},
		"inventory.tmp", FilePathTaskInventory, 10*time.Second)
	if err == nil {
		t.Error("Fetch waited for a file it could not ask for")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.pending) != 0 {
		t.Errorf("a transfer that was never asked for is still pending: %v", x.pending)
	}
}

// TestAFetchGivesUpOnTimeAndOnCancellation: a simulator that says
// nothing is the ordinary failure -- the file may have been deleted
// between being named and being asked for -- and the complaint has to
// name the file, since a caller reading a task inventory is fetching
// one per object.
func TestAFetchGivesUpOnTimeAndOnCancellation(t *testing.T) {
	t.Parallel()
	x := NewXfers(&recordingSender{})

	_, err := x.Fetch(context.Background(), msg.UUID{}, msg.UUID{},
		"inventory.tmp", FilePathTaskInventory, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "inventory.tmp") {
		t.Errorf("Fetch = %v, want the file named", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.Fetch(ctx, msg.UUID{}, msg.UUID{},
		"inventory.tmp", FilePathTaskInventory, 10*time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch = %v, want the cancellation", err)
	}
}

// TestAnAbortArrivingMidFetchEndsIt: the abort and the packets arrive on
// the same channel the caller is waiting on, and the abort has to win --
// otherwise a refused transfer is indistinguishable from a slow one.
func TestAnAbortArrivingMidFetchEndsIt(t *testing.T) {
	t.Parallel()
	s := &recordingSender{}
	x := NewXfers(s)
	s.on = func(m msg.Message) {
		if req, ok := m.(*msg.RequestXfer); ok {
			go x.fail(req.XferID.ID, client.ErrXferAborted)
		}
	}

	_, err := x.Fetch(context.Background(), msg.UUID{}, msg.UUID{},
		"inventory.tmp", FilePathTaskInventory, 10*time.Second)
	if !errors.Is(err, client.ErrXferAborted) {
		t.Errorf("Fetch = %v, want the abort", err)
	}
}
