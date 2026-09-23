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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/server"
	"github.com/quark-idlemind/slgo/viewer"
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
func servedSeed(t *testing.T, seed string) (*server.Server, *viewerHost, string, func() string) {
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

	// The token a successful login would have minted.  Asking for a
	// capability without one is what TestCapabilitiesAreRefusedWithoutTheLoginsToken
	// covers; here the login is assumed to have happened.
	token, err := vh.mintCapToken("example")
	if err != nil {
		t.Fatal(err)
	}

	return srv, vh, token, func() string {
		t.Helper()
		resp, err := http.Post(vh.capBase("example", token)+"/seed", "application/llsd+xml",
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

	srv, vh, token, ask := servedSeed(t, here.URL)

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
	// It carries the token too, or a viewer would be handed an event
	// queue URL that the gate then refuses.
	if want := vh.capBase("example", token) + "/event"; !strings.Contains(body, want) {
		t.Errorf("the event queue was not pointed at %s:\n%s", want, body)
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

	token, err := vh.mintCapToken("example")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(vh.capBase("example", token)+"/seed", "application/llsd+xml", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %s, want a refusal that says why", resp.Status)
	}
}

// Serving the viewer endpoint over TLS.
//
// The scheme is not a detail of transport here.  Everything a viewer is
// told to come back to is built from the address bound in serve -- the
// login URI a person types into a grid list, and the seed capability
// the login response carries -- so an endpoint that speaks TLS has to
// hand out https URLs or the viewer walks straight back to a port that
// is no longer listening for plaintext.

// selfSignedPair writes a certificate and key good for loopback, of the
// shape a viewer's certificate store insists on: a Subject Key
// Identifier, which Firestorm rejects a certificate for lacking, and a
// subjectAltName, which is what libcurl matches the host against
// (llsechandler_basic.cpp:905, _validateCert).
func selfSignedPair(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// The Subject Key Identifier the viewer requires: the SHA-1 of the
	// public key, which is what everything else generates too.
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	skid := sha1.Sum(pub)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "slgod viewer endpoint"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		SubjectKeyId:          skid[:],
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(
		&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	der, err = x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(
		&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// TestATLSEndpointNamesItselfWithHTTPS: the login URI and the seed both
// come from the address serve bound, so both have to say https.  A
// viewer given an http seed after a TLS login would ask this daemon for
// its capabilities in the clear, and be refused.
func TestATLSEndpointNamesItselfWithHTTPS(t *testing.T) {
	certFile, keyFile := selfSignedPair(t)
	v, _ := mintingHost(t, "$1$00157e577e57c0de028f000000000000")
	stop, err := v.serve("127.0.0.1:0", certFile, keyFile)
	if err != nil {
		t.Fatalf("serve over TLS: %v", err)
	}
	defer stop()

	uri := v.LoginURI()
	if !strings.HasPrefix(uri, "https://") {
		t.Errorf("LoginURI() = %q, want https:// so the viewer comes back over TLS", uri)
	}
	if !strings.HasPrefix(v.base+"/cap/example/seed", "https://") {
		t.Errorf("the seed capability is %q, want https://", v.base+"/cap/example/seed")
	}

	// And it really is TLS: a plaintext request is not answered.
	pemBytes, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("could not read the certificate back")
	}
	c := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
	resp, err := c.Get(uri)
	if err != nil {
		t.Fatalf("https to the endpoint: %v", err)
	}
	resp.Body.Close()
	// A GET is refused by the login handler, which is all that is
	// wanted here: the refusal came back over TLS.
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET returned %s, want the login handler's 405", resp.Status)
	}
	if resp.TLS == nil {
		t.Error("the answer did not come over TLS")
	}
}

// TestThereIsNoPlaintextEndpointLeft.
//
// What crosses this endpoint is the viewer_password digest inbound and
// the whole login response outbound -- secure_session_id, the session
// id, the circuit code -- followed by every capability URL the session
// holds.  There is no arrangement in which serving that in the clear is
// the right default, so serving it in the clear is not reachable: with
// no certificate the endpoint refuses to start rather than falling
// back.
func TestThereIsNoPlaintextEndpointLeft(t *testing.T) {
	v, _ := mintingHost(t, "$1$00157e577e57c0de028f000000000000")
	stop, err := v.serve("127.0.0.1:0", "", "")
	if err == nil {
		stop()
		t.Fatal("the endpoint served without a certificate")
	}
	if v.base != "" {
		t.Errorf("base = %q after a refused start, so something was bound", v.base)
	}
}

// TestABadCertificateIsRefusedBeforeTheListenerOpens: a daemon that
// took the flag, bound the port and only then found the certificate
// unreadable would log a line nobody is watching and serve nothing,
// which looks exactly like a viewer that cannot connect.
func TestABadCertificateIsRefusedBeforeTheListenerOpens(t *testing.T) {
	v, _ := mintingHost(t, "$1$00157e577e57c0de028f000000000000")
	stop, err := v.serve("127.0.0.1:0", filepath.Join(t.TempDir(), "absent.pem"),
		filepath.Join(t.TempDir(), "absent.key"))
	if err == nil {
		stop()
		t.Fatal("a missing certificate was accepted")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Errorf("error is %q, and does not say it is about the certificate", err)
	}
	if v.base != "" {
		t.Errorf("base = %q after a refused start, so something was bound", v.base)
	}
}

// Gating the capabilities on the login.
//
// The seed answers with every capability URL the session holds, each of
// which is a bearer credential good against the grid itself with slgod
// no longer in the way.  Before the token, the profile name was the
// whole of what those two paths asked for, and the profile name is in
// the public documentation.

// TestCapabilitiesAreRefusedWithoutTheLoginsToken: the path that used
// to work is the one that must not.
func TestCapabilitiesAreRefusedWithoutTheLoginsToken(t *testing.T) {
	_, vh, token, _ := servedSeed(t, capsServer(t, "here").URL)

	for _, path := range []string{
		"/cap/example/seed",             // the shape before the token
		"/cap/example/event",            //
		"/cap/example//seed",            // an empty token
		"/cap/example/" + token,         // the token, but nothing asked for
		"/cap/example/wrong/seed",       // a guess
		"/cap/other/" + token + "/seed", // the right token, another profile
	} {
		resp, err := http.Post(vh.base+path, "application/llsd+xml", nil)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s = %s, want 404", path, resp.Status)
		}
	}
}

// TestALaterLoginRetiresTheTokenBeforeIt: a viewer that has been
// displaced must not go on holding the session's capabilities.  One
// circuit and one peer per profile is already the rule, so a second
// live set of capability URLs would outlive what it belonged to.
func TestALaterLoginRetiresTheTokenBeforeIt(t *testing.T) {
	_, vh, first, _ := servedSeed(t, capsServer(t, "here").URL)

	second, err := vh.mintCapToken("example")
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("minting twice gave the same token")
	}
	if vh.capTokenOK("example", first) {
		t.Error("the token from the earlier login still works")
	}
	if !vh.capTokenOK("example", second) {
		t.Error("the token from the later login does not work")
	}
}

// TestATokenIsUnguessable: it stands in for a password, so it has to be
// the size of one.  Sixteen bytes is what Second Life's own capability
// URLs use for the same job.
func TestATokenIsUnguessable(t *testing.T) {
	v, _ := mintingHost(t, "$1$00157e577e57c0de028f000000000000")
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		tok, err := v.mintCapToken("example")
		if err != nil {
			t.Fatal(err)
		}
		if len(tok) != capTokenBytes*2 {
			t.Fatalf("token %q is %d characters, want %d", tok, len(tok), capTokenBytes*2)
		}
		if seen[tok] {
			t.Fatalf("token %q was minted twice", tok)
		}
		seen[tok] = true
	}
}

// TestNoTokenIsMintedWithoutALogin: a profile nothing has logged in to
// has no token, so every capability path for it is refused.  That
// covers the profile with no viewer_password too -- find() refuses to
// hand those over, so Admit never runs and nothing is ever minted.
func TestNoTokenIsMintedWithoutALogin(t *testing.T) {
	v, _ := mintingHost(t, "$1$00157e577e57c0de028f000000000000")
	if v.capTokenOK("example", "") {
		t.Error("an empty token was accepted")
	}
	if v.capTokenOK("example", "0c3e7e577e57c0de1b49b982fc5bae19") {
		t.Error("a made-up token was accepted for a profile that never logged in")
	}
}

// TestTheStandInForAMissingTokenIsNotAToken: a profile nobody has
// logged in to is compared against a stand-in rather than refused at
// once, so that the time taken does not say which profiles have a
// viewer.  The stand-in is in the source for anyone to read, so
// presenting it must be refused like any other guess.
func TestTheStandInForAMissingTokenIsNotAToken(t *testing.T) {
	v, _ := mintingHost(t, "$1$00157e577e57c0de028f000000000000")
	if len(noCapToken) != capTokenBytes*2 {
		t.Errorf("the stand-in is %d characters and a token %d; comparing against it costs less",
			len(noCapToken), capTokenBytes*2)
	}
	if v.capTokenOK("example", noCapToken) {
		t.Error("the stand-in for a missing token was accepted as a token")
	}
}

// Refusing a viewer login, end to end through the daemon's own lookup.
//
// viewer/login_test.go drives the handler with handovers composed by
// hand; this is the same question asked of find(), which is where a
// hosted session, a profile with no viewer_password and a name nobody
// holds are actually told apart.

// viewerLoginTo is a login endpoint in front of srv whose profiles have
// this viewer password, served in the clear because what is under test
// is what it answers and not how.
func viewerLoginTo(t *testing.T, ctx context.Context, srv *server.Server, viewerPassword string) (*viewerHost, string) {
	t.Helper()
	vh := newViewerHost(ctx, "127.0.0.1", srv,
		func(string) string { return viewerPassword }, nil, nil, func(string, ...any) {})
	mux := http.NewServeMux()
	mux.Handle("/", viewer.LoginHandler(vh.find, func(string, ...any) {}))
	mux.HandleFunc("/cap/", vh.serveCap)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	t.Cleanup(vh.closeAll)
	vh.base = ts.URL
	return vh, ts.URL
}

// viewerLogin posts a viewer's login and returns every byte of the
// answer, with the status and content type in front of it.
func viewerLogin(t *testing.T, url, first, last, passwd string) string {
	t.Helper()
	body := fmt.Sprintf(`<?xml version="1.0"?><methodCall><methodName>login_to_simulator</methodName>`+
		`<params><param><value><struct>`+
		`<member><name>first</name><value><string>%s</string></value></member>`+
		`<member><name>last</name><value><string>%s</string></value></member>`+
		`<member><name>passwd</name><value><string>%s</string></value></member>`+
		`</struct></value></param></params></methodCall>`, first, last, passwd)
	resp, err := http.Post(url, "text/xml", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%d %s\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), b)
}

// TestARefusedViewerLoginSaysNothingAndOpensNothing: the three ways a
// login is refused before its password has matched -- nobody by that
// name, a hosted avatar whose profile has no viewer_password, and a
// hosted avatar with a wrong password -- are one answer, to the byte.
// The last used to differ from the other two in the sentence a viewer
// shows, which told anybody who could reach the endpoint which avatars
// this daemon was holding and would hand over.
//
// And none of them leaves anything behind: no UDP socket, no
// capability token.  The socket used to be opened while the session
// was being looked up, before the password was compared.
//
// The last login is the right password, so that the test can be seen
// to notice a circuit when there is one.
func TestARefusedViewerLoginSaysNothingAndOpensNothing(t *testing.T) {
	sim := newSim(t)
	hs := loginServer(t, sim)

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

	handable, handableURL := viewerLoginTo(t, ctx, srv, "viewer-secret")
	unset, unsetURL := viewerLoginTo(t, ctx, srv, "")

	notHere := viewerLogin(t, handableURL, "Nobody", "Here", "viewer-secret")
	for _, c := range []struct{ name, got string }{
		{"a wrong password", viewerLogin(t, handableURL, "Example", "Resident", "wrong")},
		{"no password", viewerLogin(t, handableURL, "Example", "Resident", "")},
		{"a profile with no viewer_password", viewerLogin(t, unsetURL, "Example", "Resident", "viewer-secret")},
	} {
		if c.got != notHere {
			t.Errorf("%s is answered differently from a name nobody holds:\n%s\n-- against --\n%s",
				c.name, c.got, notHere)
		}
	}
	if !strings.Contains(notHere, "<string>false</string>") {
		t.Fatalf("the refusal is not a refusal:\n%s", notHere)
	}

	for _, vh := range []*viewerHost{handable, unset} {
		if _, ok := vh.circuits.Load("example"); ok {
			t.Error("a refused login opened the avatar's viewer circuit")
		}
		vh.tokenMu.Lock()
		tokens := len(vh.capTokens)
		vh.tokenMu.Unlock()
		if tokens != 0 {
			t.Errorf("a refused login minted %d capability token(s)", tokens)
		}
	}

	accepted := viewerLogin(t, handableURL, "Example", "Resident", "viewer-secret")
	if !strings.Contains(accepted, "<name>sim_port</name>") {
		t.Fatalf("the right password was refused:\n%s", accepted)
	}
	if _, ok := handable.circuits.Load("example"); !ok {
		t.Error("an accepted login opened no circuit, so the checks above prove nothing")
	}
}
