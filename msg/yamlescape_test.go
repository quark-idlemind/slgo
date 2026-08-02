package msg

import "testing"

// The escaping rules, written out with raw string literals so the
// expected YAML is exactly what appears here.
//
// The C1 controls are the trap that TestDumpParsesAsYAML caught: Go is
// happy to call U+0080 through U+009F printable text, and YAML rejects
// them outright.

// TestDumpAwkwardBytesGoHex: a Variable field carrying bytes YAML will
// not take literally is not really text, so it renders as hex.
func TestDumpAwkwardBytesGoHex(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"plain text", []byte("hello"), `"hello"`},
		{"accented text", []byte("café"), `"café"`},
		{"trailing NUL dropped", []byte("hi\x00"), `"hi"`},
		{"newline stays text", []byte("a\nb"), `"a\nb"`},
		{"tab stays text", []byte("a\tb"), `"a\tb"`},

		{"C1 low", []byte("ab"), `"0x61c28062"`},
		{"C1 NEL", []byte("ab"), `"0x61c28562"`},
		{"C1 high", []byte("ab"), `"0x61c29f62"`},
		{"DEL", []byte("a\x7fb"), `"0x617f62"`},
		{"line separator", []byte("a b"), `"0x61e280a862"`},
		{"paragraph separator", []byte("a b"), `"0x61e280a962"`},
		{"NUL inside", []byte("a\x00b"), `"0x610062"`},
		{"invalid utf8", []byte{0xff, 0xfe}, `"0xfffe"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(appendBytesYAML(nil, c.in)); got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

// TestYAMLStringEscapes exercises the quoting directly.  It also
// carries error strings and peer addresses, which come from outside
// this package, so it cannot assume its input is tame.
func TestYAMLStringEscapes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", `"plain"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash`, `"back\\slash"`},
		{"a\nb", `"a\nb"`},
		{"a\rb", `"a\rb"`},
		{"a\tb", `"a\tb"`},
		{"a\x01b", `"a\x01b"`},
		{"a\x7fb", `"a\x7fb"`},
		{"ab", `"a\x80b"`},
		{"ab", `"a\x85b"`},
		{"ab", `"a\x9fb"`},
		{"a\u2028b", `"a\u2028b"`},
		{"a\u2029b", `"a\u2029b"`},
		{"café", `"café"`},
		{"日本語", `"日本語"`},
	}
	for _, c := range cases {
		if got := string(appendYAMLString(nil, c.in)); got != c.want {
			t.Errorf("%q -> %s, want %s", c.in, got, c.want)
		}
	}
}
