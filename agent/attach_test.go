package agent

import "testing"

// TestAttachPoint: the State byte carries the attachment point with its
// nibbles swapped, so point 35 arrives as 0x32.
func TestAttachPoint(t *testing.T) {
	for _, c := range []struct {
		state uint8
		want  int
	}{
		{0x00, 0},
		{0x32, 35}, // 0x32 -> 0x23 -> 35
		{0x40, 4},
		{0x42, 36}, // 36 is 0x24, and it arrives with the nibbles the other way
	} {
		if got := attachPoint(c.state); got != c.want {
			t.Errorf("attachPoint(%#02x) = %d, want %d", c.state, got, c.want)
		}
	}
}

// TestAttachItem: the inventory item is the only stable name a worn
// object has, and it comes out of the NameValue block.
func TestAttachItem(t *testing.T) {
	const id = "97da7e57-7e57-c0de-b787-9e0b48a226f1"

	nv := []byte("AttachItemID STRING RW DS " + id + "\n")
	got, ok := attachItem(nv)
	if !ok || got.String() != id {
		t.Errorf("got %v ok=%v, want %s", got, ok, id)
	}

	// Among other lines, and with the trailing NUL the wire carries.
	nv = []byte("FirstName STRING RW SV Example\nAttachItemID STRING RW DS " + id + "\nLastName STRING RW SV Resident\n\x00")
	if got, ok := attachItem(nv); !ok || got.String() != id {
		t.Errorf("among other lines: got %v ok=%v", got, ok)
	}

	// Not worn: no such line, an empty block, a zero id.
	for _, bad := range [][]byte{
		nil,
		[]byte(""),
		[]byte("FirstName STRING RW SV Example\n"),
		[]byte("AttachItemID STRING RW DS 00000000-0000-0000-0000-000000000000\n"),
		[]byte("AttachItemID STRING RW DS not-a-uuid\n"),
	} {
		if _, ok := attachItem(bad); ok {
			t.Errorf("%q should not look like an attachment", bad)
		}
	}
}
