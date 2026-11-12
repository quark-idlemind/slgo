package llsd

import (
	"bytes"
	"reflect"
	"testing"
)

func TestBinaryRoundTrip(t *testing.T) {
	in := map[string]any{
		"b":    []byte{1, 2, 3},
		"i":    int64(-7),
		"r":    0.25,
		"s":    "text",
		"t":    true,
		"f":    false,
		"n":    nil,
		"u":    UUID("ab3f7e57-7e57-c0de-9d41-6c2e08f1b7a5"),
		"list": []any{int64(1), "two", []any{}},
		"map":  map[string]any{"inner": int64(2)},
	}
	b, err := EncodeBinary(in)
	if err != nil {
		t.Fatal(err)
	}
	out, n, err := DecodeBinary(append(b, "trailing"...))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(b) {
		t.Errorf("took %d bytes of %d", n, len(b))
	}
	if !reflect.DeepEqual(out, in) {
		t.Errorf("round trip:\n got %#v\nwant %#v", out, in)
	}
}

// The bytes LLSDBinaryFormatter writes for a small map, worked out by
// hand from the format: '{' count, 'k' length key, value, '}'.
func TestBinaryIsTheViewers(t *testing.T) {
	b, err := EncodeBinary(map[string]any{"size": 5, "offset": 0})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		'{', 0, 0, 0, 2,
		'k', 0, 0, 0, 6, 'o', 'f', 'f', 's', 'e', 't', 'i', 0, 0, 0, 0,
		'k', 0, 0, 0, 4, 's', 'i', 'z', 'e', 'i', 0, 0, 0, 5,
		'}',
	}
	if !bytes.Equal(b, want) {
		t.Errorf("got  % x\nwant % x", b, want)
	}
}

func TestBinaryRefusesTruncation(t *testing.T) {
	b, _ := EncodeBinary([]any{"abc", int64(1)})
	for n := range len(b) {
		if _, _, err := DecodeBinary(b[:n]); err == nil {
			t.Errorf("%d of %d bytes decoded", n, len(b))
		}
	}
}

func TestBinaryRefusesAHugeCount(t *testing.T) {
	if _, _, err := DecodeBinary([]byte{'[', 0x7f, 0xff, 0xff, 0xff}); err == nil {
		t.Error("an array claiming 2^31 elements in no bytes decoded")
	}
}
