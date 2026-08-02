package msg

import "testing"

// A terse update captured from the test region, for a boat known to be at
// roughly <65.8, 220.8, 20.0>.  Forty-four bytes, not an avatar.
//
// This is here because the position was first decoded as quantised
// sixteen bit values, the way everything after it is, and the result
// was plausible nonsense: in range, stable, and about 130 metres from
// where the object actually was.  Keeping a real blob whose answer is
// known independently is what catches that.
var terseBoat = []byte{
	0x60, 0xb9, 0xbe, 0x36, // local 918468960
	0x00,                   // state
	0x00,                   // not an avatar
	0x0e, 0x81, 0x83, 0x42, // position X
	0x91, 0xd5, 0x5c, 0x43, // position Y
	0x3b, 0x87, 0x9f, 0x41, // position Z
	0xff, 0x7f, 0xff, 0x7f, 0xff, 0x7f, // velocity
	0xff, 0x7f, 0xff, 0x7f, 0xff, 0x7f, // acceleration
	0xff, 0x7f, 0xff, 0x7f, 0x75, 0x02, 0xf8, 0x98, // rotation
	0xff, 0x7f, 0xff, 0x7f, 0xff, 0x7f, // angular velocity
}

func TestDecodeTerse(t *testing.T) {
	if len(terseBoat) != 44 {
		t.Fatalf("the capture is %d bytes, the simulator sent 44", len(terseBoat))
	}
	d, err := DecodeTerse(terseBoat)
	if err != nil {
		t.Fatal(err)
	}
	if d.LocalID != 918468960 {
		t.Errorf("LocalID = %d, want 918468960", d.LocalID)
	}
	if d.Avatar {
		t.Error("read as an avatar")
	}

	// Where the object is, checked against what ObjectUpdate and the
	// region survey both said independently.
	near := func(got, want float32) bool { return got > want-0.5 && got < want+0.5 }
	if !near(d.Position.X, 65.75) || !near(d.Position.Y, 220.84) || !near(d.Position.Z, 19.94) {
		t.Errorf("Position = %v, want about {65.75 220.84 19.94}", d.Position)
	}

	// Everything not moving is the midpoint of its range, which is
	// what 0x7fff means.
	if d.Velocity.X > 0.01 || d.Velocity.X < -0.01 {
		t.Errorf("Velocity.X = %v, want about zero", d.Velocity.X)
	}
}

func TestDecodeTerseTruncated(t *testing.T) {
	for n := range len(terseBoat) {
		if _, err := DecodeTerse(terseBoat[:n]); err == nil {
			t.Errorf("%d bytes decoded without complaint", n)
		}
	}
}
