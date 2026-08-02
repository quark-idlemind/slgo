package msg

import (
	"fmt"
	"reflect"
	"strconv"
	"unicode/utf8"
)

// Human and machine readable packet dumps, in YAML.
//
// This is the replacement for print_msg and the generated msgs.h in the
// C client, and it exists for the same reason: the only way anyone saw
// that the simulator answers AgentUpdate with a CameraConstraint was
// that unhandled messages got dumped.  Being able to diff two captures
// is worth more than being able to read one.
//
// The shape is fixed so it can be parsed:
//
//	message: ChatFromSimulator
//	id: {freq: Low, number: 139}
//	sequence: 1234
//	flags: [reliable, zerocoded]
//	acks: [42, 43]
//	blocks:
//	  ChatData:                     # a Single block is a mapping
//	    FromName: "Example Resident"
//	    Message: "hello"
//	  Packets:                      # Multiple and Variable are sequences
//	    - ID: 42
//	    - ID: 43
//
// Variable fields render as a quoted string when the bytes look like
// text, and as a quoted "0x..." hex string when they do not.  The
// template says which fields are which, so nothing is ambiguous.

// DumpMessage renders a message as YAML.
func DumpMessage(m Message) string {
	return string(AppendMessageYAML(nil, m))
}

// DumpPacket renders a received packet, header and all, as YAML.
func DumpPacket(p *Packet) string {
	return string(AppendPacketYAML(nil, p))
}

// AppendPacketYAML appends the YAML form of p to dst.
func AppendPacketYAML(dst []byte, p *Packet) []byte {
	if p.Addr != nil {
		dst = append(dst, "from: "...)
		dst = appendYAMLString(dst, p.Addr.String())
		dst = append(dst, '\n')
	}
	if !p.At.IsZero() {
		dst = append(dst, "at: "...)
		dst = appendYAMLString(dst, p.At.UTC().Format("2006-01-02T15:04:05.000000Z"))
		dst = append(dst, '\n')
	}
	dst = append(dst, "sequence: "...)
	dst = strconv.AppendUint(dst, uint64(p.Header.Sequence), 10)
	dst = append(dst, '\n')

	dst = append(dst, "flags: ["...)
	first := true
	for _, f := range []struct {
		bit  uint8
		name string
	}{
		{FlagZerocoded, "zerocoded"},
		{FlagReliable, "reliable"},
		{FlagResent, "resent"},
		{FlagAck, "ack"},
	} {
		if p.Header.Flags&f.bit == 0 {
			continue
		}
		if !first {
			dst = append(dst, ", "...)
		}
		dst = append(dst, f.name...)
		first = false
	}
	dst = append(dst, "]\n"...)

	if len(p.Header.Extra) > 0 {
		dst = append(dst, "extra: "...)
		dst = appendYAMLString(dst, "0x"+hexString(p.Header.Extra))
		dst = append(dst, '\n')
	}
	if len(p.Acks) > 0 {
		dst = append(dst, "acks: ["...)
		for i, a := range p.Acks {
			if i > 0 {
				dst = append(dst, ", "...)
			}
			dst = strconv.AppendUint(dst, uint64(a), 10)
		}
		dst = append(dst, "]\n"...)
	}
	if p.Err != nil {
		dst = append(dst, "error: "...)
		dst = appendYAMLString(dst, p.Err.Error())
		dst = append(dst, '\n')
		if len(p.Body) > 0 {
			dst = append(dst, "body: "...)
			dst = appendYAMLString(dst, "0x"+hexString(p.Body))
			dst = append(dst, '\n')
		}
	}
	if p.Message != nil {
		dst = AppendMessageYAML(dst, p.Message)
	}
	return dst
}

// AppendMessageYAML appends the YAML form of m to dst.
func AppendMessageYAML(dst []byte, m Message) []byte {
	info := m.MsgInfo()

	dst = append(dst, "message: "...)
	dst = append(dst, info.Name...)
	dst = append(dst, '\n')

	dst = append(dst, "id: {freq: "...)
	dst = append(dst, info.ID.Freq().String()...)
	dst = append(dst, ", number: "...)
	dst = strconv.AppendUint(dst, uint64(info.ID.Number()), 10)
	dst = append(dst, "}\n"...)

	v := reflect.ValueOf(m)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return append(dst, "blocks: {}\n"...)
	}
	v = v.Elem()
	p, err := planFor(v.Type(), info.Name)
	if err != nil {
		dst = append(dst, "error: "...)
		dst = appendYAMLString(dst, err.Error())
		return append(dst, '\n')
	}
	if len(p.blocks) == 0 {
		return append(dst, "blocks: {}\n"...)
	}

	dst = append(dst, "blocks:\n"...)
	for bi := range p.blocks {
		b := &p.blocks[bi]
		bv := v.Field(b.index)

		dst = append(dst, "  "...)
		dst = append(dst, b.name...)
		dst = append(dst, ":"...)

		switch b.quant {
		case qSingle:
			dst = append(dst, '\n')
			dst = appendBlockYAML(dst, bv, b, "    ", false)
		default:
			n := bv.Len()
			if n == 0 {
				dst = append(dst, " []\n"...)
				continue
			}
			dst = append(dst, '\n')
			for j := 0; j < n; j++ {
				dst = append(dst, "    - "...)
				dst = appendBlockYAML(dst, bv.Index(j), b, "      ", true)
			}
		}
	}
	return dst
}

// appendBlockYAML writes one block's fields, one per line at indent.
//
// inline says the caller has already positioned the cursor for the
// first field -- it wrote "- " for a sequence entry -- so that one goes
// on the current line and the rest line up under it.
func appendBlockYAML(dst []byte, bv reflect.Value, b *blockPlan, indent string, inline bool) []byte {
	if len(b.fields) == 0 {
		if !inline {
			dst = append(dst, indent...)
		}
		return append(dst, "{}\n"...)
	}
	for fi := range b.fields {
		f := &b.fields[fi]
		if fi > 0 || !inline {
			dst = append(dst, indent...)
		}
		dst = append(dst, f.name...)
		dst = append(dst, ": "...)
		dst = appendFieldYAML(dst, bv.Field(f.index), f)
		dst = append(dst, '\n')
	}
	return dst
}

func appendFieldYAML(dst []byte, v reflect.Value, f *fieldPlan) []byte {
	switch f.kind {
	case kU8, kU16, kU32, kU64:
		return strconv.AppendUint(dst, v.Uint(), 10)
	case kS8, kS16, kS32, kS64:
		return strconv.AppendInt(dst, v.Int(), 10)
	case kF32:
		return strconv.AppendFloat(dst, v.Float(), 'g', -1, 32)
	case kF64:
		return strconv.AppendFloat(dst, v.Float(), 'g', -1, 64)
	case kBool:
		return strconv.AppendBool(dst, v.Bool())
	case kUUID:
		var u UUID
		copy(u[:], arrayBytes(v))
		return append(dst, u.String()...)
	case kIPAddr:
		var a IPAddr
		copy(a[:], arrayBytes(v))
		return append(dst, a.String()...)
	case kIPPort:
		return strconv.AppendUint(dst, v.Uint(), 10)
	case kVec3:
		return appendFloatsYAML(dst, v, 3, []string{"x", "y", "z"}, 32)
	case kVec3d:
		return appendFloatsYAML(dst, v, 3, []string{"x", "y", "z"}, 64)
	case kVec4:
		return appendFloatsYAML(dst, v, 4, []string{"x", "y", "z", "w"}, 32)
	case kQuat:
		// Show the recovered W too: it is not on the wire, but it
		// is what the value means.
		q := Quaternion{
			X: float32(v.Field(0).Float()),
			Y: float32(v.Field(1).Float()),
			Z: float32(v.Field(2).Float()),
		}
		dst = append(dst, "{x: "...)
		dst = strconv.AppendFloat(dst, float64(q.X), 'g', -1, 32)
		dst = append(dst, ", y: "...)
		dst = strconv.AppendFloat(dst, float64(q.Y), 'g', -1, 32)
		dst = append(dst, ", z: "...)
		dst = strconv.AppendFloat(dst, float64(q.Z), 'g', -1, 32)
		dst = append(dst, ", w: "...)
		dst = strconv.AppendFloat(dst, float64(q.W()), 'g', -1, 32)
		return append(dst, '}')
	case kFixed:
		return appendYAMLString(dst, "0x"+hexString(arrayBytes(v)))
	case kVariable:
		return appendBytesYAML(dst, v.Bytes())
	}
	return append(dst, "null"...)
}

func appendFloatsYAML(dst []byte, v reflect.Value, n int, names []string, bits int) []byte {
	dst = append(dst, '{')
	for i := 0; i < n; i++ {
		if i > 0 {
			dst = append(dst, ", "...)
		}
		dst = append(dst, names[i]...)
		dst = append(dst, ": "...)
		dst = strconv.AppendFloat(dst, v.Field(i).Float(), 'g', -1, bits)
	}
	return append(dst, '}')
}

// appendBytesYAML renders a Variable field as text when it looks like
// text and as hex when it does not.  Most of them really are strings:
// queueString1 in the C client writes a NUL terminated one.
func appendBytesYAML(dst []byte, b []byte) []byte {
	if len(b) == 0 {
		return append(dst, `""`...)
	}
	t := b
	if t[len(t)-1] == 0 {
		t = t[:len(t)-1] // drop the C style terminator
	}
	if isText(t) {
		return appendYAMLString(dst, string(t))
	}
	return appendYAMLString(dst, "0x"+hexString(b))
}

func isText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r == '\n' || r == '\t' || r == '\r' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

const hexDigits = "0123456789abcdef"

func hexString(b []byte) string {
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexDigits[c>>4], hexDigits[c&0xf])
	}
	return string(out)
}

// appendYAMLString writes a double quoted YAML scalar.  Always quoting
// avoids every question about whether a bare value would be read back
// as a number, a boolean or null.
func appendYAMLString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for _, r := range s {
		switch r {
		case '"':
			dst = append(dst, '\\', '"')
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			if r < 0x20 || r == 0x7f {
				dst = append(dst, fmt.Sprintf(`\x%02x`, r)...)
			} else {
				dst = utf8.AppendRune(dst, r)
			}
		}
	}
	return append(dst, '"')
}
