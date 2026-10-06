package slate

// expect alphamode: a face's alpha mode, read from its material on the
// fake grid. The grid serves RenderMaterials as the region does -- a POST
// holding a zlib of LLSD binary ids, an answer the same way round -- and
// counts what it is asked, since the session asks once for an id.

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/internal/llsdbin"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids, made with tools/new-id.
var (
	matMask128 = msg.MustParseUUID("0f227e57-7e57-c0de-3254-8cd6c8871297")
	matMask64  = msg.MustParseUUID("10dc7e57-7e57-c0de-b950-84e6cad87f42")
	matEmitter = msg.MustParseUUID("39927e57-7e57-c0de-4985-32d2f6db2763")
	matOpaque  = msg.MustParseUUID("a95a7e57-7e57-c0de-7bca-58d5ab041176")
)

// theMaterials is what the region holds: a mask at two cutoffs, an
// emissive and a none.
var theMaterials = map[msg.UUID][2]int64{
	matMask128: {2, 128}, matMask64: {2, 64}, matEmitter: {3, 0}, matOpaque: {0, 0},
}

// materialsServer serves RenderMaterials and counts the ids it is asked for.
type materialsServer struct {
	mu   sync.Mutex
	asks []msg.UUID
}

func (f *fakeGrid) serveMaterials(t *testing.T) *materialsServer {
	t.Helper()
	m := &materialsServer{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		v, err := llsd.Decode(bytes.NewReader(body))
		if err != nil {
			t.Errorf("the request is not LLSD: %v", err)
			return
		}
		inner, err := llsdbin.DecodeZipped(llsd.Bytes(llsd.Map(v), "Zipped"))
		if err != nil {
			t.Errorf("the request does not decode: %v", err)
			return
		}
		var answer []any
		for _, e := range inner.([]any) {
			var id msg.UUID
			copy(id[:], e.([]byte))
			m.mu.Lock()
			m.asks = append(m.asks, id)
			m.mu.Unlock()
			if mat, ok := theMaps[id]; ok {
				answer = append(answer, map[string]any{"ID": []byte(id[:]), "Material": mat.llsd()})
			} else if def, ok := theMaterials[id]; ok {
				answer = append(answer, map[string]any{"ID": []byte(id[:]), "Material": map[string]any{
					"DiffuseAlphaMode": def[0], "AlphaMaskCutoff": def[1], "EnvIntensity": int64(0),
				}})
			}
		}
		z, _ := llsdbin.EncodeZipped(answer)
		out, _ := llsd.Encode(map[string]any{"Zipped": z})
		w.Write(out)
	}))
	t.Cleanup(s.Close)
	f.ex.mu.Lock()
	f.ex.matURL = s.URL
	f.ex.mu.Unlock()
	return m
}

func (m *materialsServer) asked(id msg.UUID) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, a := range m.asks {
		if a == id {
			n++
		}
	}
	return n
}

func withMaterial(face int, id msg.UUID) func(*sl.Seen) {
	return withFace(face, func(f *sl.Face) { f.Material = id })
}

func TestAlphaModeOfAPlainFaceIsDefault(t *testing.T) {
	f := newGrid(t)
	srv := f.serveMaterials(t)
	res := play(t, f, hdr+"expect alphamode sign face 0 is default within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "alphamode sign face 0 default")
	if len(srv.asks) != 0 {
		t.Errorf("a face with no material asked the region for %d materials", len(srv.asks))
	}
	// Default is not any of the others.
	f = newGrid(t)
	f.serveMaterials(t)
	res = play(t, f, hdr+"expect alphamode sign face 0 is blend within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched alphamode sign face 0 is blend")
}

func TestAlphaModeIsReadFromTheMaterial(t *testing.T) {
	f := newGrid(t)
	srv := f.serveMaterials(t)
	f.change(signLocal, withMaterial(1, matMask128))
	f.change(signLocal, withMaterial(2, matEmitter))
	f.change(signLocal, withMaterial(3, matOpaque))
	res := play(t, f, hdr+`expect alphamode sign face 1 is mask within 1s
expect alphamode sign face 2 is emissive within 1s
expect alphamode sign face 3 is none within 1s
expect no alphamode sign face 1 is none within 100ms
`)
	wantExit(t, res, 0)
	// The cutoff is printed for a mask and for no other mode.
	mustHave(t, res, "alphamode sign face 1 mask 128", "alphamode sign face 2 emissive", "alphamode sign face 3 none")
	mustNotHave(t, res, "emissive 0")
	// Each material was asked for once, however many polls read it.
	for _, id := range []msg.UUID{matMask128, matEmitter, matOpaque} {
		if n := srv.asked(id); n != 1 {
			t.Errorf("%s was asked for %d times, want 1", id, n)
		}
	}
	// The mode is the material's and not a cutoff: is mask takes any.
	f = newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(0, matMask64))
	wantExit(t, play(t, f, hdr+"expect alphamode sign face 0 is mask within 1s\n"), 0)
}

func TestAlphaModeBecomes(t *testing.T) {
	f := newGrid(t)
	f.serveMaterials(t)
	f.whenSaid("go", func() {
		f.changeAfter(t, 40*time.Millisecond, signLocal, withMaterial(1, matMask128))
		f.changeAfter(t, 250*time.Millisecond, signLocal, withMaterial(1, matEmitter))
	})
	res := play(t, f, hdr+`say "go" on 0
expect alphamode sign face 1 becomes mask within 1s
expect alphamode sign face 1 becomes emissive within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "alphamode sign face 1 default", "alphamode sign face 1 mask 128", "alphamode sign face 1 emissive")

	// A face that is already a mask has not become one, and another
	// face does not count.
	f = newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(1, matMask128))
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withMaterial(2, matMask128)) })
	res = play(t, f, hdr+"say \"go\" on 0\nexpect alphamode sign face 1 becomes mask within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched alphamode sign face 1 becomes mask")
}

func TestAlphaModeChangesWithTheCutoffAndTheMode(t *testing.T) {
	// A new cutoff is a new material, and a change, though the mode is the same.
	f := newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(0, matMask128))
	f.whenSaid("go", func() { f.changeAfter(t, 40*time.Millisecond, signLocal, withMaterial(0, matMask64)) })
	res := play(t, f, hdr+"say \"go\" on 0\nexpect alphamode sign face 0 changes within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "alphamode sign face 0 mask 128", "alphamode sign face 0 mask 64")

	// Nothing changes when the face does not.
	f = newGrid(t)
	f.serveMaterials(t)
	f.change(signLocal, withMaterial(0, matMask128))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect no alphamode sign face 0 changes within 200ms\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect alphamode sign face 0 changes within 200ms\n"), 1)

	// From no material to one is a change, and back is original.
	f = newGrid(t)
	f.serveMaterials(t)
	f.whenSaid("go", func() {
		f.changeAfter(t, 40*time.Millisecond, signLocal, withMaterial(0, matEmitter))
		f.changeAfter(t, 250*time.Millisecond, signLocal, withFace(0, func(fc *sl.Face) { fc.Material = msg.UUID{} }))
	})
	res = play(t, f, hdr+`say "go" on 0
expect alphamode sign face 0 changes within 1s
expect alphamode sign face 0 becomes original within 1s
`)
	wantExit(t, res, 0)
}

func TestAlphaModeOfALink(t *testing.T) {
	f, o := world(t)
	f.serveMaterials(t)
	o.extraSay = func(m msg.Message) {
		if g, ok := m.(*msg.ObjectGrab); ok && g.ObjectData.LocalID == 201 {
			f.change(201, withMaterial(0, matMask64))
		}
	}
	res := o.play(t, probeHdr+"touch vendor link 2\nexpect alphamode vendor link 2 face 0 becomes mask within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "alphamode vendor link 2 face 0 mask 64")
}

func TestAlphaModeNeedsTheCapability(t *testing.T) {
	f := newGrid(t) // serves no RenderMaterials
	f.change(signLocal, withMaterial(1, matMask128))
	res := play(t, f, hdr+"test \"a\" {\n  expect alphamode sign face 1 is mask within 1s\n}\ntest \"b\" {\n  expect fullbright sign face 1 is off within 500ms\n}\n")
	// The environment, not the product: exit 3, said as setup, and the
	// run stops before the next test.
	wantExit(t, res, 3)
	mustHave(t, res, "slate: setup: this session holds no RenderMaterials capability",
		"slgod is likely older than this slate, or the session logged in before it was upgraded: restart it from the same release")
	mustNotHave(t, res, `slate: test "b"`)
	// Another expectation on the same grid is not touched by it.
	wantExit(t, play(t, f, hdr+"expect fullbright sign face 1 is off within 500ms\n"), 0)
}

func TestAlphaModeSaysWhyItCouldNotReadAMaterial(t *testing.T) {
	f := newGrid(t)
	f.serveMaterials(t)
	// A material the region does not hold: nothing to say, and the step
	// says so rather than reading a mode that was guessed.
	f.change(signLocal, withMaterial(1, msg.MustParseUUID("3e2f7e57-7e57-c0de-9b13-70c8d1a2e65f")))
	res := play(t, f, hdr+"expect alphamode sign face 1 is mask within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "the material could not be read", "the region has no material")
}

func TestAlphaModeStaticChecks(t *testing.T) {
	ok := hdr + "expect alphamode sign face 1 is mask\n"
	mustCheck(t, ok)
	mustCheck(t, hdr+"expect alphamode sign link 1 face 0 becomes default\nexpect alphamode sign face 0 changes\nexpect alphamode sign face 0 becomes original\n")
	checkErr(t, hdr+"expect alphamode sign face all is mask\n", "there is no face all")
	checkErr(t, hdr+"expect alphamode ghost face 0 is mask\n", "ghost")
	mustCheck(t, hdr+"expect alphamode sign face 0 is mask as $m\n")
	mustCheck(t, hdr+"expect alphamode sign face 0 changes within 2s as $m\nwait 100ms\nexpect say $m on public from object sign\n")
	checkErr(t, hdr+"expect no alphamode sign face 0 is mask as $m\n", "a negative expectation matches nothing to bind")
	// A mode is text: a texture's uuid is not what it can be.
	checkErr(t, hdr+"expect alphamode sign face 0 is mask as $m\ntouch sign showing $m\n", "$m")
	checkErr(t, hdr+"expect alphamode sign face 99999999999 is mask\n", "face")
	parseErr(t, hdr+"expect alphamode sign face 0 is translucent\n", "expected default, none, blend, mask, emissive or original")
	parseErr(t, hdr+"expect alphamode sign face 0 is any as $m\n", "expected default, none, blend, mask, emissive or original")
	parseErr(t, hdr+"expect alphamode sign face 0 is $m\n", "expected default, none, blend, mask, emissive or original")
	parseErr(t, hdr+"expect alphamode sign face 0 is\n", "expected default, none, blend, mask, emissive or original")
	parseErr(t, hdr+"expect alphamode sign face 0 changes $m\n", "changes takes no value")
	parseErr(t, hdr+"expect alphamode sign is mask\n", "face")

	s := mustCheck(t, ok)
	x := s.Tests[0].Steps[0].Expect[0].AlphaMode
	if x == nil || x.Mode != "mask" || x.Face.Value != 1 || x.State.Kind != StateIs {
		t.Errorf("parsed as %+v", x)
	}
}

func TestAlphaModeBindsTheModeAsText(t *testing.T) {
	f := newGrid(t)
	f.serveMaterials(t)
	f.replyTo(func(m msg.Message) {
		switch text, _, _ := says(m); text {
		case "go":
			f.changeAfter(t, 40*time.Millisecond, signLocal, withMaterial(1, matMask128))
		case "again":
			f.relay(signSays("mask 128"))
		}
	})
	res := play(t, f, hdr+`say "go" on 0
expect alphamode sign face 1 changes within 1s as $m
say "again" on 0
expect say $m on public from object sign within 500ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, `capture $m = "mask 128" (step 1)`, `chat public from sign: "mask 128"`)

	// A mode with no cutoff is the bare word.
	f = newGrid(t)
	f.serveMaterials(t)
	f.whenSaid("go", func() { f.changeAfter(t, 40*time.Millisecond, signLocal, withMaterial(2, matEmitter)) })
	res = play(t, f, hdr+"say \"go\" on 0\nexpect alphamode sign face 2 becomes emissive within 1s as $e\n")
	wantExit(t, res, 0)
	mustHave(t, res, `capture $e = "emissive" (step 1)`)

	f = newGrid(t)
	f.serveMaterials(t)
	res = play(t, f, hdr+"expect alphamode sign face 0 is default as $d\n")
	wantExit(t, res, 0)
	mustHave(t, res, `capture $d = "default" (step 1)`)
}
