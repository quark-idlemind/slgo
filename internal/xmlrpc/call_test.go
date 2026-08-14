package xmlrpc

import (
	"os"
	"strings"
	"testing"
)

// TestDecodeARealFirestormLogin reads the request Firestorm 7.2.4
// actually sent, captured off the wire.
//
// The option list is the reason this fixture is kept rather than a
// hand-written call.  slgod answers a viewer's login by replaying the
// response the real login server gave it, and a response can only hold
// what was asked for -- so what the viewer asks for decides what slgo's
// own login has to request.  A viewer asking for a block slgo never
// requested is a viewer that comes up missing something, and the list is
// long enough that guessing it would not have worked.
func TestDecodeARealFirestormLogin(t *testing.T) {
	f, err := os.Open("../../agent/testdata/firestorm-login.xml")
	if err != nil {
		t.Skipf("no capture: %v", err)
	}
	defer f.Close()

	method, params, err := DecodeCall(f)
	if err != nil {
		t.Fatalf("decoding a real login: %v", err)
	}
	if method != "login_to_simulator" {
		t.Errorf("method = %q", method)
	}

	if got := String(params, "first"); got != "Flint" {
		t.Errorf("first = %q", got)
	}
	if got := String(params, "last"); got != "Prober" {
		t.Errorf("last = %q", got)
	}
	// The password is the same $1$ digest slgo's own HashPassword
	// produces, which is what lets slgod authenticate a viewer against
	// a stored profile by comparing strings.
	if got := String(params, "passwd"); !strings.HasPrefix(got, "$1$") {
		t.Errorf("passwd = %q, want a $1$ digest", got)
	}
	// <int> members decode as int64 rather than as their digits.
	if got, ok := Int(params, "address_size"); !ok || got != 64 {
		t.Errorf("address_size = %d, %v", got, ok)
	}

	opts, ok := params["options"].([]any)
	if !ok {
		t.Fatalf("options is %T, want an array", params["options"])
	}
	if len(opts) != 27 {
		t.Errorf("this viewer asked for %d option blocks, want 27; if the "+
			"capture changed, what slgo requests has to change with it", len(opts))
	}
	// A few that slgo does not ask for today and would have to, since
	// what it does not request cannot be in the response it replays.
	for _, want := range []string{"inventory-skeleton", "gestures", "global-textures", "buddy-list"} {
		found := false
		for _, o := range opts {
			if s, _ := o.(string); s == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the viewer did not ask for %q; the fixture may be stale", want)
		}
	}
}

func TestDecodeCallRejectsRubbish(t *testing.T) {
	for _, c := range []struct{ name, in string }{
		{"empty", ""},
		{"no methodCall", `<?xml version="1.0"?><methodResponse/>`},
		{"no methodName", `<methodCall><params><param><value><struct/></value></param></params></methodCall>`},
		{"truncated", `<methodCall><methodName>x</methodName><params><param>`},
		{"parameter is not a struct", `<methodCall><methodName>x</methodName>` +
			`<params><param><value><string>no</string></value></param></params></methodCall>`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := DecodeCall(strings.NewReader(c.in)); err == nil {
				t.Errorf("decoded %q without complaint", c.in)
			}
		})
	}
}

// TestCallWithNoParamsIsNotAnError: a call carrying no params is well
// formed, and an empty map says so more usefully than a refusal.
func TestCallWithNoParamsIsNotAnError(t *testing.T) {
	method, params, err := DecodeCall(strings.NewReader(
		`<methodCall><methodName>ping</methodName></methodCall>`))
	if err != nil {
		t.Fatal(err)
	}
	if method != "ping" || len(params) != 0 {
		t.Errorf("method %q, params %v", method, params)
	}
}

// TestResponseRoundTrips is the property slgod's login handover rests
// on: the response the real login server gave is replayed to the viewer
// with three fields swapped, so anything the encoder cannot carry would
// be silently dropped from the viewer's idea of the session.
func TestResponseRoundTrips(t *testing.T) {
	in := map[string]any{
		"login":               "true",
		"agent_id":            "876e7e57-7e57-c0de-9eeb-1bd0e1ec6995",
		"sim_port":            int64(13003),
		"seconds_since_epoch": int64(1_755_000_000),
		"region_x":            int64(256000),
		"seed_capability":     "https://example.invalid/cap/abc",
		"loginpage":           nil,
		"gestures": []any{
			map[string]any{"asset_id": "a", "item_id": "b"},
		},
		"global-textures": []any{
			map[string]any{"cloud_texture_id": "c"},
		},
		"nested": map[string]any{"deep": []any{int64(1), "two", nil}},
	}

	var b strings.Builder
	if err := EncodeResponse(&b, in); err != nil {
		t.Fatal(err)
	}
	out, err := DecodeResponse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("re-reading what we wrote: %v\n%s", err, b.String())
	}
	got, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("decoded %T, want a struct", out)
	}

	if !sameValue(in, got) {
		t.Errorf("round trip changed the response:\nwant %#v\ngot  %#v\nwire %s", in, got, b.String())
	}
}

// TestEncodeIsStable: the same map has to produce the same bytes, or a
// response cannot be diffed against a capture.
func TestEncodeIsStable(t *testing.T) {
	m := map[string]any{"b": "2", "a": "1", "c": int64(3), "d": []any{"x", "y"}}
	var first, second strings.Builder
	if err := EncodeResponse(&first, m); err != nil {
		t.Fatal(err)
	}
	if err := EncodeResponse(&second, m); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Errorf("encoding is not stable:\n%s\n%s", first.String(), second.String())
	}
	// Sorted, so a is before b is before c.
	s := first.String()
	if strings.Index(s, ">a<") > strings.Index(s, ">b<") {
		t.Errorf("members are not sorted: %s", s)
	}
}

// TestEncodeEscapes: a name or a message with markup in it must not be
// able to change the shape of the document.
func TestEncodeEscapes(t *testing.T) {
	var b strings.Builder
	if err := EncodeResponse(&b, map[string]any{
		"message": `a <b> & "c"`,
		`we<ird`:  "k",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := DecodeResponse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("escaped document did not re-read: %v\n%s", err, b.String())
	}
	m := out.(map[string]any)
	if m["message"] != `a <b> & "c"` {
		t.Errorf("message came back as %q", m["message"])
	}
	if m[`we<ird`] != "k" {
		t.Errorf("key came back wrong: %#v", m)
	}
}

func TestEncodeRefusesWhatItCannotCarry(t *testing.T) {
	var b strings.Builder
	err := EncodeResponse(&b, map[string]any{"ch": make(chan int)})
	if err == nil {
		t.Error("encoded a channel without complaint")
	}
}

func TestEncodeFault(t *testing.T) {
	var b strings.Builder
	if err := EncodeFault(&b, 4, "too many logins"); err != nil {
		t.Fatal(err)
	}
	_, err := DecodeResponse(strings.NewReader(b.String()))
	var f *Fault
	if !asFault(err, &f) {
		t.Fatalf("decoding a fault gave %v (%T)", err, err)
	}
	if f.Code != 4 || f.Message != "too many logins" {
		t.Errorf("fault = %+v", f)
	}
}

func asFault(err error, out **Fault) bool {
	f, ok := err.(*Fault)
	if ok {
		*out = f
	}
	return ok
}

// sameValue compares decoded XML-RPC values structurally.
func sameValue(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !sameValue(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameValue(x[i], y[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}
