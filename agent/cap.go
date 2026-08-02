package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CapRequest is one HTTP request against a named capability.  The
// capability supplies the scheme, host and base path; Path is appended
// to it.
type CapRequest struct {
	Cap    string // capability name, e.g. "InventoryAPIv3"
	Method string // default GET
	Path   string // appended to the capability URL
	Body   []byte
	Type   string // content type of Body
}

// CapResponse is what came back.
type CapResponse struct {
	Status int
	Body   []byte
}

// OK reports a 2xx.
func (r *CapResponse) OK() bool { return r.Status >= 200 && r.Status < 300 }

// A CapDoer performs capability requests and says which capabilities
// exist.
//
// This is the seam that lets the same code run either in the process
// holding the grid connection or in a client on the far end of a link
// to it.  An Agent implements it by making the HTTPS request; a client
// implements it by asking the server to.  Nothing that uses a
// capability -- inventory today, script upload next -- needs to know
// which it is talking to.
type CapDoer interface {
	DoCap(ctx context.Context, req CapRequest) (*CapResponse, error)
	HasCap(name string) bool
}

var _ CapDoer = (*Agent)(nil)

func (a *Agent) http() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// HasCap reports whether the simulator offered this capability.
func (a *Agent) HasCap(name string) bool {
	_, ok := a.Caps.Get(name)
	return ok
}

// DoCap makes the request against the capability's URL.
func (a *Agent) DoCap(ctx context.Context, r CapRequest) (*CapResponse, error) {
	base, ok := a.Caps.Get(r.Cap)
	if !ok {
		return nil, fmt.Errorf("agent: no %s capability", r.Cap)
	}
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	url := strings.TrimRight(base, "/") + r.Path

	var body io.Reader
	if len(r.Body) > 0 {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/llsd+xml")
	if r.Type != "" {
		req.Header.Set("Content-Type", r.Type)
	} else if len(r.Body) > 0 {
		req.Header.Set("Content-Type", "application/llsd+xml")
	}

	resp, err := a.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent: %s%s: %w", r.Cap, r.Path, err)
	}
	defer resp.Body.Close()

	// Read the body whatever the status: a failure usually explains
	// itself there, and the caller can decide.
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("agent: %s%s: %w", r.Cap, r.Path, err)
	}
	return &CapResponse{Status: resp.StatusCode, Body: b}, nil
}
