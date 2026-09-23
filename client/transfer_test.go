package client

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
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

// TestTransferHandleLeavesAloneWhatIsNotItsOwn: a client feeds this
// every message it receives, so anything claimed here is a message the
// client will not see -- and a body that will not decode is not a
// transfer whatever the name on it says.
func TestTransferHandleLeavesAloneWhatIsNotItsOwn(t *testing.T) {
	t.Parallel()
	x := NewTransfers(nil)

	if x.Handle(&Message{Name: "ChatFromSimulator"}) {
		t.Error("a message that is not a transfer was claimed")
	}
	// Only TransferPacket can be made undecodable by cutting it short.
	// TransferInfo is zerocoded, and a zerocoded message reads past its
	// end as zeros, as the viewer reads it (see msg.Unmarshal).
	if x.Handle(&Message{
		ID: msg.IDOf(&msg.TransferPacket{}), Name: "TransferPacket", Body: []byte{1},
	}) {
		t.Error("a TransferPacket whose bytes will not decode was claimed")
	}
}

// TestATransferInfoForSomethingNobodyAskedForIsDropped: transfer ids are
// chosen by whoever asks, and an answer to a request already given up on
// arrives just the same -- it must not resurrect the transfer or be
// mistaken for one still running.
func TestATransferInfoForSomethingNobodyAskedForIsDropped(t *testing.T) {
	t.Parallel()
	x := NewTransfers(nil)
	if !x.Handle(infoMsg(newTransferID(), 12, statusOK)) {
		t.Error("a TransferInfo was not claimed")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.pending) != 0 {
		t.Errorf("kept %d transfers", len(x.pending))
	}
}

// TestFetchingAnAssetAsksWithEveryIdentityTheSimulatorChecks: an asset
// read through this path is permission checked against the agent, the
// session, the owner, the task and the item, all packed into one blob of
// parameters -- and one of them in the wrong place is a refusal that
// blames permissions rather than the packing.
func TestFetchingAnAssetAsksWithEveryIdentityTheSimulatorChecks(t *testing.T) {
	t.Parallel()
	s := &recordingSender{}
	x := NewTransfers(s)

	ref := AssetRef{
		Owner: msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01"),
		Item:  msg.MustParseUUID("97c27e57-7e57-c0de-0572-44b27a1c1090"),
		Asset: msg.MustParseUUID("88fa7e57-7e57-c0de-af42-813fbc8c4b63"),
		Type:  AssetLSLText,
	}
	s.on = func(m msg.Message) {
		req, ok := m.(*msg.TransferRequest)
		if !ok {
			return
		}
		id := req.TransferInfo.TransferID
		go func() {
			x.Handle(infoMsg(id, 5, statusOK))
			x.Handle(packetMsg(id, 0, []byte("hello"), true))
		}()
	}

	got, err := x.Fetch(context.Background(), testAgentID, testSessionID, ref, 10*time.Second)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("Fetch = %q", got)
	}

	req, ok := s.Sent()[0].(*msg.TransferRequest)
	if !ok {
		t.Fatalf("what went out was %T", s.Sent()[0])
	}
	if req.TransferInfo.ChannelType != channelAsset || req.TransferInfo.SourceType != sourceSimInvItem {
		t.Errorf("asked on %+v", req.TransferInfo)
	}
	params := req.TransferInfo.Params
	if len(params) != 6*16+4 {
		t.Fatalf("the parameters are %d bytes", len(params))
	}
	for i, want := range []msg.UUID{testAgentID, testSessionID, ref.Owner, ref.Task, ref.Item, ref.Asset} {
		if got := msg.UUID(params[i*16 : i*16+16]); got != want {
			t.Errorf("identity %d is %s, want %s", i, got, want)
		}
	}
	if binary.LittleEndian.Uint32(params[6*16:]) != uint32(ref.Type) {
		t.Errorf("the asset type came out as %d", binary.LittleEndian.Uint32(params[6*16:]))
	}
}

// TestAnAssetThatNeverArrivesIsGivenUpOnAndCancelled: nothing in this
// protocol is acknowledged, so a simulator that stops sending says
// nothing at all -- and walking away without telling it leaves it
// sending packets to a transfer nobody is reassembling.
func TestAnAssetThatNeverArrivesIsGivenUpOnAndCancelled(t *testing.T) {
	t.Parallel()
	s := &recordingSender{}
	x := NewTransfers(s)

	ref := AssetRef{Asset: msg.MustParseUUID("88fa7e57-7e57-c0de-af42-813fbc8c4b63")}
	_, err := x.Fetch(context.Background(), testAgentID, testSessionID, ref, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), ref.Asset.String()) {
		t.Errorf("Fetch = %v, want the asset named", err)
	}
	abort, ok := s.Sent()[len(s.Sent())-1].(*msg.TransferAbort)
	if !ok {
		t.Fatalf("giving up sent %T, want the simulator told to stop", s.Sent()[len(s.Sent())-1])
	}
	if abort.TransferInfo.ChannelType != channelAsset {
		t.Errorf("aborted on channel %d", abort.TransferInfo.ChannelType)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.Fetch(ctx, testAgentID, testSessionID, ref, 10*time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch = %v, want the cancellation", err)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.pending) != 0 {
		t.Errorf("%d transfers survived being given up on", len(x.pending))
	}
}

// TestAFetchThatCouldNotBeAskedForIsForgotten: the request going out is
// what starts a transfer, so one that never left has to be dropped here
// -- and a pending entry nobody will ever answer is a leak that only
// shows up as a long-running program growing.
func TestAFetchThatCouldNotBeAskedForIsForgotten(t *testing.T) {
	t.Parallel()
	s := &recordingSender{err: errors.New("the circuit is gone")}
	x := NewTransfers(s)

	if _, err := x.Fetch(context.Background(), testAgentID, testSessionID,
		AssetRef{}, 10*time.Second); err == nil {
		t.Error("Fetch waited for an asset it could not ask for")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.pending) != 0 {
		t.Errorf("a transfer that was never asked for is still pending: %v", x.pending)
	}
}

// TestGivingUpWithNoConnectionSaysNothingToNobody: Fetch refuses without
// one, but a transfer already running when the connection went is given
// up on through the same path -- which must not reach for a sender that
// is not there.
func TestGivingUpWithNoConnectionSaysNothingToNobody(t *testing.T) {
	t.Parallel()
	x := NewTransfers(nil)
	id := newTransferID()
	f := waiting(x, id)
	x.abort(context.Background(), id)
	select {
	case err := <-f.err:
		if err != nil {
			t.Errorf("giving up reported %v, want nothing to report", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("giving up never woke the transfer")
	}
}

// TestARefusalReachesWhoeverIsWaitingForTheAsset: a refusal is a
// TransferInfo with a negative status and no packets after it, so a
// Fetch that only watched for bytes would sit out its whole timeout and
// then report the wrong thing -- "no answer" for an answer that came.
func TestARefusalReachesWhoeverIsWaitingForTheAsset(t *testing.T) {
	t.Parallel()
	s := &recordingSender{}
	x := NewTransfers(s)
	s.on = func(m msg.Message) {
		if req, ok := m.(*msg.TransferRequest); ok {
			id := req.TransferInfo.TransferID
			go x.Handle(infoMsg(id, 0, -3))
		}
	}

	_, err := x.Fetch(context.Background(), testAgentID, testSessionID,
		AssetRef{Type: AssetNotecard}, 10*time.Second)
	if !errors.Is(err, ErrTransferDenied) {
		t.Errorf("Fetch = %v, want the refusal", err)
	}
}
