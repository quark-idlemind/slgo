package xmlrpc

// The login response is XML-RPC, and the whole point of this decoder is
// that it is hard to upset.
//
// The C client's parser called err(1, "Unexpected value type got %s") on
// anything it did not recognise, so the day Linden Lab added an <int>
// field to the login response the program stopped starting.  A login
// server is not something this side gets to negotiate with: it will grow
// fields, and it has sent numbers as strings, and it will one day send a
// type nobody here has heard of.  None of that is a reason to refuse to
// log in.
//
// So these tests are mostly about damage.  Every early return in the
// decoder is a document that stops in the middle of something, and what
// matters is that it comes back as an error rather than as a hang, a
// panic, or -- worst -- an Account with half its fields quietly zero.

import (
	"errors"
	"strings"
	"testing"
)

// respond wraps a <value> body in the methodResponse the login server
// sends, so a case can be written as just the value.
func respond(value string) string {
	return `<?xml version="1.0"?><methodResponse><params><param><value>` +
		value + `</value></param></params></methodResponse>`
}

// TestEveryTypeBecomesAPlainGoValue: the mapping is the contract the
// rest of the package reads the login response through, and an <i4> that
// came back as a string would fail much later and somewhere else.
func TestEveryTypeBecomesAPlainGoValue(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name  string
		value string
		want  any
	}{
		{"an int", `<int>42</int>`, int64(42)},
		{"an i4", `<i4>-1</i4>`, int64(-1)},
		{"an i8", `<i8>9000000000</i8>`, int64(9000000000)},
		{"a double", `<double>1.5</double>`, 1.5},
		{"a string", `<string>hello</string>`, "hello"},
		// A <value> with no type element is a string, per the
		// specification, and the login server relies on it.
		{"no type at all", `just text`, "just text"},
		{"a boolean 1", `<boolean>1</boolean>`, true},
		{"a boolean true", `<boolean>TRUE</boolean>`, true},
		{"a boolean 0", `<boolean>0</boolean>`, false},
		// Types nobody here models keep their text rather than being a
		// reason to stop.
		{"a date", `<dateTime.iso8601>20260804T00:00:00</dateTime.iso8601>`, "20260804T00:00:00"},
		{"base64", `<base64>eA==</base64>`, "eA=="},
		{"something invented tomorrow", `<biginteger>1234</biginteger>`, "1234"},
		// A malformed number keeps its text too: one bad field is not
		// worth failing the whole login over.
		{"an int that is not a number", `<int>twelve</int>`, "twelve"},
		{"a double that is not a number", `<double>about three</double>`, "about three"},
		// Character data inside a nested element belongs to the nested
		// element, and stepping over it must not eat what follows.
		{"text with markup in it", `<string>a<b>c</b>d</string>`, "ad"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := DecodeResponse(strings.NewReader(respond(c.value)))
			if err != nil {
				t.Fatalf("%s: %v", c.value, err)
			}
			if got != c.want {
				t.Errorf("got %#v (%T), want %#v", got, got, c.want)
			}
		})
	}
}

// TestArraysAndStructsNest: the buddy list is an array of structs and
// inventory-root is an array of one, so both forms have to survive being
// inside each other.
func TestArraysAndStructsNest(t *testing.T) {
	t.Parallel()

	got, err := DecodeResponse(strings.NewReader(respond(
		`<struct>
			<member><name>numbers</name><value><array><data>
				<value><int>1</int></value>
				<value>two</value>
			</data></array></value></member>
			<member><name>empty</name><value><array><data></data></array></value></member>
			<member><name>nested</name><value><struct>
				<member><name>deep</name><value><boolean>1</boolean></value></member>
			</struct></value></member>
		</struct>`)))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("got %T, wanted a struct", got)
	}
	nums, ok := m["numbers"].([]any)
	if !ok || len(nums) != 2 || nums[0] != int64(1) || nums[1] != "two" {
		t.Errorf("numbers = %#v", m["numbers"])
	}
	// An empty array is an empty array, not a missing one: the two mean
	// different things in a login response.
	if e, ok := m["empty"].([]any); !ok || e == nil || len(e) != 0 {
		t.Errorf("empty = %#v", m["empty"])
	}
	inner, ok := m["nested"].(map[string]any)
	if !ok || inner["deep"] != true {
		t.Errorf("nested = %#v", m["nested"])
	}
}

// TestElementsNobodyModelsAreSteppedOver: a login server that adds
// something between the elements this knows about must not shift
// everything after it.
func TestElementsNobodyModelsAreSteppedOver(t *testing.T) {
	t.Parallel()

	got, err := DecodeResponse(strings.NewReader(
		`<methodResponse>
			<somethingNew><with>contents</with></somethingNew>
			<params>
				<somethingElse/>
				<param><notAValue>x</notAValue><value><struct>
					<notAMember>x</notAMember>
					<member><name>a</name><value><array>
						<notData/><data><value><int>1</int></value></data>
					</array></value></member>
					<member><notANameOrValue>x</notANameOrValue><name>b</name><value>two</value></member>
				</struct></value></param>
				<param><value><int>99</int></value></param>
			</params>
		</methodResponse>`))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("got %T (%#v), wanted the first param's struct", got, got)
	}
	if a, ok := m["a"].([]any); !ok || len(a) != 1 || a[0] != int64(1) {
		t.Errorf("a = %#v", m["a"])
	}
	if m["b"] != "two" {
		t.Errorf("b = %#v", m["b"])
	}
}

// TestADocumentThatStopsInTheMiddleIsAnError: every one of these is a
// truncation at a different depth of the decoder, which is where a
// parser that assumed well formed input either hangs or hands back
// something half filled in.
func TestADocumentThatStopsInTheMiddleIsAnError(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		xml  string
		says string
	}{
		{"nothing at all", ``, "no methodResponse"},
		{"a document that is not one", `<hello/>`, "expected methodResponse"},
		{"an unclosed root", `<methodResponse`, "syntax"},
		{"a response with neither params nor fault", `<methodResponse></methodResponse>`,
			"neither params nor fault"},
		{"params holding no param",
			`<methodResponse><params></params></methodResponse>`, "held no param"},

		{"cut off inside methodResponse", `<methodResponse>`, "EOF"},
		{"cut off inside an element nobody models",
			`<methodResponse><novel><deeper>`, "EOF"},
		{"cut off inside params", `<methodResponse><params>`, "EOF"},
		{"cut off inside something skipped in params",
			`<methodResponse><params><novel><deeper>`, "EOF"},
		{"cut off inside param", `<methodResponse><params><param>`, "EOF"},
		{"cut off inside something skipped in param",
			`<methodResponse><params><param><novel><deeper>`, "EOF"},
		{"cut off inside value",
			`<methodResponse><params><param><value>`, "EOF"},
		{"cut off inside a typed value",
			`<methodResponse><params><param><value><int>`, "EOF"},
		{"cut off inside an array",
			`<methodResponse><params><param><value><array>`, "EOF"},
		{"cut off inside an array's value",
			`<methodResponse><params><param><value><array><data><value>`, "EOF"},
		{"cut off inside something skipped in an array",
			`<methodResponse><params><param><value><array><novel><deeper>`, "EOF"},
		{"cut off inside a struct",
			`<methodResponse><params><param><value><struct>`, "EOF"},
		{"cut off inside something skipped in a struct",
			`<methodResponse><params><param><value><struct><novel><deeper>`, "EOF"},
		{"cut off inside a member",
			`<methodResponse><params><param><value><struct><member>`, "EOF"},
		{"cut off inside a member's name",
			`<methodResponse><params><param><value><struct><member><name>`, "EOF"},
		{"cut off inside a member's value",
			`<methodResponse><params><param><value><struct><member><value>`, "EOF"},
		{"cut off inside something skipped in a member",
			`<methodResponse><params><param><value><struct><member><novel><deeper>`, "EOF"},
		{"cut off inside a fault", `<methodResponse><fault>`, "EOF"},
		{"cut off inside a fault's value", `<methodResponse><fault><value>`, "EOF"},
		{"cut off inside something skipped in a fault",
			`<methodResponse><fault><novel><deeper>`, "EOF"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			v, err := DecodeResponse(strings.NewReader(c.xml))
			if err == nil {
				t.Fatalf("decoded %q as %#v; a truncated document is not a response", c.xml, v)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("err = %v, should mention %q", err, c.says)
			}
		})
	}
}

// TestAFaultCarriesItsCodeAndSentence: a refusal comes back as a fault
// rather than as a response, and the code is the part a program can act
// on.
func TestAFaultCarriesItsCodeAndSentence(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		xml  string
		want Fault
	}{
		{
			name: "a fault as the specification writes one",
			xml: `<methodResponse><fault><value><struct>
				<member><name>faultCode</name><value><int>4</int></value></member>
				<member><name>faultString</name><value><string>too many logins</string></value></member>
			</struct></value></fault></methodResponse>`,
			want: Fault{Code: 4, Message: "too many logins"},
		},
		{
			// Something in the right place that is not a struct still
			// has to become a fault, since the alternative is reporting
			// success.
			name: "a fault that is not a struct",
			xml:  `<methodResponse><fault><value><string>go away</string></value></fault></methodResponse>`,
			want: Fault{Message: "unknown"},
		},
		{
			name: "a fault with nothing in it",
			xml:  `<methodResponse><fault></fault></methodResponse>`,
			want: Fault{Message: "empty fault"},
		},
		{
			name: "a fault behind an element nobody models",
			xml: `<methodResponse><fault><novel>x</novel><value><struct>
				<member><name>faultCode</name><value><int>7</int></value></member>
			</struct></value></fault></methodResponse>`,
			want: Fault{Code: 7, Message: "unknown"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeResponse(strings.NewReader(c.xml))
			var f *Fault
			if !errors.As(err, &f) {
				t.Fatalf("err = %v (%T), wanted a *Fault", err, err)
			}
			if f.Code != c.want.Code || f.Message != c.want.Message {
				t.Errorf("fault = %+v, want %+v", *f, c.want)
			}
			// The message a person sees names both parts.
			if !strings.Contains(f.Error(), c.want.Message) {
				t.Errorf("Error() = %q, should carry the message", f.Error())
			}
		})
	}
}

// TestFieldsAreReadInWhateverFormTheyArrived: the login server has sent
// the same field as an <int> and as a <string> at different times, and
// asking for the wrong one reads a zero rather than failing, which is
// the quietest way to lose a circuit code.
func TestFieldsAreReadInWhateverFormTheyArrived(t *testing.T) {
	t.Parallel()

	m := map[string]any{
		"text":     "a string",
		"number":   int64(42),
		"fraction": 1.5,
		"yes":      true,
		"no":       false,
		"digits":   " 690139535 ",
		"words":    "not a number",
		"array":    []any{},
	}

	for _, c := range []struct {
		key  string
		want string
	}{
		{"text", "a string"},
		{"number", "42"},
		{"fraction", "1.5"},
		{"yes", "true"},
		{"no", "false"},
		{"array", ""},
		{"missing", ""},
	} {
		if got := String(m, c.key); got != c.want {
			t.Errorf("String(%q) = %q, want %q", c.key, got, c.want)
		}
	}

	for _, c := range []struct {
		key  string
		want int64
		ok   bool
	}{
		{"number", 42, true},
		{"fraction", 1, true},
		// Whitespace around a number is the login server's, not ours.
		{"digits", 690139535, true},
		{"words", 0, false},
		{"yes", 0, false},
		{"missing", 0, false},
	} {
		got, ok := Int(m, c.key)
		if got != c.want || ok != c.ok {
			t.Errorf("Int(%q) = %d, %v; want %d, %v", c.key, got, ok, c.want, c.ok)
		}
	}
}
