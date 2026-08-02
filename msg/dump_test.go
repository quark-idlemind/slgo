package msg

import (
	"math/rand"
	"net"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDumpSingleBlock(t *testing.T) {
	m := &ChatFromViewer{}
	m.AgentData.AgentID = MustParseUUID("876e7e57-7e57-c0de-9eeb-1bd0e1ec6995")
	m.ChatData.Message = []byte("hello world\x00")
	m.ChatData.Type = 1
	m.ChatData.Channel = -3

	got := DumpMessage(m)
	for _, want := range []string{
		"message: ChatFromViewer",
		"id: {freq: Low, number: 80}",
		"blocks:",
		"  AgentData:",
		"    AgentID: 876e7e57-7e57-c0de-9eeb-1bd0e1ec6995",
		"  ChatData:",
		`    Message: "hello world"`, // the NUL terminator is dropped
		"    Type: 1",
		"    Channel: -3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dump is missing %q\n%s", want, got)
		}
	}
}

func TestDumpVariableBlock(t *testing.T) {
	m := &PacketAck{Packets: []PacketAck_Packets{{ID: 42}, {ID: 43}}}
	got := DumpMessage(m)
	if !strings.Contains(got, "  Packets:\n    - ID: 42\n    - ID: 43\n") {
		t.Errorf("sequence form wrong:\n%s", got)
	}

	empty := DumpMessage(&PacketAck{})
	if !strings.Contains(empty, "Packets: []") {
		t.Errorf("empty block should be []:\n%s", empty)
	}
}

func TestDumpMultipleBlock(t *testing.T) {
	var m NeighborList
	for i := range m.NeighborBlock {
		m.NeighborBlock[i].IP = IPAddr{10, 0, 0, byte(i)}
		m.NeighborBlock[i].Port = IPPort(1000 + i)
	}
	got := DumpMessage(&m)
	if !strings.Contains(got, "    - IP: 10.0.0.0\n") {
		t.Errorf("IPADDR should render dotted:\n%s", got)
	}
	if strings.Count(got, "- IP:") != 4 {
		t.Errorf("expected four entries:\n%s", got)
	}
}

// TestDumpQuaternionShowsW: W is not on the wire, but it is what the
// value means, so the dump computes it.
func TestDumpQuaternionShowsW(t *testing.T) {
	var m AgentUpdate
	m.AgentData.BodyRotation = PackQuaternion(0, 0, 0.7071068, 0.7071068)

	// W is recomputed from the other three, so it comes back a ulp
	// shy of the value that was packed.  That is inherent to the
	// scheme, not a rounding bug here.
	got := DumpMessage(&m)
	if !strings.Contains(got, "BodyRotation: {x: 0, y: 0, z: 0.7071068, w: 0.707106") {
		t.Errorf("quaternion should show the recovered W:\n%s", got)
	}
	// The identity is the zero value.
	if !strings.Contains(got, "HeadRotation: {x: 0, y: 0, z: 0, w: 1}") {
		t.Errorf("identity quaternion should show w: 1:\n%s", got)
	}
}

func TestDumpBinaryAsHex(t *testing.T) {
	m := &ImprovedInstantMessage{}
	m.MessageBlock.BinaryBucket = []byte{0x00, 0xff, 0x01, 0x80}

	got := DumpMessage(m)
	if !strings.Contains(got, `BinaryBucket: "0x00ff0180"`) {
		t.Errorf("binary should render as hex:\n%s", got)
	}
}

func TestDumpQuotesAwkwardText(t *testing.T) {
	m := &ChatFromViewer{}
	m.ChatData.Message = []byte("say \"hi\"\nline two\ttabbed")

	got := DumpMessage(m)
	if !strings.Contains(got, `Message: "say \"hi\"\nline two\ttabbed"`) {
		t.Errorf("escaping wrong:\n%s", got)
	}
}

func TestDumpPacketHeader(t *testing.T) {
	m := &CompletePingCheck{}
	m.PingID.PingID = 9
	p := &Packet{
		Addr:    &net.UDPAddr{IP: net.IPv4(198, 51, 100, 7), Port: 13010},
		At:      time.Date(2026, 8, 1, 20, 34, 12, 0, time.UTC),
		Header:  Header{Flags: FlagReliable | FlagZerocoded, Sequence: 1234},
		Message: m,
		Acks:    []uint32{42, 43},
	}
	got := DumpPacket(p)
	for _, want := range []string{
		`from: "198.51.100.7:13010"`,
		`at: "2026-08-01T20:34:12.000000Z"`,
		"sequence: 1234",
		"flags: [zerocoded, reliable]",
		"acks: [42, 43]",
		"message: CompletePingCheck",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("packet dump missing %q\n%s", want, got)
		}
	}
}

func TestDumpPacketError(t *testing.T) {
	p := &Packet{
		Header: Header{Sequence: 7},
		ID:     MakeID(FreqHigh, 253),
		Err:    ErrUnknownMessage,
		Body:   []byte{0xde, 0xad},
	}
	got := DumpPacket(p)
	if !strings.Contains(got, `error: "msg: unknown message number"`) {
		t.Errorf("missing error:\n%s", got)
	}
	if !strings.Contains(got, `body: "0xdead"`) {
		t.Errorf("missing raw body:\n%s", got)
	}
}

// TestDumpParsesAsYAML runs every message through a real YAML parser.
// Skipped when none is installed; the point is that the hand written
// emitter is checked against something that is not itself.
func TestDumpParsesAsYAML(t *testing.T) {
	cmd, why := yamlCounter()
	if cmd == nil {
		t.Skipf("no YAML parser available: %s", why)
	}

	r := rand.New(rand.NewSource(3))
	var sb strings.Builder
	for id := range infoByID {
		m := New(id)
		fillMessage(t, m, r)
		sb.WriteString("---\n")
		sb.Write(AppendMessageYAML(nil, m))
	}

	cmd.Stdin = strings.NewReader(sb.String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("YAML parse failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "483" {
		t.Errorf("parser read %q documents, want 483", got)
	}
}

// yamlCounter returns a command that reads a multi-document YAML stream
// on stdin and prints how many documents it contained.  Ruby ships with
// macOS and has Psych built in; pyyaml is used if it happens to be
// installed.
func yamlCounter() (*exec.Cmd, string) {
	if rb, err := exec.LookPath("ruby"); err == nil {
		script := `n = 0; YAML.load_stream(STDIN.read) { |d| n += 1 }; puts n`
		return exec.Command(rb, "-ryaml", "-e", script), ""
	}
	if py, err := exec.LookPath("python3"); err == nil {
		if out, err := exec.Command(py, "-c", "import yaml").CombinedOutput(); err == nil {
			script := "import sys, yaml; print(len(list(yaml.safe_load_all(sys.stdin.read()))))"
			return exec.Command(py, "-c", script), ""
		} else {
			return nil, "python3 has no yaml module: " + strings.TrimSpace(string(out))
		}
	}
	return nil, "neither ruby nor python3 found"
}

// TestDumpRoundTripsFieldNames makes sure the dump names every field
// the template has, so nothing is silently missing from a capture.
func TestDumpRoundTripsFieldNames(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	for id := range infoByID {
		m := New(id)
		fillMessage(t, m, r)
		out := DumpMessage(m)

		v := planMustFor(t, m)
		for bi := range v.blocks {
			b := &v.blocks[bi]
			if !strings.Contains(out, "  "+b.name+":") {
				t.Fatalf("%s: block %s missing from dump", m.MsgInfo().Name, b.name)
			}
		}
	}
}

func planMustFor(t *testing.T, m Message) *plan {
	t.Helper()
	p, err := planFor(reflect.ValueOf(m).Elem().Type(), m.MsgInfo().Name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func BenchmarkDumpMessage(b *testing.B) {
	m := &ChatFromViewer{}
	m.ChatData.Message = []byte("the quick brown fox")
	buf := make([]byte, 0, 512)
	b.ReportAllocs()
	for b.Loop() {
		buf = AppendMessageYAML(buf[:0], m)
	}
}
