package main

import (
	"context"
	"io"
	"net/http"
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
