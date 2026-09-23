// Package llm asks a language model one question over the
// OpenAI-compatible /v1/chat/completions endpoint, and gets back either
// the reply or an error that says which of the ways it can fail it was.
//
// # Why this is not slbotd's client
//
// cmd/slbotd/llm.go already talks to llama-server, and this is a second
// client on purpose rather than that one moved.  slbotd's is built
// round llama.cpp's own half of the API -- /slots, to save and restore a
// conversation's kv cache, and /props, to fingerprint what a saved cache
// belongs to -- and a daemon that keeps conversations for days needs
// exactly that and cannot be pointed at anything else.  What is wanted
// here is the other half only: one request, one reply, nothing kept.
// That half is the part other servers implement, so this one is written
// to work against Ollama as well as llama-server, and moving slbotd onto
// it would have been a refactor of a daemon that works in order to gain
// nothing it uses.
//
// Three things differ in consequence:
//
//   - The slot is optional.  llama-server takes id_slot on this endpoint
//     and slbotd depends on that; Ollama has no slots.  So it is sent
//     only when one was set, which is also what lets a shell use a slot
//     slbotd leaves alone on a llama-server the two of them share.
//
//   - Temperature is always sent, zero included.  slbotd sends it only
//     when it is above zero and so gets the server's default otherwise,
//     which llama.cpp documents as 0.8.  An answer that is to be checked
//     against a manual wants the most likely reply every time, and a
//     default somebody else chose is not that.
//
//   - Structured output.  A request may carry a JSON schema in
//     response_format, which llama-server turns into a grammar the
//     sampler cannot leave, and which Ollama documents support for too.
//     A model asked for JSON in words alone can forget a brace or wander
//     into prose; asked through a grammar, it cannot.  (Which servers
//     honour it was read from their documentation, not measured here:
//     the tests beside this file are against a stand-in.)
//
// # Errors
//
// A caller has three different things to do about failure, so the three
// are told apart rather than folded into one message:
//
//   - nothing answered at all (ErrUnreachable): no server is running, or
//     the address is wrong.  The sensible response is to carry on
//     without a model, and to say once how to point at one.
//   - the server answered and refused (*StatusError): a model name it
//     does not have, a request it would not take.  Its own sentence is
//     the useful part and is kept.
//   - the server answered 200 with something that is not a reply
//     (ErrMalformed): not JSON, no choices, nothing in the message, or a
//     reply cut off by the token limit.
//
// A fourth, ErrTimeout, is the server that was reached and did not
// finish in time.  It is kept apart from ErrUnreachable because the
// advice is the opposite: the server is there, and is slow.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// Defaults for the zero values of Options.
const (
	// DefaultTimeout covers the whole request, prompt and reply.  A few
	// thousand tokens of prompt on a CPU is seconds rather than
	// milliseconds, and a small model cold is slower than the same model
	// warm; two minutes is a guess on the generous side, not a
	// measurement, and a caller that knows better sets its own.
	DefaultTimeout = 2 * time.Minute

	// DefaultMaxTokens bounds the reply.  The answers this was written
	// for are a short JSON object, and a model that has not closed it in
	// this many tokens is looping rather than thinking.
	DefaultMaxTokens = 512
)

// Options says where the model is and how to ask it.
type Options struct {
	// URL is the server's base, such as http://127.0.0.1:8080 for
	// llama-server or http://127.0.0.1:11434 for Ollama.  A trailing
	// slash and a trailing /v1 are both taken off, since either is what
	// somebody copying an address out of a server's own banner is likely
	// to have.
	URL string

	// Model is sent as the request's model.  llama-server serving one
	// model ignores it; Ollama needs it, and says so if it is wrong.
	Model string

	// Slot is llama-server's id_slot, or nil for none.  A pointer so
	// that slot 0 and no slot at all are different things; see the head
	// of this file for why it is optional.
	Slot *int

	// Timeout is for the whole request.  Zero is DefaultTimeout.
	Timeout time.Duration

	// MaxTokens bounds the reply.  Zero is DefaultMaxTokens.
	MaxTokens int

	// Temperature is sent as it stands, zero included.
	Temperature float64

	// Extra is merged into every request body, for what one server
	// understands and the other does not -- llama-server's
	// chat_template_kwargs, say.  A key here that this package also sets
	// replaces it, which is the point of being able to set it.
	Extra map[string]any
}

// Client asks one server.  Safe for concurrent use.
type Client struct {
	base string
	o    Options
	http *http.Client
}

// New points at a server.  Nothing is asked of it here, so a server
// that is not up yet is found out on the first question and not before.
func New(o Options) *Client {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = DefaultMaxTokens
	}
	base := strings.TrimRight(o.URL, "/")
	base = strings.TrimSuffix(base, "/v1")
	return &Client{
		base: base,
		o:    o,
		http: &http.Client{Timeout: o.Timeout},
	}
}

// URL is the base the client asks, as it was cleaned up.
func (c *Client) URL() string { return c.base }

// Message is one turn of the conversation sent.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Schema is a JSON schema the reply must satisfy.
type Schema struct {
	// Name is required by the OpenAI form of response_format and
	// ignored by the servers this was written for.  Letters, digits,
	// underscores and dashes.
	Name string

	// Schema is the schema itself, as anything encoding/json can
	// marshal: a map, a struct, or json.RawMessage.
	Schema any
}

// Reply is what came back.
type Reply struct {
	// Text is the message content, with the space round it taken off.
	Text string

	// FinishReason is the server's: "stop" for a reply that ended, and
	// "length" for one the token limit cut off (which Chat reports as an
	// error when a schema was asked for, since half an object is not an
	// answer).
	FinishReason string

	// PromptTokens and CompletionTokens are the server's own counts,
	// where it gives them, and zero where it does not.  They are read
	// rather than estimated, which makes them the numbers to calibrate a
	// prompt budget against.
	PromptTokens     int
	CompletionTokens int
}

// ErrUnreachable is nothing answering at the address: no server, the
// wrong port, a name that does not resolve.
var ErrUnreachable = errors.New("nothing is answering")

// ErrTimeout is a server that was asked and did not finish in time.
var ErrTimeout = errors.New("the model did not answer in time")

// ErrMalformed is a 200 that did not carry a reply.
var ErrMalformed = errors.New("the model's answer was not a reply")

// StatusError is the server refusing, with the sentence it refused in.
type StatusError struct {
	Code    int    // 404, 500, ...
	Status  string // "404 Not Found"
	Message string // the server's own words, or enough of the body to recognise
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return "the model's server said " + e.Status
	}
	return "the model's server said " + e.Status + ": " + e.Message
}

// Chat asks for one reply.  schema may be nil for free text.
func (c *Client) Chat(ctx context.Context, msgs []Message, schema *Schema) (*Reply, error) {
	body := map[string]any{
		"model":       c.o.Model,
		"messages":    msgs,
		"stream":      false,
		"max_tokens":  c.o.MaxTokens,
		"temperature": c.o.Temperature,
	}
	if c.o.Slot != nil {
		body["id_slot"] = *c.o.Slot
	}
	if schema != nil {
		name := schema.Name
		if name == "" {
			name = "reply"
		}
		body["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   name,
				"strict": true,
				"schema": schema.Schema,
			},
		}
	}
	for k, v := range c.o.Extra {
		body[k] = v
	}

	raw, err := c.post(ctx, "/v1/chat/completions", body)
	if err != nil {
		return nil, err
	}

	var out struct {
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: not JSON (%v): %s", ErrMalformed, err, excerpt(raw))
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("%w: no choices in it: %s", ErrMalformed, excerpt(raw))
	}
	ch := out.Choices[0]
	r := &Reply{
		Text:             strings.TrimSpace(ch.Message.Content),
		FinishReason:     ch.FinishReason,
		PromptTokens:     out.Usage.Prompt,
		CompletionTokens: out.Usage.Completion,
	}
	if r.Text == "" {
		return nil, fmt.Errorf("%w: the message was empty (finish reason %q)", ErrMalformed, ch.FinishReason)
	}
	if schema != nil && ch.FinishReason == "length" {
		return nil, fmt.Errorf("%w: cut off at the limit of %d tokens before it was finished",
			ErrMalformed, c.o.MaxTokens)
	}
	return r, nil
}

// post sends one request and hands back the body of a 200.
func (c *Client) post(ctx context.Context, path string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+path, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("the model's address %q: %w", c.base, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.transportError(ctx, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, c.transportError(ctx, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: resp.StatusCode, Status: resp.Status, Message: serverSays(raw)}
	}
	return raw, nil
}

// transportError sorts a failure to get an answer at all into the two
// kinds a caller can act on, and leaves the rest as they came.
//
// A dial that failed is ErrUnreachable whatever the reason underneath --
// refused, no route, no such host -- because the advice is the same for
// all of them: nothing is there, start it or give another address.  A
// deadline is ErrTimeout, whether it was this client's own or the
// caller's context.  A caller's cancellation is passed back untouched,
// since that is the caller's own doing and needs no explaining to it.
func (c *Client) transportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	var op *net.OpError
	var dns *net.DNSError
	switch {
	case errors.As(err, &dns),
		errors.Is(err, syscall.ECONNREFUSED),
		errors.As(err, &op) && op.Op == "dial":
		return &unreachable{url: c.base, err: err}
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, os.ErrDeadlineExceeded),
		isTimeout(err):
		return fmt.Errorf("%w: nothing came back from %s within %v", ErrTimeout, c.base, c.o.Timeout)
	}
	return fmt.Errorf("the model at %s: %w", c.base, err)
}

func isTimeout(err error) bool {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Timeout()
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// unreachable is ErrUnreachable with the address and the reason, so that
// the message says where was tried and errors.Is still finds it.
type unreachable struct {
	url string
	err error
}

func (e *unreachable) Error() string {
	return fmt.Sprintf("nothing is answering at %s (%v)", e.url, innermost(e.err))
}

func (e *unreachable) Is(target error) bool { return target == ErrUnreachable }
func (e *unreachable) Unwrap() error        { return e.err }

// innermost is the last error in a chain, which for a refused dial is
// "connection refused" rather than three layers of Post and dial
// repeating the address that the message has already said.
func innermost(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

// serverSays pulls the message out of an error body.  Both servers put
// one there, in slightly different places, and it is the useful half of
// the answer: "model 'x' not found" says what to do and "404" does not.
func serverSays(b []byte) string {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && len(e.Error) > 0 {
		// {"error": {"message": "..."}}, the OpenAI shape, which
		// llama-server uses.
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Error, &obj) == nil && obj.Message != "" {
			return obj.Message
		}
		// {"error": "..."}, which Ollama's own endpoints use.
		var s string
		if json.Unmarshal(e.Error, &s) == nil && s != "" {
			return s
		}
	}
	return excerpt(b)
}

// excerpt is enough of a body to recognise it by, on one line.
func excerpt(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "..."
	}
	return s
}
