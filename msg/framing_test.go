package msg

import (
	"bytes"
	"math/rand"
	"strings"
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

// TestZeroExpandStopsAtMaxPacketSize: a body expands to at most the
// viewer's buffer, and one byte more is refused, whether that byte is
// the end of a run or a byte of its own.  Why: doc/wire.md#zero-expansion
func TestZeroExpandStopsAtMaxPacketSize(t *testing.T) {
	full := bytes.Repeat([]byte{0}, MaxPacketSize)
	got, err := ZeroExpand(nil, ZeroCollapse(nil, full))
	if err != nil || len(got) != MaxPacketSize {
		t.Fatalf("%d zeros expanded to %d bytes, %v", MaxPacketSize, len(got), err)
	}
	// The ceiling is on what is appended, not on dst.
	got, err = ZeroExpand([]byte{9, 9, 9}, ZeroCollapse(nil, full))
	if err != nil || len(got) != 3+MaxPacketSize {
		t.Fatalf("after three bytes, %d zeros expanded to %d bytes, %v", MaxPacketSize, len(got), err)
	}

	over := map[string][]byte{
		"a run one too long":      append(bytes.Repeat([]byte{0}, MaxPacketSize), 0),
		"a byte after a full run": append(bytes.Repeat([]byte{0}, MaxPacketSize), 7),
		"no zeros at all":         bytes.Repeat([]byte{7}, MaxPacketSize+1),
	}
	for what, body := range over {
		if got, err := ZeroExpand(nil, ZeroCollapse(nil, body)); err == nil {
			t.Errorf("%s: expanded to %d bytes", what, len(got))
		}
	}

	// 257 bytes: a zero, 255 counts of 256, and 255 -- 65,535 zeros.
	bomb := append([]byte{0}, bytes.Repeat([]byte{0}, 255)...)
	bomb = append(bomb, 255)
	if got, err := ZeroExpand(nil, bomb); err == nil {
		t.Errorf("%d bytes expanded to %d", len(bomb), len(got))
	}
}

// TestWholePacket assembles and takes apart a packet the way a client
// would, exercising header, acks, zero coding, framing and body.
func TestWholePacket(t *testing.T) {
	m := &UseCircuitCode{}
	m.CircuitCode.Code = 690139535
	m.CircuitCode.SessionID = MustParseUUID("8d1b7e57-7e57-c0de-3bf6-2277c65663be")
	m.CircuitCode.ID = MustParseUUID("876e7e57-7e57-c0de-8597-66b760a8cb5f")

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
	const s = "876e7e57-7e57-c0de-8597-66b760a8cb5f"
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
	for _, bad := range []string{"", "nope", strings.ReplaceAll(s, "-", ""), "zz" + s[2:]} {
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

// TestQuaternionPacking follows LLQuaternion::packToVector3 and
// unpackFromVector3.
func TestQuaternionPacking(t *testing.T) {
	// The zero value is the identity rotation.
	if w := (Quaternion{}).W(); w != 1 {
		t.Errorf("identity W() = %v, want 1", w)
	}

	const eps = 1e-6
	cases := []struct{ x, y, z, w float32 }{
		{0, 0, 0, 1},                 // identity
		{0, 0, 0.7071068, 0.7071068}, // 90 degrees about Z
		{0.5, 0.5, 0.5, 0.5},
		{0, 0.7071068, 0, -0.7071068}, // negative W
		{-0.5, 0.5, -0.5, -0.5},
	}
	for _, c := range cases {
		q := PackQuaternion(c.x, c.y, c.z, c.w)
		if q.W() < 0 {
			t.Errorf("%v: recovered W is negative", c)
		}
		// q and -q are the same rotation, so compare after
		// normalising the sign of the input the way LL does.
		wx, wy, wz, ww := c.x, c.y, c.z, c.w
		if ww < 0 {
			wx, wy, wz, ww = -wx, -wy, -wz, -ww
		}
		if absf(q.X-wx) > eps || absf(q.Y-wy) > eps || absf(q.Z-wz) > eps {
			t.Errorf("%v packed to %v", c, q)
		}
		if absf(q.W()-ww) > eps {
			t.Errorf("%v recovered W = %v, want %v", c, q.W(), ww)
		}
	}
}

// TestQuaternionNormalizes checks that a non-unit input is scaled, as
// packToVector3 does before packing.
func TestQuaternionNormalizes(t *testing.T) {
	q := PackQuaternion(0, 0, 2, 2) // magnitude 2*sqrt(2)
	want := float32(0.7071068)
	if absf(q.Z-want) > 1e-6 {
		t.Errorf("Z = %v, want %v", q.Z, want)
	}
	if absf(q.W()-want) > 1e-6 {
		t.Errorf("W() = %v, want %v", q.W(), want)
	}
}

// TestQuaternionDegenerate must not divide by zero.
func TestQuaternionDegenerate(t *testing.T) {
	q := PackQuaternion(0, 0, 0, 0)
	if q != (Quaternion{}) {
		t.Errorf("degenerate quaternion packed to %v", q)
	}
	// An over-long vector part cannot yield a real W; clamp to 0.
	if w := (Quaternion{1, 1, 1}).W(); w != 0 {
		t.Errorf("over-long W() = %v, want 0", w)
	}
}

func absf(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}

// TestMustParseUUIDPanics: it is for constants and tests, where a bad
// UUID is a typo in the source and should not survive to run time.
func TestMustParseUUIDPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustParseUUID accepted something that is not a UUID")
		}
	}()
	MustParseUUID("not a uuid")
}

// TestFreqNamesItself, including a value MakeID cannot produce: ID
// carries the class in its top byte, and an ID built by hand or read
// out of a capture can hold anything.  A number there is more use in a
// log line than an empty string.
func TestFreqNamesItself(t *testing.T) {
	for f, want := range map[Freq]string{
		FreqHigh:   "High",
		FreqMedium: "Medium",
		FreqLow:    "Low",
		FreqFixed:  "Fixed",
	} {
		if got := f.String(); got != want {
			t.Errorf("Freq(%d) = %q, want %q", uint8(f), got, want)
		}
	}
	if got := Freq(9).String(); !strings.Contains(got, "9") {
		t.Errorf("an unknown Freq printed as %q, which does not say which", got)
	}
}

// TestRegisterRefusesADuplicate: the generated file registers every
// message once, and a template change that gave two of them the same
// number would otherwise leave one silently unreachable.
func TestRegisterRefusesADuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering a second message under an existing number was allowed")
		}
	}()
	// PacketAck's number, under another name.  register panics before
	// it writes anything, so the registry is left as it was.
	register(&Info{Name: "NotPacketAck", ID: LookupName("PacketAck").ID}, func() Message {
		return &PacketAck{}
	})
}

// TestNamesCoversTheTemplate.  Names is what msggen's own checks and
// the shell's completion read, so it has to be the whole registry.
func TestNamesCoversTheTemplate(t *testing.T) {
	names := Names()
	if len(names) != len(infoByID) {
		t.Errorf("Names returned %d of %d messages", len(names), len(infoByID))
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("%s appears twice", n)
		}
		seen[n] = true
	}
	if !seen["ChatFromViewer"] || !seen["PacketAck"] {
		t.Error("Names is missing messages that are certainly in the template")
	}
}

// TestDecodeBodyRefusesWhatItCannotRead: an empty datagram body has no
// message number in it, and a message number followed by too few bytes
// is a truncated packet rather than a message with zeroes in it.
func TestDecodeBodyRefusesWhatItCannotRead(t *testing.T) {
	if m, err := DecodeBody(nil); err == nil {
		t.Errorf("an empty body decoded to %T", m)
	}
	// UseCircuitCode is Low 3 and wants 36 bytes; it gets two.
	short := AppendID(nil, LookupName("UseCircuitCode").ID)
	short = append(short, 0x01, 0x02)
	if m, err := DecodeBody(short); err == nil {
		t.Errorf("a truncated UseCircuitCode decoded to %+v", m)
	}
}
