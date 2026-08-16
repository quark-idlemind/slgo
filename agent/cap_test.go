package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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
