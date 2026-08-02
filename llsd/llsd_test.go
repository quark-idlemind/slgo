package llsd

import (
	"bytes"
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
