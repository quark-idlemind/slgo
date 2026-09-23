package main

// ask end to end against a fake model server.
//
// The server is httptest's, answering /v1/chat/completions with replies
// written here, so what is tested is everything between the question
// and the screen: retrieval, the prompt, the checks, the retry, the
// fallback, and the printing.  Every reply is invented; nothing here is
// a model's real output.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/quark-idlemind/slgo/internal/askindex"
)

// fakeModel answers each chat request with the next of its replies,
// and keeps the request bodies.
type fakeModel struct {
	mu      sync.Mutex
	replies []string
	bodies  []map[string]any
}

func (f *fakeModel) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		n := len(f.bodies) - 1
		f.mu.Unlock()
		if n >= len(f.replies) {
			http.Error(w, "no more replies", 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": f.replies[n]},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func askJSON(t *testing.T, a askAnswer) string {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var askSetHome = askSuggestion{
	Command:     "landmark",
	CommandLine: "landmark --set-home",
	Why:         "it makes where you stand your home",
	Quote:       "Make where this avatar is standing the place home is.",
}

const askHomeQuestion = "how do I make where I am standing my home"

func TestAskRunKeepsACheckedSuggestion(t *testing.T) {
	f := &fakeModel{replies: []string{askJSON(t, askAnswer{Found: true, Answer: "Use landmark.", Suggestions: []askSuggestion{askSetHome}})}}
	srv := f.serve(t)
	res, err := askRun(context.Background(), newAskClient(askConfig{URL: srv.URL}, nil), askHomeQuestion, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Kept) != 1 || res.Kept[0].CommandLine != "landmark --set-home" {
		t.Fatalf("kept %+v, rejected %+v", res.Kept, res.Rejected)
	}
	if res.Retried || res.PromptTokens != 100 || res.Answer == nil || len(res.Candidates) == 0 {
		t.Errorf("result %+v", res)
	}
	if !askHasCandidate(res.Candidates, "landmark") {
		t.Errorf("landmark was not among the candidates shown")
	}
}

func TestAskRunRetriesAnInventedFlag(t *testing.T) {
	bad := askSetHome
	bad.CommandLine = "landmark --make-home"
	f := &fakeModel{replies: []string{
		askJSON(t, askAnswer{Found: true, Suggestions: []askSuggestion{bad}}),
		askJSON(t, askAnswer{Found: true, Suggestions: []askSuggestion{askSetHome}}),
	}}
	srv := f.serve(t)
	res, err := askRun(context.Background(), newAskClient(askConfig{URL: srv.URL}, nil), askHomeQuestion, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Retried || len(res.Kept) != 1 || len(res.Rejected) != 1 {
		t.Fatalf("retried %v kept %+v rejected %+v", res.Retried, res.Kept, res.Rejected)
	}
	if !strings.Contains(strings.Join(res.Rejected[0].Reasons, " "), "--make-home") {
		t.Errorf("reasons %q do not name the flag", res.Rejected[0].Reasons)
	}
	if res.PromptTokens != 200 {
		t.Errorf("prompt tokens %d, want both calls counted", res.PromptTokens)
	}
	// The retry carried the refusal to the model.
	msgs, _ := f.bodies[1]["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if s, _ := last["content"].(string); !strings.Contains(s, "--make-home") {
		t.Errorf("the retry does not say what was wrong: %q", s)
	}
}

func TestAskRunWithoutAClientIsRetrievalOnly(t *testing.T) {
	res, err := askRun(context.Background(), nil, askHomeQuestion, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != nil || len(res.Kept) != 0 || !askHasCandidate(res.Candidates, "landmark") {
		t.Fatalf("result %+v", res)
	}
}

func TestAskRunUnreachableKeepsTheCandidates(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	res, err := askRun(context.Background(), newAskClient(askConfig{URL: url}, nil), askHomeQuestion, nil)
	if err == nil || res == nil || len(res.Candidates) == 0 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if !strings.Contains(askWhyNoModel(url, err), "nothing is answering") {
		t.Errorf("why %q", askWhyNoModel(url, err))
	}
}

func TestAskClientCarriesExtra(t *testing.T) {
	f := &fakeModel{replies: []string{askJSON(t, askAnswer{Found: false, Answer: "no"})}}
	srv := f.serve(t)
	slot := 2
	extra, err := askParseExtra(`{"chat_template_kwargs": {"enable_thinking": false}}`)
	if err != nil {
		t.Fatal(err)
	}
	c := newAskClient(askConfig{URL: srv.URL, Model: "example-4b", Slot: &slot}, extra)
	if _, err := askRun(context.Background(), c, askHomeQuestion, nil); err != nil {
		t.Fatal(err)
	}
	b := f.bodies[0]
	if b["model"] != "example-4b" || b["id_slot"] != float64(2) {
		t.Errorf("body %v", b)
	}
	if k, _ := b["chat_template_kwargs"].(map[string]any); k == nil || k["enable_thinking"] != false {
		t.Errorf("extra not sent: %v", b["chat_template_kwargs"])
	}
	if newAskClient(askConfig{}, nil) != nil {
		t.Error("no URL should be no client")
	}
	if _, err := askParseExtra("[1]"); err == nil {
		t.Error("a JSON array was taken as ask_extra")
	}
}

func askHasCandidate(cands []askCandidate, name string) bool {
	for _, c := range cands {
		if c.Name == name {
			return true
		}
	}
	return false
}

// newAskShell is a test shell whose settings file and hints file are in
// a temporary directory, pointed at url for its model.
func newAskShell(t *testing.T, url string) *testShell {
	t.Helper()
	t.Setenv("SLSH_CONFIG_DIR", t.TempDir())
	cfg := DefaultConfig()
	cfg.Addr = "fake:7807"
	cfg.AskURL = url
	return newTestShellOn(t, newFakeGrid(t), cfg)
}

func TestAskCommandPrintsTheCheckedLine(t *testing.T) {
	bad := askSetHome
	bad.CommandLine = "sethome"
	f := &fakeModel{replies: []string{askJSON(t, askAnswer{Found: true, Answer: "Type sethome.", Suggestions: []askSuggestion{askSetHome, bad}})}}
	x := newAskShell(t, f.serve(t).URL)
	got := x.do(t, "ask -r "+askHomeQuestion)
	for _, want := range []string{
		"to type:  landmark --set-home",
		"it makes where you stand your home",
		`"Make where this avatar is standing the place home is." -- man landmark`,
		"Refused by the checks",
		"sethome: no such command",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Type sethome.") || strings.Contains(got, "to type:  sethome") {
		t.Errorf("unchecked model text printed:\n%s", got)
	}
}

func TestAskCommandNotFound(t *testing.T) {
	f := &fakeModel{replies: []string{askJSON(t, askAnswer{Found: false, Answer: "slsh has no command for that."})}}
	x := newAskShell(t, f.serve(t).URL)
	got := x.do(t, "ask how do I make where I am standing my home")
	if !strings.HasPrefix(got, "No slsh command found for that.\nThe nearest are:\n") || !strings.Contains(got, "landmark") {
		t.Errorf("got:\n%s", got)
	}
}

func TestAskCommandFallsBackWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	x := newAskShell(t, url)
	got := x.do(t, "ask "+askHomeQuestion)
	if !strings.HasPrefix(got, "From the index alone (nothing is answering at "+url) {
		t.Errorf("got:\n%s", got)
	}
	if !strings.Contains(got, "\nlandmark -- ") || !strings.Contains(got, "    landmark ") {
		t.Errorf("no landmark or its examples in:\n%s", got)
	}
}

func TestAskCommandWithoutAModel(t *testing.T) {
	x := newAskShell(t, "")
	got := x.do(t, "ask "+askHomeQuestion)
	if !strings.HasPrefix(got, "From the index alone (ask_url is not set)") {
		t.Errorf("got:\n%s", got)
	}
	f := &fakeModel{}
	x = newAskShell(t, f.serve(t).URL)
	got = x.do(t, "ask --index "+askHomeQuestion)
	if !strings.HasPrefix(got, "From the index alone (--index)") || len(f.bodies) != 0 {
		t.Errorf("--index asked the model, or said:\n%s", got)
	}
}

func TestAskHintsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, askHintsName)

	if h, err := loadAskHints(path); err != nil || h != nil {
		t.Fatalf("a missing file: %v %v", h, err)
	}

	os.WriteFile(path, []byte("# my words\n\nsnapshot picture\tlook texture\nwidget\tnosuchcommand look\nonly\tnosuchcommand\n"), 0o644)
	h, unknown, err := readAskHints(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []askindex.Hint{
		{When: []string{"snapshot", "picture"}, Commands: []string{"look", "texture"}},
		{When: []string{"widget"}, Commands: []string{"look"}},
	}
	if len(h) != len(want) || strings.Join(h[0].When, " ") != "snapshot picture" ||
		strings.Join(h[0].Commands, " ") != "look texture" || strings.Join(h[1].Commands, " ") != "look" {
		t.Errorf("hints %+v, want %+v", h, want)
	}
	if strings.Join(unknown, ";") != "line 4: nosuchcommand;line 5: nosuchcommand" {
		t.Errorf("unknown %q", unknown)
	}

	os.WriteFile(path, []byte("ok\tlook\nno tab here look\n"), 0o644)
	if _, err := loadAskHints(path); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("malformed line: %v", err)
	}
	os.WriteFile(path, []byte("\tlook\n"), 0o644)
	if _, err := loadAskHints(path); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("no words: %v", err)
	}
}

// TestAskHintsRaiseACommand: a hint takes a command the question's
// words would not have found to the top of the candidates.
func TestAskHintsRaiseACommand(t *testing.T) {
	q := "zqxv widget"
	hints := []askindex.Hint{{When: []string{"zqxv"}, Commands: []string{"emptytrash"}}}
	res, err := askRun(context.Background(), nil, q, hints)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) == 0 || res.Candidates[0].Name != "emptytrash" {
		t.Errorf("candidates %+v", res.Candidates)
	}
}

// The command's own hints file is read from the settings directory.
func TestAskCommandReadsTheHintsFile(t *testing.T) {
	x := newAskShell(t, "")
	dir, _ := ConfigDir()
	os.WriteFile(filepath.Join(dir, askHintsName), []byte("zqxv\temptytrash nosuchcommand\n"), 0o644)
	got := x.do(t, "ask --index zqxv")
	if !strings.Contains(got, "line 1: nosuchcommand: no such command, left out") || !strings.Contains(got, "\nemptytrash -- ") {
		t.Errorf("got:\n%s", got)
	}
}

