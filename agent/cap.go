package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"regexp"
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

	// URL is an absolute address to use instead of a named
	// capability.  Asset upload needs it: the first step posts to a
	// capability and the simulator answers with a one-shot uploader
	// URL to post the bytes to.
	//
	// Only a URL the simulator has handed us is accepted; see
	// Agent.RememberURL.
	URL string
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
	var url string
	switch {
	case r.URL != "":
		if !a.knownURL(r.URL) {
			return nil, fmt.Errorf("agent: %s is not a URL this simulator gave us", r.URL)
		}
		url = r.URL
	default:
		base, ok := a.Caps.Get(r.Cap)
		if !ok {
			return nil, fmt.Errorf("agent: no %s capability", r.Cap)
		}
		url = strings.TrimRight(base, "/") + r.Path
	}
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}

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

	// A reply may hand back a URL to post to next.  Remember those,
	// so a later request naming one is recognisable as something the
	// simulator offered rather than anywhere at all.
	a.rememberURLs(b)

	return &CapResponse{Status: resp.StatusCode, Body: b}, nil
}

// uploaderPattern finds the URLs a capability reply offers.  They are
// always on a host the simulator already gave us a capability for, so
// that is what makes one acceptable rather than the pattern itself.
var uploaderPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// rememberURLs records the addresses in a capability reply.
func (a *Agent) rememberURLs(body []byte) {
	if len(body) == 0 || len(body) > 1<<20 {
		return
	}
	found := uploaderPattern.FindAll(body, 32)
	if found == nil {
		return
	}
	a.urlMu.Lock()
	defer a.urlMu.Unlock()
	if a.urls == nil {
		a.urls = map[string]bool{}
	}
	for _, u := range found {
		s := string(u)
		if a.sameHostAsACap(s) {
			a.urls[s] = true
		}
	}
	// Keep the set from growing without bound over a long session.
	if len(a.urls) > 4096 {
		a.urls = map[string]bool{}
	}
}

// RememberURL marks a URL as one the simulator offered, for a caller
// that got it from somewhere this package did not see.
func (a *Agent) RememberURL(u string) {
	a.urlMu.Lock()
	defer a.urlMu.Unlock()
	if a.urls == nil {
		a.urls = map[string]bool{}
	}
	if a.sameHostAsACap(u) {
		a.urls[u] = true
	}
}

func (a *Agent) knownURL(u string) bool {
	a.urlMu.Lock()
	if a.urls[u] {
		a.urlMu.Unlock()
		return true
	}
	a.urlMu.Unlock()
	// A URL on the same host as a capability is one the simulator
	// serves, which is the property that matters.
	return a.sameHostAsACap(u)
}

// sameHostAsACap reports whether u is served by a host the simulator
// gave us a capability on.
func (a *Agent) sameHostAsACap(u string) bool {
	pu, err := neturl.Parse(u)
	if err != nil || pu.Host == "" {
		return false
	}
	for _, c := range a.Caps {
		pc, err := neturl.Parse(c)
		if err == nil && pc.Host == pu.Host {
			return true
		}
	}
	return false
}
