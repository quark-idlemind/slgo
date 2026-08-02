package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

	a := &Agent{Account: &Account{}, Caps: Caps{"UpdateScriptAgent": hs.URL + "/cap/upload"}}

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
	a := &Agent{Account: &Account{}, Caps: Caps{"X": "https://sim.example.com/cap/1"}}
	_, err := a.DoCap(context.Background(), CapRequest{URL: "https://evil.example.net/steal"})
	if err == nil {
		t.Fatal("a URL on a host the simulator never mentioned should be refused")
	}
	if !strings.Contains(err.Error(), "not a URL this simulator gave us") {
		t.Errorf("err = %v", err)
	}
}

func TestDoCapUnknownCapability(t *testing.T) {
	a := &Agent{Account: &Account{}, Caps: Caps{}}
	if _, err := a.DoCap(context.Background(), CapRequest{Cap: "Nope"}); err == nil {
		t.Error("expected an error")
	}
}
