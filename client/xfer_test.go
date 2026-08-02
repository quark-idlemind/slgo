package client

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"slgo/msg"
)

// sendXfer builds the message a simulator would send for one packet.
func sendXfer(id uint64, seq uint32, data []byte, last bool) *Message {
	m := &msg.SendXferPacket{}
	m.XferID.ID = id
	m.XferID.Packet = seq
	if last {
		m.XferID.Packet |= xferLastPacket
	}
	m.DataPacket.Data = data
	body, _ := m.Encode()
	return &Message{ID: msg.IDOf(m), Name: "SendXferPacket", Body: body}
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

	x.fail(9, ErrXferAborted)
	select {
	case err := <-f.err:
		if err != ErrXferAborted {
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

func decodeXfer(t *testing.T, m *Message) *msg.SendXferPacket {
	t.Helper()
	v, err := m.Decode()
	if err != nil {
		t.Fatal(err)
	}
	return v.(*msg.SendXferPacket)
}
