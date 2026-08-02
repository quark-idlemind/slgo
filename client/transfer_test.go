package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"slgo/msg"
)

func infoMsg(id msg.UUID, size, status int32) *Message {
	m := &msg.TransferInfo{}
	m.TransferInfo.TransferID = id
	m.TransferInfo.ChannelType = channelAsset
	m.TransferInfo.Size = size
	m.TransferInfo.Status = status
	b, _ := m.Encode()
	return &Message{ID: msg.IDOf(m), Name: "TransferInfo", Body: b}
}

func packetMsg(id msg.UUID, n int32, data []byte, done bool) *Message {
	m := &msg.TransferPacket{}
	m.TransferData.TransferID = id
	m.TransferData.ChannelType = channelAsset
	m.TransferData.Packet = n
	m.TransferData.Data = data
	if done {
		m.TransferData.Status = statusDone
	}
	b, _ := m.Encode()
	return &Message{ID: msg.IDOf(m), Name: "TransferPacket", Body: b}
}

func waiting(x *Transfers, id msg.UUID) *transfer {
	t := &transfer{got: map[int32][]byte{}, done: make(chan []byte, 1), err: make(chan error, 1)}
	x.mu.Lock()
	x.pending[id] = t
	x.mu.Unlock()
	return t
}

func TestTransferReassembles(t *testing.T) {
	x := NewTransfers(nil)
	id := newTransferID()
	f := waiting(x, id)

	x.Handle(infoMsg(id, 11, statusOK))
	x.Handle(packetMsg(id, 0, []byte("hello "), false))
	x.Handle(packetMsg(id, 1, []byte("world"), true))

	select {
	case b := <-f.done:
		if string(b) != "hello world" {
			t.Errorf("got %q", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("never completed")
	}
}

// TestTransferOutOfOrder: nothing is acknowledged, so packets arrive in
// any order, and the last is marked by its status rather than its
// number.
func TestTransferOutOfOrder(t *testing.T) {
	x := NewTransfers(nil)
	id := newTransferID()
	f := waiting(x, id)

	x.Handle(infoMsg(id, 9, statusOK))
	x.Handle(packetMsg(id, 2, []byte("ccc"), true))
	x.Handle(packetMsg(id, 0, []byte("aaa"), false))
	select {
	case <-f.done:
		t.Fatal("completed with a hole in the middle")
	default:
	}
	x.Handle(packetMsg(id, 1, []byte("bbb"), false))

	select {
	case b := <-f.done:
		if string(b) != "aaabbbccc" {
			t.Errorf("got %q", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("never completed")
	}
}

// TestTransferDenied: a refusal deserves to be reported as itself
// rather than as a timeout twenty seconds later.
func TestTransferDenied(t *testing.T) {
	x := NewTransfers(nil)
	id := newTransferID()
	f := waiting(x, id)

	x.Handle(infoMsg(id, 0, -3))
	select {
	case err := <-f.err:
		if !errors.Is(err, ErrTransferDenied) {
			t.Errorf("err = %v", err)
		}
		if !strings.Contains(err.Error(), "permissions") {
			t.Errorf("err should name the reason: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("refusal not reported")
	}
}

func TestTransferEmptyAsset(t *testing.T) {
	x := NewTransfers(nil)
	id := newTransferID()
	f := waiting(x, id)
	x.Handle(infoMsg(id, 0, statusOK))
	select {
	case b := <-f.done:
		if len(b) != 0 {
			t.Errorf("got %d bytes", len(b))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an empty asset should complete at once")
	}
}

func TestTransferIgnoresStrangers(t *testing.T) {
	x := NewTransfers(nil)
	x.Handle(packetMsg(newTransferID(), 0, []byte("nobody asked"), true))
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.pending) != 0 {
		t.Errorf("kept %d transfers", len(x.pending))
	}
}

func TestTransferIDsAreDistinct(t *testing.T) {
	seen := map[msg.UUID]bool{}
	for i := 0; i < 1000; i++ {
		id := newTransferID()
		if seen[id] {
			t.Fatal("two transfers got the same id")
		}
		seen[id] = true
	}
}

func TestTransferNeedsAConnection(t *testing.T) {
	x := NewTransfers(nil)
	_, err := x.Fetch(context.Background(), msg.UUID{}, msg.UUID{}, AssetRef{}, 50*time.Millisecond)
	if err == nil {
		t.Error("expected an error with no connection")
	}
}
