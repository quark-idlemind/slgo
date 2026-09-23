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

	"github.com/quark-idlemind/slgo/internal/redact"
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
	// Only a URL on a host the simulator gave us a capability on is
	// accepted, and a redirect is followed only as far as another
	// such host; see Agent.RememberURL and Agent.checkRedirect.  It is
	// the host that is checked and not the whole URL, because the
	// host is what decides who is asked.  Any path on it is allowed,
	// and that is a guess against the random ids the simulator serves
	// capabilities under rather than a way anywhere else.
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

// http is the client every request this session makes over http goes
// through: capabilities, the seed they come from, and the event queue.
//
// It is the caller's client when there is one, copied rather than used
// as it stands, because the copy is given a redirect check the caller's
// may not have.  A capability is on a host the simulator named, and
// DoCap checks that before the request goes; but Go follows a redirect
// by default, wherever it points, and a check made on the way out says
// nothing about where the answer sends the request next.  Without this
// a 3xx from a capability host would be followed to any address at all,
// with the daemon as the one asking -- which is the thing the check on
// the way out exists to stop.
//
// A check the caller set is kept, and asked after this one.
func (a *Agent) http() *http.Client {
	c := http.Client{Timeout: 60 * time.Second}
	if a.HTTP != nil {
		c = *a.HTTP
	}
	theirs := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := a.checkRedirect(req, via); err != nil {
			return err
		}
		if theirs != nil {
			return theirs(req, via)
		}
		return nil
	}
	return &c
}

// maxRedirects is Go's own limit, which a client with a CheckRedirect
// of its own no longer gets unless it says so.
const maxRedirects = 10

// checkRedirect lets a redirect through only to where the request was
// already allowed to go.
//
// That is a host the simulator gave a capability on, or the host the
// request started on.  The second is not a loophole: DoCap sends
// nothing that has not passed the first, and what else goes through
// here was addressed by the simulator -- the seed capability of a
// region just arrived in, whose host is in no set this session holds
// yet, and the event queue of a region just left, whose host is in no
// set it holds any more.
func (a *Agent) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("agent: stopped after %d redirects", maxRedirects)
	}
	if len(via) > 0 && req.URL.Host == via[0].URL.Host {
		return nil
	}
	if a.sameHostAsACap(req.URL.String()) {
		return nil
	}
	return fmt.Errorf("agent: refused a redirect to %s, which is not a host this simulator gave us a capability on", req.URL.Host)
}

// HasCap reports whether the simulator offered this capability.
func (a *Agent) HasCap(name string) bool {
	_, ok := a.Caps().Get(name)
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
		base, ok := a.Caps().Get(r.Cap)
		if !ok {
			return nil, fmt.Errorf("agent: no %s capability", r.Cap)
		}
		url = strings.TrimRight(base, "/") + r.Path
		// Path is the caller's and is pasted on as text, so what it
		// makes has to be read back.  A capability served from the
		// root of its host leaves nothing after the host to stop a
		// Path of "@elsewhere/" turning the host into a user name and
		// naming a new one, ".example.net/" making a longer host name,
		// or ":22/" another port -- each an address the simulator
		// never gave, reached by naming a capability it did.
		if !sameHost(url, base) {
			return nil, fmt.Errorf("agent: %s with path %q is not on the host of that capability", r.Cap, r.Path)
		}
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
		return nil, redact.Error(err)
	}
	req.Header.Set("Accept", "application/llsd+xml")
	if r.Type != "" {
		req.Header.Set("Content-Type", r.Type)
	} else if len(r.Body) > 0 {
		req.Header.Set("Content-Type", "application/llsd+xml")
	}

	// Named by the capability, and with the URL the HTTP client puts
	// in its error cut to the host: that URL is the credential, and
	// this error goes wherever the caller sends it, a log included.
	// See package redact.
	resp, err := a.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent: %s%s: %w", r.Cap, r.Path, redact.Error(err))
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

	// The one capability whose answer is state the session keeps: see
	// Maturity for why it is heard here or not at all.
	if r.URL == "" && r.Cap == MaturityCap {
		a.noteMaturity(resp.StatusCode, b)
	}

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
	for _, c := range a.Caps() {
		pc, err := neturl.Parse(c)
		if err == nil && pc.Host == pu.Host {
			return true
		}
	}
	return false
}

// sameHost reports whether u is on the host of base.  As above, a
// capability that will not parse is nobody's host.
func sameHost(u, base string) bool {
	pb, err := neturl.Parse(base)
	if err != nil || pb.Host == "" {
		return false
	}
	pu, err := neturl.Parse(u)
	return err == nil && pu.Host == pb.Host
}
