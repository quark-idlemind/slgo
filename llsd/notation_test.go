package llsd

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// TestNotationTypes reads every type the viewer's notation parser takes,
// each as the Go value the XML form gives.
func TestNotationTypes(t *testing.T) {
	t.Parallel()
	const id = "21217e57-7e57-c0de-347f-fdfaeee3f80a"
	for _, c := range []struct {
		name, in string
		want     any
	}{
		{"undef", `!`, nil},
		{"one", `1`, true},
		{"zero", `0`, false},
		{"t", `t`, true},
		{"T", `T`, true},
		{"f", `f`, false},
		{"F", `F`, false},
		{"true", `true`, true},
		{"TRUE", `TRUE`, true},
		{"True", `True`, true},
		{"false", `false`, false},
		{"FALSE", `FALSE`, false},
		{"integer", `i7357001`, int64(7357001)},
		{"negative integer", `i-12`, int64(-12)},
		{"real", `r0.25`, 0.25},
		{"negative real", `r-3`, float64(-3)},
		{"exponent real", `r1.5e2`, 150.0},
		{"uuid", `u` + id, id},
		{"double quoted", `"g'day"`, "g'day"},
		{"single quoted", `'have a "nice" day'`, `have a "nice" day`},
		{"escapes", `'a\tb\nc\\d\'e\x41f'`, "a\tb\nc\\d'eAf"},
		{"every escape", `"\a\b\f\n\r\t\v\q"`, "\a\b\f\n\r\t\vq"},
		{"empty string", `''`, ""},
		{"raw string", `s(6)"a"b'c "`, `a"b'c `},
		{"raw string, single quotes", `s(3)'abc'`, "abc"},
		{"empty raw string", `s(0)""`, ""},
		{"uri", `l"https://sim.example/x?a=b"`, "https://sim.example/x?a=b"},
		{"date", `d"2026-10-06T12:00:00.00Z"`, "2026-10-06T12:00:00.00Z"},
		{"hex binary", `b16"00ff3120AB"`, []byte{0x00, 0xff, 0x31, 0x20, 0xab}},
		{"empty hex binary", `b16""`, []byte{}},
		{"base64 binary", `b64"fiAQSQ=="`, []byte{0x7e, 0x20, 0x10, 0x49}},
		{"raw binary", `b(3)"a"b"`, []byte{'a', '"', 'b'}},
		{"empty array", `[]`, []any{}},
		{"empty map", `{}`, map[string]any{}},
		{"array", `[i1, r2, 'x' ,!,1]`, []any{int64(1), 2.0, "x", nil, true}},
		{"map", `{'a':i1,"b":[r1,r0],s(1)"c":{'d':!}}`, map[string]any{
			"a": int64(1), "b": []any{1.0, 0.0}, "c": map[string]any{"d": nil}}},
		{"whitespace", " \n\t{ 'a' : i1 , 'b' : [ 1 , 0 ] }\n", map[string]any{
			"a": int64(1), "b": []any{true, false}}},
	} {
		got, err := DecodeNotation([]byte(c.in))
		if err != nil {
			t.Errorf("%s: %q: %v", c.name, c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q read as %#v, want %#v", c.name, c.in, got, c.want)
		}
	}
}

// TestNotationReadsAMaterialOverride: the message the region was measured
// to send for a face, and the clear that follows it.
func TestNotationReadsAMaterialOverride(t *testing.T) {
	t.Parallel()
	v, err := DecodeNotation([]byte(`{'id':i7357001,'od':[{'bc':[r1,r0,r0,r0.5]},{'mf':r0.25,'rf':r0.75}],'te':[i2,i3]}`))
	if err != nil {
		t.Fatal(err)
	}
	m := Map(v)
	if Int(m, "id") != 7357001 {
		t.Errorf("id %v", m["id"])
	}
	od, _ := m["od"].([]any)
	if len(od) != 2 || !reflect.DeepEqual(Map(od[0])["bc"], []any{1.0, 0.0, 0.0, 0.5}) || Map(od[1])["mf"] != 0.25 {
		t.Errorf("od %#v", m["od"])
	}
	v, err = DecodeNotation([]byte(`{'id':i7357001,'od':[!,!],'te':[i2,i3]}`))
	if err != nil {
		t.Fatal(err)
	}
	od, _ = Map(v)["od"].([]any)
	if len(od) != 2 || od[0] != nil || od[1] != nil {
		t.Errorf("a clear read as %#v, want two undefs", od)
	}
}

// TestNotationLeavesWhatFollows: one value is read and the rest ignored,
// as the viewer's fromNotation does.
func TestNotationLeavesWhatFollows(t *testing.T) {
	t.Parallel()
	v, err := DecodeNotation([]byte(`  [i1] trailing`))
	if err != nil || !reflect.DeepEqual(v, []any{int64(1)}) {
		t.Errorf("read %#v, %v", v, err)
	}
}

// TestNotationRefusesWhatIsNotNotation: each of these fails, and none
// panics.
func TestNotationRefusesWhatIsNotNotation(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		``, `   `, `x`, `i`, `iabc`, `i99999999999`, `r`, `rx`, `u1234`, `u21217e57-7e57-c0de-347f-fdfaeee3f80`,
		`u2121zz57-7e57-c0de-347f-fdfaeee3f80a`, `tx`, `trux`, `fals`, `"open`, `'a\`, `'\x4`, `s(5)"abc"`, `s(3)abc`,
		`s3"abc"`, `s(3)"abc`, `l`, `l"open`, `d`, `b`, `b16"0"`, `b16"zz"`, `b16"00`, `b64"!!"`, `b(5)"ab"`, `b(2)"ab`,
		`b32"00"`, `{`, `{'a'`, `{'a':`, `{'a':i1`, `{a:i1}`, `{'a':x}`, `[`, `[i1`, `[x]`, `[i1 x]`,
	} {
		if v, err := DecodeNotation([]byte(in)); err == nil {
			t.Errorf("%q read as %#v, want an error", in, v)
		}
	}
}

// TestNotationDepthIsLimited: data from the network cannot nest a parser
// off the end of the stack.
func TestNotationDepthIsLimited(t *testing.T) {
	t.Parallel()
	ok := strings.Repeat("[", maxNotationDepth-1) + strings.Repeat("]", maxNotationDepth-1)
	if _, err := DecodeNotation([]byte(ok)); err != nil {
		t.Errorf("%d deep: %v", maxNotationDepth-1, err)
	}
	deep := bytes.Repeat([]byte("["), 100000)
	if _, err := DecodeNotation(deep); err == nil {
		t.Error("100000 deep read without error")
	}
}
