package slate

import (
	"bytes"
	"math"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

const testNonce = "0123456789abcdef"

var (
	primA  = msg.MustParseUUID("23127e57-7e57-c0de-d8f8-18e321e83064")
	primB  = msg.MustParseUUID("41227e57-7e57-c0de-7c4c-ab3b65bc8ddd")
	bridge = msg.MustParseUUID("d4437e57-7e57-c0de-9e76-d632d6b8818e")
	other  = msg.MustParseUUID("f81b7e57-7e57-c0de-1a3a-d7caff589862")
)

func testState() *runState {
	return &runState{Nonce: testNonce, Probes: map[msg.UUID]bool{primA: true, primB: true}, Bridge: bridge}
}

func TestQuoteFixtures(t *testing.T) {
	for _, c := range []struct{ raw, quoted string }{
		{``, `""`},
		{`ready`, `"ready"`},
		{`Example "East`, `"Example \"East"`},
		{`say "hi"`, `"say \"hi\""`},
		{"a\tb\nc", `"a\tb\nc"`},
		{`back\slash`, `"back\\slash"`},
		{`\n`, `"\\n"`},
	} {
		if got := quote(c.raw); got != c.quoted {
			t.Errorf("quote(%q) = %s, want %s", c.raw, got, c.quoted)
		}
		got, err := unquote(c.quoted)
		if err != nil || got != c.raw {
			t.Errorf("unquote(%s) = %q, %v, want %q", c.quoted, got, err, c.raw)
		}
	}
}

func TestUnquoteRejects(t *testing.T) {
	for _, bad := range []string{``, `"`, `abc`, `"abc`, `abc"`, `"abc\"`, `"\"`, `"a\x"`, `"a\r"`, `"\`} {
		if got, err := unquote(bad); err == nil {
			t.Errorf("unquote(%s) = %q, want an error", bad, got)
		}
	}
}

func TestSendAndAckLines(t *testing.T) {
	got, err := sendLine(testNonce, primA, -7702101, 2, -4, 7, msg.UUID{}, `ready "q" tab`+"\there")
	want := `slprobe/2 0123456789abcdef relay 23127e57-7e57-c0de-d8f8-18e321e83064 -7702101 ` +
		`slprobe/2 0123456789abcdef send 2 -4 7 00000000-0000-0000-0000-000000000000 "ready \"q\" tab\there"`
	if err != nil || got != want {
		t.Fatalf("got %s, %v", got, err)
	}
	got, err = ackLine(testNonce, primB, -5, 3)
	want = `slprobe/2 0123456789abcdef relay 41227e57-7e57-c0de-7c4c-ab3b65bc8ddd -5 slprobe/2 0123456789abcdef ack 3`
	if err != nil || got != want {
		t.Fatalf("got %s, %v", got, err)
	}
	// A key is canonical lowercase.
	got, _ = sendLine(testNonce, primA, -5, 1, 1, 0, other, "x")
	if !strings.Contains(got, " send 1 1 0 f81b7e57-7e57-c0de-1a3a-d7caff589862 ") {
		t.Fatalf("got %s", got)
	}
}

func TestSendLineLimit(t *testing.T) {
	base, err := sendLine(testNonce, primA, math.MinInt32, 1, -2, 7, msg.UUID{}, "")
	if err != nil {
		t.Fatal(err)
	}
	room := maxSay - len(base)
	at, err := sendLine(testNonce, primA, math.MinInt32, 1, -2, 7, msg.UUID{}, strings.Repeat("a", room))
	if err != nil || len(at) != maxSay {
		t.Fatalf("at the limit: %d bytes, %v", len(at), err)
	}
	if _, err := sendLine(testNonce, primA, math.MinInt32, 1, -2, 7, msg.UUID{}, strings.Repeat("a", room+1)); err == nil {
		t.Fatal("one byte over was accepted")
	}
	// An escape is two bytes on the wire.
	if _, err := sendLine(testNonce, primA, math.MinInt32, 1, -2, 7, msg.UUID{}, strings.Repeat(`"`, room/2+1)); err == nil {
		t.Fatal("escapes were not counted")
	}
}

func TestStaticCheckUsesTheCodec(t *testing.T) {
	// The static check is the codec with the widest values.
	want, _ := sendLine(nonceHex, msg.UUID{}, math.MinInt32, 1, -2, 7, msg.UUID{}, "ready")
	if got := relayLine(1, -2, 7, nullKey, "ready"); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestClassifyProbe(t *testing.T) {
	r := testState()
	p := "slprobe/2 " + testNonce + " "
	for _, c := range []struct {
		line string
		want wireMessage
	}{
		{"hello 3 " + primA.String(), wireMessage{Kind: wireHello, Link: 3, Prim: primA}},
		{`link 1 2 -5 ` + other.String() + ` "say \"hi\"\tx"`, wireMessage{Kind: wireLink, HeardBy: 1, Sender: 2, Num: -5, Key: other, Text: "say \"hi\"\tx"}},
		{`link 1 2 0 00000000-0000-0000-0000-000000000000 ""`, wireMessage{Kind: wireLink, HeardBy: 1, Sender: 2}},
		{"overflow 1 2 3 " + other.String() + " 1500", wireMessage{Kind: wireOverflow, HeardBy: 1, Sender: 2, Num: 3, Key: other, Length: 1500}},
		{"bad", wireMessage{Kind: wireBad}},
	} {
		got, err := r.Classify(sl.ChatDirect, primB, p+c.line)
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v, %v, want %+v", c.line, got, err, c.want)
		}
	}
}

func TestClassifyBridge(t *testing.T) {
	r := testState()
	p := "slprobe/2 " + testNonce + " "
	for _, c := range []struct {
		line string
		want wireMessage
	}{
		{"ready", wireMessage{Kind: wireReady}},
		{`fwd 1 00000000-0000-0000-0000-000000000000 "Example \"East" say "hi"`,
			wireMessage{Kind: wireFwd, Channel: 1, Name: `Example "East`, Tail: `say "hi"`}},
		{`fwd -5 ` + other.String() + ` "Box" a \"b\" \n`, wireMessage{Kind: wireFwd, Channel: -5, Speaker: other, Name: "Box", Tail: `a \"b\" \n`}},
		{`fwd 9 ` + other.String() + ` "" `, wireMessage{Kind: wireFwd, Channel: 9, Speaker: other}},
		{`fwd 9 ` + other.String() + ` "Name\\" tail`, wireMessage{Kind: wireFwd, Channel: 9, Speaker: other, Name: `Name\`, Tail: "tail"}},
		{"fwd-overflow 7 " + other.String() + " 2000", wireMessage{Kind: wireFwdOverflow, Channel: 7, Speaker: other, Length: 2000}},
	} {
		got, err := r.Classify(sl.ChatOwner, bridge, p+c.line)
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v, %v, want %+v", c.line, got, err, c.want)
		}
	}
	// The tail is the product's, not unescaped, and may look like protocol.
	got, _ := r.Classify(sl.ChatOwner, bridge, p+`fwd 1 `+other.String()+` "n" `+p+`ready`)
	if got.Kind != wireFwd || got.Tail != p+"ready" {
		t.Errorf("got %+v", got)
	}
}

func TestClassifyNotProtocol(t *testing.T) {
	r := testState()
	p := "slprobe/2 " + testNonce + " "
	stale := "slprobe/2 fedcba9876543210 "
	for _, c := range []struct {
		name string
		typ  uint8
		src  msg.UUID
		text string
	}{
		{"no prefix", sl.ChatDirect, primA, "hello 1 " + primA.String()},
		{"product chat from a stranger", sl.ChatDirect, other, p + "bad"},
		{"product chat on say", sl.ChatSay, primA, p + "bad"},
		{"probe line as owner chat", sl.ChatOwner, primA, p + "bad"},
		{"bridge line as direct chat", sl.ChatDirect, bridge, p + "ready"},
		{"owner chat from a stranger", sl.ChatOwner, other, p + "ready"},
		{"stale nonce from a probe", sl.ChatDirect, primA, stale + "bad"},
		{"stale nonce from the bridge", sl.ChatOwner, bridge, stale + "ready"},
		{"version only", sl.ChatDirect, primA, "slprobe/2"},
		{"nonce without a space", sl.ChatDirect, primA, "slprobe/2 " + testNonce},
		{"other version", sl.ChatDirect, primA, "slprobe/3 " + testNonce + " bad"},
		{"product chat starting with the version", sl.ChatDirect, other, "slprobe/2 is the name of my protocol"},
		{"say from the bridge", sl.ChatSay, bridge, p + "ready"},
	} {
		got, err := r.Classify(c.typ, c.src, c.text)
		if err != nil || got.Kind != wireNone {
			t.Errorf("%s: got %+v, %v", c.name, got, err)
		}
	}
	// No bridge yet: the zero key is not a source.
	r.Bridge = msg.UUID{}
	if got, err := r.Classify(sl.ChatOwner, msg.UUID{}, p+"ready"); err != nil || got.Kind != wireNone {
		t.Errorf("zero bridge: got %+v, %v", got, err)
	}
}

func TestClassifyMalformedIsAnError(t *testing.T) {
	r := testState()
	p := "slprobe/2 " + testNonce + " "
	k := other.String()
	for _, line := range []string{
		"", "nonsense", "bad extra", "hello", "hello x " + k, "hello 1", "hello 1 " + k + " more",
		"hello 01 " + k, "hello +1 " + k, "hello 1 " + strings.ToUpper(k), "hello 1 not-a-key",
		"link 1 2 3 " + k, `link 1 2 3 ` + k + ` unquoted`, `link 1 2 3 ` + k + ` "bad\x"`, `link 1 2 x ` + k + ` "t"`,
		"overflow 1 2 3 " + k, "overflow 1 2 3 " + k + " -4", "overflow 1 2 3 " + k + " x",
		"ready", "fwd 1 " + k + ` "n" x`, // ready and fwd are the bridge's
	} {
		if got, err := r.Classify(sl.ChatDirect, primA, p+line); err == nil {
			t.Errorf("probe %q: got %+v, want an error", line, got)
		}
	}
	for _, line := range []string{
		"", "ready now", "bad", "hello 1 " + k, "fwd", "fwd 1", "fwd 1 " + k, "fwd 1 " + k + " noquote tail",
		"fwd 1 " + k + ` "unterminated tail`, "fwd 1 " + k + ` "n"tail`, `fwd 1 ` + k + ` "bad\x" tail`,
		"fwd x " + k + ` "n" tail`, "fwd 1 nokey" + ` "n" tail`,
		"fwd-overflow 1 " + k, "fwd-overflow 1 " + k + " x", "fwd-overflow 1 " + k + " 1 2",
	} {
		if got, err := r.Classify(sl.ChatOwner, bridge, p+line); err == nil {
			t.Errorf("bridge %q: got %+v, want an error", line, got)
		}
	}
}

// stream is a deterministic reader: byte i is seed+i.
type stream struct{ n byte }

func (s *stream) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = s.n
		s.n++
	}
	return len(p), nil
}

func TestDrawsAreDeterministic(t *testing.T) {
	draw := func() (string, int32, []int32) {
		d := newDraws(&stream{}, nil)
		n, err := d.Nonce()
		if err != nil {
			t.Fatal(err)
		}
		c, err := d.Control()
		if err != nil {
			t.Fatal(err)
		}
		var cmds []int32
		for i := 0; i < 3; i++ {
			v, err := d.Command()
			if err != nil {
				t.Fatal(err)
			}
			cmds = append(cmds, v)
		}
		return n, c, cmds
	}
	n1, c1, m1 := draw()
	n2, c2, m2 := draw()
	if n1 != "0001020304050607" || n1 != n2 || c1 != c2 || len(m1) != 3 || m1[0] != m2[0] || m1[2] != m2[2] {
		t.Fatalf("%s %d %v / %s %d %v", n1, c1, m1, n2, c2, m2)
	}
	if !nonceRE.MatchString(n1) {
		t.Fatalf("nonce %q", n1)
	}
}

func TestDrawsRanges(t *testing.T) {
	d := newDraws(nil, []int32{-5, 7300104})
	n, err := d.Nonce()
	if err != nil || !nonceRE.MatchString(n) {
		t.Fatalf("%q %v", n, err)
	}
	seen := map[int32]bool{}
	c, err := d.Control()
	if err != nil || c < 0x40000000 || c > 0x7FFFFFFE {
		t.Fatalf("control %d %v", c, err)
	}
	seen[c] = true
	for i := 0; i < 500; i++ {
		v, err := d.Command()
		if err != nil || v >= 0 || seen[v] || v == -5 {
			t.Fatalf("command %d %v", v, err)
		}
		seen[v] = true
	}
}

func TestDrawsAvoidCollisions(t *testing.T) {
	// Every four bytes are the same, so the first draw is taken and the
	// reader must be asked again; the reserved channel is skipped.
	var src bytes.Buffer
	src.Write([]byte{0, 0, 0, 0})             // command -1, reserved
	src.Write([]byte{0, 0, 0, 0})             // again
	src.Write([]byte{0, 0, 0, 1})             // -2
	src.Write([]byte{0, 0, 0, 1})             // -2 again, taken
	src.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF}) // -2147483648
	d := newDraws(&src, []int32{-1})
	a, err := d.Command()
	if err != nil || a != -2 {
		t.Fatalf("%d %v", a, err)
	}
	b, err := d.Command()
	if err != nil || b != math.MinInt32 {
		t.Fatalf("%d %v", b, err)
	}
	// An exhausted reader is an error, not a loop.
	if _, err := d.Command(); err == nil {
		t.Fatal("no error from an empty reader")
	}
	if _, err := newDraws(strings.NewReader("ab"), nil).Nonce(); err == nil {
		t.Fatal("no error from a short reader")
	}
}

func TestSubstituteEachPlaceholder(t *testing.T) {
	tester, br := primA, bridge
	src, err := probeSource(testNonce, tester, br, -123456)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`integer COMMAND = -123456;`,
		`key     TESTER  = "` + tester.String() + `";`,
		`key     BRIDGE  = "` + br.String() + `";`,
		`string  NONCE   = "` + testNonce + `";`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("probe has no %q", want)
		}
	}
	for _, left := range []string{`"0000000000000000"`, nullKey, "-7702101"} {
		if strings.Contains(src, left) {
			t.Errorf("probe still has the default %s", left)
		}
	}
	b, err := bridgeSource(testNonce, 0x50000001, []int32{7300104, -5})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`integer CONTROL = 1342177281;`,
		`list    LISTENS = [7300104, -5];`,
		`string  NONCE   = "` + testNonce + `";`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("bridge has no %q", want)
		}
	}
	for _, left := range []string{`"0000000000000000"`, "1073741824", "LISTENS = [];"} {
		if strings.Contains(b, left) {
			t.Errorf("bridge still has the default %s", left)
		}
	}
	if e, err := bridgeSource(testNonce, 0x50000001, nil); err != nil || !strings.Contains(e, "LISTENS = [];") {
		t.Errorf("empty listens: %v", err)
	}
	// Only the initializers change.
	if strings.Count(src, "\n") != strings.Count(probeLSL, "\n") || strings.Count(b, "\n") != strings.Count(bridgeLSL, "\n") {
		t.Error("substitution changed the line count")
	}
}

func TestSubstituteFailsLoudly(t *testing.T) {
	if _, err := substitute("integer OTHER = 1;\n", "COMMAND", "5"); err == nil {
		t.Error("a missing declaration was accepted")
	}
	two := "integer COMMAND = 1;\ninteger COMMAND = 2;\n"
	if _, err := substitute(two, "COMMAND", "5"); err == nil {
		t.Error("a repeated declaration was accepted")
	}
	// A name inside a comment or another name is not a declaration.
	if _, err := substitute("// integer COMMAND = 1;\ninteger MYCOMMAND = 1;\n", "COMMAND", "5"); err == nil {
		t.Error("a comment or a longer name was taken for the declaration")
	}
	got, err := substitute("string  NONCE   = \"x\"; // keep\n", "NONCE", `"y"`)
	if err != nil || got != "string  NONCE   = \"y\"; // keep\n" {
		t.Errorf("got %q, %v", got, err)
	}
	for _, nonce := range []string{"", "0123", "0123456789ABCDEF", "0123456789abcdeg", "0123456789abcdef0"} {
		if _, err := probeSource(nonce, primA, bridge, -5); err == nil {
			t.Errorf("nonce %q was accepted", nonce)
		}
		if _, err := bridgeSource(nonce, 0x50000000, nil); err == nil {
			t.Errorf("bridge nonce %q was accepted", nonce)
		}
	}
	if s, err := lslString("a\"b\\c\nd"); err != nil || s != `"a\"b\\c\nd"` {
		t.Errorf("lslString: %s, %v", s, err)
	}
	if _, err := lslString("a\tb"); err == nil {
		t.Error("a tab in an LSL literal was accepted")
	}
}

// listing returns the ```lsl block of the document that starts with first.
func listing(t *testing.T, doc, first string) string {
	t.Helper()
	re := regexp.MustCompile("(?s)```lsl\n(.*?)```")
	var found []string
	for _, m := range re.FindAllStringSubmatch(doc, -1) {
		if strings.HasPrefix(m[1], first) {
			found = append(found, m[1])
		}
	}
	if len(found) != 1 {
		t.Fatalf("the document has %d listings starting %q, want 1", len(found), first)
	}
	return found[0]
}

func TestEmbeddedListingsAreTheDocuments(t *testing.T) {
	doc, err := os.ReadFile("../doc/slate-runner.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := listing(t, string(doc), "// slate probe 2."); got != probeLSL {
		t.Error("probeLSL differs from the listing in doc/slate-runner.md")
	}
	if got := listing(t, string(doc), "// slate bridge 2."); got != bridgeLSL {
		t.Error("bridgeLSL differs from the listing in doc/slate-runner.md")
	}
}

// lslBody is the text of the braces that follow the first occurrence of head.
func lslBody(t *testing.T, src, head string) string {
	t.Helper()
	i := strings.Index(src, head)
	if i < 0 {
		t.Fatalf("no %q in the listing", head)
	}
	open := strings.Index(src[i:], "{")
	if open < 0 {
		t.Fatalf("no body after %q", head)
	}
	depth := 0
	for j := i + open; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return src[i+open+1 : j]
			}
		}
	}
	t.Fatalf("unbalanced braces after %q", head)
	return ""
}

func TestListingStructure(t *testing.T) {
	for name, src := range map[string]string{"probe": probeLSL, "bridge": bridgeLSL} {
		// "\t" in an LSL literal is four spaces; the listings use llChar(9).
		// Why: doc/slate-runner.md#third-round-the-listings-as-written
		// A comment may mention the literal; code may not.
		for _, line := range strings.Split(src, "\n") {
			if code, _, _ := strings.Cut(line, "//"); strings.Contains(code, `"\t"`) {
				t.Errorf("%s has a \"\\t\" literal: %s", name, line)
			}
		}
		if !strings.Contains(src, "TAB = llChar(9);") {
			t.Errorf("%s does not make its tab with llChar(9)", name)
		}
	}
	listen := lslBody(t, probeLSL, "listen(integer ch")
	if !strings.Contains(listen, "llMessageLinked(") {
		t.Error("the probe's listen does not call llMessageLinked")
	}
	if !strings.Contains(listen, "unquote(") {
		t.Error("the probe's listen does not call unquote")
	}
	for _, fn := range []string{"string quote(string s)", "string unquote(string s)"} {
		if !strings.Contains(probeLSL, fn) {
			t.Errorf("the probe has no %s", fn)
		}
		if n := len(strings.TrimSpace(lslBody(t, probeLSL, fn))); n < 40 {
			t.Errorf("the probe's %s is nearly empty", fn)
		}
	}
	bl := lslBody(t, bridgeLSL, "listen(integer ch")
	if !strings.Contains(bl, "quote(") {
		t.Error("the bridge's listen does not call quote(")
	}
	if !strings.Contains(bl, "llRegionSayTo(") {
		t.Error("the bridge's listen does not relay with llRegionSayTo")
	}
	if strings.Contains(bridgeLSL, "TESTER") {
		t.Error("the bridge has a TESTER: the wearer is the owner")
	}
}

func TestScriptNames(t *testing.T) {
	if probeScript != "slate probe" || bridgeScript != "slate bridge" {
		t.Fatal(probeScript, bridgeScript)
	}
}
