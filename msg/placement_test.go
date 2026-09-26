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

	// Everything not moving is sent as 0x7fff, half a step below the
	// midpoint of its range, and read as the viewer reads it: exactly
	// zero, since it is within a step of zero.
	for name, v := range map[string]Vector3{
		"velocity": d.Velocity, "acceleration": d.Acceleration, "angular velocity": d.AngularVel,
	} {
		if v != (Vector3{}) {
			t.Errorf("%s = %v, want zero", name, v)
		}
	}
}

// TestATerseUpdateReadsItsRangesAsTheViewerDoes: velocity is a fraction
// of plus or minus 128, and acceleration and angular velocity of plus or
// minus 64 (llviewerobject.cpp:1755-1778).  Reading all three over 128
// doubled every acceleration and every spin.
func TestATerseUpdateReadsItsRangesAsTheViewerDoes(t *testing.T) {
	w := &blobWriter{}
	w.u32(7)
	w.u8(0)
	w.u8(0)
	w.f32(10)
	w.f32(20)
	w.f32(30)
	vec := func(x, y, z uint16) { w.u16(x); w.u16(y); w.u16(z) }
	vec(65535, 0, 32767)     // velocity
	vec(65535, 0, 32767)     // acceleration
	vec(32767, 32767, 32767) // rotation X, Y, Z
	w.u16(65535)             // and W
	vec(65535, 0, 32767)     // angular velocity

	d, err := DecodeTerse(w.b)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Vector3{128, -128, 0}); d.Velocity != want {
		t.Errorf("velocity = %v, want %v", d.Velocity, want)
	}
	if want := (Vector3{64, -64, 0}); d.Acceleration != want {
		t.Errorf("acceleration = %v, want %v", d.Acceleration, want)
	}
	if want := (Vector3{64, -64, 0}); d.AngularVel != want {
		t.Errorf("angular velocity = %v, want %v", d.AngularVel, want)
	}
}

// TestASixteenBitFractionNearZeroIsZero: the viewer's U16_to_F32
// (llquantize.h:72-85) reads anything within one step of zero as zero,
// and a step away from it as what it is.
func TestASixteenBitFractionNearZeroIsZero(t *testing.T) {
	step := float32(256) / 65535
	for _, c := range []struct {
		raw  uint16
		want float32
	}{
		{32767, 0}, // half a step below
		{32768, 0}, // half a step above
		{32769, -128 + 256*float32(32769)/65535},
		{32766, -128 + 256*float32(32766)/65535},
	} {
		got := u16f([]byte{byte(c.raw), byte(c.raw >> 8)}, -128, 128)
		if c.want == 0 && got != 0 {
			t.Errorf("%d over plus or minus 128 = %v, want exactly zero", c.raw, got)
		}
		if c.want != 0 && (got-c.want > 1e-4 || c.want-got > 1e-4 || (got > -step && got < step)) {
			t.Errorf("%d over plus or minus 128 = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestDecodeTerseTruncated(t *testing.T) {
	for n := range len(terseBoat) {
		if _, err := DecodeTerse(terseBoat[:n]); err == nil {
			t.Errorf("%d bytes decoded without complaint", n)
		}
	}
}

// The ObjectUpdate placement blob comes in six widths and the width
// is the only thing that says which layout it is.  Sixty bytes are
// plain floats, and 124 the same with more after them; thirty-two are
// sixteen bit fractions of ranges, so the
// same blob read as the wrong width gives a position that is in range
// and stable and wrong -- exactly the failure the terse decoder above
// was written against.

// placeFloats lays out the sixty byte form: position, velocity,
// acceleration, rotation and angular velocity, twelve bytes each.
func placeFloats(pos Vector3, rot Quaternion) []byte {
	w := &blobWriter{}
	w.vec(pos)
	w.vec(Vector3{}) // velocity
	w.vec(Vector3{}) // acceleration
	w.f32(rot.X)     // rotation, three floats
	w.f32(rot.Y)
	w.f32(rot.Z)
	w.vec(Vector3{}) // angular velocity
	return w.b
}

// quant16 is the sixteen bit quantisation the narrow forms use: a
// fraction of the span between lo and hi.
func quant16(v, lo, hi float32) uint16 {
	return uint16((v - lo) / (hi - lo) * 65535)
}

// The viewer's ranges for the thirty-two byte form's position
// (llviewerobject.cpp:1649-1651), written out here rather than taken
// from the constants that read them: X and Y from half a region below
// to half a region above, Z from a region's width below the ground to
// the highest an object may be on Second Life.
const (
	placeXYLo, placeXYHi = -128, 384
	placeZLo, placeZHi   = -256, 4096
)

// place16 lays out the thirty-two byte form: position, velocity and
// acceleration six bytes each, rotation eight, angular velocity six.
// The rotation is all four components, as they were given.
func place16(pos Vector3, qx, qy, qz, qw float32) []byte {
	w := &blobWriter{}
	w.u16(quant16(pos.X, placeXYLo, placeXYHi))
	w.u16(quant16(pos.Y, placeXYLo, placeXYHi))
	w.u16(quant16(pos.Z, placeZLo, placeZHi))
	w.raw(make([]byte, 12)) // velocity and acceleration
	w.u16(quant16(qx, -1, 1))
	w.u16(quant16(qy, -1, 1))
	w.u16(quant16(qz, -1, 1))
	w.u16(quant16(qw, -1, 1))
	w.raw(make([]byte, 6)) // angular velocity
	return w.b
}

func TestDecodePlacement(t *testing.T) {
	pos := Vector3{128.5, 64.25, 25.75}
	rot := Quaternion{0.5, -0.25, 0.125}
	q16 := place16(pos, rot.X, rot.Y, rot.Z, rot.W())

	// An avatar's blob is sixteen bytes longer because it begins with
	// a collision plane, and that is all that distinguishes it.
	plane := make([]byte, 16)

	cases := []struct {
		what string
		b    []byte
		// How far a position and a rotation may be out.  The sixteen
		// bit form's coarsest axis is Z, one part in 65535 of 4352
		// metres, which is a little under seven centimetres.
		pos, rot float32
	}{
		{"floats", placeFloats(pos, rot), 0.001, 0.00001},
		{"an avatar's floats", append(append([]byte(nil), plane...), placeFloats(pos, rot)...), 0.001, 0.00001},
		{"sixteen bit", q16, 0.07, 0.0001},
		{"an avatar's sixteen bit", append(append([]byte(nil), plane...), q16...), 0.07, 0.0001},
	}
	for _, c := range cases {
		p, q, ok := DecodePlacement(c.b)
		if !ok {
			t.Errorf("%s: %d bytes were not recognised", c.what, len(c.b))
			continue
		}
		near := func(name string, got, want, tol float32) {
			if got-want > tol || want-got > tol {
				t.Errorf("%s: %s = %v, want %v within %v", c.what, name, got, want, tol)
			}
		}
		near("position X", p.X, pos.X, c.pos)
		near("position Y", p.Y, pos.Y, c.pos)
		near("position Z", p.Z, pos.Z, c.pos)
		near("rotation X", q.X, rot.X, c.rot)
		near("rotation Y", q.Y, rot.Y, c.rot)
		near("rotation Z", q.Z, rot.Z, c.rot)
	}
}

// TestAWiderFloatBlobIsReadForItsFirstSixtyBytes: 124 bytes are the
// sixty byte form with room for more after it, and 140 the avatar's
// seventy-six, and the viewer reads the part it knows and ignores the
// rest (llviewerobject.cpp:1215-1219, 1394-1402).
func TestAWiderFloatBlobIsReadForItsFirstSixtyBytes(t *testing.T) {
	pos := Vector3{200.5, 17.25, 3012.75}
	rot := Quaternion{-0.125, 0.5, 0.25}
	// Something in the part that is not read, so that reading it
	// would show.
	more := make([]byte, 64)
	for i := range more {
		more[i] = 0x41
	}
	plane := make([]byte, 16)

	for _, c := range []struct {
		what string
		b    []byte
	}{
		{"124 bytes", append(placeFloats(pos, rot), more...)},
		{"140 bytes", append(append(append([]byte(nil), plane...), placeFloats(pos, rot)...), more...)},
	} {
		p, q, ok := DecodePlacement(c.b)
		if !ok {
			t.Errorf("%s were not recognised", c.what)
			continue
		}
		if p != pos || q != rot {
			t.Errorf("%s decoded to %v %v, want %v %v", c.what, p, q, pos, rot)
		}
	}
}

// TestASixteenBitRotationSentNegatedIsTheSameRotation: the thirty-two
// byte form carries all four components of the rotation, as the terse
// form does, and the same holds of it: q and -q are one rotation, and
// only W says which was sent.  Reading three and recovering W as
// positive turns a -q into the mirror image of q.
func TestASixteenBitRotationSentNegatedIsTheSameRotation(t *testing.T) {
	// A quarter turn anticlockwise about the vertical, (0, 0, sin 45,
	// cos 45), sent as its negative.
	s := float32(0.70710677)
	b := place16(Vector3{128, 128, 25}, 0, 0, -s, -s)
	for _, c := range []struct {
		what string
		b    []byte
	}{
		{"a prim", b},
		{"an avatar", append(make([]byte, 16), b...)},
	} {
		_, q, ok := DecodePlacement(c.b)
		if !ok {
			t.Fatalf("%s: %d bytes were not recognised", c.what, len(c.b))
		}
		near := func(got, want float32) bool { return got > want-0.001 && got < want+0.001 }
		if !near(q.Z, s) || !near(q.W(), s) || !near(q.X, 0) || !near(q.Y, 0) {
			t.Errorf("%s: rotation = %+v with W %v, want Z and W both about %v", c.what, q, q.W(), s)
		}
	}
}

// TestASixteenBitHeightRunsTo4096Metres: the thirty-two byte
// form's Z runs from a region's width below the ground to 4096 metres,
// the viewer's range (llviewerobject.cpp:1651), and not over the span X
// and Y use.  Over theirs, anything above 384 metres could not be said.
func TestASixteenBitHeightRunsTo4096Metres(t *testing.T) {
	quantum := float32(placeZHi-placeZLo) / 65535
	for _, z := range []float32{4000, 25.75, -200} {
		p, _, ok := DecodePlacement(place16(Vector3{128, 128, z}, 0, 0, 0, 1))
		if !ok {
			t.Fatal("thirty-two bytes were not recognised")
		}
		if p.Z-z > quantum || z-p.Z > quantum {
			t.Errorf("Z %v decoded as %v, want within %v", z, p.Z, quantum)
		}
	}
}

// TestASixteenByteBlobIsNotRead: there is no sixteen byte form.  The
// viewer reads no such width, so a layout for one would be a guess,
// and a guess puts the object somewhere plausible and wrong.
func TestASixteenByteBlobIsNotRead(t *testing.T) {
	b := []byte{0x80, 0x80, 0x10, 0, 0, 0, 0, 0, 0, 0x80, 0x80, 0xc0, 0, 0, 0, 0}
	if p, q, ok := DecodePlacement(b); ok {
		t.Errorf("sixteen bytes decoded to %v %v", p, q)
	}
}

// TestDecodePlacementUnknownWidth: a length that is none of the six is
// a layout this build does not know, and guessing at one would put the
// object somewhere plausible and wrong.
func TestDecodePlacementUnknownWidth(t *testing.T) {
	for _, n := range []int{0, 1, 15, 16, 17, 31, 33, 44, 59, 61, 77, 123, 125, 139, 141} {
		if p, q, ok := DecodePlacement(make([]byte, n)); ok {
			t.Errorf("%d bytes decoded to %v %v", n, p, q)
		}
	}
}

// TestDecodeTerseAvatar: an avatar's terse update carries a collision
// plane between the header and the position, and reading a prim's
// layout out of one puts the avatar sixteen bytes' worth of somewhere
// else.
func TestDecodeTerseAvatar(t *testing.T) {
	w := &blobWriter{}
	w.u32(12345) // local id
	w.u8(0)      // state
	w.u8(1)      // an avatar, so a collision plane follows
	w.f32(0)     // collision plane, four floats
	w.f32(0)
	w.f32(1)
	w.f32(-21.5)
	w.f32(128.5) // position
	w.f32(64.25)
	w.f32(21.5)
	w.raw(make([]byte, 26)) // velocity, acceleration, rotation, angular

	d, err := DecodeTerse(w.b)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.b) != 60 {
		t.Fatalf("an avatar's terse update is 60 bytes, this one is %d", len(w.b))
	}
	if !d.Avatar {
		t.Fatal("read as a prim")
	}
	if d.CollisionPlane != [4]float32{0, 0, 1, -21.5} {
		t.Errorf("collision plane = %v", d.CollisionPlane)
	}
	if d.Position != (Vector3{128.5, 64.25, 21.5}) {
		t.Errorf("position = %v, want {128.5 64.25 21.5}", d.Position)
	}
}

// TestDecodeTerseAvatarTruncated: the collision plane is the one part
// whose absence has to be noticed separately, because the length check
// for everything after it is made from an offset the plane moved.
func TestDecodeTerseAvatarTruncated(t *testing.T) {
	w := &blobWriter{}
	w.u32(12345)
	w.u8(0)
	w.u8(1)
	w.raw(make([]byte, 8)) // half a collision plane

	if d, err := DecodeTerse(w.b); err == nil {
		t.Errorf("decoded to %+v", d)
	}
}

// TestATerseRotationSentNegatedIsTheSameRotation: q and -q are one
// rotation, the simulator sends either, and only the fourth component
// says which it sent.  Measured on Agni, 2026-09-24: an avatar walking
// north read as facing south on some walks, because W was thrown away
// and recovered as positive, which for a -q is the mirror image.
func TestATerseRotationSentNegatedIsTheSameRotation(t *testing.T) {
	// A quarter turn anticlockwise about the vertical, facing north:
	// (0, 0, sin 45, cos 45), sent as its negative.
	s := float32(0.70710677)
	w := &blobWriter{}
	w.u32(7)
	w.u8(0)
	w.u8(0)
	w.f32(10)
	w.f32(20)
	w.f32(30)
	for range 6 {
		w.u16(32767)
	}
	w.u16(quant16(0, -1, 1))
	w.u16(quant16(0, -1, 1))
	w.u16(quant16(-s, -1, 1))
	w.u16(quant16(-s, -1, 1))
	for range 3 {
		w.u16(32767)
	}

	d, err := DecodeTerse(w.b)
	if err != nil {
		t.Fatal(err)
	}
	near := func(got, want float32) bool { return got > want-0.001 && got < want+0.001 }
	if !near(d.Rotation.Z, s) || !near(d.Rotation.W(), s) || !near(d.Rotation.X, 0) || !near(d.Rotation.Y, 0) {
		t.Errorf("rotation = %+v with W %v, want Z and W both about %v", d.Rotation, d.Rotation.W(), s)
	}
}
