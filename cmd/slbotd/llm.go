package main

// The model, which is llama-server over HTTP.
//
// Two halves of its API are used and they are not the same shape.
// Generation goes through /v1/chat/completions, which is the
// OpenAI-compatible endpoint and so is the half that would work against
// something else.  The kv cache goes through /slots, which is
// llama.cpp's own and has no equivalent anywhere: it is what lets a
// conversation be put down and picked up again, and it is the reason
// this daemon talks to llama-server rather than to whatever is
// convenient.
//
// # What it costs
//
// Restoring a conversation's kv cache was measured at milliseconds,
// against seconds to prefill it, so the cost of carrying a conversation
// is the disk it sits on.  How much disk is decided by the model's
// shape -- grouped query attention -- more than by its size.
// Why: doc/slbotd.md#keeping-the-kv-cache
//
// # What the server does not check
//
// A state file restores by size and format and NOTHING ELSE.  The
// server does not know, and cannot be asked, whether the file came from
// the model it is running now -- so a state saved under one model and
// restored under another may load cleanly and be nonsense.  That is
// what Fingerprint is for, and why a conversation records what it was
// saved by.  A bad file is refused with 400 and the server carries on,
// which was measured too: truncated, garbage and absent files all come
// back the same way and none of them brought it down.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLM is a llama-server.
type LLM struct {
	url   string
	model string
	http  *http.Client
}

// NewLLM points at a server.  Nothing is asked of it here: a daemon
// that refused to start because the model was not up yet would be a
// daemon that has to be started in an order, and the model is the part
// most likely to be restarted.
func NewLLM(url, model string, timeout time.Duration) *LLM {
	return &LLM{
		url:   strings.TrimRight(url, "/"),
		model: model,
		http:  &http.Client{Timeout: timeout},
	}
}

// Message is one turn as the model is told about it.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Ask is one request for a reply.
type Ask struct {
	Messages []Message

	// Slot is which of the server's parallel slots to use.  Named
	// rather than left to the server, because a conversation that
	// lands in a different slot each turn is a conversation whose kv
	// cache is somewhere else each turn.  Measured: id_slot is
	// honoured by the OpenAI-compatible endpoint, which is the whole
	// reason this daemon can pin one.
	Slot int

	MaxTokens   int
	Temperature float64
}

// Said is what came back.
type Said struct {
	Text string

	// Prompt is how many tokens the whole conversation came to and
	// Cached how many of them the server did not have to process
	// again.  Both are read rather than estimated: compaction in
	// chat.go is driven by what the tokeniser actually counted, which
	// is the difference between a budget and a guess at one.
	Prompt int
	Cached int
}

// Chat asks for a reply.
func (l *LLM) Chat(ctx context.Context, a Ask) (*Said, error) {
	body := map[string]any{
		"model":    l.model,
		"messages": a.Messages,
		"id_slot":  a.Slot,
		"stream":   false,
	}
	if a.MaxTokens > 0 {
		body["max_tokens"] = a.MaxTokens
	}
	if a.Temperature > 0 {
		body["temperature"] = a.Temperature
	}

	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			Details      struct {
				Cached int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := l.do(ctx, "POST", "/v1/chat/completions", body, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("the model answered with no choices at all")
	}
	return &Said{
		Text:   strings.TrimSpace(out.Choices[0].Message.Content),
		Prompt: out.Usage.PromptTokens,
		Cached: out.Usage.Details.Cached,
	}, nil
}

// SaveSlot writes a slot's kv cache to the server's slot-save-path,
// under a name of our choosing.
//
// The name is a file name and not a path: the directory is the
// server's, given to it with --slot-save-path, and a server started
// without one refuses every one of these.
func (l *LLM) SaveSlot(ctx context.Context, slot int, name string) (int, error) {
	var out struct {
		Saved   int `json:"n_saved"`
		Written int `json:"n_written"`
	}
	path := fmt.Sprintf("/slots/%d?action=save", slot)
	if err := l.do(ctx, "POST", path, map[string]any{"filename": name}, &out); err != nil {
		return 0, err
	}
	return out.Saved, nil
}

// RestoreSlot reads one back.
//
// Every failure is the same failure as far as the server is concerned:
// a missing file, a truncated one, one from another model and one that
// will not fit all come back as 400 with one sentence between them.  So
// a caller cannot tell them apart and must not try; the answer to any
// of them is to prefill the conversation from its text, which is why
// the text is what this daemon keeps and the kv cache is only ever an
// accelerator.
func (l *LLM) RestoreSlot(ctx context.Context, slot int, name string) (int, error) {
	var out struct {
		Restored int `json:"n_restored"`
	}
	path := fmt.Sprintf("/slots/%d?action=restore", slot)
	if err := l.do(ctx, "POST", path, map[string]any{"filename": name}, &out); err != nil {
		return 0, err
	}
	return out.Restored, nil
}

// EraseSlot drops what a slot holds, so that the next conversation to
// land in it does not match a prefix of somebody else's.
func (l *LLM) EraseSlot(ctx context.Context, slot int) error {
	path := fmt.Sprintf("/slots/%d?action=erase", slot)
	return l.do(ctx, "POST", path, map[string]any{}, nil)
}

// Props is what the server says about itself.
type Props struct {
	Model    string `json:"model_path"`
	Alias    string `json:"model_alias"`
	Ftype    string `json:"model_ftype"`
	Build    string `json:"build_info"`
	Slots    int    `json:"total_slots"`
	Settings struct {
		Ctx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

// Props asks what model is loaded and how many slots there are.
func (l *LLM) Props(ctx context.Context) (*Props, error) {
	var p Props
	if err := l.do(ctx, "GET", "/props", nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Fingerprint is everything a saved kv cache depends on.
//
// A state file is welded to the model that made it, to how that model
// was quantised, to the size of a slot's context and to the build of
// the server -- and the server checks none of it, so a state from
// another model can restore cleanly and be nonsense.  This is what
// stands in for the check it does not do: a conversation records the
// fingerprint it was saved under, and a state whose fingerprint has
// moved is not restored at all.
//
// The backstory is in it as well, because it is the head of every
// prompt: change the words an avatar was given and the cached prefix
// describes somebody else.
func (p *Props) Fingerprint(backstory string) string {
	h := sha256.New()
	for _, part := range []string{p.Model, p.Alias, p.Ftype, p.Build,
		fmt.Sprint(p.Settings.Ctx), backstory} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (l *LLM) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, l.url+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return fmt.Errorf("the model at %s: %w", l.url, err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// The server puts a sentence in the body and it is the only
		// useful half of the answer.  "Unable to restore slot" beats
		// "400 Bad Request" by the whole of what it says.
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, oneLine(b))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

// oneLine pulls the message out of an error body, or gives back enough
// of the body to recognise it by.
func oneLine(b []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return strings.ReplaceAll(s, "\n", " ")
}

// Template renders messages the way the model's own chat template will,
// without generating anything.
//
// It exists for one question: whether a second system message survives
// the template.  Some templates keep one and drop or merge the rest,
// and a memory that is silently dropped is worse than one that is
// obviously missing -- the avatar would go on answering, fluently,
// having forgotten everything, and nothing anywhere would say so.  So
// it is asked rather than assumed.  See Chatter.separateMemory.
func (l *LLM) Template(ctx context.Context, msgs []Message) (string, error) {
	var out struct {
		Prompt string `json:"prompt"`
	}
	body := map[string]any{"messages": msgs}
	if err := l.do(ctx, "POST", "/apply-template", body, &out); err != nil {
		return "", err
	}
	return out.Prompt, nil
}
