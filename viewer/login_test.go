package viewer

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/internal/xmlrpc"
)

// storedResponse stands in for what a real login server said, including
// the sort of thing nothing in this tree models.
func storedResponse() map[string]any {
	return map[string]any{
		"login":            "true",
		"agent_id":         "876e7e57-7e57-c0de-9eeb-1bd0e1ec6995",
		"session_id":       "8d1b7e57-7e57-c0de-f4f4-19d29d124acf",
		"circuit_code":     int64(690139535),
		"sim_ip":           "203.0.113.14",
		"sim_port":         int64(13003),
		"seed_capability":  "https://simhost.invalid/cap/real",
		"first_name":       `"Taren"`,
		"last_name":        "Holt",
		"region_x":         int64(256000),
		"udp_blacklist":    "EnableSimulator,TeleportFinish",
		"agent_access_max": "A",
		"inventory-skeleton": []any{
			map[string]any{"name": "My Inventory", "folder_id": "f1"},
		},
		"global-textures": []any{
			map[string]any{"cloud_texture_id": "c1"},
		},
		"loginpage": nil,
	}
}

func testHandover() *Handover {
	return &Handover{
		First:   "Taren",
		Last:    "Holt",
		Digest:  agent.HashPassword("secret"),
		Raw:     storedResponse(),
		SimIP:   "127.0.0.1",
		SimPort: 9301,
		Seed:    "http://127.0.0.1:9302/cap/seed",
	}
}

func serve(t *testing.T, h *Handover) *httptest.Server {
	t.Helper()
	handler := LoginHandler(func(first, last string) *Handover {
		if h != nil && first == h.First && last == h.Last {
			return h
		}
		return nil
	}, func(string, ...any) {})
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}

func login(t *testing.T, url string, params map[string]any) map[string]any {
	t.Helper()
	var body strings.Builder
	body.WriteString(`<?xml version="1.0"?><methodCall><methodName>login_to_simulator</methodName><params><param>`)
	if err := encodeForTest(&body, params); err != nil {
		t.Fatal(err)
	}
	body.WriteString(`</param></params></methodCall>`)

	resp, err := http.Post(url, "text/xml", strings.NewReader(body.String()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	v, err := xmlrpc.DecodeResponse(resp.Body)
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("answer is %T, want a struct", v)
	}
	return m
}

// encodeForTest writes a value the way a viewer would.  It goes through
// the same encoder the handler answers with, which is fine here: what is
// under test is the handover, and the encoder has its own tests.
func encodeForTest(b *strings.Builder, v map[string]any) error {
	var tmp strings.Builder
	if err := xmlrpc.EncodeResponse(&tmp, v); err != nil {
		return err
	}
	s := tmp.String()
	i := strings.Index(s, "<param>")
	j := strings.LastIndex(s, "</param>")
	b.WriteString(s[i+len("<param>") : j])
	return nil
}

// TestHandoverReplaysTheStoredResponse is the whole design in one test.
// The viewer is told the session's real ids, and told that the simulator
// is slgod.
func TestHandoverReplaysTheStoredResponse(t *testing.T) {
	h := testHandover()
	s := serve(t, h)

	got := login(t, s.URL, map[string]any{
		"first": "Taren", "last": "Holt",
		"passwd": agent.HashPassword("secret"),
	})

	if got["login"] != "true" {
		t.Fatalf("login = %v, message %v", got["login"], got["message"])
	}
	// The session's own identity, unchanged: the simulator must not
	// notice anything new has arrived.
	for _, k := range []string{"agent_id", "session_id", "circuit_code"} {
		if got[k] != storedResponse()[k] {
			t.Errorf("%s = %v, want the session's own %v", k, got[k], storedResponse()[k])
		}
	}
	// And the three that are swapped.
	if got["sim_ip"] != "127.0.0.1" {
		t.Errorf("sim_ip = %v, want slgod", got["sim_ip"])
	}
	if got["sim_port"] != int64(9301) {
		t.Errorf("sim_port = %v, want slgod's", got["sim_port"])
	}
	if got["seed_capability"] != "http://127.0.0.1:9302/cap/seed" {
		t.Errorf("seed_capability = %v, want slgod's", got["seed_capability"])
	}
}

// TestHandoverKeepsWhatNothingModels: the response is replayed rather
// than composed precisely so that fields this tree has never heard of
// reach the viewer intact.
func TestHandoverKeepsWhatNothingModels(t *testing.T) {
	h := testHandover()
	s := serve(t, h)
	got := login(t, s.URL, map[string]any{
		"first": "Taren", "last": "Holt",
		"passwd": agent.HashPassword("secret"),
	})

	if got["udp_blacklist"] != "EnableSimulator,TeleportFinish" {
		t.Errorf("udp_blacklist did not survive: %v", got["udp_blacklist"])
	}
	if got["agent_access_max"] != "A" {
		t.Errorf("agent_access_max did not survive: %v", got["agent_access_max"])
	}
	// A quoted name is passed on exactly as the login server sent it,
	// quotes and all: unquoting is the reader's business and a viewer
	// expects what a login server sends.
	if got["first_name"] != `"Taren"` {
		t.Errorf("first_name = %v, want it verbatim", got["first_name"])
	}
	skel, ok := got["inventory-skeleton"].([]any)
	if !ok || len(skel) != 1 {
		t.Errorf("inventory-skeleton did not survive: %#v", got["inventory-skeleton"])
	}
	if v, ok := got["loginpage"]; !ok || v != nil {
		t.Errorf("loginpage = %#v, want it still absent rather than empty", v)
	}
}

// TestHandoverDoesNotDisturbTheStoredResponse: the viewer may be
// restarted, and the session is handed over again from the same map.
func TestHandoverDoesNotDisturbTheStoredResponse(t *testing.T) {
	h := testHandover()
	s := serve(t, h)
	for i := 0; i < 2; i++ {
		got := login(t, s.URL, map[string]any{
			"first": "Taren", "last": "Holt",
			"passwd": agent.HashPassword("secret"),
		})
		if got["login"] != "true" {
			t.Fatalf("handover %d refused: %v", i, got["message"])
		}
	}
	if h.Raw["sim_ip"] != "203.0.113.14" {
		t.Errorf("the stored response was overwritten: sim_ip = %v", h.Raw["sim_ip"])
	}
	if h.Raw["seed_capability"] != "https://simhost.invalid/cap/real" {
		t.Errorf("the stored response was overwritten: seed = %v", h.Raw["seed_capability"])
	}
}

// TestRefusalsLookLikeRefusals: a viewer shows the message from a
// login:false and shows nothing useful for an XML-RPC fault, so what
// someone sees when this goes wrong depends on getting this right.
func TestRefusalsLookLikeRefusals(t *testing.T) {
	h := testHandover()
	s := serve(t, h)

	for _, c := range []struct {
		name   string
		params map[string]any
	}{
		{"wrong password", map[string]any{
			"first": "Taren", "last": "Holt", "passwd": agent.HashPassword("wrong")}},
		{"no such session", map[string]any{
			"first": "Someone", "last": "Else", "passwd": agent.HashPassword("secret")}},
		{"no name at all", map[string]any{"passwd": "x"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := login(t, s.URL, c.params)
			if got["login"] != "false" {
				t.Errorf("login = %v, want a refusal", got["login"])
			}
			if got["reason"] == "" || got["message"] == "" {
				t.Errorf("refusal has nothing to show a person: %#v", got)
			}
		})
	}
}

// TestRefusalsDoNotSayWhichAvatarsAreHere: a wrong password and an
// unknown avatar answer the same, so the endpoint cannot be used to
// enumerate what slgod is holding.
func TestRefusalsDoNotSayWhichAvatarsAreHere(t *testing.T) {
	h := testHandover()
	s := serve(t, h)

	wrong := login(t, s.URL, map[string]any{
		"first": "Taren", "last": "Holt", "passwd": agent.HashPassword("wrong")})
	absent := login(t, s.URL, map[string]any{
		"first": "Someone", "last": "Else", "passwd": agent.HashPassword("secret")})

	if wrong["reason"] != absent["reason"] {
		t.Errorf("a wrong password says %q and an unknown avatar says %q",
			wrong["reason"], absent["reason"])
	}
}

// TestPlainTextPasswordIsAccepted: a viewer sends the digest, but
// anything else asking may not, and both have to compare the same way.
func TestPlainTextPasswordIsAccepted(t *testing.T) {
	h := testHandover()
	s := serve(t, h)
	got := login(t, s.URL, map[string]any{
		"first": "Taren", "last": "Holt", "passwd": "secret"})
	if got["login"] != "true" {
		t.Errorf("a plain text password was refused: %v", got["message"])
	}
}

// TestSessionWithNoStoredResponseIsRefused: half a response is worse
// than none, and a viewer given one fails somewhere unrelated.
func TestSessionWithNoStoredResponseIsRefused(t *testing.T) {
	h := testHandover()
	h.Raw = nil
	s := serve(t, h)
	got := login(t, s.URL, map[string]any{
		"first": "Taren", "last": "Holt", "passwd": "secret"})
	if got["login"] != "false" {
		t.Errorf("handed over a session with no stored response")
	}
}

func TestNonLoginMethodIsAFault(t *testing.T) {
	s := serve(t, testHandover())
	resp, err := http.Post(s.URL, "text/xml", strings.NewReader(
		`<?xml version="1.0"?><methodCall><methodName>something_else</methodName>`+
			`<params><param><value><struct/></value></param></params></methodCall>`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := xmlrpc.DecodeResponse(resp.Body); err == nil {
		t.Error("a method that is not a login should be a fault")
	}
}

func TestGetIsNotALogin(t *testing.T) {
	s := serve(t, testHandover())
	resp, err := http.Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET returned %s", resp.Status)
	}
}

// TestARealFirestormRequestIsAccepted feeds the handler the bytes a real
// viewer sent.  Everything else here is a request this tree composed,
// which would keep agreeing with itself if the reading were wrong.
func TestARealFirestormRequestIsAccepted(t *testing.T) {
	body, err := os.ReadFile("../agent/testdata/firestorm-login.xml")
	if err != nil {
		t.Skipf("no capture: %v", err)
	}

	// The capture is Flint Prober, with the digest of the throwaway
	// password it was made with.
	_, params, err := xmlrpc.DecodeCall(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	h := testHandover()
	h.First, h.Last = "Flint", "Prober"
	h.Digest = xmlrpc.String(params, "passwd")

	s := serve(t, h)
	resp, err := http.Post(s.URL, "text/xml", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	v, err := xmlrpc.DecodeResponse(resp.Body)
	if err != nil {
		t.Fatalf("a real viewer's request was answered with: %v", err)
	}
	got := v.(map[string]any)
	if got["login"] != "true" {
		t.Fatalf("a real viewer's login was refused: %v", got["message"])
	}
	if got["sim_port"] != int64(9301) {
		t.Errorf("sim_port = %v, want slgod's", got["sim_port"])
	}
}

func TestLoginURI(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"127.0.0.1:9000", "http://127.0.0.1:9000/"},
		{"http://127.0.0.1:9000", "http://127.0.0.1:9000/"},
		{"http://127.0.0.1:9000/", "http://127.0.0.1:9000/"},
	} {
		if got := LoginURI(c.in); got != c.want {
			t.Errorf("LoginURI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHostPort(t *testing.T) {
	for _, c := range []struct {
		in   string
		host string
		port int
		bad  bool
	}{
		{in: "127.0.0.1:9301", host: "127.0.0.1", port: 9301},
		{in: ":9301", host: "127.0.0.1", port: 9301},
		{in: "[::1]:9301", host: "::1", port: 9301},
		{in: "nonsense", bad: true},
		{in: "host:port", bad: true},
	} {
		host, port, err := HostPort(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("HostPort(%q) did not complain", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("HostPort(%q): %v", c.in, err)
			continue
		}
		if host != c.host || port != c.port {
			t.Errorf("HostPort(%q) = %q, %d; want %q, %d", c.in, host, port, c.host, c.port)
		}
	}
}

// ------------------------------------------------- the one-time password

// oneTimeHandover is a session with both credentials live: the
// profile's standing password and a minted one, with a counter for how
// often the minted one was spent.
func oneTimeHandover(minted string) (*Handover, *int) {
	spent := 0
	h := testHandover()
	h.OneTime = agent.HashPassword(minted)
	h.UseOnce = func() { spent++ }
	return h, &spent
}

// TestAMintedPasswordLogsInOnceAndIsSpent: this is the whole point of
// minting one.  A profile stores its viewer password as a digest, so
// nothing outside the daemon can produce a plaintext a viewer would
// hash into a match; the daemon makes one instead, and it has to stop
// working the moment it has been used, because by then it has crossed
// a command line where anything on this machine could read it.
func TestAMintedPasswordLogsInOnceAndIsSpent(t *testing.T) {
	h, spent := oneTimeHandover("one-time-pass")
	s := serve(t, h)

	got := login(t, s.URL, map[string]any{
		"first": "Taren", "last": "Holt", "passwd": "one-time-pass"})
	if got["login"] != "true" {
		t.Fatalf("a minted password was refused: %v", got["message"])
	}
	if *spent != 1 {
		t.Fatalf("the minted password was spent %d times, want once", *spent)
	}

	// The daemon drops it when it is spent, which is what the counter
	// stands for; here the handover is emptied by hand and the same
	// login tried again.
	h.OneTime, h.UseOnce = "", nil
	again := login(t, s.URL, map[string]any{
		"first": "Taren", "last": "Holt", "passwd": "one-time-pass"})
	if again["login"] != "false" {
		t.Error("a spent password logged in a second time")
	}
}

// TestTheProfilePasswordStillWorksBesideAMintedOne: the minted password
// is an addition and not a replacement.  Every handover that worked
// before this existed went through the profile's viewer_password, and
// somebody typing it into a viewer by hand must not find that a
// credential minted for somebody else has locked them out.
func TestTheProfilePasswordStillWorksBesideAMintedOne(t *testing.T) {
	h, spent := oneTimeHandover("one-time-pass")
	s := serve(t, h)

	got := login(t, s.URL, map[string]any{
		"first": "Taren", "last": "Holt", "passwd": "secret"})
	if got["login"] != "true" {
		t.Fatalf("the profile's own password was refused: %v", got["message"])
	}
	// And it does not spend the other one: the invitation somebody
	// else is holding is still theirs to use.
	if *spent != 0 {
		t.Errorf("logging in with the profile password spent the minted one %d times", *spent)
	}
}

// TestAWrongPasswordIsRefusedWithBothLive: with two accepted digests
// the refusal has to survive both comparisons, and neither may be
// spent by an attempt that matched nothing.
func TestAWrongPasswordIsRefusedWithBothLive(t *testing.T) {
	h, spent := oneTimeHandover("one-time-pass")
	s := serve(t, h)

	got := login(t, s.URL, map[string]any{
		"first": "Taren", "last": "Holt", "passwd": "not-either-of-them"})
	if got["login"] != "false" {
		t.Error("a password matching neither digest logged in")
	}
	if *spent != 0 {
		t.Errorf("a wrong password spent the minted one %d times", *spent)
	}
}

// TestNoMintedPasswordMeansNothingExtraIsAccepted: with no credential
// outstanding the empty OneTime is still compared against, so the
// comparison has to answer "no" for it rather than matching an empty
// or absent password.
func TestNoMintedPasswordMeansNothingExtraIsAccepted(t *testing.T) {
	h := testHandover() // OneTime empty, UseOnce nil
	s := serve(t, h)

	for _, passwd := range []string{"", "$1$", agent.HashPassword("")} {
		got := login(t, s.URL, map[string]any{
			"first": "Taren", "last": "Holt", "passwd": passwd})
		if got["login"] != "false" {
			t.Errorf("passwd %q logged in against a handover with no minted password", passwd)
		}
	}
}

// TestNothingIsOpenedUntilThePasswordMatches.
//
// The circuit used to be opened while the handover was being looked
// up, which is before the password has been compared: naming an avatar
// the daemon holds was enough to open its UDP socket, and the socket
// outlives the attempt by the life of the daemon.  A wrong password
// should cost the asker a refusal and nothing else.
func TestNothingIsOpenedUntilThePasswordMatches(t *testing.T) {
	admitted := 0
	h := testHandover()
	// Admit fills in what the response needs, as the daemon's does.
	h.SimIP, h.SimPort, h.Seed = "", 0, ""
	h.Admit = func() error {
		admitted++
		h.SimIP, h.SimPort, h.Seed = "127.0.0.1", 9301, "http://127.0.0.1:9302/cap/t/seed"
		return nil
	}
	s := serve(t, h)

	for _, params := range []map[string]any{
		{"first": "Taren", "last": "Holt", "passwd": agent.HashPassword("wrong")},
		{"first": "Someone", "last": "Else", "passwd": agent.HashPassword("secret")},
		{"first": "Taren", "last": "Holt", "passwd": ""},
	} {
		login(t, s.URL, params)
	}
	if admitted != 0 {
		t.Errorf("a refused login opened the circuit %d time(s)", admitted)
	}

	got := login(t, s.URL, map[string]any{
		"first": "Taren", "last": "Holt", "passwd": agent.HashPassword("secret")})
	if got["login"] != "true" {
		t.Fatalf("the right password was refused: %v", got["message"])
	}
	if admitted != 1 {
		t.Errorf("the accepted login admitted %d times, want 1", admitted)
	}
	// And what Admit settled is what the viewer was told.
	if got["sim_port"] != int64(9301) {
		t.Errorf("sim_port = %v, want the one Admit set", got["sim_port"])
	}
	if got["seed_capability"] != "http://127.0.0.1:9302/cap/t/seed" {
		t.Errorf("seed_capability = %v, want the one Admit set", got["seed_capability"])
	}
}

// TestAHandoverThatCannotBeAdmittedIsRefused: if the circuit will not
// open there is nothing to hand over, and a response naming a port
// nothing is listening on is worse than a refusal.
func TestAHandoverThatCannotBeAdmittedIsRefused(t *testing.T) {
	h := testHandover()
	h.Admit = func() error { return errors.New("no circuit") }
	s := serve(t, h)
	got := login(t, s.URL, map[string]any{
		"first": "Taren", "last": "Holt", "passwd": agent.HashPassword("secret")})
	if got["login"] == "true" {
		t.Error("a handover that could not be admitted was answered with a session")
	}
}
