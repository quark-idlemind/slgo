package sl

import (
	"context"
	"testing"
)

// TestTheGroundAtAPointIsARectangleWithNoArea: one call on the backend
// answers both, and a point is asked as its own four sides.
func TestTheGroundAtAPointIsARectangleWithNoArea(t *testing.T) {
	w, f := newFakeSession(t)
	var asked [4]float32
	f.ground = func(west, south, east, north float32) (float32, bool) {
		asked = [4]float32{west, south, east, north}
		return 42.5, true
	}
	ctx := context.Background()

	h, known, err := w.Ground(ctx, 12.5, 30)
	if err != nil || !known || h != 42.5 {
		t.Fatalf("Ground = %v, %v, %v", h, known, err)
	}
	if asked != [4]float32{12.5, 30, 12.5, 30} {
		t.Errorf("a point was asked as %v", asked)
	}
	if _, _, err := w.HighestGround(ctx, 1, 2, 3, 4); err != nil {
		t.Fatal(err)
	}
	if asked != [4]float32{1, 2, 3, 4} {
		t.Errorf("a rectangle was asked as %v", asked)
	}

	f.ground = nil
	if _, known, _ := w.Ground(ctx, 12.5, 30); known {
		t.Error("the ground is known where no land has arrived")
	}
}
