package auth

import (
	"encoding/binary"
	"strings"
	"unicode/utf8"
)

// NameSize is the most a client's name may be, in bytes.
//
// The name is a program's -- slsh, slbotd, slbench -- and is sent
// before the caller has proved anything, so it is carried in four
// fixed64 fields rather than a string; see LoginRequest.client_0 in
// slgo.proto for why.  Thirty-two bytes is four of them, and far more
// than any program here is called.
const NameSize = 32

// PackName lays a name out as the four words LoginRequest carries:
// its bytes in order, first byte most significant, padded with zeros.
//
// A longer name is cut short rather than refused.  The client has no
// say in what its binary is called -- it is os.Args[0] -- and a label
// cut short still says which program it was, where a refusal would
// stop it logging in over a detail nobody would have noticed.  The cut
// is at a character boundary, so that what is left is still UTF-8.
func PackName(name string) [4]uint64 {
	var buf [NameSize]byte
	copy(buf[:], clipName(name))
	var out [4]uint64
	for i := range out {
		out[i] = binary.BigEndian.Uint64(buf[i*8:])
	}
	return out
}

// UnpackName reads back what PackName laid out.
//
// It ends at the first zero byte, since that is where the padding
// starts, and anything that is not UTF-8 -- which no client here sends,
// but anybody can -- is replaced rather than passed on, because the
// name ends up in sentences a person reads.  A replacement is longer
// than the byte it replaces, so the result is clipped again: what comes
// out is never longer than NameSize either.
func UnpackName(words [4]uint64) string {
	var buf [NameSize]byte
	for i, w := range words {
		binary.BigEndian.PutUint64(buf[i*8:], w)
	}
	b := buf[:]
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return clipName(strings.ToValidUTF8(string(b), "\uFFFD"))
}

// clipName cuts a name to NameSize bytes without splitting a character.
func clipName(name string) string {
	if len(name) <= NameSize {
		return name
	}
	n := NameSize
	for n > 0 && !utf8.RuneStart(name[n]) {
		n--
	}
	return name[:n]
}
