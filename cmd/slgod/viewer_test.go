package main

// The one-time viewer password: making one, spending it, and the ways
// it stops being worth anything.
//
// These reach viewerHost directly rather than through a login, because
// what is being tested is the credential and not the handover: viewer's
// own tests take a Handover with a minted digest on it and drive a real
// XML-RPC login through it (viewer/login_test.go), and this side is
// where the digest comes from and where it goes.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/server"
)

// mintingHost is a viewer host with nothing behind it but a profile
// that may be handed over, and a log to search afterwards.
func mintingHost(t *testing.T, viewerPassword string) (*viewerHost, *strings.Builder) {
	t.Helper()
	var said strings.Builder
	v := newViewerHost(context.Background(), "127.0.0.1", nil,
		func(string) string { return viewerPassword },
		nil, nil,
		func(format string, args ...any) { said.WriteString(format) },
	)
	return v, &said
}

// TestAMintedPasswordIsSpentOnce: single use is the whole of what makes
// putting a password on a viewer's command line acceptable, so the
// second look at it has to come back empty.
func TestAMintedPasswordIsSpentOnce(t *testing.T) {
	v, _ := mintingHost(t, "$1$00157e577e57c0de028f000000000000")

	pass, life, err := v.Mint("example")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if life != viewerCredentialLife {
		t.Errorf("life %v, want %v", life, viewerCredentialLife)
	}

	digest, spend := v.oneTimeFor("example")
	if digest != agent.HashPassword(pass) {
		t.Fatalf("the stored digest is not the minted password's")
	}
	if spend == nil {
		t.Fatal("no way to spend the credential")
	}
	spend()

	if digest, spend := v.oneTimeFor("example"); digest != "" || spend != nil {
		t.Errorf("a spent credential is still on offer: %q", digest)
	}
}

// TestACredentialOutlivesAViewerStartingUp: the number this pins is a
// measurement, not a preference.  Timed live on this machine with warm
// caches and the viewer having just been running, "viewer --launch"
// reached the daemon's login endpoint 46 and 50 seconds later; a cold
// start is slower again.  The minute this began as expired mid-startup,
// which presents as a feature that does not work rather than as a
// window that was too tight, so the life has to leave real room over
// that fifty seconds.
//
// Shortening it back is the mistake this catches.  What makes putting a
// password on a command line acceptable is that it works once and that
// the endpoint is on loopback; the clock was never the load-bearing
// half.
func TestACredentialOutlivesAViewerStartingUp(t *testing.T) {
	const measured = 50 * time.Second
	if viewerCredentialLife < 4*measured {
		t.Errorf("a credential lives %v, and a viewer measured here took %v to reach the login; "+
			"that leaves nothing for a cold start", viewerCredentialLife, measured)
	}
}

// TestASpentPasswordDoesNotTakeALaterOneWithIt: two launches in quick
// succession, and the login from the first arriving after the second
// was minted.  Spending by name alone would delete the credential the
// second viewer is about to use and refuse a person who did nothing
// wrong.
func TestASpentPasswordDoesNotTakeALaterOneWithIt(t *testing.T) {
	v, _ := mintingHost(t, "$1$00157e577e57c0de028f000000000000")

	if _, _, err := v.Mint("example"); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	_, spendFirst := v.oneTimeFor("example")

	second, _, err := v.Mint("example")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	spendFirst()

	digest, _ := v.oneTimeFor("example")
	if digest != agent.HashPassword(second) {
		t.Errorf("spending the first credential took the second one with it")
	}
}

// TestMintingAgainDropsTheOneBefore: a person who starts a viewer twice
// must not leave a live credential behind them, so the second minting
// is also the first one's end.
func TestMintingAgainDropsTheOneBefore(t *testing.T) {
	v, _ := mintingHost(t, "$1$00157e577e57c0de028f000000000000")

	first, _, err := v.Mint("example")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	second, _, err := v.Mint("example")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if first == second {
		t.Fatal("two mintings produced the same password")
	}
	if digest, _ := v.oneTimeFor("example"); digest == agent.HashPassword(first) {
		t.Error("the first credential is still the live one")
	}
}

// TestAnExpiredPasswordIsNotOffered: the life is short on purpose --
// the plaintext is readable in ps for as long as the viewer runs -- so
// an old one has to be refused even though nothing has swept it away.
func TestAnExpiredPasswordIsNotOffered(t *testing.T) {
	v, _ := mintingHost(t, "$1$00157e577e57c0de028f000000000000")

	if _, _, err := v.Mint("example"); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	// Wound back rather than waited out: a test that slept a minute
	// would be a test nobody runs.
	v.credMu.Lock()
	v.creds["example"].expiry = time.Now().Add(-time.Second)
	v.credMu.Unlock()

	if digest, spend := v.oneTimeFor("example"); digest != "" || spend != nil {
		t.Errorf("an expired credential is still on offer: %q", digest)
	}
	v.credMu.Lock()
	_, left := v.creds["example"]
	v.credMu.Unlock()
	if left {
		t.Error("an expired credential was offered up and then kept")
	}
}

// TestAProfileWithNoViewerPasswordCannotBeMintedFor: viewer_password is
// what marks a profile as one that may be handed to a viewer at all
// (cmd/slgod/viewer.go, find).  Minting around it would turn a
// deliberate omission into no protection whatsoever.
func TestAProfileWithNoViewerPasswordCannotBeMintedFor(t *testing.T) {
	v, _ := mintingHost(t, "")

	pass, _, err := v.Mint("example")
	if err == nil {
		t.Fatal("minted a credential for a profile that may not be handed over")
	}
	if pass != "" {
		t.Error("a refused minting still produced a password")
	}
	if !strings.Contains(err.Error(), "viewer_password") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
	if digest, _ := v.oneTimeFor("example"); digest != "" {
		t.Error("a refused minting stored something anyway")
	}
}

// TestAMintedPasswordIsNeverLogged: it reaches a viewer's argv, which
// is bad enough; a copy in the daemon's log would outlive the minute
// that makes the first acceptable, and slgod's log is written to a
// terminal somebody may be sharing.
func TestAMintedPasswordIsNeverLogged(t *testing.T) {
	v, said := mintingHost(t, "$1$00157e577e57c0de028f000000000000")

	pass, _, err := v.Mint("example")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if strings.Contains(said.String(), pass) {
		t.Errorf("the minted password was logged: %s", said.String())
	}
}

// TestMintedPasswordsAreRandomAndFitAViewersLoginBox: sixteen
// characters is what panel_login.xml:144 will hold, so one that has to
// be pasted by hand still can be; and two of them being the same would
// mean the randomness was not there at all.
func TestMintedPasswordsAreRandomAndFitAViewersLoginBox(t *testing.T) {
	v, _ := mintingHost(t, "$1$00157e577e57c0de028f000000000000")

	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		pass, _, err := v.Mint("example")
		if err != nil {
			t.Fatalf("Mint: %v", err)
		}
		// Sixteen written out rather than taken from the constant:
		// what this protects is the agreement with the viewer's login
		// field, and a test that read the same constant the code does
		// would agree with any number somebody put there.
		if len(pass) != 16 {
			t.Fatalf("password %q is %d characters; a viewer's login box holds 16", pass, len(pass))
		}
		if strings.Trim(pass, "0123456789abcdef") != "" {
			t.Fatalf("password %q is not hex, so something on the way to the viewer may eat it", pass)
		}
		if seen[pass] {
			t.Fatalf("minted %q twice", pass)
		}
		seen[pass] = true
	}
}

// TestNoEndpointMeansNoLoginURI: serve() is what learns the address, so
// a host that has not served one has nothing to tell anybody -- and
// saying nothing is what the shell reads as "restart slgod with
// -viewer".
func TestNoEndpointMeansNoLoginURI(t *testing.T) {
	v, _ := mintingHost(t, "$1$00157e577e57c0de028f000000000000")
	if uri := v.LoginURI(); uri != "" {
		t.Errorf("LoginURI() = %q before anything was bound", uri)
	}
	if v.Attached("example") {
		t.Error("a viewer is attached to a host with no endpoint")
	}
}

// The seed a viewer is served.
//
// A viewer fetches its capabilities from slgod's seed, which is a proxy
// of the simulator's with the event queue pointed here.  Which
// simulator's is the question this pair of tests answers, and it is a
// question because a session moves: Account.SeedCapability names the
// region the session logged in to and goes on naming it after every
// teleport.

// capsServer is a region's seed capability, answering with one
// capability named after the region so that a proxied reply says which
// region it came from.
func capsServer(t *testing.T, region string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := llsd.Encode(map[string]any{
			"EventQueueGet":     "https://" + region + ".invalid/cap/event",
			"SimulatorFeatures": "https://" + region + ".invalid/cap/features",
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

// seededLoginServer is loginServer with a seed capability in the
// response, which is what makes the session fetch capabilities at all.
func seededLoginServer(t *testing.T, sim *fakeSim, seed string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-9eeb-1bd0e1ec6995</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-f4f4-19d29d124acf</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-d169-d9847afe641e</string></value></member>
		  <member><name>circuit_code</name><value><int>4242</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>seed_capability</name><value><string>%s</string></value></member>
		  <member><name>first_name</name><value><string>"Example"</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`, sim.addr().IP, sim.addr().Port, seed)
	}))
	t.Cleanup(s.Close)
	return s
}

// servedSeed stands up a hosted session and the capability endpoints in
// front of it, and hands back a way to ask for the seed a viewer would
// be given.
func servedSeed(t *testing.T, seed string) (*server.Server, *viewerHost, func() string) {
	t.Helper()
	sim := newSim(t)
	hs := seededLoginServer(t, sim, seed)

	srv := server.New()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		stop, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		srv.Close(stop)
		cancel()
	})

	login := agent.Login{First: "Example", Last: "Resident", Password: "secret", URL: hs.URL}
	if _, err := srv.StartAgent(ctx, "example", login, agent.Options{Idle: -1}); err != nil {
		t.Fatalf("hosting a session: %v", err)
	}

	vh := newViewerHost(ctx, "127.0.0.1", srv,
		func(string) string { return "" }, nil, nil, func(string, ...any) {})
	mux := http.NewServeMux()
	mux.HandleFunc("/cap/", vh.serveCap)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	vh.base = ts.URL

	return srv, vh, func() string {
		t.Helper()
		resp, err := http.Post(ts.URL+"/cap/example/seed", "application/llsd+xml",
			strings.NewReader("<llsd><array/></llsd>"))
		if err != nil {
			t.Fatalf("asking for the seed: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the seed answered %s: %s", resp.Status, body)
		}
		return string(body)
	}
}

// TestTheSeedAViewerIsServedIsTheRegionItIsInNow: after a teleport,
// a.Account.SeedCapability still addresses the simulator the avatar
// logged in to and has since left, so a viewer handed it fetches its
// inventory, its textures and its uploads from somewhere else.  The
// agent is asked instead, because the agent is the only thing that
// knows: every region after the first names its seed inside a
// TeleportFinish that nothing above that package reads.
//
// The divergence is made the other way round here, because a session
// with a fake simulator has no event queue and so cannot be teleported
// in a test: the ACCOUNT is pointed at another region's seed, which is
// exactly the state a move leaves it in, and the seed served must still
// be the region's.
func TestTheSeedAViewerIsServedIsTheRegionItIsInNow(t *testing.T) {
	here := capsServer(t, "here")
	left := capsServer(t, "left")

	srv, _, ask := servedSeed(t, here.URL)

	if body := ask(); !strings.Contains(body, "here.invalid") {
		t.Fatalf("the seed did not come from the region the avatar is in:\n%s", body)
	}

	h, ok := srv.Agent("example")
	if !ok {
		t.Fatal("the session is not hosted")
	}
	h.Agent().Account.SeedCapability = left.URL

	body := ask()
	if strings.Contains(body, "left.invalid") {
		t.Errorf("the viewer was served the seed of the region the avatar logged in to:\n%s", body)
	}
	if !strings.Contains(body, "here.invalid") {
		t.Errorf("the viewer was not served the region's own seed:\n%s", body)
	}
	// And the one entry that has to be ours, or two things poll the
	// simulator's queue and split the events between them.
	if !strings.Contains(body, "/cap/example/event") {
		t.Errorf("the event queue was not pointed at the daemon:\n%s", body)
	}
}

// TestASessionWithNoCapabilitiesSaysSoRatherThanProxyingNowhere: a
// session started with SkipCaps, or one whose move carried a seed that
// would not parse, has no seed to hand on.  Proxying to an empty URL
// answers a viewer with whatever an empty request produces, which is a
// worse answer than none.
func TestASessionWithNoCapabilitiesSaysSoRatherThanProxyingNowhere(t *testing.T) {
	sim := newSim(t)
	hs := loginServer(t, sim) // no seed capability in the response

	srv := server.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() {
		stop, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		srv.Close(stop)
	})
	login := agent.Login{First: "Example", Last: "Resident", Password: "secret", URL: hs.URL}
	if _, err := srv.StartAgent(ctx, "example", login, agent.Options{Idle: -1}); err != nil {
		t.Fatalf("hosting a session: %v", err)
	}

	vh := newViewerHost(ctx, "127.0.0.1", srv,
		func(string) string { return "" }, nil, nil, func(string, ...any) {})
	mux := http.NewServeMux()
	mux.HandleFunc("/cap/", vh.serveCap)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	vh.base = ts.URL

	resp, err := http.Post(ts.URL+"/cap/example/seed", "application/llsd+xml", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %s, want a refusal that says why", resp.Status)
	}
}
