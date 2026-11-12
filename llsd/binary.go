package llsd

// The binary serialization, as LLSDSerialize::toBinary writes it: no
// "<? LLSD/Binary ?>" line in front, one type byte per value, and
// lengths and numbers big-endian.  A mesh asset is made of it -- its
// header, and every section once inflated.
//
// Map keys are written sorted, as the viewer's std::map writes them, so
// that the same value always makes the same bytes.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

// EncodeBinary writes v in LLSD's binary form.  It takes the same
// types Encode does, and float32 besides.
func EncodeBinary(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := binaryValue(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func binaryLen(b *bytes.Buffer, tag byte, n int) {
	b.WriteByte(tag)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(n))
	b.Write(l[:])
}

func binaryValue(b *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteByte('!')
	case bool:
		if t {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	case int:
		if t < math.MinInt32 || t > math.MaxInt32 {
			return fmt.Errorf("llsd: %d does not fit an LLSD integer", t)
		}
		binaryLen(b, 'i', int(int32(t)))
	case int64:
		if t < math.MinInt32 || t > math.MaxInt32 {
			return fmt.Errorf("llsd: %d does not fit an LLSD integer", t)
		}
		binaryLen(b, 'i', int(int32(t)))
	case float32:
		return binaryValue(b, float64(t))
	case float64:
		b.WriteByte('r')
		var r [8]byte
		binary.BigEndian.PutUint64(r[:], math.Float64bits(t))
		b.Write(r[:])
	case string:
		binaryLen(b, 's', len(t))
		b.WriteString(t)
	case UUID:
		id, err := parseUUID(string(t))
		if err != nil {
			return err
		}
		b.WriteByte('u')
		b.Write(id[:])
	case []byte:
		binaryLen(b, 'b', len(t))
		b.Write(t)
	case []string:
		binaryLen(b, '[', len(t))
		for _, e := range t {
			binaryLen(b, 's', len(e))
			b.WriteString(e)
		}
		b.WriteByte(']')
	case []float64:
		binaryLen(b, '[', len(t))
		for _, e := range t {
			if err := binaryValue(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case []any:
		binaryLen(b, '[', len(t))
		for _, e := range t {
			if err := binaryValue(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		binaryLen(b, '{', len(t))
		for _, k := range keys {
			binaryLen(b, 'k', len(k))
			b.WriteString(k)
			if err := binaryValue(b, t[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("llsd: cannot encode %T", v)
	}
	return nil
}

func parseUUID(s string) ([16]byte, error) {
	var id [16]byte
	h := make([]byte, 0, 32)
	for i := 0; i < len(s); i++ {
		if s[i] != '-' {
			h = append(h, s[i])
		}
	}
	if len(h) != 32 {
		return id, fmt.Errorf("llsd: %q is not a uuid", s)
	}
	for i := range id {
		hi, lo := h[2*i], h[2*i+1]
		if !isHex(hi) || !isHex(lo) {
			return id, fmt.Errorf("llsd: %q is not a uuid", s)
		}
		id[i] = hexVal(hi)<<4 | hexVal(lo)
	}
	return id, nil
}

// maxBinaryDepth bounds how deeply DecodeBinary nests, as the XML and
// notation readers do, so a hostile document cannot exhaust the stack.
const maxBinaryDepth = 64

// DecodeBinary reads one binary LLSD value from the front of b, and
// says how many bytes it took: a mesh asset's header is followed by
// the sections it describes, and their offsets count from its end.
//
// Values come back as Decode returns them: map[string]any, []any,
// string, UUID, []byte, bool, int64, float64 and nil.  A date or a
// URI comes back as the string it carries.
func DecodeBinary(b []byte) (any, int, error) {
	d := binReader{b: b}
	v, err := d.value(0)
	if err != nil {
		return nil, d.at, err
	}
	return v, d.at, nil
}

type binReader struct {
	b  []byte
	at int
}

func (d *binReader) take(n int) ([]byte, error) {
	if n < 0 || n > len(d.b)-d.at {
		return nil, fmt.Errorf("llsd: binary ends at byte %d, %d short", len(d.b), n-(len(d.b)-d.at))
	}
	p := d.b[d.at : d.at+n]
	d.at += n
	return p, nil
}

func (d *binReader) u32() (int, error) {
	p, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return int(binary.BigEndian.Uint32(p)), nil
}

func (d *binReader) sized() ([]byte, error) {
	n, err := d.u32()
	if err != nil {
		return nil, err
	}
	return d.take(n)
}

func (d *binReader) value(depth int) (any, error) {
	if depth > maxBinaryDepth {
		return nil, fmt.Errorf("llsd: binary nests deeper than %d", maxBinaryDepth)
	}
	tag, err := d.take(1)
	if err != nil {
		return nil, err
	}
	switch tag[0] {
	case '!':
		return nil, nil
	case '1':
		return true, nil
	case '0':
		return false, nil
	case 'i':
		p, err := d.take(4)
		if err != nil {
			return nil, err
		}
		return int64(int32(binary.BigEndian.Uint32(p))), nil
	case 'r':
		p, err := d.take(8)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(binary.BigEndian.Uint64(p)), nil
	case 'd':
		p, err := d.take(8)
		if err != nil {
			return nil, err
		}
		// A date is seconds since the epoch, little-endian -- the one
		// field the viewer writes in host order.
		return math.Float64frombits(binary.LittleEndian.Uint64(p)), nil
	case 'u':
		p, err := d.take(16)
		if err != nil {
			return nil, err
		}
		return UUID(fmt.Sprintf("%x-%x-%x-%x-%x", p[0:4], p[4:6], p[6:8], p[8:10], p[10:16])), nil
	case 's', 'l':
		p, err := d.sized()
		return string(p), err
	case 'b':
		p, err := d.sized()
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), p...), nil
	case '[':
		n, err := d.u32()
		if err != nil {
			return nil, err
		}
		if n > len(d.b)-d.at {
			return nil, fmt.Errorf("llsd: an array of %d cannot fit in %d bytes", n, len(d.b)-d.at)
		}
		a := make([]any, 0, n)
		for range n {
			v, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		if err := d.close(']'); err != nil {
			return nil, err
		}
		return a, nil
	case '{':
		n, err := d.u32()
		if err != nil {
			return nil, err
		}
		if n > len(d.b)-d.at {
			return nil, fmt.Errorf("llsd: a map of %d cannot fit in %d bytes", n, len(d.b)-d.at)
		}
		m := make(map[string]any, n)
		for range n {
			k, err := d.take(1)
			if err != nil {
				return nil, err
			}
			if k[0] != 'k' {
				return nil, fmt.Errorf("llsd: map key at byte %d is %q, not k", d.at-1, k[0])
			}
			key, err := d.sized()
			if err != nil {
				return nil, err
			}
			v, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			m[string(key)] = v
		}
		if err := d.close('}'); err != nil {
			return nil, err
		}
		return m, nil
	}
	return nil, fmt.Errorf("llsd: %q at byte %d is not a binary type", tag[0], d.at-1)
}

func (d *binReader) close(c byte) error {
	p, err := d.take(1)
	if err != nil {
		return err
	}
	if p[0] != c {
		return fmt.Errorf("llsd: expected %q at byte %d, found %q", c, d.at-1, p[0])
	}
	return nil
}
