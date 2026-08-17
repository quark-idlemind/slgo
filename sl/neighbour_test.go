package sl

// What a client is told about the regions around this one, both ways
// round.
//
// Nothing here opens a circuit and nothing here could: a child circuit
// needs a simulator on the other end of it, which is agent's business
// and is tested there.  What is tested here is the translation and the
// three requests -- ask, on, off -- through each backend, since a
// program is supposed to work the same whichever it got.

import (
	"context"
	"errors"
	"testing"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// TestTheNeighboursCrossTheWireFieldByField: every field of the listing
// is one a person reads off it -- which region, where it is, whether it
// answered, whether it is saying anything -- so a field lost in the
// translation is a listing that quietly says less.
func TestTheNeighboursCrossTheWireFieldByField(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	handle := msg.RegionHandle(43646, 43648)
	d.neighbours = &pb.NeighboursResponse{
		On: true,
		Neighbours: []*pb.NeighbourInfo{
			{
				Handle:    handle,
				Address:   "35.91.2.183:13032",
				Name:      "Pelmar Mill",
				Handshook: true,
				Heard:     412,
			},
			// One that was dialled and never answered, which is an
			// offer that came to nothing and has to be visible as
			// one.
			{Handle: msg.RegionHandle(43648, 43647), Address: "35.91.2.184:13011"},
		},
	}

	n, err := h.Neighbours(context.Background(), nil)
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	if !n.On || len(n.Held) != 2 {
		t.Fatalf("Neighbours = %+v", n)
	}
	if n.Held[0] != (Neighbour{
		Handle: handle, Addr: "35.91.2.183:13032", Name: "Pelmar Mill",
		Handshook: true, Heard: 412,
	}) {
		t.Errorf("the first neighbour came out as %+v", n.Held[0])
	}
	if n.Held[1].Handshook || n.Held[1].Name != "" || n.Held[1].Heard != 0 {
		t.Errorf("a circuit that was never answered came out as %+v", n.Held[1])
	}

	d.fail = errors.New("no such agent")
	if _, err := h.Neighbours(context.Background(), nil); err == nil {
		t.Error("Neighbours answered from a daemon that refused")
	}
}

// TestADirectSessionAnswersForItsOwnNeighbours: there is no daemon in a
// direct session, so the flag and the listing are the agent's own.  The
// agent here was never connected, which is why this is about the flag
// and not about a circuit: opening one needs a simulator, and
// agent/neighbour_test.go is where there is one.
func TestADirectSessionAnswersForItsOwnNeighbours(t *testing.T) {
	d := aDirectSession(t)
	ctx := context.Background()

	n, err := d.Neighbours(ctx, nil)
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	if n.On || len(n.Held) != 0 {
		t.Fatalf("Neighbours = %+v before anything asked for them", n)
	}

	on := true
	if n, err = d.Neighbours(ctx, &on); err != nil || !n.On {
		t.Fatalf("Neighbours(on) = %+v, %v", n, err)
	}
	// The set reached the agent rather than only the answer.
	if !d.a.NeighboursOn() {
		t.Error("the agent was not turned on")
	}

	off := false
	if n, err = d.Neighbours(ctx, &off); err != nil || n.On || len(n.Held) != 0 {
		t.Fatalf("Neighbours(off) = %+v, %v", n, err)
	}
	if d.a.NeighboursOn() {
		t.Error("the agent was not turned off")
	}
}

// TestBothBackendsAnswerTheSameThreeRequests: a program is supposed to
// work the same whichever backend it got, and this is the one thing
// about neighbours that can be compared without a grid -- that asking
// changes nothing, that on comes back on, and that off comes back off
// with nothing held.
//
// What it cannot compare is a circuit, since neither fake can open one.
// backend_test.go is where the two are held against a real grid.
func TestBothBackendsAnswerTheSameThreeRequests(t *testing.T) {
	hosted, _ := newFakeDaemon(t)
	direct := aDirectSession(t)

	for name, b := range map[string]Backend{"hosted": hosted, "direct": direct} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()

			n, err := b.Neighbours(ctx, nil)
			if err != nil {
				t.Fatalf("Neighbours: %v", err)
			}
			if n.On {
				t.Error("a session nobody asked came back on")
			}

			on, off := true, false
			if n, err = b.Neighbours(ctx, &on); err != nil || !n.On {
				t.Fatalf("Neighbours(on) = %+v, %v", n, err)
			}
			// Asking again changes nothing, which is what the absent
			// field means.
			if n, err = b.Neighbours(ctx, nil); err != nil || !n.On {
				t.Fatalf("asking turned it off: %+v, %v", n, err)
			}
			if n, err = b.Neighbours(ctx, &off); err != nil || n.On || len(n.Held) != 0 {
				t.Fatalf("Neighbours(off) = %+v, %v", n, err)
			}
		})
	}
}

// TestTheSessionAsksAndSetsThroughTheBackend: Where and SetDrawDistance
// are two calls for one question and this is the same pair, so the
// session must not have grown a way of setting that a caller reading is
// obliged to think about.
func TestTheSessionAsksAndSetsThroughTheBackend(t *testing.T) {
	w, f := newFakeSession(t)
	f.neighbours = Neighbours{
		On:   true,
		Held: []Neighbour{{Handle: msg.RegionHandle(43646, 43648), Name: "Pelmar Mill", Handshook: true}},
	}
	ctx := context.Background()

	n, err := w.Neighbours(ctx)
	if err != nil {
		t.Fatalf("Neighbours: %v", err)
	}
	if !n.On || len(n.Held) != 1 || n.Held[0].Name != "Pelmar Mill" {
		t.Fatalf("Neighbours = %+v", n)
	}

	if n, err = w.SetNeighbours(ctx, false); err != nil {
		t.Fatalf("SetNeighbours: %v", err)
	}
	if n.On || len(n.Held) != 0 {
		t.Errorf("SetNeighbours(false) = %+v, want nothing held", n)
	}
	if n, err = w.SetNeighbours(ctx, true); err != nil || !n.On {
		t.Errorf("SetNeighbours(true) = %+v, %v", n, err)
	}
}

// TestADirectSessionIsStillABackend: aDirectSession builds one by hand,
// so the interface is worth restating where the new method was added.
func TestADirectSessionIsStillABackend(t *testing.T) {
	var _ Backend = (*Direct)(nil)
	var _ agent.CapDoer = (*Direct)(nil)
}
