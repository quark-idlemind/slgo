package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sample = `
version 2.0

// A comment { with braces } in it.
{
	TestMessage Low 1 NotTrusted Zerocoded
	{
		TestBlock1 Single
		{	Test1	U32	}
	}
	{
		NeighborBlock Multiple 4
		{	Test0	LLUUID		}
		{	Name	Variable	1	}
		{	Pad	Fixed		8	}
	}
	{
		Items Variable
		{	Where	LLVector3	}
	}
}

{
	OpenCircuit Fixed 0xFFFFFFFC NotTrusted Unencoded UDPBlackListed
	{
		CircuitInfo Single
		{	IP	IPADDR	}
		{	Port	IPPORT	}
	}
}
`

func TestParseSample(t *testing.T) {
	msgs, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("parsed %d messages, want 2", len(msgs))
	}

	m := msgs[0]
	if m.Name != "TestMessage" || m.Freq != "Low" || m.Number != 1 {
		t.Errorf("header = %+v", m)
	}
	if m.Trusted || !m.Zerocoded || m.Deprecation != "" {
		t.Errorf("flags = %+v", m)
	}
	if len(m.Block) != 3 {
		t.Fatalf("%d blocks, want 3", len(m.Block))
	}
	if m.Block[1].Quant != "Multiple" || m.Block[1].Count != 4 {
		t.Errorf("block 1 = %+v", m.Block[1])
	}
	if m.Block[2].Quant != "Variable" {
		t.Errorf("block 2 = %+v", m.Block[2])
	}
	if f := m.Block[1].Field[1]; f.Type != "Variable" || f.Size != 1 {
		t.Errorf("Name field = %+v", f)
	}
	if f := m.Block[1].Field[2]; f.Type != "Fixed" || f.Size != 8 {
		t.Errorf("Pad field = %+v", f)
	}

	o := msgs[1]
	if o.Freq != "Fixed" || o.Number != 0xfffffffc || o.Deprecation != "UDPBlackListed" {
		t.Errorf("OpenCircuit = %+v", o)
	}
}

func TestGenerateSample(t *testing.T) {
	msgs, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate("msg", "sample.msg", msgs)
	if err != nil {
		t.Fatal(err)
	}
	// gofmt aligns struct fields into columns, so compare with
	// runs of whitespace collapsed.
	got := squeeze(string(out))

	for _, want := range []string{
		"package msg",
		"type TestMessage_TestBlock1 struct",
		"Test1 uint32 `ll:\"U32\"`",
		"Name []byte `ll:\"Variable,1\"`",
		"Pad [8]byte `ll:\"Fixed,8\"`",
		"TestBlock1 TestMessage_TestBlock1 `ll:\"Single\"`",
		"NeighborBlock [4]TestMessage_NeighborBlock `ll:\"Multiple,4\"`",
		"Items []TestMessage_Items `ll:\"Variable\"`",
		"ID: MakeID(FreqLow, 1)",
		"MakeID(FreqFixed, 0xfffffffc)",
		"Deprecation: \"UDPBlackListed\"",
		"func (m *TestMessage) Encode() ([]byte, error) { return Marshal(m) }",
		"func (m *TestMessage) Decode(b []byte) error   { return Unmarshal(b, m) }",
		"register(&infoTestMessage, func() Message { return new(TestMessage) })",
	} {
		if !strings.Contains(got, squeeze(want)) {
			t.Errorf("generated source is missing %q", want)
		}
	}
}

// squeeze collapses runs of spaces and tabs to a single space.
func squeeze(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t'
	}), " ")
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"unknown type", `{ M Low 1 NotTrusted Unencoded { B Single { F Blah } } }`, "unknown field type"},
		{"unknown priority", `{ M Sideways 1 NotTrusted Unencoded { B Single { F U8 } } }`, "unknown priority"},
		{"unknown trust", `{ M Low 1 Maybe Unencoded { B Single { F U8 } } }`, "unknown trust"},
		{"unknown encoding", `{ M Low 1 NotTrusted Rot13 { B Single { F U8 } } }`, "unknown encoding"},
		{"unknown quantifier", `{ M Low 1 NotTrusted Unencoded { B Lots { F U8 } } }`, "unknown block quantifier"},
		{"bad number", `{ M Low xx NotTrusted Unencoded { B Single { F U8 } } }`, "bad message number"},
		{"high out of range", `{ M High 300 NotTrusted Unencoded { B Single { F U8 } } }`, "out of range"},
		{"fixed too small", `{ M Fixed 5 NotTrusted Unencoded { B Single { F U8 } } }`, "at least 0xffffff00"},
		{"bad variable width", `{ M Low 1 NotTrusted Unencoded { B Single { F Variable 3 } } }`, "must be 1, 2 or 4"},
		{"bad multiple count", `{ M Low 1 NotTrusted Unencoded { B Multiple x { F U8 } } }`, "bad Multiple count"},
		{"unclosed message", `{ M Low 1 NotTrusted Unencoded { B Single { F U8 } }`, "not closed"},
		{"duplicate message", `{ M Low 1 NotTrusted Unencoded { B Single { F U8 } } }
		                       { M Low 2 NotTrusted Unencoded { B Single { F U8 } } }`, "already defined"},
		{"duplicate block", `{ M Low 1 NotTrusted Unencoded { B Single { F U8 } } { B Single { G U8 } } }`, "two blocks named"},
		{"duplicate field", `{ M Low 1 NotTrusted Unencoded { B Single { F U8 } { F U8 } } }`, "two fields named"},
		{"junk flag", `{ M Low 1 NotTrusted Unencoded Sideways { B Single { F U8 } } }`, "unknown message flag"},

		// A template is somebody else's file arriving over a network, so
		// every one of these is a way half of one can turn up: a truncated
		// fetch stops between any two tokens, and the parser has to say
		// where rather than dereference its way off the end.
		{"nothing after version", `version`, "unexpected end of template"},
		{"nothing after the brace", `{`, "unexpected end of template"},
		{"nothing after the name", `{ M`, "unexpected end of template"},
		{"nothing after the priority", `{ M Low`, "unexpected end of template"},
		{"nothing after the number", `{ M Low 1`, "unexpected end of template"},
		{"nothing after the trust", `{ M Low 1 NotTrusted`, "unexpected end of template"},
		{"nothing after the flags", `{ M Low 1 NotTrusted Unencoded`, "is not closed"},
		{"nothing after the block brace", `{ M Low 1 NotTrusted Unencoded {`, "unexpected end of template"},
		{"nothing after the block name", `{ M Low 1 NotTrusted Unencoded { B`, "unexpected end of template"},
		{"nothing after the field brace", `{ M Low 1 NotTrusted Unencoded { B Single {`, "unexpected end of template"},
		{"nothing after the field name", `{ M Low 1 NotTrusted Unencoded { B Single { F`, "unexpected end of template"},
		{"nothing after Multiple", `{ M Low 1 NotTrusted Unencoded { B Multiple`, "unexpected end of template"},
		{"nothing after the field type", `{ M Low 1 NotTrusted Unencoded { B Single { F Variable`, "unexpected end of template"},
		{"nothing closing the field", `{ M Low 1 NotTrusted Unencoded { B Single { F U8`, `wanted "}"`},
		{"nothing closing the block", `{ M Low 1 NotTrusted Unencoded { B Single { F U8 }`, "block B is not closed"},

		// And the ways a whole file can be wrong rather than short.
		{"not a message at all", `TestMessage Low 1`, "wanted a message"},
		{"a brace where a name goes", `{ { B Single { F U8 } } }`, "wanted a word"},
		{"a word where the message ends", `{ M Low 1 NotTrusted Unencoded { B Single { F U8 } } junk }`, `wanted "}"`},
		{"a word where the block ends", `{ M Low 1 NotTrusted Unencoded { B Single { F U8 } junk } }`, `wanted "}"`},
		{"unusable message name", `{ 1M Low 1 NotTrusted Unencoded { B Single { F U8 } } }`, "not a usable message name"},
		{"unusable block name", `{ M Low 1 NotTrusted Unencoded { 1B Single { F U8 } } }`, "not a usable block name"},
		{"unusable field name", `{ M Low 1 NotTrusted Unencoded { B Single { 1F U8 } } }`, "not a usable field name"},
		{"bad Fixed size", `{ M Low 1 NotTrusted Unencoded { B Single { F Fixed x } } }`, "bad Fixed size"},
		{"low out of range", `{ M Low 70000 NotTrusted Unencoded { B Single { F U8 } } }`, "Low number 70000 is out of range"},
		{"medium out of range", `{ M Medium 0 NotTrusted Unencoded { B Single { F U8 } } }`, "out of range 1..254"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.src))
			if err == nil {
				t.Fatalf("expected an error containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not contain %q", err, c.want)
			}
		})
	}
}

// TestParseRealTemplate is the one that matters: the actual file, from
// where Linden Lab publishes it.
//
// Fetched rather than kept here, for the same reason the generator
// fetches it -- the template is theirs, and a copy in this repository
// would be a stale second opinion, which is exactly what the last one
// turned out to be.  Skipped when there is no network, so an offline
// build still passes.
//
// What it checks is deliberately not an exact count.  The template is
// somebody else's file and it moves; a test asserting "483 messages"
// fails the day Linden Lab adds one, which says nothing about this
// parser.  So: it parses, it is the right order of magnitude, and the
// messages this client actually depends on are present and shaped as
// expected.
func TestParseRealTemplate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ViewerURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("cannot reach %s: %v", ViewerURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("%s answered %s", ViewerURL, resp.Status)
	}
	src, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Skipf("reading %s: %v", ViewerURL, err)
	}

	msgs, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) < 400 {
		t.Fatalf("parsed %d messages, which is too few to be the real template", len(msgs))
	}
	if _, err := Generate("msg", ViewerURL, msgs); err != nil {
		t.Fatal(err)
	}

	// A message this client depends on, checked in full: the parser
	// getting the shape right matters more than the count.
	byName := map[string]*Message{}
	for i := range msgs {
		byName[msgs[i].Name] = &msgs[i]
	}
	chat, ok := byName["ChatFromSimulator"]
	if !ok {
		t.Fatal("the template has no ChatFromSimulator")
	}
	if chat.Freq != "Low" {
		t.Errorf("ChatFromSimulator is %s frequency, want Low", chat.Freq)
	}
	if len(chat.Block) != 1 || chat.Block[0].Name != "ChatData" {
		t.Fatalf("ChatFromSimulator has blocks %v, want one called ChatData", chat.Block)
	}
	for _, want := range []string{"FromName", "SourceID", "ChatType", "Message"} {
		found := false
		for _, f := range chat.Block[0].Field {
			if f.Name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("ChatFromSimulator.ChatData has no %s field", want)
		}
	}

	// And the ones every session depends on existing at all.
	for _, want := range []string{
		"UseCircuitCode", "CompleteAgentMovement", "AgentUpdate",
		"PacketAck", "StartPingCheck", "RegionHandshake", "LogoutRequest",
	} {
		if _, ok := byName[want]; !ok {
			t.Errorf("the template has no %s", want)
		}
	}
}

// --------------------------------------------------------------- reading

// TestATemplateIsTakenFromAPathAsWellAsAURL: -template takes one flag
// and decides from the string itself, because a path and a URL are never
// confusable and one flag is one thing to remember.  The path form is
// what makes an offline build possible at all, so it is the one checked
// here; the URL form is checked below against a server on loopback.
func TestATemplateIsTakenFromAPathAsWellAsAURL(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "message_template.msg")
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != sample {
		t.Errorf("read returned %d bytes, want the file's %d", len(got), len(sample))
	}

	// A path that is not there is the caller's mistake, reported as the
	// file system's own words rather than as a fetch.
	if _, err := read(filepath.Join(t.TempDir(), "nothing.msg")); err == nil {
		t.Error("read invented a template that is not on disk")
	}
}

// TestAFetchedTemplateHasToLookLikeOne: a proxy or a login page answering
// 200 with HTML would otherwise reach the parser and be reported as a
// syntax error on line 1, which points the reader at Linden Lab's file
// rather than at their own network.
func TestAFetchedTemplateHasToLookLikeOne(t *testing.T) {
	t.Parallel()
	var body string
	var status int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	defer srv.Close()

	body, status = sample, http.StatusOK
	got, err := read(srv.URL)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != sample {
		t.Errorf("read returned %d bytes, want the %d served", len(got), len(sample))
	}

	body, status = "<html><body>sign in</body></html>", http.StatusOK
	if _, err := read(srv.URL); err == nil {
		t.Error("a login page was taken for a message template")
	} else if !strings.Contains(err.Error(), "did not answer with a message template") {
		t.Errorf("read = %v, want it to say what came back was not a template", err)
	}

	body, status = "no such file", http.StatusNotFound
	if _, err := read(srv.URL); err == nil {
		t.Error("a 404 was taken for a message template")
	} else if !strings.Contains(err.Error(), "404") {
		t.Errorf("read = %v, want it to carry the status", err)
	}
}

// TestAFetchThatCannotBeMadeSaysWhatToDoInstead: a machine with no way
// out is the ordinary way this fails during a build, and the reader is
// one flag away from not needing the network at all -- which is worth
// saying, because "connection refused" does not suggest it.
func TestAFetchThatCannotBeMadeSaysWhatToDoInstead(t *testing.T) {
	t.Parallel()
	// Port 1 on loopback: nothing listens there, so this fails without
	// asking anything of the network the machine is on.
	_, err := read("http://127.0.0.1:1/message_template.msg")
	if err == nil {
		t.Fatal("read fetched a template from a port nothing is listening on")
	}
	if !strings.Contains(err.Error(), "-template also takes a path") {
		t.Errorf("read = %v, want it to name the offline alternative", err)
	}
}

// TestAFetchThatBreaksPartWayThroughIsNotHalfATemplate: the body is read
// to the end before anything looks at it, so a connection that goes away
// mid-file is reported as the transfer it was rather than as a template
// that stops in the middle of a message.
func TestAFetchThatBreaksPartWayThroughIsNotHalfATemplate(t *testing.T) {
	t.Parallel()
	// More Content-Length than body: the client asks for the rest, the
	// server has already finished, and the read ends short.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		io.WriteString(w, "// version 2.0\n")
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	defer srv.Close()

	if _, err := read(srv.URL); err == nil {
		t.Error("half a template came back as a whole one")
	} else if !strings.Contains(err.Error(), "reading") {
		t.Errorf("read = %v, want it to say the read is what failed", err)
	}
}

// TestAnUnaskableURLIsRefusedBeforeAnythingIsDialled: -template is a
// string off a command line, so it can be something no request can be
// built from at all -- and that is the caller's typo rather than a
// network failure, worth saying before a timeout can be blamed.
func TestAnUnaskableURLIsRefusedBeforeAnythingIsDialled(t *testing.T) {
	t.Parallel()
	if _, err := read("http://exa\x7fmple.invalid/template.msg"); err == nil {
		t.Error("a URL with a control character in it was fetched")
	}
}

// ------------------------------------------------------------ generating

// TestGeneratedSourceThatWillNotParseIsReportedWithTheSource: the
// generator writes Go and the template is somebody else's file, so a name
// that is a Go keyword produces source that does not compile.  What comes
// back then is the UNFORMATTED source as well as the error, so that the
// line numbers in the error mean something.
func TestGeneratedSourceThatWillNotParseIsReportedWithTheSource(t *testing.T) {
	t.Parallel()
	// "type" passes the template's own name rule and is a Go keyword, so
	// it reaches the emitter and comes back out as `type type struct`.
	msgs, err := Parse([]byte(`{ type Low 1 NotTrusted Unencoded { B Single { F U8 } } }`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := Generate("msg", "sample.msg", msgs)
	if err == nil {
		t.Fatal("a message called type generated Go that parses")
	}
	if !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("Generate = %v, want it to say the generated source is the problem", err)
	}
	if !strings.Contains(string(out), "type type struct") {
		t.Error("the unformatted source was not returned, so the error's line numbers name nothing")
	}
}

// TestStatsCountWhatWasParsed: -stats is a summary for a person deciding
// whether the file they fetched is the file they meant, so the counts
// have to be of the template and the ordering has to be stable enough to
// compare two runs by eye.
func TestStatsCountWhatWasParsed(t *testing.T) {
	t.Parallel()
	msgs, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "stats")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	printStats(f, msgs)
	f.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"2 messages, 4 blocks, 7 fields",
		"Low=1", "Fixed=1",
		"Single=2", "Multiple=1", "Variable=1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stats are missing %q:\n%s", want, got)
		}
	}

	// Ties are broken by name, so that two runs of the same template
	// print the same line and a difference means a difference.
	if got := counts(map[string]int{"b": 1, "a": 1, "c": 3}); got != "c=3 a=1 b=1" {
		t.Errorf("counts = %q, want the commonest first and ties by name", got)
	}
}

// ----------------------------------------------------------------- main

// msggen runs the program in this process, so that what is measured is
// the program and not a copy of its argument handling.
//
// The two things that make that possible are that the flags live in a set
// which can be replaced, and that everything main writes goes to a file
// descriptor a test can point somewhere else.  os.Exit is the one thing
// it cannot survive, so the paths that end in one are not driven from
// here; they are what read and Parse are tested for directly above.
func msggen(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()

	dir := t.TempDir()
	out, errs := create(t, filepath.Join(dir, "stdout")), create(t, filepath.Join(dir, "stderr"))

	saveArgs, saveOut, saveErr, saveFlags := os.Args, os.Stdout, os.Stderr, flag.CommandLine
	os.Args = append([]string{"msggen"}, args...)
	os.Stdout, os.Stderr = out, errs
	flag.CommandLine = flag.NewFlagSet("msggen", flag.ContinueOnError)
	defer func() {
		os.Args, os.Stdout, os.Stderr, flag.CommandLine = saveArgs, saveOut, saveErr, saveFlags
	}()

	main()

	out.Close()
	errs.Close()
	return readBack(t, out.Name()), readBack(t, errs.Name())
}

func create(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func readBack(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestMsggenWritesAFileNamingWhereTheTemplateCameFrom: the generated
// header is the only record of which of the three templates was used, and
// they do not agree -- so a file that did not say would leave a reader
// guessing which grid it describes.
func TestMsggenWritesAFileNamingWhereTheTemplateCameFrom(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "message_template.msg")
	if err := os.WriteFile(src, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "messages_gen.go")

	msggen(t, "-template", src, "-out", dst, "-package", "wire")

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("nothing was written: %v", err)
	}
	code := string(data)
	if !strings.Contains(code, "Code generated by cmd/msggen from "+src) {
		t.Error("the generated file does not say where the template came from")
	}
	if !strings.Contains(code, "package wire") {
		t.Error("-package was not honoured")
	}
	if !strings.Contains(code, "type TestMessage struct") {
		t.Error("the sample's messages are not in the generated file")
	}
}

// TestMsggenWritesToStdoutWhenAsked: -out - is how the output is read
// rather than kept, and it has to be the same bytes as the file form --
// nothing about where it goes may change what is generated.
func TestMsggenWritesToStdoutWhenAsked(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "message_template.msg")
	if err := os.WriteFile(src, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}

	got, _ := msggen(t, "-template", src, "-out", "-")
	if !strings.Contains(got, "package msg") {
		t.Errorf("stdout did not carry the generated file:\n%s", got)
	}

	dst := filepath.Join(dir, "messages_gen.go")
	msggen(t, "-template", src, "-out", dst)
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != got {
		t.Error("-out - and -out FILE produced different source")
	}
}

// TestMsggenStatsGoToStderrSoStdoutStaysTheFile: -stats is commentary,
// and a caller writing the generated source to stdout must not find a
// summary in the middle of it.
func TestMsggenStatsGoToStderrSoStdoutStaysTheFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "message_template.msg")
	if err := os.WriteFile(src, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}

	got, summary := msggen(t, "-template", src, "-out", "-", "-stats")
	if strings.Contains(got, "messages,") {
		t.Errorf("the summary was written to stdout with the source:\n%s", got)
	}
	if !strings.Contains(summary, "2 messages, 4 blocks, 7 fields") {
		t.Errorf("-stats said nothing about what was parsed:\n%s", summary)
	}
}

// TestEveryHeaderWordTheTemplateUses: the sample above is one Low
// message and one Fixed one, which leaves the rest of the header
// vocabulary unexercised -- and each word is a switch arm that decides a
// field of the generated Info.  A template that used one of them and got
// it silently dropped would produce a client that talks to the grid on
// the wrong terms.
func TestEveryHeaderWordTheTemplateUses(t *testing.T) {
	t.Parallel()
	const src = `
{
	Ping High 1 Trusted Unencoded UDPDeprecated
	{ PingID Single { PingID U8 } }
}
{
	Chat Medium 254 Trusted Zerocoded Deprecated
	{ Data Single { Text Variable 2 } }
}
`
	msgs, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("parsed %d messages, want 2", len(msgs))
	}
	if m := msgs[0]; m.Freq != "High" || !m.Trusted || m.Zerocoded || m.Deprecation != "UDPDeprecated" {
		t.Errorf("Ping = %+v", m)
	}
	if m := msgs[1]; m.Freq != "Medium" || !m.Trusted || !m.Zerocoded || m.Deprecation != "Deprecated" {
		t.Errorf("Chat = %+v", m)
	}

	out, err := Generate("msg", "sample.msg", msgs)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got := squeeze(string(out))
	for _, want := range []string{
		"// Ping is High 1, trusted, UDPDeprecated.",
		"// Chat is Medium 254, trusted, zerocoded, Deprecated.",
		"Trusted: true",
		"Zerocoded: true",
		"MakeID(FreqHigh, 1)",
		"MakeID(FreqMedium, 254)",
	} {
		if !strings.Contains(got, squeeze(want)) {
			t.Errorf("generated source is missing %q", want)
		}
	}
}
