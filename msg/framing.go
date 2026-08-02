package msg

import (
	"encoding/binary"
	"fmt"
)

// Packet header flags.
const (
	FlagZerocoded = 0x80
	FlagReliable  = 0x40
	FlagResent    = 0x20
	FlagAck       = 0x10
)

// HeaderSize is the fixed part of a packet header: flags, a four byte
// sequence number and the extra-header length.
const HeaderSize = 6

// Header is the packet header that precedes a message.
type Header struct {
	Flags    uint8
	Sequence uint32 // network order on the wire, unlike everything else
	Extra    []byte
}

func (h *Header) Zerocoded() bool { return h.Flags&FlagZerocoded != 0 }
func (h *Header) Reliable() bool  { return h.Flags&FlagReliable != 0 }
func (h *Header) HasAcks() bool   { return h.Flags&FlagAck != 0 }

// AppendHeader writes h onto dst.
func AppendHeader(dst []byte, h *Header) []byte {
	dst = append(dst, h.Flags)
	dst = binary.BigEndian.AppendUint32(dst, h.Sequence)
	dst = append(dst, uint8(len(h.Extra)))
	return append(dst, h.Extra...)
}

// DecodeHeader reads a packet header, returning it and the number of
// bytes it occupied.
func DecodeHeader(b []byte) (Header, int, error) {
	if len(b) < HeaderSize {
		return Header{}, 0, ErrShort
	}
	h := Header{
		Flags:    b[0],
		Sequence: binary.BigEndian.Uint32(b[1:5]),
	}
	n := int(b[5])
	if len(b) < HeaderSize+n {
		return Header{}, 0, ErrShort
	}
	if n > 0 {
		h.Extra = b[HeaderSize : HeaderSize+n]
	}
	return h, HeaderSize + n, nil
}

// SplitAcks removes the acknowledgements a sender appended to the tail
// of a packet, returning the remaining body and the sequence numbers.
//
// The tail is a count byte preceded by that many big endian sequence
// numbers.  This has to happen before zero expansion: the acks are
// appended after the body is encoded and are never themselves coded.
func SplitAcks(body []byte) ([]byte, []uint32, error) {
	if len(body) < 1 {
		return nil, nil, ErrShort
	}
	n := int(body[len(body)-1])
	end := len(body) - 1
	if end < n*4 {
		return nil, nil, fmt.Errorf("msg: %d acks do not fit in %d bytes", n, end)
	}
	acks := make([]uint32, 0, n)
	for i := 0; i < n; i++ {
		off := end - 4*(i+1)
		acks = append(acks, binary.BigEndian.Uint32(body[off:off+4]))
	}
	return body[:end-4*n], acks, nil
}

// ZeroExpand undoes the zero coding of a packet body, appending to dst.
//
// A zero byte is followed by a count.  A count of zero means 256 zeros
// and that another count byte follows, so a run is 256*k + c where k is
// the number of zero count bytes and c the first non-zero one.
func ZeroExpand(dst, src []byte) ([]byte, error) {
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c != 0 {
			dst = append(dst, c)
			continue
		}
		run := 0
		for {
			i++
			if i >= len(src) {
				return nil, fmt.Errorf("msg: zero coded body ends in a run")
			}
			n := src[i]
			if n != 0 {
				run += int(n)
				break
			}
			run += 256
		}
		for ; run > 0; run-- {
			dst = append(dst, 0)
		}
	}
	return dst, nil
}

// ZeroCollapse applies zero coding, appending to dst.
//
// Runs are emitted at most 255 at a time.  That is the conservative
// subset of what ZeroExpand accepts and is what every decoder handles,
// including the one in the C client.
func ZeroCollapse(dst, src []byte) []byte {
	for i := 0; i < len(src); {
		if src[i] != 0 {
			dst = append(dst, src[i])
			i++
			continue
		}
		run := 0
		for i < len(src) && src[i] == 0 {
			run++
			i++
		}
		for run > 0 {
			n := run
			if n > 255 {
				n = 255
			}
			dst = append(dst, 0, uint8(n))
			run -= n
		}
	}
	return dst
}

// AppendID writes a message number in its framed form:
//
//	High    nn
//	Medium  FF nn
//	Low     FF FF hi lo
//	Fixed   FF FF FF nn
func AppendID(dst []byte, id ID) []byte {
	n := id.Number()
	switch id.Freq() {
	case FreqHigh:
		return append(dst, uint8(n))
	case FreqMedium:
		return append(dst, 0xff, uint8(n))
	case FreqLow:
		return append(dst, 0xff, 0xff, uint8(n>>8), uint8(n))
	default:
		return append(dst, 0xff, 0xff, 0xff, uint8(n))
	}
}

// DecodeID reads a framed message number and reports how many bytes it
// used.
func DecodeID(b []byte) (ID, int, error) {
	if len(b) < 1 {
		return 0, 0, ErrShort
	}
	if b[0] != 0xff {
		return MakeID(FreqHigh, uint32(b[0])), 1, nil
	}
	if len(b) < 2 {
		return 0, 0, ErrShort
	}
	if b[1] != 0xff {
		return MakeID(FreqMedium, uint32(b[1])), 2, nil
	}
	if len(b) < 4 {
		return 0, 0, ErrShort
	}
	if b[2] != 0xff {
		return MakeID(FreqLow, uint32(b[2])<<8|uint32(b[3])), 4, nil
	}
	return MakeID(FreqFixed, uint32(b[3])), 4, nil
}

// The registry is filled by the generated file.
var (
	infoByID   = map[ID]*Info{}
	newByID    = map[ID]func() Message{}
	infoByName = map[string]*Info{}
)

func register(info *Info, mk func() Message) {
	if _, dup := infoByID[info.ID]; dup {
		panic(fmt.Sprintf("msg: duplicate message id %v (%s)", info.ID, info.Name))
	}
	infoByID[info.ID] = info
	newByID[info.ID] = mk
	infoByName[info.Name] = info
}

// Lookup returns the template metadata for a message number, or nil.
func Lookup(id ID) *Info { return infoByID[id] }

// LookupName returns the template metadata for a message name, or nil.
func LookupName(name string) *Info { return infoByName[name] }

// IDOf is a shorthand for m.MsgInfo().ID.
func IDOf(m Message) ID { return m.MsgInfo().ID }

// New allocates a zero value of the message with this number.  It
// returns nil for a number that is not in the template.
func New(id ID) Message {
	if mk := newByID[id]; mk != nil {
		return mk()
	}
	return nil
}

// Names returns every message name in the template, for tests and
// tooling.
func Names() []string {
	out := make([]string, 0, len(infoByID))
	for _, i := range infoByID {
		out = append(out, i.Name)
	}
	return out
}

// DecodeBody reads a framed message number from b and decodes the rest
// of b into a freshly allocated message.
func DecodeBody(b []byte) (Message, error) {
	id, n, err := DecodeID(b)
	if err != nil {
		return nil, err
	}
	m := New(id)
	if m == nil {
		return nil, fmt.Errorf("msg: unknown message %v", id)
	}
	if err := m.Decode(b[n:]); err != nil {
		return nil, err
	}
	return m, nil
}
