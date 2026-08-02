package msg

import (
	"bytes"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// fill puts deterministic non-zero data into every field of a message,
// reading the same struct tags the codec does.
func fillMessage(t *testing.T, m Message, r *rand.Rand) {
	t.Helper()
	v := reflect.ValueOf(m).Elem()
	rt := v.Type()
	for i := 0; i < rt.NumField(); i++ {
		head, _, _ := strings.Cut(rt.Field(i).Tag.Get("ll"), ",")
		fv := v.Field(i)
		switch head {
		case "Single":
			fillBlock(fv, r)
		case "Multiple":
			for j := 0; j < fv.Len(); j++ {
				fillBlock(fv.Index(j), r)
			}
		case "Variable":
			n := r.Intn(3)
			s := reflect.MakeSlice(fv.Type(), n, n)
			for j := 0; j < n; j++ {
				fillBlock(s.Index(j), r)
			}
			fv.Set(s)
		default:
			t.Fatalf("%s: block %s has quantifier %q", m.MsgInfo().Name, rt.Field(i).Name, head)
		}
	}
}

func fillBlock(v reflect.Value, r *rand.Rand) {
	rt := v.Type()
	for i := 0; i < rt.NumField(); i++ {
		head, _, _ := strings.Cut(rt.Field(i).Tag.Get("ll"), ",")
		fv := v.Field(i)
		switch head {
		case "Variable":
			// Keep well inside the smallest prefix.
			b := make([]byte, r.Intn(9))
			r.Read(b)
			fv.SetBytes(b)
		case "Fixed", "LLUUID", "IPADDR":
			r.Read(fv.Slice(0, fv.Len()).Bytes())
		case "BOOL":
			fv.SetBool(r.Intn(2) == 1)
		case "LLVector3", "LLQuaternion":
			setFloats(fv, 3, r)
		case "LLVector4":
			setFloats(fv, 4, r)
		case "LLVector3d":
			setFloats(fv, 3, r)
		default:
			setScalar(fv, r)
		}
	}
}

func setFloats(v reflect.Value, n int, r *rand.Rand) {
	for i := 0; i < n; i++ {
		v.Field(i).SetFloat(roundTrippable(v.Field(i), r))
	}
}

func setScalar(v reflect.Value, r *rand.Rand) {
	switch v.Kind() {
	case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		x := r.Uint64()
		if b := v.Type().Bits(); b < 64 {
			x &= 1<<uint(b) - 1
		}
		v.SetUint(x)
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		x := int64(r.Uint64())
		if b := v.Type().Bits(); b < 64 {
			x >>= uint(64 - b)
		}
		v.SetInt(x)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(roundTrippable(v, r))
	case reflect.Bool:
		v.SetBool(r.Intn(2) == 1)
	}
}

// roundTrippable avoids NaN, which is never equal to itself, and keeps
// float32 values exactly representable.
func roundTrippable(v reflect.Value, r *rand.Rand) float64 {
	x := (r.Float64() - 0.5) * 1e4
	if v.Kind() == reflect.Float32 {
		return float64(float32(x))
	}
	return x
}

// TestRoundTripAll encodes and decodes every message in the template.
func TestRoundTripAll(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	n := 0
	for id := range infoByID {
		m := New(id)
		if m == nil {
			t.Fatalf("%v: New returned nil", id)
		}
		name := m.MsgInfo().Name

		fillMessage(t, m, r)
		wire, err := m.Encode()
		if err != nil {
			t.Fatalf("%s: encode: %v", name, err)
		}

		got := New(id)
		if err := got.Decode(wire); err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if !reflect.DeepEqual(m, got) {
			t.Fatalf("%s: round trip differs\n have %+v\n want %+v", name, got, m)
		}

		// Encoding what we decoded must give the same bytes.
		again, err := got.Encode()
		if err != nil {
			t.Fatalf("%s: re-encode: %v", name, err)
		}
		if !bytes.Equal(wire, again) {
			t.Fatalf("%s: re-encode differs", name)
		}
		n++
	}
	if n != 483 {
		t.Errorf("round tripped %d messages, expected 483", n)
	}
}

// TestWireSizes pins the width of every template type, from the sizes
// documented at https://wiki.secondlife.com/wiki/Message.
//
// LLQuaternion is the one worth stating out loud: it is "transmitted in
// messages as a triplet of floats, 12 bytes wide (represented in memory
// as a quad of floats, 16 bytes wide)".  Twelve on the wire, sixteen in
// memory, which is the trap the C generator fell into by mapping it to
// a four component read.
func TestWireSizes(t *testing.T) {
	cases := []struct {
		tmpl string
		zero any
		size int
		want int
	}{
		{"U8", uint8(0), 0, 1},
		{"U16", uint16(0), 0, 2},
		{"U32", uint32(0), 0, 4},
		{"U64", uint64(0), 0, 8},
		{"S8", int8(0), 0, 1},
		{"S16", int16(0), 0, 2},
		{"S32", int32(0), 0, 4},
		{"S64", int64(0), 0, 8},
		{"F32", float32(0), 0, 4},
		{"F64", float64(0), 0, 8},
		{"BOOL", false, 0, 1},
		{"LLUUID", UUID{}, 0, 16},
		{"LLVector3", Vector3{}, 0, 12},
		{"LLVector3d", Vector3d{}, 0, 24},
		{"LLVector4", Vector4{}, 0, 16},
		{"LLQuaternion", Quaternion{}, 0, 12},
		{"IPADDR", IPAddr{}, 0, 4},
		{"IPPORT", IPPort(0), 0, 2},
		{"Fixed", [32]byte{}, 32, 32},
		{"Variable", []byte(nil), 1, 1}, // just the length prefix
		{"Variable", []byte(nil), 2, 2},
		{"Variable", []byte(nil), 4, 4},
	}
	for _, c := range cases {
		k, ok := kindByName[c.tmpl]
		if !ok {
			t.Errorf("%s: not in kindByName", c.tmpl)
			continue
		}
		f := fieldPlan{name: c.tmpl, kind: k, size: c.size}
		v := reflect.New(reflect.TypeOf(c.zero)).Elem()
		w := &buf{}
		if err := encodeField(w, v, &f); err != nil {
			t.Errorf("%s: %v", c.tmpl, err)
			continue
		}
		if len(w.b) != c.want {
			t.Errorf("%s encodes to %d bytes, want %d", c.tmpl, len(w.b), c.want)
		}
	}
}

// TestAgentUpdateSize records that the whole message comes to 114
// bytes, which is what messages/q_AgentUpdate.c allocates with
// sizeof(luuid_t) * 2 + 6 * 12 + 10.
//
// Treat that agreement as a curiosity, not as evidence: the only call
// to queueAgentUpdate is commented out at s.c:1267, so this message has
// never been sent and the C was never validated against a sim.  The
// authority for the field widths is TestWireSizes above.
func TestAgentUpdateSize(t *testing.T) {
	var m AgentUpdate
	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	const want = 16*2 + 12*2 + 1 + 12*4 + 4 + 4 + 1
	if len(b) != want {
		t.Errorf("AgentUpdate encodes to %d bytes, want %d", len(b), want)
	}
}

// TestGoldenUseCircuitCode checks bytes against messages/q_UseCircuitCode.c:
// queue32(code) then two UUIDs, little endian, framed as Low 3.
func TestGoldenUseCircuitCode(t *testing.T) {
	session := MustParseUUID("0b9b7e57-7e57-c0de-edc7-d18ee7e201a4")
	agent := MustParseUUID("0f697e57-7e57-c0de-abef-6c32c5735a8e")

	m := &UseCircuitCode{}
	m.CircuitCode.Code = 0x12345678
	m.CircuitCode.SessionID = session
	m.CircuitCode.ID = agent

	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x78, 0x56, 0x34, 0x12}
	want = append(want, session[:]...)
	want = append(want, agent[:]...)
	if !bytes.Equal(body, want) {
		t.Errorf("body\n have %x\n want %x", body, want)
	}

	framed := AppendID(nil, m.MsgInfo().ID)
	if !bytes.Equal(framed, []byte{0xff, 0xff, 0x00, 0x03}) {
		t.Errorf("framing = %x, want ffff0003", framed)
	}
}

// TestGoldenPacketAck matches messages/q_PacketAck.c: a count of one
// followed by a little endian sequence number, framed as Fixed 0xFFFFFFFB.
func TestGoldenPacketAck(t *testing.T) {
	m := &PacketAck{Packets: []PacketAck_Packets{{ID: 0xAABBCCDD}}}
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x01, 0xdd, 0xcc, 0xbb, 0xaa}
	if !bytes.Equal(body, want) {
		t.Errorf("body = %x, want %x", body, want)
	}
	if framed := AppendID(nil, m.MsgInfo().ID); !bytes.Equal(framed, []byte{0xff, 0xff, 0xff, 0xfb}) {
		t.Errorf("framing = %x, want fffffffb", framed)
	}
}

// TestGoldenCompletePingCheck: High 2, one byte of body.
func TestGoldenCompletePingCheck(t *testing.T) {
	m := &CompletePingCheck{}
	m.PingID.PingID = 0x2a
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, []byte{0x2a}) {
		t.Errorf("body = %x, want 2a", body)
	}
	if framed := AppendID(nil, m.MsgInfo().ID); !bytes.Equal(framed, []byte{0x02}) {
		t.Errorf("framing = %x, want 02", framed)
	}
}

// TestVariableTwoBytePrefix checks a Variable 2 field, which is where
// the C client's String2 encoding lives.
func TestVariableTwoBytePrefix(t *testing.T) {
	m := &ChatFromViewer{}
	m.ChatData.Message = []byte("hello")
	m.ChatData.Type = 1
	m.ChatData.Channel = -3

	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// AgentData is two UUIDs of zero, then the chat block.
	rest := body[32:]
	want := []byte{0x05, 0x00, 'h', 'e', 'l', 'l', 'o', 0x01, 0xfd, 0xff, 0xff, 0xff}
	if !bytes.Equal(rest, want) {
		t.Errorf("chat block\n have %x\n want %x", rest, want)
	}
}

// TestIPPortIsNetworkOrder guards the one field type whose byte order
// differs from every other integer in the protocol.
func TestIPPortIsNetworkOrder(t *testing.T) {
	m := &OpenCircuit{}
	m.CircuitInfo.IP = IPAddr{10, 0, 0, 1}
	m.CircuitInfo.Port = 13010 // 0x32d2

	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{10, 0, 0, 1, 0x32, 0xd2}
	if !bytes.Equal(body, want) {
		t.Errorf("body = %x, want %x", body, want)
	}

	var back OpenCircuit
	if err := back.Decode(body); err != nil {
		t.Fatal(err)
	}
	if back.CircuitInfo.Port != 13010 {
		t.Errorf("port round tripped to %d", back.CircuitInfo.Port)
	}
}

// TestMultipleBlock covers the only quantifier with a fixed repeat
// count: NeighborList carries exactly four.
func TestMultipleBlock(t *testing.T) {
	var m NeighborList
	for i := range m.NeighborBlock {
		m.NeighborBlock[i].IP = IPAddr{byte(i), 0, 0, 1}
		m.NeighborBlock[i].Port = IPPort(1000 + i)
		// Set the Variable field: a decoded empty slice is not
		// deep-equal to an unset nil one.
		m.NeighborBlock[i].Name = []byte{byte('a' + i)}
	}
	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var back NeighborList
	if err := back.Decode(b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, m) {
		t.Errorf("have %+v want %+v", back, m)
	}
}

// TestTrailingBytesIgnored is the forward compatibility rule: the grid
// appends blocks to existing messages and we must not choke.
func TestTrailingBytesIgnored(t *testing.T) {
	m := &CompletePingCheck{}
	m.PingID.PingID = 7
	b, _ := m.Encode()
	b = append(b, 0xde, 0xad, 0xbe, 0xef)

	var back CompletePingCheck
	if err := back.Decode(b); err != nil {
		t.Fatalf("trailing bytes should be ignored, got %v", err)
	}
	if back.PingID.PingID != 7 {
		t.Errorf("PingID = %d", back.PingID.PingID)
	}
}

// TestMissingTrailingVariableBlock is the other half of that rule: this
// is exactly the ImprovedInstantMessage MetaData case, where the live
// grid accepts a message that stops before a Variable block it knows
// about.
func TestMissingTrailingVariableBlock(t *testing.T) {
	full := &ImprovedInstantMessage{}
	full.MessageBlock.Message = []byte("hi")
	b, err := full.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Drop the trailing MetaData count byte the encoder wrote.
	short := b[:len(b)-1]

	var back ImprovedInstantMessage
	if err := back.Decode(short); err != nil {
		t.Fatalf("a missing trailing Variable block should decode as empty, got %v", err)
	}
	if len(back.MetaData) != 0 {
		t.Errorf("MetaData = %v, want empty", back.MetaData)
	}
	if string(back.MessageBlock.Message) != "hi" {
		t.Errorf("Message = %q", back.MessageBlock.Message)
	}
}

// TestTruncationIsAnError makes sure the tolerance above does not
// swallow a genuinely short packet.
func TestTruncationIsAnError(t *testing.T) {
	var m UseCircuitCode
	if err := m.Decode([]byte{0x01, 0x02}); err == nil {
		t.Error("expected an error decoding a truncated UseCircuitCode")
	} else if !strings.Contains(err.Error(), "UseCircuitCode") {
		t.Errorf("error should name the message, got %v", err)
	}
}

// TestVariableFieldTooLong rejects a payload that cannot be described by
// its length prefix rather than silently truncating it.
func TestVariableFieldTooLong(t *testing.T) {
	m := &UUIDNameReply{
		UUIDNameBlock: []UUIDNameReply_UUIDNameBlock{{
			FirstName: bytes.Repeat([]byte("x"), 300), // Variable 1
		}},
	}
	if _, err := m.Encode(); err == nil {
		t.Error("expected an error for a 300 byte Variable 1 field")
	}
}

// TestVariableBlockTooMany does the same for the block count.
func TestVariableBlockTooMany(t *testing.T) {
	m := &PacketAck{Packets: make([]PacketAck_Packets, 256)}
	if _, err := m.Encode(); err == nil {
		t.Error("expected an error for 256 instances of a Variable block")
	}
}

func TestRegistryIsComplete(t *testing.T) {
	if len(infoByID) != 483 {
		t.Errorf("registry holds %d messages, expected 483", len(infoByID))
	}
	if len(newByID) != len(infoByID) {
		t.Errorf("%d constructors for %d messages", len(newByID), len(infoByID))
	}
	names := map[string]bool{}
	for id, info := range infoByID {
		if info.ID != id {
			t.Errorf("%s: registered under %v but reports %v", info.Name, id, info.ID)
		}
		if names[info.Name] {
			t.Errorf("duplicate name %s", info.Name)
		}
		names[info.Name] = true
		if m := New(id); m == nil || m.MsgInfo() != info {
			t.Errorf("%s: New does not round trip to its Info", info.Name)
		}
	}
}

// TestDecodeBody goes from framed bytes to a typed message.
func TestDecodeBody(t *testing.T) {
	m := &CompletePingCheck{}
	m.PingID.PingID = 99
	body, _ := m.Encode()

	packet := AppendID(nil, m.MsgInfo().ID)
	packet = append(packet, body...)

	got, err := DecodeBody(packet)
	if err != nil {
		t.Fatal(err)
	}
	ping, ok := got.(*CompletePingCheck)
	if !ok {
		t.Fatalf("got %T, want *CompletePingCheck", got)
	}
	if ping.PingID.PingID != 99 {
		t.Errorf("PingID = %d", ping.PingID.PingID)
	}
}

func TestUnknownMessage(t *testing.T) {
	// High 253 is not in the template.
	if _, err := DecodeBody([]byte{253}); err == nil {
		t.Error("expected an error for an unknown message number")
	}
}

func BenchmarkEncodeChatFromViewer(b *testing.B) {
	m := &ChatFromViewer{}
	m.ChatData.Message = []byte("the quick brown fox")
	buf := make([]byte, 0, 128)
	b.ReportAllocs()
	for b.Loop() {
		buf, _ = MarshalAppend(buf[:0], m)
	}
}

func BenchmarkDecodeChatFromViewer(b *testing.B) {
	m := &ChatFromViewer{}
	m.ChatData.Message = []byte("the quick brown fox")
	wire, _ := m.Encode()
	var out ChatFromViewer
	b.ReportAllocs()
	for b.Loop() {
		_ = out.Decode(wire)
	}
}
