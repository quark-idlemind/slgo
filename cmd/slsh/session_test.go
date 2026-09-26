package main

// The plumbing commands.
//
// status and watch are below the level the rest of the shell works at:
// everything else asks about inventory or objects or people, and these
// two ask about the connection.  They are here because when the
// plumbing is what is wrong, the alternative is a second program that
// exists only for the days it goes wrong -- and which is therefore
// never up to date when one arrives.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// TestStatusIsTheCircuitRatherThanTheWorld.
func TestStatusIsTheCircuitRatherThanTheWorld(t *testing.T) {
	x, d := newDaemonShell(t)
	d.status = &pb.StatusResponse{
		Agent: &pb.AgentInfo{
			Name: "fake", AvatarName: "Quark Idlemind", Region: "Test Region",
			ChannelVersion: "Second Life Server 2026.01",
			Caps:           []string{"SimulatorFeatures", "ViewerAsset"},
		},
		PacketsIn: 4231, PacketsOut: 918, Resent: 3, Abandoned: 1,
		Duplicates: 7, Clients: 2,
	}

	got := x.do(t, "status")
	for _, want := range []string{
		"Quark Idlemind (fake) in Test Region",
		"Second Life Server 2026.01",
		"4231 in, 918 out (3 resent, 1 abandoned)",
		"duplicates  7",
		"clients     2",
		"caps        2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status should say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "no handler for") {
		t.Errorf("a session that understood everything should not list handlers:\n%s", got)
	}
}

// TestStatusCountsPlacementsByWidth: the width of an ObjectUpdate's
// placement blob says which form it is in, and the line says how many
// of each have arrived, narrowest first.  A daemon that has counted
// none prints no line.
func TestStatusCountsPlacementsByWidth(t *testing.T) {
	x, d := newDaemonShell(t)
	d.status = &pb.StatusResponse{
		Agent:           &pb.AgentInfo{Name: "fake"},
		PlacementWidths: map[uint32]uint64{76: 40, 60: 2211, 32: 3},
	}
	if got, want := x.do(t, "status"), "placements  32 bytes: 3, 60 bytes: 2211, 76 bytes: 40\n"; !strings.Contains(got, want) {
		t.Errorf("status should say %q:\n%s", want, got)
	}

	d.status = &pb.StatusResponse{Agent: &pb.AgentInfo{Name: "fake"}}
	if got := x.do(t, "status"); strings.Contains(got, "placements") {
		t.Errorf("no placements counted should print no line:\n%s", got)
	}
}

// TestStatusPutsTheCommonestUnhandledMessageFirst.
//
// "no handler for" names the messages this build does not understand,
// which is how a protocol change announces itself -- and the one
// arriving hundreds of times is the one worth writing a handler for.
func TestStatusPutsTheCommonestUnhandledMessageFirst(t *testing.T) {
	x, d := newDaemonShell(t)
	d.status = &pb.StatusResponse{
		Agent:     &pb.AgentInfo{Name: "fake"},
		Unhandled: map[string]uint64{"RarelySeen": 2, "SeenConstantly": 900, "Middling": 40},
	}

	got := x.do(t, "status")
	i, j, k := strings.Index(got, "SeenConstantly"), strings.Index(got, "Middling"), strings.Index(got, "RarelySeen")
	if i < 0 || j < 0 || k < 0 {
		t.Fatalf("all three should be listed:\n%s", got)
	}
	if !(i < j && j < k) {
		t.Errorf("the commonest should be first:\n%s", got)
	}

	d.fail = errStatusUnavailable
	if got := x.do(t, "status"); !strings.Contains(got, "not holding that session") {
		t.Errorf("status should report the failure, got %q", got)
	}
	if got := x.do(t, "status --help"); !strings.Contains(got, "status") {
		t.Errorf("status --help printed %q", got)
	}
}

// errStatusUnavailable stands in for a daemon that has the connection
// but not the session.
var errStatusUnavailable = errUnavailable("not holding that session")

type errUnavailable string

func (e errUnavailable) Error() string { return string(e) }

// TestWatchOpensAConnectionOfItsOwn, and says so when it cannot.
//
// It has to be its own: a session has a single goroutine consuming the
// relay, and a watch of "*" applied to the shared stream would pour
// every message the region produces through the reader that keeps this
// shell's idea of the world up to date.
func TestWatchOpensAConnectionOfItsOwn(t *testing.T) {
	// Nothing is listening on port 1, so the dial fails at once --
	// which is the half of watch that can be reached without a daemon
	// that speaks TLS.
	x := newTestShellOn(t, newFakeGrid(t), Config{Addr: "127.0.0.1:1", Prefix: 27})

	got := x.do(t, "watch")
	if !strings.Contains(got, "opening a second connection to watch on") {
		t.Errorf("watch should say what it was doing when it failed, got %q", got)
	}
}

// TestWatchNeedsADaemonToRelayFrom: a direct session holds the circuit
// itself and there is nothing to subscribe to a second time.
func TestWatchNeedsADaemonToRelayFrom(t *testing.T) {
	x := newTestShellOn(t, newFakeGrid(t), Config{Direct: true, Prefix: 27})

	if got := x.do(t, "watch"); !strings.Contains(got, "needs a daemon to relay from") {
		t.Errorf("watch printed %q", got)
	}
	// The flags are read before that, since --help must answer
	// wherever the session came from.
	if got := x.do(t, "watch --help"); !strings.Contains(got, "NAME") {
		t.Errorf("watch --help printed %q", got)
	}
	if got := x.do(t, "watch -t 5s ChatFromSimulator"); !strings.Contains(got, "needs a daemon") {
		t.Errorf("watch with flags printed %q", got)
	}
}

// TestShowMessageReportsWhatItCannotRead.
//
// A message the template does not have is still reported rather than
// skipped: a number this build has never heard of is exactly what a
// protocol change looks like, and "not in this template" is the useful
// half of that.
func TestShowMessageReportsWhatItCannotRead(t *testing.T) {
	var b strings.Builder

	// One it knows.
	m := &msg.ChatFromSimulator{}
	m.ChatData.FromName = append([]byte("Somebody"), 0)
	m.ChatData.Message = append([]byte("hello"), 0)
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	showMessage(&b, &client.Message{
		ID: msg.IDOf(m), Name: m.MsgInfo().Name, Sequence: 12, Body: body})
	if got := b.String(); !strings.Contains(got, "ChatFromSimulator") || !strings.Contains(got, "hello") {
		t.Errorf("a decodable message printed %q", got)
	}

	// One it cannot decode: the right number, the wrong bytes.
	b.Reset()
	showMessage(&b, &client.Message{
		ID: msg.IDOf(m), Name: m.MsgInfo().Name, Sequence: 13, Body: []byte{0xff}})
	if got := b.String(); !strings.Contains(got, "undecodable") {
		t.Errorf("a corrupt message printed %q", got)
	}

	// One that is not in the template at all, which is what a protocol
	// change looks like from here.
	b.Reset()
	showMessage(&b, &client.Message{
		ID: msg.MakeID(msg.FreqLow, 65530), Sequence: 14, Body: []byte{1, 2, 3}})
	if got := b.String(); !strings.Contains(got, "unknown ") || !strings.Contains(got, "3 bytes, not in this template") {
		t.Errorf("a message this build has never heard of printed %q", got)
	}
}

// TestWatchPrintsWhatArrivesAndThenStops.
//
// This is the whole of watch: a connection of its own, an attach to the
// same agent named outright, everything relayed printed as it comes,
// and a count at the end.  It needs a daemon that can be dialled --
// TLS and the shared secret -- because that is what watch does.
func TestWatchPrintsWhatArrivesAndThenStops(t *testing.T) {
	d, addr := newAuthDaemon(t)
	x := newTestShellOn(t, newFakeGrid(t), Config{Addr: addr, Prefix: 27})

	m := &msg.ChatFromSimulator{}
	m.ChatData.FromName = append([]byte("Somebody"), 0)
	m.ChatData.Message = append([]byte("something relayed"), 0)
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Message{Message: &pb.InboundMessage{
		Id: uint32(msg.IDOf(m)), Name: m.MsgInfo().Name, Sequence: 7, Body: body,
	}}}

	got := x.do(t, "watch -t 300ms ChatFromSimulator")
	if !strings.Contains(got, "watching [ChatFromSimulator] for 300ms") {
		t.Errorf("watch should say what it is watching and for how long:\n%s", got)
	}
	if !strings.Contains(got, "something relayed") {
		t.Errorf("the relayed message should have been printed:\n%s", got)
	}
	if !strings.Contains(got, "1 messages") {
		t.Errorf("watch should count what it saw:\n%s", got)
	}
}

// TestWatchStopsWithItsContext, which is Ctrl-C rather than the time
// running out -- and it still says how much it saw.
func TestWatchStopsWithItsContext(t *testing.T) {
	_, addr := newAuthDaemon(t)
	x := newTestShellOn(t, newFakeGrid(t), Config{Addr: addr, Prefix: 27})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	x.Do(ctx, "watch -t 1m")

	if got := x.out.String(); !strings.Contains(got, "0 messages") {
		t.Errorf("a watch that was interrupted should still report:\n%s", got)
	}
}

// TestWatchSaysWhenTheDaemonGoesAway.
//
// A stream that ends is not a watch that finished: the count would say
// "0 messages" and read as a quiet region rather than as a daemon that
// is no longer there.
func TestWatchSaysWhenTheDaemonGoesAway(t *testing.T) {
	d, addr := newAuthDaemon(t)
	d.hangUp = true
	x := newTestShellOn(t, newFakeGrid(t), Config{Addr: addr, Prefix: 27})

	got := x.do(t, "watch -t 1m")
	if !strings.Contains(got, "the stream ended") {
		t.Errorf("watch should say the daemon went away:\n%s", got)
	}
}

// TestWatchNamesTheSessionItWantedWhenItCannotHaveIt.
//
// The attach names this shell's agent outright rather than taking the
// daemon's default, which could in principle have moved since -- so a
// refusal is a daemon that is no longer holding what this shell is
// attached to, and worth saying rather than watching nothing.
func TestWatchNamesTheSessionItWantedWhenItCannotHaveIt(t *testing.T) {
	d, addr := newAuthDaemon(t)
	d.attachFail = errors.New("no agent named \"fake\"")
	x := newTestShellOn(t, newFakeGrid(t), Config{Addr: addr, Prefix: 27})

	if got := x.do(t, "watch -t 1m"); !strings.Contains(got, "attaching to watch") {
		t.Errorf("watch should say what it was doing when it failed:\n%s", got)
	}
}
