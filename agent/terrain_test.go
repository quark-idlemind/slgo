package agent

import (
	"bytes"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

func layer(typ uint8, body string) *msg.LayerData {
	m := &msg.LayerData{}
	m.LayerID.Type = typ
	m.LayerData.Data = []byte(body)
	return m
}

// TestOnlyLandIsKept: wind is on the same message and never stops, so
// recording it would grow without bound and drown the land it is mixed
// in with.
func TestOnlyLandIsKept(t *testing.T) {
	var tr Terrain
	tr.note(layer('L', "land"))
	tr.note(layer('7', "wind"))
	tr.note(layer('8', "cloud"))
	tr.note(layer('M', "varland"))
	tr.note(layer('W', "water"))

	got := tr.Patches()
	if len(got) != 2 {
		t.Fatalf("kept %d patches, want the two land ones: %v", len(got), got)
	}
	if got[0].Type != 'L' || string(got[0].Data) != "land" {
		t.Errorf("first patch = %c %q", got[0].Type, got[0].Data)
	}
	if got[1].Type != 'M' || string(got[1].Data) != "varland" {
		t.Errorf("second patch = %c %q", got[1].Type, got[1].Data)
	}
}

// TestTheLayerTypeSurvives: a variable sized region sends its land as M,
// and a viewer handed it as L decodes it as something else.
func TestTheLayerTypeSurvives(t *testing.T) {
	var tr Terrain
	tr.note(layer('M', "varland"))
	if got := tr.Patches()[0].Type; got != 'M' {
		t.Errorf("layer type = %c, want M", got)
	}
}

// TestPatchesAreCopied: the body belongs to a receive buffer that is
// reused, so keeping the slice would keep whatever arrived next.
func TestPatchesAreCopied(t *testing.T) {
	var tr Terrain
	buf := []byte("original")
	m := &msg.LayerData{}
	m.LayerID.Type = 'L'
	m.LayerData.Data = buf
	tr.note(m)

	copy(buf, "OVERWRIT")
	if got := string(tr.Patches()[0].Data); got != "original" {
		t.Errorf("patch = %q; the receive buffer was kept rather than copied", got)
	}
}

// TestTerrainIsBounded: terraforming sends more land after the first
// burst, and nothing here can tell an edit from a fresh patch, so a
// session on a region being landscaped must not grow forever.
func TestTerrainIsBounded(t *testing.T) {
	var tr Terrain
	body := bytes.Repeat([]byte("x"), 8<<10)
	for i := 0; i < 200; i++ {
		m := &msg.LayerData{}
		m.LayerID.Type = 'L'
		m.LayerData.Data = body
		tr.note(m)
	}
	n, size, dropped := tr.Stats()
	if size > TerrainLimit {
		t.Errorf("held %d bytes, over the %d limit", size, TerrainLimit)
	}
	if dropped == 0 {
		t.Error("nothing was dropped, so the limit did nothing")
	}
	if n == 0 {
		t.Error("everything was dropped")
	}
}

// TestCrossingForgetsTheLastRegion: the land of the region just left
// describes somewhere else entirely.
func TestCrossingForgetsTheLastRegion(t *testing.T) {
	var tr Terrain
	tr.note(layer('L', "somewhere"))
	tr.forget()
	if n, _, _ := tr.Stats(); n != 0 {
		t.Errorf("%d patches survived a crossing", n)
	}
}
