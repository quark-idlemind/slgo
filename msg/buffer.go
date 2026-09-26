package msg

import (
	"encoding/binary"
	"fmt"
	"math"
)

// buf is an append-only writer.  It never fails: the caller sizes
// nothing up front and Go grows the slice.
type buf struct {
	b []byte
}

func (w *buf) u8(v uint8)   { w.b = append(w.b, v) }
func (w *buf) u16(v uint16) { w.b = binary.LittleEndian.AppendUint16(w.b, v) }
func (w *buf) u32(v uint32) { w.b = binary.LittleEndian.AppendUint32(w.b, v) }
func (w *buf) u64(v uint64) { w.b = binary.LittleEndian.AppendUint64(w.b, v) }

func (w *buf) f32(v float32) { w.u32(math.Float32bits(v)) }
func (w *buf) f64(v float64) { w.u64(math.Float64bits(v)) }

func (w *buf) bytes(v []byte) { w.b = append(w.b, v...) }

// cur is a reading cursor over a message body.
//
// A lenient cursor reads a width past the end as zeros, and cuts a
// payload at the end (see cut), rather than failing, and says that it
// did either in padded.  See Unmarshal for when that is right.
type cur struct {
	b       []byte
	i       int
	lenient bool
	padded  bool
}

func (r *cur) remaining() int { return len(r.b) - r.i }
func (r *cur) atEnd() bool    { return r.i >= len(r.b) }

func (r *cur) take(n int) ([]byte, error) {
	if n < 0 {
		return nil, ErrShort
	}
	if r.remaining() < n {
		if !r.lenient {
			return nil, ErrShort
		}
		s := make([]byte, n)
		copy(s, r.b[min(r.i, len(r.b)):])
		r.i = len(r.b)
		r.padded = true
		return s, nil
	}
	s := r.b[r.i : r.i+n]
	r.i += n
	return s, nil
}

// cut takes n bytes, or on a lenient cursor as many of them as there
// are.  It is for a Variable field's payload, which is cut at the end
// of the body and never padded: its length is only what the packet
// claims.  A negative n, a four byte length on a 32 bit build, is more
// than there is.
func (r *cur) cut(n int) ([]byte, error) {
	if n < 0 || r.remaining() < n {
		if !r.lenient {
			return nil, ErrShort
		}
		n = r.remaining()
		r.padded = true
	}
	s := r.b[r.i : r.i+n]
	r.i += n
	return s, nil
}

func (r *cur) u8() (uint8, error) {
	s, err := r.take(1)
	if err != nil {
		return 0, err
	}
	return s[0], nil
}

func (r *cur) u16() (uint16, error) {
	s, err := r.take(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(s), nil
}

func (r *cur) u32() (uint32, error) {
	s, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(s), nil
}

func (r *cur) u64() (uint64, error) {
	s, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(s), nil
}

func (r *cur) f32() (float32, error) {
	v, err := r.u32()
	return math.Float32frombits(v), err
}

func (r *cur) f64() (float64, error) {
	v, err := r.u64()
	return math.Float64frombits(v), err
}

// fieldError names the field that went wrong.  Decoding a 500 byte
// packet and being told only "truncated" is not worth much.
type fieldError struct {
	msg, block, field string
	err               error
}

func (e *fieldError) Error() string {
	return fmt.Sprintf("msg: %s.%s.%s: %v", e.msg, e.block, e.field, e.err)
}

func (e *fieldError) Unwrap() error { return e.err }
