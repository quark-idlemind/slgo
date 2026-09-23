package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// capAgent is an Agent with nothing wired up but its capabilities,
// which is all a capability request needs.
func capAgent(c Caps) *Agent {
	a := &Agent{Account: &Account{}}
	a.SetCaps(c)
	return a
}

// A capability reply can hand back a URL to post to next -- asset
// upload works that way.  The server will follow one, but only to a
// host the simulator already serves a capability on, so this cannot be
// turned into a way to make the server fetch anything at all.
func TestDoCapAbsoluteURL(t *testing.T) {
	var gotPath string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path == "/cap/upload" {
			fmt.Fprintf(w, `<llsd><map><key>state</key><string>upload</string>`+
				`<key>uploader</key><string>%s/uploader/abc</string></map></llsd>`,
				"http://"+r.Host)
			return
		}
		fmt.Fprint(w, `<llsd><map><key>state</key><string>complete</string></map></llsd>`)
	}))
	defer hs.Close()

	a := capAgent(Caps{"UpdateScriptAgent": hs.URL + "/cap/upload"})

	// Step one: an ordinary capability request.
	resp, err := a.DoCap(context.Background(), CapRequest{
		Cap: "UpdateScriptAgent", Method: "POST", Body: []byte("x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK() {
		t.Fatalf("status %d", resp.Status)
	}

	// Step two: the URL that reply handed back.
	uploader := hs.URL + "/uploader/abc"
	resp, err = a.DoCap(context.Background(), CapRequest{
		URL: uploader, Method: "POST", Body: []byte("asset bytes"),
	})
	if err != nil {
		t.Fatalf("the uploader URL was refused: %v", err)
	}
	if !resp.OK() || gotPath != "/uploader/abc" {
		t.Errorf("status %d, path %q", resp.Status, gotPath)
	}
}

func TestDoCapRefusesForeignURL(t *testing.T) {
	a := capAgent(Caps{"X": "https://sim.example.com/cap/1"})
	_, err := a.DoCap(context.Background(), CapRequest{URL: "https://evil.example.net/steal"})
	if err == nil {
		t.Fatal("a URL on a host the simulator never mentioned should be refused")
	}
	if !strings.Contains(err.Error(), "not a URL this simulator gave us") {
		t.Errorf("err = %v", err)
	}
}

func TestDoCapUnknownCapability(t *testing.T) {
	a := capAgent(Caps{})
	if _, err := a.DoCap(context.Background(), CapRequest{Cap: "Nope"}); err == nil {
		t.Error("expected an error")
	}
}

// TestACapRequestUsesTheClientItWasGiven: one process can hold as many
// sessions as it likes, so the HTTP client belongs to the session rather
// than to the package.
func TestACapRequestUsesTheClientItWasGiven(t *testing.T) {
	var used bool
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "" {
			used = true
		}
		fmt.Fprint(w, "ok")
	}))
	defer hs.Close()

	a := capAgent(Caps{"X": hs.URL})
	a.HTTP = &http.Client{Transport: headerStamp{http.DefaultTransport}}
	if _, err := a.DoCap(context.Background(), CapRequest{Cap: "X"}); err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Error("the session's own HTTP client was not used")
	}
}

// headerStamp marks the requests that went through it.
type headerStamp struct{ rt http.RoundTripper }

func (h headerStamp) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("X-Test", "yes")
	return h.rt.RoundTrip(r)
}

// TestACapRequestThatCannotBeMade: a capability URL comes from the
// simulator, so a malformed one is the simulator's doing and not a
// reason to panic on the way out.
func TestACapRequestThatCannotBeMade(t *testing.T) {
	a := capAgent(Caps{"X": "://not a url"})
	if _, err := a.DoCap(context.Background(), CapRequest{Cap: "X"}); err == nil {
		t.Error("expected an error building the request")
	}
}

// TestACapReplyThatStopsHalfway: the body is read whatever the status,
// because a failure usually explains itself there -- and a body that
// stops in the middle has to be an error rather than a short answer
// taken as the whole one.
func TestACapReplyThatStopsHalfway(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback TCP: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		// Promise a hundred bytes and then hang up.
		io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort")
		c.Close()
	}()

	a := capAgent(Caps{"X": "http://" + ln.Addr().String()})
	if _, err := a.DoCap(context.Background(), CapRequest{Cap: "X"}); err == nil {
		t.Error("a truncated body was accepted as an answer")
	}
}

// TestOnlyURLsTheSimulatorOfferedAreRemembered: asset upload hands back
// an address to post to next, and following one is the whole point --
// but only to a host the simulator already serves a capability on, so
// this cannot be turned into a way to make the session fetch anything at
// all.
func TestOnlyURLsTheSimulatorOfferedAreRemembered(t *testing.T) {
	a := capAgent(Caps{"X": "https://sim.example.com/cap/1"})

	a.RememberURL("https://sim.example.com/upload/1")
	a.RememberURL("https://evil.example.net/steal")
	a.RememberURL("not a url at all")

	if !a.knownURL("https://sim.example.com/upload/1") {
		t.Error("an address on the simulator's own host was refused")
	}
	if a.knownURL("https://evil.example.net/steal") {
		t.Error("an address on somebody else's host was accepted")
	}

	// A reply that is empty, or far too big to be one, is not scanned at
	// all: the set exists to hold a handful of uploader URLs.
	a.rememberURLs(nil)
	a.rememberURLs(make([]byte, 1<<20+1))

	// And it is emptied rather than grown without bound over a session
	// long enough to see thousands.
	for i := range 5000 {
		a.urls[fmt.Sprintf("https://sim.example.com/upload/%d", i)] = true
	}
	a.rememberURLs([]byte("https://sim.example.com/upload/again"))
	if len(a.urls) > 4096 {
		t.Errorf("the remembered set grew to %d", len(a.urls))
	}
}

// TestACapWithNoHostIsNobodysHost: a capability that will not parse must
// not match everything.
func TestACapWithNoHostIsNobodysHost(t *testing.T) {
	a := capAgent(Caps{"broken": "://nonsense"})
	if a.sameHostAsACap("https://sim.example.com/x") {
		t.Error("a capability that is not a URL matched one that is")
	}
	if a.sameHostAsACap("/relative/path") {
		t.Error("an address with no host matched a capability")
	}
}

// elsewhere is a server the simulator never mentioned, which counts the
// requests that reach it.  Anything that does is the daemon fetching an
// address a client chose.
func elsewhere(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "you should not be here")
	}))
	t.Cleanup(hs.Close)
	return hs, &hits
}

// TestARedirectOffTheSimulatorsHostIsNotFollowed: the host of a
// capability is checked before the request goes, and Go follows a
// redirect by default wherever it points -- so without a check on every
// hop, one 3xx from a capability host would have the daemon fetch any
// address at all.  Both with the default client and with one the caller
// supplied, because the check has to be on whichever is used.
func TestARedirectOffTheSimulatorsHostIsNotFollowed(t *testing.T) {
	away, hits := elsewhere(t)
	sim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, away.URL+"/private", http.StatusFound)
	}))
	defer sim.Close()

	for _, tc := range []struct {
		name string
		hc   *http.Client
	}{
		{"default client", nil},
		{"the caller's client", &http.Client{Transport: headerStamp{http.DefaultTransport}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := capAgent(Caps{"X": sim.URL + "/cap/1"})
			a.HTTP = tc.hc
			_, err := a.DoCap(context.Background(), CapRequest{Cap: "X"})
			if err == nil {
				t.Error("a redirect off the simulator's host was followed")
			} else if !strings.Contains(err.Error(), "refused a redirect") {
				t.Errorf("err = %v", err)
			}
			// And by absolute URL, which is the form a client names.
			_, err = a.DoCap(context.Background(), CapRequest{URL: sim.URL + "/uploader/1", Method: "POST"})
			if err == nil {
				t.Error("a redirect from an uploader URL off the simulator's host was followed")
			}
			if n := hits.Load(); n != 0 {
				t.Errorf("the other host was asked %d times", n)
			}
		})
	}
}

// TestARedirectAmongTheSimulatorsHostsIsFollowed: the check is on where
// a redirect goes, not on there being one.  The same host, or another
// host the simulator gave a capability on, is where the request could
// have been sent in the first place.
func TestARedirectAmongTheSimulatorsHostsIsFollowed(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "other")
	}))
	defer other.Close()
	sim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cap/moved":
			http.Redirect(w, r, "/cap/here", http.StatusFound)
		case "/cap/here":
			fmt.Fprint(w, "here")
		case "/cap/across":
			http.Redirect(w, r, other.URL+"/cap/2", http.StatusFound)
		}
	}))
	defer sim.Close()

	a := capAgent(Caps{
		"Moved":  sim.URL + "/cap/moved",
		"Across": sim.URL + "/cap/across",
		"Other":  other.URL + "/cap/2",
	})
	for name, want := range map[string]string{"Moved": "here", "Across": "other"} {
		resp, err := a.DoCap(context.Background(), CapRequest{Cap: name})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(resp.Body) != want {
			t.Errorf("%s: body %q, want %q", name, resp.Body, want)
		}
	}
}

// TestARedirectLoopStops: a client with a CheckRedirect of its own loses
// Go's limit of ten unless it keeps one, and a simulator that redirected
// to itself forever would then hold the request until its timeout.
func TestARedirectLoopStops(t *testing.T) {
	sim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	}))
	defer sim.Close()
	a := capAgent(Caps{"X": sim.URL + "/cap/1"})
	_, err := a.DoCap(context.Background(), CapRequest{Cap: "X"})
	if err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Errorf("err = %v, want the loop stopped", err)
	}
}

// TestTheSeedMayRedirectOnItsOwnHostAndNoFurther: a region's seed is
// asked before this session holds any capability on its host -- at
// login, and after every move -- so "a host the simulator gave us a
// capability on" alone would refuse every redirect the seed made, even
// to itself.  The host the request started on is allowed as well;
// nothing else is.
func TestTheSeedMayRedirectOnItsOwnHostAndNoFurther(t *testing.T) {
	away, hits := elsewhere(t)
	seed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/seed/moved":
			http.Redirect(w, r, "/seed/here", http.StatusTemporaryRedirect)
		case "/seed/here":
			fmt.Fprint(w, `<llsd><map><key>X</key><string>http://sim.invalid/cap/x</string></map></llsd>`)
		case "/seed/away":
			http.Redirect(w, r, away.URL+"/private", http.StatusTemporaryRedirect)
		}
	}))
	defer seed.Close()

	a := capAgent(Caps{})
	caps, err := RequestCaps(context.Background(), seed.URL+"/seed/moved", []string{"X"}, a.http())
	if err != nil {
		t.Fatalf("a seed redirected on its own host was refused: %v", err)
	}
	if _, ok := caps.Get("X"); !ok {
		t.Errorf("caps = %v", caps)
	}
	if _, err := RequestCaps(context.Background(), seed.URL+"/seed/away", []string{"X"}, a.http()); err == nil {
		t.Error("a seed redirected off its host was followed")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the other host was asked %d times", n)
	}
}

// TestAPathCannotMoveACapabilityToAnotherHost: Path is pasted onto the
// capability as text.  On a capability served from the root of its host
// a Path of "@host:port/" turns the capability's host into a user name
// and names a new one -- the daemon then fetches an address the
// simulator never gave, by way of a capability it did.
func TestAPathCannotMoveACapabilityToAnotherHost(t *testing.T) {
	away, hits := elsewhere(t)
	sim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "sim")
	}))
	defer sim.Close()

	a := capAgent(Caps{"Asset": sim.URL})
	path := "@" + strings.TrimPrefix(away.URL, "http://") + "/private"
	if _, err := a.DoCap(context.Background(), CapRequest{Cap: "Asset", Path: path}); err == nil {
		t.Error("a path that names another host was sent")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the other host was asked %d times", n)
	}

	// And an ordinary path on the same capability still goes.
	resp, err := a.DoCap(context.Background(), CapRequest{Cap: "Asset", Path: "/?texture_id=1"})
	if err != nil || string(resp.Body) != "sim" {
		t.Errorf("an ordinary path: %v", err)
	}
}
