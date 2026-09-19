package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The model is reached over HTTP, so a stand-in server is the whole of
// what is needed to test the half of this daemon that talks to it.
//
// The shapes it answers with are the ones llama-server b11056 actually
// answered with, copied from what it sent rather than from what its
// documentation says: the usage block, the slot save and restore
// replies and the error body are all as measured.

// fakeLLM is a llama-server that is not there.
type fakeLLM struct {
	*httptest.Server

	mu       sync.Mutex
	asks     []map[string]any // every chat request, decoded
	saved    []string         // filenames saved, in order
	restored []string         // filenames restored, in order
	erased   []int

	// reply is what it says; restoreErr makes every restore fail, the
	// way a stale or missing state file does.
	reply      string
	restoreErr bool
	prompt     int
	cached     int
}

func newFakeLLM(t *testing.T) *fakeLLM {
	t.Helper()
	f := &fakeLLM{reply: "Evening.", prompt: 120, cached: 100}
	mux := http.NewServeMux()

	mux.HandleFunc("/props", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"model_path": "/models/small.gguf", "model_alias": "small",
			"model_ftype": "Q4_K - Medium", "build_info": "b11056-e613ef2c8",
			"total_slots":                 2,
			"default_generation_settings": map[string]any{"n_ctx": 2048},
		})
	})

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var got map[string]any
		json.Unmarshal(b, &got)
		f.mu.Lock()
		f.asks = append(f.asks, got)
		reply, prompt, cached := f.reply, f.prompt, f.cached
		f.mu.Unlock()

		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": reply},
			}},
			"usage": map[string]any{
				"prompt_tokens":         prompt,
				"prompt_tokens_details": map[string]any{"cached_tokens": cached},
			},
		})
	})

	mux.HandleFunc("/slots/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var got struct {
			Filename string `json:"filename"`
		}
		json.Unmarshal(b, &got)
		slot := strings.TrimPrefix(r.URL.Path, "/slots/")

		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Query().Get("action") {
		case "save":
			f.saved = append(f.saved, got.Filename)
			json.NewEncoder(w).Encode(map[string]any{
				"id_slot": slot, "filename": got.Filename,
				"n_saved": 47, "n_written": 578920,
			})
		case "restore":
			if f.restoreErr {
				// Word for word what the real server answers, for a
				// missing file, a truncated one and one from another
				// model alike.
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
					"code": 400, "type": "invalid_request_error",
					"message": "Unable to restore slot: No available space in KV cache or invalid slot save file",
				}})
				return
			}
			f.restored = append(f.restored, got.Filename)
			json.NewEncoder(w).Encode(map[string]any{"n_restored": 47, "n_read": 578920})
		case "erase":
			n := 0
			if _, err := jsonScan(slot, &n); err == nil {
				f.erased = append(f.erased, n)
			}
			json.NewEncoder(w).Encode(map[string]any{})
		}
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// jsonScan reads an integer out of a path segment.
func jsonScan(s string, into *int) (int, error) {
	var n int
	err := json.Unmarshal([]byte(s), &n)
	*into = n
	return n, err
}

func (f *fakeLLM) sawAsks() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.asks...)
}

func (f *fakeLLM) sawSaved() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.saved...)
}

func (f *fakeLLM) sawRestored() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.restored...)
}

// ---------------------------------------------------------------- tests

func TestAskingTheModel(t *testing.T) {
	f := newFakeLLM(t)
	l := NewLLM(f.URL, "small", 10*time.Second)

	got, err := l.Chat(context.Background(), Ask{
		Messages:  []Message{{Role: "user", Content: "hello"}},
		Slot:      3,
		MaxTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "Evening." || got.Prompt != 120 || got.Cached != 100 {
		t.Errorf("got %+v", got)
	}

	asks := f.sawAsks()
	if len(asks) != 1 {
		t.Fatalf("%d requests", len(asks))
	}
	// The slot is named rather than left to the server: a conversation
	// that lands somewhere different each turn has its kv cache
	// somewhere else each turn.
	if asks[0]["id_slot"] != float64(3) {
		t.Errorf("id_slot = %v, want 3", asks[0]["id_slot"])
	}
	if asks[0]["max_tokens"] != float64(64) {
		t.Errorf("max_tokens = %v", asks[0]["max_tokens"])
	}
}

func TestSavingAndRestoringASlot(t *testing.T) {
	f := newFakeLLM(t)
	l := NewLLM(f.URL, "small", 10*time.Second)
	ctx := context.Background()

	if _, err := l.SaveSlot(ctx, 1, "example-abc.bin"); err != nil {
		t.Fatal(err)
	}
	if got := f.sawSaved(); len(got) != 1 || got[0] != "example-abc.bin" {
		t.Errorf("saved %v", got)
	}
	if _, err := l.RestoreSlot(ctx, 1, "example-abc.bin"); err != nil {
		t.Fatal(err)
	}
	if got := f.sawRestored(); len(got) != 1 || got[0] != "example-abc.bin" {
		t.Errorf("restored %v", got)
	}
}

// The server puts a sentence in the body and "400 Bad Request" on its
// own says none of it.
func TestARefusalCarriesTheServersOwnWords(t *testing.T) {
	f := newFakeLLM(t)
	f.restoreErr = true
	l := NewLLM(f.URL, "small", 10*time.Second)

	_, err := l.RestoreSlot(context.Background(), 1, "gone.bin")
	if err == nil {
		t.Fatal("a refused restore came back as success")
	}
	if !strings.Contains(err.Error(), "Unable to restore slot") {
		t.Errorf("err = %v, want the server's own sentence", err)
	}
}

// A state file is welded to the model that made it and the server
// checks none of that, so the fingerprint is the only thing standing
// between a stale cache and a reply made of somebody else's tokens.
func TestTheFingerprintMovesWithEverythingThatMatters(t *testing.T) {
	base := &Props{Model: "/models/a.gguf", Alias: "a", Ftype: "Q4_K - Medium", Build: "b11056"}
	base.Settings.Ctx = 2048
	story := "You are Hobb."

	same := base.Fingerprint(story)
	if same == "" {
		t.Fatal("no fingerprint at all")
	}
	if again := base.Fingerprint(story); again != same {
		t.Error("the same everything gave two fingerprints")
	}

	for _, tc := range []struct {
		name  string
		alter func(*Props) string
	}{
		{"another model", func(p *Props) string { p.Model = "/models/b.gguf"; return story }},
		{"another quantisation", func(p *Props) string { p.Ftype = "Q8_0"; return story }},
		{"another build", func(p *Props) string { p.Build = "b11057"; return story }},
		{"another context size", func(p *Props) string { p.Settings.Ctx = 4096; return story }},
		{"another backstory", func(p *Props) string { return "You are somebody else." }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := *base
			s := tc.alter(&p)
			if got := p.Fingerprint(s); got == same {
				t.Errorf("%s did not move the fingerprint", tc.name)
			}
		})
	}
}
