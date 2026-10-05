package llsdbin

import (
	"bytes"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

// uuidOf reads a signed id written with hyphens.
func uuidOf(t *testing.T, s string) UUID {
	t.Helper()
	raw, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(raw) != 16 {
		t.Fatalf("%q is not a uuid", s)
	}
	return UUID(raw)
}

const (
	idA = "01667e57-7e57-c0de-98d4-3e2b5ee32e62"
	idB = "28ea7e57-7e57-c0de-b3d7-94aac98e244a"
)

// TestDecodeGolden reads bytes built by hand from the format's
// description, one of each type, so that Encode and Decode cannot agree
// with each other and both be wrong.
func TestDecodeGolden(t *testing.T) {
	a := uuidOf(t, idA)
	var in []byte
	in = append(in, '{', 0, 0, 0, 9)
	add := func(key string, val ...byte) {
		in = append(in, 'k', 0, 0, 0, byte(len(key)))
		in = append(in, key...)
		in = append(in, val...)
	}
	add("a", '!')
	add("b", '1')
	add("c", '0')
	add("d", 'i', 0xff, 0xff, 0xff, 0xfe)                                 // -2
	add("e", 'r', 0x3f, 0xf8, 0, 0, 0, 0, 0, 0)                           // 1.5
	add("f", append([]byte{'u'}, a[:]...)...)                             // a uuid
	add("g", 's', 0, 0, 0, 2, 'h', 'i')                                   // a string
	add("h", '[', 0, 0, 0, 2, 'i', 0, 0, 0, 7, 'l', 0, 0, 0, 1, 'x', ']') // [7, uri x]
	add("i", 'b', 0, 0, 0, 3, 1, 2, 3)
	in = append(in, '}')

	got, err := Decode(in)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"a": nil, "b": true, "c": false, "d": int64(-2), "e": 1.5, "f": a, "g": "hi",
		"h": []any{int64(7), URI("x")}, "i": []byte{1, 2, 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}

	// A date is a real with its own letter.
	d, err := Decode([]byte{'d', 0x40, 0x59, 0, 0, 0, 0, 0, 0})
	if err != nil || d != Date(100) {
		t.Errorf("date: %v, %v", d, err)
	}
}

func TestEncodeGolden(t *testing.T) {
	got, err := Encode(map[string]any{"z": int64(1), "a": []any{true, nil}})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{'{', 0, 0, 0, 2,
		'k', 0, 0, 0, 1, 'a', '[', 0, 0, 0, 2, '1', '!', ']',
		'k', 0, 0, 0, 1, 'z', 'i', 0, 0, 0, 1,
		'}'}
	if !bytes.Equal(got, want) {
		t.Errorf("got % x\nwant % x", got, want)
	}
}

func TestRoundTrip(t *testing.T) {
	a, b := uuidOf(t, idA), uuidOf(t, idB)
	v := []any{
		map[string]any{"ID": []byte(a[:]), "Material": map[string]any{
			"DiffuseAlphaMode": int64(2), "AlphaMaskCutoff": int64(128),
			"SpecColor": []any{int64(255), int64(0), int64(0), int64(255)},
			"NormMap":   b, "Note": "a string", "Link": URI("http://example.invalid/"),
			"When": Date(1.7e9), "Scale": 0.25, "On": true, "Off": false, "Nothing": nil,
		}},
		[]any{},
		map[string]any{},
		"",
	}
	raw, err := Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, any(v)) {
		t.Errorf("got %#v\nwant %#v", got, v)
	}

	// And through zlib.
	z, err := EncodeZipped(v)
	if err != nil {
		t.Fatal(err)
	}
	got, err = DecodeZipped(z)
	if err != nil || !reflect.DeepEqual(got, any(v)) {
		t.Errorf("zipped: %v, %#v", err, got)
	}
}

func TestEncodeNarrowTypes(t *testing.T) {
	raw, err := Encode([]any{3, int32(4), uint8(5), float32(0.5)})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Decode(raw)
	if !reflect.DeepEqual(got, []any{int64(3), int64(4), int64(5), 0.5}) {
		t.Errorf("got %#v", got)
	}
	if _, err := Encode(int64(1) << 40); err == nil {
		t.Error("an integer that does not fit 32 bits was written")
	}
	if _, err := Encode(struct{}{}); err == nil {
		t.Error("a struct was written")
	}
}

func TestDecodeRefuses(t *testing.T) {
	deep := bytes.Repeat([]byte{'[', 0, 0, 0, 1}, maxDepth+2)
	for name, in := range map[string][]byte{
		"empty":           {},
		"unknown type":    {'?'},
		"short integer":   {'i', 0, 0},
		"short string":    {'s', 0, 0, 0, 5, 'a'},
		"negative length": {'s', 0xff, 0xff, 0xff, 0xff},
		"count past data": {'[', 0x7f, 0xff, 0xff, 0xff, ']'},
		"unclosed array":  {'[', 0, 0, 0, 1, '1'},
		"unclosed map":    {'{', 0, 0, 0, 0},
		"map without key": {'{', 0, 0, 0, 1, 's', 0, 0, 0, 0, '1', '}'},
		"trailing bytes":  {'1', '1'},
		"nested too deep": deep,
		"short uuid":      {'u', 1, 2, 3},
		"short real":      {'r', 1, 2, 3},
	} {
		if v, err := Decode(in); err == nil {
			t.Errorf("%s: read %#v", name, v)
		}
	}
}

func TestUnzipRefusesWhatIsNotZlib(t *testing.T) {
	if _, err := Unzip([]byte("not zlib at all")); err == nil {
		t.Error("read text as zlib")
	}
	z, _ := Zip([]byte("abc"))
	if _, err := Unzip(z[:len(z)-3]); err == nil {
		t.Error("read a cut zlib stream")
	}
}
