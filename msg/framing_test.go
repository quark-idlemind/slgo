package msg

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestIDRoundTripAll frames and unframes every message number in the
// template.  This is where the C client's allocPacket goes wrong for
// Medium messages: it takes the marker byte from bits 8..15 of a code
// whose 0xFF lives at bits 24..31, and emits 00 nn instead of FF nn.
func TestIDRoundTripAll(t *testing.T) {
	for id, info := range infoByID {
		b := AppendID(nil, id)
		got, n, err := DecodeID(b)
		if err != nil {
			t.Fatalf("%s: %v", info.Name, err)
		}
		if n != len(b) {
			t.Errorf("%s: consumed %d of %d bytes", info.Name, n, len(b))
		}
		if got != id {
			t.Errorf("%s: framed %x, decoded %v, want %v", info.Name, b, got, id)
		}

		wantLen := map[Freq]int{FreqHigh: 1, FreqMedium: 2, FreqLow: 4, FreqFixed: 4}[id.Freq()]
		if len(b) != wantLen {
			t.Errorf("%s: %s framing is %d bytes, want %d", info.Name, id.Freq(), len(b), wantLen)
		}
	}
}

func TestIDFramingShape(t *testing.T) {
	cases := []struct {
		freq Freq
		num  uint32
		want []byte
	}{
		{FreqHigh, 1, []byte{0x01}},
		{FreqHigh, 254, []byte{0xfe}},
		{FreqMedium, 1, []byte{0xff, 0x01}},
		{FreqMedium, 18, []byte{0xff, 0x12}},
		{FreqLow, 3, []byte{0xff, 0xff, 0x00, 0x03}},
		{FreqLow, 431, []byte{0xff, 0xff, 0x01, 0xaf}},
		{FreqFixed, 0xfffffffb, []byte{0xff, 0xff, 0xff, 0xfb}},
	}
	for _, c := range cases {
		got := AppendID(nil, MakeID(c.freq, c.num))
		if !bytes.Equal(got, c.want) {
			t.Errorf("%s %d: framed %x, want %x", c.freq, c.num, got, c.want)
		}
	}
}

func TestDecodeIDShort(t *testing.T) {
	for _, b := range [][]byte{{}, {0xff}, {0xff, 0xff}, {0xff, 0xff, 0xff}} {
		if _, _, err := DecodeID(b); err == nil {
			t.Errorf("DecodeID(%x) should fail", b)
		}
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	h := Header{Flags: FlagReliable | FlagZerocoded, Sequence: 0x01020304, Extra: []byte{9, 8}}
	b := AppendHeader(nil, &h)

	// Sequence is network order, unlike the message body.
	if !bytes.Equal(b[:6], []byte{0xc0, 0x01, 0x02, 0x03, 0x04, 0x02}) {
		t.Errorf("header = %x", b[:6])
	}

	got, n, err := DecodeHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(b) {
		t.Errorf("consumed %d of %d", n, len(b))
	}
	if got.Flags != h.Flags || got.Sequence != h.Sequence || !bytes.Equal(got.Extra, h.Extra) {
		t.Errorf("have %+v want %+v", got, h)
	}
	if !got.Reliable() || !got.Zerocoded() || got.HasAcks() {
		t.Errorf("flag helpers disagree with %#x", got.Flags)
	}
}

func TestHeaderShort(t *testing.T) {
	if _, _, err := DecodeHeader([]byte{0, 1, 2}); err == nil {
		t.Error("expected an error for a short header")
	}
	// Claims two extra bytes but supplies one.
	if _, _, err := DecodeHeader([]byte{0, 0, 0, 0, 1, 2, 9}); err == nil {
		t.Error("expected an error for a truncated extra header")
	}
}

// TestSplitAcks matches the tail handling in s.c: a count byte preceded
// by that many big endian sequence numbers, taken from the end.
func TestSplitAcks(t *testing.T) {
	body := []byte{0xaa, 0xbb}
	body = append(body, 0x00, 0x00, 0x00, 0x07) // ack 7
	body = append(body, 0x00, 0x00, 0x00, 0x09) // ack 9
	body = append(body, 0x02)

	rest, acks, err := SplitAcks(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rest, []byte{0xaa, 0xbb}) {
		t.Errorf("body = %x", rest)
	}
	// s.c walks backwards from the end, so the last one appended
	// comes out first.
	if len(acks) != 2 || acks[0] != 9 || acks[1] != 7 {
		t.Errorf("acks = %v, want [9 7]", acks)
	}
}

func TestSplitAcksBad(t *testing.T) {
	if _, _, err := SplitAcks(nil); err == nil {
		t.Error("expected an error on an empty body")
	}
	if _, _, err := SplitAcks([]byte{0x05}); err == nil {
		t.Error("expected an error when the acks do not fit")
	}
}

func TestZeroCodeRoundTrip(t *testing.T) {
	cases := [][]byte{
		{},
		{1, 2, 3},
		{0},
		{0, 0, 0, 0},
		append(bytes.Repeat([]byte{0}, 300), 1),
		append([]byte{1}, bytes.Repeat([]byte{0}, 255)...),
		append([]byte{1}, bytes.Repeat([]byte{0}, 256)...),
		bytes.Repeat([]byte{0}, 1000),
	}
	for _, want := range cases {
		coded := ZeroCollapse(nil, want)
		got, err := ZeroExpand(nil, coded)
		if err != nil {
			t.Fatalf("%d zeros: %v", len(want), err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("round trip of %x gave %x", want, got)
		}
	}
}

func TestZeroCodeRandom(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 500; i++ {
		n := r.Intn(600)
		want := make([]byte, n)
		for j := range want {
			// Mostly zeros, so runs are common.
			if r.Intn(4) != 0 {
				want[j] = 0
			} else {
				want[j] = byte(r.Intn(255) + 1)
			}
		}
		got, err := ZeroExpand(nil, ZeroCollapse(nil, want))
		if err != nil {
			t.Fatalf("%v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("round trip differs at length %d", n)
		}
	}
}

// TestZeroExpandExtendedRun accepts the 256-per-zero-count-byte form
// the C client emits and understands, which ZeroCollapse never
// produces itself.
func TestZeroExpandExtendedRun(t *testing.T) {
	// 0x00 then a zero count (256) then 5: 256 + 5 zeros.
	got, err := ZeroExpand(nil, []byte{0x00, 0x00, 0x05, 0x42})
	if err != nil {
		t.Fatal(err)
	}
	want := append(bytes.Repeat([]byte{0}, 261), 0x42)
	if !bytes.Equal(got, want) {
		t.Errorf("got %d bytes, want %d", len(got), len(want))
	}
}

func TestZeroExpandTruncated(t *testing.T) {
	if _, err := ZeroExpand(nil, []byte{1, 2, 0}); err == nil {
		t.Error("expected an error when a run has no count byte")
	}
	if _, err := ZeroExpand(nil, []byte{0, 0}); err == nil {
		t.Error("expected an error when the count bytes run out")
	}
}

// TestWholePacket assembles and takes apart a packet the way a client
// would, exercising header, acks, zero coding, framing and body.
func TestWholePacket(t *testing.T) {
	m := &UseCircuitCode{}
	m.CircuitCode.Code = 690139535
	m.CircuitCode.SessionID = MustParseUUID("8d1b7e57-7e57-c0de-f4f4-19d29d124acf")
	m.CircuitCode.ID = MustParseUUID("876e7e57-7e57-c0de-9eeb-1bd0e1ec6995")

	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	payload := AppendID(nil, m.MsgInfo().ID)
	payload = append(payload, body...)

	h := Header{Flags: FlagReliable | FlagZerocoded, Sequence: 42}
	packet := AppendHeader(nil, &h)
	packet = ZeroCollapse(packet, payload)

	// ... and back.
	gotHdr, n, err := DecodeHeader(packet)
	if err != nil {
		t.Fatal(err)
	}
	rest := packet[n:]
	if gotHdr.HasAcks() {
		rest, _, err = SplitAcks(rest)
		if err != nil {
			t.Fatal(err)
		}
	}
	if gotHdr.Zerocoded() {
		rest, err = ZeroExpand(nil, rest)
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := DecodeBody(rest)
	if err != nil {
		t.Fatal(err)
	}
	ucc, ok := got.(*UseCircuitCode)
	if !ok {
		t.Fatalf("got %T", got)
	}
	if ucc.CircuitCode.Code != m.CircuitCode.Code ||
		ucc.CircuitCode.SessionID != m.CircuitCode.SessionID ||
		ucc.CircuitCode.ID != m.CircuitCode.ID {
		t.Errorf("have %+v want %+v", ucc.CircuitCode, m.CircuitCode)
	}
	if gotHdr.Sequence != 42 {
		t.Errorf("sequence = %d", gotHdr.Sequence)
	}
}

func TestUUIDStringRoundTrip(t *testing.T) {
	const s = "876e7e57-7e57-c0de-9eeb-1bd0e1ec6995"
	u, err := ParseUUID(s)
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != s {
		t.Errorf("%q round tripped to %q", s, u.String())
	}
	if u.IsZero() {
		t.Error("not the nil UUID")
	}
	if !(UUID{}).IsZero() {
		t.Error("the zero value should be the nil UUID")
	}
	for _, bad := range []string{"", "nope", "876e7e577e57c0de9eeb1bd0e1ec6995", "zz8d79aa-75be-47c0-8e28-26040267ec6d"} {
		if _, err := ParseUUID(bad); err == nil {
			t.Errorf("ParseUUID(%q) should fail", bad)
		}
	}
}

func TestIDString(t *testing.T) {
	if got := infoChatFromViewer.ID.String(); got != "ChatFromViewer" {
		t.Errorf("ID.String() = %q", got)
	}
	if got := MakeID(FreqHigh, 253).String(); got != "High(253)" {
		t.Errorf("unknown ID.String() = %q", got)
	}
}
