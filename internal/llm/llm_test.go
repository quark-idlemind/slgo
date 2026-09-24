package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// server is a stand-in for llama-server or Ollama: it records the last
// request body and answers with whatever the test says.
type server struct {
	*httptest.Server
	mu   sync.Mutex
	path string
	body map[string]any
}

func newServer(t *testing.T, status int, reply string) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.path = r.URL.Path
		s.body = nil
		json.Unmarshal(b, &s.body)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) sent() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body
}

const okReply = `{"choices":[{"message":{"role":"assistant","content":"  {\"found\":false}\n"},"finish_reason":"stop"}],
	"usage":{"prompt_tokens":120,"completion_tokens":7}}`

func TestAReplyComesBackWithTheServersCounts(t *testing.T) {
	s := newServer(t, 200, okReply)
	c := New(Options{URL: s.URL, Model: "small"})
	r, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != `{"found":false}` {
		t.Errorf("text %q, want the content with the space round it taken off", r.Text)
	}
	if r.PromptTokens != 120 || r.CompletionTokens != 7 || r.FinishReason != "stop" {
		t.Errorf("got %+v", r)
	}
	s.mu.Lock()
	path := s.path
	s.mu.Unlock()
	if path != "/v1/chat/completions" {
		t.Errorf("asked %s", path)
	}
}

// The request carries what was configured, and temperature zero is sent
// rather than left to the server's default.
func TestTheRequestSaysWhatWasConfigured(t *testing.T) {
	s := newServer(t, 200, okReply)
	slot := 3
	c := New(Options{URL: s.URL, Model: "small", Slot: &slot, MaxTokens: 99})
	if _, err := c.Chat(context.Background(), []Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	b := s.sent()
	if b["model"] != "small" {
		t.Errorf("model %v", b["model"])
	}
	if b["id_slot"] != float64(3) {
		t.Errorf("id_slot %v, want 3", b["id_slot"])
	}
	if b["max_tokens"] != float64(99) {
		t.Errorf("max_tokens %v, want 99", b["max_tokens"])
	}
	if v, ok := b["temperature"]; !ok || v != float64(0) {
		t.Errorf("temperature %v (present %v), want an explicit 0", v, ok)
	}
	if b["stream"] != false {
		t.Errorf("stream %v", b["stream"])
	}
	msgs, _ := b["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages %v", b["messages"])
	}
	if m := msgs[0].(map[string]any); m["role"] != "system" || m["content"] != "be brief" {
		t.Errorf("first message %v", m)
	}
	if _, ok := b["response_format"]; ok {
		t.Errorf("response_format sent with no schema asked for")
	}
}

// Ollama has no slots, so no slot is no id_slot at all -- not id_slot 0,
// which on a llama-server shared with slbotd would be somebody else's.
func TestNoSlotIsNotSlotZero(t *testing.T) {
	s := newServer(t, 200, okReply)
	if _, err := New(Options{URL: s.URL}).Chat(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if v, ok := s.sent()["id_slot"]; ok {
		t.Errorf("id_slot %v sent with none configured", v)
	}
	zero := 0
	if _, err := New(Options{URL: s.URL, Slot: &zero}).Chat(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if v, ok := s.sent()["id_slot"]; !ok || v != float64(0) {
		t.Errorf("slot 0 configured, id_slot %v (present %v)", v, ok)
	}
}

func TestDefaultsFillTheZeroValues(t *testing.T) {
	s := newServer(t, 200, okReply)
	c := New(Options{URL: s.URL})
	if _, err := c.Chat(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if s.sent()["max_tokens"] != float64(DefaultMaxTokens) {
		t.Errorf("max_tokens %v", s.sent()["max_tokens"])
	}
	if c.http.Timeout != DefaultTimeout {
		t.Errorf("timeout %v", c.http.Timeout)
	}
}

// An address copied from a banner often ends in / or /v1; either way the
// request goes to one /v1/chat/completions and not two.
func TestTheAddressIsCleanedUp(t *testing.T) {
	s := newServer(t, 200, okReply)
	for _, u := range []string{s.URL, s.URL + "/", s.URL + "/v1", s.URL + "/v1/"} {
		c := New(Options{URL: u})
		if c.URL() != s.URL {
			t.Errorf("%q became %q", u, c.URL())
		}
		if _, err := c.Chat(context.Background(), nil, nil); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
}

func TestASchemaGoesInResponseFormat(t *testing.T) {
	s := newServer(t, 200, okReply)
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"found": map[string]any{"type": "boolean"}},
		"required":   []string{"found"},
	}
	if _, err := New(Options{URL: s.URL}).Chat(context.Background(), nil, &Schema{Name: "answer", Schema: schema}); err != nil {
		t.Fatal(err)
	}
	rf, _ := s.sent()["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format %v", s.sent()["response_format"])
	}
	js, _ := rf["json_schema"].(map[string]any)
	if js["name"] != "answer" {
		t.Errorf("name %v", js["name"])
	}
	got, _ := json.Marshal(js["schema"])
	want, _ := json.Marshal(schema)
	if string(got) != string(want) {
		t.Errorf("schema\n got %s\nwant %s", got, want)
	}
}

// Extra reaches the body, and wins over what this package set.
func TestExtraIsMergedIn(t *testing.T) {
	s := newServer(t, 200, okReply)
	c := New(Options{URL: s.URL, Extra: map[string]any{
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
		"temperature":          0.25,
	}})
	if _, err := c.Chat(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	b := s.sent()
	if kw, _ := b["chat_template_kwargs"].(map[string]any); kw["enable_thinking"] != false {
		t.Errorf("chat_template_kwargs %v", b["chat_template_kwargs"])
	}
	if b["temperature"] != 0.25 {
		t.Errorf("temperature %v", b["temperature"])
	}
}

// Nothing listening is ErrUnreachable, and says where it looked.
func TestNothingListeningIsUnreachable(t *testing.T) {
	// A port that was open a moment ago and is not now.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := "http://" + l.Addr().String()
	l.Close()

	_, err = New(Options{URL: addr}).Chat(context.Background(), nil, nil)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("got %v, want ErrUnreachable", err)
	}
	if errors.Is(err, ErrTimeout) || errors.Is(err, ErrMalformed) {
		t.Errorf("%v is more than one kind", err)
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("%q does not say where it looked", err)
	}
}

func TestAHostThatDoesNotResolveIsUnreachable(t *testing.T) {
	// .invalid is reserved never to resolve (RFC 6761).
	_, err := New(Options{URL: "http://nowhere.invalid:8080"}).Chat(context.Background(), nil, nil)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("got %v, want ErrUnreachable", err)
	}
}

// A refusal keeps the server's sentence, in both servers' shapes.
func TestAnHTTPErrorKeepsTheServersWords(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{404, `{"error":{"code":404,"message":"model \"nonesuch\" not found","type":"not_found_error"}}`, `model "nonesuch" not found`},
		{400, `{"error":"invalid schema"}`, "invalid schema"},
		{503, `Loading model`, "Loading model"},
		{500, ``, ""},
	} {
		s := newServer(t, c.status, c.body)
		_, err := New(Options{URL: s.URL}).Chat(context.Background(), nil, nil)
		var se *StatusError
		if !errors.As(err, &se) {
			t.Errorf("%d: got %v, want a StatusError", c.status, err)
			continue
		}
		if se.Code != c.status || se.Message != c.want {
			t.Errorf("%d: got %+v, want message %q", c.status, se, c.want)
		}
		if errors.Is(err, ErrUnreachable) || errors.Is(err, ErrMalformed) {
			t.Errorf("%d: %v is more than one kind", c.status, err)
		}
	}
}

func TestAReplyThatIsNotAReplyIsMalformed(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"not JSON", `<html>hello</html>`, "not JSON"},
		{"no choices", `{"choices":[]}`, "no choices"},
		{"empty", `{"choices":[{"message":{"role":"assistant","content":"  "},"finish_reason":"stop"}]}`, "empty"},
		{"cut off", `{"choices":[{"message":{"role":"assistant","content":"{\"found\": tr"},"finish_reason":"length"}]}`, "cut off"},
		// Nothing at all, and the limit reached: the tokens went on
		// reasoning the message does not carry.
		{"spent reasoning", `{"choices":[{"message":{"role":"assistant","content":"","reasoning":"Let me think"},"finish_reason":"length"}]}`,
			"limit of 512 tokens was reached; a model that reasons before it answers"},
	} {
		s := newServer(t, 200, c.body)
		_, err := New(Options{URL: s.URL}).Chat(context.Background(), nil, &Schema{Schema: map[string]any{}})
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: got %v, want ErrMalformed", c.name, err)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %q does not say %q", c.name, err, c.want)
		}
	}
}

// Cut off without a schema is still text, and is handed back.
func TestCutOffFreeTextIsStillAReply(t *testing.T) {
	s := newServer(t, 200, `{"choices":[{"message":{"content":"a long"},"finish_reason":"length"}]}`)
	r, err := New(Options{URL: s.URL}).Chat(context.Background(), nil, nil)
	if err != nil || r.Text != "a long" || r.FinishReason != "length" {
		t.Errorf("got %+v, %v", r, err)
	}
}

func TestASlowServerIsATimeout(t *testing.T) {
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer s.Close()
	defer close(release)

	_, err := New(Options{URL: s.URL, Timeout: 50 * time.Millisecond}).Chat(context.Background(), nil, nil)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("client timeout: got %v, want ErrTimeout", err)
	}
	if errors.Is(err, ErrUnreachable) {
		t.Errorf("%v is also unreachable", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = New(Options{URL: s.URL}).Chat(ctx, nil, nil)
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("context deadline: got %v, want ErrTimeout", err)
	}
}

// A caller that cancels gets its own cancellation back, not a story
// about the server.
func TestCancellingIsTheCallersOwn(t *testing.T) {
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer s.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := New(Options{URL: s.URL}).Chat(ctx, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrTimeout) || errors.Is(err, ErrUnreachable) {
		t.Errorf("%v is dressed up as something else", err)
	}
}
