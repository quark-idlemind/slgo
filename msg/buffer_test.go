package msg

import (
	"errors"
	"testing"
)

// The cursor is what stands between a short datagram and a panic.  Every
// read has to check before it slices, because these bytes came off the
// network and a simulator -- or something pretending to be one -- can
// send any length it likes.  A missed check here is a crash in the
// receive loop, which takes the whole session with it.

// TestCursorRefusesToReadPastTheEnd walks a body that stops one byte
// short of each width, which is the case a length check gets wrong.
func TestCursorRefusesToReadPastTheEnd(t *testing.T) {
	body := []byte{1, 2, 3, 4, 5, 6, 7, 8}

	cases := []struct {
		what string
		read func(*cur) error
		fits int // bytes the read needs
	}{
		{"u8", func(r *cur) error { _, err := r.u8(); return err }, 1},
		{"u16", func(r *cur) error { _, err := r.u16(); return err }, 2},
		{"u32", func(r *cur) error { _, err := r.u32(); return err }, 4},
		{"u64", func(r *cur) error { _, err := r.u64(); return err }, 8},
		{"f32", func(r *cur) error { _, err := r.f32(); return err }, 4},
		{"f64", func(r *cur) error { _, err := r.f64(); return err }, 8},
	}
	for _, c := range cases {
		short := c.fits - 1
		r := &cur{b: body[:short]}
		if err := c.read(r); !errors.Is(err, ErrShort) {
			t.Errorf("%s over %d bytes returned %v, want ErrShort", c.what, short, err)
		}
		if r.i != 0 {
			t.Errorf("%s advanced the cursor to %d after failing", c.what, r.i)
		}

		r = &cur{b: body[:c.fits]}
		if err := c.read(r); err != nil {
			t.Errorf("%s over exactly %d bytes: %v", c.what, c.fits, err)
		}
		if r.i != c.fits || !r.atEnd() {
			t.Errorf("%s left the cursor at %d of %d", c.what, r.i, c.fits)
		}
	}
}

// TestCursorRefusesANegativeLength: the count comes off the wire, and a
// Variable field with a four byte prefix can name a length that does not
// fit in an int on a 32 bit build.  Slicing by it would panic.
func TestCursorRefusesANegativeLength(t *testing.T) {
	r := &cur{b: []byte{1, 2, 3, 4}}
	if _, err := r.take(-1); !errors.Is(err, ErrShort) {
		t.Errorf("take(-1) returned %v, want ErrShort", err)
	}
	if r.remaining() != 4 {
		t.Errorf("the cursor moved: %d bytes left", r.remaining())
	}
	if _, err := r.cut(-1); !errors.Is(err, ErrShort) || r.remaining() != 4 {
		t.Errorf("cut(-1) returned %v with %d bytes left, want ErrShort and 4", err, r.remaining())
	}
	// On a lenient cursor it is more than there is, like any other.
	r.lenient = true
	if s, err := r.cut(-1); err != nil || len(s) != 4 || !r.padded {
		t.Errorf("lenient cut(-1) returned %d bytes, %v, padded %v; want the four there are", len(s), err, r.padded)
	}
}

// TestFieldErrorNamesTheFieldAndKeepsTheCause: being told only
// "truncated" about a five hundred byte packet is not worth much, and a
// caller filtering on ErrShort still has to be able to find it.
func TestFieldErrorNamesTheFieldAndKeepsTheCause(t *testing.T) {
	e := &fieldError{msg: "ChatFromViewer", block: "ChatData", field: "Message", err: ErrShort}

	const want = "msg: ChatFromViewer.ChatData.Message: msg: truncated message"
	if e.Error() != want {
		t.Errorf("Error() = %q, want %q", e.Error(), want)
	}
	if !errors.Is(e, ErrShort) {
		t.Error("errors.Is could not see through the field error to ErrShort")
	}
	if errors.Unwrap(e) != ErrShort {
		t.Errorf("Unwrap = %v", errors.Unwrap(e))
	}
}
