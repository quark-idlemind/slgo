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

	// summaries are the requests that asked for a note rather than for
	// conversation, kept apart because what is IN them is the point:
	// the backstory must never be one of them.
	summaries  []map[string]any
	summary    string
	summaryErr bool

	// dropsSystem makes the template keep only the first system
	// message, the way some models' templates do.
	dropsSystem bool

	// router makes it a server of several models, as b11960 answered
	// when started without one: a POST that names no model is refused,
	// and a GET /props that names none is answered for the router.
	// named is the model each request named, in order.
	router bool
	named  []string
}

func newFakeLLM(t *testing.T) *fakeLLM {
	t.Helper()
	f := &fakeLLM{
		reply: "Evening.", prompt: 120, cached: 100,
		summary: "They talked about the tide and the cargo. Their name is Quark.",
	}
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
		asking := isSummaryAsk(got)
		if asking {
			f.summaries = append(f.summaries, got)
		} else {
			f.asks = append(f.asks, got)
		}
		reply, prompt, cached := f.reply, f.prompt, f.cached
		if asking {
			reply = f.summary
			if f.summaryErr {
				f.mu.Unlock()
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
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

	mux.HandleFunc("/apply-template", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var got struct {
			Messages []Message `json:"messages"`
		}
		json.Unmarshal(b, &got)

		f.mu.Lock()
		drops := f.dropsSystem
		f.mu.Unlock()

		var out strings.Builder
		seenSystem := false
		for _, m := range got.Messages {
			if m.Role == "system" {
				if seenSystem && drops {
					continue
				}
				seenSystem = true
			}
			out.WriteString("<|im_start|>" + m.Role + "\n" + m.Content + "<|im_end|>\n")
		}
		json.NewEncoder(w).Encode(map[string]any{"prompt": out.String()})
	})

	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		router := f.router
		f.mu.Unlock()
		if !router {
			mux.ServeHTTP(w, r)
			return
		}

		model := r.URL.Query().Get("model")
		if r.Method == http.MethodPost {
			// The router reads a POST's model from its body and
			// ignores ?model=, so the body is read here and put back.
			b, _ := io.ReadAll(r.Body)
			var got struct {
				Model string `json:"model"`
			}
			json.Unmarshal(b, &got)
			model = got.Model
			r.Body = io.NopCloser(strings.NewReader(string(b)))
		}
		f.mu.Lock()
		f.named = append(f.named, model)
		f.mu.Unlock()

		switch {
		case model == "" && r.Method == http.MethodGet && r.URL.Path == "/props":
			json.NewEncoder(w).Encode(map[string]any{
				"role": "router", "max_instances": 2, "models_autoload": true,
				"model_alias": "llama-server", "model_path": "none",
				"build_info":                  "b11960-8642541b2",
				"default_generation_settings": map[string]any{"params": nil, "n_ctx": 0},
			})
		case model == "":
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
				"code": 400, "type": "invalid_request_error",
				"message": "model name is missing from the request",
			}})
		default:
			mux.ServeHTTP(w, r)
		}
	}))
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

// isSummaryAsk tells a request for a note from a request for
// conversation, by the instruction at the head of it.
func isSummaryAsk(req map[string]any) bool {
	msgs, _ := req["messages"].([]any)
	if len(msgs) == 0 {
		return false
	}
	first, _ := msgs[0].(map[string]any)
	content, _ := first["content"].(string)
	return strings.HasPrefix(content, Summarise[:40])
}

func (f *fakeLLM) sawSummaries() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.summaries...)
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

// A server of several models refuses a POST that does not name one, so
// every request this daemon makes names llm-model: the slots and the
// template as well as the chat.
func TestAServerOfSeveralModelsIsToldWhichOne(t *testing.T) {
	f := newFakeLLM(t)
	f.router = true
	l := NewLLM(f.URL, "small", 10*time.Second)
	ctx := context.Background()

	p, err := l.Props(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Model != "/models/small.gguf" || p.Settings.Ctx != 2048 {
		t.Errorf("props %+v, want the model's own", p)
	}
	if _, err := l.Chat(ctx, Ask{Messages: []Message{{Role: "user", Content: "hello"}}}); err != nil {
		t.Errorf("chat: %v", err)
	}
	if _, err := l.SaveSlot(ctx, 1, "example-abc.bin"); err != nil {
		t.Errorf("save: %v", err)
	}
	if _, err := l.RestoreSlot(ctx, 1, "example-abc.bin"); err != nil {
		t.Errorf("restore: %v", err)
	}
	if err := l.EraseSlot(ctx, 1); err != nil {
		t.Errorf("erase: %v", err)
	}
	if _, err := l.Template(ctx, []Message{{Role: "user", Content: "hello"}}); err != nil {
		t.Errorf("template: %v", err)
	}

	f.mu.Lock()
	named := append([]string(nil), f.named...)
	f.mu.Unlock()
	if len(named) != 6 {
		t.Fatalf("%d requests, want 6", len(named))
	}
	for i, m := range named {
		if m != "small" {
			t.Errorf("request %d named %q, want small", i, m)
		}
	}
}

// Asked without a model, a router answers for itself rather than
// failing, and a fingerprint of that answer would be the same for every
// model.  So it is an error, and the error says what to set.
func TestARouterNotToldWhichModelIsAnError(t *testing.T) {
	f := newFakeLLM(t)
	f.router = true
	l := NewLLM(f.URL, "", 10*time.Second)

	p, err := l.Props(context.Background())
	if err == nil {
		t.Fatalf("props %+v from a router with no model, want an error", p)
	}
	if !strings.Contains(err.Error(), "llm-model") {
		t.Errorf("err = %v, want it to name llm-model", err)
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

// llm-slots leaves the server's last slots alone: the pool is the first
// so many, and a setting above what the server has is the server's.
func TestLLMSlotsLimitsThePool(t *testing.T) {
	f := newFakeLLM(t) // two slots
	for _, c := range []struct{ set, want int }{{0, 2}, {1, 1}, {5, 2}} {
		cfg := DefaultConfig()
		cfg.LLMURL, cfg.ChatDir, cfg.LLMSlots = f.URL, t.TempDir(), c.set
		ch, err := NewChatter(cfg, func(string, ...any) {})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.ready(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := ch.slots.N(); got != c.want {
			t.Errorf("llm-slots %d: pool of %d, want %d", c.set, got, c.want)
		}
	}
}
