package sl

// Building against a live simulator.
//
// Skipped unless SLGO_TEST_ADDR names a running slgod, like the other
// live tests here -- see backend_test.go for why they are guarded that
// way and not run by default.
//
// It CHANGES THINGS: it rezzes prims on whatever land the avatar is
// standing on, and takes them away again.  A failure part way through
// can leave a prim behind, which is worth knowing before running it
// somewhere that matters.
//
// This exists because Build had no test at all.  It is the one
// operation that puts several prims in the world and links them in a
// single call, so the thing worth checking is not that it returns
// without error -- it is that the SIMULATOR ends up agreeing: the first
// prim asked for is the root, and every other prim hangs off it.
// Nothing about the request guarantees that; the linking is done by the
// simulator and it decides what the root is.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// liveSession attaches to a running slgod, or skips.
func liveSession(t *testing.T) *Session {
	t.Helper()
	addr := os.Getenv("SLGO_TEST_ADDR")
	if addr == "" {
		t.Skip("set SLGO_TEST_ADDR to a running slgod to exercise this against the grid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	h, err := Attach(ctx, addr, os.Getenv("SLGO_TEST_AGENT"))
	if err != nil {
		t.Fatalf("attaching: %v", err)
	}
	s, err := New(h)
	if err != nil {
		h.Close()
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestBuildLinksToTheFirstPrim builds a small tower and checks the
// simulator's own account of it.
func TestBuildLinksToTheFirstPrim(t *testing.T) {
	s := liveSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	where, err := s.Where(ctx)
	if err != nil {
		t.Fatalf("where: %v", err)
	}

	// The interest list has to fill before the objects it describes can
	// be found, and a session that has just attached has heard nothing.
	if err := s.Settle(ctx, 10*time.Second); err != nil {
		t.Fatalf("settling: %v", err)
	}

	at := where.Position
	at.X += 2
	prims := []Prim{
		{
			Name:        "slgo test base",
			Description: "the root",
			Position:    at,
			Size:        msg.Vector3{X: 1.0, Y: 1.0, Z: 0.25},
		},
		{
			Name:        "slgo test middle",
			Position:    msg.Vector3{X: at.X, Y: at.Y, Z: at.Z + 0.5},
			Size:        msg.Vector3{X: 0.6, Y: 0.6, Z: 0.6},
		},
		{
			// Turned, so that a rotation surviving the round trip is
			// exercised as well as a position.
			Name:     "slgo test cap",
			Position: msg.Vector3{X: at.X, Y: at.Y, Z: at.Z + 1.0},
			Size:     msg.Vector3{X: 0.4, Y: 0.4, Z: 0.4},
			Rotation: msg.PackQuaternion(0, 0, 0.3826834, 0.9238795), // 45 degrees about Z
		},
	}

	b, err := s.Build(ctx, prims)
	if err != nil {
		t.Fatalf("build: %v\n"+
			"        (an avatar with no active group cannot rez on land that "+
			"grants building to one)", err)
	}
	// Take it away whatever happens next, so a failed assertion does
	// not leave a tower standing.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.Delete(ctx, b.Root, msg.UUID{}); err != nil {
			t.Logf("could not clean up %s: %v", b.Root.ID, err)
		}
	})

	if len(b.Parts) != len(prims) {
		t.Fatalf("built %d prims, asked for %d", len(b.Parts), len(prims))
	}

	// What the whole call is for: the first prim asked for is the root.
	if parent, known := s.Parent(b.Root); !known || parent != 0 {
		t.Errorf("the root reports parent %d (known %v), want 0", parent, known)
	}
	for _, p := range b.Parts[1:] {
		parent, known := s.Parent(p)
		if !known {
			t.Errorf("%s was never described by the simulator", p.Name)
			continue
		}
		if parent != b.Root.Local {
			t.Errorf("%s hangs off %d, want the root %d", p.Name, parent, b.Root.Local)
		}
	}

	// And the simulator's own account of it, which is the only thing
	// that says the name and description actually stuck.
	props, err := s.Properties(ctx, b.Root, 20*time.Second)
	if err != nil {
		t.Fatalf("properties: %v", err)
	}
	if props.Name != prims[0].Name {
		t.Errorf("the root is called %q, want %q", props.Name, prims[0].Name)
	}
	if props.Description != prims[0].Description {
		t.Errorf("description is %q, want %q", props.Description, prims[0].Description)
	}
}

// TestBuildOnePrim: a build of one is still a build, and is the case a
// caller reaches for without thinking about linking at all.
func TestBuildOnePrim(t *testing.T) {
	s := liveSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	where, err := s.Where(ctx)
	if err != nil {
		t.Fatalf("where: %v", err)
	}
	if err := s.Settle(ctx, 10*time.Second); err != nil {
		t.Fatalf("settling: %v", err)
	}

	at := where.Position
	at.X += 3
	b, err := s.Build(ctx, []Prim{{
		Name:     "slgo test single",
		Position: at,
		Size:     msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5},
	}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.Delete(ctx, b.Root, msg.UUID{}); err != nil {
			t.Logf("could not clean up %s: %v", b.Root.ID, err)
		}
	})

	if len(b.Parts) != 1 {
		t.Fatalf("built %d prims, want 1", len(b.Parts))
	}
	if parent, known := s.Parent(b.Root); known && parent != 0 {
		t.Errorf("a lone prim reports parent %d, want 0", parent)
	}
}
