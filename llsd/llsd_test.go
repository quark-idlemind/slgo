package llsd

import (
	"bytes"
	"strings"
	"testing"
)

func TestDecodeTypes(t *testing.T) {
	const doc = `<llsd><map>
	  <key>s</key><string>hello</string>
	  <key>i</key><integer>-7</integer>
	  <key>r</key><real>1.5</real>
	  <key>b</key><boolean>1</boolean>
	  <key>u</key><uuid>876e7e57-7e57-c0de-9eeb-1bd0e1ec6995</uuid>
	  <key>n</key><undef/>
	  <key>empty</key><map/>
	  <key>list</key><array><integer>1</integer><integer>2</integer></array>
	  <key>bin</key><binary>fiAQSQ==</binary>
	</map></llsd>`

	v, err := Decode(bytes.NewReader([]byte(doc)))
	if err != nil {
		t.Fatal(err)
	}
	m := Map(v)
	if m == nil {
		t.Fatalf("top level is %T", v)
	}
	if m["s"] != "hello" || m["i"] != int64(-7) || m["r"] != 1.5 || m["b"] != true {
		t.Errorf("scalars = %v", m)
	}
	if m["n"] != nil {
		t.Errorf("undef = %v", m["n"])
	}
	if mm, ok := m["empty"].(map[string]any); !ok || len(mm) != 0 {
		t.Errorf("empty map = %T %v", m["empty"], m["empty"])
	}
	if l, ok := m["list"].([]any); !ok || len(l) != 2 || l[1] != int64(2) {
		t.Errorf("array = %v", m["list"])
	}
	if b, ok := m["bin"].([]byte); !ok || !bytes.Equal(b, []byte{126, 32, 16, 73}) {
		t.Errorf("binary = %v", m["bin"])
	}
}

// TestIntFromBinary is the accessor gap the event queue turned up: the
// simulator sends packed fields such as ParcelFlags as <binary>, and
// reading them as zero says "everything is off" rather than "I could
// not tell".
func TestIntFromBinary(t *testing.T) {
	m := map[string]any{
		"flags": []byte{126, 32, 16, 73}, // a real ParcelFlags word
		"one":   []byte{1},
		"short": []byte{0x01, 0x00},
		"odd":   []byte{1, 2, 3},
		"text":  "42",
		"num":   int64(9),
	}
	if got := Int(m, "flags"); got != 0x7E201049 {
		t.Errorf("flags = %#x, want 0x7e201049", got)
	}
	if got := Int(m, "one"); got != 1 {
		t.Errorf("one = %d", got)
	}
	if got := Int(m, "short"); got != 256 {
		t.Errorf("short = %d, want big endian 256", got)
	}
	if got := Int(m, "odd"); got != 0 {
		t.Errorf("a three byte integer should not be guessed at, got %d", got)
	}
	if Int(m, "text") != 42 || Int(m, "num") != 9 {
		t.Error("the other forms regressed")
	}
	if !Bool(m, "one") || Bool(m, "missing") {
		t.Error("Bool disagrees")
	}
	if !bytes.Equal(Bytes(m, "flags"), []byte{126, 32, 16, 73}) {
		t.Error("Bytes disagrees")
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	in := map[string]any{
		"a": "text & <markup>",
		"b": int64(42),
		"c": true,
		"d": []any{"x", int64(1)},
		"e": []byte{0, 1, 2, 255},
		"f": nil,
	}
	enc, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Decode(bytes.NewReader(enc))
	if err != nil {
		t.Fatalf("%v\n%s", err, enc)
	}
	out := Map(v)
	if out["a"] != in["a"] || out["b"] != in["b"] || out["c"] != in["c"] {
		t.Errorf("round trip = %v", out)
	}
	if b, ok := out["e"].([]byte); !ok || !bytes.Equal(b, []byte{0, 1, 2, 255}) {
		t.Errorf("binary round trip = %v", out["e"])
	}
	if _, ok := out["f"]; !ok {
		t.Error("undef was dropped")
	}
}

// TestDecodeUnknownType: a type this build has never seen must not fail
// the document, the same rule the XML-RPC decoder follows.
func TestDecodeUnknownType(t *testing.T) {
	const doc = `<llsd><map>
	  <key>known</key><integer>1</integer>
	  <key>future</key><wibble>42</wibble>
	  <key>after</key><string>still here</string>
	</map></llsd>`
	v, err := Decode(bytes.NewReader([]byte(doc)))
	if err != nil {
		t.Fatalf("an unknown type should not fail the decode: %v", err)
	}
	m := Map(v)
	if m["future"] != "42" {
		t.Errorf("future = %v, want its text", m["future"])
	}
	if m["after"] != "still here" {
		t.Error("parsing stopped at the unknown type")
	}
}

// TestDecodeSkipsThePrologue: a capability that answers with an XML
// declaration, a doctype or a comment ahead of the payload is answering
// correctly, and the scan for <llsd> has to walk past all of it.
func TestDecodeSkipsThePrologue(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<!-- from the seed capability -->
<llsd><string>ok</string></llsd>`

	v, err := Decode(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if v != "ok" {
		t.Errorf("value = %#v", v)
	}
}

// TestDecodeRefusesADocumentThatIsNotLLSD: the capability servers also
// answer with HTML error pages, and reading one as an empty document
// would turn "the service is down" into "the service said nothing".
func TestDecodeRefusesADocumentThatIsNotLLSD(t *testing.T) {
	cases := map[string]string{
		"an error page":       `<html><body>503</body></html>`,
		"nothing at all":      ``,
		"only a comment":      `<!-- silence -->`,
		"a truncated tag":     `<llsd`,
		"a truncated body":    `<llsd>`,
		"a truncated string":  `<llsd><string>abc`,
		"a truncated map":     `<llsd><map>`,
		"a truncated key":     `<llsd><map><key>a`,
		"a truncated value":   `<llsd><map><key>a</key><string>x`,
		"a truncated array":   `<llsd><array>`,
		"a truncated element": `<llsd><array><string>x`,
	}
	for what, doc := range cases {
		if v, err := Decode(strings.NewReader(doc)); err == nil {
			t.Errorf("%s decoded to %#v, expected a refusal", what, v)
		}
	}
}

// TestDecodeEmptyDocument: <llsd/> is what a capability sends when it
// has nothing to say, and it is a document rather than a failure.
func TestDecodeEmptyDocument(t *testing.T) {
	for _, doc := range []string{`<llsd></llsd>`, `<llsd/>`} {
		v, err := Decode(strings.NewReader(doc))
		if err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		if v != nil {
			t.Errorf("%s decoded to %#v, want nil", doc, v)
		}
	}
}

// TestDecodeMalformedScalars: a number the sender got wrong must come
// back as the text that was sent, not as a zero that reads like a
// measurement, and an empty element is the sender's zero.
func TestDecodeMalformedScalars(t *testing.T) {
	const doc = `<llsd><map>
	  <key>emptyint</key><integer/>
	  <key>badint</key><integer>seventeen</integer>
	  <key>emptyreal</key><real/>
	  <key>badreal</key><real>NaNsense</real>
	  <key>badbinary</key><binary>not base64!</binary>
	  <key>truefalse</key><boolean>true</boolean>
	  <key>zero</key><boolean>0</boolean>
	</map></llsd>`

	m := Map(mustDecode(t, doc))
	if m["emptyint"] != int64(0) {
		t.Errorf("an empty <integer> = %#v, want int64(0)", m["emptyint"])
	}
	if m["badint"] != "seventeen" {
		t.Errorf("a bad <integer> = %#v, want the text back", m["badint"])
	}
	if m["emptyreal"] != float64(0) {
		t.Errorf("an empty <real> = %#v, want float64(0)", m["emptyreal"])
	}
	if m["badreal"] != "NaNsense" {
		t.Errorf("a bad <real> = %#v, want the text back", m["badreal"])
	}
	if m["badbinary"] != "not base64!" {
		t.Errorf("bad <binary> = %#v, want the text back", m["badbinary"])
	}
	if m["truefalse"] != true || m["zero"] != false {
		t.Errorf("booleans = %#v %#v", m["truefalse"], m["zero"])
	}
}

// TestDecodeIgnoresMarkupInsideAValue: the inventory server has been
// seen wrapping text in presentation markup, and the value is the text,
// with the nested element consumed rather than left to be read as the
// next value of the enclosing map.
func TestDecodeIgnoresMarkupInsideAValue(t *testing.T) {
	const doc = `<llsd><map>
	  <key>note</key><string>before<b>bold</b>after</string>
	  <key>after</key><integer>3</integer>
	</map></llsd>`

	m := Map(mustDecode(t, doc))
	if m["note"] != "beforeafter" {
		t.Errorf("note = %#v, want the text with the markup dropped", m["note"])
	}
	if m["after"] != int64(3) {
		t.Errorf("the nested element swallowed what followed: %#v", m)
	}
}

// TestDecodeArrayOfMapsIsWhatTheEventQueueSends, which is the shape
// every poll comes back in: an array of event maps.
func TestDecodeArrayOfMapsIsWhatTheEventQueueSends(t *testing.T) {
	const doc = `<llsd><map>
	  <key>events</key>
	  <array>
	    <map><key>message</key><string>ParcelProperties</string></map>
	    <map><key>message</key><string>ChatterBoxInvitation</string></map>
	  </array>
	  <key>id</key><integer>17</integer>
	</map></llsd>`

	m := Map(mustDecode(t, doc))
	ev, ok := m["events"].([]any)
	if !ok || len(ev) != 2 {
		t.Fatalf("events = %#v", m["events"])
	}
	if String(Map(ev[1]), "message") != "ChatterBoxInvitation" {
		t.Errorf("second event = %#v", ev[1])
	}
	if Int(m, "id") != 17 {
		t.Errorf("id = %d", Int(m, "id"))
	}
}

// TestDecodeDropsAValueWithNoKey: a map whose value arrives before any
// <key> has nowhere to go, and putting it under the previous key would
// silently overwrite a field that was read correctly.
func TestDecodeDropsAValueWithNoKey(t *testing.T) {
	const doc = `<llsd><map>
	  <string>orphan</string>
	  <key>real</key><string>value</string>
	</map></llsd>`

	m := Map(mustDecode(t, doc))
	if len(m) != 1 || m["real"] != "value" {
		t.Errorf("map = %#v, want only the keyed value", m)
	}
}

func mustDecode(t *testing.T, doc string) any {
	t.Helper()
	v, err := Decode(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("%v\n%s", err, doc)
	}
	return v
}

// TestEncodeGolden writes the XML out by hand rather than reading back
// what Encode produced: a round trip through this package's own decoder
// would agree with any consistent mistake, and what has to be right is
// the text on the wire.
func TestEncodeGolden(t *testing.T) {
	cases := []struct {
		what string
		in   any
		want string
	}{
		{"undef", nil, `<llsd><undef/></llsd>`},
		{"a string", "hi", `<llsd><string>hi</string></llsd>`},
		{"markup in a string", `a & <b>`, `<llsd><string>a &amp; &lt;b&gt;</string></llsd>`},
		{"true", true, `<llsd><boolean>1</boolean></llsd>`},
		{"false", false, `<llsd><boolean>0</boolean></llsd>`},
		{"an int", 42, `<llsd><integer>42</integer></llsd>`},
		{"an int64", int64(-1), `<llsd><integer>-1</integer></llsd>`},
		{"a real", 1.5, `<llsd><real>1.5</real></llsd>`},
		{"binary", []byte{126, 32, 16, 73}, `<llsd><binary>fiAQSQ==</binary></llsd>`},
		{"an empty array", []any{}, `<llsd><array></array></llsd>`},
		{"strings", []string{"a", "b"},
			`<llsd><array><string>a</string><string>b</string></array></llsd>`},
		{"a mixed array", []any{int64(1), nil},
			`<llsd><array><integer>1</integer><undef/></array></llsd>`},
		// One key, because a map's iteration order is not fixed.
		{"a map", map[string]any{"k&": int64(1)},
			`<llsd><map><key>k&amp;</key><integer>1</integer></map></llsd>`},
	}
	for _, c := range cases {
		b, err := Encode(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.what, err)
			continue
		}
		if string(b) != c.want {
			t.Errorf("%s\n have %s\n want %s", c.what, b, c.want)
		}
	}
}

// TestEncodeRefusesWhatItCannotSpell, at the top level and nested: a
// value quietly dropped from a capability request comes back as the
// server rejecting the whole call, which is a long way from the bug.
func TestEncodeRefusesWhatItCannotSpell(t *testing.T) {
	type unknown struct{ X int }
	cases := map[string]any{
		"on its own":  unknown{},
		"in an array": []any{"fine", unknown{}},
		"in a map":    map[string]any{"k": unknown{}},
	}
	for what, v := range cases {
		if b, err := Encode(v); err == nil {
			t.Errorf("%s: encoded to %s, expected a refusal", what, b)
		}
	}
}

// TestAccessorsConvert: the capability servers are not consistent about
// which spelling a field arrives in -- the same flag comes back as a
// boolean from one and as "true" or 1 from another -- so the accessors
// take whichever turns up rather than making every caller check.
func TestAccessorsConvert(t *testing.T) {
	m := map[string]any{
		"str":   "text",
		"int":   int64(-3),
		"real":  2.5,
		"yes":   true,
		"no":    false,
		"num":   "  42  ",
		"words": "not a number",
		"bin":   []byte{0, 0},
		"one":   "1",
		"TRUE":  "TrUe",
	}

	// String converts, because a field that arrived as a number is
	// still the field that was asked for.
	for _, c := range []struct{ key, want string }{
		{"str", "text"},
		{"int", "-3"},
		{"real", "2.5"},
		{"yes", "true"},
		{"no", "false"},
		{"missing", ""},
		{"bin", ""}, // binary is not text; Bytes is the way to read it
	} {
		if got := String(m, c.key); got != c.want {
			t.Errorf("String(%q) = %q, want %q", c.key, got, c.want)
		}
	}

	for _, c := range []struct {
		key  string
		want int64
	}{
		{"int", -3},
		{"real", 2},
		{"yes", 1},
		{"no", 0},
		{"num", 42},
		{"words", 0},
		{"bin", 0},
		{"missing", 0},
	} {
		if got := Int(m, c.key); got != c.want {
			t.Errorf("Int(%q) = %d, want %d", c.key, got, c.want)
		}
	}

	for _, c := range []struct {
		key  string
		want bool
	}{
		{"yes", true},
		{"no", false},
		{"int", true},
		{"one", true},
		{"TRUE", true},
		{"words", false},
		{"bin", false},
		{"real", false}, // a float is not a form the servers send
		{"missing", false},
	} {
		if got := Bool(m, c.key); got != c.want {
			t.Errorf("Bool(%q) = %v, want %v", c.key, got, c.want)
		}
	}

	if Map("not a map") != nil {
		t.Error("Map of a non-map should be nil, so that a caller can test it")
	}
	if Bytes(m, "str") != nil {
		t.Error("Bytes of a string should be nil rather than its bytes")
	}
}
