package msg

import (
	"bytes"
	"strings"
	"testing"
)

// Raw exists so that a message this build does not understand can still
// be moved: slgod relays to its clients, and a capture is replayed
// without ever being decoded.  What has to hold is that the bytes come
// out the way they went in, and that an unknown number still gets a name
// good enough to appear in a log.

// TestRawKeepsTheBytesItWasGiven, which is the entire contract for a
// relay: nothing in the middle may alter the body.
func TestRawKeepsTheBytesItWasGiven(t *testing.T) {
	id := LookupName("ChatFromSimulator").ID
	body := []byte{0x04, 'z', 'o', 'r', 'k', 0x01, 0x02}

	r := NewRaw(id, body)
	if !r.Known() {
		t.Error("a message in the template should be Known")
	}
	if r.MsgInfo().Name != "ChatFromSimulator" || r.MsgInfo().ID != id {
		t.Errorf("info = %+v", r.MsgInfo())
	}
	out, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, body) {
		t.Errorf("Encode = %x, want %x", out, body)
	}
}

// TestRawNamesAMessageItHasNeverHeardOf: the template gains messages,
// and a relay that refused to name an unknown one would leave whoever
// reads the log with a number and nothing else.
func TestRawNamesAMessageItHasNeverHeardOf(t *testing.T) {
	// High 253 is not in the template and has not been for as long as
	// the template has existed.
	id := MakeID(FreqHigh, 253)
	r := NewRaw(id, nil)

	if r.Known() {
		t.Error("High 253 should not be Known")
	}
	if r.MsgInfo().ID != id {
		t.Errorf("id = %v, want %v", r.MsgInfo().ID, id)
	}
	name := r.MsgInfo().Name
	if !strings.Contains(name, "High") || !strings.Contains(name, "253") {
		t.Errorf("name = %q, want something naming High 253", name)
	}
}

// TestRawDecodeCopies: the receiver reuses one read buffer forever, so a
// Raw that pointed into it would be rewritten by the next datagram.
func TestRawDecodeCopies(t *testing.T) {
	src := []byte{1, 2, 3, 4}
	var r Raw
	if err := r.Decode(src); err != nil {
		t.Fatal(err)
	}
	src[0] = 0xff
	if r.Body[0] != 1 {
		t.Errorf("Decode aliased its argument: Body = %x", r.Body)
	}
}
