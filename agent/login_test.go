package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The fixture is a real login response from the live grid, with its
// UUIDs replaced and the openid token removed.  It is the reason this
// exists: 348 members, 262 <int> values and a premium_packages tree
// several levels deep, all of which the C client's parser died on.
func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/login_response.xml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeRealLoginResponse(t *testing.T) {
	v, err := decodeResponse(strings.NewReader(string(fixture(t))))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("top level is %T, want a struct", v)
	}

	if got := getString(m, "login"); got != "true" {
		t.Errorf("login = %q", got)
	}
	if _, ok := getInt(m, "circuit_code"); !ok {
		t.Error("circuit_code did not decode as a number")
	}
	// <int> is the type that killed the C parser.
	if v, ok := m["max-agent-groups"].(int64); !ok || v != 50 {
		t.Errorf("max-agent-groups = %v (%T), want int64 50", m["max-agent-groups"], m["max-agent-groups"])
	}
	// <double> was the other one.
	pp, ok := m["premium_packages"].(map[string]any)
	if !ok {
		t.Fatalf("premium_packages is %T", m["premium_packages"])
	}
	base, ok := pp["Base"].(map[string]any)
	if !ok {
		t.Fatalf("premium_packages.Base is %T", pp["Base"])
	}
	ben, ok := base["benefits"].(map[string]any)
	if !ok {
		t.Fatalf("benefits is %T", base["benefits"])
	}
	if v, ok := ben["lastname_change_cost"].(float64); !ok || v < 49 || v > 50 {
		t.Errorf("lastname_change_cost = %v (%T), want a float64 near 49.99",
			ben["lastname_change_cost"], ben["lastname_change_cost"])
	}
	// An array of structs.
	root, ok := m["inventory-root"].([]any)
	if !ok || len(root) != 1 {
		t.Fatalf("inventory-root is %T", m["inventory-root"])
	}
	if _, ok := root[0].(map[string]any)["folder_id"].(string); !ok {
		t.Error("inventory-root[0].folder_id missing")
	}
}

func TestAccountFromRealResponse(t *testing.T) {
	v, err := decodeResponse(strings.NewReader(string(fixture(t))))
	if err != nil {
		t.Fatal(err)
	}
	a, err := accountFrom(v.(map[string]any))
	if err != nil {
		t.Fatal(err)
	}

	if a.AgentID.IsZero() || a.SessionID.IsZero() || a.SecureSessionID.IsZero() {
		t.Errorf("identifiers not filled in: %+v", a)
	}
	if a.CircuitCode == 0 {
		t.Error("no circuit code")
	}
	if a.SimIP == nil || a.SimPort == 0 {
		t.Errorf("no simulator address: %v:%d", a.SimIP, a.SimPort)
	}
	if !strings.HasPrefix(a.SeedCapability, "https://") {
		t.Errorf("seed capability = %q", a.SeedCapability)
	}
	if a.InventoryRoot.IsZero() {
		t.Error("no inventory root")
	}
	// The live server wraps first_name in quotes.
	if strings.Contains(a.FirstName, `"`) || strings.Contains(a.LastName, `"`) {
		t.Errorf("name still quoted: %q %q", a.FirstName, a.LastName)
	}
	if a.Message == "" {
		t.Error("no message of the day")
	}
	// Raw holds the top level members; the nested trees hang off
	// them, as TestDecodeRealLoginResponse checks.
	if len(a.Raw) < 30 {
		t.Errorf("Raw kept only %d top level members", len(a.Raw))
	}
	// Things we do not model must still be reachable.
	for _, key := range []string{"agent_access_max", "premium_packages", "udp_blacklist"} {
		if _, ok := a.Raw[key]; !ok {
			t.Errorf("Raw is missing %s", key)
		}
	}
}

func TestDecodeUnknownValueTypeSurvives(t *testing.T) {
	// A type nobody has seen before must not be fatal.  This is the
	// exact failure that stopped the C client logging in.
	const body = `<?xml version="1.0"?><methodResponse><params><param><value><struct>
	  <member><name>login</name><value><string>true</string></value></member>
	  <member><name>future</name><value><wibble>42</wibble></value></member>
	  <member><name>after</name><value><int>7</int></value></member>
	</struct></value></param></params></methodResponse>`

	v, err := decodeResponse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("an unknown value type should not fail the decode: %v", err)
	}
	m := v.(map[string]any)
	if m["future"] != "42" {
		t.Errorf("unknown type kept as %v, want the text", m["future"])
	}
	// Everything after it must still be there.
	if m["after"] != int64(7) {
		t.Errorf("after = %v, parsing stopped early", m["after"])
	}
}

func TestDecodeFault(t *testing.T) {
	const body = `<?xml version="1.0"?><methodResponse><fault><value><struct>
	  <member><name>faultCode</name><value><int>4</int></value></member>
	  <member><name>faultString</name><value><string>bad things</string></value></member>
	</struct></value></fault></methodResponse>`

	_, err := decodeResponse(strings.NewReader(body))
	f, ok := err.(*Fault)
	if !ok {
		t.Fatalf("got %v (%T), want a *Fault", err, err)
	}
	if f.Code != 4 || f.Message != "bad things" {
		t.Errorf("fault = %+v", f)
	}
}

func TestDecodeUntypedValueIsString(t *testing.T) {
	const body = `<?xml version="1.0"?><methodResponse><params><param><value><struct>
	  <member><name>bare</name><value>hello</value></member>
	</struct></value></param></params></methodResponse>`
	v, err := decodeResponse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := v.(map[string]any)["bare"]; got != "hello" {
		t.Errorf("bare = %v", got)
	}
}

func TestHashPassword(t *testing.T) {
	// "$1$" + md5, and an existing digest passes through untouched.
	const digest = "$1$00157e577e57c0de028f000000000000"
	if got := hashPassword("example-password"); got != digest {
		t.Errorf("hashPassword = %q, want %q", got, digest)
	}
	if got := hashPassword(digest); got != digest {
		t.Errorf("an existing digest was hashed again: %q", got)
	}
	// Something that merely looks like one is hashed.
	if got := hashPassword("$1$notactuallyhexadecimal0000000000"); strings.HasSuffix(got, "0000") {
		t.Errorf("a non-digest was passed through: %q", got)
	}
}

func TestStartLocation(t *testing.T) {
	cases := map[string]string{
		"":                           "last",
		"last":                       "last",
		"home":                       "home",
		"the test region":            "uri:the test region&128&128&21",
		"the test region/188/203/28": "uri:the test region&188&203&28",
		"the test region/188":        "uri:the test region&188&128&21",
	}
	for in, want := range cases {
		if got := startLocation(in); got != want {
			t.Errorf("startLocation(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoginBodyIsWellFormedXML(t *testing.T) {
	l := Login{
		First:    "Example",
		Last:     "Resident",
		Password: "secret",
		// A region name with an ampersand, to make sure the
		// escaping is real.
		Start:   "Bits & Pieces/10/20/30",
		Options: []string{"login-flags"},
	}
	b, err := l.body()
	if err != nil {
		t.Fatal(err)
	}
	v, err := decodeMethodCallForTest(b)
	if err != nil {
		t.Fatalf("generated body does not parse: %v\n%s", err, b)
	}
	if v["first"] != "Example" || v["last"] != "Resident" {
		t.Errorf("names = %v", v)
	}
	if v["passwd"] != hashPassword("secret") {
		t.Errorf("passwd = %v", v["passwd"])
	}
	if v["start"] != "uri:Bits & Pieces&10&20&30" {
		t.Errorf("start = %q", v["start"])
	}
	if v["agree_to_tos"] != "true" {
		t.Errorf("agree_to_tos = %v", v["agree_to_tos"])
	}
}

// TestLoginBodyCarriesMachine: the login server wants to know which
// computer this is, and a missing id0 is not the same as a blank one.
func TestLoginBodyCarriesMachine(t *testing.T) {
	l := Login{
		First: "Example", Last: "Resident", Password: "secret",
		MAC: "02:1B:9C:4E:77:A3",
		ID0: "5e027e577e57c0deb52d5fd2dded42e5",
	}
	b, err := l.body()
	if err != nil {
		t.Fatal(err)
	}
	v, err := decodeMethodCallForTest(b)
	if err != nil {
		t.Fatalf("generated body does not parse: %v\n%s", err, b)
	}
	if v["mac"] != l.MAC {
		t.Errorf("mac = %v, want %q", v["mac"], l.MAC)
	}
	if v["id0"] != l.ID0 {
		t.Errorf("id0 = %v, want %q", v["id0"], l.ID0)
	}

	l.ID0 = ""
	b, err = l.body()
	if err != nil {
		t.Fatal(err)
	}
	if v, err = decodeMethodCallForTest(b); err != nil {
		t.Fatal(err)
	}
	if _, ok := v["id0"]; ok {
		t.Errorf("an unset id0 was sent anyway: %v", v["id0"])
	}
}

func TestLoginAgainstServer(t *testing.T) {
	body := fixture(t)
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/llsd+xml")
		w.Write(body)
	}))
	defer srv.Close()

	a, err := Login{
		First: "Example", Last: "Resident", Password: "secret",
		URL: srv.URL,
	}.Do(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a.CircuitCode == 0 || a.SimIP == nil {
		t.Errorf("account not filled in: %+v", a)
	}
	if !strings.Contains(string(gotBody), "login_to_simulator") {
		t.Errorf("request body was not a login_to_simulator call")
	}
}

func TestLoginRefused(t *testing.T) {
	const refused = `<?xml version="1.0"?><methodResponse><params><param><value><struct>
	  <member><name>login</name><value><string>false</string></value></member>
	  <member><name>reason</name><value><string>key</string></value></member>
	  <member><name>message</name><value><string>Could not authenticate.</string></value></member>
	</struct></value></param></params></methodResponse>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, refused)
	}))
	defer srv.Close()

	_, err := Login{First: "A", Last: "B", Password: "x", URL: srv.URL}.Do(context.Background())
	le, ok := err.(*LoginError)
	if !ok {
		t.Fatalf("got %v (%T), want *LoginError", err, err)
	}
	if le.Reason != "key" || !strings.Contains(le.Message, "authenticate") {
		t.Errorf("error = %+v", le)
	}
}

func TestLoginHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "go away", http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := Login{First: "A", Last: "B", Password: "x", URL: srv.URL}.Do(context.Background())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v, want the status", err)
	}
}

func TestLoginNeedsAName(t *testing.T) {
	if _, err := (Login{Password: "x"}).Do(context.Background()); err == nil {
		t.Error("expected an error without a name")
	}
}
