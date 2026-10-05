// Package llsdbin reads and writes LLSD in its binary form, and the
// zlib wrapper that a few capabilities put round it.
//
// The binary form is what the viewer's LLSDSerialize::toBinary writes
// with no header: one type byte, then the value, big endian throughout.
//
//	!                    undef
//	1  0                 true, false
//	i  int32             integer
//	r  float64           real
//	u  16 bytes          uuid
//	s  int32, bytes      string
//	l  int32, bytes      uri
//	b  int32, bytes      binary
//	d  float64           date, in seconds since 1970
//	[  int32 n, n values, ]
//	{  int32 n, n of (k, int32, key bytes, value), }
//
// Values decode to plain Go values, as package llsd's do, except that
// the types the XML form cannot tell apart stay apart here:
//
//	undef    nil           integer  int64
//	boolean  bool          real     float64
//	string   string        uuid     UUID
//	uri      URI           date     Date
//	binary   []byte        array    []any
//	map      map[string]any
//
// This package is internal and is not promised.  Its one caller is the
// RenderMaterials capability (sl.Session.Materials), the only place
// this tree meets the binary form.
// Why: doc/materials.md#the-capability
package llsdbin

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
)

// UUID is a uuid, as the sixteen bytes that travel.
type UUID [16]byte

// URI is a uri, which is a string that says it is one.
type URI string

// Date is a date as the wire has it: seconds since 1970-01-01 UTC.
type Date float64

// maxDepth is how deeply arrays and maps may nest.  The viewer's own
// parser has a limit for the same reason: a hand-built answer nested
// a million deep would otherwise take the stack with it.
const maxDepth = 64

// Encode writes v, which is one of the Go types in the package comment
// and also int, int32, uint8 and float32 for convenience.  A map's keys
// are written in order, so the same value is always the same bytes.
func Encode(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := encode(&b, v, 0); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func u32(b *bytes.Buffer, n int) {
	var x [4]byte
	binary.BigEndian.PutUint32(x[:], uint32(n))
	b.Write(x[:])
}

func f64(b *bytes.Buffer, f float64) {
	var x [8]byte
	binary.BigEndian.PutUint64(x[:], math.Float64bits(f))
	b.Write(x[:])
}

func sized(b *bytes.Buffer, t byte, p []byte) {
	b.WriteByte(t)
	u32(b, len(p))
	b.Write(p)
}

func integer(b *bytes.Buffer, n int64) error {
	if n < math.MinInt32 || n > math.MaxInt32 {
		return fmt.Errorf("llsdbin: %d does not fit an integer, which is 32 bits", n)
	}
	b.WriteByte('i')
	u32(b, int(int32(n)))
	return nil
}

func encode(b *bytes.Buffer, v any, depth int) error {
	if depth > maxDepth {
		return errors.New("llsdbin: nested too deeply")
	}
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
		return integer(b, int64(t))
	case int32:
		return integer(b, int64(t))
	case int64:
		return integer(b, t)
	case uint8:
		return integer(b, int64(t))
	case float32:
		b.WriteByte('r')
		f64(b, float64(t))
	case float64:
		b.WriteByte('r')
		f64(b, t)
	case UUID:
		b.WriteByte('u')
		b.Write(t[:])
	case string:
		sized(b, 's', []byte(t))
	case URI:
		sized(b, 'l', []byte(t))
	case []byte:
		sized(b, 'b', t)
	case Date:
		b.WriteByte('d')
		f64(b, float64(t))
	case []any:
		b.WriteByte('[')
		u32(b, len(t))
		for _, e := range t {
			if err := encode(b, e, depth+1); err != nil {
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
		b.WriteByte('{')
		u32(b, len(t))
		for _, k := range keys {
			sized(b, 'k', []byte(k))
			if err := encode(b, t[k], depth+1); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("llsdbin: cannot encode %T", v)
	}
	return nil
}

// Decode reads one value, which must be all of b.
func Decode(b []byte) (any, error) {
	r := &reader{b: b}
	v, err := r.value(0)
	if err != nil {
		return nil, err
	}
	if len(r.b) > 0 {
		return nil, fmt.Errorf("llsdbin: %d bytes after the value", len(r.b))
	}
	return v, nil
}

var errShort = errors.New("llsdbin: the data ends inside a value")

type reader struct{ b []byte }

func (r *reader) take(n int) ([]byte, error) {
	if n < 0 || n > len(r.b) {
		return nil, errShort
	}
	p := r.b[:n]
	r.b = r.b[n:]
	return p, nil
}

func (r *reader) int32() (int, error) {
	p, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return int(int32(binary.BigEndian.Uint32(p))), nil
}

func (r *reader) float() (float64, error) {
	p, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(binary.BigEndian.Uint64(p)), nil
}

// sized reads a length and that many bytes.
func (r *reader) sized() ([]byte, error) {
	n, err := r.int32()
	if err != nil {
		return nil, err
	}
	return r.take(n)
}

// count reads how many elements follow, refusing one that the rest of
// the data could not hold: every element takes a byte at least, so a
// count past the bytes left is a lie, and is not allocated for.
func (r *reader) count() (int, error) {
	n, err := r.int32()
	if err != nil {
		return 0, err
	}
	if n < 0 || n > len(r.b) {
		return 0, errShort
	}
	return n, nil
}

func (r *reader) closer(c byte) error {
	p, err := r.take(1)
	if err != nil {
		return err
	}
	if p[0] != c {
		return fmt.Errorf("llsdbin: wanted %q at the end, found %q", c, p[0])
	}
	return nil
}

func (r *reader) value(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("llsdbin: nested too deeply")
	}
	p, err := r.take(1)
	if err != nil {
		return nil, err
	}
	switch p[0] {
	case '!':
		return nil, nil
	case '1':
		return true, nil
	case '0':
		return false, nil
	case 'i':
		n, err := r.int32()
		return int64(n), err
	case 'r':
		return r.float()
	case 'd':
		f, err := r.float()
		return Date(f), err
	case 'u':
		q, err := r.take(16)
		if err != nil {
			return nil, err
		}
		return UUID(q), nil
	case 's', 'l', 'b':
		q, err := r.sized()
		if err != nil {
			return nil, err
		}
		switch p[0] {
		case 's':
			return string(q), nil
		case 'l':
			return URI(q), nil
		}
		return append([]byte(nil), q...), nil
	case '[':
		n, err := r.count()
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, n)
		for range n {
			e, err := r.value(depth + 1)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
		return out, r.closer(']')
	case '{':
		n, err := r.count()
		if err != nil {
			return nil, err
		}
		out := make(map[string]any, n)
		for range n {
			if err := r.closer('k'); err != nil {
				return nil, err
			}
			k, err := r.sized()
			if err != nil {
				return nil, err
			}
			e, err := r.value(depth + 1)
			if err != nil {
				return nil, err
			}
			out[string(k)] = e
		}
		return out, r.closer('}')
	}
	return nil, fmt.Errorf("llsdbin: %q is not a type", p[0])
}

// Zip compresses b as zlib, which is what the capabilities that carry
// binary LLSD in a <binary> wrap it in.
func Zip(b []byte) ([]byte, error) {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return z.Bytes(), nil
}

// MaxUnzipped is the most Unzip will inflate.  An answer is a few
// kilobytes for a handful of materials and a region's every material
// was 131 kB; a limit far above that stops a few bytes of zlib
// becoming gigabytes without refusing anything real.
const MaxUnzipped = 64 << 20

// Unzip is Zip undone, refusing more than MaxUnzipped.
func Unzip(z []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(z))
	if err != nil {
		return nil, fmt.Errorf("llsdbin: not zlib: %w", err)
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, MaxUnzipped+1))
	if err != nil {
		return nil, fmt.Errorf("llsdbin: zlib: %w", err)
	}
	if len(b) > MaxUnzipped {
		return nil, fmt.Errorf("llsdbin: more than %d bytes inflated", MaxUnzipped)
	}
	return b, nil
}

// DecodeZipped is Unzip and then Decode: what a capability's <binary>
// holds.
func DecodeZipped(z []byte) (any, error) {
	b, err := Unzip(z)
	if err != nil {
		return nil, err
	}
	return Decode(b)
}

// EncodeZipped is Encode and then Zip.
func EncodeZipped(v any) ([]byte, error) {
	b, err := Encode(v)
	if err != nil {
		return nil, err
	}
	return Zip(b)
}
