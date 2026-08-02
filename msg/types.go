// Package msg implements the Second Life UDP message layer: the wire
// types, a generic encoder and decoder driven by struct tags, and the
// message structures generated from message_template.msg.
//
// Numbers on the wire are little endian, with three exceptions that are
// network order: the packet sequence number, IPADDR and IPPORT.
package msg

//go:generate go run ../cmd/msggen -template ../message_template.msg -out messages_gen.go

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
)

// UUID is an LLUUID.  Sixteen bytes, sent verbatim.
type UUID [16]byte

// Zero is the nil UUID.
var Zero UUID

func (u UUID) IsZero() bool { return u == Zero }

func (u UUID) String() string {
	var b [36]byte
	hex.Encode(b[0:8], u[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], u[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], u[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], u[8:10])
	b[23] = '-'
	hex.Encode(b[24:36], u[10:16])
	return string(b[:])
}

// ParseUUID accepts the usual 8-4-4-4-12 form.
func ParseUUID(s string) (UUID, error) {
	var u UUID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return u, fmt.Errorf("msg: %q is not a UUID", s)
	}
	stripped := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	if _, err := hex.Decode(u[:], []byte(stripped)); err != nil {
		return UUID{}, fmt.Errorf("msg: %q is not a UUID: %w", s, err)
	}
	return u, nil
}

// MustParseUUID is ParseUUID for constants and tests.
func MustParseUUID(s string) UUID {
	u, err := ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

// Vector3 is an LLVector3: three little endian float32.
type Vector3 struct{ X, Y, Z float32 }

// Vector3d is an LLVector3d: three little endian float64.
type Vector3d struct{ X, Y, Z float64 }

// Vector4 is an LLVector4: four little endian float32.
type Vector4 struct{ X, Y, Z, W float32 }

// Quaternion is an LLQuaternion in its wire form.  Only X, Y and Z
// travel -- twelve bytes, not sixteen -- because the quaternion is a
// unit and W can be recovered from the other three.  From
// LLTemplateMessageBuilder::addQuat:
//
//	addData(varname, quat.packToVector3().mV, MVT_LLQuaternion, sizeof(LLVector3))
//
// The zero value is the identity rotation, since W() of {0,0,0} is 1.
// Use PackQuaternion and W to convert to and from a full quaternion.
type Quaternion struct{ X, Y, Z float32 }

// Linden Lab's FP_MAG_THRESHOLD: below this a quaternion is treated as
// degenerate and left unscaled.
const fpMagThreshold = 1e-7

// PackQuaternion reduces a full quaternion to the three components that
// travel, following LLQuaternion::packToVector3.  The quaternion is
// normalized, and if W is negative the vector part is negated instead
// -- q and -q are the same rotation, so this costs nothing and lets the
// receiver assume W is non-negative.
func PackQuaternion(x, y, z, w float32) Quaternion {
	mag := float32(math.Sqrt(float64(x*x + y*y + z*z + w*w)))
	if mag > fpMagThreshold {
		x /= mag
		y /= mag
		z /= mag
		// w is deliberately not scaled: it is not sent, and
		// only its sign is used below.
	}
	if w >= 0 {
		return Quaternion{x, y, z}
	}
	return Quaternion{-x, -y, -z}
}

// W recovers the fourth component, following
// LLQuaternion::unpackFromVector3.  It is never negative: PackQuaternion
// arranges the sign so it does not need to be.
func (q Quaternion) W() float32 {
	t := 1 - (q.X*q.X + q.Y*q.Y + q.Z*q.Z)
	if t <= 0 {
		return 0
	}
	return float32(math.Sqrt(float64(t)))
}

// IPAddr is an IPADDR: four bytes in network order, held here exactly
// as they appear on the wire so there is no byte order to get wrong.
type IPAddr [4]byte

func (a IPAddr) String() string { return fmt.Sprintf("%d.%d.%d.%d", a[0], a[1], a[2], a[3]) }

// IPPort is an IPPORT.  Two bytes, network order.
type IPPort uint16

// Freq is a message's priority class, which fixes how its number is
// framed on the wire.
type Freq uint8

const (
	FreqHigh   Freq = iota // one byte
	FreqMedium             // 0xFF, one byte
	FreqLow                // 0xFF 0xFF, two bytes big endian
	FreqFixed              // 0xFF 0xFF 0xFF, one byte
)

func (f Freq) String() string {
	switch f {
	case FreqHigh:
		return "High"
	case FreqMedium:
		return "Medium"
	case FreqLow:
		return "Low"
	case FreqFixed:
		return "Fixed"
	}
	return fmt.Sprintf("Freq(%d)", uint8(f))
}

// ID identifies a message independently of its framing.  It is not the
// wire encoding; use AppendID and DecodeID for that.
type ID uint32

// MakeID combines a priority class and a number.  For Fixed messages
// only the low byte of the template number is significant.
func MakeID(f Freq, number uint32) ID {
	if f == FreqFixed {
		number &= 0xff
	}
	return ID(uint32(f)<<24 | number&0x00ffffff)
}

func (id ID) Freq() Freq     { return Freq(id >> 24) }
func (id ID) Number() uint32 { return uint32(id) & 0x00ffffff }

func (id ID) String() string {
	if info := Lookup(id); info != nil {
		return info.Name
	}
	return fmt.Sprintf("%s(%d)", id.Freq(), id.Number())
}

// Info is the template metadata for one message.
type Info struct {
	Name string
	ID   ID

	// Trusted messages are only accepted between simulators.
	Trusted bool

	// Zerocoded messages are usually sent with runs of zeros
	// collapsed.  It is a hint, not a guarantee: the flag in the
	// packet header is what decides.
	Zerocoded bool

	// Deprecation carries UDPDeprecated, UDPBlackListed or
	// Deprecated from the template, and is empty otherwise.
	Deprecation string
}

// Message is implemented by every generated message struct.
type Message interface {
	MsgInfo() *Info
	Encode() ([]byte, error)
	Decode([]byte) error
}

// ErrShort is reported when a message ends before its fields do.
var ErrShort = errors.New("msg: truncated message")
