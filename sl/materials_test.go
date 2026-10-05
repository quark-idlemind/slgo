package sl

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/quark-idlemind/slgo/internal/llsdbin"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

var (
	matMask    = mustUUID("715d7e57-7e57-c0de-63a7-315acc29ee6d")
	matEmit    = mustUUID("75af7e57-7e57-c0de-4c01-bcc6bf0848aa")
	matNone    = mustUUID("a4787e57-7e57-c0de-beff-e42c6f221b72")
	matMissing = mustUUID("bba07e57-7e57-c0de-b043-cf9608d9f0e1")
	matNormal  = mustUUID("d5e97e57-7e57-c0de-afa8-c5ef41aca4f7")
)

// aMaterial is a Material map as the capability sends it.
func aMaterial(mode, cutoff int64) map[string]any {
	return map[string]any{
		"DiffuseAlphaMode": mode, "AlphaMaskCutoff": cutoff, "EnvIntensity": int64(0),
		"NormMap": llsdbin.UUID(matNormal), "NormOffsetX": int64(2500), "NormOffsetY": int64(-5000),
		"NormRepeatX": int64(10000), "NormRepeatY": int64(20000), "NormRotation": int64(15708),
		"SpecMap": llsdbin.UUID{}, "SpecOffsetX": int64(0), "SpecOffsetY": int64(0),
		"SpecRepeatX": int64(10000), "SpecRepeatY": int64(10000), "SpecRotation": int64(0),
		"SpecColor": []any{int64(255), int64(128), int64(0), int64(255)}, "SpecExp": int64(51),
	}
}

// aMaterialsRegion serves RenderMaterials and counts what it is asked.
// It knows the materials in known, answers for no other, and checks the
// request is what the viewer sends: an LLSD XML map holding a zlib of
// LLSD binary, an array of sixteen byte binaries.
type aMaterialsRegion struct {
	mu    sync.Mutex
	asked [][]msg.UUID
}

func (r *aMaterialsRegion) serve(t *testing.T, known map[msg.UUID]map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "POST" {
			t.Errorf("method %s, want POST", req.Method)
		}
		body, _ := io.ReadAll(req.Body)
		v, err := llsd.Decode(bytes.NewReader(body))
		if err != nil {
			t.Errorf("the request is not LLSD: %v", err)
			return
		}
		inner, err := llsdbin.DecodeZipped(llsd.Bytes(llsd.Map(v), "Zipped"))
		if err != nil {
			t.Errorf("the request's Zipped does not decode: %v", err)
			return
		}
		var ids []msg.UUID
		var answer []any
		for _, e := range inner.([]any) {
			raw, ok := e.([]byte)
			if !ok || len(raw) != 16 {
				t.Errorf("an id in the request is %#v", e)
				return
			}
			var id msg.UUID
			copy(id[:], raw)
			ids = append(ids, id)
			if m, ok := known[id]; ok {
				answer = append(answer, map[string]any{"ID": []byte(id[:]), "Material": m})
			}
		}
		r.mu.Lock()
		r.asked = append(r.asked, ids)
		r.mu.Unlock()
		z, err := llsdbin.EncodeZipped(answer)
		if err != nil {
			t.Error(err)
			return
		}
		out, _ := llsd.Encode(map[string]any{"Zipped": z})
		w.Write(out)
	}
}

func (r *aMaterialsRegion) requests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.asked)
}

func theMaterials() map[msg.UUID]map[string]any {
	return map[msg.UUID]map[string]any{
		matMask: aMaterial(2, 128), matEmit: aMaterial(3, 0), matNone: aMaterial(0, 0),
	}
}

func TestMaterialsReadsWhatTheCapabilitySends(t *testing.T) {
	w, f := newFakeSession(t)
	r := &aMaterialsRegion{}
	f.ServeCap(t, MaterialsCap, r.serve(t, theMaterials()))

	got, err := w.Materials(context.Background(), []msg.UUID{matMask, matEmit, {}, matMissing})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[matMissing] != nil {
		t.Fatalf("got %d materials, want the two the region knows: %v", len(got), got)
	}
	if len(r.asked) != 1 || len(r.asked[0]) != 3 {
		t.Fatalf("asked %v, want one request of the three non-zero ids", r.asked)
	}
	m := got[matMask]
	if m.AlphaMode != AlphaModeMask || m.AlphaMaskCutoff != 128 {
		t.Errorf("the mask material read as %v %d", m.AlphaMode, m.AlphaMaskCutoff)
	}
	if got[matEmit].AlphaMode != AlphaModeEmissive {
		t.Errorf("the emissive one read as %v", got[matEmit].AlphaMode)
	}
	// The rest of a material, in the units a person uses.
	if m.NormMap != matNormal || m.NormOffsetX != 0.25 || m.NormOffsetY != -0.5 ||
		m.NormRepeatX != 1 || m.NormRepeatY != 2 || m.NormRotation != 1.5708 {
		t.Errorf("the normal map's placing read as %+v", m)
	}
	if m.SpecColor != [4]uint8{255, 128, 0, 255} || m.SpecExp != 51 || !m.SpecMap.IsZero() {
		t.Errorf("the specular settings read as %+v", m)
	}
}

func TestMaterialsAreCachedByID(t *testing.T) {
	w, f := newFakeSession(t)
	r := &aMaterialsRegion{}
	f.ServeCap(t, MaterialsCap, r.serve(t, theMaterials()))
	ctx := context.Background()

	if _, err := w.Materials(ctx, []msg.UUID{matMask}); err != nil {
		t.Fatal(err)
	}
	// The same id sends nothing, and a new one asks for only itself.
	if _, err := w.Materials(ctx, []msg.UUID{matMask}); err != nil {
		t.Fatal(err)
	}
	if r.requests() != 1 {
		t.Fatalf("%d requests after asking twice for one id, want 1", r.requests())
	}
	got, err := w.Materials(ctx, []msg.UUID{matMask, matNone})
	if err != nil || len(got) != 2 {
		t.Fatalf("%v, %v", got, err)
	}
	if r.requests() != 2 || len(r.asked[1]) != 1 || r.asked[1][0] != matNone {
		t.Fatalf("asked %v, want the second request to name only the new id", r.asked)
	}

	// What is handed back is a copy: spoiling it does not spoil the cache.
	got[matMask].AlphaMode = AlphaModeNone
	again, _ := w.Materials(ctx, []msg.UUID{matMask})
	if again[matMask].AlphaMode != AlphaModeMask {
		t.Error("a caller changed the cached material")
	}

	// An id the region did not know is asked about again.
	w.Materials(ctx, []msg.UUID{matMissing})
	w.Materials(ctx, []msg.UUID{matMissing})
	if r.requests() != 4 {
		t.Errorf("%d requests, want an unknown id asked for each time", r.requests())
	}
}

func TestMaterialsAreAskedForInBatches(t *testing.T) {
	w, f := newFakeSession(t)
	r := &aMaterialsRegion{}
	known := map[msg.UUID]map[string]any{}
	var ids []msg.UUID
	for i := range 120 {
		id := matNormal // a signed id, one byte varied
		id[15] = byte(i)
		ids = append(ids, id)
		known[id] = aMaterial(1, 0)
	}
	f.ServeCap(t, MaterialsCap, r.serve(t, known))
	got, err := w.Materials(context.Background(), ids)
	if err != nil || len(got) != 120 {
		t.Fatalf("%d materials, %v", len(got), err)
	}
	if len(r.asked) != 3 || len(r.asked[0]) != 50 || len(r.asked[2]) != 20 {
		t.Errorf("asked in %d requests, want 50, 50 and 20", len(r.asked))
	}
}

func TestMaterialsWithoutTheCapabilityIsASentence(t *testing.T) {
	w, f := newFakeSession(t)
	_, err := w.Materials(context.Background(), []msg.UUID{matMask})
	if !errors.Is(err, ErrNoMaterialsCap) {
		t.Fatalf("error %v, want ErrNoMaterialsCap", err)
	}
	if len(f.Sent()) != 0 {
		t.Error("something was sent")
	}
	// Nothing to read needs no capability.
	if got, err := w.Materials(context.Background(), []msg.UUID{{}}); err != nil || len(got) != 0 {
		t.Errorf("a zero id: %v, %v", got, err)
	}
}

func TestMaterialsRefuseWhatIsNotAnAnswer(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"a status": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 503) },
		"not llsd": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) },
		"no zip": func(w http.ResponseWriter, r *http.Request) {
			b, _ := llsd.Encode(map[string]any{"Other": "x"})
			w.Write(b)
		},
		"a material with no mode": func(w http.ResponseWriter, r *http.Request) {
			z, _ := llsdbin.EncodeZipped([]any{map[string]any{"ID": []byte(matMask[:]), "Material": map[string]any{}}})
			b, _ := llsd.Encode(map[string]any{"Zipped": z})
			w.Write(b)
		},
	} {
		t.Run(name, func(t *testing.T) {
			w, f := newFakeSession(t)
			f.ServeCap(t, MaterialsCap, h)
			if got, err := w.Materials(context.Background(), []msg.UUID{matMask}); err == nil {
				t.Errorf("read %v", got)
			}
		})
	}
}

// aBoxWithMaterials is a box whose faces name the materials: face 0 none,
// 1 a mask, 2 an emissive, 3 a material the region lacks, the rest plain.
func aBoxWithMaterials(t *testing.T, f *fakeBackend) {
	t.Helper()
	faces := PlainFaces(6)
	faces[1].Material = matMask
	faces[2].Material = matEmit
	faces[3].Material = matMissing
	faces[4].Material = matNone
	te, err := EncodeTextureEntry(faces)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.objects = []*Seen{{Object: Object{ID: thePrim, Local: 4242, Name: "a thing"}, TextureEntry: te}}
	f.mu.Unlock()
}

func TestFaceAlphaMode(t *testing.T) {
	w, f := newFakeSession(t)
	r := &aMaterialsRegion{}
	f.ServeCap(t, MaterialsCap, r.serve(t, theMaterials()))
	aBoxWithMaterials(t, f)
	ctx := context.Background()

	for _, c := range []struct {
		face   int
		mode   AlphaMode
		cutoff uint8
	}{
		{1, AlphaModeMask, 128}, {2, AlphaModeEmissive, 0}, {4, AlphaModeNone, 0},
	} {
		mode, cutoff, err := w.FaceAlphaMode(ctx, aThing(w), c.face)
		if err != nil || mode != c.mode || cutoff != c.cutoff {
			t.Errorf("face %d: %v %d, %v; want %v %d", c.face, mode, cutoff, err, c.mode, c.cutoff)
		}
	}
	if r.requests() != 3 {
		t.Errorf("%d requests for three faces, want one each", r.requests())
	}

	// A face with no material is the default, and asks nobody.
	before := r.requests()
	mode, cutoff, err := w.FaceAlphaMode(ctx, aThing(w), 0)
	if err != nil || mode != AlphaModeDefault || cutoff != 0 {
		t.Errorf("a plain face: %v %d, %v", mode, cutoff, err)
	}
	if r.requests() != before {
		t.Error("a face with no material sent a request")
	}

	// A material the region does not know is an error, not a guess.
	if _, _, err := w.FaceAlphaMode(ctx, aThing(w), 3); err == nil {
		t.Error("a material the region lacks read as a mode")
	}
	if _, _, err := w.FaceAlphaMode(ctx, aThing(w), 9); err == nil {
		t.Error("a face the box lacks read as a mode")
	}
}

// A plain face needs no capability: a session without one still says
// default, and only a face with a material is refused.
func TestAPlainFaceNeedsNoCapability(t *testing.T) {
	w, f := newFakeSession(t)
	aBoxWithMaterials(t, f)
	ctx := context.Background()
	if mode, _, err := w.FaceAlphaMode(ctx, aThing(w), 0); err != nil || mode != AlphaModeDefault {
		t.Errorf("a plain face: %v, %v", mode, err)
	}
	if _, _, err := w.FaceAlphaMode(ctx, aThing(w), 1); !errors.Is(err, ErrNoMaterialsCap) {
		t.Errorf("a face with a material: %v, want ErrNoMaterialsCap", err)
	}
}

func TestAlphaModeNames(t *testing.T) {
	for m, want := range map[AlphaMode]string{
		AlphaModeNone: "none", AlphaModeBlend: "blend", AlphaModeMask: "mask",
		AlphaModeEmissive: "emissive", AlphaModeDefault: "default", 9: "AlphaMode(9)",
	} {
		if m.String() != want {
			t.Errorf("%d is %q, want %q", int(m), m.String(), want)
		}
	}
	// The first four are the protocol's own numbers.
	if AlphaModeNone != 0 || AlphaModeBlend != 1 || AlphaModeMask != 2 || AlphaModeEmissive != 3 {
		t.Error("the modes are not the protocol's numbers")
	}
}
