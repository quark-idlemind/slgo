package agent

// The seed capability, which is how everything above UDP is reached.
//
// The reply also carries a Metadata map describing throttles and
// benefits.  It is nested, and the C client flattened it into the
// capability names -- which is why its log filled up with "Unknown
// capability: Metadata.account_level_benefits".  Anything that is not a
// URL is not a capability, and that is the whole of the rule.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedServer answers a capability request the way a simulator does.
func seedServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("seed capability was asked with %s, want POST", r.Method)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(hs.Close)
	return hs
}

// TestTheSeedCapabilityHandsBackOnlyURLs: a capability is a name and an
// address, and anything else in the reply belongs to something other
// than this map.
func TestTheSeedCapabilityHandsBackOnlyURLs(t *testing.T) {
	t.Parallel()

	hs := seedServer(t, `<llsd><map>
		<key>EventQueueGet</key><string>https://sim.example.com/cap/eq</string>
		<key>InventoryAPIv3</key><string>https://sim.example.com/cap/inv</string>
		<key>NotOffered</key><string></string>
		<key>Metadata</key><map>
			<key>account_level_benefits</key><string>whatever</string>
		</map>
	</map></llsd>`, http.StatusOK)

	caps, err := RequestCaps(context.Background(), hs.URL, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := caps.Get("InventoryAPIv3"); !ok || got != "https://sim.example.com/cap/inv" {
		t.Errorf("InventoryAPIv3 = %q, %v", got, ok)
	}
	// A capability offered as an empty string is one the simulator does
	// not have, and reporting it as present would send requests nowhere.
	if _, ok := caps.Get("NotOffered"); ok {
		t.Error("an empty URL was treated as a capability")
	}
	if _, ok := caps.Get("Metadata"); ok {
		t.Error("the metadata map was taken for a capability")
	}
	// The same rule applies to a map built anywhere else, since a client
	// on the far end of a link puts one together from what it was told.
	if _, ok := (Caps{"Blank": ""}).Get("Blank"); ok {
		t.Error("a capability with no URL was reported as present")
	}

	names := caps.Names()
	if len(names) != 2 || names[0] != "EventQueueGet" || names[1] != "InventoryAPIv3" {
		t.Errorf("Names = %v, want them sorted", names)
	}
}

// TestASeedCapabilityThatWillNotAnswer: none of these are worth
// continuing past, because everything above UDP needs the answer.
func TestASeedCapabilityThatWillNotAnswer(t *testing.T) {
	t.Parallel()

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	for _, c := range []struct {
		name string
		seed string
		body string
		code int
		says string
	}{
		{name: "no seed capability at all", says: "no seed capability"},
		{
			name: "a refusal",
			body: "who are you", code: http.StatusForbidden,
			says: "returned 403",
		},
		{
			name: "a reply that is not LLSD",
			body: "<not-llsd", code: http.StatusOK,
			says: "seed capability",
		},
		{
			// A map is what carries the names.  Anything else is a
			// simulator answering a different question.
			name: "LLSD that is not a map",
			body: `<llsd><string>go away</string></llsd>`, code: http.StatusOK,
			says: "wanted a map",
		},
		{name: "a server that is not there", seed: deadURL, says: "seed capability"},
		// The seed URL comes out of the login response, so one that
		// will not even parse is the login server's doing.
		{name: "a seed that is not a URL", seed: "://nonsense", says: "://nonsense"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			seed := c.seed
			if seed == "" && c.code != 0 {
				seed = seedServer(t, c.body, c.code).URL
			}
			_, err := RequestCaps(context.Background(), seed, []string{"EventQueueGet"}, nil)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("err = %v, should mention %q", err, c.says)
			}
		})
	}
}

// TestASeedRequestNamesWhatItWants: the request is the list of
// capabilities, and naming one that does not exist is harmless -- the
// reply simply omits it.
func TestASeedRequestNamesWhatItWants(t *testing.T) {
	t.Parallel()

	var asked string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 1<<16)
		n, _ := r.Body.Read(b)
		asked = string(b[:n])
		fmt.Fprint(w, `<llsd><map></map></llsd>`)
	}))
	defer hs.Close()

	if _, err := RequestCaps(context.Background(), hs.URL, nil, nil); err != nil {
		t.Fatal(err)
	}
	// An empty list means the defaults, since a session that asked for
	// nothing would get nothing.
	for _, want := range []string{"InventoryAPIv3", "EventQueueGet", "LSLSyntax"} {
		if !strings.Contains(asked, want) {
			t.Errorf("the request did not ask for %s: %s", want, asked)
		}
	}
}
